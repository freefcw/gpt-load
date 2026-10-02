package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gpt-load/internal/channel"
	"gpt-load/internal/dialect"
	"gpt-load/internal/platform/config"
	"gpt-load/internal/state"
)

func publishDataPlaneLimitTestConfig(t *testing.T, handler *Handler, manager *state.Manager, settings config.Settings) {
	t.Helper()
	_, err := manager.Publish(state.CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		SystemSettings:  settings,
		Groups: []state.GroupConfig{{
			ConnectionType: "api_key", ID: 1, Name: "openai", ChannelID: channel.OpenAI,
			Params: []byte("{}"), Models: []state.ModelConfig{{ID: "gpt-4o"}}, Enabled: true,
		}},
		Credentials: []state.CredentialConfig{testCredentialConfig(1, 1)},
		AccessKeys: []state.AccessKeyConfig{{
			ID: 1, Name: "client", KeyHash: handler.encryption.Hash("gl-client"), Status: state.AccessKeyStatusActive,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGlobalConcurrencyRejectsBeforeForwardAndReleases(t *testing.T) {
	forwarder := &scriptedForwarder{results: []UpstreamResult{{StatusCode: http.StatusOK, Header: make(http.Header), Body: []byte("{\"ok\":true}")}}}
	engine, handler, manager, _ := newRequestLogHandlerTestRuntime(t, forwarder, &recordingAccessKeyRPMLimiter{}, &recordingRequestLogSink{}, "sk-upstream", "sk-upstream-two")
	publishDataPlaneLimitTestConfig(t, handler, manager, config.Settings{state.SettingGlobalConcurrencyLimit: json.Number("1")})
	held, ok := handler.dataPlane.AcquireGlobal(1)
	if !ok {
		t.Fatal("failed to occupy global slot")
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString("{\"model\":\"gpt-4o\"}"))
	request.Header.Set("Authorization", "Bearer gl-client")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests || len(forwarder.inputs) != 0 {
		t.Fatalf("response=%d forward_calls=%d", response.Code, len(forwarder.inputs))
	}
	held()
	request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString("{\"model\":\"gpt-4o\"}"))
	request.Header.Set("Authorization", "Bearer gl-client")
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("released global slot response=%d body=%s", response.Code, response.Body.String())
	}
	if snapshot := handler.dataPlane.Snapshot(); snapshot.Global != 0 {
		t.Fatalf("global count leaked: %+v", snapshot)
	}
}

func TestGroupConcurrencyRejectsWithoutFallbackAndReleases(t *testing.T) {
	forwarder := &scriptedForwarder{results: []UpstreamResult{{StatusCode: http.StatusOK, Header: make(http.Header), Body: []byte("{\"ok\":true}")}}}
	engine, handler, manager, _ := newRequestLogHandlerTestRuntime(t, forwarder, &recordingAccessKeyRPMLimiter{}, &recordingRequestLogSink{}, "sk-upstream")
	_, err := manager.Publish(state.CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		Groups:          []state.GroupConfig{{ConnectionType: "api_key", ID: 1, Name: "openai", ChannelID: channel.OpenAI, Params: []byte("{}"), Models: []state.ModelConfig{{ID: "gpt-4o"}}, Settings: config.Settings{state.SettingConcurrencyLimit: json.Number("1")}, Enabled: true}},
		Credentials:     []state.CredentialConfig{testCredentialConfig(1, 1)},
		AccessKeys:      []state.AccessKeyConfig{{ID: 1, Name: "client", KeyHash: handler.encryption.Hash("gl-client"), Status: state.AccessKeyStatusActive}},
	})
	if err != nil {
		t.Fatal(err)
	}
	held, ok := handler.dataPlane.AcquireGroup(1, 1)
	if !ok {
		t.Fatal("failed to occupy group slot")
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString("{\"model\":\"gpt-4o\"}"))
	request.Header.Set("Authorization", "Bearer gl-client")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests || len(forwarder.inputs) != 0 {
		t.Fatalf("response=%d forward_calls=%d", response.Code, len(forwarder.inputs))
	}
	held()
	if snapshot := handler.dataPlane.Snapshot(); snapshot.Groups[1] != 0 {
		t.Fatalf("group count leaked: %+v", snapshot)
	}
}

func TestGroupConcurrencyReleasesBetweenRetries(t *testing.T) {
	invalid := UpstreamResult{
		StatusCode: http.StatusTooManyRequests, Header: make(http.Header), RequestWritten: true,
		Body: []byte("{\"error\":\"rate_limited\"}"), ClassificationBody: []byte("{\"error\":\"rate_limited\"}"),
	}
	forwarder := &scriptedForwarder{results: []UpstreamResult{
		invalid,
		{StatusCode: http.StatusOK, Header: make(http.Header), Body: []byte("{\"ok\":true}")},
	}}
	engine, handler, manager, _ := newRequestLogHandlerTestRuntime(t, forwarder, &recordingAccessKeyRPMLimiter{}, &recordingRequestLogSink{}, "sk-upstream", "sk-upstream-two")
	_, err := manager.Publish(state.CompileInput{
		SystemSettings: config.Settings{state.SettingRetryCount: json.Number("1")}, ChannelRegistry: channel.NewRegistry(),
		Groups:      []state.GroupConfig{{ConnectionType: "api_key", ID: 1, Name: "openai", ChannelID: channel.OpenAI, Params: []byte("{}"), Models: []state.ModelConfig{{ID: "gpt-4o"}}, Settings: config.Settings{state.SettingConcurrencyLimit: json.Number("1")}, Enabled: true}},
		Credentials: []state.CredentialConfig{testCredentialConfig(1, 1), testCredentialConfig(2, 1)},
		AccessKeys:  []state.AccessKeyConfig{{ID: 1, Name: "client", KeyHash: handler.encryption.Hash("gl-client"), Status: state.AccessKeyStatusActive}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString("{\"model\":\"gpt-4o\"}"))
	request.Header.Set("Authorization", "Bearer gl-client")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(forwarder.inputs) != 2 {
		t.Fatalf("response=%d attempts=%d body=%s", response.Code, len(forwarder.inputs), response.Body.String())
	}
	if snapshot := handler.dataPlane.Snapshot(); snapshot.Groups[1] != 0 {
		t.Fatalf("group count leaked after retry: %+v", snapshot)
	}
}

// forward panic 被应用层恢复后，group 名额必须经 defer 兜底释放，不得永久 +1。
func TestGroupConcurrencyReleasedWhenForwardPanics(t *testing.T) {
	forwarder := &scriptedForwarder{onCall: func(int) { panic("forward exploded") }}
	engine, handler, manager, _ := newRequestLogHandlerTestRuntime(t, forwarder, &recordingAccessKeyRPMLimiter{}, &recordingRequestLogSink{}, "sk-upstream")
	_, err := manager.Publish(state.CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		Groups:          []state.GroupConfig{{ConnectionType: "api_key", ID: 1, Name: "openai", ChannelID: channel.OpenAI, Params: []byte("{}"), Models: []state.ModelConfig{{ID: "gpt-4o"}}, Settings: config.Settings{state.SettingConcurrencyLimit: json.Number("1")}, Enabled: true}},
		Credentials:     []state.CredentialConfig{testCredentialConfig(1, 1)},
		AccessKeys:      []state.AccessKeyConfig{{ID: 1, Name: "client", KeyHash: handler.encryption.Hash("gl-client"), Status: state.AccessKeyStatusActive}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString("{\"model\":\"gpt-4o\"}"))
	request.Header.Set("Authorization", "Bearer gl-client")
	panicked := false
	func() {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		engine.ServeHTTP(httptest.NewRecorder(), request)
	}()
	if !panicked || len(forwarder.inputs) != 1 {
		t.Fatalf("panicked=%v forward_calls=%d", panicked, len(forwarder.inputs))
	}
	if snapshot := handler.dataPlane.Snapshot(); snapshot.Groups[1] != 0 || snapshot.Global != 0 {
		t.Fatalf("concurrency leaked after forward panic: %+v", snapshot)
	}
}

// cancel 请求豁免三类并发名额：名额全部占满时仍应放行；普通请求照常受限。
func TestCancelRequestExemptFromConcurrencyLimits(t *testing.T) {
	forwarder := &scriptedForwarder{results: []UpstreamResult{{StatusCode: http.StatusOK, Header: make(http.Header), Body: []byte("{\"id\":\"resp_1\",\"status\":\"cancelled\"}")}}}
	engine, handler, manager, _ := newRequestLogHandlerTestRuntime(t, forwarder, &recordingAccessKeyRPMLimiter{}, &recordingRequestLogSink{}, "sk-upstream")
	handler.dialects = dialect.NewSet(dialect.NewOpenAI(), dialect.NewOpenAIResponses())
	limiter := &recordingAccessKeyConcurrencyLimiter{reject: true}
	handler.concurrency = limiter
	_, err := manager.Publish(state.CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		SystemSettings:  config.Settings{state.SettingGlobalConcurrencyLimit: json.Number("1")},
		Groups:          []state.GroupConfig{{ConnectionType: "api_key", ID: 1, Name: "openai", ChannelID: channel.OpenAI, Params: []byte("{}"), Models: []state.ModelConfig{{ID: "gpt-4o"}}, Settings: config.Settings{state.SettingConcurrencyLimit: json.Number("1")}, Enabled: true}},
		Credentials:     []state.CredentialConfig{testCredentialConfig(1, 1)},
		AccessKeys: []state.AccessKeyConfig{{
			ID: 1, Name: "client", KeyHash: handler.encryption.Hash("gl-client"),
			Status: state.AccessKeyStatusActive, ConcurrencyLimit: 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	heldGlobal, ok := handler.dataPlane.AcquireGlobal(1)
	if !ok {
		t.Fatal("failed to occupy global slot")
	}
	defer heldGlobal()
	heldGroup, ok := handler.dataPlane.AcquireGroup(1, 1)
	if !ok {
		t.Fatal("failed to occupy group slot")
	}
	defer heldGroup()

	request := httptest.NewRequest(http.MethodPost, "/v1/responses/resp_1/cancel", strings.NewReader("{}"))
	request.Header.Set("Authorization", "Bearer gl-client")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(forwarder.inputs) != 1 {
		t.Fatalf("cancel response=%d forward_calls=%d body=%s", response.Code, len(forwarder.inputs), response.Body.String())
	}
	if calls, _ := limiter.snapshot(); len(calls) != 0 {
		t.Fatalf("cancel request acquired access-key concurrency: %#v", calls)
	}
	if snapshot := handler.dataPlane.Snapshot(); snapshot.Global != 1 || snapshot.Groups[1] != 1 {
		t.Fatalf("cancel request touched data-plane slots: %+v", snapshot)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString("{\"model\":\"gpt-4o\"}"))
	request.Header.Set("Authorization", "Bearer gl-client")
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	assertGatewayReasonTest(t, response, http.StatusTooManyRequests, reasonAccessKeyConcurrencyLimited.Code)
}
