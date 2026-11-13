package ratelimit

import (
	"sync"
	"time"
)

// CredentialLimiter 按凭据统计 RPM 滑动窗口与在途请求数。与 AccessKeyRPM 同为
// 进程内状态；请求准备成功、进入转发前才扣减真实限额，不看上游结果，
// 否则本地限额会被上游 429 绕开。调度中的待执行预留不属于这里的计数。
type CredentialLimiter struct {
	mu          sync.Mutex
	windows     map[uint]timestampDeque
	inFlight    map[uint]int64
	now         func() time.Time
	lastCleanup time.Time
}

func NewCredentialLimiter() *CredentialLimiter {
	return &CredentialLimiter{
		windows: make(map[uint]timestampDeque), inFlight: make(map[uint]int64), now: time.Now,
	}
}

// Available 只读判断凭据当前是否还有名额，不扣减。limit <= 0 视为不限。
func (limiter *CredentialLimiter) Available(credentialID uint, rpmLimit, concurrencyLimit int64) bool {
	if limiter == nil || (rpmLimit <= 0 && concurrencyLimit <= 0) {
		return true
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := limiter.now()
	limiter.cleanupLocked(now)
	return limiter.availableLocked(credentialID, rpmLimit, concurrencyLimit, now)
}

// AvailableWithRetryAfter checks a credential once and returns its RPM
// recovery time from the same snapshot. The scheduler uses this to avoid
// taking the limiter lock twice for an exhausted credential.
func (limiter *CredentialLimiter) AvailableWithRetryAfter(credentialID uint, rpmLimit, concurrencyLimit int64) (bool, time.Time) {
	if limiter == nil || (rpmLimit <= 0 && concurrencyLimit <= 0) {
		return true, time.Time{}
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := limiter.now()
	limiter.cleanupLocked(now)
	available, retryAfter := limiter.availableWithRetryAfterLocked(credentialID, rpmLimit, concurrencyLimit, now)
	return available, retryAfter
}

func (limiter *CredentialLimiter) availableLocked(credentialID uint, rpmLimit, concurrencyLimit int64, now time.Time) bool {
	available, _ := limiter.availableWithRetryAfterLocked(credentialID, rpmLimit, concurrencyLimit, now)
	return available
}

func (limiter *CredentialLimiter) pruneWindowLocked(credentialID uint, now time.Time) timestampDeque {
	window := limiter.windows[credentialID]
	window.dropThrough(now.Add(-time.Minute))
	if window.len() == 0 {
		delete(limiter.windows, credentialID)
	} else {
		limiter.windows[credentialID] = window
	}
	return window
}

// cleanupLocked 由访问触发（非定时器），持锁时最多每分钟全量清理一次闲置窗口。
// 仅清理过期 RPM 记录；长流可超过一分钟，在途数必须保留到 release。
func (limiter *CredentialLimiter) cleanupLocked(now time.Time) {
	if !limiter.lastCleanup.IsZero() && now.Sub(limiter.lastCleanup) < time.Minute {
		return
	}
	for credentialID := range limiter.windows {
		limiter.pruneWindowLocked(credentialID, now)
	}
	limiter.lastCleanup = now
}

func (limiter *CredentialLimiter) availableWithRetryAfterLocked(credentialID uint, rpmLimit, concurrencyLimit int64, now time.Time) (bool, time.Time) {
	concurrencyBlocked := concurrencyLimit > 0 && limiter.inFlight[credentialID] >= concurrencyLimit
	var retryAfter time.Time
	if rpmLimit > 0 {
		window := limiter.pruneWindowLocked(credentialID, now)
		if int64(window.len()) >= rpmLimit {
			retryAfter = window.at(window.len() - int(rpmLimit)).Add(time.Minute)
		}
	}
	if concurrencyBlocked || !retryAfter.IsZero() {
		return false, retryAfter
	}
	return true, time.Time{}
}

// Acquire 在同一把锁内完成判定与扣减：RPM 窗口记一次，并发在途加一。
// 上限未配置也照常记账，limit <= 0 只是不参与判定；release 只归还并发名额，
// RPM 记录不回滚，重复调用 release 无副作用。
func (limiter *CredentialLimiter) Acquire(credentialID uint, rpmLimit, concurrencyLimit int64) (release func(), allowed bool) {
	if limiter == nil {
		return func() {}, true
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := limiter.now()
	limiter.cleanupLocked(now)
	if !limiter.availableLocked(credentialID, rpmLimit, concurrencyLimit, now) {
		return nil, false
	}
	// 先裁剪再写入，避免无 RPM 上限的凭据窗口无限增长。
	window := limiter.pruneWindowLocked(credentialID, now)
	window.push(now)
	limiter.windows[credentialID] = window
	limiter.inFlight[credentialID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			limiter.mu.Lock()
			defer limiter.mu.Unlock()
			if limiter.inFlight[credentialID] <= 1 {
				delete(limiter.inFlight, credentialID)
				return
			}
			limiter.inFlight[credentialID]--
		})
	}, true
}

// RetryAfter 返回该凭据 RPM 窗口最早可恢复的时刻；没有 RPM 限制或未满时返回零值。
func (limiter *CredentialLimiter) RetryAfter(credentialID uint, rpmLimit int64) time.Time {
	if limiter == nil || rpmLimit <= 0 {
		return time.Time{}
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := limiter.now()
	limiter.cleanupLocked(now)
	window := limiter.pruneWindowLocked(credentialID, now)
	count := window.len()
	if int64(count) < rpmLimit {
		return time.Time{}
	}
	return window.at(count - int(rpmLimit)).Add(time.Minute)
}

// Usage 返回当前 RPM 窗口内的请求数与在途请求数，供管理面实时展示与调度器负载感知。
func (limiter *CredentialLimiter) Usage(credentialID uint) (rpmUsed, inFlight int64) {
	if limiter == nil {
		return 0, 0
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := limiter.now()
	limiter.cleanupLocked(now)
	window := limiter.pruneWindowLocked(credentialID, now)
	return int64(window.len()), limiter.inFlight[credentialID]
}

// InFlightForTest 返回当前在途数，仅供测试断言名额是否泄漏。
func (limiter *CredentialLimiter) InFlightForTest(credentialID uint) int64 {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	return limiter.inFlight[credentialID]
}
