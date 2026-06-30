package postgresql

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/go-version"
)

func TestPostgresqlSchemaMetricsUseCollectorCompatibleKeys(t *testing.T) {
	table := pgTableSchemaMetric("app", "orders", "BASE TABLE", "HEAP", "42", "128", "4096", "1024", "NULL")
	if table["TABLE_SCHEMA"] != "app" || table["TABLE_NAME"] != "orders" {
		t.Fatalf("table identity keys are not collector-compatible: %#v", table)
	}
	if table["ENGINE"] != "HEAP" || table["TABLE_ROWS"] != "42" || table["AVG_ROW_LENGTH"] != "128" {
		t.Fatalf("table statistics keys are not collector-compatible: %#v", table)
	}
	if table["DATA_LENGTH"] != "4096" || table["INDEX_LENGTH"] != "1024" || table["TABLE_COLLATION"] != "NULL" {
		t.Fatalf("table size/collation keys are not collector-compatible: %#v", table)
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

	index := pgIndexSchemaMetric("app", "orders", "idx_orders_customer", "1", "1", "customer_id", "NULL", "NULL", "NULL", "NULL", "NO", "btree", "NULL", "NULL")
	if index["INDEX_NAME"] != "idx_orders_customer" || index["COLUMN_NAME"] != "customer_id" {
		t.Fatalf("index identity keys are not collector-compatible: %#v", index)
	}
	if index["NON_UNIQUE"] != "1" || index["SEQ_IN_INDEX"] != "1" || index["INDEX_TYPE"] != "btree" {
		t.Fatalf("index metadata keys are not collector-compatible: %#v", index)
	}
	if index["CARDINALITY"] != "NULL" {
		t.Fatalf("postgresql indexes should expose NULL cardinality when estimate is unavailable: %#v", index)
	}
	if index["EXPRESSION"] != "" || index["PREDICATE"] != "" {
		t.Fatalf("plain index should expose empty absent expression/predicate: %#v", index)
	}

	expressionIndex := pgIndexSchemaMetric("app", "users", "idx_users_lower_email", "1", "1", "NULL", "NULL", "NULL", "NULL", "NULL", "YES", "btree", "lower(email)", "NULL")
	if expressionIndex["COLUMN_NAME"] != "lower(email)" || expressionIndex["EXPRESSION"] != "lower(email)" {
		t.Fatalf("expression index should preserve expression identity in COLUMN_NAME and EXPRESSION: %#v", expressionIndex)
	}

	partialIndex := pgIndexSchemaMetric("app", "orders", "idx_orders_customer_open", "1", "1", "customer_id", "NULL", "NULL", "NULL", "NULL", "NO", "btree", "NULL", "status = 'open'")
	if partialIndex["PREDICATE"] != "status = 'open'" {
		t.Fatalf("partial index should preserve predicate identity: %#v", partialIndex)
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

func TestPostgresqlBaseQueryMetricLatencyExcludesQueryText(t *testing.T) {
	query := pgQueryMetricLatency("appdb", "42", 3, 9000, 3000, 12)

	if _, ok := query["query"]; ok {
		t.Fatalf("base query metric should not include query text: %#v", query)
	}
	if query["SUM_ROWS_SENT"] != uint64(12) {
		t.Fatalf("base query metric should expose SUM_ROWS_SENT: %#v", query)
	}
}
