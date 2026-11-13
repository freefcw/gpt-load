package scheduler

import (
	"testing"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
	"gpt-load/internal/state"
)

func failureTarget(model string, operation execution.Operation) state.FailureAvoidanceKey {
	return state.FailureAvoidanceKey{Model: model, Operation: operation}
}

func pickFailureAt(t *testing.T, snapshot *state.ConfigSnapshot, registry *state.CredentialRegistry, query Query, now time.Time) (Selection, error) {
	t.Helper()
	iterator := newWithClock(snapshot, registry, query, func() time.Time { return now })
	defer iterator.Close()
	return iterator.Next()
}

func failureDeadline(progress *state.SchedulingState, ref state.CredentialRef, key state.FailureAvoidanceKey) time.Time {
	var deadline time.Time
	progress.WithLock(func(ledger *state.SchedulingLedger) {
		member := ledger.Members[ref.ID]
		if member != nil && member.GroupID == ref.GroupID && member.IdentityGeneration == ref.IdentityGeneration {
			deadline = member.FailureAvoidance[key]
		}
	})
	return deadline
}

func replaceFailureIdentity(t *testing.T, registry *state.CredentialRegistry, ref state.CredentialRef, version, generation uint64) state.CredentialRef {
	t.Helper()
	err := registry.ReplaceCredentials([]state.CredentialEntry{{
		ID: ref.ID, GroupID: ref.GroupID, Version: version, IdentityGeneration: generation,
		Status: state.CredentialStatusActive, Fingerprint: "fixture", EncryptedValue: "cipher",
	}})
	if err != nil {
		t.Fatal(err)
	}
	current, ok := registry.CredentialRef(ref.ID)
	if !ok {
		t.Fatal("replacement credential missing")
	}
	return current
}

func TestFailureAvoidanceScopedModelAndOperation(t *testing.T) {
	now := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	chat := execution.OperationChatCompletion
	t.Run("model A does not hide model B on same credential", func(t *testing.T) {
		snapshot, registry := modelRotationFixture(t)
		ref, _ := registry.CredentialRef(1)
		AvoidFailedCredential(registry.SchedulingState(), ref, ref.Version, failureTarget("m1", chat), now)
		query := modelRotationQuery(1, 2)
		query.Limiter = loadLimiter{usage: map[uint][2]int64{2: {0, 3}}}
		selection, err := pickFailureAt(t, snapshot, registry, query, now)
		if err != nil || selection.CredentialID != 1 || selection.UpstreamModelID == nil || *selection.UpstreamModelID != "m2" {
			t.Fatalf("selection = %+v, error = %v; want credential 1's healthy sibling model", selection, err)
		}
	})
	t.Run("alternate operation remains available", func(t *testing.T) {
		snapshot, registry := modelRotationFixture(t)
		ref, _ := registry.CredentialRef(1)
		AvoidFailedCredential(registry.SchedulingState(), ref, 0, failureTarget("m1", chat), now)
		query := modelRotationQuery(1)
		query.ClientProtocol = protocol.OpenAIResponses
		query.Operation = execution.OperationResponsesCreate
		selection, err := pickFailureAt(t, snapshot, registry, query, now)
		if err != nil || selection.CredentialID != 1 || selection.UpstreamModelID == nil || *selection.UpstreamModelID != "m1" {
			t.Fatalf("selection = %+v, error = %v; want m1 for unaffected operation", selection, err)
		}
	})
}

func TestFailureAvoidanceAllModelsFallbackAndHealthyBusyCredential(t *testing.T) {
	now := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	t.Run("all avoided falls back", func(t *testing.T) {
		snapshot, registry := modelRotationFixture(t)
		ref, _ := registry.CredentialRef(1)
		for _, model := range []string{"m1", "m2", "m3"} {
			AvoidFailedCredential(registry.SchedulingState(), ref, 0, failureTarget(model, execution.OperationChatCompletion), now)
		}
		selection, err := pickFailureAt(t, snapshot, registry, modelRotationQuery(1), now)
		if err != nil || selection.CredentialID != 1 || selection.UpstreamModelID == nil {
			t.Fatalf("all avoided selection = %+v, error = %v; want fallback, not exhaustion", selection, err)
		}
	})

	snapshot, registry := modelRotationFixture(t)
	group := snapshot.Groups[1]
	group.CredentialConcurrencyLimit = 10
	snapshot.Groups[1] = group
	ref, _ := registry.CredentialRef(1)
	for _, model := range []string{"m1", "m2", "m3"} {
		AvoidFailedCredential(registry.SchedulingState(), ref, 0, failureTarget(model, execution.OperationChatCompletion), now)
	}
	query := modelRotationQuery(1, 2)
	query.Limiter = loadLimiter{usage: map[uint][2]int64{2: {0, 7}}}
	selection, err := pickFailureAt(t, snapshot, registry, query, now)
	if err != nil || selection.CredentialID != 2 {
		t.Fatalf("selection = %+v, error = %v; want other healthy busy credential", selection, err)
	}
}

func TestFailureAvoidanceStickyBypassAndHardLimit(t *testing.T) {
	now := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	t.Run("sticky bypass", func(t *testing.T) {
		snapshot, registry := modelRotationFixture(t)
		ref, _ := registry.CredentialRef(1)
		AvoidFailedCredential(registry.SchedulingState(), ref, 0, state.FailureAvoidanceKey{}, now)
		query := modelRotationQuery(1, 2)
		query.PreferredCredentialID = 1
		selection, err := pickFailureAt(t, snapshot, registry, query, now)
		if err != nil || selection.CredentialID != 1 {
			t.Fatalf("selection = %+v, error = %v; want sticky credential 1", selection, err)
		}
	})
	t.Run("sticky bypasses model filtering", func(t *testing.T) {
		snapshot, registry := modelRotationFixture(t)
		ref, _ := registry.CredentialRef(1)
		AvoidFailedCredential(registry.SchedulingState(), ref, 0, failureTarget("m1", execution.OperationChatCompletion), now)
		query := modelRotationQuery(1, 2)
		query.PreferredCredentialID = 1
		selection, err := pickFailureAt(t, snapshot, registry, query, now)
		if err != nil || selection.CredentialID != 1 || selection.UpstreamModelID == nil || *selection.UpstreamModelID != "m1" {
			t.Fatalf("sticky model selection = %+v, error = %v; want avoided m1", selection, err)
		}
	})
	t.Run("hard limit still applies", func(t *testing.T) {
		snapshot, registry := modelRotationFixture(t)
		group := snapshot.Groups[1]
		group.CredentialConcurrencyLimit = 10
		snapshot.Groups[1] = group
		ref, _ := registry.CredentialRef(1)
		AvoidFailedCredential(registry.SchedulingState(), ref, 0, state.FailureAvoidanceKey{}, now)
		query := modelRotationQuery(1, 2)
		query.PreferredCredentialID = 1
		query.Limiter = loadLimiter{usage: map[uint][2]int64{1: {0, 10}}, blocked: map[uint]struct{}{1: {}}}
		selection, err := pickFailureAt(t, snapshot, registry, query, now)
		if err != nil || selection.CredentialID != 2 {
			t.Fatalf("selection = %+v, error = %v; want hard-limit fallback", selection, err)
		}
	})
}

func TestFailureAvoidanceSuccessClearsOnlyMatchingTarget(t *testing.T) {
	now := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	progress := state.NewSchedulingState()
	ref := state.CredentialRef{ID: 1, GroupID: 1, Version: 1, IdentityGeneration: 1}
	progress.SyncCredential(state.CredentialRuntimeView{ID: 1, GroupID: 1, Version: 1, IdentityGeneration: 1, Status: state.CredentialStatusActive})
	modelA := failureTarget("m1", execution.OperationChatCompletion)
	modelB := failureTarget("m2", execution.OperationChatCompletion)
	otherOperation := failureTarget("m1", execution.OperationResponsesCreate)
	AvoidFailedCredential(progress, ref, 0, modelA, now)
	ForgetCredentialFailure(progress, ref, modelB, now)
	if failureDeadline(progress, ref, modelA).IsZero() {
		t.Fatal("model B success cleared model A failure")
	}
	ForgetCredentialFailure(progress, ref, otherOperation, now)
	if failureDeadline(progress, ref, modelA).IsZero() {
		t.Fatal("unrelated success cleared model A failure")
	}
	ForgetCredentialFailure(progress, ref, modelA, now)
	if !failureDeadline(progress, ref, modelA).IsZero() {
		t.Fatal("matching success did not clear model A failure")
	}
}

func TestFailureAvoidanceWideFailureClearsOnSpecificSuccess(t *testing.T) {
	now := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	progress := state.NewSchedulingState()
	ref := state.CredentialRef{ID: 1, GroupID: 1, Version: 1, IdentityGeneration: 1}
	progress.SyncCredential(state.CredentialRuntimeView{ID: 1, GroupID: 1, Version: 1, IdentityGeneration: 1, Status: state.CredentialStatusActive})
	wide := state.FailureAvoidanceKey{}
	model := failureTarget("m1", execution.OperationChatCompletion)
	AvoidFailedCredential(progress, ref, 0, wide, now)
	progress.WithLock(func(ledger *state.SchedulingLedger) {
		if !ledger.Members[1].AvoidsFailureTarget(failureTarget("m2", execution.OperationChatCompletion), now) {
			t.Fatal("credential-wide failure did not apply across models")
		}
	})
	ForgetCredentialFailure(progress, ref, model, now)
	if !failureDeadline(progress, ref, wide).IsZero() {
		t.Fatal("specific success did not clear credential-wide failure")
	}
}

func TestFailureAvoidanceStaleIdentityAndVersionFeedback(t *testing.T) {
	now := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	t.Run("stale version ignored and zero accepts refresh", func(t *testing.T) {
		_, registry := modelRotationFixture(t)
		old, _ := registry.CredentialRef(1)
		current := replaceFailureIdentity(t, registry, old, old.Version+1, old.IdentityGeneration)
		key := failureTarget("m2", execution.OperationChatCompletion)
		AvoidFailedCredential(registry.SchedulingState(), current, old.Version, key, now)
		if !failureDeadline(registry.SchedulingState(), current, key).IsZero() {
			t.Fatal("stale expected version recorded failure")
		}
		AvoidFailedCredential(registry.SchedulingState(), old, 0, key, now)
		if failureDeadline(registry.SchedulingState(), current, key).IsZero() {
			t.Fatal("zero expected version rejected same-identity token refresh")
		}
		ForgetCredentialFailure(registry.SchedulingState(), old, key, now)
		if !failureDeadline(registry.SchedulingState(), current, key).IsZero() {
			t.Fatal("same-identity success captured before refresh did not clear failure")
		}
		AvoidFailedCredential(registry.SchedulingState(), current, current.Version, key, now)
		if failureDeadline(registry.SchedulingState(), current, key).IsZero() {
			t.Fatal("current expected version rejected failure")
		}
	})
	t.Run("stale identity cannot change or clear current state", func(t *testing.T) {
		_, registry := modelRotationFixture(t)
		old, _ := registry.CredentialRef(1)
		current := replaceFailureIdentity(t, registry, old, old.Version, old.IdentityGeneration+1)
		m1 := failureTarget("m1", execution.OperationChatCompletion)
		m2 := failureTarget("m2", execution.OperationChatCompletion)
		AvoidFailedCredential(registry.SchedulingState(), current, 0, m1, now)
		AvoidFailedCredential(registry.SchedulingState(), old, 0, m2, now)
		ForgetCredentialFailure(registry.SchedulingState(), old, m1, now)
		if failureDeadline(registry.SchedulingState(), current, m1).IsZero() || !failureDeadline(registry.SchedulingState(), current, m2).IsZero() {
			t.Fatal("stale identity feedback changed current failure state")
		}
		ForgetCredentialFailure(registry.SchedulingState(), current, m1, now)
		if !failureDeadline(registry.SchedulingState(), current, m1).IsZero() {
			t.Fatal("current identity success did not clear failure")
		}
	})
}

func TestFailureAvoidanceOutOfOrderSuccessCannotClearNewerFailure(t *testing.T) {
	progress := state.NewSchedulingState()
	ref := state.CredentialRef{ID: 1, GroupID: 1, Version: 1, IdentityGeneration: 1}
	progress.SyncCredential(state.CredentialRuntimeView{ID: 1, GroupID: 1, Version: 1, IdentityGeneration: 1, Status: state.CredentialStatusActive})
	failureAt := time.Date(2025, time.January, 2, 3, 4, 20, 0, time.UTC)
	key := failureTarget("m1", execution.OperationChatCompletion)
	wide := state.FailureAvoidanceKey{}
	AvoidFailedCredential(progress, ref, 0, key, failureAt)
	AvoidFailedCredential(progress, ref, 0, wide, failureAt)
	ForgetCredentialFailure(progress, ref, key, failureAt.Add(-time.Nanosecond))
	if failureDeadline(progress, ref, key).IsZero() || failureDeadline(progress, ref, wide).IsZero() {
		t.Fatal("older same-identity success cleared newer failure")
	}
	ForgetCredentialFailure(progress, ref, key, failureAt)
	if !failureDeadline(progress, ref, key).IsZero() || !failureDeadline(progress, ref, wide).IsZero() {
		t.Fatal("newer same-identity success did not clear failure")
	}
}

func TestFailureAvoidanceAliasRouteTierScope(t *testing.T) {
	now := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	snapshot, registry := modelRotationFixture(t)
	routes := snapshot.ExecutionCandidates[protocol.OpenAICompletions][execution.OperationChatCompletion]["public"]
	for index := range routes {
		if routes[index].GroupID == 1 && routes[index].UpstreamModelID == "m2" {
			routes[index].Mode = channel.RouteConverted
		}
	}
	ref, _ := registry.CredentialRef(1)
	for _, model := range []string{"m1", "m3"} {
		AvoidFailedCredential(registry.SchedulingState(), ref, 0, failureTarget(model, execution.OperationChatCompletion), now)
	}
	query := modelRotationQuery(1, 3)
	query.Limiter = loadLimiter{usage: map[uint][2]int64{3: {0, 3}}}
	selection, err := pickFailureAt(t, snapshot, registry, query, now)
	if err != nil || selection.CredentialID != 3 || selection.RouteMode != channel.RouteNative {
		t.Fatalf("native-first selection = %+v, error = %v; healthy converted m2 must not mask failed native targets", selection, err)
	}
	snapshot.Settings.RouteStrategy = state.RouteStrategyWeightedMix
	selection, err = pickFailureAt(t, snapshot, registry, query, now)
	if err != nil || selection.CredentialID != 1 || selection.RouteMode != channel.RouteConverted || selection.UpstreamModelID == nil || *selection.UpstreamModelID != "m2" {
		t.Fatalf("weighted-mix selection = %+v, error = %v; want idle credential's healthy m2", selection, err)
	}
}

func TestFailureAvoidanceOnlyCountsAvailableAliasTargets(t *testing.T) {
	now := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	snapshot, registry := modelRotationFixture(t)
	ref, _ := registry.CredentialRef(1)
	for _, model := range []string{"m2", "m3"} {
		if ok, _ := registry.SetModelCooldown(ref, model, now.Add(time.Hour), now); !ok {
			t.Fatal("set model cooldown")
		}
	}
	AvoidFailedCredential(registry.SchedulingState(), ref, 0, failureTarget("m1", execution.OperationChatCompletion), now)
	query := modelRotationQuery(1, 2)
	query.Limiter = loadLimiter{usage: map[uint][2]int64{2: {0, 3}}}
	selection, err := pickFailureAt(t, snapshot, registry, query, now)
	if err != nil || selection.CredentialID != 2 {
		t.Fatalf("selection = %+v, error = %v; cooled sibling models must not mask the only available model's failure", selection, err)
	}
}

func TestFailureAvoidanceCredentialWideRoutingAndSpecificRecovery(t *testing.T) {
	now := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	snapshot, registry := modelRotationFixture(t)
	ref, _ := registry.CredentialRef(1)
	progress := registry.SchedulingState()
	modelA := failureTarget("m1", execution.OperationChatCompletion)
	AvoidFailedCredential(progress, ref, 0, modelA, now)
	AvoidFailedCredential(progress, ref, 0, state.FailureAvoidanceKey{}, now)
	query := modelRotationQuery(1, 2)
	query.Limiter = loadLimiter{usage: map[uint][2]int64{2: {0, 3}}}
	for _, responses := range []bool{false, true} {
		request := query
		if responses {
			request.ClientProtocol, request.Operation = protocol.OpenAIResponses, execution.OperationResponsesCreate
		}
		selection, err := pickFailureAt(t, snapshot, registry, request, now)
		if err != nil || selection.CredentialID != 2 {
			t.Fatalf("credential-wide selection = %+v, error = %v; want healthy credential across all models and operations", selection, err)
		}
	}
	ForgetCredentialFailure(progress, ref, failureTarget("m2", execution.OperationChatCompletion), now)
	if failureDeadline(progress, ref, modelA).IsZero() || !failureDeadline(progress, ref, state.FailureAvoidanceKey{}).IsZero() {
		t.Fatal("model B success must clear wide failure but preserve model A failure")
	}
	selection, err := pickFailureAt(t, snapshot, registry, query, now)
	if err != nil || selection.CredentialID != 1 || selection.UpstreamModelID == nil || *selection.UpstreamModelID == "m1" {
		t.Fatalf("recovered selection = %+v, error = %v; want idle credential's healthy sibling", selection, err)
	}
}

func TestFailureAvoidanceCleansIdleMembersOnUnrelatedScheduling(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	snapshot, registry := modelRotationFixture(t)
	ref, _ := registry.CredentialRef(1)
	key := failureTarget("m1", execution.OperationChatCompletion)
	AvoidFailedCredential(registry.SchedulingState(), ref, 0, key, now)
	// Only credential 3 participates in this later request. A sweep must still
	// release expired state for credential 1, which will never be queried again.
	if _, err := pickFailureAt(t, snapshot, registry, modelRotationQuery(3), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	registry.SchedulingState().WithLock(func(ledger *state.SchedulingLedger) {
		if ledger.Members[1].FailureAvoidance != nil {
			t.Fatal("unrelated scheduling retained idle member's expired avoidance")
		}
	})
	registry.RemoveCredential(1)
	AvoidFailedCredential(registry.SchedulingState(), ref, 0, key, now.Add(time.Minute))
	registry.SchedulingState().WithLock(func(ledger *state.SchedulingLedger) {
		if _, exists := ledger.Members[1]; exists {
			t.Fatal("late feedback recreated a deleted member")
		}
	})
}

func TestFailureAvoidanceKeepsTargetEligibilityTimeConsistent(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	snapshot, registry := modelRotationFixture(t)
	ref, _ := registry.CredentialRef(1)
	if ok, _ := registry.SetModelCooldown(ref, "m2", base.Add(time.Second), base); !ok {
		t.Fatal("could not cool m2")
	}
	if ok, _ := registry.SetModelCooldown(ref, "m3", base.Add(time.Hour), base); !ok {
		t.Fatal("could not cool m3")
	}
	AvoidFailedCredential(registry.SchedulingState(), ref, 0, failureTarget("m1", execution.OperationChatCompletion), base)
	query := modelRotationQuery(1, 2)
	query.Limiter = loadLimiter{usage: map[uint][2]int64{2: {0, 3}}}
	reads := 0
	iterator := newWithClock(snapshot, registry, query, func() time.Time {
		reads++
		if reads == 1 {
			return base // Next freezes the available targets here.
		}
		return base.Add(2 * time.Second) // m2 expires before avoidance filtering.
	})
	defer iterator.Close()
	selection, err := iterator.Next()
	if err != nil || selection.CredentialID != 2 {
		t.Fatalf("selection = %+v, %v; newly expired m2 must not mask avoided m1 in the frozen target pool", selection, err)
	}
}
