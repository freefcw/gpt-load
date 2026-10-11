package gateway

import (
	"time"

	"gpt-load/internal/scheduler"
)

func credentialLimitStatus(iterator *scheduler.Iterator, slotRejected bool, slotRetryAt time.Time) (time.Time, bool) {
	if until, limited := iterator.CredentialLimitedUntil(); limited {
		return until, true
	}
	if slotRejected {
		return slotRetryAt, true
	}
	return time.Time{}, false
}

func credentialSlotRetryAfter(limiter scheduler.CredentialLimiter, selection scheduler.Selection) time.Time {
	if limiter == nil {
		return time.Time{}
	}
	return limiter.RetryAfter(selection.CredentialID, selection.CredentialRPMLimit)
}

func earliestRetryAfter(current *time.Time, candidate time.Time) {
	if candidate.IsZero() || (!current.IsZero() && !candidate.Before(*current)) {
		return
	}
	*current = candidate
}
