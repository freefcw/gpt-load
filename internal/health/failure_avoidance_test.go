package health

import (
	"testing"

	"gpt-load/internal/execution"
)

func TestFailureAvoidanceScopeRespectsAttribution(t *testing.T) {
	base := Decision{
		Category: FailureCategoryUpstreamHostError, Origin: execution.ErrorOriginUpstream,
		Scope: execution.ErrorScopeGroup, Retry: RetryNextCandidate, Effect: EffectNone,
		RuleID: "candidate.transient_capacity",
	}
	for _, test := range []struct {
		name   string
		modify func(*Decision)
		want   execution.ErrorScope
	}{
		{"group capacity stays on attempted target", func(*Decision) {}, execution.ErrorScopeModel},
		{"unscoped capacity stays on attempted target", func(d *Decision) { d.Scope = "" }, execution.ErrorScopeModel},
		{"credential capacity", func(d *Decision) { d.Scope = execution.ErrorScopeCredential }, execution.ErrorScopeCredential},
		{"request capacity", func(d *Decision) { d.Scope = execution.ErrorScopeRequest }, ""},
		{"internal failure", func(d *Decision) { d.Origin = execution.ErrorOriginInternal }, ""},
		{"model cooldown", func(d *Decision) { d.Effect = EffectCooldownModel }, ""},
		{"credential cooldown", func(d *Decision) { d.Effect = EffectCooldownCredential }, ""},
		{"group skip", func(d *Decision) { d.Effect = EffectSkipGroup }, ""},
		{"ambiguous retry alone", func(d *Decision) { d.Category = FailureCategoryAmbiguous }, ""},
		{"client retry alone", func(d *Decision) { d.Category = FailureCategoryClientError }, ""},
		{"conversion retry alone", func(d *Decision) { d.Category = FailureCategoryConversionUnsupported }, ""},
		{"custom retry alone", func(d *Decision) { d.RuleID = "custom.group.1" }, ""},
		{"no-replay model rejection", func(d *Decision) {
			d.Category, d.Scope, d.Retry, d.RuleID = FailureCategoryModelUnavailable, execution.ErrorScopeModel, RetryNone, "safety.replay_unknown"
		}, execution.ErrorScopeModel},
		{"resource lookup", func(d *Decision) {
			d.Category, d.Scope, d.Retry, d.RuleID = FailureCategoryModelUnavailable, execution.ErrorScopeModel, RetryNone, "model.resource_unavailable"
		}, ""},
		{"invalid key accounting", func(d *Decision) {
			d.Category, d.Scope, d.Effect, d.RuleID = FailureCategoryInvalidKey, execution.ErrorScopeCredential, EffectRecordCredentialFailure, "auth.invalid_credential"
		}, execution.ErrorScopeCredential},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision := base
			test.modify(&decision)
			if got := decision.FailureAvoidanceScope(execution.OperationChatCompletion); got != test.want {
				t.Fatalf("scope = %q, want %q for %+v", got, test.want, decision)
			}
		})
	}
	if scope := base.FailureAvoidanceScope(""); scope != "" {
		t.Fatalf("unknown operation created scope %q", scope)
	}
}
