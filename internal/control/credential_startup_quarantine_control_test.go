package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/execution"
	"gpt-load/internal/outboundproxy"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/protocol"
	"gpt-load/internal/state"
	stateloader "gpt-load/internal/state/loader"
	"gpt-load/internal/storage/models"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

func TestReadHomeSubscriptionAccountsRendersQuarantinedCredential(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	now := time.Date(2026, time.November, 12, 12, 0, 0, 0, time.UTC)
	fixture.service.now = func() time.Time { return now }

	_, healthy := createHomeSubscriptionCredential(t, fixture, "healthy", "healthy-account", "healthy@example.com")
	_, quarantined := createHomeSubscriptionCredential(t, fixture, "quarantined", "quarantined-account", "quarantined@example.com")
	damaged, err := fixture.encryption.Encrypt(`{"type":"codex","access_token":"must-not-leak-home"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&models.Credential{}).Where("id = ?", quarantined.ID).Updates(map[string]any{
		"data": damaged, "auth_state": models.CredentialAuthStateReauthorizationRequired,
		"auth_error_code": models.CredentialStartupDecryptFailed,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if !fixture.registry.SetCredentialAuthState(quarantined.ID, state.CredentialAuthStateReauthorizationRequired) {
		t.Fatal("publish quarantine auth state")
	}

	recentBucket := now.Truncate(time.Hour).Add(-time.Hour).UnixMilli()
	if err := fixture.db.Create(&[]models.CredentialAttemptStat{
		{CredentialID: healthy.ID, BucketStartMS: recentBucket, SuccessCount: 2},
		{CredentialID: quarantined.ID, BucketStartMS: recentBucket, SuccessCount: 5},
	}).Error; err != nil {
		t.Fatal(err)
	}

	result, err := fixture.service.ReadHomeSubscriptionAccounts(t.Context())
	if err != nil {
		t.Fatalf("ReadHomeSubscriptionAccounts() error = %v", err)
	}
	var found bool
	for _, item := range result.Items {
		if item.Credential.CredentialID != quarantined.ID {
			continue
		}
		found = true
		if item.Credential.Mask != fmt.Sprintf("Credential #%d", quarantined.ID) ||
			item.Credential.AuthErrorCode != models.CredentialStartupDecryptFailed ||
			item.Credential.AuthState != string(models.CredentialAuthStateReauthorizationRequired) {
			t.Fatalf("quarantined card = %#v", item.Credential)
		}
	}
	if !found {
		t.Fatalf("quarantined credential missing from %#v", result.Items)
	}
	raw := strings.ToLower(fmt.Sprintf("%#v", result))
	if strings.Contains(raw, "must-not-leak-home") || strings.Contains(raw, damaged) {
		t.Fatal("home response exposed quarantined secret material")
	}
}

func TestRefreshGroupCredentialRefusesStartupQuarantine(t *testing.T) {
	t.Parallel()
	for _, code := range []string{
		models.CredentialStartupDecryptFailed,
		models.CredentialStartupShapeInvalid,
		models.CredentialStartupFingerprintMismatch,
	} {
		t.Run(code, func(t *testing.T) {
			t.Parallel()
			fixture, groupID, credentialID := newSubscriptionCredentialFixture(t)
			var before models.Credential
			if err := fixture.db.First(&before, credentialID).Error; err != nil {
				t.Fatal(err)
			}
			updates := map[string]any{
				"auth_state": models.CredentialAuthStateReauthorizationRequired, "auth_error_code": code,
			}
			if code != models.CredentialStartupFingerprintMismatch {
				damaged, err := fixture.encryption.Encrypt(`{"type":"codex","token":"must-not-refresh"}`)
				if err != nil {
					t.Fatal(err)
				}
				updates["data"] = damaged
				before.Data = damaged
			}
			if err := fixture.db.Model(&models.Credential{}).Where("id = ?", credentialID).Updates(updates).Error; err != nil {
				t.Fatal(err)
			}
			if !fixture.registry.SetCredentialAuthState(credentialID, state.CredentialAuthStateReauthorizationRequired) {
				t.Fatal("publish quarantine auth state")
			}
			fixture.service.recoverSubscriptionCredential = func(
				context.Context, channel.ID, execution.CredentialSnapshot,
			) (subscriptionruntime.Credential, *execution.ErrorEvidence) {
				t.Fatal("startup quarantine must not reach credential recovery")
				return subscriptionruntime.Credential{}, nil
			}

			_, err := fixture.service.RefreshGroupCredential(t.Context(), groupID, credentialID)
			if !errors.Is(err, app_errors.ErrCredentialReauthorizationRequired) {
				t.Fatalf("RefreshGroupCredential() error = %v", err)
			}
			var stored models.Credential
			if err := fixture.db.First(&stored, credentialID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Data != before.Data || stored.Fingerprint != before.Fingerprint ||
				stored.SecretVersion != before.SecretVersion || stored.AuthErrorCode != code ||
				stored.AuthState != models.CredentialAuthStateReauthorizationRequired {
				t.Fatal("refresh rewrote a quarantined credential")
			}
			auth, ok := fixture.registry.CredentialAuthStateOf(credentialID)
			if !ok || auth != state.CredentialAuthStateReauthorizationRequired {
				t.Fatalf("registry auth = %q ok=%t", auth, ok)
			}
			if len(fixture.registry.CollectCredentialCandidates([]uint{groupID}, nil, time.Now())) != 0 {
				t.Fatal("quarantined credential became schedulable")
			}
		})
	}
}

func TestDiscoverGroupModelsSkipsQuarantinedAPIKey(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	group := seedPersistedDiscoveryGroup(t, fixture, true, models.JSON(`{}`))
	seedPersistedDiscoveryCredential(t, fixture, group.ID, 1, "must-not-send", models.CredentialStatusActive)
	seedPersistedDiscoveryCredential(t, fixture, group.ID, 2, "also-skip", models.CredentialStatusActive)
	seedPersistedDiscoveryCredential(t, fixture, group.ID, 3, "healthy-key", models.CredentialStatusActive)
	if err := fixture.db.Model(&models.Credential{}).Where("id = ?", 1).Updates(map[string]any{
		"auth_state":      models.CredentialAuthStateReauthorizationRequired,
		"auth_error_code": models.CredentialStartupFingerprintMismatch,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&models.Credential{}).Where("id = ?", 2).
		Update("auth_state", models.CredentialAuthStateOutcomeUnknown).Error; err != nil {
		t.Fatal(err)
	}
	var calls []string
	fixture.service.executor = newRecordingDiscoveryExecutor(&recordingDiscoveryExecutorTarget{
		value: protocol.OpenAICompletions,
		listFn: func(_ context.Context, _, apiKey string, _ state.HeaderRules) ([]string, error) {
			calls = append(calls, apiKey)
			if apiKey != "healthy-key" {
				return nil, errors.New("unexpected credential")
			}
			return []string{"gpt-4o"}, nil
		},
	})

	result, err := fixture.service.DiscoverGroupModels(t.Context(), group.ID)
	if err != nil || len(result.Models) != 1 || len(calls) != 1 || calls[0] != "healthy-key" {
		t.Fatalf("result=%#v err=%v calls=%v", result, err, calls)
	}
}

func TestProxyImpactUsesFallbackLabelForQuarantinedCredential(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	groupID := createGroupWithCredentials(t, fixture, "sk-proxy-quarantine")
	item, err := fixture.service.SaveProxy(t.Context(), 0, ProxySaveRequest{Name: "quarantine", URL: "http://quarantine-proxy.example:8080"})
	if err != nil {
		t.Fatal(err)
	}
	var credential models.Credential
	if err := fixture.db.Where("group_id = ?", groupID).Take(&credential).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.UpdateGroupCredential(t.Context(), groupID, credential.ID, CredentialUpdateRequest{
		Proxy: optionalField[outboundproxy.Config]{Set: true, Value: outboundproxy.Config{Mode: outboundproxy.ModeCustom, ProxyID: item.ID}},
	}); err != nil {
		t.Fatal(err)
	}
	damaged, err := fixture.encryption.Encrypt("not-a-credential")
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&models.Credential{}).Where("id = ?", credential.ID).Updates(map[string]any{
		"data": damaged, "auth_state": models.CredentialAuthStateReauthorizationRequired,
		"auth_error_code": models.CredentialStartupShapeInvalid,
	}).Error; err != nil {
		t.Fatal(err)
	}

	impact, err := fixture.service.ProxyImpact(t.Context(), []uint{item.ID})
	if err != nil {
		t.Fatalf("ProxyImpact() error = %v", err)
	}
	if len(impact.Credentials) != 1 || impact.Credentials[0].Label != fmt.Sprintf("Credential #%d", credential.ID) {
		t.Fatalf("proxy impact = %#v", impact.Credentials)
	}
}

func TestRevealAndExportRefuseQuarantinedCredentials(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	groupID := createGroupWithCredentials(t, fixture, "sk-export-healthy\nsk-export-quarantined")
	var rows []models.Credential
	if err := fixture.db.Where("group_id = ?", groupID).Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("credentials = %d", len(rows))
	}
	quarantined := rows[1]
	damaged, err := fixture.encryption.Encrypt("not-a-credential")
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&models.Credential{}).Where("id = ?", quarantined.ID).Updates(map[string]any{
		"data": damaged, "auth_state": models.CredentialAuthStateReauthorizationRequired,
		"auth_error_code": models.CredentialStartupDecryptFailed,
	}).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.service.RevealGroupCredential(t.Context(), groupID, quarantined.ID); !errors.Is(err, app_errors.ErrCredentialReauthorizationRequired) {
		t.Fatalf("RevealGroupCredential() error = %v", err)
	}
	if _, err := fixture.service.TestGroupCredential(t.Context(), groupID, quarantined.ID); !errors.Is(err, app_errors.ErrCredentialReauthorizationRequired) {
		t.Fatalf("TestGroupCredential() error = %v", err)
	}
	downloaded, err := fixture.service.DownloadAllGroupCredentials(t.Context(), groupID)
	if err != nil {
		t.Fatalf("DownloadAllGroupCredentials() error = %v", err)
	}
	if downloaded.CredentialCount != 1 || len(downloaded.Files) != 1 || downloaded.Files[0].Content == nil ||
		!strings.Contains(*downloaded.Files[0].Content, "sk-export-healthy") ||
		strings.Contains(*downloaded.Files[0].Content, "sk-export-quarantined") ||
		strings.Contains(*downloaded.Files[0].Content, "not-a-credential") {
		t.Fatalf("download = %#v", downloaded)
	}
}

func TestReimportRepairsQuarantinedAPIKeyWithoutDisturbingAffinityRevision(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	groupID := createGroupWithCredentials(t, fixture, "sk-repair-same\nsk-repair-sibling")
	var rows []models.Credential
	if err := fixture.db.Where("group_id = ?", groupID).Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("credentials = %d", len(rows))
	}
	target, sibling := rows[0], rows[1]
	weight := 40
	if _, err := fixture.service.UpdateGroupCredential(t.Context(), groupID, target.ID, CredentialUpdateRequest{
		WeightManual: optionalField[int]{Set: true, Value: weight},
		RPMLimit:     optionalField[int64]{Set: true, Value: 12},
	}); err != nil {
		t.Fatal(err)
	}
	before, ok := fixture.registry.CredentialRef(target.ID)
	if !ok {
		t.Fatal("missing target ref")
	}
	siblingBefore, ok := fixture.registry.CredentialRef(sibling.ID)
	if !ok {
		t.Fatal("missing sibling ref")
	}
	if err := fixture.db.Model(&models.Credential{}).Where("id = ?", target.ID).Updates(map[string]any{
		"auth_state":      models.CredentialAuthStateReauthorizationRequired,
		"auth_error_code": models.CredentialStartupDecryptFailed,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if !fixture.registry.SetCredentialAuthState(target.ID, state.CredentialAuthStateReauthorizationRequired) {
		t.Fatal("publish quarantine auth state")
	}

	imported, err := fixture.service.ImportGroupCredentials(t.Context(), groupID, CredentialImportRequest{Credentials: "sk-repair-same"})
	if err != nil || imported.CredentialsAdded != 1 || imported.CredentialsDuplicated != 0 {
		t.Fatalf("import = %#v err=%v", imported, err)
	}
	var stored models.Credential
	if err := fixture.db.First(&stored, target.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.AuthState != models.CredentialAuthStateReady || stored.AuthErrorCode != "" ||
		stored.SecretVersion != target.SecretVersion+1 || stored.Fingerprint != target.Fingerprint ||
		stored.RPMLimit != 12 || stored.WeightManual == nil || *stored.WeightManual != weight {
		t.Fatalf("repaired credential = %#v", stored)
	}
	after, ok := fixture.registry.CredentialRef(target.ID)
	if !ok || after.Version != stored.SecretVersion || after.IdentityGeneration != before.IdentityGeneration ||
		after.ConfigurationRevision != before.ConfigurationRevision {
		t.Fatalf("repaired ref = %#v before=%#v", after, before)
	}
	siblingAfter, ok := fixture.registry.CredentialRef(sibling.ID)
	if !ok || siblingAfter.ConfigurationRevision != siblingBefore.ConfigurationRevision {
		t.Fatalf("sibling revision changed: before=%d after=%d", siblingBefore.ConfigurationRevision, siblingAfter.ConfigurationRevision)
	}
	candidates := fixture.registry.CollectCredentialCandidates([]uint{groupID}, nil, time.Now())
	var schedulable bool
	for _, candidate := range candidates {
		if candidate.ID == target.ID && candidate.RPMLimit == 12 {
			schedulable = true
		}
	}
	if !schedulable {
		t.Fatalf("repaired credential is not schedulable: %#v", candidates)
	}

	var group models.Group
	if err := fixture.db.First(&group, groupID).Error; err != nil {
		t.Fatal(err)
	}
	legacyIdentity := "legacy-identity"
	if err := fixture.db.Model(&models.Credential{}).Where("id = ?", target.ID).Updates(map[string]any{
		"identity_fingerprint": legacyIdentity,
		"auth_state":           models.CredentialAuthStateReauthorizationRequired,
		"auth_error_code":      models.CredentialStartupFingerprintMismatch,
	}).Error; err != nil {
		t.Fatal(err)
	}
	legacyGeneration := stateloader.CredentialIdentityGeneration(legacyIdentity, group.ChannelID, string(group.ConnectionType), json.RawMessage(group.Params))
	repaired, err := fixture.service.ImportGroupCredentials(t.Context(), groupID, CredentialImportRequest{Credentials: "sk-repair-same"})
	if err != nil || repaired.CredentialsAdded != 1 {
		t.Fatalf("identity repair = %#v err=%v", repaired, err)
	}
	if err := fixture.db.First(&stored, target.ID).Error; err != nil {
		t.Fatal(err)
	}
	restored, ok := fixture.registry.CredentialRef(target.ID)
	wantGeneration := stateloader.CredentialIdentityGeneration(stored.IdentityFingerprint, group.ChannelID, string(group.ConnectionType), json.RawMessage(group.Params))
	if !ok || stored.IdentityFingerprint != target.Fingerprint || stored.AuthState != models.CredentialAuthStateReady ||
		restored.IdentityGeneration == legacyGeneration || restored.IdentityGeneration != wantGeneration {
		t.Fatalf("identity repair stored=%#v ref=%#v legacy=%d", stored, restored, legacyGeneration)
	}
}
