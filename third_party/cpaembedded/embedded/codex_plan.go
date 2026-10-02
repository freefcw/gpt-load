package embedded

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// CodexCredentialPlan derives presentation-only plan metadata without
// rewriting the canonical credential bytes used for identity fingerprints.
func CodexCredentialPlan(c CodexCredential) string {
	if plan := safeCodexPlan(c.PlanType); plan != "" {
		return plan
	}
	for _, token := range []string{c.IDToken, c.AccessToken} {
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			continue
		}
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			continue
		}
		var claims struct {
			Auth struct {
				Plan string `json:"chatgpt_plan_type"`
			} `json:"https://api.openai.com/auth"`
		}
		err = json.Unmarshal(payload, &claims)
		clear(payload)
		if err == nil {
			if plan := safeCodexPlan(claims.Auth.Plan); plan != "" {
				return plan
			}
		}
	}
	return safeCodexPlan(c.PlanType)
}

func safeCodexPlan(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) > 64 {
		return ""
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '_' || char == '-') {
			return ""
		}
	}
	return value
}
