package state

import (
	"time"

	"gpt-load/internal/execution"
)

// FailureAvoidanceKey is either credential-wide (the zero value), or specific
// to the actual upstream model and operation, not the client-facing alias.
type FailureAvoidanceKey struct {
	Model     string
	Operation execution.Operation
}

func (key FailureAvoidanceKey) Valid() bool {
	if key.Model == "" {
		return key.Operation == ""
	}
	return key.Operation.Valid()
}

// PruneFailureAvoidance requires the scheduling lock. The global sweep also
// cleans idle members that never get selected again, without adding a timer.
// Active members are pruned separately on access for exact deadline behavior.
func (ledger *SchedulingLedger) PruneFailureAvoidance(now time.Time) {
	const cleanupInterval = time.Minute
	if !ledger.lastFailureCleanup.IsZero() && now.Sub(ledger.lastFailureCleanup) < cleanupInterval {
		return
	}
	for _, member := range ledger.Members {
		member.PruneFailureAvoidance(now)
	}
	ledger.lastFailureCleanup = now
}

// PruneFailureAvoidance requires the scheduling lock. An empty map is released
// so expired or successfully recovered targets retain no allocation.
func (member *SchedulingMember) PruneFailureAvoidance(now time.Time) {
	for key, until := range member.FailureAvoidance {
		if !until.After(now) {
			delete(member.FailureAvoidance, key)
		}
	}
	if len(member.FailureAvoidance) == 0 {
		member.FailureAvoidance = nil
	}
}

// AvoidsFailureTarget reads previously pruned state under the scheduling lock.
func (member *SchedulingMember) AvoidsFailureTarget(target FailureAvoidanceKey, now time.Time) bool {
	return member.FailureAvoidance[FailureAvoidanceKey{}].After(now) ||
		member.FailureAvoidance[target].After(now)
}
