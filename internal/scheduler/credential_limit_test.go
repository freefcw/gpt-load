package scheduler

import (
	"errors"
	"testing"

	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
	"gpt-load/internal/state"
)

// blockingLimiter 把指定凭据标记为满额，其余凭据一律放行。
type blockingLimiter struct {
	blocked map[uint]struct{}
}

func (limiter blockingLimiter) Available(credentialID uint, rpmLimit, concurrencyLimit int64) bool {
	if rpmLimit <= 0 && concurrencyLimit <= 0 {
		return true
	}
	_, blocked := limiter.blocked[credentialID]
	return !blocked
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
