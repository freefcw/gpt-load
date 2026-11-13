package ratelimit

import (
	"sync"
	"testing"
	"time"
)

func TestCredentialLimiterEnforcesRPMAndConcurrencyTogether(t *testing.T) {
	base := time.Date(2026, time.September, 12, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: base}
	limiter := NewCredentialLimiter()
	limiter.now = clock.current

	first, ok := limiter.Acquire(7, 3, 1)
	if !ok {
		t.Fatal("first acquire rejected")
	}
	if _, ok := limiter.Acquire(7, 3, 1); ok {
		t.Fatal("second acquire allowed while one is in flight with concurrency 1")
	}
	first()
	second, ok := limiter.Acquire(7, 3, 1)
	if !ok {
		t.Fatal("acquire after release rejected")
	}
	second()
	third, ok := limiter.Acquire(7, 3, 1)
	if !ok {
		t.Fatal("third acquire rejected")
	}
	third()
	if _, ok := limiter.Acquire(7, 3, 1); ok {
		t.Fatal("fourth acquire allowed beyond rpm 3 even with no in-flight")
	}
	if got := limiter.RetryAfter(7, 3); !got.Equal(base.Add(time.Minute)) {
		t.Fatalf("RetryAfter = %v, want %v", got, base.Add(time.Minute))
	}
	clock.set(base.Add(time.Minute))
	if _, ok := limiter.Acquire(7, 3, 1); !ok {
		t.Fatal("acquire after window expiry rejected")
	}
}

func TestCredentialLimiterAvailableDoesNotCharge(t *testing.T) {
	limiter := NewCredentialLimiter()
	for range 5 {
		if !limiter.Available(7, 1, 1) {
			t.Fatal("Available charged the window")
		}
	}
	if _, ok := limiter.Acquire(7, 1, 1); !ok {
		t.Fatal("acquire after read-only checks rejected")
	}
	if limiter.Available(7, 1, 1) {
		t.Fatal("Available reports capacity while both limits are exhausted")
	}
}

func TestCredentialLimiterUsageReportsWindowAndInFlight(t *testing.T) {
	base := time.Date(2026, time.October, 25, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: base}
	limiter := NewCredentialLimiter()
	limiter.now = clock.current

	if rpmUsed, inFlight := limiter.Usage(7); rpmUsed != 0 || inFlight != 0 {
		t.Fatalf("idle usage = %d/%d, want 0/0", rpmUsed, inFlight)
	}
	release, ok := limiter.Acquire(7, 3, 2)
	if !ok {
		t.Fatal("acquire rejected")
	}
	secondRelease, ok := limiter.Acquire(7, 3, 2)
	if !ok {
		t.Fatal("second acquire rejected")
	}
	defer secondRelease()
	if rpmUsed, inFlight := limiter.Usage(7); rpmUsed != 2 || inFlight != 2 {
		t.Fatalf("active usage = %d/%d, want 2/2", rpmUsed, inFlight)
	}
	release()
	if rpmUsed, inFlight := limiter.Usage(7); rpmUsed != 2 || inFlight != 1 {
		t.Fatalf("usage after release = %d/%d, want 2/1", rpmUsed, inFlight)
	}
	if _, ok := limiter.Acquire(8, 0, 0); !ok {
		t.Fatal("unlimited acquire rejected")
	}
	if rpmUsed, inFlight := limiter.Usage(8); rpmUsed != 1 || inFlight != 1 {
		t.Fatalf("unlimited usage = %d/%d, want 1/1 (accounted even without limits)", rpmUsed, inFlight)
	}
	clock.set(base.Add(time.Minute))
	if rpmUsed, inFlight := limiter.Usage(7); rpmUsed != 0 || inFlight != 1 {
		t.Fatalf("expired window usage = %d/%d, want 0/1", rpmUsed, inFlight)
	}
	if len(limiter.windows) != 0 {
		t.Fatalf("expired window state = %d entries, want 0 (including credential 8 without limits)", len(limiter.windows))
	}
}

func TestCredentialLimiterActivityCleansUnusedWindows(t *testing.T) {
	tests := []struct {
		name     string
		activity func(*testing.T, *CredentialLimiter, uint)
	}{
		{"Acquire", func(t *testing.T, limiter *CredentialLimiter, id uint) {
			release, ok := limiter.Acquire(id, 1, 1)
			if !ok {
				t.Fatal("activity acquire rejected")
			}
			release()
		}},
		{"UnlimitedAcquire", func(t *testing.T, limiter *CredentialLimiter, id uint) {
			release, ok := limiter.Acquire(id, 0, 0)
			if !ok {
				t.Fatal("unlimited activity acquire rejected")
			}
			release()
		}},
		{"Usage", func(t *testing.T, limiter *CredentialLimiter, id uint) {
			if rpm, inFlight := limiter.Usage(id); rpm != 0 || inFlight != 0 {
				t.Fatalf("unused credential usage = %d/%d, want 0/0", rpm, inFlight)
			}
		}},
		{"Available", func(t *testing.T, limiter *CredentialLimiter, id uint) {
			if !limiter.Available(id, 1, 0) {
				t.Fatal("unused credential unavailable")
			}
		}},
		{"ConcurrencyOnlyAvailable", func(t *testing.T, limiter *CredentialLimiter, id uint) {
			if !limiter.Available(id, 0, 1) {
				t.Fatal("unused credential unavailable")
			}
		}},
		{"AvailableWithRetryAfter", func(t *testing.T, limiter *CredentialLimiter, id uint) {
			if ok, retry := limiter.AvailableWithRetryAfter(id, 1, 0); !ok || !retry.IsZero() {
				t.Fatalf("unused credential availability = %v/%v, want true/zero", ok, retry)
			}
		}},
		{"ConcurrencyOnlyAvailableWithRetryAfter", func(t *testing.T, limiter *CredentialLimiter, id uint) {
			if ok, retry := limiter.AvailableWithRetryAfter(id, 0, 1); !ok || !retry.IsZero() {
				t.Fatalf("unused credential availability = %v/%v, want true/zero", ok, retry)
			}
		}},
		{"RetryAfter", func(t *testing.T, limiter *CredentialLimiter, id uint) {
			if retry := limiter.RetryAfter(id, 1); !retry.IsZero() {
				t.Fatalf("unused credential RetryAfter = %v, want zero", retry)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := time.Date(2026, time.October, 25, 12, 0, 0, 0, time.UTC)
			clock := &fakeClock{now: base}
			limiter := NewCredentialLimiter()
			limiter.now = clock.current
			const unusedIDs = 256
			const activeID = unusedIDs + 1
			for id := uint(1); id <= activeID; id++ {
				// Include both limited and unlimited windows in the sweep.
				limit := int64(id % 2)
				release, ok := limiter.Acquire(id, limit, limit)
				if !ok {
					t.Fatalf("initial acquire for %d rejected", id)
				}
				release()
			}
			clock.set(base.Add(30 * time.Second))
			release, ok := limiter.Acquire(activeID, 2, 1)
			if !ok {
				t.Fatal("active credential acquire rejected")
			}
			release()

			clock.set(base.Add(time.Minute - time.Nanosecond))
			tt.activity(t, limiter, activeID+1)
			for id := uint(1); id <= unusedIDs; id++ {
				if limiter.windows[id].len() != 1 {
					t.Fatalf("window %d removed before expiry", id)
				}
			}
			clock.set(base.Add(time.Minute))
			tt.activity(t, limiter, activeID+2)
			for id := uint(1); id <= unusedIDs; id++ {
				if _, exists := limiter.windows[id]; exists {
					t.Fatalf("unused expired window %d retained", id)
				}
			}
			window := limiter.windows[activeID]
			if window.len() != 1 || !window.at(0).Equal(base.Add(30*time.Second)) {
				t.Fatalf("active window = %+v, want only the unexpired timestamp", window)
			}
		})
	}
}

func TestCredentialLimiterCleanupPreservesLongRunningRequests(t *testing.T) {
	base := time.Date(2026, time.October, 25, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: base}
	limiter := NewCredentialLimiter()
	limiter.now = clock.current
	first, ok := limiter.Acquire(7, 0, 0)
	if !ok {
		t.Fatal("first unlimited acquire rejected")
	}
	second, ok := limiter.Acquire(7, 0, 0)
	if !ok {
		t.Fatal("second unlimited acquire rejected")
	}
	clock.set(base.Add(2 * time.Minute))
	limiter.Usage(8) // Activity on another credential expires RPM, not live streams.
	if len(limiter.windows) != 0 {
		t.Fatal("expired RPM window retained")
	}
	if rpm, inFlight := limiter.Usage(7); rpm != 0 || inFlight != 2 {
		t.Fatalf("long-running usage = %d/%d, want 0/2", rpm, inFlight)
	}
	if _, ok := limiter.Acquire(7, 1, 2); ok {
		t.Fatal("cleanup freed occupied concurrency slots")
	}
	first()
	first()
	if got := limiter.InFlightForTest(7); got != 1 {
		t.Fatalf("in-flight after double release = %d, want 1", got)
	}
	second()
	second()
	if len(limiter.inFlight) != 0 {
		t.Fatal("released streams left in-flight state behind")
	}
}

func TestCredentialLimiterCleanupCadencePreservesExactExpiry(t *testing.T) {
	for _, limit := range []int64{0, 1} {
		name := "limited"
		if limit == 0 {
			name = "unlimited"
		}
		t.Run(name, func(t *testing.T) {
			base := time.Date(2026, time.October, 25, 12, 0, 0, 0, time.UTC)
			clock := &fakeClock{now: base}
			limiter := NewCredentialLimiter()
			limiter.now = clock.current
			limiter.Usage(99) // Start the activity-triggered cleanup cadence.
			clock.set(base.Add(30 * time.Second))
			for id := uint(1); id <= 3; id++ {
				release, ok := limiter.Acquire(id, limit, limit)
				if !ok {
					t.Fatalf("initial acquire for %d rejected", id)
				}
				release()
			}
			clock.set(base.Add(time.Minute))
			limiter.Usage(99)
			clock.set(base.Add(90*time.Second - time.Nanosecond))
			if rpm, _ := limiter.Usage(1); rpm != 1 {
				t.Fatalf("usage before exact expiry = %d, want 1", rpm)
			}
			clock.set(base.Add(90 * time.Second))
			limiter.Usage(99)
			if len(limiter.windows) != 3 || !limiter.lastCleanup.Equal(base.Add(time.Minute)) {
				t.Fatal("global cleanup ran less than one minute after previous sweep")
			}
			if rpm, _ := limiter.Usage(1); rpm != 0 {
				t.Fatalf("usage at exact expiry = %d, want 0", rpm)
			}
			release, ok := limiter.Acquire(2, limit, limit)
			if !ok {
				t.Fatal("acquire at exact expiry rejected")
			}
			release()
			window := limiter.windows[2]
			if window.len() != 1 || !window.at(0).Equal(clock.current()) {
				t.Fatalf("acquire window = %+v, want only the new timestamp", window)
			}
			clock.set(base.Add(2 * time.Minute))
			limiter.Usage(99)
			if _, exists := limiter.windows[3]; exists {
				t.Fatal("next activity-triggered sweep retained unused expired window")
			}
			if limiter.windows[2].len() != 1 {
				t.Fatal("next sweep removed the live window")
			}
		})
	}
}

func TestCredentialLimiterUnlimitedStillAccounts(t *testing.T) {
	limiter := NewCredentialLimiter()
	if !limiter.Available(7, 0, 0) {
		t.Fatal("unlimited availability rejected")
	}
	release, ok := limiter.Acquire(7, 0, 0)
	if !ok || release == nil {
		t.Fatal("unlimited acquire rejected")
	}
	if rpmUsed, inFlight := limiter.Usage(7); rpmUsed != 1 || inFlight != 1 {
		t.Fatalf("active usage = %d/%d, want 1/1", rpmUsed, inFlight)
	}
	release()
	release()
	if rpmUsed, inFlight := limiter.Usage(7); rpmUsed != 1 || inFlight != 0 {
		t.Fatalf("usage after double release = %d/%d, want 1/0", rpmUsed, inFlight)
	}
	if len(limiter.inFlight) != 0 {
		t.Fatal("released in-flight left state behind")
	}
}

func TestCredentialLimiterConcurrentAcquireNeverExceedsLimit(t *testing.T) {
	limiter := NewCredentialLimiter()
	start := make(chan struct{})
	var group sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for range 64 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			if _, ok := limiter.Acquire(7, 5, 3); ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	close(start)
	group.Wait()
	if allowed != 3 {
		t.Fatalf("allowed = %d, want 3 (min of rpm 5 and concurrency 3)", allowed)
	}
}
