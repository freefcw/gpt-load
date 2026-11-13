package scheduler

import "gpt-load/internal/state"

// Close cancels a selection that has not acquired its real credential slot.
// Callers must defer Close after New, including on preparation failure and
// cancellation. Next and ChargeReplay also cancel the previous reservation.
// It is idempotent and does not release a committed request or live session.
// Like Next, it must not run concurrently on the same Iterator.
func (iterator *Iterator) Close() {
	if iterator == nil || iterator.progress == nil || iterator.reservedCredentialID == 0 {
		return
	}
	iterator.progress.WithLock(iterator.releaseReservationLocked)
}

// AcquireCredential replaces pending scheduling load with the real limiter
// charge, without exposing a gap or double-count to another selection. acquire
// must only perform local limiter accounting: it must not do IO, call back into
// the scheduler, or acquire the credential registry/configuration locks.
// Lock order is SchedulingState -> limiter; the limiter must not take the
// scheduling lock. On failure the pending load is canceled without RPM charge.
func (iterator *Iterator) AcquireCredential(acquire func() (func(), bool)) (release func(), allowed bool) {
	if iterator == nil || iterator.reservedCredentialID == 0 {
		return acquire()
	}
	iterator.progress.WithLock(func(ledger *state.SchedulingLedger) {
		defer iterator.releaseReservationLocked(ledger)
		release, allowed = acquire()
	})
	return release, allowed
}

func (iterator *Iterator) releaseReservationLocked(ledger *state.SchedulingLedger) {
	id := iterator.reservedCredentialID
	if id == 0 {
		return
	}
	if ledger.Reservations[id] <= 1 {
		delete(ledger.Reservations, id)
	} else {
		ledger.Reservations[id]--
	}
	iterator.reservedCredentialID = 0
}

func (iterator *Iterator) reserveCredentialLocked(ledger *state.SchedulingLedger, id uint) {
	if iterator.usage == nil {
		return
	}
	ledger.Reservations[id]++
	iterator.reservedCredentialID = id
}

// Read real load only after entering the shared selection lock. Reservations
// are added to both dimensions: they represent imminent work, not charged RPM.
func (iterator *Iterator) refreshLoadLocked(ledger *state.SchedulingLedger, candidates []weightedCredential) {
	if iterator.usage == nil {
		return
	}
	for i := range candidates {
		candidate := &candidates[i]
		rpm, inFlight := iterator.usage.Usage(candidate.meta.ID)
		pending := ledger.Reservations[candidate.meta.ID]
		candidate.rpmUsed, candidate.inFlight = rpm+pending, inFlight+pending
	}
}
