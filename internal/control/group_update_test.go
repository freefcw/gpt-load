package control

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"gpt-load/internal/catalog"
	"gpt-load/internal/channel"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/storage/models"
)

func TestMapGroupRowToStateCarriesDeepClonedParams(t *testing.T) {
	t.Parallel()
	group := models.Group{
		ID: 1, Name: "channel-group", ChannelID: string(channel.OpenAICompatible),
		Params: models.JSON(`{"base_url":"https://api.example.com/v1"}`),
		Models: models.JSON(`[]`), Overrides: models.JSON(`{}`), Enabled: false,
	}

	got, err := mapGroupRowToState(group)
	if err != nil {
		t.Fatalf("mapGroupRowToState() error = %v", err)
	}
	group.Params[0] = '['
	if string(got.Params) != `{"base_url":"https://api.example.com/v1"}` {
		t.Fatalf("GroupConfig.Params = %s, want independent canonical params", got.Params)
	}
}

func TestSubscriptionGroupSettingsAndModelsRemainUpdatable(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	stage := mustImportSubscriptionStage(t, fixture, "account-group-update", "group-update@example.com")
	created, err := fixture.service.CreateGroup(t.Context(), GroupCreateRequest{
		Name:           stringPointer("subscription-before"),
		ChannelID:      channel.Codex,
		ConnectionType: models.ConnectionTypeSubscription,
		Params:         json.RawMessage(`{}`),
		Models: optionalGroupModels{Set: true, Values: []GroupModel{
			{ID: "gpt-before"},
		}},
		StagedCredentialIDs: []string{stage.StageID},
	})
	if err != nil {
		t.Fatal(err)
	}

	settings, err := fixture.service.UpdateGroupSettings(t.Context(), created.GroupID, GroupSettingsUpdateRequest{
		Name: optionalField[string]{Set: true, Value: "subscription-after"},
	})
	if err != nil {
		t.Fatalf("UpdateGroupSettings() error = %v", err)
	}
	if settings.Name != "subscription-after" || settings.ConnectionType != models.ConnectionTypeSubscription {
		t.Fatalf("updated settings = %#v", settings)
	}

	groupModels, err := fixture.service.UpdateGroupModels(t.Context(), created.GroupID, GroupModelsUpdateRequest{
		Models: optionalGroupModels{Set: true, Values: []GroupModel{{ID: "gpt-after"}}},
	})
	if err != nil {
		t.Fatalf("UpdateGroupModels() error = %v", err)
	}
	if len(groupModels.Items) != 1 || groupModels.Items[0].ID != "gpt-after" {
		t.Fatalf("updated models = %#v", groupModels)
	}
}

func TestGroupCatalogSyncTriggerOnlyTracksProviderAndModelIDChanges(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	created, err := fixture.service.CreateGroup(t.Context(), GroupCreateRequest{
		ChannelID: channel.OpenAI,
		Params:    json.RawMessage(`{}`),
		Models: optionalGroupModels{Set: true, Values: []GroupModel{
			{ID: "model-a", Alias: "public-a", AliasEnabled: true},
		}},
		Credentials: "sk-catalog-trigger", ConnectionType: "api_key",
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator := newCatalogSyncCoordinator(
		fixture.service,
		nil,
		filepath.Join(t.TempDir(), "catalog.json"),
		catalog.Metadata{},
		false,
	)
	if _, err := fixture.service.CreateGroup(t.Context(), GroupCreateRequest{
		ChannelID:   channel.Anthropic,
		Params:      json.RawMessage(`{}`),
		Models:      optionalGroupModels{Set: true, Values: []GroupModel{{ID: "created-model"}}},
		Credentials: "sk-catalog-trigger-created", ConnectionType: "api_key",
	}); err != nil {
		t.Fatal(err)
	}
	assertCatalogGroupWake(t, coordinator)

	if _, err := fixture.service.UpdateGroupModels(t.Context(), created.GroupID, GroupModelsUpdateRequest{
		Models: optionalGroupModels{Set: true, Values: []GroupModel{
			{ID: "model-a", Alias: "renamed", AliasEnabled: true},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	assertNoCatalogGroupWake(t, coordinator)

	if _, err := fixture.service.UpdateGroupSettings(t.Context(), created.GroupID, GroupSettingsUpdateRequest{
		Params: optionalField[json.RawMessage]{Set: true, Value: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatal(err)
	}
	assertNoCatalogGroupWake(t, coordinator)

	if _, err := fixture.service.UpdateGroupSettings(t.Context(), created.GroupID, GroupSettingsUpdateRequest{
		Name: optionalField[string]{Set: true, Value: "renamed-group"},
	}); err != nil {
		t.Fatal(err)
	}
	assertNoCatalogGroupWake(t, coordinator)

	if _, err := fixture.service.UpdateGroupModels(t.Context(), created.GroupID, GroupModelsUpdateRequest{
		Models: optionalGroupModels{Set: true, Values: []GroupModel{{ID: "model-b"}}},
	}); err != nil {
		t.Fatal(err)
	}
	assertCatalogGroupWake(t, coordinator)

	custom, err := fixture.service.CreateGroup(t.Context(), GroupCreateRequest{
		ChannelID:   channel.OpenAICompatible,
		Params:      json.RawMessage(`{"base_url":"https://catalog-trigger-custom.example.com/v1"}`),
		Models:      optionalGroupModels{Set: true, Values: []GroupModel{{ID: "custom-a"}}},
		Credentials: "sk-catalog-trigger-custom", ConnectionType: "api_key",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCatalogGroupWake(t, coordinator)
	if _, err := fixture.service.UpdateGroupModels(t.Context(), custom.GroupID, GroupModelsUpdateRequest{
		Models: optionalGroupModels{Set: true, Values: []GroupModel{{ID: "custom-b"}}},
	}); err != nil {
		t.Fatal(err)
	}
	assertCatalogGroupWake(t, coordinator)
	if err := fixture.service.DeleteGroup(t.Context(), custom.GroupID); err != nil {
		t.Fatal(err)
	}
	assertCatalogGroupWake(t, coordinator)
	if err := fixture.service.DeleteGroup(t.Context(), created.GroupID); err != nil {
		t.Fatal(err)
	}
	assertCatalogGroupWake(t, coordinator)
}

func assertCatalogGroupWake(t *testing.T, coordinator *CatalogSyncCoordinator) {
	t.Helper()
	select {
	case <-coordinator.groupWake:
	default:
		t.Fatal("catalog group sync was not requested")
	}
}

func assertNoCatalogGroupWake(t *testing.T, coordinator *CatalogSyncCoordinator) {
	t.Helper()
	select {
	case <-coordinator.groupWake:
		t.Fatal("irrelevant group change requested catalog sync")
	default:
	}
}

func TestGroupCredentialLimitsPersistAndResolve(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	created, err := fixture.service.CreateGroup(t.Context(), GroupCreateRequest{
		ChannelID: channel.OpenAI, Params: json.RawMessage(`{}`),
		Models:      optionalGroupModels{Set: true, Values: []GroupModel{{ID: "gpt-4o"}}},
		Credentials: "sk-group-limit-one\nsk-group-limit-two", ConnectionType: "api_key",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 分组 0 是不限，凭据侧还没单独设值，所以生效值也是不限。
	settings, err := fixture.service.GetGroupSettings(t.Context(), created.GroupID)
	if err != nil {
		t.Fatal(err)
	}
	if settings.CredentialRPMLimit != 0 || settings.CredentialConcurrencyLimit != 0 {
		t.Fatalf("default group limits = %d/%d, want 0/0", settings.CredentialRPMLimit, settings.CredentialConcurrencyLimit)
	}

	updated, err := fixture.service.UpdateGroupSettings(t.Context(), created.GroupID, GroupSettingsUpdateRequest{
		CredentialRPMLimit:         optionalField[int64]{Set: true, Value: 60},
		CredentialConcurrencyLimit: optionalField[int64]{Set: true, Value: 2},
	})
	if err != nil {
		t.Fatalf("UpdateGroupSettings() error = %v", err)
	}
	if updated.CredentialRPMLimit != 60 || updated.CredentialConcurrencyLimit != 2 {
		t.Fatalf("updated limits = %d/%d, want 60/2", updated.CredentialRPMLimit, updated.CredentialConcurrencyLimit)
	}

	// 重新读一次，确认落库而不是只改了返回值。
	reloaded, err := fixture.service.GetGroupSettings(t.Context(), created.GroupID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.CredentialRPMLimit != 60 || reloaded.CredentialConcurrencyLimit != 2 {
		t.Fatalf("reloaded limits = %d/%d, want 60/2", reloaded.CredentialRPMLimit, reloaded.CredentialConcurrencyLimit)
	}

	collection, err := fixture.service.ListGroupCredentials(t.Context(), created.GroupID, CredentialCollectionQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(collection.Items) != 2 {
		t.Fatalf("credentials = %d, want 2", len(collection.Items))
	}
	for _, item := range collection.Items {
		// 凭据自身是 0，表示继承分组，所以生效值等于分组默认值。
		if item.RPMLimit != 0 || item.EffectiveRPMLimit != 60 || item.GroupRPMLimit != 60 ||
			item.ConcurrencyLimit != 0 || item.EffectiveConcurrencyLimit != 2 || item.GroupConcurrencyLimit != 2 {
			t.Fatalf("inherited credential limits = %+v", item)
		}
	}

	// 凭据填自己的值就覆盖分组；填 0 则回到继承。
	overridden, err := fixture.service.UpdateGroupCredential(t.Context(), created.GroupID, collection.Items[0].CredentialID, CredentialUpdateRequest{
		RPMLimit:         optionalField[int64]{Set: true, Value: 10},
		ConcurrencyLimit: optionalField[int64]{Set: true, Value: 1},
	})
	if err != nil {
		t.Fatalf("UpdateGroupCredential() error = %v", err)
	}
	if overridden.RPMLimit != 10 || overridden.EffectiveRPMLimit != 10 || overridden.GroupRPMLimit != 60 ||
		overridden.ConcurrencyLimit != 1 || overridden.EffectiveConcurrencyLimit != 1 {
		t.Fatalf("overridden credential limits = %+v", overridden)
	}
	inherited, err := fixture.service.UpdateGroupCredential(t.Context(), created.GroupID, collection.Items[0].CredentialID, CredentialUpdateRequest{
		RPMLimit:         optionalField[int64]{Set: true, Value: 0},
		ConcurrencyLimit: optionalField[int64]{Set: true, Value: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if inherited.RPMLimit != 0 || inherited.EffectiveRPMLimit != 60 || inherited.EffectiveConcurrencyLimit != 2 {
		t.Fatalf("reset credential limits = %+v, want inherited 60/2", inherited)
	}

	// 限额不接受空值和负数：0 已经有含义，不能再拿 null 表示清空。
	for _, request := range []GroupSettingsUpdateRequest{
		{CredentialRPMLimit: optionalField[int64]{Set: true, Null: true}},
		{CredentialConcurrencyLimit: optionalField[int64]{Set: true, Value: -1}},
	} {
		if _, err := fixture.service.UpdateGroupSettings(t.Context(), created.GroupID, request); !errors.Is(err, app_errors.ErrValidation) {
			t.Fatalf("invalid group limit error = %v, want validation", err)
		}
	}
	if _, err := fixture.service.UpdateGroupCredential(t.Context(), created.GroupID, collection.Items[0].CredentialID, CredentialUpdateRequest{
		RPMLimit: optionalField[int64]{Set: true, Value: -1},
	}); !errors.Is(err, app_errors.ErrValidation) {
		t.Fatalf("invalid credential limit error = %v, want validation", err)
	}
}
