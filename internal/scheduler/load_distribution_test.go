package scheduler

import (
	"fmt"
	"testing"

	"gpt-load/internal/ratelimit"
	"gpt-load/internal/state"
)

func TestLoadAwareSequentialTrafficPreservesWeights(t *testing.T) {
	for _, weights := range [][2]int{{1, 1}, {2, 1}, {40, 5}, {100, 99}} {
		t.Run(fmt.Sprint(weights), func(t *testing.T) {
			r := newFairnessRegistry(t)
			snapshot := schedulerSnapshot()
			for i, id := range []uint{11, 12} {
				if err := r.UpdateCredentialConfig(id, state.CredentialStatusActive, &weights[i]); err != nil {
					t.Fatal(err)
				}
			}
			limiter := ratelimit.NewCredentialLimiter()
			query := fairnessQuery(0)
			query.Limiter = limiter
			counts := map[uint]int{}
			const rounds = 10
			for range rounds * (weights[0] + weights[1]) {
				iterator := New(snapshot, r, query)
				selection, err := iterator.Next()
				if err != nil {
					iterator.Close()
					t.Fatal(err)
				}
				release, ok := iterator.AcquireCredential(func() (func(), bool) {
					return limiter.Acquire(selection.CredentialID, 0, 0)
				})
				iterator.Close()
				if !ok {
					t.Fatal("unlimited acquire rejected")
				}
				counts[selection.CredentialID]++
				release()
			}
			for i, id := range []uint{11, 12} {
				if got, want := counts[id], rounds*weights[i]; got != want {
					t.Fatalf("distribution %v, credential %d = %d, want %d", counts, id, got, want)
				}
			}
		})
	}
}

func TestLoadAwareDoesNotForceTrafficOntoLongRunningAccount(t *testing.T) {
	r := newFairnessRegistry(t)
	snapshot := schedulerSnapshot()
	limiter := ratelimit.NewCredentialLimiter()
	for range 10 {
		release, ok := limiter.Acquire(12, 0, 0)
		if !ok {
			t.Fatal("initial acquire rejected")
		}
		defer release()
	}
	query := fairnessQuery(0)
	query.Limiter = limiter
	for i := range 350 {
		iterator := New(snapshot, r, query)
		selection, err := iterator.Next()
		if err != nil {
			iterator.Close()
			t.Fatal(err)
		}
		if selection.CredentialID != 11 {
			iterator.Close()
			t.Fatalf("pick %d forced traffic onto busy credential %d", i+1, selection.CredentialID)
		}
		release, ok := iterator.AcquireCredential(func() (func(), bool) {
			return limiter.Acquire(selection.CredentialID, 0, 0)
		})
		iterator.Close()
		if !ok {
			t.Fatal("unlimited acquire rejected")
		}
		release()
	}
}

func TestLoadAwareWeightedPendingAndActiveRequests(t *testing.T) {
	r := newFairnessRegistry(t)
	snapshot := schedulerSnapshot()
	weight := 100
	if err := r.UpdateCredentialConfig(11, state.CredentialStatusActive, &weight); err != nil {
		t.Fatal(err)
	}
	// Credential 12 retains default weight 50: total live load should be 2:1,
	// whether a request is still preparing or has already acquired a slot.
	limiter := ratelimit.NewCredentialLimiter()
	query := fairnessQuery(0)
	query.Limiter = limiter
	counts := map[uint]int{}
	for i := range 600 {
		iterator := New(snapshot, r, query)
		defer iterator.Close()
		selection, err := iterator.Next()
		if err != nil {
			t.Fatal(err)
		}
		counts[selection.CredentialID]++
		if i%2 == 0 {
			release, ok := iterator.AcquireCredential(func() (func(), bool) {
				return limiter.Acquire(selection.CredentialID, 0, 0)
			})
			if !ok {
				t.Fatal("unlimited acquire rejected")
			}
			defer release()
			iterator.Close()
		}
	}
	if counts[11] != 400 || counts[12] != 200 {
		t.Fatalf("mixed pending/active distribution = %v, want 400:200", counts)
	}
}

func TestCompareLoadIsTransitiveAcrossUnequalWeights(t *testing.T) {
	var candidates []weightedCredential
	for _, weight := range []int64{1, 2, 4} {
		for _, inFlight := range []int64{0, 1, 3} {
			for _, rpm := range []int64{0, 2, 9} {
				candidates = append(candidates, weightedCredential{weight: weight, inFlight: inFlight, rpmUsed: rpm})
			}
		}
	}
	for _, a := range candidates {
		for _, b := range candidates {
			if compareLoad(a, b) != -compareLoad(b, a) {
				t.Fatalf("comparison not antisymmetric: %+v / %+v", a, b)
			}
			for _, c := range candidates {
				if compareLoad(a, b) <= 0 && compareLoad(b, c) <= 0 && compareLoad(a, c) > 0 {
					t.Fatalf("comparison not transitive: %+v / %+v / %+v", a, b, c)
				}
			}
		}
	}
}
