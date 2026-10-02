package state

import (
	"encoding/json"
	"testing"

	"gpt-load/internal/channel"
	"gpt-load/internal/platform/config"
)

func TestDataPlaneConcurrencySettingsCompile(t *testing.T) {
	runtime, err := ResolveRuntimeSettings(config.Settings{
		SettingGlobalConcurrencyLimit: json.Number("7"),
	})
	if err != nil || runtime.GlobalConcurrencyLimit != 7 {
		t.Fatalf("runtime concurrency = %+v, err=%v", runtime, err)
	}
	group, err := ResolveGroupRuntimeSettings(runtime, config.Settings{
		SettingConcurrencyLimit: json.Number("3"),
	})
	if err != nil || group.ConcurrencyLimit != 3 {
		t.Fatalf("group concurrency = %+v, err=%v", group, err)
	}
}

func TestGroupConcurrencyInheritsGlobalUnlessOverridden(t *testing.T) {
	base, err := ResolveRuntimeSettings(config.Settings{
		SettingGlobalConcurrencyLimit: json.Number("7"),
	})
	if err != nil {
		t.Fatal(err)
	}
	group, err := ResolveGroupRuntimeSettings(base, nil)
	if err != nil || group.ConcurrencyLimit != 7 {
		t.Fatalf("inherited group concurrency = %+v, err=%v", group, err)
	}
	group, err = ResolveGroupRuntimeSettings(base, config.Settings{
		SettingConcurrencyLimit: json.Number("0"),
	})
	if err != nil || group.ConcurrencyLimit != 0 {
		t.Fatalf("explicit unlimited group concurrency = %+v, err=%v", group, err)
	}
}

func TestDataPlaneConcurrencySettingsAllowZeroUnlimited(t *testing.T) {
	if err := ValidateRuntimeSetting(SettingGlobalConcurrencyLimit, json.Number("0")); err != nil {
		t.Fatalf("global zero rejected: %v", err)
	}
	if err := ValidateRuntimeSetting(SettingConcurrencyLimit, json.Number("0")); err != nil {
		t.Fatalf("group zero rejected: %v", err)
	}
}

func TestCompileKeepsDisabledGroupConcurrencyInCatalog(t *testing.T) {
	snapshot, err := Compile(CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		SystemSettings:  config.Settings{SettingGlobalConcurrencyLimit: json.Number("9")},
		Groups: []GroupConfig{{
			ID: 3, Name: "disabled", ConnectionType: "api_key", ChannelID: channel.OpenAI,
			Params: []byte("{}"), Models: []ModelConfig{{ID: "gpt-4o"}},
			Settings: config.Settings{SettingConcurrencyLimit: json.Number("4")}, Enabled: false,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := snapshot.Groups[3]; exists {
		t.Fatal("disabled group unexpectedly entered execution groups")
	}
	if got := snapshot.GroupCatalog[3].ConcurrencyLimit; got != 4 {
		t.Fatalf("disabled group catalog concurrency = %d, want 4", got)
	}
}
