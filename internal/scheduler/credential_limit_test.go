package scheduler

import (
	"errors"
	"testing"
	"time"

	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
	"gpt-load/internal/state"
)

// blockingLimiter 把指定凭据标记为满额，其余凭据一律放行。
type blockingLimiter struct {
	blocked    map[uint]struct{}
	retryAfter map[uint]time.Time
}

func (limiter blockingLimiter) Available(credentialID uint, rpmLimit, concurrencyLimit int64) bool {
	if rpmLimit <= 0 && concurrencyLimit <= 0 {
		return true
	}
	_, blocked := limiter.blocked[credentialID]
	return !blocked
}

func (limiter blockingLimiter) RetryAfter(credentialID uint, _ int64) time.Time {
	return limiter.retryAfter[credentialID]
}

func TestIteratorSkipsCredentialExhaustingItsLocalLimit(t *testing.T) {
	t.Parallel()

	snapshot := channelSchedulerSnapshot(t)
	group := snapshot.Groups[2]
	group.CredentialRPMLimit = 60
	snapshot.Groups[2] = group

	iterator := New(snapshot, fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 11, GroupID: 1},
		{ID: 21, GroupID: 2},
	}}, Query{
		ClientProtocol: protocol.OpenAICompletions,
		Operation:      execution.OperationChatCompletion,
		ExternalModel:  modelPointer("public"),
		Limiter:        blockingLimiter{blocked: map[uint]struct{}{21: {}}},
	})

	first, err := iterator.Next()
	if err != nil {
		t.Fatalf("first Next() error = %v", err)
	}
	// 21 继承分组的 RPM 限额且已被标记满额，应被跳过，落到无限额的 11。
	if first.CredentialID != 11 {
		t.Fatalf("first Selection credential = %d, want 11 (21 is locally exhausted)", first.CredentialID)
	}
	if first.CredentialRPMLimit != 0 || first.CredentialConcurrencyLimit != 0 {
		t.Fatalf("unlimited credential limits = %d/%d, want 0/0", first.CredentialRPMLimit, first.CredentialConcurrencyLimit)
	}
	if _, err := iterator.Next(); !errors.Is(err, ErrExhausted) {
		t.Fatalf("second Next() error = %v, want ErrExhausted", err)
	}
}

func TestIteratorUsesCredentialLimitOverGroupDefault(t *testing.T) {
	t.Parallel()

	snapshot := channelSchedulerSnapshot(t)
	group := snapshot.Groups[2]
	group.CredentialRPMLimit = 60
	snapshot.Groups[2] = group

	iterator := New(snapshot, fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 21, GroupID: 2, RPMLimit: 10},
	}}, Query{
		ClientProtocol: protocol.OpenAICompletions,
		Operation:      execution.OperationChatCompletion,
		ExternalModel:  modelPointer("public"),
		Limiter:        blockingLimiter{},
	})

	selection, err := iterator.Next()
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if selection.CredentialRPMLimit != 10 {
		t.Fatalf("CredentialRPMLimit = %d, want credential value 10 over group default 60", selection.CredentialRPMLimit)
	}
}

func TestIteratorInheritsGroupLimitWhenCredentialUnset(t *testing.T) {
	t.Parallel()

	snapshot := channelSchedulerSnapshot(t)
	group := snapshot.Groups[2]
	group.CredentialConcurrencyLimit = 4
	snapshot.Groups[2] = group

	iterator := New(snapshot, fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 21, GroupID: 2},
	}}, Query{
		ClientProtocol: protocol.OpenAICompletions,
		Operation:      execution.OperationChatCompletion,
		ExternalModel:  modelPointer("public"),
		Limiter:        blockingLimiter{},
	})

	selection, err := iterator.Next()
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if selection.CredentialConcurrencyLimit != 4 {
		t.Fatalf("CredentialConcurrencyLimit = %d, want inherited group default 4", selection.CredentialConcurrencyLimit)
	}
}

func TestIteratorReportsEarliestRetryAfterWhenAllCandidatesRPMExhausted(t *testing.T) {
	t.Parallel()

	retryAt := time.Now().Add(30 * time.Second)
	laterRetryAt := retryAt.Add(10 * time.Second)
	iterator := New(channelSchedulerSnapshot(t), fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 11, GroupID: 1, RPMLimit: 10},
		{ID: 21, GroupID: 2, RPMLimit: 10},
	}}, Query{
		ClientProtocol: protocol.OpenAICompletions,
		Operation:      execution.OperationChatCompletion,
		ExternalModel:  modelPointer("public"),
		Limiter: blockingLimiter{
			blocked:    map[uint]struct{}{11: {}, 21: {}},
			retryAfter: map[uint]time.Time{11: laterRetryAt, 21: retryAt},
		},
	})

	if _, err := iterator.Next(); !errors.Is(err, ErrExhausted) {
		t.Fatalf("Next() error = %v, want ErrExhausted", err)
	}
	until, limited := iterator.CredentialLimitedUntil()
	if !limited {
		t.Fatal("CredentialLimitedUntil() limited = false, want true")
	}
	if !until.Equal(retryAt) {
		t.Fatalf("CredentialLimitedUntil() until = %v, want earliest %v", until, retryAt)
	}
}

func TestIteratorReportsLowerPriorityRPMRecoveryWhenHigherPriorityIsFull(t *testing.T) {
	t.Parallel()

	snapshot := channelSchedulerSnapshot(t)
	high := snapshot.Groups[1]
	high.Priority = 100
	high.CredentialConcurrencyLimit = 1
	snapshot.Groups[1] = high
	low := snapshot.Groups[2]
	low.Priority = 0
	low.CredentialRPMLimit = 10
	snapshot.Groups[2] = low
	retryAt := time.Now().Add(30 * time.Second)
	iterator := New(snapshot, fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 11, GroupID: 1},
		{ID: 21, GroupID: 2},
	}}, Query{
		ClientProtocol: protocol.OpenAICompletions,
		Operation:      execution.OperationChatCompletion,
		ExternalModel:  modelPointer("public"),
		Limiter: blockingLimiter{
			blocked:    map[uint]struct{}{11: {}, 21: {}},
			retryAfter: map[uint]time.Time{21: retryAt},
		},
	})

	if _, err := iterator.Next(); !errors.Is(err, ErrExhausted) {
		t.Fatalf("Next() error = %v, want ErrExhausted", err)
	}
	until, limited := iterator.CredentialLimitedUntil()
	if !limited || !until.Equal(retryAt) {
		t.Fatalf("CredentialLimitedUntil() = %v, %t; want %v, true", until, limited, retryAt)
	}
}

func TestIteratorDoesNotReportUnselectedHigherPriorityLimitAfterLowerPrioritySelection(t *testing.T) {
	t.Parallel()

	snapshot := channelSchedulerSnapshot(t)
	high := snapshot.Groups[1]
	high.Priority = 100
	high.CredentialRPMLimit = 10
	snapshot.Groups[1] = high
	low := snapshot.Groups[2]
	low.Priority = 0
	snapshot.Groups[2] = low
	retryAt := time.Now().Add(30 * time.Second)
	iterator := New(snapshot, fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 11, GroupID: 1},
		{ID: 21, GroupID: 2},
	}}, Query{
		ClientProtocol: protocol.OpenAICompletions,
		Operation:      execution.OperationChatCompletion,
		ExternalModel:  modelPointer("public"),
		Limiter: blockingLimiter{
			blocked:    map[uint]struct{}{11: {}},
			retryAfter: map[uint]time.Time{11: retryAt},
		},
	})

	selection, err := iterator.Next()
	if err != nil || selection.CredentialID != 21 {
		t.Fatalf("Next() = %#v, %v; want lower-priority credential 21", selection, err)
	}
	if until, limited := iterator.CredentialLimitedUntil(); limited || !until.IsZero() {
		t.Fatalf("CredentialLimitedUntil() = %v, %t; want zero, false after a real selection", until, limited)
	}

	iterator.SkipGroup(2)
	if _, err := iterator.Next(); !errors.Is(err, ErrExhausted) {
		t.Fatalf("second Next() error = %v, want ErrExhausted", err)
	}
	if _, limited := iterator.CredentialLimitedUntil(); limited {
		t.Fatal("a failed lower-priority attempt left an unrelated higher-priority limit as the final reason")
	}
}

func TestIteratorReportsStoreDowngradedPoolLimitAfterRegularPoolSelection(t *testing.T) {
	t.Parallel()

	snapshot := responsesStoreSchedulerSnapshot(t, true)
	retryAt := time.Now().Add(30 * time.Second)
	iterator := New(snapshot, fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 11, GroupID: 1, RPMLimit: 10},
		{ID: 21, GroupID: 2},
		{ID: 31, GroupID: 3, RPMLimit: 10},
		{ID: 41, GroupID: 4, RPMLimit: 10},
	}}, Query{
		ClientProtocol:           protocol.OpenAIResponses,
		Operation:                execution.OperationResponsesCreate,
		ResponsesStorePreference: execution.ResponsesStorePreferencePreferStored,
		ExternalModel:            modelPointer("gpt"),
		Limiter: blockingLimiter{
			blocked:    map[uint]struct{}{11: {}, 31: {}, 41: {}},
			retryAfter: map[uint]time.Time{11: retryAt, 31: retryAt.Add(10 * time.Second), 41: retryAt.Add(20 * time.Second)},
		},
	})

	first, err := iterator.Next()
	if err != nil || first.CredentialID != 21 || first.ResponsesStoreDowngraded {
		t.Fatalf("first Next() = (%#v, %v), want exact regular credential 21", first, err)
	}
	if _, err := iterator.Next(); !errors.Is(err, ErrExhausted) {
		t.Fatalf("second Next() error = %v, want ErrExhausted", err)
	}
	until, limited := iterator.CredentialLimitedUntil()
	if !limited || !until.Equal(retryAt) {
		t.Fatalf("CredentialLimitedUntil() = %v, %t; want %v, true from downgraded pool", until, limited, retryAt)
	}
}

func TestIteratorClearsPreviousCredentialLimitAfterLaterSelection(t *testing.T) {
	t.Parallel()

	snapshot := channelSchedulerSnapshot(t)
	limiter := blockingLimiter{blocked: map[uint]struct{}{21: {}}, retryAfter: map[uint]time.Time{21: time.Now().Add(time.Minute)}}
	iterator := New(snapshot, fakeCredentialSource{keys: []state.CredentialMeta{{ID: 21, GroupID: 2, RPMLimit: 10}}}, Query{
		ClientProtocol: protocol.OpenAICompletions,
		Operation:      execution.OperationChatCompletion,
		ExternalModel:  modelPointer("public"),
		Limiter:        limiter,
	})
	if _, err := iterator.Next(); !errors.Is(err, ErrExhausted) {
		t.Fatalf("first Next() error = %v, want ErrExhausted", err)
	}
	if _, limited := iterator.CredentialLimitedUntil(); !limited {
		t.Fatal("first exhausted Next() did not report local limit")
	}
	delete(limiter.blocked, 21)
	selection, err := iterator.Next()
	if err != nil || selection.CredentialID != 21 {
		t.Fatalf("second Next() = (%#v, %v), want credential 21", selection, err)
	}
	if until, limited := iterator.CredentialLimitedUntil(); limited || !until.IsZero() {
		t.Fatalf("CredentialLimitedUntil() after selection = %v, %t; want zero, false", until, limited)
	}
}

func TestIteratorReportsZeroRetryAfterWhenOnlyConcurrencyExhausted(t *testing.T) {
	t.Parallel()

	iterator := New(channelSchedulerSnapshot(t), fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 21, GroupID: 2, ConcurrencyLimit: 1},
	}}, Query{
		ClientProtocol: protocol.OpenAICompletions,
		Operation:      execution.OperationChatCompletion,
		ExternalModel:  modelPointer("public"),
		Limiter:        blockingLimiter{blocked: map[uint]struct{}{21: {}}},
	})

	if _, err := iterator.Next(); !errors.Is(err, ErrExhausted) {
		t.Fatalf("Next() error = %v, want ErrExhausted", err)
	}
	until, limited := iterator.CredentialLimitedUntil()
	if !limited {
		t.Fatal("CredentialLimitedUntil() limited = false, want true")
	}
	if !until.IsZero() {
		t.Fatalf("CredentialLimitedUntil() until = %v, want zero (concurrency has no known recovery)", until)
	}
}

func TestIteratorReportsNoCredentialLimitWhenUnlimited(t *testing.T) {
	t.Parallel()

	iterator := New(channelSchedulerSnapshot(t), fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 21, GroupID: 2},
	}}, Query{
		ClientProtocol: protocol.OpenAICompletions,
		Operation:      execution.OperationChatCompletion,
		ExternalModel:  modelPointer("public"),
		Limiter:        blockingLimiter{blocked: map[uint]struct{}{21: {}}},
	})

	if _, err := iterator.Next(); err != nil {
		t.Fatalf("first Next() error = %v", err)
	}
	if _, err := iterator.Next(); !errors.Is(err, ErrExhausted) {
		t.Fatalf("second Next() error = %v, want ErrExhausted", err)
	}
	until, limited := iterator.CredentialLimitedUntil()
	if limited {
		t.Fatal("CredentialLimitedUntil() limited = true, want false without configured limits")
	}
	if !until.IsZero() {
		t.Fatalf("CredentialLimitedUntil() until = %v, want zero", until)
	}
}

func TestIteratorCanRetryCredentialAfterLastMomentSlotRace(t *testing.T) {
	t.Parallel()

	iterator := New(channelSchedulerSnapshot(t), fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 11, GroupID: 1},
	}}, Query{
		ClientProtocol: protocol.OpenAICompletions,
		Operation:      execution.OperationChatCompletion,
		ExternalModel:  modelPointer("public"),
	})
	first, err := iterator.Next()
	if err != nil {
		t.Fatalf("first Next() error = %v", err)
	}
	iterator.RetryCredential(first.CredentialID)
	second, err := iterator.Next()
	if err != nil {
		t.Fatalf("second Next() error = %v", err)
	}
	if second.CredentialID != first.CredentialID {
		t.Fatalf("second selection = %#v, want retried credential %d", second, first.CredentialID)
	}
}
