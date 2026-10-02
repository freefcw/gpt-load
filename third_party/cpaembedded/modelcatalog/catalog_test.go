package modelcatalog

import "testing"

func TestCodexCapabilities(t *testing.T) {
	for _, model := range []string{"gpt-6.1-sol", "gpt-6-sol", "gpt-6-luna", "gpt-6-astra"} {
		if !SupportsReasoningUpdates(model) {
			t.Errorf("%s should support reasoning updates", model)
		}
		if len(ReasoningLevels(model)) == 0 {
			t.Errorf("%s has no reasoning levels", model)
		}
	}
	if SupportsReasoningUpdates("unknown-model") {
		t.Fatal("unknown model advertises reasoning updates")
	}
	if !SupportsReasoningUpdates("gpt-6.1-sol(high)") {
		t.Fatal("model suffix should not hide reasoning update support")
	}
	if len(ReasoningLevels("gpt-6.1-sol(high)")) == 0 {
		t.Fatal("model suffix should not hide reasoning levels")
	}
}
