package gateway

import "gpt-load/internal/state"

// liveRequestLease owns the Access Key and global slots for one create request.
// The slots move to the stored live session only after the session is registered.
type liveRequestLease struct {
	releaseRequest func()
	releaseGlobal  func()
	handedOff      bool
}

func (handler *Handler) acquireLiveRequestLease(request *dataPlaneRequestContext) (*liveRequestLease, *reason) {
	releaseRequest, allowed := handler.concurrency.Acquire(request.accessKey.ID, request.accessKey.ConcurrencyLimit)
	if !allowed {
		return nil, &reasonAccessKeyConcurrencyLimited
	}
	releaseGlobal, allowed := handler.acquireGlobalConcurrency(request.snapshot.Settings.GlobalConcurrencyLimit)
	if !allowed {
		releaseRequest()
		return nil, &reasonConcurrencyLimitExceeded
	}
	return &liveRequestLease{releaseRequest: releaseRequest, releaseGlobal: releaseGlobal}, nil
}

func (lease *liveRequestLease) releaseOnReturn() {
	if lease == nil || lease.handedOff {
		return
	}
	lease.handedOff = true
	lease.releaseGlobal()
	lease.releaseRequest()
}

func (lease *liveRequestLease) handoff() func() {
	if lease == nil || lease.handedOff {
		return func() {}
	}
	lease.handedOff = true
	return func() {
		lease.releaseGlobal()
		lease.releaseRequest()
	}
}

type liveAttemptLease struct {
	release   func()
	handedOff bool
}

func (handler *Handler) acquireLiveAttemptLease(group state.GroupView) (*liveAttemptLease, bool) {
	release, allowed := handler.acquireGroupConcurrency(group)
	if !allowed {
		return nil, false
	}
	return &liveAttemptLease{release: release}, true
}

func (lease *liveAttemptLease) releaseOnReturn() {
	if lease == nil || lease.handedOff {
		return
	}
	lease.handedOff = true
	lease.release()
}

func (lease *liveAttemptLease) handoff() func() {
	if lease == nil || lease.handedOff {
		return func() {}
	}
	lease.handedOff = true
	return lease.release
}
