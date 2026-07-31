package mysql

import (
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Releem/mysqlconfigurer/models"
)

type tableSizeSnapshot struct {
	engine    map[string]models.MetricGroupValue
	totalSize uint64
}

type tableSizeCacheInput struct {
	totalTables    uint64
	effectiveRAM   uint64
	tableThreshold int64
	ramMultiplier  int64
	ttl            time.Duration
	now            time.Time
}

type tableSizeCache struct {
	mu             sync.Mutex
	lastSuccessful *tableSizeSnapshot
	expiresAt      time.Time
}

type tableSizeCacheResult struct {
	engine     map[string]models.MetricGroupValue
	usedStale  bool
	refreshErr error
}

func effectiveTableSizeRAM(metrics *models.Metrics) uint64 {
	if metrics == nil {
		return 0
	}
	memory, ok := metrics.System.Info["PhysicalMemory"].(models.MetricGroupValue)
	if !ok {
		return 0
	}
	total := tableSizeUint64(memory["total"])
	if total == 0 {
		return 0
	}
	// AWS RDS enhanced OS metrics report memory.total in kilobytes; local/GCP use bytes.
	if host, ok := metrics.System.Info["Host"].(models.MetricGroupValue); ok {
		if instanceType, _ := host["InstanceType"].(string); instanceType == "aws/rds" {
			const kib = uint64(1024)
			if total > math.MaxUint64/kib {
				return math.MaxUint64
			}
			return total * kib
		}
	}
	return total
}

func tableSizeUint64(value interface{}) uint64 {
	switch value := value.(type) {
	case uint:
		return uint64(value)
	case uint8:
		return uint64(value)
	case uint16:
		return uint64(value)
	case uint32:
		return uint64(value)
	case uint64:
		return value
	case int:
		if value > 0 {
			return uint64(value)
		}
	case int8:
		if value > 0 {
			return uint64(value)
		}
	case int16:
		if value > 0 {
			return uint64(value)
		}
	case int32:
		if value > 0 {
			return uint64(value)
		}
	case int64:
		if value > 0 {
			return uint64(value)
		}
	case float32:
		return tableSizeFloat64(float64(value))
	case float64:
		return tableSizeFloat64(value)
	case string:
		value = strings.TrimSpace(value)
		if parsed, err := strconv.ParseUint(value, 10, 64); err == nil {
			return parsed
		}
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			return tableSizeFloat64(parsed)
		}
	}

	return 0
}

func tableSizeFloat64(value float64) uint64 {
	if value <= 0 || math.IsNaN(value) {
		return 0
	}
	if value >= float64(math.MaxUint64) {
		return math.MaxUint64
	}
	return uint64(value)
}

func (cache *tableSizeCache) getOrRefresh(
	input tableSizeCacheInput,
	refresh func() (map[string]models.MetricGroupValue, error),
) (tableSizeCacheResult, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	if cache.lastSuccessful != nil &&
		tableSizeCacheEligible(input, cache.lastSuccessful.totalSize) &&
		input.now.Before(cache.expiresAt) {
		return tableSizeCacheResult{
			engine: cloneTableSizeEngine(cache.lastSuccessful.engine),
		}, nil
	}

	engine, err := refresh()
	if err != nil {
		if cache.lastSuccessful == nil {
			return tableSizeCacheResult{}, err
		}
		return tableSizeCacheResult{
			engine:     cloneTableSizeEngine(cache.lastSuccessful.engine),
			usedStale:  true,
			refreshErr: err,
		}, nil
	}

	storedEngine := cloneTableSizeEngine(engine)
	snapshot := &tableSizeSnapshot{
		engine:    storedEngine,
		totalSize: tableSizeEngineTotal(storedEngine),
	}
	cache.lastSuccessful = snapshot
	cache.expiresAt = time.Time{}
	if tableSizeCacheEligible(input, snapshot.totalSize) && input.ttl > 0 {
		cache.expiresAt = input.now.Add(input.ttl)
	}

	return tableSizeCacheResult{engine: cloneTableSizeEngine(storedEngine)}, nil
}

func tableSizeCacheEligible(input tableSizeCacheInput, totalSize uint64) bool {
	if input.effectiveRAM == 0 || input.tableThreshold < 0 || input.ramMultiplier < 0 {
		return false
	}
	if input.totalTables <= uint64(input.tableThreshold) {
		return false
	}

	multiplier := uint64(input.ramMultiplier)
	if input.effectiveRAM != 0 && multiplier > math.MaxUint64/input.effectiveRAM {
		return false
	}
	return totalSize > input.effectiveRAM*multiplier
}

func tableSizeCacheTTLFromSeconds(seconds time.Duration) time.Duration {
	if seconds <= 0 {
		return 0
	}
	const maxSeconds = time.Duration(math.MaxInt64) / time.Second
	if seconds > maxSeconds {
		return time.Duration(math.MaxInt64)
	}
	return seconds * time.Second
}

func tableSizeEngineTotal(engine map[string]models.MetricGroupValue) uint64 {
	var total uint64
	for _, metrics := range engine {
		size := tableSizeUint64(metrics["Total Size"])
		if size > math.MaxUint64-total {
			return math.MaxUint64
		}
		total += size
	}
	return total
}

func cloneTableSizeEngine(engine map[string]models.MetricGroupValue) map[string]models.MetricGroupValue {
	if engine == nil {
		return nil
	}

	cloned := make(map[string]models.MetricGroupValue, len(engine))
	for name, metrics := range engine {
		clonedMetrics := make(models.MetricGroupValue, len(metrics))
		for key, value := range metrics {
			clonedMetrics[key] = value
		}
		cloned[name] = clonedMetrics
	}
	return cloned
}
