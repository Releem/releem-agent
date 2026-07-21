# EXPLAIN SELECT/WITH Alignment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restrict MySQL and PostgreSQL EXPLAIN eligibility to leading `SELECT`/`WITH`, and make PostgreSQL connection failures record `connection_failed: <error>` on the query.

**Architecture:** Narrow PostgreSQL `isPgExplainableStatement`; add a mirrored MySQL leading-command helper used by `CollectExplain` instead of substring `SELECT `; change the injectable PG explain `connect` to return `(*sql.DB, error)` and cache failure reasons so later queries for the same DB get the same `connection_failed: …` marker without reconnect spam.

**Tech Stack:** Go 1.x, `database/sql`, existing `metrics/mysql` and `metrics/postgresql` collectors, `go test`.

## Global Constraints

- Eligible leading commands only: `select`, `with` (after whitespace/comments).
- Non-eligible queries: skip without writing `explain` / `explain_error`.
- PG permission failures: `explain_error = "need_grant_permission"` (already via `ExecuteExplain`).
- PG other EXPLAIN errors: `explain_error = err.Error()`.
- PG connection failures: `explain_error = "connection_failed: " + underlying error`; mark DB failed for rest of collection; reuse cached reason.
- Prefer injectable PG `connect` returning `(*sql.DB, error)` over a broad `utils.ConnectionDatabase` API break.
- MySQL dump skip (`SQL_NO_CACHE` without `WHERE`) stays after eligibility.
- Do not commit unless the user asks (override plan commit steps).

## File map

| File | Role |
|------|------|
| `metrics/postgresql/explain_sql.go` | `isPgExplainableStatement` → select/with only |
| `metrics/postgresql/explain.go` | connect `(db, err)`, failed-DB reason cache, `connection_failed:` markers |
| `metrics/postgresql/dbCollectQueries_schema_test.go` | eligibility + MERGE expectations |
| `metrics/postgresql/query_optimization_contract_test.go` | connection_failed contract |
| `metrics/mysql/dbCollectQueries.go` | leading-command eligibility in `CollectExplain` |
| `metrics/mysql/explain_eligibility.go` (new) | MySQL `isMySQLExplainableStatement` + leading command helper |
| `metrics/mysql/explain_eligibility_test.go` (new) | MySQL eligibility unit tests |

---

### Task 1: PostgreSQL eligibility = SELECT / WITH only

**Files:**
- Modify: `metrics/postgresql/explain_sql.go` (`isPgExplainableStatement`)
- Modify: `metrics/postgresql/dbCollectQueries_schema_test.go` (`TestPostgresqlExplainableStatementsMatchPostgresqlCommands`, remove/replace `TestPostgresqlExplainSupportsMerge`)

**Interfaces:**
- Produces: `isPgExplainableStatement(queryText string) bool` true only for leading `select`/`with`

- [x] **Step 1: Update failing tests**

In `TestPostgresqlExplainableStatementsMatchPostgresqlCommands`, set wants:

```go
"TABLE orders":                      false,
"DELETE FROM orders WHERE id = 1":   false,
"INSERT INTO orders(id) VALUES (1)": false,
"UPDATE orders SET status = 'done'": false,
"-- trace\nUPDATE orders SET status = 'done'": false,
```

Keep SELECT/WITH/commented SELECT true; EXPLAIN/PREPARE/VACUUM/empty false.

Replace `TestPostgresqlExplainSupportsMerge` with:

```go
func TestPostgresqlExplainRejectsMerge(t *testing.T) {
	if isPgExplainableStatement("MERGE INTO target USING source ON false WHEN NOT MATCHED THEN INSERT DEFAULT VALUES") {
		t.Fatalf("MERGE must not be eligible for EXPLAIN")
	}
}
```

- [ ] **Step 2: Run tests to verify fail**

```bash
/usr/local/go/bin/go test ./metrics/postgresql/ -run 'TestPostgresqlExplainableStatementsMatchPostgresqlCommands|TestPostgresqlExplainSupportsMerge|TestPostgresqlExplainRejectsMerge' -count=1
```

Expected: FAIL on TABLE/INSERT/UPDATE/DELETE/MERGE still true or MERGE test name mismatch.

- [ ] **Step 3: Minimal implementation**

```go
func isPgExplainableStatement(queryText string) bool {
	switch pgLeadingCommand(queryText) {
	case "select", "with":
		return true
	default:
		return false
	}
}
```

- [ ] **Step 4: Run tests to verify pass**

Same command as Step 2. Expected: PASS.

---

### Task 2: MySQL eligibility = leading SELECT / WITH

**Files:**
- Create: `metrics/mysql/explain_eligibility.go`
- Create: `metrics/mysql/explain_eligibility_test.go`
- Modify: `metrics/mysql/dbCollectQueries.go` (`CollectExplain` eligibility gate ~542–555)

**Interfaces:**
- Produces: `isMySQLExplainableStatement(queryText string) bool`
- Consumes: same leading-command semantics as PG (whitespace + `--` / `/* */` comments)

- [ ] **Step 1: Write failing tests**

```go
package mysql

import "testing"

func TestMySQLExplainableStatementsSelectAndWithOnly(t *testing.T) {
	for query, want := range map[string]bool{
		"SELECT * FROM orders": true,
		"  with recent AS (SELECT 1) SELECT * FROM recent": true,
		"/* app=checkout */ SELECT * FROM orders": true,
		"-- trace\nSELECT 1": true,
		"INSERT INTO t(id) VALUES (1)": false,
		"UPDATE t SET x=1": false,
		"DELETE FROM t": false,
		"REPLACE INTO t VALUES (1)": false,
		"INSERT INTO t SELECT * FROM s": false, // leading INSERT, not SELECT
		"EXPLAIN SELECT * FROM t": false,
		"": false,
	} {
		if got := isMySQLExplainableStatement(query); got != want {
			t.Fatalf("isMySQLExplainableStatement(%q) = %v, want %v", query, got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify fail**

```bash
/usr/local/go/bin/go test ./metrics/mysql/ -run TestMySQLExplainableStatementsSelectAndWithOnly -count=1
```

Expected: FAIL (undefined function).

- [ ] **Step 3: Implement helper** (mirror PG lexer lightly in `explain_eligibility.go`)

```go
func isMySQLExplainableStatement(queryText string) bool {
	switch mysqlLeadingCommand(queryText) {
	case "select", "with":
		return true
	default:
		return false
	}
}
```

Implement `mysqlLeadingCommand` / whitespace+comment skip analogous to `pgLeadingCommand` / `skipPgWhitespaceAndComments`.

- [ ] **Step 4: Wire `CollectExplain`**

Replace the substring SELECT gate with:

```go
if digests[k]["schema_name"].(string) == "mysql" || digests[k]["schema_name"].(string) == "information_schema" ||
	digests[k]["schema_name"].(string) == "performance_schema" || digests[k]["schema_name"].(string) == "NULL" ||
	!isMySQLExplainableStatement(digests[k]["query_text"].(string)) ||
	digests[k]["explain"] != nil {
	continue
}
```

Keep the earlier `SQL_NO_CACHE` dump skip block unchanged (still uses Contains for dump detection).

- [ ] **Step 5: Run MySQL package tests**

```bash
/usr/local/go/bin/go test ./metrics/mysql/ -count=1
```

Expected: PASS.

---

### Task 3: PostgreSQL `connection_failed: <error>`

**Files:**
- Modify: `metrics/postgresql/explain.go`
- Modify: `metrics/postgresql/query_optimization_contract_test.go`
- Modify: `metrics/postgresql/dbCollectQueries_schema_test.go` (any `state.connect` stubs)
- Modify: other PG tests that assign `state.connect`

**Interfaces:**
- Change `pgExplainCollectionState.connect` to:
  `func(*config.Config, logging.Logger, string) (*sql.DB, error)`
- Change `failedDatabases` to `map[string]string` (database → error text)
- Default connect wraps `u.ConnectionDatabase`: if `db == nil`, return `(nil, fmt.Errorf("connection failed"))` unless a richer helper is added; prefer a local wrapper that calls `sql.Open`+`Ping` only if needed—minimal approach: injectable tests supply real errors; production wrapper:

```go
connect: func(configuration *config.Config, logger logging.Logger, database string) (*sql.DB, error) {
	db := u.ConnectionDatabase(configuration, logger, database)
	if db == nil {
		return nil, fmt.Errorf("connection failed")
	}
	return db, nil
},
```

Better: add `ConnectionPostgreSQLErr` in utils that returns the Ping/Open error, and use it from the PG explain default connect when `dbType == postgresql`. Spec allows scoped helper—add:

```go
// utils
func ConnectionDatabaseErr(...) (*sql.DB, error)
```

that mirrors `ConnectionPostgreSQL` but returns err instead of nil-only. Keep `ConnectionDatabase` as thin wrapper for other callers.

- [ ] **Step 1: Update contract test to require prefixed error**

```go
state.connect = func(_ *config.Config, _ logging.Logger, database string) (*sql.DB, error) {
	connects[database]++
	if database == "blocked" {
		return nil, errors.New("dial tcp: connection refused")
	}
	return db, nil
}
// ...
wantPrefix := "connection_failed: "
if detail := details[...]; !strings.HasPrefix(detail.ExplainError, wantPrefix) ||
	!strings.Contains(detail.ExplainError, "connection refused") {
	t.Fatalf(...)
}
```

Also assert query `"2"` reuses failure without second connect (`connects["blocked"] == 1`) and both have the same `explain_error` string.

- [ ] **Step 2: Run test to verify fail**

```bash
/usr/local/go/bin/go test ./metrics/postgresql/ -run 'ConnectionFailure|connection_failed' -count=1
```

Expected: FAIL on signature and/or exact `"connection_failed"` match.

- [ ] **Step 3: Implement state + collect path**

In `explain.go`:

- `failedDatabases map[string]string`
- `connect func(...) (*sql.DB, error)`
- `connection(...) (*sql.DB, error)` records err text on failure
- On failed DB / nil db:

```go
detail.ExplainError = "connection_failed: " + state.failedReason(database)
```

`failedReason` returns cached string or `"connection failed"` fallback.

Default `newPgExplainCollectionState` uses `ConnectionDatabaseErr` (new) or wrapper as above.

- [ ] **Step 4: Fix all test stubs** to new connect signature; run PG tests:

```bash
/usr/local/go/bin/go test ./metrics/postgresql/ -count=1
```

Expected: PASS (if unrelated quota `>` vs `>=` failures appear, fix only if they block this work and are clearly wrong—prefer `>=` to match MySQL `i > 100` semantics of 101st break = 100 successes; investigate before changing).

---

### Task 4: Cross-package verification

- [ ] **Step 1: Run focused packages**

```bash
/usr/local/go/bin/go test ./metrics/postgresql/ ./metrics/mysql/ -count=1
```

Expected: PASS for eligibility + connection contracts.

- [ ] **Step 2: Spec checklist**

- [ ] Both engines SELECT/WITH only
- [ ] PG `need_grant_permission` unchanged path
- [ ] PG `connection_failed: <err>`
- [ ] MySQL dump skip preserved
- [ ] No installer/`search_path` changes

---

## Spec coverage self-check

| Spec requirement | Task |
|------------------|------|
| PG select/with only | 1 |
| MySQL leading select/with | 2 |
| PG need_grant_permission | already; smoke in existing tests |
| PG connection_failed: err | 3 |
| No pg_dump heuristic | N/A (skipped) |
| MySQL SQL_NO_CACHE kept | 2 (untouched block) |
