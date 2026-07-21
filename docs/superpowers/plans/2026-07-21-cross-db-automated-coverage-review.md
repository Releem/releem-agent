# Cross-Database Automated Coverage and Review Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Review every Agent and Platform change from `origin/master`, add complete behavioral coverage for schema checks, query optimization, and applied-index detection, and fix all blocking correctness findings before live testing.

**Architecture:** Four expert reviewers first inspect the complete branch diffs from independent database, Python, and architecture perspectives. Test workers then own disjoint test files, while the main agent owns all production-code fixes and integration. A final independent review repeats the full diff audit after the suites pass.

**Tech Stack:** Go 1.x, `database/sql`, PostgreSQL, MySQL/MariaDB, Python 3, `unittest`, Bats, ClickHouse mocks, S3 fakes.

## Global Constraints

- Agent worktree is `/home/dkochetov/Документы/laptop/releem/worktrees/releem-agent-pg-query-optimization-and-schema-checks`.
- Platform worktree is `/home/dkochetov/Документы/laptop/releem/worktrees/releem-platform-pg-query-optimization-and-schema-checks`.
- Compare each branch with its own `origin/master` merge base.
- Review all changed production code in `origin/master...HEAD`, not only PostgreSQL files.
- Query optimization disabled must preserve the existing monitoring payload.
- PostgreSQL native timing values stay in microseconds.
- The PostgreSQL collector gathers 100 successful EXPLAIN plans by total time and 100 additional unique successful plans by mean time.
- Exhaustion recommendations use a strict `> 75%` threshold; exactly 75% does not trigger.
- PostgreSQL destructive index checks preserve partial predicates, INCLUDE columns, expression identity, ordering, NULL ordering, opclass, collation, uniqueness, validity, partition identity, clustered state, and replica identity.
- MySQL `INDEX_DUPLICATION` means exact duplicate; `REDUNDANT_INDEX` includes exact and covering indexes; `UNUSED_INDEX` uses `Min_Uptime_Unused_Index`.
- Test workers may edit only their assigned test files. Production fixes belong to the main agent.
- Do not revert unrelated changes.
- Do not create implementation commits unless the user explicitly requests them.

---

### Task 1: Capture Baselines and Dispatch Four Expert Reviews

**Files:**
- Create outside repositories: `/tmp/releem-cross-db-review/agent-baseline.txt`
- Create outside repositories: `/tmp/releem-cross-db-review/platform-baseline.txt`
- Create outside repositories: `/tmp/releem-cross-db-review/{postgresql,mysql-mariadb,python,architecture}.md`

**Interfaces:**
- Produces four findings reports with severity, exact file/line, missing test, and proposed fix.
- Produces one changed-file-to-test ownership table consumed by Tasks 2-8.

- [ ] **Step 1: Capture reproducible branch baselines**

Run in each worktree:

```bash
git status --short --branch
git merge-base HEAD origin/master
git log --oneline origin/master..HEAD
git diff --stat origin/master...HEAD
git diff --check origin/master...HEAD
```

Write the command, output, current HEAD, and merge-base SHA to the corresponding baseline file.

- [ ] **Step 2: Run baseline suites before adding tests**

Agent:

```bash
/usr/local/go/bin/go test -count=1 ./...
bats tests/install.bats tests/mysqlconfigurer.bats
```

Platform:

```bash
./scripts/run_python_tests.sh all
```

Record every failure exactly. Do not hide baseline failures behind later fixes.

- [ ] **Step 3: Dispatch the PostgreSQL DBA reviewer**

The reviewer reads both full diffs and reports on PostgreSQL catalogs, version gates, PGSS, EXPLAIN, search path, sequence exhaustion, stale statistics, structured index identity, partitions, failure continuation, and recommendation safety. It must enumerate uncovered branches and must not edit files.

- [ ] **Step 4: Dispatch the MySQL/MariaDB DBA reviewer**

The reviewer reads both full diffs and reports on MySQL/MariaDB metadata, AUTO_INCREMENT, fragmentation, exact/covering/unused indexes, uptime, foreign-key support, generated columns, managed-service compatibility, and installer grants. It must enumerate uncovered branches and must not edit files.

- [ ] **Step 5: Dispatch the Python reviewer**

The reviewer reads the Platform full diff and reports on payload validation, S3 failure semantics, ClickHouse writes, TWEngine adaptation, queue transitions, applied-index parsing, environment isolation, mocks, and exception handling. It must enumerate uncovered branches and must not edit files.

- [ ] **Step 6: Dispatch the architecture reviewer**

The reviewer reads both full diffs and reports on Agent-to-Platform contracts, legacy compatibility, failure metadata, observability, resource lifetime, destructive recommendations, process boundaries, and deployment order. It must map each changed production file to at least one existing or missing test and must not edit files.

- [ ] **Step 7: Reconcile reports**

Create a flat findings table:

```text
ID | Severity | Repository | File:line | Behavior | Existing test | Required test | Production fix
```

Deduplicate equivalent findings. Preserve disagreements as explicit adjudication items for the main agent.

---

### Task 2: Fix Build, Contract, and Baseline Blockers

**Files:**
- Modify only production files identified by Task 1 findings.
- Test existing focused files before creating broader matrix tests.

**Interfaces:**
- Produces buildable Agent and importable Platform.
- Preserves current query-optimization-disabled payload exactly.
- Establishes explicit required/optional payload behavior for later tests.

- [ ] **Step 1: Add or select the smallest failing regression test for each blocker**

For an Agent contract blocker, use an exact key-set assertion:

```go
func assertExactKeys(t *testing.T, row models.MetricGroupValue, expected ...string) {
	t.Helper()
	got := make(map[string]bool, len(row))
	for key := range row { got[key] = true }
	for _, key := range expected {
		if !got[key] { t.Fatalf("missing key %q in %#v", key, row) }
		delete(got, key)
	}
	if len(got) != 0 { t.Fatalf("unexpected keys: %#v", got) }
}
```

For Platform payload failures, assert the exact exception and message category:

```python
with self.assertRaises(PostgresPayloadError) as raised:
    validate_postgresql_db_payload(payload)
self.assertIn("Missing PostgreSQL", str(raised.exception))
```

- [ ] **Step 2: Verify RED**

Run only the focused package/module and confirm the regression test fails for the reported defect.

- [ ] **Step 3: Implement the minimum production fix**

Keep the fix inside the existing module boundary. Do not add format fields to all monitoring payloads merely to identify the query-optimization payload; use the existing task endpoint/collection mode or an optimization-only container.

- [ ] **Step 4: Verify blocker tests and complete baseline suites**

```bash
/usr/local/go/bin/go test -count=1 ./...
./scripts/run_python_tests.sh all
```

Expected: Agent builds and all previously recorded baseline failures are either green or documented as unrelated pre-existing failures with exact evidence.

---

### Task 3: Complete PostgreSQL Agent Collector Coverage

**Files:**
- Create: `metrics/postgresql/query_optimization_contract_test.go`
- Create: `metrics/postgresql/schema_matrix_test.go`
- Extend only when unavoidable: `metrics/postgresql/schema_integration_test.go`

**Interfaces:**
- Exercises `PostgresCapabilities`, `collectPostgresQueryDetails`, `collectExplainDetails`, `CollectDbSchema`, structured index serialization, and failure continuation.
- Produces fixtures matching Platform native lowercase PostgreSQL fields.

- [ ] **Step 1: Add table-driven capability tests**

```go
func TestPgStatStatementsCapabilityMatrix(t *testing.T) {
	tests := []struct{
		name, timing string
		hasRows bool
		wantRowsExpr string
	}{
		{"pg12_total_time_without_rows", "total_time", false, "0::bigint"},
		{"pg14_total_exec_time_with_rows", "total_exec_time", true, "sum(s.rows)::bigint"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query := pgStatStatementsQuery(PostgresCapabilitySnapshot{TimingColumn: test.timing, HasRows: test.hasRows, PgStatStatementsRelation: `"public"."pg_stat_statements"`}, postgresQueryFull)
			if !strings.Contains(query, test.wantRowsExpr) { t.Fatalf("query=%s", query) }
		})
	}
}
```

- [ ] **Step 2: Add the 100 plus 100 successful EXPLAIN matrix**

Assert failures and empty plans do not consume quotas, candidates are unique across rankings, connection failures are cached per database, and a new collection resets state.

- [ ] **Step 3: Add structured schema matrix cases**

The table must include ordinary, expression, partial, INCLUDE, descending, NULLS FIRST, custom opclass, custom collation, unique, invalid, unready, exclusion, partitioned, attached partition, clustered, and replica-identity indexes. Assert exact serialized key objects and booleans.

- [ ] **Step 4: Add section and database failure continuation tests**

Assert successful sections before and after a failed section remain present. Assert failure metadata identifies enough context for Platform to reject unsafe incomplete snapshots after Task 2 fixes.

- [ ] **Step 5: Run tests**

```bash
/usr/local/go/bin/go test -count=1 ./metrics/postgresql
/usr/local/go/bin/go test -race -count=1 ./metrics/postgresql
```

Expected: all tests pass; live integration tests skip only when their DSN environment variables are absent.

---

### Task 4: Complete MySQL and MariaDB Agent Collector Coverage

**Files:**
- Create: `metrics/mysql/schema_matrix_additional_test.go`
- Extend only when unavoidable: `metrics/mysql/dbCollectQueries_schema_test.go`
- Extend installer cases: `tests/install.bats`

**Interfaces:**
- Exercises MySQL/MariaDB schema metadata serialization and query-optimization grants.
- Produces payload fixtures consumed by Platform MySQL tests.

- [ ] **Step 1: Add table-driven schema serialization cases**

Cover AUTO_INCREMENT signed/unsigned types, generated columns, functional expressions, nullable index fields, prefix lengths, invisible indexes, FULLTEXT, SPATIAL, foreign-key usage, and MariaDB missing/nullable metadata.

```go
tests := []struct {
	name string
	row mysqlIndexSchemaMetric
	wantExpression interface{}
}{
	{"ordinary", mysqlIndexSchemaMetric{ColumnName: "customer_id"}, nil},
	{"functional", mysqlIndexSchemaMetric{Expression: "lower(`email`)"}, "lower(`email`)"},
}
```

- [ ] **Step 2: Add cloud and permission failure tests**

Assert one inaccessible schema does not discard successful schemas, and managed-service permission failures are reported without panic.

- [ ] **Step 3: Extend Bats grant coverage**

Assert generated PostgreSQL and MySQL grants contain only simple GRANT statements, target the configured monitoring user, and do not use procedures or expose passwords.

- [ ] **Step 4: Run tests**

```bash
/usr/local/go/bin/go test -count=1 ./metrics/mysql
bats tests/install.bats tests/mysqlconfigurer.bats
```

---

### Task 5: Enforce Platform Payload and S3 Snapshot Contracts

**Files:**
- Create: `tests/test_postgresql_payload_contract.py`
- Create: `tests/test_query_optimization_s3_contract.py`
- Modify as findings require: `src/v2/postgresql_payload.py`
- Modify as findings require: `src/v2/schema_checks/s3_storage.py`
- Modify as findings require: `main_api.py`

**Interfaces:**
- `validate_postgresql_db_payload(db_payload)` validates optimization query and schema rows.
- Required S3 JSON reads raise a typed error; optional history reads retain default behavior.
- Incomplete Agent schema metadata prevents schema recommendations.

- [ ] **Step 1: Write exact payload validation tests**

```python
def test_pg_query_row_requires_native_microsecond_fields(self):
    payload = {"Queries": [{"datname": "app", "queryid": "1", "calls": 1, "rows": 1}]}
    with self.assertRaises(PostgresPayloadError):
        validate_postgresql_db_payload(payload)

def test_pg_schema_rejects_string_boolean(self):
    payload = pg_payload_with_index(is_valid="false")
    with self.assertRaises(PostgresPayloadError):
        validate_postgresql_db_payload(payload)
```

Also cover structured EXPLAIN list/dict/string, null query ID, negative counts, uppercase legacy fields in optimization payloads, and exact optional fields.

- [ ] **Step 2: Write required S3 object tests**

```python
with self.assertRaises(QueryOptimizationSnapshotError):
    read_required_s3_json(fake_s3, bucket, "missing.json")
```

Assert missing/corrupt current queries, tables, columns, indexes, variables, or metrics fails the queue item. Historical optional snapshots may still return `None` and be skipped.

- [ ] **Step 3: Implement validation at API ingestion and worker reload**

Call PostgreSQL validation only for the query-optimization endpoint/task payload, preserving marker-free regular monitoring. Persist and reload enough schema failure metadata to reject incomplete destructive analysis.

- [ ] **Step 4: Run tests**

```bash
/home/dkochetov/Документы/laptop/releem/Releem_Platform/venv/bin/python -B -m unittest \
  tests.test_postgresql_payload_contract \
  tests.test_query_optimization_s3_contract \
  tests.test_query_optimization_memory
```

---

### Task 6: Complete PostgreSQL Platform Schema-Check Coverage

**Files:**
- Create: `tests/test_pg_schema_check_matrix.py`
- Modify as findings require: `src/v2/schema_checks/query_optimization_schema.py`
- Modify as findings require: `src/v2/schema_checks/index_checks.py`
- Modify as findings require: `src/v2/schema_checks/common.py`

**Interfaces:**
- Consumes native PostgreSQL schema sections.
- Produces deterministic check rows for sequence exhaustion, stale statistics, missing PK, exact duplicate, redundant, and unused indexes.

- [ ] **Step 1: Add a scenario table for sequence and stale-statistics checks**

```python
cases = [
    ("below_threshold", pg_sequence(last_value="74", min_value="1", max_value="100"), False),
    ("exact_threshold", pg_sequence(last_value="75", min_value="0", max_value="100"), False),
    ("above_threshold", pg_sequence(last_value="76", min_value="0", max_value="100"), True),
    ("cycling", pg_sequence(last_value="99", min_value="0", max_value="100", cycle=True), False),
]
```

Cover ascending, descending, owned, unowned, integer, bigint, missing last value, zero increment, fresh analyze, fresh autoanalyze, high churn, empty table, and missing timestamps.

- [ ] **Step 2: Add the PostgreSQL index identity matrix**

Cover exact duplicates, covering prefixes, different order, expressions, predicates, INCLUDE, uniqueness, access method, direction, NULL order, opclass, collation, `NULLS NOT DISTINCT`, partitions, validity, exclusion, cluster, replica identity, FK support, and uptime/stat-reset gates.

- [ ] **Step 3: Assert both findings and non-findings**

Every positive case asserts type, severity, schema, table, index identity, and SQL. Every negative case asserts the candidate index is absent from all destructive recommendations.

- [ ] **Step 4: Run tests**

```bash
/home/dkochetov/Документы/laptop/releem/Releem_Platform/venv/bin/python -B -m unittest \
  tests.test_pg_schema_check_matrix \
  tests.test_query_optimization_schema_checks \
  tests.test_schema_healthcheck
```

---

### Task 7: Complete MySQL and MariaDB Platform Schema-Check Coverage

**Files:**
- Create: `tests/test_mysql_schema_check_matrix.py`
- Modify as findings require: `src/v2/schema_checks/query_optimization_schema.py`
- Modify as findings require: `src/v2/schema_checks/duplicate_indexes.py`
- Modify as findings require: `src/v2/schema_checks/redundant_indexes.py`
- Modify as findings require: `src/v2/schema_checks/unused_indexes.py`

**Interfaces:**
- Produces distinct `INDEX_DUPLICATION`, `REDUNDANT_INDEX`, and `UNUSED_INDEX` results.
- Uses one `Min_Uptime_Unused_Index` contract for unused-index history.

- [ ] **Step 1: Add AUTO_INCREMENT and fragmentation boundaries**

```python
cases = [
    ("int_74", mysql_autoincrement("int", 1_589_137_898), False),
    ("int_75", mysql_autoincrement("int", 1_610_612_735), False),
    ("int_76", mysql_autoincrement("int", 1_632_087_971), True),
]
```

Compute production fixture values from the signed/unsigned type maximum instead of embedding approximate percentages in assertions. Cover BIGINT, unsigned types, non-auto-increment columns, table-size guards, and zero/NULL size fields.

- [ ] **Step 2: Add exact, covering, and unused index matrices**

Assert exact duplicate indexes appear in both `INDEX_DUPLICATION` and `REDUNDANT_INDEX`; strict left-prefix indexes appear only in `REDUNDANT_INDEX`; unused indexes appear only in `UNUSED_INDEX` after sufficient uptime. Cover prefix length, expression, visibility, type, order, uniqueness, PK and FK-support exclusions.

- [ ] **Step 3: Add charset, collation, datatype, PK, and FK cases**

Use isolated fixtures so each case proves one finding and one non-finding. Include MariaDB-shaped rows with absent optional keys.

- [ ] **Step 4: Run tests**

```bash
/home/dkochetov/Документы/laptop/releem/Releem_Platform/venv/bin/python -B -m unittest \
  tests.test_mysql_schema_check_matrix \
  tests.test_query_optimization_schema_checks \
  tests.test_schema_healthcheck
```

---

### Task 8: Complete Query Optimization and Applied-Index Detection Coverage

**Files:**
- Create: `tests/test_applied_index_detection_matrix.py`
- Extend: `tests/test_releem_collector_pg.py`
- Extend: `tests/test_clickhousedb_pg.py`
- Extend: `tests/test_query_optimization_processing.py`
- Modify as findings require: `src/v2/query_optimization_index_apply.py`
- Modify as findings require: `query_optimization.py`
- Modify as findings require: `src/v2/releem_collector_pg.py`

**Interfaces:**
- Parses TWEngine CREATE INDEX statements into native index identity.
- Matches exact-name and equivalent differently named indexes without accepting non-equivalent definitions.
- Completes or fails queue records deterministically.

- [ ] **Step 1: Add PostgreSQL applied-index cases**

```python
cases = [
    ("same_name_same_definition", "CREATE INDEX idx_a ON app.t (a)", pg_index("idx_a", ["a"]), True),
    ("different_name_same_definition", "CREATE INDEX idx_a ON app.t (a)", pg_index("idx_b", ["a"]), True),
    ("different_predicate", "CREATE INDEX idx_a ON app.t (a) WHERE active", pg_index("idx_b", ["a"], predicate="deleted = false"), False),
]
```

Add quoted schema/table/index names, expression keys, INCLUDE, DESC, NULLS FIRST/LAST, uniqueness, access method, opclass, collation, `NULLS NOT DISTINCT`, attached partitions, invalid indexes, and same table name in two schemas.

- [ ] **Step 2: Add MySQL applied-index cases**

Cover exact name, equivalent different name, order mismatch, prefix mismatch, functional expression, uniqueness, visibility, FULLTEXT/SPATIAL types, and schema qualification.

- [ ] **Step 3: Add worker and TWEngine response cases**

Assert no-job queries are skipped, structured EXPLAIN is saved, malformed recommendation structures do not abort unrelated queries, all queue terminal states are explicit, and schema payload memory is released after use.

- [ ] **Step 4: Run tests**

```bash
/home/dkochetov/Документы/laptop/releem/Releem_Platform/venv/bin/python -B -m unittest \
  tests.test_applied_index_detection_matrix \
  tests.test_releem_collector_pg \
  tests.test_clickhousedb_pg \
  tests.test_query_optimization_processing \
  tests.test_query_optimization_memory
```

---

### Task 9: Automated Gate and Final Expert Review

**Files:**
- Update outside repositories: `/tmp/releem-cross-db-review/final-review.md`
- Update outside repositories: `/tmp/releem-cross-db-review/coverage-map.md`

**Interfaces:**
- Produces the green gate required by the live-validation plan.

- [ ] **Step 1: Run complete suites from clean process environments**

```bash
/usr/local/go/bin/go test -count=1 ./...
/usr/local/go/bin/go test -race -count=1 ./metrics/postgresql ./metrics/mysql
bats tests/install.bats tests/mysqlconfigurer.bats
./scripts/run_python_tests.sh all
```

- [ ] **Step 2: Capture coverage evidence**

```bash
/usr/local/go/bin/go test -coverprofile=/tmp/releem-agent-postgresql.cover ./metrics/postgresql
/usr/local/go/bin/go test -coverprofile=/tmp/releem-agent-mysql.cover ./metrics/mysql
/home/dkochetov/Документы/laptop/releem/Releem_Platform/venv/bin/python -m coverage run --branch -m unittest discover -s tests -p 'test*.py'
/home/dkochetov/Документы/laptop/releem/Releem_Platform/venv/bin/python -m coverage report -m
```

If `coverage` is unavailable, record that fact and use the reviewed changed-file-to-test map; do not add a runtime dependency solely for reporting.

- [ ] **Step 3: Repeat all four expert reviews**

Each reviewer reads `origin/master...HEAD` plus the working-tree diff, the initial report, test results, and coverage map. Reviewers report only remaining actionable findings with exact file/line references.

- [ ] **Step 4: Fix and re-review all blocking findings**

Use one consolidated fix pass in the main stream. Add a failing regression test before each production fix, rerun focused tests, then repeat the relevant reviewer.

- [ ] **Step 5: Verify final worktree state**

```bash
git diff --check
git status --short --branch
```

Expected: no formatting errors, no generated binaries or secret-bearing artifacts, and no unresolved P0/P1/P2 or Critical/High/Important findings.
