package control

// CredentialLimitUsage 读取网关进程内的凭据限额计数，不落库。
// RPM 是 60 秒滑动窗口，并发是尚未归还的在途请求；对应上限小于等于 0 时不记账。
type CredentialLimitUsage interface {
	Usage(credentialID uint) (rpmUsed, inFlight int64)
}

func (s *Service) credentialLiveUsage(credentialID uint) (rpmUsed, inFlight int64) {
	if s == nil || s.limitUsage == nil || credentialID == 0 {
		return 0, 0
	}
	return s.limitUsage.Usage(credentialID)
}

func (s *Service) applyCredentialLiveUsage(item *CredentialItemResponse) {
	if item == nil {
		return
	}
	item.RPMUsed, item.ConcurrencyUsed = s.credentialLiveUsage(item.CredentialID)
}
