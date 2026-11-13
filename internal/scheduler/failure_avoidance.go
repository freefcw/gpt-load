package scheduler

import (
	"time"

	"gpt-load/internal/state"
)

// failureAvoidance 是快速失败凭据的避让时长。容量不足等失败不触发健康冷却，
// 名额又在失败后立即归还；不避让时负载优先排序会把它当成最空闲的候选反复首选。
const failureAvoidance = 15 * time.Second

// AvoidFailedCredential 只接受当前身份的反馈；expectedVersion 非零时还要求
// 凭据版本匹配（例如刷新失败）。目标为空表示账号级，否则仅影响该模型和操作。
// 全部候选失败时仍可分配，不改变健康状态，也不影响粘滞与重放。
func AvoidFailedCredential(progress *state.SchedulingState, ref state.CredentialRef, expectedVersion uint64, target state.FailureAvoidanceKey, failedAt time.Time) {
	if progress == nil || !target.Valid() {
		return
	}
	until := failedAt.Add(failureAvoidance)
	progress.WithLock(func(ledger *state.SchedulingLedger) {
		ledger.PruneFailureAvoidance(failedAt)
		member := matchingFailureMember(ledger, ref)
		if member == nil || expectedVersion != 0 && member.Version != expectedVersion {
			return
		}
		member.PruneFailureAvoidance(failedAt)
		if until.After(member.FailureAvoidance[target]) {
			if member.FailureAvoidance == nil {
				member.FailureAvoidance = make(map[state.FailureAvoidanceKey]time.Time)
			}
			member.FailureAvoidance[target] = until
		}
	})
}

// ForgetCredentialFailure 成功只解除账号级和同一模型/操作的避让，
// 不能用其他模型的成功或旧身份的迟到反馈清除当前目标的问题。
func ForgetCredentialFailure(progress *state.SchedulingState, ref state.CredentialRef, target state.FailureAvoidanceKey, succeededAt time.Time) {
	if progress == nil || !target.Valid() {
		return
	}
	progress.WithLock(func(ledger *state.SchedulingLedger) {
		ledger.PruneFailureAvoidance(succeededAt)
		member := matchingFailureMember(ledger, ref)
		if member == nil {
			return
		}
		// Callbacks may acquire the lock out of completion-time order. An older
		// success must not erase a failure that completed after that success.
		for _, key := range []state.FailureAvoidanceKey{{}, target} {
			if !member.FailureAvoidance[key].After(succeededAt.Add(failureAvoidance)) {
				delete(member.FailureAvoidance, key)
			}
		}
		member.PruneFailureAvoidance(succeededAt)
	})
}

func matchingFailureMember(ledger *state.SchedulingLedger, ref state.CredentialRef) *state.SchedulingMember {
	member := ledger.Members[ref.ID]
	if member == nil || member.GroupID != ref.GroupID || member.IdentityGeneration != ref.IdentityGeneration {
		return nil
	}
	return member
}

// A credential is avoided only if every available target in the current tier
// is avoided. A model-A failure must not hide its healthy model-B target.
func (iterator *Iterator) refreshFailureAvoidanceLocked(ledger *state.SchedulingLedger, candidates []weightedCredential, scope selectionScope) {
	now := iterator.now()
	for i := range candidates {
		candidate := &candidates[i]
		candidate.failureAvoided = false
		member := ledger.Members[candidate.meta.ID]
		member.PruneFailureAvoidance(now)
		if len(member.FailureAvoidance) == 0 {
			continue
		}
		candidate.failureAvoided = member.AvoidsFailureTarget(state.FailureAvoidanceKey{}, now)
		if candidate.failureAvoided || scope.pool == nil {
			continue
		}
		candidate.failureAvoided = true
		for _, target := range scope.pool.targetsByGroup[candidate.meta.GroupID] {
			// Use the same eligibility instant as Next/selectTarget. A cooldown
			// expiring mid-selection must not make a model look usable here
			// while it remains excluded from the final target pool.
			if !iterator.targetAvailable(target, candidate.meta, scope.modes, scope.at) {
				continue
			}
			key := state.FailureAvoidanceKey{Model: target.target.UpstreamModelID, Operation: iterator.operation}
			if !member.AvoidsFailureTarget(key, now) {
				candidate.failureAvoided = false
				break
			}
		}
	}
}
