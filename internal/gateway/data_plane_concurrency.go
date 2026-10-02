package gateway

import (
	"net/http"

	"gpt-load/internal/dialect"
	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
	"gpt-load/internal/state"
)

func (handler *Handler) acquireGlobalConcurrency(limit int64) (func(), bool) {
	if handler == nil || handler.dataPlane == nil {
		return func() {}, true
	}
	return handler.dataPlane.AcquireGlobal(handler.currentGlobalConcurrencyLimit(limit))
}

func (handler *Handler) acquireGroupConcurrency(group state.GroupView) (func(), bool) {
	if handler == nil || handler.dataPlane == nil {
		return func() {}, true
	}
	if current := handler.currentGroupView(group.ID); current != nil {
		group = *current
	}
	return handler.dataPlane.AcquireGroup(group.ID, group.ConcurrencyLimit)
}

// Current runtime policy wins at the instant of admission. The request may
// have selected its route from an older snapshot while a control-plane update
// is being published; using the latest limit prevents stale configuration from
// admitting work after an operator lowers a cap.
func (handler *Handler) currentGlobalConcurrencyLimit(fallback int64) int64 {
	if handler != nil && handler.manager != nil {
		if snapshot := handler.manager.Current(); snapshot != nil {
			return snapshot.Settings.GlobalConcurrencyLimit
		}
	}
	return fallback
}

func (handler *Handler) currentAccessKeyConcurrencyLimit(accessKeyID uint, fallback int64) int64 {
	if handler != nil && handler.manager != nil {
		if snapshot := handler.manager.Current(); snapshot != nil {
			if accessKey, ok := snapshot.AccessKeysByID[accessKeyID]; ok {
				return accessKey.ConcurrencyLimit
			}
		}
	}
	return fallback
}

func (handler *Handler) currentGroupView(groupID uint) *state.GroupView {
	if handler == nil || handler.manager == nil {
		return nil
	}
	snapshot := handler.manager.Current()
	if snapshot == nil {
		return nil
	}
	group, ok := snapshot.Groups[groupID]
	if !ok {
		return nil
	}
	return &group
}

func (handler *Handler) acquireAccessKeyConcurrency(accessKeyID uint, fallback int64) (func(), bool) {
	if handler == nil || handler.concurrency == nil {
		return func() {}, true
	}
	return handler.concurrency.Acquire(
		accessKeyID,
		handler.currentAccessKeyConcurrencyLimit(accessKeyID, fallback),
	)
}

func concurrencyControlRequest(request *http.Request, route route) bool {
	if request == nil || route.Protocol != protocol.OpenAIResponses {
		return false
	}
	metadata, err := dialect.NewOpenAIResponses().InspectRequest(&dialect.ParsedRequest{
		Method: request.Method, Path: request.URL.Path, RawQuery: request.URL.RawQuery,
	})
	return err == nil && metadata.Operation == execution.OperationResponsesCancel
}
