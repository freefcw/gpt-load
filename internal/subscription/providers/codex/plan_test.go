package codex

import (
	"encoding/base64"
	"testing"
)

func TestCodexRuntimeCredentialDerivesPlanWithoutChangingCanonical(t *testing.T) {
	token := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_plan_type":"plus"}}`)) + ".signature"
	raw := []byte(`{"type":"codex","id_token":"` + token + `","access_token":"access","refresh_token":"refresh","account_id":"account"}`)
	value, err := ParseCredentialJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if value.PlanType != "" {
		t.Fatalf("plan derivation changed persisted credential metadata: %q", value.PlanType)
	}
	account := codexRuntimeCredential(value, raw).Account()
	if account.PlanType != "plus" {
		t.Fatalf("runtime plan = %q, want plus", account.PlanType)
	}
}
