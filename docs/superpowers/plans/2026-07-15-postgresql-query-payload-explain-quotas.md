# PostgreSQL Query Payload and EXPLAIN Quotas Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve the legacy PostgreSQL monitoring payload when query optimization is disabled and collect 100 additional unique successful EXPLAIN plans for each ranking when it is enabled.

**Architecture:** Choose the legacy or versioned serializer at the Agent collection boundary. Keep Agent-to-Platform timing values in microseconds. Use independent success quotas for total-time and mean-time rankings with one shared deduplication and connection state.

**Tech Stack:** Go, `database/sql`, PostgreSQL `pg_stat_statements`, Python dataclasses, `unittest`.

## Global Constraints

- Disabled query optimization emits the `origin/master` PostgreSQL query fields.
- Legacy payloads omit `rows`, `SUM_ROWS_SENT`, and both format markers.
- Versioned payloads use `total_exec_time_us` and `mean_exec_time_us` directly.
- Schema collection and format markers occur only with query optimization enabled.
- Each ranking stops at 100 successful EXPLAIN results or candidate exhaustion.
- Failed or empty results do not consume a success quota.
- The mean-time pass does not retry any total-time candidate.
- Unrelated metrics, installer, and MySQL behavior remain unchanged.

---

### Task 1: Mode-Specific Agent Query Payloads

**Files:**
- Modify: `metrics/postgresql/query_collector.go`
- Modify: `metrics/postgresql/dbCollectQueries.go`
- Test: `metrics/postgresql/collector_refactor_test.go`
- Test: `metrics/postgresql/dbCollectQueries_schema_test.go`

**Interfaces:**
- Consumes: `PostgresQueryDetail` and `Config.QueryOptimization`.
- Produces: `legacyMetricGroupValue()`, `postgresQueryDetailMetricsForMode(rows, enabled)`, and `configurePostgresQueryPayloadMode(metrics, enabled)`.

- [ ] **Step 1: Write the failing payload-mode test**

```go
func TestPostgresQueryPayloadModes(t *testing.T) {
	row := PostgresQueryDetail{
		PostgresQueryStats: PostgresQueryStats{
			Datname: "app", QueryID: "42", Calls: 3,
			TotalExecTimeUS: 9000, MeanExecTimeUS: 3000, Rows: 12,
		},
		Query: "select 1",
	}
	legacy := postgresQueryDetailMetricsForMode([]PostgresQueryDetail{row}, false)[0]
	for key, want := range map[string]interface{}{
		"query": "select 1", "query_text": "select 1",
		"total_exec_time_us": float64(9000), "mean_exec_time_us": float64(3000),
	} {
		if legacy[key] != want { t.Fatalf("legacy %s: got %#v want %#v", key, legacy[key], want) }
	}
	for _, key := range []string{"rows", "SUM_ROWS_SENT", "total_exec_time_ms", "mean_exec_time_ms"} {
		if _, ok := legacy[key]; ok { t.Fatalf("legacy payload contains %s", key) }
	}
	native := postgresQueryDetailMetricsForMode([]PostgresQueryDetail{row}, true)[0]
	if native["rows"] != uint64(12) || native["total_exec_time_us"] != float64(9000) {
		t.Fatalf("unexpected native payload: %#v", native)
	}
}

func TestPostgresPayloadModeControlsFormatMarkers(t *testing.T) {
	metrics := &models.Metrics{}
	configurePostgresQueryPayloadMode(metrics, false)
	if metrics.DB.QueriesFormat != "" || metrics.DB.DatabaseSchemaFormat != "" {
		t.Fatalf("legacy mode contains format markers: %#v", metrics.DB)
	}
	configurePostgresQueryPayloadMode(metrics, true)
	if metrics.DB.QueriesFormat != postgresQueryPayloadFormat {
		t.Fatalf("missing native query format: %#v", metrics.DB)
	}
}
```

- [ ] **Step 2: Run the test and verify RED**

```bash
/usr/local/go/bin/go test -count=1 ./metrics/postgresql -run 'TestPostgres(QueryPayloadModes|PayloadModeControlsFormatMarkers)$'
```

Expected: compilation fails because the microsecond fields and mode helpers do not exist.

- [ ] **Step 3: Implement microsecond fields and serializers**

```go
type PostgresQueryStats struct {
	Datname string
	QueryID string
	Calls uint64
	TotalExecTimeUS float64
	MeanExecTimeUS float64
	Rows uint64
}

func (stats PostgresQueryStats) metricGroupValue() models.MetricGroupValue {
	return models.MetricGroupValue{
		"datname": stats.Datname, "queryid": stats.QueryID, "calls": stats.Calls,
		"total_exec_time_us": stats.TotalExecTimeUS,
		"mean_exec_time_us": stats.MeanExecTimeUS, "rows": stats.Rows,
	}
}

func (detail PostgresQueryDetail) legacyMetricGroupValue() models.MetricGroupValue {
	return models.MetricGroupValue{
		"datname": detail.Datname, "queryid": detail.QueryID,
		"query": detail.Query, "query_text": detail.Query, "calls": detail.Calls,
		"total_exec_time_us": detail.TotalExecTimeUS,
		"mean_exec_time_us": detail.MeanExecTimeUS,
	}
}
```

Change the `pg_stat_statements` expressions to multiply PostgreSQL milliseconds by 1000 and alias them as `total_exec_time_us` and `mean_exec_time_us`. Scan into the renamed fields. Implement `postgresQueryDetailMetricsForMode` by choosing `metricGroupValue` only for enabled optimization and `legacyMetricGroupValue` otherwise. Update existing test fixtures to use `TotalExecTimeUS`, `MeanExecTimeUS`, and the `_us` ranking names.

- [ ] **Step 4: Switch format and serializer at `GetMetrics`**

```go
func configurePostgresQueryPayloadMode(metrics *models.Metrics, enabled bool) {
	metrics.DB.QueriesFormat = ""
	metrics.DB.DatabaseSchemaFormat = ""
	if enabled { setPostgresQueryPayloadFormat(metrics) }
}
```

Call the helper at entry, serialize through `postgresQueryDetailMetricsForMode`, and rename ranking selectors to `total_exec_time_us` and `mean_exec_time_us`. Keep `setPostgresSchemaPayloadFormat` after the existing optimization guard.

- [ ] **Step 5: Verify GREEN and scope**

```bash
/usr/local/go/bin/go test -count=1 ./metrics/postgresql
```

```bash
git diff --check -- metrics/postgresql/query_collector.go metrics/postgresql/dbCollectQueries.go metrics/postgresql/collector_refactor_test.go metrics/postgresql/dbCollectQueries_schema_test.go
```

Expected: tests pass and diff check is empty. Do not commit implementation files without an explicit user request.

---

### Task 2: Independent Successful EXPLAIN Quotas

**Files:**
- Modify: `metrics/postgresql/explain.go`
- Modify: `metrics/postgresql/dbCollectQueries.go`
- Test: `metrics/postgresql/collector_refactor_test.go`
- Test: `metrics/postgresql/dbCollectQueries_schema_test.go`

**Interfaces:**
- Consumes: Task 1 microsecond rankings.
- Produces: `collectExplainDetails(...) int` and injectable state functions.

- [ ] **Step 1: Write a failing two-quota test**

Build 205 eligible details: the first 105 rank highest by total time, the final 100 rank highest by mean time, and the first 5 EXPLAIN calls fail. Configure the state as follows:

```go
state.connect = func(*config.Config, logging.Logger, string) *sql.DB { return db }
state.executeExplain = func(_ *sql.DB, queryID, _ string, _ bool, _ logging.Logger) (string, error) {
	if queryID < "005" { return "", errors.New("expected explain failure") }
	return "{}", nil
}
totalSuccesses := collectExplainDetails(details, "total_exec_time_us", true, logger, &config.Config{}, state)
meanSuccesses := collectExplainDetails(details, "mean_exec_time_us", true, logger, &config.Config{}, state)
if totalSuccesses != 100 || meanSuccesses != 100 {
	t.Fatalf("success quotas: total=%d mean=%d", totalSuccesses, meanSuccesses)
}
explained := 0
for _, detail := range details { if detail.Explain != "" { explained++ } }
if explained != 200 { t.Fatalf("expected 200 unique plans, got %d", explained) }
```

Delete the obsolete attempt-cap test. Keep the test that proves deduplication resets for a new collection.

- [ ] **Step 2: Run the quota test and verify RED**

```bash
/usr/local/go/bin/go test -count=1 ./metrics/postgresql -run TestPostgresqlExplainCollectsIndependentSuccessfulQuotas
```

Expected: compilation fails because injected functions and a success return value do not exist, or fewer than 200 plans are produced.

- [ ] **Step 3: Implement success quotas**

```go
const maxPgExplainSuccessesPerRanking = 100

type pgExplainCollectionState struct {
	attempted map[string]struct{}
	database string
	db *sql.DB
	failedDatabases map[string]struct{}
	connect func(*config.Config, logging.Logger, string) *sql.DB
	executeExplain func(*sql.DB, string, string, bool, logging.Logger) (string, error)
}

func newPgExplainCollectionState() *pgExplainCollectionState {
	return &pgExplainCollectionState{
		attempted: make(map[string]struct{}),
		connect: u.ConnectionDatabase,
		executeExplain: ExecuteExplain,
	}
}
```

Remove the global attempt count. `tryBeginAttempt` rejects only empty or duplicate keys. `collectExplainDetails` uses a local success count, increments it only for non-empty plans, returns it, and stops at `maxPgExplainSuccessesPerRanking`. Both calls in `GetMetrics` keep sharing one state.

- [ ] **Step 4: Verify GREEN and scope**

```bash
/usr/local/go/bin/go test -count=1 ./metrics/postgresql
```

```bash
/usr/local/go/bin/go test -count=1 ./...
```

```bash
git diff --check -- metrics/postgresql/explain.go metrics/postgresql/dbCollectQueries.go metrics/postgresql/collector_refactor_test.go metrics/postgresql/dbCollectQueries_schema_test.go
```

Expected: both suites pass and diff check is empty. Do not commit implementation files without an explicit user request.

---

### Task 3: Platform Microsecond Contract

**Files:**
- Modify: `/home/dkochetov/Документы/laptop/releem/worktrees/releem-platform-pg-query-optimization-and-schema-checks/src/v2/postgresql_payload.py`
- Modify: `/home/dkochetov/Документы/laptop/releem/worktrees/releem-platform-pg-query-optimization-and-schema-checks/src/v2/dboptimizer_config.py`
- Test: `/home/dkochetov/Документы/laptop/releem/worktrees/releem-platform-pg-query-optimization-and-schema-checks/tests/test_postgresql_payload.py`
- Test: `/home/dkochetov/Документы/laptop/releem/worktrees/releem-platform-pg-query-optimization-and-schema-checks/tests/test_releem_collector_pg.py`
- Test: `/home/dkochetov/Документы/laptop/releem/worktrees/releem-platform-pg-query-optimization-and-schema-checks/tests/test_clickhousedb_pg.py`

**Interfaces:**
- Consumes: versioned rows containing `_us` timing fields and `rows`.
- Produces: direct `PostgresQuery.total_exec_time_us`, `mean_exec_time_us`, and `rows_per_call` values.

- [ ] **Step 1: Write failing parser tests**

```python
query = parse_postgresql_queries([{
    "datname": "appdb", "queryid": 42, "calls": 4,
    "total_exec_time_us": 12500, "mean_exec_time_us": 3125,
    "rows": 10, "query": "select * from orders",
}], POSTGRESQL_PGSS_FORMAT)[0]
self.assertEqual(12500, query.total_exec_time_us)
self.assertEqual(3125, query.mean_exec_time_us)
self.assertEqual(2, query.rows_per_call)
```

Add a test that supplies `_ms` fields and expects `PostgresPayloadError` mentioning `total_exec_time_us`. Update PG collector and ClickHouse fixtures to `_us` without changing expected stored values.

- [ ] **Step 2: Run focused tests and verify RED**

```bash
/home/dkochetov/Документы/laptop/releem/Releem_Platform/venv/bin/python -B -m unittest tests.test_postgresql_payload tests.test_releem_collector_pg tests.test_clickhousedb_pg
```

Expected: parser failures because the dataclass still requires `_ms` fields.

- [ ] **Step 3: Implement direct microsecond parsing**

```python
total_exec_time_us: float
mean_exec_time_us: float
```

Require and parse those names in `PostgresQuery.from_row`. Remove properties that multiply by 1000. Preserve `rows_per_call`. Change PostgreSQL `dboptimizer_config` entries to:

```python
"sum_time_us": "total_exec_time_us",
"avg_time_us": "mean_exec_time_us",
```

- [ ] **Step 4: Verify GREEN and scope**

```bash
/home/dkochetov/Документы/laptop/releem/Releem_Platform/venv/bin/python -B -m unittest tests.test_postgresql_payload tests.test_releem_collector_pg tests.test_clickhousedb_pg
```

```bash
git diff --check -- src/v2/postgresql_payload.py src/v2/dboptimizer_config.py tests/test_postgresql_payload.py tests/test_releem_collector_pg.py tests/test_clickhousedb_pg.py
```

Expected: focused tests pass and diff check is empty. If an existing background thread keeps the process alive after the success summary, record the summary and terminate only that process. Do not commit implementation files without an explicit user request.

---

### Task 4: Cross-Repository Verification

**Files:**
- Verify: all Agent and Platform files from Tasks 1-3.

**Interfaces:**
- Consumes: final legacy and versioned payloads.
- Produces: test evidence for both contracts.

- [ ] **Step 1: Run Agent verification**

```bash
/usr/local/go/bin/go test -count=1 ./...
```

```bash
bash -n install.sh
```

Expected: Go tests pass and shell syntax exits zero. Existing installer Bats failures are reported separately.

- [ ] **Step 2: Run Platform verification**

```bash
/home/dkochetov/Документы/laptop/releem/Releem_Platform/venv/bin/python -B -m unittest tests.test_postgresql_payload tests.test_releem_collector_pg tests.test_clickhousedb_pg
```

Expected: focused tests pass.

- [ ] **Step 3: Inspect final scope in both worktrees**

```bash
git status --short
```

```bash
git diff --check
```

Expected: no new whitespace errors in touched files and all pre-existing unrelated changes remain preserved.
