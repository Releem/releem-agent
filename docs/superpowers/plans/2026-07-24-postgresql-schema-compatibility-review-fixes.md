# PostgreSQL Schema Compatibility Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the four actionable review findings without changing the existing task payload or PostgreSQL installation contracts.

**Architecture:** Keep the legacy custom-query task on the error-returning database connection API and isolate connection ownership in a small helper. Generate PostgreSQL catalog SQL from `server_version_num`, extend the existing SQL-aware placeholder normalizer for `INTERVAL`, and omit absent schema failure metadata through the model's JSON contract.

**Tech Stack:** Go 1.x, `database/sql`, PostgreSQL catalogs, standard-library JSON, Go tests.

## Global Constraints

- Preserve existing MySQL and PostgreSQL task exit codes and user-facing errors.
- Support the installer-advertised PostgreSQL 9.5+ range with version-specific collector SQL.
- Do not edit built binaries, generated packages, secrets, or local configuration.
- Follow test-driven development: every production change must be preceded by a failing regression test.
- Do not create a Git commit unless the user asks for one.

---

### Task 1: Guard the legacy custom-query connection path

**Files:**
- Create: `tasks/task_custom_QO_test.go`
- Modify: `tasks/task_custom_QO.go`

**Interfaces:**
- Consumes: `utils.ConnectionDatabaseErr(*config.Config, logging.Logger, string) (*sql.DB, error)`.
- Produces: `executeQueryExplain(queryExplainDatabaseConnector, *config.Config, logging.Logger, string, string) (string, error)`.

- [ ] **Step 1: Write the failing connection-error regression test**

```go
func TestExecuteQueryExplainReturnsConnectionErrorWithoutNilHandlePanic(t *testing.T) {
	wantErr := errors.New("connection failed")
	logger := *logging.Init("custom-query-explain-test", false, false, io.Discard)
	defer logger.Close()

	explain, err := executeQueryExplain(
		func(*config.Config, logging.Logger, string) (*sql.DB, error) {
			return nil, wantErr
		},
		&config.Config{},
		logger,
		"app",
		"SELECT 1",
	)

	if explain != "" {
		t.Fatalf("explain = %q, want empty", explain)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run: `/usr/local/go/bin/go test ./tasks -run TestExecuteQueryExplainReturnsConnectionErrorWithoutNilHandlePanic -count=1`

Expected: build failure because `executeQueryExplain` does not exist.

- [ ] **Step 3: Add the error-returning helper and use it from the task**

```go
type queryExplainDatabaseConnector func(*config.Config, logging.Logger, string) (*sql.DB, error)

func executeQueryExplain(
	connect queryExplainDatabaseConnector,
	configuration *config.Config,
	logger logging.Logger,
	schemaName string,
	queryText string,
) (string, error) {
	db, err := connect(configuration, logger, schemaName)
	if err != nil {
		return "", err
	}
	if db == nil {
		return "", fmt.Errorf("connect to database %q: nil database handle", schemaName)
	}
	defer db.Close()
	return mysql.ExecuteExplain(db, queryText, logger)
}
```

Replace the legacy `ConnectionDatabase`/`db.Close` sequence with:

```go
explainResult, explainError := executeQueryExplain(
	utils.ConnectionDatabaseErr,
	configuration,
	logger,
	input.SchemaName,
	input.QueryText,
)
```

Keep the existing `need_grant_permission` and exit-code 7 branches unchanged.

- [ ] **Step 4: Run the focused test and verify GREEN**

Run: `/usr/local/go/bin/go test ./tasks -run TestExecuteQueryExplainReturnsConnectionErrorWithoutNilHandlePanic -count=1`

Expected: PASS.

### Task 2: Generate PostgreSQL collector SQL by server version

**Files:**
- Modify: `metrics/postgresql/collector_refactor_test.go`
- Modify: `metrics/postgresql/schema_collector.go`

**Interfaces:**
- Consumes: integer PostgreSQL `server_version_num`.
- Produces: `postgresColumnsQuery(int) string`; `postgresStructuredIndexQuery(int) string` remains unchanged at its call sites.

- [ ] **Step 1: Write failing version-matrix tests**

```go
func TestPostgresColumnsQueryMatchesServerVersion(t *testing.T) {
	tests := []struct {
		version int
		want    []string
		omit    []string
	}{
		{version: 90500, want: []string{"false AS is_identity", "false AS is_generated", "'' AS generation_expression"}, omit: []string{"is_identity = 'YES'", "is_generated <> 'NEVER'"}},
		{version: 100000, want: []string{"is_identity = 'YES'", "false AS is_generated", "'' AS generation_expression"}, omit: []string{"is_generated <> 'NEVER'"}},
		{version: 120000, want: []string{"is_identity = 'YES'", "is_generated <> 'NEVER'", "COALESCE(generation_expression, '')"}},
	}
	for _, test := range tests {
		query := postgresColumnsQuery(test.version)
		for _, fragment := range test.want {
			if !strings.Contains(query, fragment) {
				t.Errorf("PG %d columns query must contain %q: %s", test.version, fragment, query)
			}
		}
		for _, fragment := range test.omit {
			if strings.Contains(query, fragment) {
				t.Errorf("PG %d columns query must omit %q: %s", test.version, fragment, query)
			}
		}
	}
}

func TestStructuredIndexQueryUsesVersionSpecificKeyCount(t *testing.T) {
	if query := postgresStructuredIndexQuery(100000); strings.Contains(query, "indnkeyatts") || !strings.Contains(query, "idx.indnatts") {
		t.Fatalf("PG10 query must use indnatts: %s", query)
	}
	if query := postgresStructuredIndexQuery(110000); !strings.Contains(query, "idx.indnkeyatts") {
		t.Fatalf("PG11 query must use indnkeyatts: %s", query)
	}
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `/usr/local/go/bin/go test ./metrics/postgresql -run 'TestPostgresColumnsQueryMatchesServerVersion|TestStructuredIndexQueryUsesVersionSpecificKeyCount' -count=1`

Expected: build failure for the missing column query helper and/or PG10 assertion failure because the index query references `indnkeyatts`.

- [ ] **Step 3: Add the version-specific column projection**

```go
func postgresColumnsQuery(serverVersionNum int) string {
	isIdentity := "false AS is_identity"
	if serverVersionNum >= 100000 {
		isIdentity = "is_identity = 'YES'"
	}
	isGenerated := "false AS is_generated"
	generationExpression := "'' AS generation_expression"
	if serverVersionNum >= 120000 {
		isGenerated = "is_generated <> 'NEVER'"
		generationExpression = "COALESCE(generation_expression, '')"
	}
	return fmt.Sprintf(`
SELECT table_schema, table_name, column_name, ordinal_position,
	COALESCE(column_default, ''), is_nullable = 'YES', data_type,
	character_maximum_length, numeric_precision::bigint, numeric_scale::bigint,
	%s, %s, %s
FROM information_schema.columns
WHERE table_catalog = $1 AND %s
ORDER BY table_schema, table_name, ordinal_position`,
		isIdentity,
		isGenerated,
		generationExpression,
		pgUserSchemaPredicate("table_schema"),
	)
}
```

Pass `serverVersionNum` through `collectPostgresColumns` and keep its scan shape unchanged.

- [ ] **Step 4: Add the version-specific index key-count expression**

```go
keyAttributeCount := "idx.indnatts"
if serverVersionNum >= 110000 {
	keyAttributeCount = "idx.indnkeyatts"
}
```

Use the expression for both the key (`<=`) and include-column (`>`) filters. On PostgreSQL 9.5–10, `indnatts` makes the include-column result empty because INCLUDE indexes do not exist.

- [ ] **Step 5: Run the focused tests and verify GREEN**

Run: `/usr/local/go/bin/go test ./metrics/postgresql -run 'TestPostgresColumnsQueryMatchesServerVersion|TestStructuredIndexQueryUsesVersionSpecificKeyCount|TestStructuredIndexQueryAggregatesKeysAndIncludeColumns' -count=1`

Expected: PASS.

### Task 3: Normalize INTERVAL placeholders before PREPARE

**Files:**
- Modify: `metrics/postgresql/dbCollectQueries_schema_test.go`
- Modify: `metrics/postgresql/explain_prepared.go`

**Interfaces:**
- Consumes: pg_stat_statements SQL containing the invalid PostgreSQL form `interval $N`.
- Produces: `$N::interval` while preserving quoted strings, identifiers, dollar-quoted strings, and comments.

- [ ] **Step 1: Write the failing INTERVAL regression test**

```go
func TestNormalizePgStatStatementsTypedParametersNormalizesInterval(t *testing.T) {
	query := "SELECT now() - interval $1"
	want := "SELECT now() - $1::interval"
	if got := normalizePgStatStatementsTypedParameters(query); got != want {
		t.Fatalf("normalizePgStatStatementsTypedParameters() = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run: `/usr/local/go/bin/go test ./metrics/postgresql -run TestNormalizePgStatStatementsTypedParametersNormalizesInterval -count=1`

Expected: FAIL because the function returns `interval $1` unchanged.

- [ ] **Step 3: Extend the existing type allow-list**

```go
if dataType != "date" && dataType != "timestamp" && dataType != "interval" {
	continue
}
```

Do not change the existing SQL-aware quote/comment scanner.

- [ ] **Step 4: Run focused normalization tests and verify GREEN**

Run: `/usr/local/go/bin/go test ./metrics/postgresql -run 'TestNormalizePgStatStatementsTypedParameters' -count=1`

Expected: PASS.

### Task 4: Omit absent schema failure metadata from JSON

**Files:**
- Create: `models/models_test.go`
- Modify: `models/models.go`

**Interfaces:**
- Consumes: zero/nil and populated `Metrics.DB.FailedDatabaseSchema`.
- Produces: JSON that omits the key when empty and retains it when populated.

- [ ] **Step 1: Write failing JSON contract tests**

```go
func TestMetricsJSONOmitsAbsentFailedDatabaseSchema(t *testing.T) {
	payload, err := json.Marshal(Metrics{})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte(`"FailedDatabaseSchema"`)) {
		t.Fatalf("absent failure metadata must be omitted: %s", payload)
	}
}

func TestMetricsJSONIncludesFailedDatabaseSchemaWhenPresent(t *testing.T) {
	var metrics Metrics
	metrics.DB.FailedDatabaseSchema = []string{"information_schema_indexes"}
	payload, err := json.Marshal(metrics)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(payload, []byte(`"FailedDatabaseSchema":["information_schema_indexes"]`)) {
		t.Fatalf("failure metadata missing from payload: %s", payload)
	}
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `/usr/local/go/bin/go test ./models -run 'TestMetricsJSON' -count=1`

Expected: the absent-metadata test fails because the key is serialized as `null`.

- [ ] **Step 3: Add the JSON tag**

```go
FailedDatabaseSchema []string `json:"FailedDatabaseSchema,omitempty"`
```

- [ ] **Step 4: Run the focused tests and verify GREEN**

Run: `/usr/local/go/bin/go test ./models -run 'TestMetricsJSON' -count=1`

Expected: PASS.

### Task 5: Verify the complete change

**Files:**
- Review all files changed by Tasks 1–4.

**Interfaces:**
- Consumes: all four completed fixes.
- Produces: formatting-clean, vet-clean, test-green branch changes.

- [ ] **Step 1: Format changed Go files**

Run: `/usr/local/go/bin/gofmt -w tasks/task_custom_QO.go tasks/task_custom_QO_test.go metrics/postgresql/schema_collector.go metrics/postgresql/collector_refactor_test.go metrics/postgresql/explain_prepared.go metrics/postgresql/dbCollectQueries_schema_test.go models/models.go models/models_test.go`

Expected: exit code 0.

- [ ] **Step 2: Run focused package tests**

Run: `/usr/local/go/bin/go test ./tasks ./models ./metrics/postgresql -count=1`

Expected: PASS.

- [ ] **Step 3: Run the full test suite**

Run: `/usr/local/go/bin/go test ./... -count=1`

Expected: PASS.

- [ ] **Step 4: Run static analysis**

Run: `/usr/local/go/bin/go vet ./...`

Expected: exit code 0.

- [ ] **Step 5: Check the final diff**

Run: `git diff --check`

Expected: no output and exit code 0. Confirm the two pre-existing untracked binaries remain untouched.
