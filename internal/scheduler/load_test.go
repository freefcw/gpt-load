package scheduler

import (
	"testing"
	"time"

	"gpt-load/internal/ratelimit"
	"gpt-load/internal/state"
)

// loadLimiter 按注入的用量参与负载感知选号；blocked 中的凭据按硬上限满额排除。
type loadLimiter struct {
	usage   map[uint][2]int64 // {rpmUsed, inFlight}
	blocked map[uint]struct{}
}

func (limiter loadLimiter) Available(credentialID uint, rpmLimit, concurrencyLimit int64) bool {
	if rpmLimit <= 0 && concurrencyLimit <= 0 {
		return true
	}
	_, blocked := limiter.blocked[credentialID]
	return !blocked
}

func (loadLimiter) RetryAfter(uint, int64) time.Time { return time.Time{} }

func (limiter loadLimiter) Usage(credentialID uint) (int64, int64) {
	usage := limiter.usage[credentialID]
	return usage[0], usage[1]
}

func loadPick(t *testing.T, snapshot *state.ConfigSnapshot, registry *state.CredentialRegistry, preferred uint, limiter CredentialLimiter) uint {
	t.Helper()
	query := fairnessQuery(preferred)
	query.Limiter = limiter
	iterator := New(snapshot, registry, query)
	defer iterator.Close()
	selection, err := iterator.Next()
	if err != nil {
		t.Fatal(err)
	}
	return selection.CredentialID
}

func TestLoadAwarePrefersIdleCredentialOverLowerProgress(t *testing.T) {
	r := newFairnessRegistry(t)
	snapshot := schedulerSnapshot()
	limiter := loadLimiter{usage: map[uint][2]int64{}}
	// 粘滞请求推高 11 的 Progress；非粘滞时 12 在途 5、11 空闲，仍应选 11。
	for range 3 {
		loadPick(t, snapshot, r, 11, limiter)
	}
	limiter.usage[12] = [2]int64{0, 5}
	if got := loadPick(t, snapshot, r, 0, limiter); got != 11 {
		t.Fatalf("selected %d, want idle credential 11 despite its larger progress", got)
	}
}

func TestLoadAwareScalesInFlightByWeight(t *testing.T) {
	r := newFairnessRegistry(t)
	snapshot := schedulerSnapshot()
	four, one := 4, 1
	if err := r.UpdateCredentialConfig(11, state.CredentialStatusActive, &four); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateCredentialConfig(12, state.CredentialStatusActive, &one); err != nil {
		t.Fatal(err)
	}
	limiter := loadLimiter{usage: map[uint][2]int64{11: {0, 6}, 12: {0, 3}}}
	// 当前在途/权重：11 为 6/4，小于 12 的 3/1，即使 Progress 更高也应选 11。
	for range 4 {
		loadPick(t, snapshot, r, 11, limiter)
	}
	if got := loadPick(t, snapshot, r, 0, limiter); got != 11 {
		t.Fatalf("selected %d, want lower in-flight/weight credential 11", got)
	}
}

func TestLoadAwareFallsBackToRPMPerWeight(t *testing.T) {
	r := newFairnessRegistry(t)
	snapshot := schedulerSnapshot()
	limiter := loadLimiter{usage: map[uint][2]int64{11: {0, 2}, 12: {5, 2}}}
	for range 3 {
		loadPick(t, snapshot, r, 11, limiter)
	}
	// 当前在途/权重相同，落到当前 RPM 比值：0/1 < 5/1。
	if got := loadPick(t, snapshot, r, 0, limiter); got != 11 {
		t.Fatalf("selected %d, want lower rpm/weight credential 11", got)
	}
}

func TestLoadAwareZeroLoadUsesWeightedProgress(t *testing.T) {
	r := newFairnessRegistry(t)
	snapshot := schedulerSnapshot()
	five, forty := 5, 40
	if err := r.UpdateCredentialConfig(11, state.CredentialStatusActive, &five); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateCredentialConfig(12, state.CredentialStatusActive, &forty); err != nil {
		t.Fatal(err)
	}
	// 所有实时计数为零时回到 Progress，仍按权重轮转而不是永远优选 40。
	limiter := loadLimiter{usage: map[uint][2]int64{}}
	counts := map[uint]int{}
	for range 450 {
		counts[loadPick(t, snapshot, r, 0, limiter)]++
	}
	if counts[11] != 50 || counts[12] != 400 {
		t.Fatalf("zero-load distribution = %v, want 50:400", counts)
	}
}

func TestLoadAwarePrefersIdleOverBusyHigherWeight(t *testing.T) {
	r := newFairnessRegistry(t)
	snapshot := schedulerSnapshot()
	forty, twenty := 40, 20
	if err := r.UpdateCredentialConfig(11, state.CredentialStatusActive, &forty); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateCredentialConfig(12, state.CredentialStatusActive, &twenty); err != nil {
		t.Fatal(err)
	}
	// 3/40 > 0/20：空闲的权重 20 凭据胜出，权重不应压过空闲状态。
	limiter := loadLimiter{usage: map[uint][2]int64{11: {0, 3}}}
	if got := loadPick(t, snapshot, r, 0, limiter); got != 12 {
		t.Fatalf("selected %d, want idle credential 12", got)
	}
}

func TestLoadAwareZeroLoadKeepsProgressRotation(t *testing.T) {
	r := newFairnessRegistry(t)
	snapshot := schedulerSnapshot()
	limiter := loadLimiter{usage: map[uint][2]int64{}}
	for i, want := range []uint{11, 12, 11, 12} {
		if got := loadPick(t, snapshot, r, 0, limiter); got != want {
			t.Fatalf("pick %d selected %d, want %d (zero load must keep progress rotation)", i, got, want)
		}
	}
}

func TestLoadAwareDropsSoftSaturatedCandidate(t *testing.T) {
	r := newFairnessRegistry(t)
	snapshot := schedulerSnapshot()
	group := snapshot.Groups[1]
	group.CredentialConcurrencyLimit = 10
	snapshot.Groups[1] = group
	two, one := 2, 1
	if err := r.UpdateCredentialConfig(11, state.CredentialStatusActive, &two); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateCredentialConfig(12, state.CredentialStatusActive, &one); err != nil {
		t.Fatal(err)
	}
	// 11 在途 7/10 达到七成阈值被剔除，尽管 7/2 的在途权重比优于 12 的 6/1。
	limiter := loadLimiter{usage: map[uint][2]int64{11: {0, 7}, 12: {0, 6}}}
	if got := loadPick(t, snapshot, r, 0, limiter); got != 12 {
		t.Fatalf("selected %d, want 12 (11 is soft-saturated at 70%%)", got)
	}
}

func TestLoadAwareFallsBackWhenAllSoftSaturated(t *testing.T) {
	r := newFairnessRegistry(t)
	snapshot := schedulerSnapshot()
	group := snapshot.Groups[1]
	group.CredentialConcurrencyLimit = 10
	snapshot.Groups[1] = group
	limiter := loadLimiter{usage: map[uint][2]int64{11: {0, 7}, 12: {0, 8}}}
	// 全部软饱和时退回全体，按 inFlight/weight 选最小的 11。
	if got := loadPick(t, snapshot, r, 0, limiter); got != 11 {
		t.Fatalf("selected %d, want least-loaded credential 11 when all are soft-saturated", got)
	}
}

func TestLoadAwareStickyBypassesSoftSaturation(t *testing.T) {
	r := newFairnessRegistry(t)
	snapshot := schedulerSnapshot()
	group := snapshot.Groups[1]
	group.CredentialConcurrencyLimit = 10
	snapshot.Groups[1] = group
	limiter := loadLimiter{usage: map[uint][2]int64{11: {0, 7}, 12: {0, 0}}}
	if got := loadPick(t, snapshot, r, 11, limiter); got != 11 {
		t.Fatalf("sticky selected %d, want preferred credential 11 despite soft saturation", got)
	}
}

func TestLoadAwareStickyFallsBackAtHardLimit(t *testing.T) {
	r := newFairnessRegistry(t)
	snapshot := schedulerSnapshot()
	group := snapshot.Groups[1]
	group.CredentialConcurrencyLimit = 10
	snapshot.Groups[1] = group
	limiter := loadLimiter{
		usage:   map[uint][2]int64{11: {0, 10}},
		blocked: map[uint]struct{}{11: {}},
	}
	if got := loadPick(t, snapshot, r, 11, limiter); got != 12 {
		t.Fatalf("sticky selected %d, want fallback to 12 when preferred hits the hard limit", got)
	}
}

func TestSoftSaturatedBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		candidate weightedCredential
		want      bool
	}{
		{"concurrency below threshold", weightedCredential{inFlight: 4, concurrencyLimit: 6}, false},
		{"concurrency at threshold", weightedCredential{inFlight: 5, concurrencyLimit: 6}, true},
		{"rpm below threshold", weightedCredential{rpmUsed: 20, rpmLimit: 30}, false},
		{"rpm at threshold", weightedCredential{rpmUsed: 21, rpmLimit: 30}, true},
		{"no limits never saturates", weightedCredential{inFlight: 99, rpmUsed: 99}, false},
		{"limit one idle", weightedCredential{concurrencyLimit: 1}, false},
		{"limit one full", weightedCredential{inFlight: 1, concurrencyLimit: 1}, true},
		{"limit two remaining slot", weightedCredential{inFlight: 1, concurrencyLimit: 2}, false},
		{"limit two full", weightedCredential{inFlight: 2, concurrencyLimit: 2}, true},
		{"limit three remaining slot", weightedCredential{rpmUsed: 2, rpmLimit: 3}, false},
		{"limit three full", weightedCredential{rpmUsed: 3, rpmLimit: 3}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.candidate.softSaturated(); got != test.want {
				t.Fatalf("softSaturated() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestLoadAwareDropsRPMSaturatedCandidate(t *testing.T) {
	r := newFairnessRegistry(t)
	snapshot := schedulerSnapshot()
	group := snapshot.Groups[1]
	group.CredentialRPMLimit = 10
	snapshot.Groups[1] = group
	// 11 的 RPM 用量 7/10 达到七成阈值被剔除，尽管它 in-flight 更低、不剔除时按负载应选它。
	limiter := loadLimiter{usage: map[uint][2]int64{11: {7, 0}, 12: {0, 5}}}
	if got := loadPick(t, snapshot, r, 0, limiter); got != 12 {
		t.Fatalf("selected %d, want 12 (11 is RPM soft-saturated at 70%%)", got)
	}
}

func TestLoadAwareWithRealCredentialLimiter(t *testing.T) {
	r := newFairnessRegistry(t)
	snapshot := schedulerSnapshot()
	limiter := ratelimit.NewCredentialLimiter()
	releases := make([]func(), 0, 3)
	for range 3 {
		release, ok := limiter.Acquire(11, 0, 0)
		if !ok {
			t.Fatal("acquire rejected")
		}
		releases = append(releases, release)
	}
	query := fairnessQuery(0)
	query.Limiter = limiter
	iterator := New(snapshot, r, query)
	defer iterator.Close()
	selection, err := iterator.Next()
	if err != nil {
		t.Fatal(err)
	}
	// 等权重下 11 有 3 个在途，真实限流器的用量应让选择落到 12。
	if selection.CredentialID != 12 {
		t.Fatalf("selected %d, want 12 (11 has 3 in-flight on the real limiter)", selection.CredentialID)
	}
	for _, release := range releases {
		release()
	}
	if _, inFlight := limiter.Usage(11); inFlight != 0 {
		t.Fatalf("in-flight after release = %d, want 0", inFlight)
	}
}

func TestLoadAwareAvoidsRecentlyFailedCredential(t *testing.T) {
	r := newFairnessRegistry(t)
	snapshot := schedulerSnapshot()
	progress := r.SchedulingState()
	// 11 快速失败后名额已归还、在途为 0；12 有 3 个在途。不避让时 11 会因最空闲被首选。
	limiter := loadLimiter{usage: map[uint][2]int64{12: {0, 3}}}
	ref11, _ := r.CredentialRef(11)
	ref12, _ := r.CredentialRef(12)
	AvoidFailedCredential(progress, ref11, 0, state.FailureAvoidanceKey{}, time.Now())
	if got := loadPick(t, snapshot, r, 0, limiter); got != 12 {
		t.Fatalf("selected %d, want 12 while failed credential 11 is avoided", got)
	}
	if got := loadPick(t, snapshot, r, 11, limiter); got != 11 {
		t.Fatalf("sticky request selected %d, want its affinity 11", got)
	}
	// 全部候选都需避让时仍按负载分配，不能因此无候选。
	AvoidFailedCredential(progress, ref12, 0, state.FailureAvoidanceKey{}, time.Now())
	if got := loadPick(t, snapshot, r, 0, limiter); got != 11 {
		t.Fatalf("selected %d, want least-loaded 11 when every candidate is avoided", got)
	}
	ForgetCredentialFailure(progress, ref12, state.FailureAvoidanceKey{}, time.Now())
	if got := loadPick(t, snapshot, r, 0, limiter); got != 12 {
		t.Fatalf("selected %d, want 12 once its success cleared the avoidance", got)
	}
}

func TestLoadAwareFailureAvoidanceExpires(t *testing.T) {
	r := newFairnessRegistry(t)
	snapshot := schedulerSnapshot()
	limiter := loadLimiter{usage: map[uint][2]int64{12: {0, 3}}}
	ref, _ := r.CredentialRef(11)
	AvoidFailedCredential(r.SchedulingState(), ref, 0, state.FailureAvoidanceKey{}, time.Now().Add(-failureAvoidance))
	if got := loadPick(t, snapshot, r, 0, limiter); got != 11 {
		t.Fatalf("selected %d, want idle 11 after its avoidance expired", got)
	}
}

func TestFailureAvoidancePrefersHealthySoftSaturatedCredential(t *testing.T) {
	r := newFairnessRegistry(t)
	snapshot := schedulerSnapshot()
	group := snapshot.Groups[1]
	group.CredentialConcurrencyLimit = 10
	snapshot.Groups[1] = group
	limiter := loadLimiter{usage: map[uint][2]int64{12: {0, 7}}}
	ref, _ := r.CredentialRef(11)
	AvoidFailedCredential(r.SchedulingState(), ref, 0, state.FailureAvoidanceKey{}, time.Now())
	for range 20 {
		if got := loadPick(t, snapshot, r, 0, limiter); got != 12 {
			t.Fatalf("selected %d, want healthy 12 even at 7/10 load", got)
		}
	}
}

func TestAvoidanceFallbackLayers(t *testing.T) {
	for _, test := range []struct {
		name       string
		candidates []weightedCredential
		want       []uint
	}{
		{"healthy before failed idle", []weightedCredential{
			{meta: state.CredentialMeta{ID: 11}, failureAvoided: true},
			{meta: state.CredentialMeta{ID: 12}, inFlight: 7, concurrencyLimit: 10},
		}, []uint{12}},
		{"prefer unsaturated healthy", []weightedCredential{
			{meta: state.CredentialMeta{ID: 11}, failureAvoided: true},
			{meta: state.CredentialMeta{ID: 12}, inFlight: 7, concurrencyLimit: 10},
			{meta: state.CredentialMeta{ID: 13}},
		}, []uint{13}},
		{"all failed still filter saturation", []weightedCredential{
			{meta: state.CredentialMeta{ID: 11}, failureAvoided: true},
			{meta: state.CredentialMeta{ID: 12}, failureAvoided: true, inFlight: 7, concurrencyLimit: 10},
		}, []uint{11}},
		{"all failed and saturated stay available", []weightedCredential{
			{meta: state.CredentialMeta{ID: 11}, failureAvoided: true, inFlight: 8, concurrencyLimit: 10},
			{meta: state.CredentialMeta{ID: 12}, failureAvoided: true, inFlight: 7, concurrencyLimit: 10},
		}, []uint{11, 12}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := withoutAvoided(test.candidates)
			if len(got) != len(test.want) {
				t.Fatalf("candidate count = %d, want %d", len(got), len(test.want))
			}
			for i, candidate := range got {
				if candidate.meta.ID != test.want[i] {
					t.Fatalf("candidate %d = %d, want %d", i, candidate.meta.ID, test.want[i])
				}
			}
		})
	}
}
