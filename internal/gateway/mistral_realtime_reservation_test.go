package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"gpt-load/internal/channel"
	"gpt-load/internal/platform/config"
	"gpt-load/internal/ratelimit"
	"gpt-load/internal/state"
)

func TestMistralRealtimePreparationDoesNotChargeCredential(t *testing.T) {
	for _, outcome := range []string{"failure", "cancellation"} {
		t.Run(outcome, func(t *testing.T) {
			var upstreamHits atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				upstreamHits.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer upstream.Close()
			handler, engine, input := websocketTestHandler(t, upstream.URL+"/v1", channel.Mistral)
			input.SystemSettings = config.Settings{state.SettingRetryCount: 3}
			if _, err := handler.manager.Publish(input); err != nil {
				t.Fatal(err)
			}
			registry := handler.registry.(*state.CredentialRegistry)
			limiter := ratelimit.NewCredentialLimiter()
			handler.credentialLimiter = limiter
			sink := &recordingRequestLogSink{}
			handler.requestLogSink = sink
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			preparations := 0
			handler.encryption = &reservationPreparationEncryption{
				Service: handler.encryption,
				beforeDecrypt: func() error {
					preparations++
					assertGatewayReservationUsage(t, registry, limiter, 1, 1, 0, 0)
					if got := handler.manager.Concurrency().Snapshot().Groups[1]; got != 0 {
						t.Fatalf("group concurrency during preparation = %d, want 0", got)
					}
					if outcome == "failure" {
						return errors.New("credential preparation failed")
					}
					cancel()
					return nil
				},
			}
			request := httptest.NewRequest(http.MethodGet, "/v1/audio/transcriptions/realtime?model=public", nil).WithContext(ctx)
			request.Header.Set("Authorization", "Bearer gl-client")
			request.Header.Set("Connection", "Upgrade")
			request.Header.Set("Upgrade", "websocket")
			request.Header.Set("Sec-WebSocket-Version", "13")
			request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)

			if outcome == "failure" {
				assertGatewayReasonTest(t, response, reasonConfigurationChanged.Status, reasonConfigurationChanged.Code)
			} else {
				assertGatewayReasonTest(t, response, http.StatusGatewayTimeout, "mistral_realtime_timeout")
			}
			if preparations != 1 || upstreamHits.Load() != 0 {
				t.Fatalf("preparations=%d upstream dials=%d, want 1/0", preparations, upstreamHits.Load())
			}
			assertGatewayReservationUsage(t, registry, limiter, 1, 0, 0, 0)
			if !limiter.Available(1, 1, 1) {
				t.Fatal("preparation consumed credential capacity")
			}
			if counts := handler.manager.Concurrency().Snapshot(); counts.Global != 0 || len(counts.Groups) != 0 || len(counts.AccessKeys) != 0 {
				t.Fatalf("preparation leaked concurrency: %+v", counts)
			}
			events := sink.snapshot()
			if len(events) != 1 || len(events[0].Attempts) != 0 {
				t.Fatalf("preparation should log one request without upstream attempts: %+v", events)
			}
		})
	}
}
