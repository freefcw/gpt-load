package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gpt-load/internal/ratelimit"
	"gpt-load/internal/state"
)

func TestCodexLiveLocalPreparationDoesNotChargeCredential(t *testing.T) {
	for _, outcome := range []string{"invalid_media_offer", "relay_cancellation", "direct_cancellation"} {
		t.Run(outcome, func(t *testing.T) {
			fake := &liveFakeOpener{}
			handler, engine, sink, registry, _ := liveGatewayFixture(t, fake)
			if outcome == "direct_cancellation" {
				setLiveGroupMode(handler, 1, state.CodexLiveDirect)
				setLiveGroupMode(handler, 2, state.CodexLiveDirect)
			}
			// Select credential 1 deterministically, with another candidate
			// available to catch accidental retries of a local setup failure.
			group := handler.manager.Current().Groups[1]
			group.Priority = 100
			handler.manager.Current().Groups[1] = group
			limiter := ratelimit.NewCredentialLimiter()
			handler.credentialLimiter = limiter
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			preparations := 0
			handler.encryption = &reservationPreparationEncryption{
				Service: handler.encryption,
				beforeDecrypt: func() error {
					preparations++
					assertGatewayReservationUsage(t, registry, limiter, 1, 1, 0, 0)
					if outcome != "invalid_media_offer" {
						cancel()
					}
					return nil
				},
			}
			offer := "not a valid SDP offer"
			if outcome != "invalid_media_offer" {
				_, offer = liveClientOffer(t)
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/live", strings.NewReader(offer)).WithContext(ctx)
			request.Header.Set("Authorization", "Bearer gl-client")
			request.Header.Set("Content-Type", "application/sdp")
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)

			assertGatewayReasonTest(t, response, reasonLiveUnavailable.Status, reasonLiveUnavailable.Code)
			fake.mu.Lock()
			attempts := len(fake.attempted)
			fake.mu.Unlock()
			if preparations != 1 || attempts != 0 {
				t.Fatalf("preparations=%d OpenLive calls=%d, want 1/0", preparations, attempts)
			}
			for _, id := range []uint{1, 2} {
				assertGatewayReservationUsage(t, registry, limiter, id, 0, 0, 0)
			}
			if counts := handler.manager.Concurrency().Snapshot(); counts.Global != 0 || len(counts.Groups) != 0 || len(counts.AccessKeys) != 0 {
				t.Fatalf("local preparation leaked concurrency: %+v", counts)
			}
			events := sink.snapshot()
			if len(events) != 1 || len(events[0].Attempts) != 0 {
				t.Fatalf("preparation should log one request without upstream attempts: %+v", events)
			}
		})
	}
}
