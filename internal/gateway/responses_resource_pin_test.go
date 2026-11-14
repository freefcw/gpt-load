package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gpt-load/internal/state"
)

// A stored Responses resource is pinned to the credential that created it.
// Session affinity may prefer another account, but that preference must not
// move the resource, and a pinned credential that is quarantined or locally
// rate-limited must fail closed instead of falling through.
func TestPinnedResponseResourceKeepsOwnerAgainstSessionAffinity(t *testing.T) {
	forwarder := &scriptedForwarder{results: []UpstreamResult{
		storedResponse("resp_private"),
		storedResponse("resp_other"),
		storedResponse("resp_private"),
		storedResponse("resp_session"),
	}}
	_, engine := plantPinnedResponseWithForeignSession(t, forwarder)

	response := servePinnedResource(engine, "other-account-session")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	followUp := serveResponsesWithSession(t, engine, `{"model":"gpt-4o","input":"stay-on-session"}`, "other-account-session")
	if followUp.Code != http.StatusOK {
		t.Fatalf("follow-up status=%d body=%s", followUp.Code, followUp.Body.String())
	}
	assertAffinityAttemptKeys(t, forwarder.inputs, []string{"sk-one", "sk-two", "sk-one", "sk-two"})
}

func TestPinnedResponseResourceDoesNotBypassCredentialLimitsOrQuarantine(t *testing.T) {
	t.Run("rpm", func(t *testing.T) {
		forwarder := newPinnedResourceForwarder()
		handler, engine := plantPinnedResponseWithForeignSession(t, forwarder)
		setCredentialLimit(t, handler, 1, 0)
		// The create already consumed the owner's only RPM slot. The resource
		// read must report that limit and must not move to the session account.
		response := servePinnedResource(engine, "other-account-session")
		assertGatewayReasonTest(t, response, http.StatusTooManyRequests, reasonCredentialRateLimited.Code)
		if response.Header().Get("Retry-After") == "" {
			t.Fatal("Retry-After missing on pinned credential RPM limit")
		}
		if len(forwarder.inputs) != 2 {
			t.Fatalf("attempts=%d, rate-limited pin forwarded or switched credentials", len(forwarder.inputs))
		}
	})

	t.Run("concurrency", func(t *testing.T) {
		forwarder := newPinnedResourceForwarder()
		handler, engine := plantPinnedResponseWithForeignSession(t, forwarder)
		setCredentialLimit(t, handler, 0, 1)
		hold, ok := handler.credentialLimiter.Acquire(1, 0, 1)
		if !ok {
			t.Fatal("could not occupy the pinned credential concurrency slot")
		}
		defer hold()
		response := servePinnedResource(engine, "other-account-session")
		assertGatewayReasonTest(t, response, http.StatusTooManyRequests, reasonCredentialRateLimited.Code)
		if len(forwarder.inputs) != 2 {
			t.Fatalf("attempts=%d, concurrency-limited pin forwarded or switched credentials", len(forwarder.inputs))
		}
	})

	t.Run("quarantine", func(t *testing.T) {
		forwarder := newPinnedResourceForwarder()
		handler, engine := plantPinnedResponseWithForeignSession(t, forwarder)
		registry := handler.registry.(*state.CredentialRegistry)
		if !registry.SetCredentialAuthState(1, state.CredentialAuthStateReauthorizationRequired) {
			t.Fatal("could not quarantine the pinned credential")
		}
		response := servePinnedResource(engine, "other-account-session")
		assertGatewayReasonTest(t, response, http.StatusServiceUnavailable, reasonNoCandidate.Code)
		if len(forwarder.inputs) != 2 {
			t.Fatalf("attempts=%d, quarantined pin forwarded or switched credentials", len(forwarder.inputs))
		}
	})
}

func newPinnedResourceForwarder() *scriptedForwarder {
	return &scriptedForwarder{results: []UpstreamResult{
		storedResponse("resp_private"),
		storedResponse("resp_other"),
		storedResponse("must-not-forward"),
	}}
}

// plantPinnedResponseWithForeignSession stores resp_private on sk-one, then
// records Session-Id affinity on sk-two while the owner is disabled.
func plantPinnedResponseWithForeignSession(t *testing.T, forwarder *scriptedForwarder) (*Handler, http.Handler) {
	t.Helper()
	handler, engine, _ := newContinuationFixture(t, forwarder)
	group := handler.manager.Current().Groups[1]
	group.AffinityEnabled = true
	handler.manager.Current().Groups[1] = group
	serveContinuation(t, engine, "gl-client", `{"model":"gpt-4o","input":"private","store":true}`, http.StatusOK)

	registry := handler.registry.(*state.CredentialRegistry)
	if err := registry.SetCredentialStatus(1, state.CredentialStatusDisabled); err != nil {
		t.Fatal(err)
	}
	response := serveResponsesWithSession(t, engine, `{"model":"gpt-4o","input":"other-session","store":true}`, "other-account-session")
	if response.Code != http.StatusOK {
		t.Fatalf("session setup status=%d body=%s", response.Code, response.Body.String())
	}
	if err := registry.SetCredentialStatus(1, state.CredentialStatusActive); err != nil {
		t.Fatal(err)
	}
	assertAffinityAttemptKeys(t, forwarder.inputs, []string{"sk-one", "sk-two"})
	return handler, engine
}

func setCredentialLimit(t *testing.T, handler *Handler, rpm, concurrency int64) {
	t.Helper()
	registry := handler.registry.(*state.CredentialRegistry)
	entries, err := registry.SnapshotGroupCredentialEntriesExact(1, []uint{1})
	if err != nil {
		t.Fatal(err)
	}
	entries[0].RPMLimit = rpm
	entries[0].ConcurrencyLimit = concurrency
	if err := registry.RestoreGroupCredentialEntriesExact(1, entries); err != nil {
		t.Fatal(err)
	}
}

func servePinnedResource(engine http.Handler, sessionID string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/v1/responses/resp_private", nil)
	request.Header.Set("Authorization", "Bearer gl-client")
	request.Header.Set("Session-Id", sessionID)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	return response
}

func serveResponsesWithSession(t *testing.T, engine http.Handler, body, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer gl-client")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Session-Id", sessionID)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	return response
}
