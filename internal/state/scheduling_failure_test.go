package state

import (
	"testing"
	"time"

	"gpt-load/internal/execution"
)

func failureMemberView(id, group uint, generation, version uint64) CredentialRuntimeView {
	return CredentialRuntimeView{
		ID: id, GroupID: group, IdentityGeneration: generation, Version: version,
		Status: CredentialStatusActive,
	}
}

func TestFailureAvoidanceFollowsMemberLifecycle(t *testing.T) {
	for _, scenario := range []string{"remove", "identity_replacement", "group_move", "replace_all", "replace_group"} {
		t.Run(scenario, func(t *testing.T) {
			s := NewSchedulingState()
			s.SyncCredentials(0, []CredentialRuntimeView{
				failureMemberView(1, 1, 1, 1), failureMemberView(2, 2, 1, 1),
			})
			s.WithLock(func(ledger *SchedulingLedger) {
				for _, member := range ledger.Members {
					member.FailureAvoidance = map[FailureAvoidanceKey]time.Time{{}: time.Now().Add(time.Minute)}
				}
			})
			switch scenario {
			case "remove":
				s.Remove(1)
			case "identity_replacement":
				s.SyncCredential(failureMemberView(1, 1, 2, 2))
			case "group_move":
				s.SyncCredential(failureMemberView(1, 3, 1, 2))
			case "replace_all":
				s.SyncCredentials(0, []CredentialRuntimeView{failureMemberView(2, 2, 1, 1)})
			case "replace_group":
				s.SyncCredentials(1, nil)
			}
			s.WithLock(func(ledger *SchedulingLedger) {
				if member := ledger.Members[1]; member != nil && len(member.FailureAvoidance) != 0 {
					t.Fatalf("%s retained old avoidance: %+v", scenario, member.FailureAvoidance)
				}
				if len(ledger.Members[2].FailureAvoidance) != 1 {
					t.Fatal("unrelated member lost its avoidance")
				}
			})
		})
	}
}

func TestFailureAvoidanceSurvivesSameIdentityTokenRefresh(t *testing.T) {
	s := NewSchedulingState()
	s.SyncCredential(failureMemberView(1, 1, 1, 1))
	s.WithLock(func(ledger *SchedulingLedger) {
		ledger.Members[1].FailureAvoidance = map[FailureAvoidanceKey]time.Time{{}: time.Now().Add(time.Minute)}
	})
	s.SyncCredential(failureMemberView(1, 1, 1, 2))
	s.WithLock(func(ledger *SchedulingLedger) {
		member := ledger.Members[1]
		if member.Version != 2 || len(member.FailureAvoidance) != 1 {
			t.Fatalf("refresh must update version without discarding identity state: %+v", member)
		}
	})
}

func TestFailureAvoidancePrunesExactlyAndSweepsIdleMembers(t *testing.T) {
	s := NewSchedulingState()
	s.SyncCredentials(0, []CredentialRuntimeView{
		failureMemberView(1, 1, 1, 1), failureMemberView(2, 1, 1, 1),
	})
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	a := FailureAvoidanceKey{Model: "a", Operation: execution.OperationChatCompletion}
	b := FailureAvoidanceKey{Model: "b", Operation: execution.OperationChatCompletion}
	s.WithLock(func(ledger *SchedulingLedger) {
		ledger.Members[1].FailureAvoidance = map[FailureAvoidanceKey]time.Time{
			a: base.Add(15 * time.Second), b: base.Add(2 * time.Minute),
		}
		ledger.Members[2].FailureAvoidance = map[FailureAvoidanceKey]time.Time{a: base.Add(15 * time.Second)}
		ledger.PruneFailureAvoidance(base)
		ledger.Members[1].PruneFailureAvoidance(base.Add(15*time.Second - time.Nanosecond))
		if len(ledger.Members[1].FailureAvoidance) != 2 {
			t.Fatal("entry removed before its deadline")
		}
		ledger.Members[1].PruneFailureAvoidance(base.Add(15 * time.Second))
		if _, exists := ledger.Members[1].FailureAvoidance[a]; exists {
			t.Fatal("access retained entry at its exact deadline")
		}
		ledger.PruneFailureAvoidance(base.Add(15 * time.Second))
		if len(ledger.Members[2].FailureAvoidance) != 1 {
			t.Fatal("global cleanup ran more than once per minute")
		}
		ledger.PruneFailureAvoidance(base.Add(time.Minute))
		if ledger.Members[2].FailureAvoidance != nil {
			t.Fatal("global sweep retained expired state on idle member")
		}
		if !ledger.Members[1].AvoidsFailureTarget(b, base.Add(time.Minute)) {
			t.Fatal("sweep removed an active avoidance entry")
		}
	})
}

func TestFailureAvoidanceIsNotCheckpointed(t *testing.T) {
	s := NewSchedulingState()
	view := failureMemberView(1, 1, 1, 1)
	s.SyncCredential(view)
	s.WithLock(func(ledger *SchedulingLedger) {
		ledger.Members[1].FailureAvoidance = map[FailureAvoidanceKey]time.Time{{}: time.Now().Add(time.Minute)}
	})
	restarted := NewSchedulingState()
	restarted.SyncCredential(view)
	restarted.RestoreCheckpoint(s.CaptureCheckpoint())
	restarted.WithLock(func(ledger *SchedulingLedger) {
		if ledger.Members[1].FailureAvoidance != nil {
			t.Fatal("process-local failure avoidance survived a restart")
		}
	})
}
