package state

import (
	"testing"
	"time"
)

func revisionTestEntry(id uint, status CredentialStatus, weight *int) CredentialEntry {
	return CredentialEntry{
		ID:                 id,
		GroupID:            1,
		Status:             status,
		WeightManual:       cloneWeight(weight),
		Version:            1,
		IdentityGeneration: 1,
		Fingerprint:        "fp",
		EncryptedValue:     "cipher",
	}
}

func credentialRevisionForTest(t *testing.T, registry *CredentialRegistry, id uint) uint64 {
	t.Helper()
	ref, ok := registry.CredentialRef(id)
	if !ok {
		t.Fatalf("CredentialRef(%d) not found", id)
	}
	return ref.ConfigurationRevision
}

func TestCredentialConfigurationRevisionTracksPerCredentialChanges(t *testing.T) {
	registry := NewCredentialRegistry()
	if err := registry.ReplaceCredentials([]CredentialEntry{
		revisionTestEntry(1, CredentialStatusActive, nil),
		revisionTestEntry(2, CredentialStatusActive, nil),
	}); err != nil {
		t.Fatalf("ReplaceCredentials() error = %v", err)
	}
	rev1 := credentialRevisionForTest(t, registry, 1)
	rev2 := credentialRevisionForTest(t, registry, 2)
	if rev1 == 0 || rev2 == 0 {
		t.Fatalf("revisions = %d, %d; want both non-zero", rev1, rev2)
	}

	until := time.Now().Add(time.Minute)
	if !registry.SetCooldown(1, until) {
		t.Fatal("SetCooldown() = false")
	}
	if got := credentialRevisionForTest(t, registry, 1); got != rev1 {
		t.Fatalf("SetCooldown changed revision: got %d, want %d", got, rev1)
	}

	if !registry.ReplaceCredentialSecretIfMatch(1, 1, 2, "fp2", "cipher2") {
		t.Fatal("ReplaceCredentialSecretIfMatch() = false")
	}
	if got := credentialRevisionForTest(t, registry, 1); got != rev1 {
		t.Fatalf("ReplaceCredentialSecretIfMatch changed revision: got %d, want %d", got, rev1)
	}

	if err := registry.UpdateCredentialConfig(1, CredentialStatusActive, nil); err != nil {
		t.Fatalf("UpdateCredentialConfig() error = %v", err)
	}
	if got := credentialRevisionForTest(t, registry, 1); got != rev1 {
		t.Fatalf("UpdateCredentialConfig with same config changed revision: got %d, want %d", got, rev1)
	}

	weight := 100
	if err := registry.UpdateCredentialConfig(1, CredentialStatusActive, &weight); err != nil {
		t.Fatalf("UpdateCredentialConfig() error = %v", err)
	}
	newRev1 := credentialRevisionForTest(t, registry, 1)
	if newRev1 <= rev1 {
		t.Fatalf("weight change did not bump revision: got %d, want > %d", newRev1, rev1)
	}
	if got := credentialRevisionForTest(t, registry, 2); got != rev2 {
		t.Fatalf("credential 1 weight change touched credential 2 revision: got %d, want %d", got, rev2)
	}
	rev1 = newRev1

	if err := registry.SetCredentialStatus(2, CredentialStatusDisabled); err != nil {
		t.Fatalf("SetCredentialStatus() error = %v", err)
	}
	newRev2 := credentialRevisionForTest(t, registry, 2)
	if newRev2 == rev2 {
		t.Fatalf("status change did not bump credential 2 revision: got %d, want != %d", newRev2, rev2)
	}
	if got := credentialRevisionForTest(t, registry, 1); got != rev1 {
		t.Fatalf("credential 2 status change touched credential 1 revision: got %d, want %d", got, rev1)
	}
	rev2 = newRev2

	// 用相同配置再次整体替换，修订号保持不变。
	if err := registry.ReplaceCredentials([]CredentialEntry{
		revisionTestEntry(1, CredentialStatusActive, &weight),
		revisionTestEntry(2, CredentialStatusDisabled, nil),
	}); err != nil {
		t.Fatalf("ReplaceCredentials() error = %v", err)
	}
	if got := credentialRevisionForTest(t, registry, 1); got != rev1 {
		t.Fatalf("re-ReplaceCredentials changed credential 1 revision: got %d, want %d", got, rev1)
	}
	if got := credentialRevisionForTest(t, registry, 2); got != rev2 {
		t.Fatalf("re-ReplaceCredentials changed credential 2 revision: got %d, want %d", got, rev2)
	}
}

func TestReconcileGroupDetectsCredentialLimitChanges(t *testing.T) {
	registry := NewCredentialRegistry()
	entry := revisionTestEntry(1, CredentialStatusActive, nil)
	if err := registry.ReplaceCredentials([]CredentialEntry{entry}); err != nil {
		t.Fatalf("ReplaceCredentials() error = %v", err)
	}
	before := credentialRevisionForTest(t, registry, 1)
	limited := entry
	limited.RPMLimit = 60
	changed, err := registry.ReconcileGroup(1, []CredentialEntry{limited})
	if err != nil {
		t.Fatalf("ReconcileGroup() error = %v", err)
	}
	if !changed {
		t.Fatal("ReconcileGroup() changed = false, want true when only RPMLimit differs")
	}
	after := credentialRevisionForTest(t, registry, 1)
	if after <= before {
		t.Fatalf("RPMLimit change did not bump revision: got %d, want > %d", after, before)
	}
	before = after
	limited.ConcurrencyLimit = 3
	changed, err = registry.ReconcileGroup(1, []CredentialEntry{limited})
	if err != nil {
		t.Fatalf("ReconcileGroup() concurrency error = %v", err)
	}
	if !changed {
		t.Fatal("ReconcileGroup() changed = false, want true when only ConcurrencyLimit differs")
	}
	after = credentialRevisionForTest(t, registry, 1)
	if after <= before {
		t.Fatalf("ConcurrencyLimit change did not bump revision: got %d, want > %d", after, before)
	}
	for _, view := range registry.Snapshot() {
		if view.ID == 1 {
			if view.RPMLimit != 60 || view.ConcurrencyLimit != 3 {
				t.Fatalf("runtime view limits = %d/%d, want 60/3", view.RPMLimit, view.ConcurrencyLimit)
			}
			return
		}
	}
	t.Fatal("credential 1 missing from runtime view")
}
