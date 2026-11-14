package dialect

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func decodeTestJSON(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return object
}

func TestSanitizeClaudeThinkingHistory(t *testing.T) {
	const signed = `{"type":"thinking","thinking":"mine","signature":"EqQBsig"}`
	const unsigned = `{"type":"thinking","thinking":"from gemini"}`
	const emptySigned = `{"type":"thinking","thinking":"from gpt","signature":""}`
	const redacted = `{"type":"redacted_thinking"}`
	const toolUse = `{"type":"tool_use","id":"toolu_1","name":"f","input":{}}`
	const toolResult = `{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}`
	for _, test := range []struct {
		name, body, want string
		changed          bool
	}{
		{
			name:    "signed thinking is kept",
			body:    `{"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"q"},{"role":"assistant","content":[` + signed + `,{"type":"text","text":"a"}]}]}`,
			changed: false,
		},
		{
			name:    "unsigned thinking in an earlier turn is removed",
			body:    `{"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"q"},{"role":"assistant","content":[` + unsigned + `,{"type":"text","text":"a"}]},{"role":"user","content":"next"}]}`,
			want:    `{"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"q"},{"role":"assistant","content":[{"type":"text","text":"a"}]},{"role":"user","content":"next"}]}`,
			changed: true,
		},
		{
			name:    "assistant turn left empty is removed",
			body:    `{"messages":[{"role":"user","content":"q"},{"role":"assistant","content":[` + emptySigned + `,` + redacted + `]},{"role":"user","content":"next"}]}`,
			want:    `{"messages":[{"role":"user","content":"q"},{"role":"user","content":"next"}]}`,
			changed: true,
		},
		{
			name:    "final tool turn disables thinking",
			body:    `{"thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"q"},{"role":"assistant","content":[` + unsigned + `,` + toolUse + `]},` + toolResult + `]}`,
			want:    `{"messages":[{"role":"user","content":"q"},{"role":"assistant","content":[` + toolUse + `]},` + toolResult + `]}`,
			changed: true,
		},
		{
			name:    "earlier tool turn keeps thinking enabled",
			body:    `{"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"q"},{"role":"assistant","content":[` + unsigned + `,` + toolUse + `]},` + toolResult + `,{"role":"assistant","content":[` + signed + `,{"type":"text","text":"done"}]},{"role":"user","content":"more"}]}`,
			want:    `{"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"q"},{"role":"assistant","content":[` + toolUse + `]},` + toolResult + `,{"role":"assistant","content":[` + signed + `,{"type":"text","text":"done"}]},{"role":"user","content":"more"}]}`,
			changed: true,
		},
		{
			name:    "string content is untouched",
			body:    `{"messages":[{"role":"user","content":"q"},{"role":"assistant","content":"a"}]}`,
			changed: false,
		},
		{
			name:    "split final tool turn disables thinking",
			body:    `{"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"q"},{"role":"assistant","content":[` + unsigned + `]},{"role":"assistant","content":[` + toolUse + `]},` + toolResult + `]}`,
			want:    `{"messages":[{"role":"user","content":"q"},{"role":"assistant","content":[` + toolUse + `]},` + toolResult + `]}`,
			changed: true,
		},
		{
			name:    "earlier split tool turn keeps thinking enabled",
			body:    `{"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"q"},{"role":"assistant","content":[` + unsigned + `]},{"role":"assistant","content":[` + toolUse + `]},` + toolResult + `,{"role":"assistant","content":[{"type":"text","text":"done"}]},{"role":"user","content":"more"}]}`,
			want:    `{"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"q"},{"role":"assistant","content":[` + toolUse + `]},` + toolResult + `,{"role":"assistant","content":[{"type":"text","text":"done"}]},{"role":"user","content":"more"}]}`,
			changed: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			sanitized, changed, err := SanitizeClaudeThinkingHistory([]byte(test.body))
			if err != nil || changed != test.changed {
				t.Fatalf("changed=%t err=%v", changed, err)
			}
			if !changed {
				if string(sanitized) != test.body {
					t.Fatalf("unchanged body was rewritten: %s", sanitized)
				}
				return
			}
			if got, want := decodeTestJSON(t, sanitized), decodeTestJSON(t, []byte(test.want)); !reflect.DeepEqual(got, want) {
				t.Fatalf("sanitized = %s\nwant      %s", sanitized, test.want)
			}
		})
	}
}

func TestIsClaudeModel(t *testing.T) {
	for model, want := range map[string]bool{
		"claude-opus-5-5":                      true,
		"Claude-Sonnet-5":                      true,
		"anthropic/claude-haiku-5-5":           true,
		"us.anthropic.claude-opus-5-5-v1:0":    true,
		"deepseek-v4-1-flash-1017":             false,
		"kimi-k3":                              false,
		"gemini-4-argon":                       false,
		"openrouter/anthropic/claude-opus-5-5": true,
	} {
		if got := IsClaudeModel(model); got != want {
			t.Errorf("IsClaudeModel(%q) = %t, want %t", model, got, want)
		}
	}
}

func TestStripEmbeddedThoughtSignatures(t *testing.T) {
	long := "call_abc_ts_" + strings.Repeat("Q", 1200)
	short := "call_ab_ts_shortsig"
	for _, test := range []struct {
		name, body, want string
	}{
		{
			name: "responses call ids",
			body: `{"input":[{"type":"function_call","id":"fc_1","call_id":"` + long + `","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"` + long + `","output":"ok"},{"type":"function_call","call_id":"` + short + `","name":"f","arguments":"{}"}]}`,
			want: `{"input":[{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"call_abc","output":"ok"},{"type":"function_call","call_id":"` + short + `","name":"f","arguments":"{}"}]}`,
		},
		{
			name: "chat tool calls",
			body: `{"messages":[{"role":"assistant","tool_calls":[{"id":"` + long + `","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"` + long + `","content":"ok"}]}`,
			want: `{"messages":[{"role":"assistant","tool_calls":[{"id":"call_abc","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_abc","content":"ok"}]}`,
		},
		{
			name: "anthropic tool use",
			body: `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"` + long + `","name":"f","input":{"n":1.50}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + long + `","content":"ok"}]}]}`,
			want: `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_abc","name":"f","input":{"n":1.50}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_abc","content":"ok"}]}]}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			sanitized, changed, err := StripEmbeddedThoughtSignatures([]byte(test.body))
			if err != nil || !changed {
				t.Fatalf("changed=%t err=%v", changed, err)
			}
			if got, want := decodeTestJSON(t, sanitized), decodeTestJSON(t, []byte(test.want)); !reflect.DeepEqual(got, want) {
				t.Fatalf("sanitized = %s\nwant      %s", sanitized, test.want)
			}
		})
	}
	for _, body := range []string{
		`{"input":[{"type":"function_call","call_id":"` + short + `"}]}`,
		`{"input":[{"type":"message","id":"msg_ts_` + strings.Repeat("x", 100) + `","content":"a"}]}`,
		`{"input":[{"type":"function_call","call_id":"` + strings.Repeat("y", 100) + `"}]}`,
	} {
		sanitized, changed, err := StripEmbeddedThoughtSignatures([]byte(body))
		if err != nil || changed || string(sanitized) != body {
			t.Fatalf("body changed: changed=%t err=%v body=%s", changed, err, sanitized)
		}
	}
}

func TestStripEmbeddedThoughtSignaturesLeavesToolPayloads(t *testing.T) {
	long := "call_payload_ts_" + strings.Repeat("Q", 80)
	body := `{"input":[{"type":"function_call","call_id":"` + long + `","name":"f","arguments":"{\"call_id\":\"` + long + `\"}"},{"type":"function_call_output","call_id":"` + long + `","output":"{\"tool_use_id\":\"` + long + `\"}"}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"` + long + `","name":"f","input":{"call_id":"` + long + `"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + long + `","content":{"tool_call_id":"` + long + `"}}]}]}`
	sanitized, changed, err := StripEmbeddedThoughtSignatures([]byte(body))
	if err != nil || !changed {
		t.Fatalf("changed=%t err=%v", changed, err)
	}
	got := decodeTestJSON(t, sanitized)
	input := got["input"].([]any)
	call := input[0].(map[string]any)
	output := input[1].(map[string]any)
	if call["call_id"] != "call_payload" || output["call_id"] != "call_payload" {
		t.Fatalf("protocol call ids = %#v %#v", call["call_id"], output["call_id"])
	}
	if call["arguments"] != `{"call_id":"`+long+`"}` || output["output"] != `{"tool_use_id":"`+long+`"}` {
		t.Fatalf("tool payloads rewritten: %#v %#v", call["arguments"], output["output"])
	}
	messages := got["messages"].([]any)
	toolUse := messages[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	toolResult := messages[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if toolUse["id"] != "call_payload" || toolResult["tool_use_id"] != "call_payload" {
		t.Fatalf("anthropic ids = %#v %#v", toolUse["id"], toolResult["tool_use_id"])
	}
	toolInput := toolUse["input"].(map[string]any)
	toolContent := toolResult["content"].(map[string]any)
	if toolInput["call_id"] != long || toolContent["tool_call_id"] != long {
		t.Fatalf("nested tool data rewritten: %#v %#v", toolInput, toolContent)
	}
}

func TestStripEmbeddedThoughtSignaturesFitsOversizedBase(t *testing.T) {
	base := "call_" + strings.Repeat("b", 80)
	other := "call_" + strings.Repeat("c", 80)
	full := base + "_ts_" + strings.Repeat("S", 40)
	again := base + "_ts_" + strings.Repeat("T", 12)
	body := `{"input":[{"type":"function_call","call_id":"` + full + `","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"` + again + `","output":"ok"}]}`
	sanitized, changed, err := StripEmbeddedThoughtSignatures([]byte(body))
	if err != nil || !changed {
		t.Fatalf("changed=%t err=%v", changed, err)
	}
	got := decodeTestJSON(t, sanitized)
	input := got["input"].([]any)
	callID := input[0].(map[string]any)["call_id"].(string)
	outputID := input[1].(map[string]any)["call_id"].(string)
	if callID != outputID || len(callID) > 64 || callID == base || !strings.HasPrefix(callID, "id_") {
		t.Fatalf("fitted ids call=%q output=%q len=%d", callID, outputID, len(callID))
	}
	repeat, _, err := StripEmbeddedThoughtSignatures([]byte(body))
	if err != nil || string(repeat) != string(sanitized) {
		t.Fatal("shortening is not stable")
	}
	otherBody := `{"input":[{"type":"function_call","call_id":"` + other + `_ts_sig","name":"f","arguments":"{}"}]}`
	otherSanitized, _, err := StripEmbeddedThoughtSignatures([]byte(otherBody))
	if err != nil {
		t.Fatal(err)
	}
	otherID := decodeTestJSON(t, otherSanitized)["input"].([]any)[0].(map[string]any)["call_id"].(string)
	if otherID == callID || len(otherID) > 64 {
		t.Fatalf("distinct bases collapsed to %q", otherID)
	}
}
