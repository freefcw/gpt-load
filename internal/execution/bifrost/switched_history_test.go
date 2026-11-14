package bifrost

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"

	"gpt-load/internal/channel"
	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
)

func switchedHistorySpec(channelID string, clientProtocol protocol.Protocol, operation execution.Operation, path, model, body string) execution.AttemptSpec {
	// Body sanitization reads only the routing identity, so the target is not resolved.
	return execution.NewAttemptSpec(execution.AttemptSpec{
		RequestID: "switch-request", AttemptID: "switch-attempt", Sequence: 1,
		ChannelID: channelID, ClientProtocol: clientProtocol, Operation: operation,
		ClientModel: model, UpstreamModel: model, Method: http.MethodPost, Path: path,
		Query: make(map[string][]string), Header: http.Header{}, Body: []byte(body),
		TargetConfig: json.RawMessage(`{}`), Credential: execution.NewCredentialSnapshot(1, 1, 1, []byte(`{"api_key":"`+testAPIKey+`"}`)),
	})
}

func TestNativePassthroughSanitizesSwitchedHistory(t *testing.T) {
	longID := "call_g_ts_" + strings.Repeat("S", 1200)

	gpt := switchedHistorySpec(string(channel.OpenAI), protocol.OpenAIResponses, execution.OperationResponsesCreate,
		"/v1/responses", "gpt-6-astra",
		`{"model":"gpt-6-astra","input":[{"type":"function_call","call_id":"`+longID+`","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"`+longID+`","output":"ok"}]}`)
	body, _, err := sanitizeNativePassthroughRequest(gpt, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range gjson.GetBytes(body, "input.#.call_id").Array() {
		if id.String() != "call_g" {
			t.Fatalf("call_id not shortened: %s", body)
		}
	}

	claude := switchedHistorySpec(string(channel.Anthropic), protocol.Anthropic, execution.OperationChatCompletion,
		"/v1/messages", "claude-opus-5-5",
		`{"model":"claude-opus-5-5","max_tokens":64,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"q"},{"role":"assistant","content":[{"type":"thinking","thinking":"from gemini"},{"type":"text","text":"a"}]},{"role":"user","content":"next"}]}`)
	body, _, err = sanitizeNativePassthroughRequest(claude, true)
	if err != nil {
		t.Fatal(err)
	}
	if blocks := gjson.GetBytes(body, "messages.1.content").Array(); len(blocks) != 1 || blocks[0].Get("type").String() != "text" {
		t.Fatalf("unsigned thinking reaches Claude: %s", body)
	}

	// Anthropic-compatible upstreams that are not Claude keep their thinking history.
	deepseek := switchedHistorySpec(string(channel.DeepSeek), protocol.Anthropic, execution.OperationChatCompletion,
		"/v1/messages", "deepseek-v4-1-flash",
		`{"model":"deepseek-v4-1-flash","max_tokens":64,"messages":[{"role":"user","content":"q"},{"role":"assistant","content":[{"type":"thinking","thinking":"mine"},{"type":"text","text":"a"}]},{"role":"user","content":"next"}]}`)
	body, _, err = sanitizeNativePassthroughRequest(deepseek, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(gjson.GetBytes(body, "messages.1.content").Array()) != 2 {
		t.Fatalf("DeepSeek thinking history removed: %s", body)
	}
}

func TestResponsesPassthroughShortensToolCallIDs(t *testing.T) {
	longID := "call_g_ts_" + strings.Repeat("S", 1200)
	spec := switchedHistorySpec(string(channel.OpenAI), protocol.OpenAIResponses, execution.OperationResponsesPassthrough,
		"/v1/responses/resp_private/vendor_action", "gpt-4o",
		`{"model":"gpt-4o","input":[{"type":"function_call","call_id":"`+longID+`","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"`+longID+`","output":"ok"}]}`)
	body, _, err := sanitizeNativePassthroughRequest(spec, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range gjson.GetBytes(body, "input.#.call_id").Array() {
		if id.String() != "call_g" {
			t.Fatalf("passthrough call_id not shortened: %s", body)
		}
	}
}

func TestConvertedOpenAIRouteShortensToolCallIDs(t *testing.T) {
	longID := "call_g_ts_" + strings.Repeat("S", 1200)
	payload := []byte(`{"model":"gpt-4o","input":[{"type":"function_call","call_id":"` + longID + `","name":"f","arguments":"{\"call_id\":\"` + longID + `\"}"},{"type":"function_call_output","call_id":"` + longID + `","output":"ok"}]}`)
	for _, provider := range []channel.ProviderKind{channel.ProviderOpenAI, channel.ProviderGroq, channel.ProviderOpenAICompatible} {
		sanitized, err := sanitizeConvertedOpenAIHistory(provider, "gpt-4o", payload)
		if err != nil {
			t.Fatal(err)
		}
		spec := execution.AttemptSpec{
			RouteMode: execution.RouteConverted, ClientProtocol: protocol.OpenAIResponses,
			Operation: execution.OperationResponsesCreate, ClientModel: "gpt-4o", UpstreamModel: "gpt-4o",
			Body: sanitized,
		}
		request, err := buildConvertedResponsesRequest(spec, schemas.OpenAI)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		ids := gjson.GetBytes(encoded, "input.#.call_id").Array()
		if len(ids) != 2 || ids[0].String() != "call_g" || ids[1].String() != "call_g" {
			t.Fatalf("%s converted call ids = %s", provider, encoded)
		}
		if !bytes.Contains(encoded, []byte(longID)) {
			t.Fatalf("%s converted request dropped the tool argument id", provider)
		}
	}
	kept, err := sanitizeConvertedOpenAIHistory(channel.ProviderAnthropic, "claude-sonnet-4-6", payload)
	if err != nil || string(kept) != string(payload) {
		t.Fatalf("non-OpenAI conversion changed history: %s", kept)
	}
	if !openAIShapedCallIDs(channel.ProviderGoogleVertex, "gpt-4o") ||
		openAIShapedCallIDs(channel.ProviderGoogleVertex, "claude-sonnet-4-6") {
		t.Fatal("vertex OpenAI wire detection mismatch")
	}
}
