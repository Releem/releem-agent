package mysql

import (
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Releem/mysqlconfigurer/models"
)

func TestEffectiveTableSizeRAM(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	const kib = uint64(1024)

	tests := []struct {
		name          string
		physicalTotal interface{}
		instanceType  string
		want          uint64
	}{
		{
			name:          "physical with zero memory limit",
			physicalTotal: uint64(8 * gib),
			want:          8 * gib,
		},
		{
			name:          "physical is not capped by memory limit",
			physicalTotal: uint64(8 * gib),
			want:          8 * gib,
		},
		{
			name:          "physical below memory limit",
			physicalTotal: uint64(gib),
			want:          gib,
		},
		{
			name: "memory limit does not replace missing physical",
			want: 0,
		},
		{
			name: "physical missing with zero memory limit",
			want: 0,
		},
		{
			name:          "uint64 physical value",
			physicalTotal: uint64(8 * gib),
			want:          8 * gib,
		},
		{
			name:          "int physical value",
			physicalTotal: int(8 * gib),
			want:          8 * gib,
		},
		{
			name:          "float64 physical value",
			physicalTotal: float64(8 * gib),
			want:          8 * gib,
		},
		{
			name:          "numeric string physical value",
			physicalTotal: "8589934592",
			want:          8 * gib,
		},
		{
			name:          "aws/rds kilobytes are converted to bytes",
			physicalTotal: int(8 * gib / kib),
			instanceType:  "aws/rds",
			want:          8 * gib,
		},
		{
			name:          "non-aws instance type keeps bytes",
			physicalTotal: uint64(8 * gib),
			instanceType:  "gcp/cloudsql",
			want:          8 * gib,
		},
		{
			name:          "aws/rds overflow saturates",
			physicalTotal: uint64(math.MaxUint64),
			instanceType:  "aws/rds",
			want:          math.MaxUint64,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metrics := &models.Metrics{}
			if tt.physicalTotal != nil || tt.instanceType != "" {
				metrics.System.Info = models.MetricGroupValue{}
			}
			if tt.physicalTotal != nil {
				metrics.System.Info["PhysicalMemory"] = models.MetricGroupValue{
					"total": tt.physicalTotal,
				}
			}
			if tt.instanceType != "" {
				metrics.System.Info["Host"] = models.MetricGroupValue{
					"InstanceType": tt.instanceType,
				}
			}
			got := effectiveTableSizeRAM(metrics)

			if got != tt.want {
				t.Fatalf("effectiveTableSizeRAM() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTableSizeUint64AcceptsMetricNumberRepresentations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value interface{}
		want  uint64
	}{
		{name: "uint", value: uint(1), want: 1},
		{name: "uint8", value: uint8(2), want: 2},
		{name: "uint16", value: uint16(3), want: 3},
		{name: "uint32", value: uint32(4), want: 4},
		{name: "uint64", value: uint64(math.MaxUint64), want: math.MaxUint64},
		{name: "int", value: int(5), want: 5},
		{name: "int8", value: int8(6), want: 6},
		{name: "int16", value: int16(7), want: 7},
		{name: "int32", value: int32(8), want: 8},
		{name: "int64", value: int64(9), want: 9},
		{name: "float32 truncates fractional bytes", value: float32(10.75), want: 10},
		{name: "float64 truncates fractional bytes", value: float64(11.75), want: 11},
		{name: "integer string", value: " 12 ", want: 12},
		{name: "decimal string", value: "13.75", want: 13},
		{name: "positive infinity saturates", value: math.Inf(1), want: math.MaxUint64},
		{name: "zero signed value", value: int64(0), want: 0},
		{name: "negative signed value", value: int64(-1), want: 0},
		{name: "negative float", value: float64(-1), want: 0},
		{name: "NaN", value: math.NaN(), want: 0},
		{name: "negative infinity", value: math.Inf(-1), want: 0},
		{name: "invalid string", value: "twelve", want: 0},
		{name: "unsupported type", value: []byte("14"), want: 0},
		{name: "nil", value: nil, want: 0},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tableSizeUint64(tt.value); got != tt.want {
				t.Fatalf("tableSizeUint64(%#v) = %d, want %d", tt.value, got, tt.want)
			}
		})
	}
}

func TestTableSizeCacheThresholds(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	baseTime := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name          string
		totalTables   uint64
		effectiveRAM  uint64
		tableLimit    int64
		ramMultiplier int64
		engine        map[string]models.MetricGroupValue
		wantCalls     int
	}{
		{
			name:          "table count equal to threshold does not cache",
			totalTables:   1000,
			effectiveRAM:  gib,
			tableLimit:    1000,
			ramMultiplier: 4,
			engine: tableSizeTestEngine(
				4*gib+1,
				128,
			),
			wantCalls: 2,
		},
		{
			name:          "total size equal to RAM multiple does not cache",
			totalTables:   1001,
			effectiveRAM:  gib,
			tableLimit:    1000,
			ramMultiplier: 4,
			engine: tableSizeTestEngine(
				3*gib,
				gib,
			),
			wantCalls: 2,
		},
		{
			name:          "both values greater than thresholds cache",
			totalTables:   1001,
			effectiveRAM:  gib,
			tableLimit:    1000,
			ramMultiplier: 4,
			engine: tableSizeTestEngine(
				3*gib,
				gib+1,
			),
			wantCalls: 1,
		},
		{
			name:          "table threshold failing refreshes again",
			totalTables:   999,
			effectiveRAM:  gib,
			tableLimit:    1000,
			ramMultiplier: 4,
			engine: tableSizeTestEngine(
				5*gib,
				128,
			),
			wantCalls: 2,
		},
		{
			name:          "RAM threshold failing refreshes again",
			totalTables:   1001,
			effectiveRAM:  gib,
			tableLimit:    1000,
			ramMultiplier: 4,
			engine: tableSizeTestEngine(
				3*gib,
				gib-1,
			),
			wantCalls: 2,
		},
		{
			name:          "total size addition saturates",
			totalTables:   1001,
			effectiveRAM:  math.MaxUint64 / 2,
			tableLimit:    1000,
			ramMultiplier: 1,
			engine: tableSizeTestEngine(
				math.MaxUint64,
				1,
			),
			wantCalls: 1,
		},
		{
			name:          "RAM multiplication overflow does not cache",
			totalTables:   1001,
			effectiveRAM:  math.MaxUint64,
			tableLimit:    1000,
			ramMultiplier: 2,
			engine: tableSizeTestEngine(
				math.MaxUint64,
				0,
			),
			wantCalls: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := &tableSizeCache{}
			calls := 0
			refresh := func() (map[string]models.MetricGroupValue, error) {
				calls++
				return tt.engine, nil
			}
			input := tableSizeCacheInput{
				totalTables:    tt.totalTables,
				effectiveRAM:   tt.effectiveRAM,
				tableThreshold: tt.tableLimit,
				ramMultiplier:  tt.ramMultiplier,
				ttl:            time.Hour,
				now:            baseTime,
			}

			if _, err := cache.getOrRefresh(input, refresh); err != nil {
				t.Fatalf("first getOrRefresh() error = %v", err)
			}
			if _, err := cache.getOrRefresh(input, refresh); err != nil {
				t.Fatalf("second getOrRefresh() error = %v", err)
			}

			if calls != tt.wantCalls {
				t.Fatalf("refresh calls = %d, want %d", calls, tt.wantCalls)
			}
		})
	}
}

func TestTableSizeCacheZeroEffectiveRAMDoesNotPersist(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	cache := &tableSizeCache{}
	calls := 0
	refresh := func() (map[string]models.MetricGroupValue, error) {
		calls++
		return tableSizeTestEngine(5*gib, 1024), nil
	}
	input := tableSizeCacheInput{
		totalTables:    1001,
		effectiveRAM:   0,
		tableThreshold: 1000,
		ramMultiplier:  4,
		ttl:            time.Hour,
		now:            time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC),
	}

	if _, err := cache.getOrRefresh(input, refresh); err != nil {
		t.Fatalf("first getOrRefresh() error = %v", err)
	}
	if _, err := cache.getOrRefresh(input, refresh); err != nil {
		t.Fatalf("second getOrRefresh() error = %v", err)
	}

	if calls != 2 {
		t.Fatalf("refresh calls = %d, want 2", calls)
	}
}

func TestTableSizeCacheIneligibleSuccessNotReusedAfterInputsBecomeEligible(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	cache := &tableSizeCache{}
	calls := 0
	refresh := func() (map[string]models.MetricGroupValue, error) {
		calls++
		if calls == 1 {
			return tableSizeTestEngine(5*gib, 1024), nil
		}
		return tableSizeTestEngine(6*gib, 2048), nil
	}
	input := tableSizeCacheInput{
		totalTables:    1000,
		effectiveRAM:   gib,
		tableThreshold: 1000,
		ramMultiplier:  4,
		ttl:            time.Hour,
		now:            time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC),
	}

	if _, err := cache.getOrRefresh(input, refresh); err != nil {
		t.Fatalf("ineligible getOrRefresh() error = %v", err)
	}
	input.totalTables = 1001
	eligible, err := cache.getOrRefresh(input, refresh)
	if err != nil {
		t.Fatalf("eligible getOrRefresh() error = %v", err)
	}

	if calls != 2 {
		t.Fatalf("refresh calls = %d, want 2", calls)
	}
	if got := eligible.engine["InnoDB"]["Total Size"]; got != 6*gib {
		t.Fatalf("eligible InnoDB Total Size = %v, want %d", got, 6*gib)
	}
}

func TestTableSizeCacheIneligibleSuccessUsedAsStaleFallback(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	refreshFailure := errors.New("second table size query failed")
	cache := &tableSizeCache{}
	calls := 0
	refresh := func() (map[string]models.MetricGroupValue, error) {
		calls++
		if calls == 1 {
			return tableSizeTestEngine(5*gib, 1024), nil
		}
		return nil, refreshFailure
	}
	input := tableSizeCacheInput{
		totalTables:    1000,
		effectiveRAM:   gib,
		tableThreshold: 1000,
		ramMultiplier:  4,
		ttl:            time.Hour,
		now:            time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC),
	}

	if _, err := cache.getOrRefresh(input, refresh); err != nil {
		t.Fatalf("ineligible getOrRefresh() error = %v", err)
	}
	result, err := cache.getOrRefresh(input, refresh)

	if err != nil {
		t.Fatalf("second getOrRefresh() error = %v, want stale fallback", err)
	}
	if !result.usedStale || !errors.Is(result.refreshErr, refreshFailure) {
		t.Fatalf("second result = %#v, want stale result with refresh error", result)
	}
	if got := result.engine["InnoDB"]["Total Size"]; got != 5*gib {
		t.Fatalf("stale InnoDB Total Size = %v, want %d", got, 5*gib)
	}
	if calls != 2 {
		t.Fatalf("refresh calls = %d, want 2", calls)
	}
}

func TestTableSizeCacheEligibleSuccessThenIneligibleSuccessForcesNextRefresh(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	cache := &tableSizeCache{}
	calls := 0
	refresh := func() (map[string]models.MetricGroupValue, error) {
		calls++
		switch calls {
		case 1:
			return tableSizeTestEngine(5*gib, 1), nil
		case 2:
			return tableSizeTestEngine(6*gib, 2), nil
		default:
			return tableSizeTestEngine(7*gib, 3), nil
		}
	}
	input := tableSizeCacheInput{
		totalTables:    1001,
		effectiveRAM:   gib,
		tableThreshold: 1000,
		ramMultiplier:  4,
		ttl:            time.Hour,
		now:            time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC),
	}

	if _, err := cache.getOrRefresh(input, refresh); err != nil {
		t.Fatalf("eligible getOrRefresh() error = %v", err)
	}
	input.totalTables = 1000
	ineligible, err := cache.getOrRefresh(input, refresh)
	if err != nil {
		t.Fatalf("ineligible getOrRefresh() error = %v", err)
	}
	if got := ineligible.engine["InnoDB"]["Total Size"]; got != 6*gib {
		t.Fatalf("ineligible InnoDB Total Size = %v, want %d", got, 6*gib)
	}

	input.totalTables = 1001
	next, err := cache.getOrRefresh(input, refresh)
	if err != nil {
		t.Fatalf("next eligible getOrRefresh() error = %v", err)
	}
	if calls != 3 {
		t.Fatalf("refresh calls = %d, want 3", calls)
	}
	if got := next.engine["InnoDB"]["Total Size"]; got != 7*gib {
		t.Fatalf("next eligible InnoDB Total Size = %v, want %d", got, 7*gib)
	}
}

func TestTableSizeCacheLatestIneligibleSuccessReplacesStaleFallback(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	refreshFailure := errors.New("next table size query failed")
	cache := &tableSizeCache{}
	calls := 0
	refresh := func() (map[string]models.MetricGroupValue, error) {
		calls++
		switch calls {
		case 1:
			return tableSizeTestEngine(5*gib, 1), nil
		case 2:
			return tableSizeTestEngine(6*gib, 2), nil
		default:
			return nil, refreshFailure
		}
	}
	input := tableSizeCacheInput{
		totalTables:    1001,
		effectiveRAM:   gib,
		tableThreshold: 1000,
		ramMultiplier:  4,
		ttl:            time.Hour,
		now:            time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC),
	}

	if _, err := cache.getOrRefresh(input, refresh); err != nil {
		t.Fatalf("eligible getOrRefresh() error = %v", err)
	}
	input.totalTables = 1000
	if _, err := cache.getOrRefresh(input, refresh); err != nil {
		t.Fatalf("ineligible getOrRefresh() error = %v", err)
	}

	input.totalTables = 1001
	stale, err := cache.getOrRefresh(input, refresh)
	if err != nil {
		t.Fatalf("stale getOrRefresh() error = %v, want nil", err)
	}
	if calls != 3 {
		t.Fatalf("refresh calls = %d, want 3", calls)
	}
	if !stale.usedStale {
		t.Fatal("usedStale = false, want true")
	}
	if !errors.Is(stale.refreshErr, refreshFailure) {
		t.Fatalf("refreshErr = %v, want %v", stale.refreshErr, refreshFailure)
	}
	if got := stale.engine["InnoDB"]["Total Size"]; got != 6*gib {
		t.Fatalf("stale InnoDB Total Size = %v, want latest successful %d", got, 6*gib)
	}
}

func TestTableSizeCacheClonesSnapshots(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	refreshed := map[string]models.MetricGroupValue{
		"InnoDB": {
			"Enabled":      "YES",
			"Table Number": uint64(1200),
			"Total Size":   4 * gib,
			"Data Size":    3 * gib,
			"Index Size":   gib,
		},
		"MyISAM": {
			"Enabled":      "YES",
			"Table Number": uint64(2),
			"Total Size":   uint64(4096),
			"Data Size":    uint64(3072),
			"Index Size":   uint64(1024),
		},
	}
	cache := &tableSizeCache{}
	calls := 0
	refresh := func() (map[string]models.MetricGroupValue, error) {
		calls++
		return refreshed, nil
	}
	input := tableSizeCacheInput{
		totalTables:    1202,
		effectiveRAM:   gib,
		tableThreshold: 1000,
		ramMultiplier:  4,
		ttl:            time.Hour,
		now:            now,
	}

	first, err := cache.getOrRefresh(input, refresh)
	if err != nil {
		t.Fatalf("first getOrRefresh() error = %v", err)
	}
	delete(first.engine, "MyISAM")
	first.engine["InnoDB"]["Index Size"] = uint64(7)
	refreshed["MyISAM"]["Index Size"] = uint64(9)

	second, err := cache.getOrRefresh(input, refresh)
	if err != nil {
		t.Fatalf("second getOrRefresh() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls)
	}
	if got := second.engine["InnoDB"]["Index Size"]; got != gib {
		t.Fatalf("cached InnoDB Index Size = %v, want %d", got, gib)
	}
	if got := second.engine["MyISAM"]["Index Size"]; got != uint64(1024) {
		t.Fatalf("cached MyISAM Index Size = %v, want 1024", got)
	}
	if got := second.engine["InnoDB"]["Data Size"]; got != 3*gib {
		t.Fatalf("cached InnoDB Data Size = %v, want %d", got, 3*gib)
	}
	if got := second.engine["MyISAM"]["Data Size"]; got != uint64(3072) {
		t.Fatalf("cached MyISAM Data Size = %v, want 3072", got)
	}

	second.engine["MyISAM"]["Data Size"] = uint64(11)
	third, err := cache.getOrRefresh(input, refresh)
	if err != nil {
		t.Fatalf("third getOrRefresh() error = %v", err)
	}
	if got := third.engine["MyISAM"]["Data Size"]; got != uint64(3072) {
		t.Fatalf("independent MyISAM Data Size = %v, want 3072", got)
	}
}

func TestTableSizeCacheTTLFromSecondsSaturates(t *testing.T) {
	tests := []struct {
		name    string
		seconds time.Duration
		want    time.Duration
	}{
		{
			name:    "normal duration",
			seconds: 604800,
			want:    7 * 24 * time.Hour,
		},
		{
			name:    "extreme duration",
			seconds: time.Duration(math.MaxInt64),
			want:    time.Duration(math.MaxInt64),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tableSizeCacheTTLFromSeconds(tt.seconds); got != tt.want {
				t.Fatalf("tableSizeCacheTTLFromSeconds(%d) = %d, want %d", tt.seconds, got, tt.want)
			}
		})
	}
}

func tableSizeTestEngine(innodbTotal, myisamTotal uint64) map[string]models.MetricGroupValue {
	return map[string]models.MetricGroupValue{
		"InnoDB": {
			"Enabled":      "YES",
			"Table Number": uint64(1000),
			"Total Size":   innodbTotal,
			"Data Size":    innodbTotal,
			"Index Size":   uint64(0),
		},
		"MyISAM": {
			"Enabled":      "YES",
			"Table Number": uint64(1),
			"Total Size":   myisamTotal,
			"Data Size":    uint64(0),
			"Index Size":   myisamTotal,
		},
	}
}

func TestTableSizeCacheTTLFreshOneSecondBeforeExpiry(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	collectedAt := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	cache := &tableSizeCache{}
	calls := 0
	refresh := func() (map[string]models.MetricGroupValue, error) {
		calls++
		return tableSizeTestEngine(4*gib, 1), nil
	}
	input := tableSizeCacheInput{
		totalTables:    1001,
		effectiveRAM:   gib,
		tableThreshold: 1000,
		ramMultiplier:  4,
		ttl:            24 * time.Hour,
		now:            collectedAt,
	}

	if _, err := cache.getOrRefresh(input, refresh); err != nil {
		t.Fatalf("first getOrRefresh() error = %v", err)
	}
	input.now = collectedAt.Add(24*time.Hour - time.Second)
	if _, err := cache.getOrRefresh(input, refresh); err != nil {
		t.Fatalf("second getOrRefresh() error = %v", err)
	}

	if calls != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls)
	}
}

func TestTableSizeCacheTTLExactlyAtExpiryRefreshes(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	collectedAt := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	cache := &tableSizeCache{}
	calls := 0
	refresh := func() (map[string]models.MetricGroupValue, error) {
		calls++
		return tableSizeTestEngine(4*gib, 1), nil
	}
	input := tableSizeCacheInput{
		totalTables:    1001,
		effectiveRAM:   gib,
		tableThreshold: 1000,
		ramMultiplier:  4,
		ttl:            24 * time.Hour,
		now:            collectedAt,
	}

	if _, err := cache.getOrRefresh(input, refresh); err != nil {
		t.Fatalf("first getOrRefresh() error = %v", err)
	}
	input.now = collectedAt.Add(24 * time.Hour)
	if _, err := cache.getOrRefresh(input, refresh); err != nil {
		t.Fatalf("second getOrRefresh() error = %v", err)
	}

	if calls != 2 {
		t.Fatalf("refresh calls = %d, want 2", calls)
	}
}

func TestTableSizeCacheTTLSuccessfulRefreshReplacesSnapshotAndTimestamp(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	firstTime := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	secondTime := firstTime.Add(24 * time.Hour)
	cache := &tableSizeCache{}
	calls := 0
	refresh := func() (map[string]models.MetricGroupValue, error) {
		calls++
		if calls == 1 {
			return tableSizeTestEngine(4*gib, 1), nil
		}
		return tableSizeTestEngine(5*gib, 2), nil
	}
	input := tableSizeCacheInput{
		totalTables:    1001,
		effectiveRAM:   gib,
		tableThreshold: 1000,
		ramMultiplier:  4,
		ttl:            24 * time.Hour,
		now:            firstTime,
	}

	if _, err := cache.getOrRefresh(input, refresh); err != nil {
		t.Fatalf("first getOrRefresh() error = %v", err)
	}
	input.now = secondTime
	second, err := cache.getOrRefresh(input, refresh)
	if err != nil {
		t.Fatalf("second getOrRefresh() error = %v", err)
	}
	if got := second.engine["InnoDB"]["Total Size"]; got != 5*gib {
		t.Fatalf("refreshed InnoDB Total Size = %v, want %d", got, 5*gib)
	}

	input.now = secondTime.Add(24*time.Hour - time.Second)
	third, err := cache.getOrRefresh(input, refresh)
	if err != nil {
		t.Fatalf("third getOrRefresh() error = %v", err)
	}
	if got := third.engine["MyISAM"]["Total Size"]; got != uint64(2) {
		t.Fatalf("cached MyISAM Total Size = %v, want 2", got)
	}
	if calls != 2 {
		t.Fatalf("refresh calls = %d, want 2", calls)
	}
}

func TestTableSizeCacheStaleFallbackReturnsSnapshotAndRefreshError(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	refreshFailure := errors.New("table size query failed")
	collectedAt := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	cache := &tableSizeCache{}
	calls := 0
	refresh := func() (map[string]models.MetricGroupValue, error) {
		calls++
		if calls == 1 {
			return tableSizeTestEngine(4*gib, 1024), nil
		}
		return nil, refreshFailure
	}
	input := tableSizeCacheInput{
		totalTables:    1001,
		effectiveRAM:   gib,
		tableThreshold: 1000,
		ramMultiplier:  4,
		ttl:            time.Hour,
		now:            collectedAt,
	}

	if _, err := cache.getOrRefresh(input, refresh); err != nil {
		t.Fatalf("first getOrRefresh() error = %v", err)
	}
	input.now = collectedAt.Add(time.Hour)
	stale, err := cache.getOrRefresh(input, refresh)

	if err != nil {
		t.Fatalf("stale getOrRefresh() error = %v, want nil", err)
	}
	if !stale.usedStale {
		t.Fatal("usedStale = false, want true")
	}
	if !errors.Is(stale.refreshErr, refreshFailure) {
		t.Fatalf("refreshErr = %v, want %v", stale.refreshErr, refreshFailure)
	}
	if got := stale.engine["InnoDB"]["Total Size"]; got != 4*gib {
		t.Fatalf("stale InnoDB Total Size = %v, want %d", got, 4*gib)
	}
	if got := stale.engine["MyISAM"]["Index Size"]; got != uint64(1024) {
		t.Fatalf("stale MyISAM Index Size = %v, want 1024", got)
	}
	if calls != 2 {
		t.Fatalf("refresh calls = %d, want 2", calls)
	}
}

func TestTableSizeCacheFirstErrorReturnsOriginalError(t *testing.T) {
	refreshFailure := errors.New("initial table size query failed")
	cache := &tableSizeCache{}

	result, err := cache.getOrRefresh(
		tableSizeCacheInput{
			totalTables:    1001,
			effectiveRAM:   1024,
			tableThreshold: 1000,
			ramMultiplier:  4,
			ttl:            time.Hour,
			now:            time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC),
		},
		func() (map[string]models.MetricGroupValue, error) {
			return nil, refreshFailure
		},
	)

	if !errors.Is(err, refreshFailure) {
		t.Fatalf("getOrRefresh() error = %v, want %v", err, refreshFailure)
	}
	if result.engine != nil {
		t.Fatalf("result.engine = %v, want nil", result.engine)
	}
}

func TestTableSizeCacheConcurrentCallersShareRefreshAndReceiveIndependentSnapshots(t *testing.T) {
	const (
		callers = 20
		gib     = uint64(1024 * 1024 * 1024)
	)
	cache := &tableSizeCache{}
	input := tableSizeCacheInput{
		totalTables:    1001,
		effectiveRAM:   gib,
		tableThreshold: 1000,
		ramMultiplier:  4,
		ttl:            time.Hour,
		now:            time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC),
	}
	start := make(chan struct{})
	refreshStarted := make(chan struct{}, 1)
	releaseRefresh := make(chan struct{})
	var calls int32
	refresh := func() (map[string]models.MetricGroupValue, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			refreshStarted <- struct{}{}
		}
		<-releaseRefresh
		return tableSizeTestEngine(4*gib, 1024), nil
	}
	type response struct {
		result tableSizeCacheResult
		err    error
	}
	responses := make(chan response, callers)

	for i := 0; i < callers; i++ {
		go func() {
			<-start
			result, err := cache.getOrRefresh(input, refresh)
			responses <- response{result: result, err: err}
		}()
	}
	close(start)
	<-refreshStarted
	close(releaseRefresh)

	results := make([]tableSizeCacheResult, 0, callers)
	for i := 0; i < callers; i++ {
		response := <-responses
		if response.err != nil {
			t.Fatalf("concurrent getOrRefresh() error = %v", response.err)
		}
		if got := response.result.engine["InnoDB"]["Total Size"]; got != 4*gib {
			t.Fatalf("concurrent InnoDB Total Size = %v, want %d", got, 4*gib)
		}
		if got := response.result.engine["MyISAM"]["Index Size"]; got != uint64(1024) {
			t.Fatalf("concurrent MyISAM Index Size = %v, want 1024", got)
		}
		results = append(results, response.result)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}

	results[0].engine["InnoDB"]["Total Size"] = uint64(7)
	delete(results[0].engine, "MyISAM")
	for i := 1; i < callers; i++ {
		if got := results[i].engine["InnoDB"]["Total Size"]; got != 4*gib {
			t.Fatalf("result %d InnoDB Total Size = %v, want %d", i, got, 4*gib)
		}
		if got := results[i].engine["MyISAM"]["Index Size"]; got != uint64(1024) {
			t.Fatalf("result %d MyISAM Index Size = %v, want 1024", i, got)
		}
	}
}

func TestTableSizeCacheRefreshPanicAllowsLaterRefresh(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	panicValue := errors.New("first table size refresh panic")
	cache := &tableSizeCache{}
	input := tableSizeCacheInput{
		totalTables:    1001,
		effectiveRAM:   gib,
		tableThreshold: 1000,
		ramMultiplier:  4,
		ttl:            time.Hour,
		now:            time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC),
	}

	var recovered interface{}
	func() {
		defer func() {
			recovered = recover()
		}()
		_, _ = cache.getOrRefresh(input, func() (map[string]models.MetricGroupValue, error) {
			panic(panicValue)
		})
	}()
	if recovered != panicValue {
		t.Fatalf("leader panic = %v, want original %v", recovered, panicValue)
	}

	type response struct {
		result tableSizeCacheResult
		err    error
	}
	laterDone := make(chan response, 1)
	var laterCalls int32
	go func() {
		result, err := cache.getOrRefresh(input, func() (map[string]models.MetricGroupValue, error) {
			atomic.AddInt32(&laterCalls, 1)
			return tableSizeTestEngine(5*gib, 1024), nil
		})
		laterDone <- response{result: result, err: err}
	}()

	select {
	case response := <-laterDone:
		if response.err != nil {
			t.Fatalf("later getOrRefresh() error = %v", response.err)
		}
		if got := response.result.engine["InnoDB"]["Total Size"]; got != 5*gib {
			t.Fatalf("later InnoDB Total Size = %v, want %d", got, 5*gib)
		}
	case <-time.After(time.Second):
		t.Fatal("later refresh blocked on poisoned in-flight attempt")
	}
	if got := atomic.LoadInt32(&laterCalls); got != 1 {
		t.Fatalf("later refresh calls = %d, want 1", got)
	}
}
