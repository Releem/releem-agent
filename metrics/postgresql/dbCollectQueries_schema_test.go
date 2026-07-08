package postgresql

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Releem/mysqlconfigurer/models"
	"github.com/hashicorp/go-version"
)

func TestPostgresqlSchemaMetricsUseCollectorCompatibleKeys(t *testing.T) {
	table := pgTableSchemaMetric(pgTableSchemaMetricInput{
		TABLE_SCHEMA:        "app",
		TABLE_NAME:          "orders",
		TABLE_TYPE:          "BASE TABLE",
		ENGINE:              "HEAP",
		TABLE_ROWS:          "42",
		AVG_ROW_LENGTH:      "128",
		DATA_LENGTH:         "4096",
		INDEX_LENGTH:        "1024",
		TABLE_COLLATION:     "NULL",
		TABLE_SIZE_BYTES:    "5120",
		N_MOD_SINCE_ANALYZE: "7",
		LAST_ANALYZE:        "2026-07-01 00:00:00+00",
		LAST_AUTOANALYZE:    "NULL",
	})
	if table["TABLE_SCHEMA"] != "app" || table["TABLE_NAME"] != "orders" {
		t.Fatalf("table identity keys are not collector-compatible: %#v", table)
	}
	if table["ENGINE"] != "HEAP" || table["TABLE_ROWS"] != "42" || table["AVG_ROW_LENGTH"] != "128" {
		t.Fatalf("table statistics keys are not collector-compatible: %#v", table)
	}
	if table["DATA_LENGTH"] != "4096" || table["INDEX_LENGTH"] != "1024" || table["TABLE_COLLATION"] != "NULL" {
		t.Fatalf("table size/collation keys are not collector-compatible: %#v", table)
	}
	if table["N_MOD_SINCE_ANALYZE"] != "7" || table["LAST_ANALYZE"] != "2026-07-01 00:00:00+00" || table["LAST_AUTOANALYZE"] != "NULL" || table["TABLE_SIZE_BYTES"] != "5120" {
		t.Fatalf("postgresql table metrics should expose planner statistics keys: %#v", table)
	}

	column := pgColumnSchemaMetric("app", "orders", "customer_id", "2", "NULL", "NO", "bigint", "NULL", "64", "0", "NULL")
	if column["COLUMN_NAME"] != "customer_id" || column["ORDINAL_POSITION"] != "2" {
		t.Fatalf("column identity keys are not collector-compatible: %#v", column)
	}
	if column["CHARACTER_MAXIMUM_LENGTH"] != "NULL" || column["NUMERIC_PRECISION"] != "64" || column["NUMERIC_SCALE"] != "0" {
		t.Fatalf("column numeric metadata keys are not collector-compatible: %#v", column)
	}
	if column["CHARACTER_SET_NAME"] != "NULL" {
		t.Fatalf("postgresql columns should expose NULL character set: %#v", column)
	}

	index := pgIndexSchemaMetric(pgIndexInput("app", "orders", "idx_orders_customer", "customer_id", "NULL", "NULL"))
	if index["INDEX_NAME"] != "idx_orders_customer" || index["COLUMN_NAME"] != "customer_id" {
		t.Fatalf("index identity keys are not collector-compatible: %#v", index)
	}
	if index["NON_UNIQUE"] != "1" || index["SEQ_IN_INDEX"] != "1" || index["INDEX_TYPE"] != "btree" {
		t.Fatalf("index metadata keys are not collector-compatible: %#v", index)
	}
	if index["CARDINALITY"] != "NULL" {
		t.Fatalf("postgresql indexes should expose NULL cardinality when estimate is unavailable: %#v", index)
	}
	if index["LAST_IDX_SCAN"] != "NULL" {
		t.Fatalf("postgresql indexes should expose last_idx_scan key: %#v", index)
	}
	if index["EXPRESSION"] != "" || index["PREDICATE"] != "" {
		t.Fatalf("plain index should expose empty absent expression/predicate: %#v", index)
	}

	expressionIndex := pgIndexSchemaMetric(pgIndexInput("app", "users", "idx_users_lower_email", "NULL", "lower(email)", "NULL"))
	if expressionIndex["COLUMN_NAME"] != "lower(email)" || expressionIndex["EXPRESSION"] != "lower(email)" {
		t.Fatalf("expression index should preserve expression identity in COLUMN_NAME and EXPRESSION: %#v", expressionIndex)
	}

	partialIndex := pgIndexSchemaMetric(pgIndexInput("app", "orders", "idx_orders_customer_open", "customer_id", "NULL", "status = 'open'"))
	if partialIndex["PREDICATE"] != "status = 'open'" {
		t.Fatalf("partial index should preserve predicate identity: %#v", partialIndex)
	}
}

func TestPostgresqlSequenceMetricUsesCollectorCompatibleKeys(t *testing.T) {
	sequence := pgSequenceSchemaMetric("app", "orders_id_seq", "1700000000", "2147483647", "1")

	if sequence["SEQUENCE_SCHEMA"] != "app" || sequence["SEQUENCE_NAME"] != "orders_id_seq" {
		t.Fatalf("sequence identity keys are not collector-compatible: %#v", sequence)
	}
	if sequence["LAST_VALUE"] != "1700000000" || sequence["MAX_VALUE"] != "2147483647" || sequence["INCREMENT_BY"] != "1" {
		t.Fatalf("sequence range keys are not collector-compatible: %#v", sequence)
	}
}

func TestPostgresqlIndexMetricExposesExactPgIndexIdentity(t *testing.T) {
	index := pgIndexSchemaMetric(pgIndexInput("app", "orders", "idx_orders_customer", "customer_id", "", ""))

	for _, key := range []string{
		"PG_RELAM",
		"PG_INDKEY",
		"PG_INDCLASS",
		"PG_INDCOLLATION",
		"PG_INDOPTION",
		"PG_INDNKEYATTS",
		"PG_INDNATTS",
		"PG_INDEXPRS",
		"PG_INDPRED",
		"PG_INDISVALID",
		"PG_INDISREADY",
		"PG_INDISEXCLUSION",
		"LAST_IDX_SCAN",
	} {
		if _, ok := index[key]; !ok {
			t.Fatalf("postgresql index metric should expose exact pg_index key %s: %#v", key, index)
		}
	}
}

func TestPostgresqlIndexSchemaQueryCollectsUsageStatistics(t *testing.T) {
	if !strings.Contains(pgIndexSchemaQuery("app"), "pg_stat_user_indexes") {
		t.Fatalf("postgresql index schema query should collect pg_stat_user_indexes usage statistics")
	}
	if !strings.Contains(pgIndexSchemaQuery("app"), "idx_scan") {
		t.Fatalf("postgresql index schema query should collect idx_scan")
	}
	if !strings.Contains(pgIndexSchemaQuery("app"), "last_idx_scan") {
		t.Fatalf("postgresql index schema query should collect last_idx_scan")
	}
}

func TestPostgresqlIndexVectorExpressionUsesTextCast(t *testing.T) {
	for input, expected := range map[string]string{
		"idx.indkey":       "idx.indkey::text",
		"idx.indclass":     "idx.indclass::text",
		"idx.indcollation": "idx.indcollation::text",
		"idx.indoption":    "idx.indoption::text",
	} {
		if got := pgIndexVectorExpression(input); got != expected {
			t.Fatalf("postgresql vector expression for %s should use text cast, got %s", input, got)
		}
	}
}

func TestPostgresqlIndexAttributeCountExpressionsUseVersionSafeFallback(t *testing.T) {
	nkeyatts, natts := pgIndexAttributeCountExpressions(110000)
	if nkeyatts != "idx.indnkeyatts::text" || natts != "idx.indnatts::text" {
		t.Fatalf("postgresql 11+ should use native pg_index attribute counts, got %s and %s", nkeyatts, natts)
	}

	nkeyatts, natts = pgIndexAttributeCountExpressions(100000)
	expected := "array_length(idx.indkey::int2[], 1)::text"
	if nkeyatts != expected || natts != expected {
		t.Fatalf("postgresql before 11 should derive index attribute counts from indkey, got %s and %s", nkeyatts, natts)
	}
}

func TestPostgresqlIndexSchemaQueryAvoidsVersionSpecificColumnsOnOlderServers(t *testing.T) {
	pg10Query := pgIndexSchemaQueryForVersion(100000)
	if strings.Contains(pg10Query, "idx.indnkeyatts") || strings.Contains(pg10Query, "idx.indnatts") {
		t.Fatalf("postgresql before 11 query should not reference pg_index attribute count columns: %s", pg10Query)
	}

	pg12Query := pgIndexSchemaQueryForVersion(120000)
	if strings.Contains(pg12Query, "sui.last_idx_scan") {
		t.Fatalf("postgresql before 16 query should not reference pg_stat_user_indexes.last_idx_scan: %s", pg12Query)
	}

	pg16Query := pgIndexSchemaQueryForVersion(160000)
	if !strings.Contains(pg16Query, "sui.last_idx_scan") {
		t.Fatalf("postgresql 16+ query should collect last_idx_scan: %s", pg16Query)
	}
}

func TestPostgresqlSequencesAreCollectedOnlyWhenViewExists(t *testing.T) {
	if !pgSupportsSequencesView(100000) {
		t.Fatalf("postgresql 10+ should support pg_sequences")
	}
	if pgSupportsSequencesView(90600) {
		t.Fatalf("postgresql before 10 should not query pg_sequences")
	}
}

func pgIndexInput(schema, tableName, indexName, columnName, expression, predicate string) pgIndexSchemaMetricInput {
	return pgIndexSchemaMetricInput{
		TABLE_SCHEMA:      schema,
		TABLE_NAME:        tableName,
		INDEX_NAME:        indexName,
		NON_UNIQUE:        "1",
		SEQ_IN_INDEX:      "1",
		COLUMN_NAME:       columnName,
		COLLATION:         "NULL",
		CARDINALITY:       "NULL",
		SUB_PART:          "NULL",
		PACKED:            "NULL",
		NULLABLE:          "NO",
		INDEX_TYPE:        "btree",
		EXPRESSION:        expression,
		PREDICATE:         predicate,
		PG_RELAM:          "btree",
		PG_INDKEY:         "1",
		PG_INDCLASS:       "1978",
		PG_INDCOLLATION:   "0",
		PG_INDOPTION:      "0",
		PG_INDNKEYATTS:    "1",
		PG_INDNATTS:       "1",
		PG_INDEXPRS:       expression,
		PG_INDPRED:        predicate,
		PG_INDISVALID:     "true",
		PG_INDISREADY:     "true",
		PG_INDISEXCLUSION: "false",
		LAST_IDX_SCAN:     "NULL",
	}
}

func TestPostgresqlExplainSearchPathRetryHelpers(t *testing.T) {
	if !isUndefinedRelationError(fmt.Errorf("pq: relation \"orders\" does not exist")) {
		t.Fatalf("undefined relation errors should trigger search_path retry")
	}
	if isUndefinedRelationError(fmt.Errorf("pq: permission denied for table orders")) {
		t.Fatalf("permission errors should not trigger search_path retry")
	}

	searchPath, ok := pgSearchPathList([]string{"app", `tenant"one`})
	if !ok {
		t.Fatalf("user schemas should produce a search_path")
	}
	if searchPath != `"app","tenant""one"` {
		t.Fatalf("search_path should quote PostgreSQL identifiers safely, got %s", searchPath)
	}

	if _, ok := pgSearchPathList([]string{"public"}); ok {
		t.Fatalf("public-only schema list should not add a retry search_path")
	}

	if !shouldRetryPreparedExplainWithSearchPath(fmt.Errorf("pq: relation \"orders\" does not exist"), []string{"app"}) {
		t.Fatalf("prepared explain should retry with search_path on undefined relation")
	}
	if shouldRetryPreparedExplainWithSearchPath(fmt.Errorf("pq: there is no parameter $1"), []string{"app"}) {
		t.Fatalf("prepared explain should not retry search_path for parameter binding errors")
	}
	if shouldRetryPreparedExplainWithSearchPath(fmt.Errorf("pq: relation \"orders\" does not exist"), []string{"public"}) {
		t.Fatalf("prepared explain should not retry search_path without non-public schemas")
	}
}

func TestPostgresqlUserSchemaPredicateExcludesInternalSchemas(t *testing.T) {
	predicate := pgUserSchemaPredicate("n.nspname")
	for _, condition := range []string{
		"n.nspname NOT IN ('information_schema', 'pg_catalog')",
		"n.nspname NOT LIKE 'pg_toast%'",
		"n.nspname NOT LIKE 'pg_temp_%'",
	} {
		if !strings.Contains(predicate, condition) {
			t.Fatalf("postgresql user schema predicate should include %q: %s", condition, predicate)
		}
	}
}

func TestPostgresqlTableRelkindPredicateIncludesPartitionedTables(t *testing.T) {
	predicate := pgTableRelkindPredicate("c.relkind")
	if !strings.Contains(predicate, "'p'") {
		t.Fatalf("postgresql table relkind predicate should include partitioned tables: %s", predicate)
	}
}

func TestPostgresqlQueryMetricIncludesRowsSentForPlatformCollector(t *testing.T) {
	query := pgQueryMetric("appdb", "42", "select * from orders", 3, 9000, 3000, 12)

	if query["SUM_ROWS_SENT"] != uint64(12) {
		t.Fatalf("query metric should expose SUM_ROWS_SENT for rows_sent calculation: %#v", query)
	}
}

func TestPgStatStatementsQueriesCollectRows(t *testing.T) {
	for name, query := range map[string]string{
		"new":         PG_STAT_STATEMENTS,
		"old":         PG_STAT_STATEMENTS_OLD_VERSION,
		"newFallback": PG_STAT_STATEMENTS_NO_ROWS,
		"oldFallback": PG_STAT_STATEMENTS_OLD_VERSION_NO_ROWS,
	} {
		if !strings.Contains(query, "rows_sent") {
			t.Fatalf("%s pg_stat_statements query should expose rows_sent: %s", name, query)
		}
		if !strings.Contains(query, "NULLIF(sum(s.calls), 0)") {
			t.Fatalf("%s pg_stat_statements query should guard mean_exec_time division: %s", name, query)
		}
	}
	if !strings.Contains(PG_STAT_STATEMENTS, "sum(s.rows)") {
		t.Fatalf("pg_stat_statements query should collect rows when supported: %s", PG_STAT_STATEMENTS)
	}
	if strings.Contains(PG_STAT_STATEMENTS_NO_ROWS, "sum(s.rows)") {
		t.Fatalf("pg_stat_statements fallback query should not reference rows column: %s", PG_STAT_STATEMENTS_NO_ROWS)
	}
}

func TestPgStatStatementsQuerySelectsRowsFallback(t *testing.T) {
	query := PgStatStatementsQuery(version.Must(version.NewVersion("14.0")), false)
	if query != PG_STAT_STATEMENTS_NO_ROWS {
		t.Fatalf("expected rows fallback query for PG 14, got: %s", query)
	}

	query = PgStatStatementsQuery(version.Must(version.NewVersion("14.0")), true)
	if query != PG_STAT_STATEMENTS {
		t.Fatalf("expected rows-enabled query for PG 14, got: %s", query)
	}

	query = PgStatStatementsQuery(version.Must(version.NewVersion("12.0")), true)
	if query != PG_STAT_STATEMENTS_OLD_VERSION {
		t.Fatalf("expected old timing query for PG 12, got: %s", query)
	}
}

func TestPgStatStatementsRowsSupportIsCached(t *testing.T) {
	models.PgStatStatementsSupportsRows = false
	models.PgStatStatementsSupportsRowsDetected = false
	t.Cleanup(func() {
		models.PgStatStatementsSupportsRows = false
		models.PgStatStatementsSupportsRowsDetected = false
	})

	calls := 0
	probe := func() (bool, error) {
		calls++
		return calls == 1, nil
	}

	if !detectPgStatStatementsSupportsRows(probe, nil) {
		t.Fatalf("first rows-support probe should return the database result")
	}
	if !detectPgStatStatementsSupportsRows(probe, nil) {
		t.Fatalf("second rows-support probe should return cached result")
	}
	if calls != 1 {
		t.Fatalf("rows-support detection should query once, got %d probes", calls)
	}
}

func TestPgStatStatementsRowsSupportProbeErrorIsNotCached(t *testing.T) {
	models.PgStatStatementsSupportsRows = false
	models.PgStatStatementsSupportsRowsDetected = false
	t.Cleanup(func() {
		models.PgStatStatementsSupportsRows = false
		models.PgStatStatementsSupportsRowsDetected = false
	})

	calls := 0
	probe := func() (bool, error) {
		calls++
		if calls == 1 {
			return false, fmt.Errorf("temporary connection error")
		}
		return true, nil
	}

	if detectPgStatStatementsSupportsRows(probe, nil) {
		t.Fatalf("failed rows-support probe should return false")
	}
	if models.PgStatStatementsSupportsRowsDetected {
		t.Fatalf("failed rows-support probe should not mark detection complete")
	}
	if !detectPgStatStatementsSupportsRows(probe, nil) {
		t.Fatalf("successful retry should return the database result")
	}
	if calls != 2 {
		t.Fatalf("rows-support detection should retry after a probe error, got %d probes", calls)
	}
}

func TestPostgresqlBaseQueryMetricLatencyExcludesQueryText(t *testing.T) {
	query := pgQueryMetricLatency("appdb", "42", 3, 9000, 3000, 12)

	if _, ok := query["query"]; ok {
		t.Fatalf("base query metric should not include query text: %#v", query)
	}
	if query["SUM_ROWS_SENT"] != uint64(12) {
		t.Fatalf("base query metric should expose SUM_ROWS_SENT: %#v", query)
	}
}
