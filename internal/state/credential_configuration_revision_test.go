package state

import (
	"testing"
	"time"
)

func TestCredentialConfigurationRevisionTracksRoutingChanges(t *testing.T) {
	registry := NewCredentialRegistry()
	if got := registry.ConfigurationRevision(); got != 0 {
		t.Fatalf("initial configuration revision = %d, want 0", got)
	}
	if err := registry.ReplaceCredentials([]CredentialEntry{{
		ID: 1, GroupID: 1, Status: CredentialStatusActive,
		Version: 1, IdentityGeneration: 1, Fingerprint: "fingerprint", EncryptedValue: "cipher",
	}}); err != nil {
		t.Fatal(err)
	}
	first := registry.ConfigurationRevision()
	if first == 0 {
		t.Fatal("ReplaceCredentials() did not advance configuration revision")
	}
	if !registry.SetCooldown(1, time.Now().Add(time.Minute)) {
		t.Fatal("SetCooldown() = false")
	}
	if got := registry.ConfigurationRevision(); got != first {
		t.Fatalf("runtime health changed configuration revision to %d, want %d", got, first)
	}
	weight := 100
	if err := registry.UpdateCredentialConfig(1, CredentialStatusActive, &weight); err != nil {
		t.Fatal(err)
	}
	if got := registry.ConfigurationRevision(); got <= first {
		t.Fatalf("weight update revision = %d, want > %d", got, first)
	}
}
