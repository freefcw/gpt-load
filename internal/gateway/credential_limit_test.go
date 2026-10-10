package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"gpt-load/internal/channel"
	"gpt-load/internal/execution"
	"gpt-load/internal/platform/config"
	"gpt-load/internal/ratelimit"
	"gpt-load/internal/state"
)

// NewHandler 默认接入凭据本地限额：单凭据 RPM=1 时第二次请求返回 429。
func TestHandlerLocalCredentialRPMRejectsWith429(t *testing.T) {
	forwarder := &scriptedForwarder{results: []UpstreamResult{{
		StatusCode: http.StatusOK, Header: make(http.Header),
		Body: []byte(`{"id":"ok","model":"gpt-4o","choices":[]}`),
	}}}
	engine, handler, manager, registry := newRequestLogHandlerTestRuntime(
		t, forwarder, &recordingAccessKeyRPMLimiter{}, &recordingRequestLogSink{}, "sk-one",
	)
	publishLimitedCredentials(t, handler, manager, registry, 1, 0)

	if recorder := serveChat(engine); recorder.Code != http.StatusOK {
		t.Fatalf("first status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	recorder := serveChat(engine)
	assertGatewayReasonTest(t, recorder, http.StatusTooManyRequests, reasonCredentialRateLimited.Code)
	if recorder.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After header missing on credential rate limit")
	}
}

// 两个凭据各限并发 1：第一个被占满后调度器必须换到第二个，而不是 429。
func TestHandlerSwitchesCredentialWhenLocalLimitIsReached(t *testing.T) {
	limiter := ratelimit.NewCredentialLimiter()
	forwarder := &scriptedForwarder{results: []UpstreamResult{{
		StatusCode: http.StatusOK, Header: make(http.Header),
		Body: []byte(`{"id":"ok","model":"gpt-4o","choices":[]}`),
	}}}
	engine, handler, manager, registry := newRequestLogHandlerTestRuntime(
		t, forwarder, &recordingAccessKeyRPMLimiter{}, &recordingRequestLogSink{}, "sk-one", "sk-two",
	)
	handler.credentialLimiter = limiter
	publishLimitedCredentials(t, handler, manager, registry, 0, 1)

	// 先把凭据 1 的唯一并发名额占住。
	holdOne, ok := limiter.Acquire(1, 0, 1)
	if !ok {
		t.Fatal("could not pre-occupy credential 1")
	}
	defer holdOne()

	recorder := serveChat(engine)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if len(forwarder.inputs) != 1 || forwarder.inputs[0].Credential.ID != 2 {
		t.Fatalf("expected forward via credential 2, got inputs=%d", len(forwarder.inputs))
	}
	if limiter.InFlightForTest(2) != 0 {
		t.Fatal("credential 2 concurrency slot leaked after request completion")
	}
}

// 所有候选都满额且没有任何尝试发出时，答 429 而不是 503。
func TestHandlerReturns429WhenEveryCredentialIsLocallyLimited(t *testing.T) {
	limiter := ratelimit.NewCredentialLimiter()
	engine, handler, manager, registry := newRequestLogHandlerTestRuntime(
		t, &scriptedForwarder{}, &recordingAccessKeyRPMLimiter{}, &recordingRequestLogSink{}, "sk-one",
	)
	handler.credentialLimiter = limiter
	publishLimitedCredentials(t, handler, manager, registry, 1, 0)

	if _, ok := limiter.Acquire(1, 1, 0); !ok {
		t.Fatal("could not exhaust credential 1 rpm")
	}
	recorder := serveChat(engine)
	assertGatewayReasonTest(t, recorder, http.StatusTooManyRequests, reasonCredentialRateLimited.Code)
	if recorder.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After header missing on credential rate limit")
	}
}

// RPM 记账发生在调度时，不看上游结果：上游失败的那次也消耗名额。
func TestHandlerChargesCredentialRPMEvenWhenUpstreamFails(t *testing.T) {
	limiter := ratelimit.NewCredentialLimiter()
	forwarder := &scriptedForwarder{results: []UpstreamResult{{
		StatusCode: http.StatusInternalServerError, Header: make(http.Header),
		Body: []byte(`{"error":{"message":"boom"}}`),
	}}}
	engine, handler, manager, registry := newRequestLogHandlerTestRuntime(
		t, forwarder, &recordingAccessKeyRPMLimiter{}, &recordingRequestLogSink{}, "sk-one",
	)
	handler.credentialLimiter = limiter
	publishLimitedCredentials(t, handler, manager, registry, 1, 0)

	serveChat(engine)
	if limiter.Available(1, 1, 0) {
		t.Fatal("failed upstream attempt did not consume the credential rpm slot")
	}
}

// 换号重试时，前一个凭据的并发名额必须在下一次转发前归还。
func TestHandlerReleasesCredentialSlotBeforeCredentialSwitch(t *testing.T) {
	limiter := ratelimit.NewCredentialLimiter()
	forwarder := &scriptedForwarder{results: []UpstreamResult{
		{
			StatusCode:         http.StatusUnauthorized,
			Header:             make(http.Header),
			Body:               []byte(`{"error":{"message":"invalid key"}}`),
			ClassificationBody: []byte(`{"error":{"message":"invalid key"}}`),
			ErrorSummary:       "invalid key",
			RequestWritten:     true,
		},
		{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body:           []byte(`{"id":"ok","model":"gpt-4o","choices":[]}`),
			RequestWritten: true,
		},
	}}
	engine, handler, manager, registry := newRequestLogHandlerTestRuntime(
		t, forwarder, &recordingAccessKeyRPMLimiter{}, &recordingRequestLogSink{}, "sk-one", "sk-two",
	)
	handler.credentialLimiter = limiter
	publishLimitedCredentials(t, handler, manager, registry, 0, 1)

	var firstCredential uint
	forwarder.onCall = func(index int) {
		if index == 0 {
			firstCredential = forwarder.inputs[0].Credential.ID
			return
		}
		if got := limiter.InFlightForTest(firstCredential); got != 0 {
			t.Fatalf("credential %d in-flight = %d during retry, want released to 0", firstCredential, got)
		}
	}

	recorder := serveChat(engine)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if len(forwarder.inputs) != 2 || forwarder.inputs[1].Credential.ID == firstCredential {
		t.Fatalf("expected retry to switch credential, inputs=%d first=%d", len(forwarder.inputs), firstCredential)
	}
}

// 订阅凭据 401 强制刷新后用同一凭据重放：并发上限 1 时重放名额必须先释放再申请。
func TestSubscriptionRefreshReplayWithCredentialConcurrencyOne(t *testing.T) {
	forwarder := &scriptedForwarder{results: []UpstreamResult{
		{
			DispatchState: execution.DispatchMaybeSent, ResponseStarted: true,
			StatusCode: http.StatusUnauthorized,
			ExecutionError: &execution.ErrorEvidence{
				Kind: execution.ErrorKindHTTP, StatusCode: http.StatusUnauthorized,
				Hint:         execution.FailureHintRefreshRequired,
				ReplaySafety: execution.ReplaySafetyRejectedBeforeProcessing,
				Summary:      "access token expired",
			},
		},
		{
			DispatchState: execution.DispatchMaybeSent, ResponseStarted: true,
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: []byte(`{"id":"ok","model":"gpt-4o"}`),
		},
	}}
	handler, manager, registry := newHandlerForTest(t, forwarder, "placeholder")
	if _, err := manager.Publish(state.CompileInput{
		SystemSettings:  config.Settings{state.SettingRetryCount: 1},
		ChannelRegistry: channel.NewRegistry(),
		Groups: []state.GroupConfig{{
			ID: 1, Name: "subscription", ChannelID: channel.Codex, Priority: 100,
			Settings:       config.Settings{state.SettingRetryCount: 0},
			ConnectionType: "subscription", Params: json.RawMessage(`{}`),
			Models: []state.ModelConfig{{ID: "gpt-4o"}}, Enabled: true,
		}},
		Credentials: []state.CredentialConfig{{
			ID: 1, GroupID: 1, Status: state.CredentialStatusActive,
			Version: 1, IdentityGeneration: 1, Fingerprint: "subscription-account",
		}},
		AccessKeys: []state.AccessKeyConfig{{
			ID: 1, Name: "client", KeyHash: handler.encryption.Hash("gl-client"),
			Status: state.AccessKeyStatusActive,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	canonical := `{"type":"codex","access_token":"access","refresh_token":"refresh","account_id":"account-1"}`
	encrypted, err := handler.encryption.Encrypt(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceCredentials([]state.CredentialEntry{{
		ID: 1, GroupID: 1, Version: 1, IdentityGeneration: 1,
		Fingerprint: "subscription-account", Status: state.CredentialStatusActive,
		EncryptedValue: encrypted, ConcurrencyLimit: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	bindGatewayRoutesForTest(t, engine, handler)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-4o"}`))
	request.Header.Set("Authorization", "Bearer gl-client")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	if response.Code != http.StatusOK || len(forwarder.inputs) != 2 {
		t.Fatalf("status=%d inputs=%d body=%s", response.Code, len(forwarder.inputs), response.Body.String())
	}
	if forwarder.inputs[0].Credential.ID != forwarder.inputs[1].Credential.ID ||
		forwarder.inputs[0].ForceCredentialRefresh || !forwarder.inputs[1].ForceCredentialRefresh {
		t.Fatalf("inputs = %#v", forwarder.inputs)
	}
}

// 本地限额满额时 websocket turn 返回 credential_rate_limited 错误事件。
func TestWebsocketTurnReturnsCredentialRateLimited(t *testing.T) {
	handler, engine, _ := websocketTestHandler(t, "http://127.0.0.1:1/v1", channel.OpenAI)
	registry := handler.registry.(*state.CredentialRegistry)
	entries, err := registry.SnapshotGroupCredentialEntriesExact(1, []uint{1})
	if err != nil {
		t.Fatal(err)
	}
	entries[0].RPMLimit = 1
	if err := registry.RestoreGroupCredentialEntriesExact(1, entries); err != nil {
		t.Fatal(err)
	}
	if _, ok := handler.credentialLimiter.Acquire(1, 1, 0); !ok {
		t.Fatal("could not exhaust credential 1 rpm")
	}
	server := httptest.NewServer(engine)
	defer server.Close()
	conn := dialGatewayWebsocket(t, server.URL)
	defer conn.Close()
	if err := conn.WriteJSON(map[string]any{"type": "response.create", "model": "public", "input": "test"}); err != nil {
		t.Fatal(err)
	}
	var event struct {
		Status int `json:"status"`
		Error  struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := conn.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Status != http.StatusTooManyRequests || event.Error.Code != reasonCredentialRateLimited.Code {
		t.Fatalf("event = %+v, want 429 %s", event, reasonCredentialRateLimited.Code)
	}
}

func publishLimitedCredentials(
	t *testing.T,
	handler *Handler,
	manager *state.Manager,
	registry *state.CredentialRegistry,
	rpm, concurrency int64,
) {
	t.Helper()
	views := registry.Snapshot()
	entries, err := registry.SnapshotGroupCredentialEntriesExact(1, credentialIDsOf(views))
	if err != nil {
		t.Fatal(err)
	}
	for index := range entries {
		entries[index].RPMLimit = rpm
		entries[index].ConcurrencyLimit = concurrency
	}
	if err := registry.RestoreGroupCredentialEntriesExact(1, entries); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(state.CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		Groups: []state.GroupConfig{{ConnectionType: "api_key", ID: 1, Name: "openai", ChannelID: channel.OpenAI,
			Params: json.RawMessage(`{}`), Models: []state.ModelConfig{{ID: "gpt-4o"}}, Enabled: true,
		}},
		Credentials: credentialConfigsOf(views),
		AccessKeys: []state.AccessKeyConfig{{
			ID: 1, Name: "client", KeyHash: handler.encryption.Hash("gl-client"), Status: state.AccessKeyStatusActive,
		}},
	}); err != nil {
		t.Fatal(err)
	}
}

func credentialIDsOf(views []state.CredentialRuntimeView) []uint {
	ids := make([]uint, 0, len(views))
	for _, view := range views {
		ids = append(ids, view.ID)
	}
	return ids
}

func credentialConfigsOf(views []state.CredentialRuntimeView) []state.CredentialConfig {
	configs := make([]state.CredentialConfig, 0, len(views))
	for _, view := range views {
		configs = append(configs, testCredentialConfig(view.ID, view.GroupID))
	}
	return configs
}

func serveChat(engine interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer gl-client")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}
