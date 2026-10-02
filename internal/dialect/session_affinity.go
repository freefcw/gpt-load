package dialect

import (
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxSessionAffinityIDBytes = 256

// inspectSessionAffinityID accepts only explicit, bounded session headers.
// Prompt-derived affinity is handled separately and must not be promoted to
// an explicit Codex account session.
func inspectSessionAffinityID(headers http.Header) string {
	if headers == nil {
		return ""
	}
	value := strings.TrimSpace(headers.Get("Session-Id"))
	if value == "" {
		value = strings.TrimSpace(headers.Get("Session_id"))
	}
	if value == "" || len(value) > maxSessionAffinityIDBytes || !utf8.ValidString(value) {
		return ""
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return ""
		}
	}
	return value
}
