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
	return handler.dataPlane.AcquireGlobal(limit)
}

func (handler *Handler) acquireGroupConcurrency(group state.GroupView) (func(), bool) {
	if handler == nil || handler.dataPlane == nil {
		return func() {}, true
	}
	return handler.dataPlane.AcquireGroup(group.ID, group.ConcurrencyLimit)
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
