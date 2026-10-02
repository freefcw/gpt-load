// Package modelcatalog shares pinned Codex capabilities without exposing CPA types.
package modelcatalog

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed codex.json
var snapshot []byte

var capabilities = func() map[string]bool {
	var models []struct {
		ID      string `json:"id"`
		Updates bool   `json:"support_configuration_update"`
	}
	if err := json.Unmarshal(snapshot, &models); err != nil {
		panic(err)
	}
	result := make(map[string]bool, len(models))
	for _, m := range models {
		result[m.ID] = m.Updates
	}
	return result
}()

func SupportsReasoningUpdates(model string) bool { return capabilities[normalizeModelID(model)] }

func normalizeModelID(model string) string {
	model = strings.TrimSpace(model)
	if open := strings.LastIndexByte(model, '('); open >= 0 && strings.HasSuffix(model, ")") {
		return strings.TrimSpace(model[:open])
	}
	return model
}
