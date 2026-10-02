package ratelimit

import "sync"

// DataPlaneConcurrency tracks process-local global and Group-level in-flight
// work. Access Key and Credential limits are intentionally owned by their
// existing limiters; this type only adds the missing aggregate dimensions.
type DataPlaneConcurrency struct {
	mu     sync.Mutex
	global int64
	groups map[uint]int64
}

type DataPlaneConcurrencySnapshot struct {
	Global int64
	Groups map[uint]int64
}

func NewDataPlaneConcurrency() *DataPlaneConcurrency {
	return &DataPlaneConcurrency{groups: make(map[uint]int64)}
}

func (c *DataPlaneConcurrency) AcquireGlobal(limit int64) (func(), bool) {
	if c == nil {
		return func() {}, true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if limit > 0 && c.global >= limit {
		return nil, false
	}
	c.global++
	return c.releaseGlobal(), true
}

func (c *DataPlaneConcurrency) AcquireGroup(groupID uint, limit int64) (func(), bool) {
	if c == nil {
		return func() {}, true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if limit > 0 && c.groups[groupID] >= limit {
		return nil, false
	}
	c.groups[groupID]++
	return c.releaseGroup(groupID), true
}

func (c *DataPlaneConcurrency) releaseGlobal() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			c.global--
			c.mu.Unlock()
		})
	}
}

func (c *DataPlaneConcurrency) releaseGroup(groupID uint) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			if c.groups[groupID] <= 1 {
				delete(c.groups, groupID)
			} else {
				c.groups[groupID]--
			}
			c.mu.Unlock()
		})
	}
}

func (c *DataPlaneConcurrency) Snapshot() DataPlaneConcurrencySnapshot {
	if c == nil {
		return DataPlaneConcurrencySnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	groups := make(map[uint]int64, len(c.groups))
	for id, count := range c.groups {
		groups[id] = count
	}
	return DataPlaneConcurrencySnapshot{Global: c.global, Groups: groups}
}
