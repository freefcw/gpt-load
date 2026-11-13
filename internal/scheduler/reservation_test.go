package scheduler

import (
	"errors"
	"sync"
	"testing"
	"time"

	"gpt-load/internal/ratelimit"
	"gpt-load/internal/state"
)

type observedCredentialSource struct {
	CredentialSource
	collected chan struct{}
}

func (source observedCredentialSource) CollectCredentialCandidates(groups []uint, excluded func(uint) bool, now time.Time) []state.CredentialMeta {
	candidates := source.CredentialSource.CollectCredentialCandidates(groups, excluded, now)
	close(source.collected)
	return candidates
}

func assertReservations(t *testing.T, registry *state.CredentialRegistry, want map[uint]int64) {
	t.Helper()
	registry.SchedulingState().WithLock(func(ledger *state.SchedulingLedger) {
		if len(ledger.Reservations) != len(want) {
			t.Fatalf("pending = %v, want %v", ledger.Reservations, want)
		}
		for id, count := range want {
			if ledger.Reservations[id] != count {
				t.Fatalf("pending = %v, want %v", ledger.Reservations, want)
			}
		}
	})
}

func TestConcurrentSelectionsReserveLoadBeforePreparation(t *testing.T) {
	r := newFairnessRegistry(t)
	snapshot := schedulerSnapshot()
	limiter := ratelimit.NewCredentialLimiter()
	hold, ok := limiter.Acquire(11, 0, 0)
	if !ok {
		t.Fatal("initial acquire rejected")
	}
	defer hold()
	query := fairnessQuery(0)
	query.Limiter = limiter
	const requests = 64
	type pending struct {
		iterator  *Iterator
		selection Selection
	}
	prepared := make(chan pending, requests)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range requests {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			iterator := New(snapshot, r, query)
			selection, err := iterator.Next()
			if err != nil {
				iterator.Close()
				t.Error(err)
				return
			}
			// All requests finish selection before any request can acquire.
			prepared <- pending{iterator, selection}
		}()
	}
	close(start)
	workers.Wait()
	close(prepared)
	counts := map[uint]int64{}
	var batch []pending
	for request := range prepared {
		defer request.iterator.Close()
		batch = append(batch, request)
		counts[request.selection.CredentialID]++
	}
	if len(batch) != requests {
		t.Fatalf("selected %d requests, want %d", len(batch), requests)
	}
	// Include the pre-existing request in total load. The old implementation
	// selected credential 12 for all 64 requests and ended at 1:64.
	if difference := counts[11] + 1 - counts[12]; difference < -1 || difference > 1 {
		t.Fatalf("unbalanced pending batch: %v plus one active on 11", counts)
	}
	assertReservations(t, r, counts)
	if rpm, flight := limiter.Usage(12); rpm != 0 || flight != 0 {
		t.Fatalf("preparation charged real usage: %d/%d", rpm, flight)
	}
	var releases []func()
	for _, request := range batch {
		release, ok := request.iterator.AcquireCredential(func() (func(), bool) {
			return limiter.Acquire(request.selection.CredentialID, 0, 0)
		})
		if !ok {
			t.Fatal("unlimited acquire rejected")
		}
		releases = append(releases, release)
		request.iterator.Close()
	}
	assertReservations(t, r, nil)
	for _, id := range []uint{11, 12} {
		want := counts[id]
		if id == 11 {
			want++
		}
		if rpm, flight := limiter.Usage(id); rpm != want || flight != want {
			t.Fatalf("credential %d: %d/%d, want %d/%d", id, rpm, flight, want, want)
		}
	}
	for _, release := range releases {
		release()
	}
}

func TestReservationAbandonedOrRetriedDoesNotChargeRPM(t *testing.T) {
	r := newFairnessRegistry(t)
	limiter := ratelimit.NewCredentialLimiter()
	query := fairnessQuery(0)
	query.Limiter = limiter
	iterator := New(schedulerSnapshot(), r, query)
	defer iterator.Close()
	first, err := iterator.Next()
	if err != nil {
		t.Fatal(err)
	}
	assertReservations(t, r, map[uint]int64{first.CredentialID: 1})
	second, err := iterator.Next()
	if err != nil {
		t.Fatal(err)
	}
	if first.CredentialID == second.CredentialID {
		t.Fatal("retry selected the already-tried credential")
	}
	assertReservations(t, r, map[uint]int64{second.CredentialID: 1})
	if _, err := iterator.Next(); !errors.Is(err, ErrExhausted) {
		t.Fatalf("Next() = %v, want exhausted", err)
	}
	assertReservations(t, r, nil)
	iterator.Close()
	iterator.Close()
	for _, id := range []uint{11, 12} {
		if rpm, flight := limiter.Usage(id); rpm != 0 || flight != 0 {
			t.Fatalf("abandoned preparation charged %d/%d on %d", rpm, flight, id)
		}
	}
}

func TestReservationCommitKeepsSlotUntilExplicitRelease(t *testing.T) {
	r := newFairnessRegistry(t)
	limiter := ratelimit.NewCredentialLimiter()
	query := fairnessQuery(0)
	query.Limiter = limiter
	iterator := New(schedulerSnapshot(), r, query)
	defer iterator.Close()
	selection, err := iterator.Next()
	if err != nil {
		t.Fatal(err)
	}
	// Pending load is advisory, so it cannot block its own hard limit of one.
	release, ok := iterator.AcquireCredential(func() (func(), bool) {
		return limiter.Acquire(selection.CredentialID, 1, 1)
	})
	if !ok {
		t.Fatal("reservation blocked its own acquire")
	}
	defer release()
	assertReservations(t, r, nil)
	iterator.Close()
	if rpm, flight := limiter.Usage(selection.CredentialID); rpm != 1 || flight != 1 {
		t.Fatalf("Close released a transferred slot: %d/%d", rpm, flight)
	}
	release()
	release()
	if rpm, flight := limiter.Usage(selection.CredentialID); rpm != 1 || flight != 0 {
		t.Fatalf("usage after repeated release = %d/%d", rpm, flight)
	}
}

func TestReservationCommitCannotBeObservedAsDoubleLoad(t *testing.T) {
	r := newFairnessRegistry(t)
	snapshot := schedulerSnapshot()
	limiter := ratelimit.NewCredentialLimiter()
	for range 2 {
		release, ok := limiter.Acquire(12, 0, 0)
		if !ok {
			t.Fatal("initial acquire rejected")
		}
		defer release()
	}
	query := fairnessQuery(0)
	query.Limiter = limiter
	first := New(snapshot, r, query)
	defer first.Close()
	selection, err := first.Next()
	if err != nil || selection.CredentialID != 11 {
		t.Fatalf("first selection = %+v, %v", selection, err)
	}
	source := observedCredentialSource{CredentialSource: r, collected: make(chan struct{})}
	second := New(snapshot, source, query)
	defer second.Close()
	charged, finish := make(chan struct{}), make(chan struct{})
	var finishOnce sync.Once
	finishCommit := func() { finishOnce.Do(func() { close(finish) }) }
	defer finishCommit()
	committed := make(chan func(), 1)
	go func() {
		release, _ := first.AcquireCredential(func() (func(), bool) {
			release, ok := limiter.Acquire(11, 0, 0)
			close(charged)
			<-finish // Real usage exists while the pending reservation still does.
			return release, ok
		})
		committed <- release
	}()
	<-charged
	selected := make(chan Selection, 1)
	go func() {
		selection, err := second.Next()
		if err != nil {
			t.Error(err)
		}
		selected <- selection
	}()
	<-source.collected
	select {
	case selection := <-selected:
		finishCommit()
		(<-committed)()
		t.Fatalf("selection observed an unfinished reservation transfer: %+v", selection)
	case <-time.After(20 * time.Millisecond):
	}
	finishCommit()
	release := <-committed
	defer release()
	// Correct load is 11:1, 12:2. Counting pending + actual twice would make
	// loads equal and send this selection to 12 via the progress tiebreak.
	if selection := <-selected; selection.CredentialID != 11 {
		t.Fatalf("selection after commit = %d, want 11", selection.CredentialID)
	}
}

func TestReservationFailedAcquireCancelsPending(t *testing.T) {
	r := newFairnessRegistry(t)
	limiter := ratelimit.NewCredentialLimiter()
	query := fairnessQuery(0)
	query.Limiter = limiter
	iterator := New(schedulerSnapshot(), r, query)
	defer iterator.Close()
	selection, err := iterator.Next()
	if err != nil {
		t.Fatal(err)
	}
	hold, ok := limiter.Acquire(selection.CredentialID, 1, 1)
	if !ok {
		t.Fatal("initial acquire rejected")
	}
	defer hold()
	if _, ok := iterator.AcquireCredential(func() (func(), bool) {
		return limiter.Acquire(selection.CredentialID, 1, 1)
	}); ok {
		t.Fatal("acquire exceeded hard limit")
	}
	assertReservations(t, r, nil)
	if rpm, flight := limiter.Usage(selection.CredentialID); rpm != 1 || flight != 1 {
		t.Fatalf("failed acquire changed real usage: %d/%d", rpm, flight)
	}
}

func TestReplayReplacesAndCancelsReservation(t *testing.T) {
	r := newFairnessRegistry(t)
	limiter := ratelimit.NewCredentialLimiter()
	query := fairnessQuery(0)
	query.Limiter = limiter
	iterator := New(schedulerSnapshot(), r, query)
	defer iterator.Close()
	selection, err := iterator.Next()
	if err != nil {
		t.Fatal(err)
	}
	ref, ok := r.CredentialRef(selection.CredentialID)
	if !ok || !iterator.ChargeReplay(selection, ref) {
		t.Fatal("replay rejected")
	}
	assertReservations(t, r, map[uint]int64{selection.CredentialID: 1})
	ref.IdentityGeneration++
	if iterator.ChargeReplay(selection, ref) {
		t.Fatal("replay accepted stale identity")
	}
	assertReservations(t, r, nil)
	if rpm, flight := limiter.Usage(selection.CredentialID); rpm != 0 || flight != 0 {
		t.Fatalf("replay preparation charged usage: %d/%d", rpm, flight)
	}
}

func TestReservationsInfluenceSoftSaturationButNotHardLimits(t *testing.T) {
	r := newFairnessRegistry(t)
	snapshot := schedulerSnapshot()
	group := snapshot.Groups[1]
	group.CredentialConcurrencyLimit = 10
	snapshot.Groups[1] = group
	limiter := ratelimit.NewCredentialLimiter()
	query := fairnessQuery(11)
	query.Limiter = limiter
	for range 7 {
		iterator := New(snapshot, r, query)
		defer iterator.Close()
		selection, err := iterator.Next()
		if err != nil || selection.CredentialID != 11 {
			t.Fatalf("sticky selection = %+v, %v", selection, err)
		}
	}
	assertReservations(t, r, map[uint]int64{11: 7})
	if got := loadPick(t, snapshot, r, 0, limiter); got != 12 {
		t.Fatalf("non-sticky selection = %d, want 12", got)
	}
	if got := loadPick(t, snapshot, r, 11, limiter); got != 11 {
		t.Fatalf("sticky selection = %d, want 11", got)
	}
	if rpm, flight := limiter.Usage(11); rpm != 0 || flight != 0 {
		t.Fatalf("pending reservations charged real usage: %d/%d", rpm, flight)
	}
}

func TestReservationsAreNotRestoredFromSchedulingCheckpoint(t *testing.T) {
	r := newFairnessRegistry(t)
	query := fairnessQuery(0)
	query.Limiter = ratelimit.NewCredentialLimiter()
	iterator := New(schedulerSnapshot(), r, query)
	defer iterator.Close()
	selection, err := iterator.Next()
	if err != nil {
		t.Fatal(err)
	}
	assertReservations(t, r, map[uint]int64{selection.CredentialID: 1})
	checkpoint := r.SchedulingState().CaptureCheckpoint()
	restarted := newFairnessRegistry(t)
	restarted.SchedulingState().RestoreCheckpoint(checkpoint)
	assertReservations(t, restarted, nil)
}
