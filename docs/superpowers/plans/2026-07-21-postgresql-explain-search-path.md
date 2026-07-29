# PostgreSQL EXPLAIN search_path Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop runtime EXPLAIN schema guessing and configure the monitoring role `search_path` in the Linux installer, matching Datadog-style resolution.

**Architecture:** Remove `pg_catalog` baseline and candidate-schema retry from Go EXPLAIN. After existing QO grants, set `ALTER ROLE … IN DATABASE … SET search_path TO "$user", public, <user schemas>` per database. Windows has no PostgreSQL grant path today—leave it unchanged.

**Tech Stack:** Go `database/sql`, Bash `install.sh`, Bats.

## Global Constraints

- Do not force `search_path` during EXPLAIN (no `pg_catalog` baseline, no per-schema retry).
- Installer search_path always starts with `"$user", public`, then user schemas ordered by `nspname`.
- Same schema exclusions as grants: not `information_schema`, `pg_catalog`, `pg_toast%`, `pg_temp_%`.
- No `DO`, procedural blocks, `EXECUTE format`, or `set_config` in installer SQL.
- Every grant/ALTER uses `ON_ERROR_STOP=1`; failures return non-zero.
- Do not commit unless the user explicitly asks.
- Go path: `/usr/local/go/bin/go` when `go` is missing from PATH.

---

### Task 1: Runtime EXPLAIN uses connection search_path

**Files:**
- Modify: `metrics/postgresql/explain_sql.go`
- Modify: `metrics/postgresql/explain_prepared.go`
- Modify: `metrics/postgresql/dbCollectQueries_schema_test.go`

**Interfaces:**
- Consumes: `ExecuteExplain(db, queryId, queryText, supportsParameterizedExplain, logger)`
- Produces: direct/prepared EXPLAIN without any `SET`/`SET LOCAL search_path`; undefined relation returns original error

- [ ] **Step 1: Rewrite failing Go tests for no search_path override**

Replace the assertions in `TestPostgresqlExplainPreservesOriginalQuotedIdentifiers` that require `SET LOCAL search_path = "pg_catalog"` with the opposite:

```go
	if recordedQueryContains(recorder.queries, "search_path") {
		t.Fatalf("direct EXPLAIN must not override search_path: %#v", recorder.queries)
	}
```

Replace `TestPostgresqlExplainLooksUpCandidateSchemas` and `TestPostgresqlParameterizedExplainLooksUpCandidateSchemas` so undefined-relation failures return the original error and do **not** query `pg_namespace`:

```go
func TestPostgresqlExplainDoesNotRetryCandidateSchemas(t *testing.T) {
	recorder := &pgExplainRecordingDriver{queryError: fmt.Errorf(`pq: relation "orders" does not exist`)}
	sql.Register("releem_pg_explain_no_schema_retry_test", recorder)
	db, err := sql.Open("releem_pg_explain_no_schema_retry_test", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	logger := *logging.Init("postgresql-explain-no-schema-retry-test", false, false, io.Discard)
	_, err = ExecuteExplain(db, "42", `SELECT "OrderID" FROM orders`, true, logger)
	if err == nil || !strings.Contains(err.Error(), `relation "orders" does not exist`) {
		t.Fatalf("original undefined relation error should be returned, got %v", err)
	}
	if recordedQueryContains(recorder.queries, "FROM pg_namespace") {
		t.Fatalf("undefined relations must not trigger candidate schema lookup: %#v", recorder.queries)
	}
	if recordedQueryContains(recorder.queries, "search_path") {
		t.Fatalf("EXPLAIN must not override search_path: %#v", recorder.queries)
	}
}

func TestPostgresqlParameterizedExplainDoesNotRetryCandidateSchemas(t *testing.T) {
	recorder := &pgExplainRecordingDriver{prepareError: fmt.Errorf(`pq: relation "orders" does not exist`)}
	sql.Register("releem_pg_explain_prepared_no_schema_retry_test", recorder)
	db, err := sql.Open("releem_pg_explain_prepared_no_schema_retry_test", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	logger := *logging.Init("postgresql-explain-prepared-no-schema-retry-test", false, false, io.Discard)
	_, err = ExecuteExplain(db, "42", "SELECT * FROM orders WHERE id = $1", true, logger)
	if err == nil || !strings.Contains(err.Error(), `relation "orders" does not exist`) {
		t.Fatalf("original undefined relation error should be returned, got %v", err)
	}
	if recordedQueryContains(recorder.queries, "FROM pg_namespace") {
		t.Fatalf("undefined relations must not trigger candidate schema lookup: %#v", recorder.queries)
	}
	if recordedQueryContains(recorder.queries, "search_path") {
		t.Fatalf("prepared EXPLAIN must not override search_path: %#v", recorder.queries)
	}
}
```

Delete `TestPostgresqlExplainCandidateSchemasRequireUniqueSuccess` (tests removed helpers).

Update any remaining assertions that expect `SET search_path = "pg_catalog"` (prepared path) to assert absence of `search_path` instead.

- [ ] **Step 2: Run tests and verify RED**

```bash
export PATH=/usr/local/go/bin:$PATH
cd /home/dkochetov/Документы/laptop/releem/worktrees/releem-agent-pg-query-optimization-and-schema-checks
go test -count=1 ./metrics/postgresql -run 'TestPostgresqlExplain(PreservesOriginalQuotedIdentifiers|DoesNotRetryCandidateSchemas)|TestPostgresqlParameterizedExplainDoesNotRetryCandidateSchemas'
```

Expected: FAIL — still sets `search_path` / still looks up `pg_namespace`, or deleted helper tests still referenced.

- [ ] **Step 3: Implement Datadog-style EXPLAIN path**

In `explain_sql.go`, simplify `ExecuteExplain` so undefined-relation errors are returned without retry:

```go
func ExecuteExplain(db *sql.DB, queryId string, queryText string, supportsParameterizedExplain bool, logger logging.Logger) (string, error) {
	if db == nil {
		return "", fmt.Errorf("database connection is nil")
	}
	if !isPgExplainableStatement(queryText) {
		return "", fmt.Errorf("unsupported PostgreSQL statement for EXPLAIN")
	}

	if containsUnquotedPgParameter(queryText) {
		if !supportsParameterizedExplain {
			return "", fmt.Errorf("parameterized EXPLAIN requires PostgreSQL 12 or newer")
		}
		explain, err := executePreparedExplain(db, queryId, queryText)
		if err != nil {
			logger.Error("Explain prepared statement error: ", err)
			logger.Error("Explain prepared statement queryText: ", queryText)
			logger.Error("Explain prepared statement queryId: ", queryId)
			if isExplainPermissionError(err) {
				return explain, errors.New("need_grant_permission")
			}
		}
		return explain, err
	}

	var explain string
	err := executeDirectExplain(db, queryText, &explain)
	if err == nil {
		return explain, nil
	}
	logger.Error("Explain Error: ", err)
	if isExplainPermissionError(err) {
		return explain, errors.New("need_grant_permission")
	}
	return explain, err
}
```

Replace direct EXPLAIN with a transaction that only runs EXPLAIN (no search_path):

```go
func executeDirectExplain(db *sql.DB, queryText string, explain *string) error {
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return tx.QueryRowContext(ctx, "EXPLAIN (FORMAT JSON) "+queryText).Scan(explain)
}
```

Delete from `explain_sql.go`: `pgExplainAmbiguousSchemaError`, `pgExplainBaselineSchema`, `retryPgExplainForCandidateSchemas`, `fetchPgUserSchemas`, `resolvePgExplainCandidateSchemas`, `executeDirectExplainInSchema`.

In `explain_prepared.go`, flatten prepared EXPLAIN to not take/set schema:

```go
func executePreparedExplain(db *sql.DB, queryId string, queryText string) (explain string, returnErr error) {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		logger.Error("Explain prepared statement error: ", err)
		return "", err
	}
	defer conn.Close()

	stmtName := "releem_" + queryId

	if _, err = conn.ExecContext(ctx, "SET plan_cache_mode = force_generic_plan"); err != nil {
		logger.Error("Explain prepared statement error: ", err)
		return "", err
	}
	defer func() {
		if _, err := conn.ExecContext(ctx, "RESET plan_cache_mode"); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("reset plan_cache_mode: %w", err)
		}
	}()

	query := fmt.Sprintf("PREPARE %s AS %s", stmtName, normalizePgStatStatementsTypedParameters(queryText))
	if _, err = conn.ExecContext(ctx, query); err != nil {
		logger.Error("Explain prepared statement error: ", err)
		return "", err
	}
	defer func() {
		if _, err := conn.ExecContext(ctx, "DEALLOCATE PREPARE "+stmtName); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("deallocate prepared statement: %w", err)
		}
	}()

	var paramsCount int
	if err = conn.QueryRowContext(ctx, "SELECT COALESCE(cardinality(parameter_types), 0) FROM pg_prepared_statements WHERE name = $1", stmtName).Scan(&paramsCount); err != nil {
		logger.Error("Explain prepared statement error: ", err)
		return "", err
	}

	executeQuery := "EXPLAIN (FORMAT JSON) EXECUTE " + stmtName
	if paramsCount > 0 {
		nullParams := strings.TrimRight(strings.Repeat("NULL,", paramsCount), ",")
		executeQuery += "(" + nullParams + ")"
	}
	if err = conn.QueryRowContext(ctx, executeQuery).Scan(&explain); err != nil {
		logger.Error("Explain prepared statement error: ", err)
		return "", err
	}
	return explain, nil
}
```

Delete `executePreparedExplainInSchema`.

Keep `isUndefinedRelationError` if still used by tests for SQLSTATE classification; otherwise keep the SQLSTATE classification test only.

- [ ] **Step 4: Run PostgreSQL package tests**

```bash
export PATH=/usr/local/go/bin:$PATH
go test -count=1 ./metrics/postgresql
```

Expected: PASS

- [ ] **Step 5: Commit only if the user asks**

Do not commit unless explicitly requested.

---

### Task 2: Installer sets per-database search_path

**Files:**
- Modify: `install.sh` (`grant_postgresql_query_optimization_access`)
- Modify: `tests/install.bats`
- Modify: `docs/superpowers/specs/2026-07-14-postgresql-query-optimization-grants-design.md` (add search_path behavior note)

**Interfaces:**
- Consumes: existing `quote_postgresql_identifier`, `read_postgresql_root_catalog`, `postgresql_root_exec`
- Produces: after grants, for each database:
  `ALTER ROLE "<role>" IN DATABASE "<db>" SET search_path TO "$user", public, "<schema>", ...;`
  or `"$user", public` when no user schemas exist

- [ ] **Step 1: Add failing Bats coverage**

Add a focused test near the other `grant_postgresql_query_optimization_access` tests in `tests/install.bats`:

```bash
@test "postgresql query optimization sets per-database search_path for pg14" {
    run bash -c '
        RELEEM_TEST_MODE=1 source "$1"
        postgresql_root_exec() {
            printf "%s\n" "$*" >&2
            case "$*" in
                *"SHOW server_version_num"*) printf "140000\n" ;;
                *"pg_database"*) printf "appdb\000" ;;
                *"pg_namespace"*) printf "app\000analytics\000" ;;
                *"GRANT CONNECT"*|*"GRANT pg_read_all_data"*|*"ALTER ROLE"*) ;;
            esac
        }
        grant_postgresql_query_optimization_access postgres releem
    ' _ "${INSTALL_SH}"

    [ "$status" -eq 0 ]
    [[ "$output" == *'GRANT pg_read_all_data TO "releem";'* ]]
    [[ "$output" == *'ALTER ROLE "releem" IN DATABASE "appdb" SET search_path TO "$user", public, "app", "analytics";'* ]]
}

@test "postgresql query optimization sets search_path with only defaults when no user schemas" {
    run bash -c '
        RELEEM_TEST_MODE=1 source "$1"
        postgresql_root_exec() {
            printf "%s\n" "$*" >&2
            case "$*" in
                *"SHOW server_version_num"*) printf "140000\n" ;;
                *"pg_database"*) printf "postgres\000" ;;
                *"pg_namespace"*) printf "" ;;
                *"GRANT"*|*"ALTER ROLE"*) ;;
            esac
        }
        grant_postgresql_query_optimization_access postgres releem
    ' _ "${INSTALL_SH}"

    [ "$status" -eq 0 ]
    [[ "$output" == *'ALTER ROLE "releem" IN DATABASE "postgres" SET search_path TO "$user", public;'* ]]
}

@test "postgresql query optimization sets search_path for pg12 after schema grants" {
    run bash -c '
        RELEEM_TEST_MODE=1 source "$1"
        postgresql_root_exec() {
            printf "%s\n" "$*" >&2
            case "$*" in
                *"SHOW server_version_num"*) printf "120000\n" ;;
                *"pg_database"*) printf "shop\000" ;;
                *"pg_namespace"*) printf "sales\000" ;;
                *"GRANT"*|*"ALTER ROLE"*) ;;
            esac
        }
        grant_postgresql_query_optimization_access postgres releem
    ' _ "${INSTALL_SH}"

    [ "$status" -eq 0 ]
    [[ "$output" == *'GRANT USAGE ON SCHEMA "sales" TO "releem";'* ]]
    [[ "$output" == *'ALTER ROLE "releem" IN DATABASE "shop" SET search_path TO "$user", public, "sales";'* ]]
}
```

Also update `postgresql catalog grants do not require mapfile delimiter support` so the PG14 mock answers `pg_namespace` (empty or one schema) and assert an `ALTER ROLE` search_path line appears; otherwise the new enumeration will fail the mock.

- [ ] **Step 2: Run Bats and verify RED**

```bash
cd /home/dkochetov/Документы/laptop/releem/worktrees/releem-agent-pg-query-optimization-and-schema-checks
bats tests/install.bats --filter 'postgresql query optimization sets'
```

Expected: FAIL — `ALTER ROLE … SET search_path` not present.

- [ ] **Step 3: Implement search_path ALTER in install.sh**

Add a helper near `grant_postgresql_query_optimization_access`:

```bash
function postgresql_user_schema_search_path() {
    local quoted_schemas=("$@")
    local search_path='"$user", public'
    local quoted_schema
    for quoted_schema in "${quoted_schemas[@]}"; do
        [ -z "${quoted_schema}" ] && continue
        search_path="${search_path}, ${quoted_schema}"
    done
    printf '%s' "${search_path}"
}

function set_postgresql_monitoring_role_search_path() {
    local pg_superuser="$1"
    local quoted_monitoring_role="$2"
    local database="$3"
    shift 3
    local quoted_schemas=("$@")
    local quoted_database search_path
    quoted_database=$(quote_postgresql_identifier "${database}")
    search_path=$(postgresql_user_schema_search_path "${quoted_schemas[@]}")
    postgresql_root_exec "${pg_superuser}" -v ON_ERROR_STOP=1 -c "ALTER ROLE ${quoted_monitoring_role} IN DATABASE ${quoted_database} SET search_path TO ${search_path};"
}
```

Refactor `grant_postgresql_query_optimization_access` so **both** version branches enumerate schemas per database for search_path:

- PG14+: after `GRANT CONNECT` loop and `GRANT pg_read_all_data`, loop databases, read `pg_namespace` with the same filter/order as today, quote schemas, call `set_postgresql_monitoring_role_search_path`.
- PG12/13: while looping schemas for GRANT USAGE/SELECT, accumulate quoted schema names; after grants for that database (or after collecting schemas), call `set_postgresql_monitoring_role_search_path` with that database’s quoted schemas.

Keep the existing rerun warning for PG12/13; for PG14+ add the same style warning that search_path must be refreshed after new schemas, or extend the shared warning to cover both.

Exact shape of the PG14+ tail:

```bash
    if (( server_version_num >= 140000 )); then
        if ! postgresql_root_exec "${pg_superuser}" -v ON_ERROR_STOP=1 -c "GRANT pg_read_all_data TO ${quoted_monitoring_role};"; then
            return 1
        fi
    fi

    for database in "${databases[@]}"; do
        [ -z "${database}" ] && continue
        if read_postgresql_root_catalog "${pg_superuser}" -d "${database}" -At -0 -c "SELECT nspname
FROM pg_namespace
WHERE nspname NOT IN ('information_schema', 'pg_catalog')
  AND nspname NOT LIKE 'pg_toast%'
  AND nspname NOT LIKE 'pg_temp_%'
ORDER BY nspname;"; then
            schemas=("${postgresql_catalog_records[@]}")
        else
            return $?
        fi

        quoted_schemas=()
        for schema in "${schemas[@]}"; do
            [ -z "${schema}" ] && continue
            quoted_schema=$(quote_postgresql_identifier "${schema}")
            quoted_schemas+=("${quoted_schema}")
            if (( server_version_num < 140000 )); then
                if ! postgresql_root_exec "${pg_superuser}" -d "${database}" -v ON_ERROR_STOP=1 -c "GRANT USAGE ON SCHEMA ${quoted_schema} TO ${quoted_monitoring_role};"; then
                    return 1
                fi
                if ! postgresql_root_exec "${pg_superuser}" -d "${database}" -v ON_ERROR_STOP=1 -c "GRANT SELECT ON ALL TABLES IN SCHEMA ${quoted_schema} TO ${quoted_monitoring_role};"; then
                    return 1
                fi
                if ! postgresql_root_exec "${pg_superuser}" -d "${database}" -v ON_ERROR_STOP=1 -c "GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA ${quoted_schema} TO ${quoted_monitoring_role};"; then
                    return 1
                fi
            fi
        done
        if ! set_postgresql_monitoring_role_search_path "${pg_superuser}" "${quoted_monitoring_role}" "${database}" "${quoted_schemas[@]}"; then
            return 1
        fi
    done

    printf "\033[33m Rerun the installer after adding PostgreSQL schemas or objects so grants and search_path stay current.\033[0m\n"
```

Preserve the CONNECT grants loop before this. Avoid duplicating the old PG12-only schema loop—merge into the shared per-database loop above.

- [ ] **Step 4: Run Bats for PostgreSQL grant tests**

```bash
bats tests/install.bats --filter 'postgresql'
```

Expected: PASS for grant/search_path related tests.

- [ ] **Step 5: Update grants design note**

Append to `docs/superpowers/specs/2026-07-14-postgresql-query-optimization-grants-design.md`:

```markdown
## search_path

After grants, the installer sets per-database role defaults:

`ALTER ROLE "<role>" IN DATABASE "<db>" SET search_path TO "$user", public, <user schemas…>;`

This lets EXPLAIN resolve unqualified names without runtime schema retries. Rerun the installer after adding schemas.
```

- [ ] **Step 6: Commit only if the user asks**

Do not commit unless explicitly requested.

---

### Task 3: Full verification

**Files:**
- Verify only

- [ ] **Step 1: Run Go suite**

```bash
export PATH=/usr/local/go/bin:$PATH
cd /home/dkochetov/Документы/laptop/releem/worktrees/releem-agent-pg-query-optimization-and-schema-checks
go test -count=1 ./...
```

Expected: PASS

- [ ] **Step 2: Shell syntax check**

```bash
bash -n install.sh
```

Expected: exit 0

- [ ] **Step 3: Confirm no leftover candidate-schema symbols**

```bash
rg -n 'retryPgExplainForCandidateSchemas|resolvePgExplainCandidateSchemas|pgExplainBaselineSchema|executeDirectExplainInSchema|executePreparedExplainInSchema' metrics/postgresql
```

Expected: no matches

---

## Spec coverage checklist

| Spec requirement | Task |
|---|---|
| No runtime search_path override / no candidate retry | Task 1 |
| Return original undefined-relation error | Task 1 |
| Per-DB `ALTER ROLE … SET search_path` with `"$user", public, schemas` | Task 2 |
| PG14+ keeps `pg_read_all_data`, still enumerates schemas for search_path | Task 2 |
| PG12/13 keeps per-schema grants + search_path | Task 2 |
| Empty schemas → `"$user", public` only | Task 2 |
| No procedural installer SQL | Task 2 |
| Go + Bats tests | Tasks 1–2 |
| Windows unchanged (no PG grant path) | N/A (explicit) |
