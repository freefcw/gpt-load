package gateway

import (
	"net/http"
	"testing"
	"time"

	"gpt-load/internal/execution"
	"gpt-load/internal/health"
	"gpt-load/internal/protocol"
	"gpt-load/internal/ratelimit"
	"gpt-load/internal/scheduler"
	"gpt-load/internal/state"
)

func feedbackEntries(registry *state.CredentialRegistry, id uint) map[state.FailureAvoidanceKey]time.Time {
	entries := make(map[state.FailureAvoidanceKey]time.Time)
	registry.SchedulingState().WithLock(func(ledger *state.SchedulingLedger) {
		if member := ledger.Members[id]; member != nil {
			for key, until := range member.FailureAvoidance {
				entries[key] = until
			}
		}
	})
	return entries
}

func capacityFeedback(now time.Time) health.Decision {
	return judgeUpstreamResult(capacityRejectedResult(), now, health.DecisionContext{
		Operation: execution.OperationChatCompletion, Method: http.MethodPost,
	})
}

func TestSchedulingFeedbackIgnoresReplacedIdentity(t *testing.T) {
	_, handler, registry, _ := newStatsHandlerTestRuntime(t, &scriptedForwarder{}, "old")
	old, _ := registry.CredentialRef(1)
	entry := testCredentialEntry(t, handler.encryption, 1, 1, "new")
	entry.Version, entry.IdentityGeneration = old.Version+1, old.IdentityGeneration+1
	if err := registry.ApplyCredentialImport(1, []state.CredentialEntry{entry}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	target := failureAvoidanceTarget("gpt-4o", execution.OperationChatCompletion)
	decision := capacityFeedback(now)
	handler.applyGroupDecisionEffect(state.GroupView{}, old, 0, decision, 503, now, target)
	if got := feedbackEntries(registry, 1); len(got) != 0 {
		t.Fatalf("stale failure affected replacement: %v", got)
	}
	current, _ := registry.CredentialRef(1)
	handler.applyGroupDecisionEffect(state.GroupView{}, current, 0, decision, 503, now, target)
	handler.recordCredentialSuccess(old, now.Add(time.Second), target)
	if got := feedbackEntries(registry, 1); len(got) != 1 || got[target].IsZero() {
		t.Fatalf("stale success cleared replacement's failure: %v", got)
	}
	handler.recordCredentialSuccess(current, now.Add(2*time.Second), target)
	if got := feedbackEntries(registry, 1); len(got) != 0 {
		t.Fatalf("matching success failed to clear avoidance: %v", got)
	}
}

func TestSchedulingFeedbackRespectsExpectedCredentialVersion(t *testing.T) {
	_, handler, registry, _ := newStatsHandlerTestRuntime(t, &scriptedForwarder{}, "key")
	old, _ := registry.CredentialRef(1)
	if !registry.ReplaceCredentialSecretIfMatch(1, old.Version, old.Version+1, "refreshed", "cipher-refreshed") {
		t.Fatal("refresh failed")
	}
	now := time.Now()
	target := failureAvoidanceTarget("gpt-4o", execution.OperationChatCompletion)
	decision := capacityFeedback(now)
	handler.applyGroupDecisionEffect(state.GroupView{}, old, old.Version, decision, 503, now, target)
	if got := feedbackEntries(registry, 1); len(got) != 0 {
		t.Fatalf("stale version changed avoidance: %v", got)
	}
	// A zero expected version deliberately keeps same-identity account feedback
	// valid across token refresh, matching existing health attribution semantics.
	handler.applyGroupDecisionEffect(state.GroupView{}, old, 0, decision, 503, now, target)
	if got := feedbackEntries(registry, 1); len(got) != 1 {
		t.Fatalf("same-identity unversioned feedback lost: %v", got)
	}
}

func TestSchedulingFeedbackIsolatesModelAndOperation(t *testing.T) {
	_, handler, registry, _ := newStatsHandlerTestRuntime(t, &scriptedForwarder{}, "one", "two")
	ref, _ := registry.CredentialRef(1)
	now := time.Now()
	a := failureAvoidanceTarget("different-model", execution.OperationChatCompletion)
	b := failureAvoidanceTarget("gpt-4o", execution.OperationChatCompletion)
	handler.applyGroupDecisionEffect(state.GroupView{}, ref, 0, capacityFeedback(now), 503, now, a)
	limiter := ratelimit.NewCredentialLimiter()
	for range 3 {
		release, ok := limiter.Acquire(2, 0, 0)
		if !ok {
			t.Fatal("initial acquire rejected")
		}
		defer release()
	}
	iterator := scheduler.New(handler.manager.Current(), registry, scheduler.Query{
		ClientProtocol: protocol.OpenAICompletions, Operation: execution.OperationChatCompletion,
		ExternalModel: &b.Model, Limiter: limiter,
	})
	defer iterator.Close()
	selection, err := iterator.Next()
	if err != nil || selection.CredentialID != 1 {
		t.Fatalf("model A failure biased unrelated model B: %+v, %v", selection, err)
	}
	for _, success := range []state.FailureAvoidanceKey{
		b, failureAvoidanceTarget(a.Model, execution.OperationResponsesCreate),
	} {
		handler.recordCredentialSuccess(ref, now.Add(time.Second), success)
		if got := feedbackEntries(registry, 1); len(got) != 1 || got[a].IsZero() {
			t.Fatalf("unrelated success cleared model A: %v", got)
		}
	}
	handler.recordCredentialSuccess(ref, now.Add(2*time.Second), a)
	if got := feedbackEntries(registry, 1); len(got) != 0 {
		t.Fatalf("matching success did not clear model A: %v", got)
	}
}

func TestSchedulingFeedbackDoesNotDuplicateModelCooldown(t *testing.T) {
	_, handler, registry, _ := newStatsHandlerTestRuntime(t, &scriptedForwarder{}, "one")
	ref, _ := registry.CredentialRef(1)
	now := time.Now()
	decision := judgeUpstreamResult(UpstreamResult{
		DispatchState: execution.DispatchMaybeSent, StatusCode: http.StatusTooManyRequests,
		ExecutionError: &execution.ErrorEvidence{
			Kind: execution.ErrorKindHTTP, Hint: execution.FailureHintRateLimited,
			OriginHint: execution.ErrorOriginUpstream, ScopeHint: execution.ErrorScopeModel,
			StatusCode: http.StatusTooManyRequests, Code: "model_quota_exhausted",
		},
	}, now, health.DecisionContext{
		Operation: execution.OperationChatCompletion, Method: http.MethodPost, DefaultRateLimitCooldown: time.Minute,
	})
	if decision.Effect != health.EffectCooldownModel {
		t.Fatalf("expected model cooldown: %+v", decision)
	}
	target := failureAvoidanceTarget("model-a", execution.OperationChatCompletion)
	handler.applyGroupDecisionEffect(state.GroupView{}, ref, 0, decision, 429, now, target)
	if got := feedbackEntries(registry, 1); len(got) != 0 {
		t.Fatalf("hard model cooldown created extra avoidance: %v", got)
	}
	if !registry.ModelCooldowns(1, now)[target.Model].After(now) {
		t.Fatal("existing model cooldown was lost")
	}
}

func TestSchedulingFeedbackMissingModelCannotWidenTargetScope(t *testing.T) {
	_, handler, registry, _ := newStatsHandlerTestRuntime(t, &scriptedForwarder{}, "one")
	ref, _ := registry.CredentialRef(1)
	now := time.Now()
	missing := failureAvoidanceTarget("", execution.OperationChatCompletion)
	decision := capacityFeedback(now)
	handler.applyGroupDecisionEffect(state.GroupView{}, ref, 0, decision, 503, now, missing)
	if got := feedbackEntries(registry, 1); len(got) != 0 {
		t.Fatalf("missing model widened target-scoped rejection: %v", got)
	}
	// Explicit credential scope does not require a model, including resource
	// requests. Its success can clear account-wide, but not model-specific state.
	decision.Scope = execution.ErrorScopeCredential
	handler.applyGroupDecisionEffect(state.GroupView{}, ref, 0, decision, 503, now, missing)
	if got := feedbackEntries(registry, 1); len(got) != 1 || got[state.FailureAvoidanceKey{}].IsZero() {
		t.Fatalf("credential-wide rejection was lost: %v", got)
	}
	handler.recordCredentialSuccess(ref, now.Add(time.Second), failureAvoidanceTarget("", execution.OperationResponsesRetrieve))
	if got := feedbackEntries(registry, 1); len(got) != 0 {
		t.Fatalf("resource success did not clear credential-wide rejection: %v", got)
	}
}
