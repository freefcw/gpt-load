package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gpt-load/internal/platform/encryption"
	"gpt-load/internal/ratelimit"
	"gpt-load/internal/state"
)

// Observe preparation after selection, before any actual limiter charge.
type reservationPreparationEncryption struct {
	encryption.Service
	beforeDecrypt func() error
}

func (service *reservationPreparationEncryption) Decrypt(value string) (string, error) {
	if err := service.beforeDecrypt(); err != nil {
		return "", err
	}
	return service.Service.Decrypt(value)
}

func assertGatewayReservationUsage(t *testing.T, registry *state.CredentialRegistry, limiter *ratelimit.CredentialLimiter, id uint, pending, rpm, inFlight int64) {
	t.Helper()
	var gotPending int64
	registry.SchedulingState().WithLock(func(ledger *state.SchedulingLedger) {
		gotPending = ledger.Reservations[id]
	})
	gotRPM, gotInFlight := limiter.Usage(id)
	if gotPending != pending || gotRPM != rpm || gotInFlight != inFlight {
		t.Fatalf("credential %d: pending/RPM/inFlight = %d/%d/%d, want %d/%d/%d", id, gotPending, gotRPM, gotInFlight, pending, rpm, inFlight)
	}
}

func TestHandlerPendingReservationDuringPreparation(t *testing.T) {
	for _, outcome := range []string{"preparation_failure", "cancellation", "success"} {
		t.Run(outcome, func(t *testing.T) {
			forwarder := &scriptedForwarder{results: []UpstreamResult{{
				StatusCode: http.StatusOK, Header: make(http.Header),
				Body: []byte(`{"id":"ok","model":"gpt-4o","choices":[]}`),
			}}}
			engine, handler, manager, registry := newRequestLogHandlerTestRuntime(
				t, forwarder, &recordingAccessKeyRPMLimiter{}, &recordingRequestLogSink{}, "sk-one",
			)
			limiter := ratelimit.NewCredentialLimiter()
			handler.credentialLimiter = limiter
			publishLimitedCredentials(t, handler, manager, registry, 1, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			prepared := false
			handler.encryption = &reservationPreparationEncryption{
				Service: handler.encryption,
				beforeDecrypt: func() error {
					prepared = true
					assertGatewayReservationUsage(t, registry, limiter, 1, 1, 0, 0)
					switch outcome {
					case "preparation_failure":
						return errors.New("preparation failed")
					case "cancellation":
						cancel()
					}
					return nil
				},
			}
			forwarder.onCall = func(int) {
				assertGatewayReservationUsage(t, registry, limiter, 1, 0, 1, 1)
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx)
			request.Header.Set("Authorization", "Bearer gl-client")
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)
			if !prepared {
				t.Fatal("request never reached preparation after selection")
			}
			if outcome == "success" {
				if response.Code != http.StatusOK || len(forwarder.inputs) != 1 {
					t.Fatalf("status=%d forwards=%d body=%s", response.Code, len(forwarder.inputs), response.Body.String())
				}
				assertGatewayReservationUsage(t, registry, limiter, 1, 0, 1, 0)
				return
			}
			if len(forwarder.inputs) != 0 {
				t.Fatal("failed or canceled preparation reached the upstream")
			}
			assertGatewayReservationUsage(t, registry, limiter, 1, 0, 0, 0)
			if !limiter.Available(1, 1, 1) {
				t.Fatal("failed or canceled preparation consumed credential capacity")
			}
		})
	}
}

func TestLivePendingReservationClosePreservesTransferredCredentialSlot(t *testing.T) {
	fake := &liveFakeOpener{}
	handler, engine, _, registry, _ := liveGatewayFixture(t, fake)
	setLiveGroupMode(handler, 1, state.CodexLiveDirect)
	setLiveGroupMode(handler, 2, state.CodexLiveDirect)
	limiter := ratelimit.NewCredentialLimiter()
	handler.credentialLimiter = limiter
	_, offer := liveClientOffer(t)
	request := httptest.NewRequest(http.MethodPost, "/v1/live", strings.NewReader(offer))
	request.Header.Set("Authorization", "Bearer gl-client")
	request.Header.Set("Content-Type", "application/sdp")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	fake.mu.Lock()
	selected := append([]uint(nil), fake.selected...)
	fake.mu.Unlock()
	if len(selected) != 1 {
		t.Fatalf("selected credentials = %v", selected)
	}
	// ServeHTTP has returned, so the iterator's Close has already run. The
	// committed slot belongs to the live call, not to the request iterator.
	assertGatewayReservationUsage(t, registry, limiter, selected[0], 0, 1, 1)
	if limiter.Available(selected[0], 0, 1) {
		t.Fatal("request return released the transferred credential slot")
	}
	call, ok := handler.liveSessions.lookup("rtc_1", 1)
	if !ok {
		t.Fatal("missing live call")
	}
	if err := handler.liveSessions.finishCall(call, "client_hangup"); err != nil {
		t.Fatal(err)
	}
	assertGatewayReservationUsage(t, registry, limiter, selected[0], 0, 1, 0)
}
