package control

import (
	"testing"

	"gpt-load/internal/ratelimit"
)

func TestLiveLimitUsageReportsDataPlaneConcurrency(t *testing.T) {
	limiter := ratelimit.NewDataPlaneConcurrency()
	releaseGlobal, ok := limiter.AcquireGlobal(2)
	if !ok {
		t.Fatal("global acquire rejected")
	}
	releaseGroup, ok := limiter.AcquireGroup(7, 3)
	if !ok {
		t.Fatal("group acquire rejected")
	}
	usage := NewLiveLimitUsage(nil, nil, nil, limiter)
	if !usage.DataPlaneConfigured() {
		t.Fatal("data-plane usage should be configured")
	}
	got := usage.DataPlaneUsage()
	if got.Global != 1 || got.Groups[7] != 1 {
		t.Fatalf("usage = %+v", got)
	}
	releaseGroup()
	releaseGlobal()
}
