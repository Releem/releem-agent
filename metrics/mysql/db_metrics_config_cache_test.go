package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	logging "github.com/google/logger"
)

const (
	tableSizeCacheTestSupportQuery = "SELECT ENGINE,SUPPORT FROM information_schema.ENGINES ORDER BY ENGINE ASC"
	tableSizeCacheTestHeavyQuery   = "SELECT ENGINE, IFNULL(SUM(DATA_LENGTH+INDEX_LENGTH), 0), IFNULL(COUNT(ENGINE), 0), IFNULL(SUM(DATA_LENGTH), 0), IFNULL(SUM(INDEX_LENGTH), 0) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND ENGINE IS NOT NULL  GROUP BY ENGINE ORDER BY ENGINE ASC"
)

var tableSizeCacheTestDriverID atomic.Uint64

func TestDBMetricsConfigGathererReusesLargeServerTableSizes(t *testing.T) {
	fake := &tableSizeCacheSQLFake{
		supportRows: [][]driver.Value{
			{"InnoDB", "DEFAULT"},
			{"MyISAM", "YES"},
		},
		heavyRows: [][]driver.Value{
			{"InnoDB", int64(550), int64(11), int64(400), int64(150)},
			{"MyISAM", int64(50), int64(1), int64(30), int64(20)},
		},
	}
	setTableSizeCacheTestDB(t, fake)

	logger := *logging.Init("mysql-table-size-cache-test", true, false, io.Discard)
	gatherer := NewDBMetricsConfigGatherer(logger, &config.Config{
		TableSizeCacheTableThreshold: 10,
		TableSizeCacheRAMMultiplier:  4,
		TableSizeCacheTTL:            3600,
	})

	first := tableSizeCacheTestMetrics()
	if err := gatherer.GetMetrics(first); err != nil {
		t.Fatalf("first GetMetrics() error = %v", err)
	}
	assertTableSizeCachePayload(t, first)

	second := tableSizeCacheTestMetrics()
	if err := gatherer.GetMetrics(second); err != nil {
		t.Fatalf("second GetMetrics() error = %v", err)
	}
	assertTableSizeCachePayload(t, second)

	supportQueries, heavyQueries := fake.counts()
	if supportQueries != 2 {
		t.Fatalf("support query count = %d, want 2", supportQueries)
	}
	if heavyQueries != 1 {
		t.Fatalf("heavy query count = %d, want 1", heavyQueries)
	}
}

func TestDBMetricsConfigGathererMemoryLimitDoesNotReplaceMissingPhysicalRAM(t *testing.T) {
	const mib = int64(1024 * 1024)
	fake := &tableSizeCacheSQLFake{
		supportRows: [][]driver.Value{
			{"InnoDB", "DEFAULT"},
			{"MyISAM", "YES"},
		},
		heavyRows: [][]driver.Value{
			{"InnoDB", 5 * mib, int64(11), 4 * mib, mib},
			{"MyISAM", mib, int64(1), mib, int64(0)},
		},
	}
	setTableSizeCacheTestDB(t, fake)
	gatherer := newTableSizeCacheTestGatherer(config.Config{
		MemoryLimit:                  1,
		TableSizeCacheTableThreshold: 10,
		TableSizeCacheRAMMultiplier:  4,
		TableSizeCacheTTL:            3600,
	})

	for call := 1; call <= 2; call++ {
		metrics := tableSizeCacheTestMetrics()
		delete(metrics.System.Info, "PhysicalMemory")
		if err := gatherer.GetMetrics(metrics); err != nil {
			t.Fatalf("GetMetrics() call %d error = %v", call, err)
		}
	}

	_, heavyQueries := fake.counts()
	if heavyQueries != 2 {
		t.Fatalf("heavy query count = %d, want 2 without physical RAM", heavyQueries)
	}
}

func TestDBMetricsConfigGathererCacheBoundaryTableCount(t *testing.T) {
	fake := tableSizeCacheBoundaryFake()
	setTableSizeCacheTestDB(t, fake)
	gatherer := newTableSizeCacheTestGatherer(config.Config{
		TableSizeCacheTableThreshold: 12,
		TableSizeCacheRAMMultiplier:  4,
		TableSizeCacheTTL:            3600,
	})

	for call := 1; call <= 2; call++ {
		if err := gatherer.GetMetrics(tableSizeCacheTestMetrics()); err != nil {
			t.Fatalf("GetMetrics() call %d error = %v", call, err)
		}
	}

	_, heavyQueries := fake.counts()
	if heavyQueries != 2 {
		t.Fatalf("heavy query count = %d, want 2 at table-count boundary", heavyQueries)
	}
}

func TestDBMetricsConfigGathererCacheBoundaryTotalSize(t *testing.T) {
	fake := tableSizeCacheBoundaryFake()
	setTableSizeCacheTestDB(t, fake)
	gatherer := newTableSizeCacheTestGatherer(config.Config{
		TableSizeCacheTableThreshold: 10,
		TableSizeCacheRAMMultiplier:  4,
		TableSizeCacheTTL:            3600,
	})

	for call := 1; call <= 2; call++ {
		metrics := tableSizeCacheTestMetrics()
		metrics.System.Info["PhysicalMemory"].(models.MetricGroupValue)["total"] = uint64(150)
		if err := gatherer.GetMetrics(metrics); err != nil {
			t.Fatalf("GetMetrics() call %d error = %v", call, err)
		}
	}

	_, heavyQueries := fake.counts()
	if heavyQueries != 2 {
		t.Fatalf("heavy query count = %d, want 2 at total-size boundary", heavyQueries)
	}
}

func TestDBMetricsConfigGathererExpiredCacheRefreshes(t *testing.T) {
	fake := tableSizeCacheBoundaryFake()
	setTableSizeCacheTestDB(t, fake)
	gatherer := newTableSizeCacheTestGatherer(config.Config{
		TableSizeCacheTableThreshold: 10,
		TableSizeCacheRAMMultiplier:  4,
		TableSizeCacheTTL:            3600,
	})
	baseTime := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	gatherer.now = tableSizeCacheTestClock(baseTime, baseTime.Add(time.Hour))

	for call := 1; call <= 2; call++ {
		if err := gatherer.GetMetrics(tableSizeCacheTestMetrics()); err != nil {
			t.Fatalf("GetMetrics() call %d error = %v", call, err)
		}
	}

	_, heavyQueries := fake.counts()
	if heavyQueries != 2 {
		t.Fatalf("heavy query count = %d, want 2 after exact TTL expiry", heavyQueries)
	}
}

func TestDBMetricsConfigGathererStaleCacheSurvivesRefreshError(t *testing.T) {
	refreshFailure := errors.New("table size refresh failed")
	fake := tableSizeCacheBoundaryFake()
	fake.heavyErrors = map[int]error{2: refreshFailure}
	setTableSizeCacheTestDB(t, fake)
	gatherer := newTableSizeCacheTestGatherer(config.Config{
		TableSizeCacheTableThreshold: 10,
		TableSizeCacheRAMMultiplier:  4,
		TableSizeCacheTTL:            3600,
	})
	baseTime := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	gatherer.now = tableSizeCacheTestClock(baseTime, baseTime.Add(time.Hour))

	first := tableSizeCacheTestMetrics()
	if err := gatherer.GetMetrics(first); err != nil {
		t.Fatalf("first GetMetrics() error = %v", err)
	}
	second := tableSizeCacheTestMetrics()
	if err := gatherer.GetMetrics(second); err != nil {
		t.Fatalf("second GetMetrics() error = %v, want stale fallback", err)
	}
	assertTableSizeCachePayload(t, second)

	_, heavyQueries := fake.counts()
	if heavyQueries != 2 {
		t.Fatalf("heavy query count = %d, want 2 after failed expiry refresh", heavyQueries)
	}
}

func TestDBMetricsConfigGathererIneligibleSuccessSurvivesRefreshError(t *testing.T) {
	refreshFailure := errors.New("table size refresh failed")
	fake := tableSizeCacheBoundaryFake()
	fake.heavyErrors = map[int]error{2: refreshFailure}
	setTableSizeCacheTestDB(t, fake)
	gatherer := newTableSizeCacheTestGatherer(config.Config{
		TableSizeCacheTableThreshold: 12,
		TableSizeCacheRAMMultiplier:  4,
		TableSizeCacheTTL:            3600,
	})

	first := tableSizeCacheTestMetrics()
	if err := gatherer.GetMetrics(first); err != nil {
		t.Fatalf("first GetMetrics() error = %v", err)
	}
	second := tableSizeCacheTestMetrics()
	if err := gatherer.GetMetrics(second); err != nil {
		t.Fatalf("second GetMetrics() error = %v, want stale fallback", err)
	}
	assertTableSizeCachePayload(t, second)

	_, heavyQueries := fake.counts()
	if heavyQueries != 2 {
		t.Fatalf("heavy query count = %d, want 2", heavyQueries)
	}
}

func TestDBMetricsConfigGathererFirstErrorIsReturned(t *testing.T) {
	refreshFailure := errors.New("first table size query failed")
	fake := tableSizeCacheBoundaryFake()
	fake.heavyErrors = map[int]error{1: refreshFailure}
	setTableSizeCacheTestDB(t, fake)
	gatherer := newTableSizeCacheTestGatherer(config.Config{
		TableSizeCacheTableThreshold: 10,
		TableSizeCacheRAMMultiplier:  4,
		TableSizeCacheTTL:            3600,
	})

	err := gatherer.GetMetrics(tableSizeCacheTestMetrics())
	if !errors.Is(err, refreshFailure) {
		t.Fatalf("GetMetrics() error = %v, want %v", err, refreshFailure)
	}
}

func TestDBMetricsConfigGathererEngineMergeAddsLiveEngineWithZeroSizes(t *testing.T) {
	fake := tableSizeCacheBoundaryFake()
	fake.supportBatches = [][][]driver.Value{
		{
			{"InnoDB", "DEFAULT"},
			{"MyISAM", "YES"},
		},
		{
			{"ARCHIVE", "YES"},
			{"InnoDB", "DEFAULT"},
			{"MyISAM", "YES"},
		},
	}
	setTableSizeCacheTestDB(t, fake)
	gatherer := newTableSizeCacheTestGatherer(config.Config{
		TableSizeCacheTableThreshold: 10,
		TableSizeCacheRAMMultiplier:  4,
		TableSizeCacheTTL:            3600,
	})

	if err := gatherer.GetMetrics(tableSizeCacheTestMetrics()); err != nil {
		t.Fatalf("first GetMetrics() error = %v", err)
	}
	second := tableSizeCacheTestMetrics()
	if err := gatherer.GetMetrics(second); err != nil {
		t.Fatalf("second GetMetrics() error = %v", err)
	}

	archive := second.DB.Metrics.Engine["ARCHIVE"]
	wantArchive := models.MetricGroupValue{
		"Enabled":      "YES",
		"Table Number": uint64(0),
		"Total Size":   uint64(0),
		"Data Size":    uint64(0),
		"Index Size":   uint64(0),
	}
	for field, want := range wantArchive {
		if got := archive[field]; got != want {
			t.Fatalf("ARCHIVE %q = %#v (%T), want %#v (%T)", field, got, got, want, want)
		}
	}

	supportQueries, heavyQueries := fake.counts()
	if supportQueries != 2 {
		t.Fatalf("support query count = %d, want 2", supportQueries)
	}
	if heavyQueries != 1 {
		t.Fatalf("heavy query count = %d, want 1", heavyQueries)
	}
}

func TestDBMetricsConfigGathererNoMyISAMReportsZeroIndexes(t *testing.T) {
	fake := &tableSizeCacheSQLFake{
		supportRows: [][]driver.Value{
			{"InnoDB", "DEFAULT"},
		},
		heavyRows: [][]driver.Value{
			{"InnoDB", int64(550), int64(11), int64(400), int64(150)},
		},
	}
	setTableSizeCacheTestDB(t, fake)
	gatherer := newTableSizeCacheTestGatherer(config.Config{
		TableSizeCacheTableThreshold: 10,
		TableSizeCacheRAMMultiplier:  4,
		TableSizeCacheTTL:            3600,
	})

	metrics := tableSizeCacheTestMetrics()
	if err := gatherer.GetMetrics(metrics); err != nil {
		t.Fatalf("GetMetrics() error = %v", err)
	}
	if got := metrics.DB.Metrics.TotalMyisamIndexes; got != 0 {
		t.Fatalf("TotalMyisamIndexes = %d, want 0", got)
	}
}

func TestDBMetricsConfigGathererFirstIterationErrorRetriesWithoutCachingPartialSizes(t *testing.T) {
	iterationFailure := errors.New("table size rows iteration failed")
	fake := tableSizeCacheBoundaryFake()
	fake.heavyBatches = [][][]driver.Value{
		{
			{"InnoDB", int64(550), int64(11), int64(400), int64(150)},
		},
		{
			{"InnoDB", int64(650), int64(12), int64(475), int64(175)},
			{"MyISAM", int64(60), int64(2), int64(35), int64(25)},
		},
	}
	fake.heavyIterationErrors = map[int]error{1: iterationFailure}
	setTableSizeCacheTestDB(t, fake)
	gatherer := newTableSizeCacheTestGatherer(config.Config{
		TableSizeCacheTableThreshold: 10,
		TableSizeCacheRAMMultiplier:  4,
		TableSizeCacheTTL:            3600,
	})

	err := gatherer.GetMetrics(tableSizeCacheTestMetrics())
	if !errors.Is(err, iterationFailure) {
		t.Fatalf("first GetMetrics() error = %v, want %v", err, iterationFailure)
	}

	second := tableSizeCacheTestMetrics()
	if err := gatherer.GetMetrics(second); err != nil {
		t.Fatalf("second GetMetrics() error = %v", err)
	}
	if got := second.DB.Metrics.Engine["InnoDB"]["Total Size"]; got != uint64(650) {
		t.Fatalf("second InnoDB Total Size = %#v (%T), want 650 (uint64)", got, got)
	}
	if got := second.DB.Metrics.TotalMyisamIndexes; got != 25 {
		t.Fatalf("second TotalMyisamIndexes = %d, want 25", got)
	}

	_, heavyQueries := fake.counts()
	if heavyQueries != 2 {
		t.Fatalf("heavy query count = %d, want 2 after first iteration failure", heavyQueries)
	}
	_, heavyCloses := fake.closeCounts()
	if heavyCloses != 2 {
		t.Fatalf("heavy rows close count = %d, want 2", heavyCloses)
	}
}

func TestDBMetricsConfigGathererSizesScanErrorRetriesWithoutCachingPartialSizes(t *testing.T) {
	fake := tableSizeCacheBoundaryFake()
	fake.heavyBatches = [][][]driver.Value{
		{
			{"InnoDB", int64(550), int64(11), int64(400), int64(150)},
			{"MyISAM", nil, int64(1), int64(30), int64(20)},
		},
		{
			{"InnoDB", int64(650), int64(12), int64(475), int64(175)},
			{"MyISAM", int64(60), int64(2), int64(35), int64(25)},
		},
	}
	setTableSizeCacheTestDB(t, fake)
	gatherer := newTableSizeCacheTestGatherer(config.Config{
		TableSizeCacheTableThreshold: 10,
		TableSizeCacheRAMMultiplier:  4,
		TableSizeCacheTTL:            3600,
	})

	if err := gatherer.GetMetrics(tableSizeCacheTestMetrics()); err == nil {
		t.Fatal("first GetMetrics() error = nil, want scan error")
	}

	second := tableSizeCacheTestMetrics()
	if err := gatherer.GetMetrics(second); err != nil {
		t.Fatalf("second GetMetrics() error = %v", err)
	}
	if got := second.DB.Metrics.Engine["InnoDB"]["Total Size"]; got != uint64(650) {
		t.Fatalf("second InnoDB Total Size = %#v (%T), want 650 (uint64)", got, got)
	}
	if got := second.DB.Metrics.TotalMyisamIndexes; got != 25 {
		t.Fatalf("second TotalMyisamIndexes = %d, want 25", got)
	}

	_, heavyQueries := fake.counts()
	if heavyQueries != 2 {
		t.Fatalf("heavy query count = %d, want 2 after first scan failure", heavyQueries)
	}
	_, heavyCloses := fake.closeCounts()
	if heavyCloses != 2 {
		t.Fatalf("heavy rows close count = %d, want 2", heavyCloses)
	}
}

func TestDBMetricsConfigGathererExpiredIterationErrorUsesStaleAndRetries(t *testing.T) {
	iterationFailure := errors.New("expired table size rows iteration failed")
	fake := tableSizeCacheBoundaryFake()
	fake.heavyBatches = [][][]driver.Value{
		{
			{"InnoDB", int64(550), int64(11), int64(400), int64(150)},
			{"MyISAM", int64(50), int64(1), int64(30), int64(20)},
		},
		{
			{"InnoDB", int64(999), int64(13), int64(700), int64(299)},
		},
		{
			{"InnoDB", int64(700), int64(14), int64(500), int64(200)},
			{"MyISAM", int64(70), int64(3), int64(40), int64(30)},
		},
	}
	fake.heavyIterationErrors = map[int]error{2: iterationFailure}
	setTableSizeCacheTestDB(t, fake)
	gatherer := newTableSizeCacheTestGatherer(config.Config{
		TableSizeCacheTableThreshold: 10,
		TableSizeCacheRAMMultiplier:  4,
		TableSizeCacheTTL:            3600,
	})
	baseTime := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	gatherer.now = tableSizeCacheTestClock(
		baseTime,
		baseTime.Add(time.Hour),
		baseTime.Add(time.Hour+time.Second),
	)

	first := tableSizeCacheTestMetrics()
	if err := gatherer.GetMetrics(first); err != nil {
		t.Fatalf("first GetMetrics() error = %v", err)
	}

	second := tableSizeCacheTestMetrics()
	if err := gatherer.GetMetrics(second); err != nil {
		t.Fatalf("second GetMetrics() error = %v, want stale fallback", err)
	}
	assertTableSizeCachePayload(t, second)

	third := tableSizeCacheTestMetrics()
	if err := gatherer.GetMetrics(third); err != nil {
		t.Fatalf("third GetMetrics() error = %v", err)
	}
	if got := third.DB.Metrics.Engine["InnoDB"]["Total Size"]; got != uint64(700) {
		t.Fatalf("third InnoDB Total Size = %#v (%T), want 700 (uint64)", got, got)
	}
	if got := third.DB.Metrics.TotalMyisamIndexes; got != 30 {
		t.Fatalf("third TotalMyisamIndexes = %d, want 30", got)
	}

	_, heavyQueries := fake.counts()
	if heavyQueries != 3 {
		t.Fatalf("heavy query count = %d, want 3 after failed expired refresh", heavyQueries)
	}
	_, heavyCloses := fake.closeCounts()
	if heavyCloses != 3 {
		t.Fatalf("heavy rows close count = %d, want 3", heavyCloses)
	}
}

func TestDBMetricsConfigGathererSupportIterationErrorIsReturned(t *testing.T) {
	iterationFailure := errors.New("engine support rows iteration failed")
	fake := tableSizeCacheBoundaryFake()
	fake.supportIterationErrors = map[int]error{1: iterationFailure}
	setTableSizeCacheTestDB(t, fake)
	gatherer := newTableSizeCacheTestGatherer(config.Config{
		TableSizeCacheTableThreshold: 10,
		TableSizeCacheRAMMultiplier:  4,
		TableSizeCacheTTL:            3600,
	})

	err := gatherer.GetMetrics(tableSizeCacheTestMetrics())
	if !errors.Is(err, iterationFailure) {
		t.Fatalf("GetMetrics() error = %v, want %v", err, iterationFailure)
	}

	supportQueries, heavyQueries := fake.counts()
	if supportQueries != 1 {
		t.Fatalf("support query count = %d, want 1", supportQueries)
	}
	if heavyQueries != 0 {
		t.Fatalf("heavy query count = %d, want 0", heavyQueries)
	}
	supportCloses, _ := fake.closeCounts()
	if supportCloses != 1 {
		t.Fatalf("support rows close count = %d, want 1", supportCloses)
	}
}

func TestDBMetricsConfigGathererSupportScanErrorClosesRows(t *testing.T) {
	fake := tableSizeCacheBoundaryFake()
	fake.supportRows = [][]driver.Value{
		{"InnoDB"},
	}
	setTableSizeCacheTestDB(t, fake)
	gatherer := newTableSizeCacheTestGatherer(config.Config{
		TableSizeCacheTableThreshold: 10,
		TableSizeCacheRAMMultiplier:  4,
		TableSizeCacheTTL:            3600,
	})

	if err := gatherer.GetMetrics(tableSizeCacheTestMetrics()); err == nil {
		t.Fatal("GetMetrics() error = nil, want support scan error")
	}

	_, heavyQueries := fake.counts()
	if heavyQueries != 0 {
		t.Fatalf("heavy query count = %d, want 0", heavyQueries)
	}
	supportCloses, _ := fake.closeCounts()
	if supportCloses != 1 {
		t.Fatalf("support rows close count = %d, want 1 after scan error", supportCloses)
	}
}

func TestDBMetricsConfigGathererFirstCloseErrorIsReturned(t *testing.T) {
	closeFailure := errors.New("table size rows close failed")
	fake := tableSizeCacheBoundaryFake()
	fake.heavyCloseErrors = map[int]error{1: closeFailure}
	setTableSizeCacheTestDB(t, fake)
	gatherer := newTableSizeCacheTestGatherer(config.Config{
		TableSizeCacheTableThreshold: 10,
		TableSizeCacheRAMMultiplier:  4,
		TableSizeCacheTTL:            3600,
	})

	err := gatherer.GetMetrics(tableSizeCacheTestMetrics())
	if !errors.Is(err, closeFailure) {
		t.Fatalf("GetMetrics() error = %v, want %v", err, closeFailure)
	}

	_, heavyQueries := fake.counts()
	if heavyQueries != 1 {
		t.Fatalf("heavy query count = %d, want 1", heavyQueries)
	}
	_, heavyCloses := fake.closeCounts()
	if heavyCloses != 1 {
		t.Fatalf("heavy rows close count = %d, want 1", heavyCloses)
	}
}

func tableSizeCacheBoundaryFake() *tableSizeCacheSQLFake {
	return &tableSizeCacheSQLFake{
		supportRows: [][]driver.Value{
			{"InnoDB", "DEFAULT"},
			{"MyISAM", "YES"},
		},
		heavyRows: [][]driver.Value{
			{"InnoDB", int64(550), int64(11), int64(400), int64(150)},
			{"MyISAM", int64(50), int64(1), int64(30), int64(20)},
		},
	}
}

func newTableSizeCacheTestGatherer(configuration config.Config) *DBMetricsConfigGatherer {
	logger := *logging.Init("mysql-table-size-cache-test", true, false, io.Discard)
	return NewDBMetricsConfigGatherer(logger, &configuration)
}

func tableSizeCacheTestClock(times ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		if index >= len(times) {
			return times[len(times)-1]
		}
		now := times[index]
		index++
		return now
	}
}

func tableSizeCacheTestMetrics() *models.Metrics {
	metrics := &models.Metrics{}
	metrics.DB.Metrics.Databases = []string{"app"}
	metrics.DB.Metrics.TotalTables = 12
	metrics.System.Info = models.MetricGroupValue{
		"PhysicalMemory": models.MetricGroupValue{
			"total": uint64(100),
		},
	}
	return metrics
}

func assertTableSizeCachePayload(t *testing.T, metrics *models.Metrics) {
	t.Helper()

	innodb := metrics.DB.Metrics.Engine["InnoDB"]
	wantInnoDB := models.MetricGroupValue{
		"Enabled":      "DEFAULT",
		"Table Number": uint64(11),
		"Total Size":   uint64(550),
		"Data Size":    uint64(400),
		"Index Size":   uint64(150),
	}
	for field, want := range wantInnoDB {
		if got := innodb[field]; got != want {
			t.Fatalf("InnoDB %q = %#v (%T), want %#v (%T)", field, got, got, want, want)
		}
	}
	if got := metrics.DB.Metrics.TotalMyisamIndexes; got != 20 {
		t.Fatalf("TotalMyisamIndexes = %d, want 20", got)
	}
}

type tableSizeCacheSQLFake struct {
	mu                     sync.Mutex
	supportRows            [][]driver.Value
	supportBatches         [][][]driver.Value
	heavyRows              [][]driver.Value
	heavyBatches           [][][]driver.Value
	heavyErrors            map[int]error
	supportIterationErrors map[int]error
	heavyIterationErrors   map[int]error
	supportCloseErrors     map[int]error
	heavyCloseErrors       map[int]error
	supportQueries         int
	heavyQueries           int
	supportCloses          int
	heavyCloses            int
}

func (fake *tableSizeCacheSQLFake) Open(string) (driver.Conn, error) {
	return &tableSizeCacheSQLConn{fake: fake}, nil
}

func (fake *tableSizeCacheSQLFake) counts() (int, int) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.supportQueries, fake.heavyQueries
}

func (fake *tableSizeCacheSQLFake) closeCounts() (int, int) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.supportCloses, fake.heavyCloses
}

type tableSizeCacheSQLConn struct {
	fake *tableSizeCacheSQLFake
}

func (connection *tableSizeCacheSQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("Prepare is not supported")
}

func (connection *tableSizeCacheSQLConn) Close() error { return nil }

func (connection *tableSizeCacheSQLConn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("Begin is not supported")
}

func (connection *tableSizeCacheSQLConn) QueryContext(
	_ context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Rows, error) {
	connection.fake.mu.Lock()
	defer connection.fake.mu.Unlock()

	switch query {
	case tableSizeCacheTestSupportQuery:
		if len(args) != 0 {
			return nil, fmt.Errorf("support query args = %v, want none", args)
		}
		connection.fake.supportQueries++
		values := tableSizeCacheDriverRowsForCall(
			connection.fake.supportBatches,
			connection.fake.supportRows,
			connection.fake.supportQueries,
		)
		call := connection.fake.supportQueries
		return &tableSizeCacheSQLRows{
			columns:  []string{"ENGINE", "SUPPORT"},
			values:   cloneTableSizeCacheDriverRows(values),
			nextErr:  connection.fake.supportIterationErrors[call],
			closeErr: connection.fake.supportCloseErrors[call],
			onClose: func() {
				connection.fake.mu.Lock()
				connection.fake.supportCloses++
				connection.fake.mu.Unlock()
			},
		}, nil
	case tableSizeCacheTestHeavyQuery:
		if len(args) != 1 || args[0].Value != "app" {
			return nil, fmt.Errorf("heavy query args = %v, want schema app", args)
		}
		connection.fake.heavyQueries++
		if err := connection.fake.heavyErrors[connection.fake.heavyQueries]; err != nil {
			return nil, err
		}
		values := tableSizeCacheDriverRowsForCall(
			connection.fake.heavyBatches,
			connection.fake.heavyRows,
			connection.fake.heavyQueries,
		)
		call := connection.fake.heavyQueries
		return &tableSizeCacheSQLRows{
			columns:  []string{"ENGINE", "TOTAL_SIZE", "TABLE_NUMBER", "DATA_SIZE", "INDEX_SIZE"},
			values:   cloneTableSizeCacheDriverRows(values),
			nextErr:  connection.fake.heavyIterationErrors[call],
			closeErr: connection.fake.heavyCloseErrors[call],
			onClose: func() {
				connection.fake.mu.Lock()
				connection.fake.heavyCloses++
				connection.fake.mu.Unlock()
			},
		}, nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
}

type tableSizeCacheSQLRows struct {
	columns  []string
	values   [][]driver.Value
	index    int
	nextErr  error
	closeErr error
	onClose  func()
	closed   bool
}

func (rows *tableSizeCacheSQLRows) Columns() []string { return rows.columns }

func (rows *tableSizeCacheSQLRows) Close() error {
	if rows.closed {
		return nil
	}
	rows.closed = true
	if rows.onClose != nil {
		rows.onClose()
	}
	return rows.closeErr
}

func (rows *tableSizeCacheSQLRows) Next(destination []driver.Value) error {
	if rows.nextErr != nil && rows.index >= 1 {
		return rows.nextErr
	}
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}

func cloneTableSizeCacheDriverRows(rows [][]driver.Value) [][]driver.Value {
	cloned := make([][]driver.Value, len(rows))
	for i := range rows {
		cloned[i] = append([]driver.Value(nil), rows[i]...)
	}
	return cloned
}

func tableSizeCacheDriverRowsForCall(
	batches [][][]driver.Value,
	fallback [][]driver.Value,
	call int,
) [][]driver.Value {
	if len(batches) == 0 {
		return fallback
	}
	index := call - 1
	if index >= len(batches) {
		index = len(batches) - 1
	}
	return batches[index]
}

func setTableSizeCacheTestDB(t *testing.T, fake *tableSizeCacheSQLFake) {
	t.Helper()

	driverName := fmt.Sprintf("mysql-table-size-cache-test-%d", tableSizeCacheTestDriverID.Add(1))
	sql.Register(driverName, fake)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}

	previousDB := models.DB
	models.DB = db
	t.Cleanup(func() {
		models.DB = previousDB
		_ = db.Close()
	})
}

var _ driver.Driver = (*tableSizeCacheSQLFake)(nil)
var _ driver.QueryerContext = (*tableSizeCacheSQLConn)(nil)
var _ driver.Rows = (*tableSizeCacheSQLRows)(nil)
