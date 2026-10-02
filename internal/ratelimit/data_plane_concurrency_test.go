package ratelimit

import "testing"

func TestDataPlaneConcurrencyLimitsAndIdempotentRelease(t *testing.T) {
	limiter := NewDataPlaneConcurrency()
	globalRelease, ok := limiter.AcquireGlobal(1)
	if !ok {
		t.Fatal("first global acquire rejected")
	}
	if _, ok := limiter.AcquireGlobal(1); ok {
		t.Fatal("second global acquire allowed at limit")
	}
	groupRelease, ok := limiter.AcquireGroup(7, 1)
	if !ok {
		t.Fatal("first group acquire rejected")
	}
	if _, ok := limiter.AcquireGroup(7, 1); ok {
		t.Fatal("second group acquire allowed at limit")
	}
	globalRelease()
	globalRelease()
	groupRelease()
	groupRelease()
	counts := limiter.Snapshot()
	if counts.Global != 0 || len(counts.Groups) != 0 {
		t.Fatalf("counts after release = %+v", counts)
	}
}

func TestDataPlaneConcurrencyTracksUnlimitedWorkForObservability(t *testing.T) {
	limiter := NewDataPlaneConcurrency()
	globalRelease, ok := limiter.AcquireGlobal(0)
	if !ok || limiter.Snapshot().Global != 1 {
		t.Fatalf("unlimited global acquire = ok:%t snapshot:%+v", ok, limiter.Snapshot())
	}
	groupRelease, ok := limiter.AcquireGroup(9, 0)
	if !ok || limiter.Snapshot().Groups[9] != 1 {
		t.Fatalf("unlimited group acquire = ok:%t snapshot:%+v", ok, limiter.Snapshot())
	}
	groupRelease()
	globalRelease()
}
