package embedded

import (
	"encoding/base64"
	"testing"
)

func TestCodexCredentialPlanDerivesWithoutMutatingCanonicalInput(t *testing.T) {
	token := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_plan_type":"plus"}}`)) + ".signature"
	raw := []byte(`{"type":"codex","id_token":"` + token + `","access_token":"access","refresh_token":"refresh","account_id":"account"}`)
	credential, err := ParseCodexCredentialJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if credential.PlanType != "" {
		t.Fatalf("parsed credential persisted derived plan %q", credential.PlanType)
	}
	if got := CodexCredentialPlan(credential); got != "plus" {
		t.Fatalf("derived plan = %q, want plus", got)
	}
}

func TestCodexCredentialPlanPrefersExplicitPlan(t *testing.T) {
	token := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_plan_type":"plus"}}`)) + ".signature"
	if got := CodexCredentialPlan(CodexCredential{PlanType: "pro", AccessToken: token}); got != "pro" {
		t.Fatalf("explicit plan = %q, want pro", got)
	}
}
