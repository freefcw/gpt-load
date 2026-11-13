package scheduler

import (
	"time"

	"gpt-load/internal/state"
)

// ChargeReplay 为已允许的显式同凭据重放取得名额，不放松普通重试的 tried 去重。
func (iterator *Iterator) ChargeReplay(selection Selection, ref state.CredentialRef) bool {
	iterator.Close()
	if ref.ID != selection.CredentialID || ref.GroupID != selection.GroupID {
		return false
	}
	var charged bool
	consume := func(metas []state.CredentialMeta) {
		for _, meta := range metas {
			if meta.ID != ref.ID || meta.IdentityGeneration != ref.IdentityGeneration {
				continue
			}
			if selection.UpstreamModelID != nil && modelCooldownUntil(meta.ModelCooldowns, *selection.UpstreamModelID, iterator.operation, iterator.now()).After(iterator.now()) {
				continue
			}
			weight := effectiveWeight(selection.Group.WeightManual, meta.WeightManual)
			if weight > 0 {
				_, charged = iterator.selectCredential([]weightedCredential{{meta: meta, weight: weight}}, ref.ID, selectionScope{})
			}
		}
	}
	groups := []uint{ref.GroupID}
	if source, ok := iterator.credentials.(interface {
		WithCredentialCandidates([]uint, func(uint) bool, time.Time, func([]state.CredentialMeta))
	}); ok {
		source.WithCredentialCandidates(groups, nil, iterator.now(), consume)
	} else {
		consume(iterator.credentials.CollectCredentialCandidates(groups, nil, iterator.now()))
	}
	return charged
}

// selectCredential 只操作 detached 的内存候选。
func (iterator *Iterator) selectCredential(candidates []weightedCredential, preferred uint, scope selectionScope) (state.CredentialMeta, bool) {
	var selected state.CredentialMeta
	var found bool
	iterator.progress.WithLock(func(ledger *state.SchedulingLedger) {
		ledger.PruneFailureAvoidance(iterator.now())
		if ledger.Sequence == ^uint64(0) {
			return
		}
		eligible := iterator.admitCandidatesLocked(ledger, candidates)
		if len(eligible) == 0 {
			return
		}
		// 粘滞凭据不参与负载避让；软阈值不改变已建立的亲和关系。
		preferredCandidate, preferredFound := findCandidate(eligible, preferred)
		if !preferredFound {
			iterator.refreshLoadLocked(ledger, eligible)
			iterator.refreshFailureAvoidanceLocked(ledger, eligible, scope)
			eligible = withoutAvoided(eligible)
		}
		less := func(left, right weightedCredential) bool {
			if iterator.usage != nil {
				if order := compareLoad(left, right); order != 0 {
					return order < 0
				}
			}
			a, b := ledger.Members[left.meta.ID], ledger.Members[right.meta.ID]
			if order := a.Progress.Compare(b.Progress); order != 0 {
				return order < 0
			}
			if a.LastSelected != b.LastSelected {
				return a.LastSelected < b.LastSelected
			}
			return a.ID < b.ID
		}
		first := eligible[0]
		for _, candidate := range eligible[1:] {
			if less(candidate, first) {
				first = candidate
			}
		}
		if preferredFound {
			first = preferredCandidate
		} else if ledger.LastMember == first.meta.ID && len(eligible) > 1 && ledger.Consecutive >= 100 {
			var alternative weightedCredential
			for _, candidate := range eligible {
				// Fairness must not send work back to a busier long-running
				// account just because the idle account keeps winning.
				if iterator.usage != nil && compareLoad(first, candidate) != 0 {
					continue
				}
				if candidate.meta.ID != first.meta.ID && (alternative.meta.ID == 0 || less(candidate, alternative)) {
					alternative = candidate
				}
			}
			if alternative.meta.ID != 0 {
				limit := uint64(99 + (first.weight+alternative.weight-1)/alternative.weight)
				if ledger.Consecutive >= limit {
					first = alternative
				}
			}
		}
		member := ledger.Members[first.meta.ID]
		next, ok := member.Progress.Advance(uint64(first.weight))
		if !ok {
			return
		}
		member.Progress = next
		ledger.Sequence++
		member.LastSelected = ledger.Sequence
		ledger.Started = true
		if next.Compare(ledger.Watermark) > 0 {
			ledger.Watermark = next
		}
		if ledger.LastMember != member.ID {
			ledger.LastMember, ledger.Consecutive = member.ID, 1
		} else if ledger.Consecutive < 99+state.MaxWeight*state.MaxWeight {
			// 超过所有合法阈值后无需继续增长；亲和和单候选仍可持续分配。
			ledger.Consecutive++
		}
		iterator.reserveCredentialLocked(ledger, first.meta.ID)
		selected, found = first.meta, true
	})
	return selected, found
}

// admitCandidatesLocked checks detached identities and aligns newly admitted
// members without mixing lifecycle calibration with load-selection policy.
func (iterator *Iterator) admitCandidatesLocked(ledger *state.SchedulingLedger, candidates []weightedCredential) []weightedCredential {
	eligible := candidates[:0]
	baseline := ledger.Watermark
	hasBaseline := false
	for _, candidate := range candidates {
		member := ledger.Members[candidate.meta.ID]
		if member == nil || member.GroupID != candidate.meta.GroupID ||
			member.IdentityGeneration != candidate.meta.IdentityGeneration {
			continue
		}
		eligible = append(eligible, candidate)
		if !member.Pending && (!hasBaseline || member.Progress.Compare(baseline) > 0) {
			baseline, hasBaseline = member.Progress, true
		}
	}
	// 同批成员共用已有成员的领先进度，不能继承落后成员的历史欠额。
	// 入场对齐本组在轮转成员的最前进度；整组待入场时才沿用候选池基线。
	var frontiers map[uint]state.SchedulingProgress
	for _, candidate := range eligible {
		member := ledger.Members[candidate.meta.ID]
		// 旧请求可以继续记账，但不能用过期候选池完成新配置的入场校准。
		if !member.Pending || ledger.GroupsKnown && !ledger.Groups[member.GroupID] ||
			iterator.snapshot == nil || iterator.snapshot.Revision < ledger.GroupRevision {
			continue
		}
		if frontiers == nil {
			frontiers = groupFrontiers(ledger)
		}
		if frontier, ok := frontiers[member.GroupID]; ok {
			member.Admit(frontier)
		} else {
			member.Admit(baseline)
		}
	}
	return eligible
}

// findCandidate 在候选中定位粘滞凭据。
func findCandidate(candidates []weightedCredential, id uint) (weightedCredential, bool) {
	for _, candidate := range candidates {
		if candidate.meta.ID == id {
			return candidate, true
		}
	}
	return weightedCredential{}, false
}

// withoutAvoided 先避让失败候选，再在健康候选中避让软饱和。
// 两层分别回退：健康但较忙不能导致刚失败的空闲凭据重新入选。
func withoutAvoided(candidates []weightedCredential) []weightedCredential {
	healthy := candidates[:0]
	for _, candidate := range candidates {
		if !candidate.failureAvoided {
			healthy = append(healthy, candidate)
		}
	}
	if len(healthy) > 0 {
		candidates = healthy
	}
	fresh := candidates[:0]
	for _, candidate := range candidates {
		if !candidate.softSaturated() {
			fresh = append(fresh, candidate)
		}
	}
	if len(fresh) == 0 {
		return candidates
	}
	return fresh
}

// compareLoad 按当前在途/权重、RPM/权重依次比较，均相等时回到 Progress。
// 两个维度都不加一：空闲或 RPM 窗口过期时必须回到历史加权轮转，
// 不能让高权重账号凭空获得更低的“负载”。待执行请求由预留计数体现。
func compareLoad(left, right weightedCredential) int {
	if order := comparePerWeight(left.inFlight, left.weight, right.inFlight, right.weight); order != 0 {
		return order
	}
	return comparePerWeight(left.rpmUsed, left.weight, right.rpmUsed, right.weight)
}

// comparePerWeight 比较 leftUsed/leftWeight 与 rightUsed/rightWeight 的大小。
func comparePerWeight(leftUsed, leftWeight, rightUsed, rightWeight int64) int {
	l, r := leftUsed*rightWeight, rightUsed*leftWeight
	switch {
	case l < r:
		return -1
	case l > r:
		return 1
	}
	return 0
}

// groupFrontiers 只在有成员入场时扫描账本，返回各分组在轮转成员的最前进度。
func groupFrontiers(ledger *state.SchedulingLedger) map[uint]state.SchedulingProgress {
	frontiers := make(map[uint]state.SchedulingProgress)
	for _, member := range ledger.Members {
		if member.Pending {
			continue
		}
		if current, ok := frontiers[member.GroupID]; !ok || member.Progress.Compare(current) > 0 {
			frontiers[member.GroupID] = member.Progress
		}
	}
	return frontiers
}
