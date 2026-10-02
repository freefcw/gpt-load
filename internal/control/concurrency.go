package control

// ConcurrencyView is the operator-facing snapshot of one data-plane limit.
type ConcurrencyView struct {
	Current int64 `json:"current"`
	Limit   int64 `json:"limit"`
}

// DataPlaneConcurrencyUsage carries process-local aggregate usage to control
// plane responses without exposing the gateway limiter implementation.
type DataPlaneConcurrencyUsage struct {
	Global int64
	Groups map[uint]int64
}

func (s *Service) dataPlaneUsage() DataPlaneConcurrencyUsage {
	if s == nil || s.limitUsage == nil || !s.limitUsage.DataPlaneConfigured() {
		return DataPlaneConcurrencyUsage{}
	}
	return s.limitUsage.DataPlaneUsage()
}

func (s *Service) dataPlaneConfigured() bool {
	return s != nil && s.limitUsage != nil && s.limitUsage.DataPlaneConfigured()
}

func dataPlaneConcurrencyView(current, limit int64) ConcurrencyView {
	return ConcurrencyView{Current: current, Limit: limit}
}
