package gateway

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"gpt-load/internal/execution"
	"gpt-load/internal/health"
	"gpt-load/internal/ratelimit"
)

// capacityRejectingForwarder 让 rejecting 中的 key 在处理前以 server_is_overloaded 拒绝：
// 可换号重试、不冷却也不计失败，名额随即归还；其余 key 正常完成。
type capacityRejectingForwarder struct {
	mu        sync.Mutex
	rejecting map[string]bool
	attempts  []string
}

func (forwarder *capacityRejectingForwarder) Forward(ctx context.Context, input ForwardInput) UpstreamResult {
	return forwarder.ForwardStream(ctx, input, nil)
}

func (forwarder *capacityRejectingForwarder) ForwardStream(_ context.Context, input ForwardInput, _ http.ResponseWriter) UpstreamResult {
	forwarder.mu.Lock()
	forwarder.attempts = append(forwarder.attempts, input.APIKey)
	forwarder.mu.Unlock()
	if forwarder.rejecting[input.APIKey] {
		return capacityRejectedResult()
	}
	return UpstreamResult{
		DispatchState: execution.DispatchMaybeSent, ResponseStarted: true,
		StatusCode: http.StatusOK, Committed: true,
		Stream: StreamObservation{EndReason: StreamEndCleanEOF},
	}
}

func (forwarder *capacityRejectingForwarder) takeAttempts() []string {
	forwarder.mu.Lock()
	defer forwarder.mu.Unlock()
	attempts := forwarder.attempts
	forwarder.attempts = nil
	return attempts
}

func capacityRejectedResult() UpstreamResult {
	return UpstreamResult{
		DispatchState: execution.DispatchMaybeSent, ResponseStarted: true,
		StatusCode: http.StatusServiceUnavailable,
		ExecutionError: &execution.ErrorEvidence{
			Kind: execution.ErrorKindHTTP, OriginHint: execution.ErrorOriginUpstream,
			ScopeHint: execution.ErrorScopeGroup, StatusCode: http.StatusServiceUnavailable,
			Type: "service_unavailable_error", Code: "server_is_overloaded", Summary: "rejected before generation",
			ReplaySafety: execution.ReplaySafetyRejectedBeforeProcessing,
		},
	}
}

// 健康账号都挂着长流时，快速失败的账号在途数为 0；它只应被发现一次，随后被避让。
func TestHandlerAvoidsFastFailingCredentialWhileOthersAreBusy(t *testing.T) {
	for _, test := range []struct {
		keys      int
		rejecting []int
	}{
		{keys: 3, rejecting: []int{3}},
		{keys: 10, rejecting: []int{8, 9, 10}},
	} {
		t.Run(fmt.Sprintf("%d_of_%d_rejecting", len(test.rejecting), test.keys), func(t *testing.T) {
			keys := make([]string, test.keys)
			for index := range keys {
				keys[index] = fmt.Sprintf("sk-%02d", index+1)
			}
			forwarder := &capacityRejectingForwarder{rejecting: make(map[string]bool)}
			for _, position := range test.rejecting {
				forwarder.rejecting[keys[position-1]] = true
			}
			engine, handler, _, _ := newStatsHandlerTestRuntime(t, forwarder, keys...)
			limiter := ratelimit.NewCredentialLimiter()
			handler.credentialLimiter = limiter
			// 测试夹具按 key 顺序分配凭据 ID 1..n。
			for index, key := range keys {
				if !forwarder.rejecting[key] {
					limiter.Acquire(uint(index+1), 0, 0)
				}
			}
			rejectedAttempts := 0
			for request := range 20 {
				httpRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
					bytes.NewBufferString(`{"model":"gpt-4o","stream":true}`))
				httpRequest.Header.Set("Authorization", "Bearer gl-client")
				recorder := httptest.NewRecorder()
				engine.ServeHTTP(recorder, httpRequest)
				attempts := forwarder.takeAttempts()
				for _, key := range attempts {
					if forwarder.rejecting[key] {
						rejectedAttempts++
					}
				}
				// 前两个请求可能用于发现快速失败的账号；之后必须一次命中健康账号。
				if request >= 2 && (recorder.Code != http.StatusOK || len(attempts) != 1) {
					t.Fatalf("request %d: status=%d attempts=%v", request, recorder.Code, attempts)
				}
			}
			if rejectedAttempts != len(test.rejecting) {
				t.Fatalf("attempts on rejecting credentials = %d, want each discovered once (%d)", rejectedAttempts, len(test.rejecting))
			}
		})
	}
}

func TestFailureAvoidsCredentialFollowsCandidateAttribution(t *testing.T) {
	for _, test := range []struct {
		name      string
		result    UpstreamResult
		operation execution.Operation
		method    string
		wantRule  health.RuleID
		want      bool
	}{
		{
			name: "capacity rejection", result: capacityRejectedResult(),
			operation: execution.OperationChatCompletion, method: http.MethodPost,
			wantRule: "candidate.transient_capacity", want: true,
		},
		{
			name: "websocket capacity without replay",
			result: UpstreamResult{DispatchState: execution.DispatchMaybeSent, ExecutionError: &execution.ErrorEvidence{
				Kind: execution.ErrorKindProvider, OriginHint: execution.ErrorOriginUpstream,
				Hint: execution.FailureHintCandidateUnavailable, ScopeHint: execution.ErrorScopeModel,
				Code: "turn_rate_limit_exceeded", ReplaySafety: execution.ReplaySafetyUnknown,
			}},
			operation: execution.OperationResponsesCreate, method: http.MethodPost,
			wantRule: "safety.replay_unknown", want: true,
		},
		{
			name: "resource lookup",
			result: UpstreamResult{DispatchState: execution.DispatchMaybeSent, StatusCode: http.StatusNotFound,
				ExecutionError: &execution.ErrorEvidence{
					Kind: execution.ErrorKindHTTP, OriginHint: execution.ErrorOriginUpstream,
					Hint: execution.FailureHintModelUnavailable, StatusCode: http.StatusNotFound,
				}},
			operation: execution.OperationResponsesRetrieve, method: http.MethodGet,
			wantRule: "model.resource_unavailable", want: false,
		},
		{
			name: "request rejection",
			result: UpstreamResult{DispatchState: execution.DispatchMaybeSent, StatusCode: http.StatusBadRequest,
				ExecutionError: &execution.ErrorEvidence{
					Kind: execution.ErrorKindHTTP, OriginHint: execution.ErrorOriginUpstream,
					Hint: execution.FailureHintRequestRejected, StatusCode: http.StatusBadRequest,
				}},
			operation: execution.OperationChatCompletion, method: http.MethodPost,
			wantRule: "fallback.http_client_error", want: false,
		},
		{
			name: "unsupported conversion",
			result: UpstreamResult{DispatchState: execution.DispatchNotSent, ExecutionError: &execution.ErrorEvidence{
				Kind: execution.ErrorKindConversionUnsupported, OriginHint: execution.ErrorOriginInternal,
			}},
			operation: execution.OperationChatCompletion, method: http.MethodPost,
			wantRule: "conversion.not_sent", want: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision := judgeUpstreamResult(test.result, time.Now(), health.DecisionContext{
				Method: test.method, Operation: test.operation,
			})
			if decision.RuleID != test.wantRule {
				t.Fatalf("rule = %q, want %q", decision.RuleID, test.wantRule)
			}
			if got := decision.FailureAvoidanceScope(test.operation) != ""; got != test.want {
				t.Fatalf("FailureAvoidanceScope(%q) active = %t, want %t", decision.RuleID, got, test.want)
			}
		})
	}
}
