package postgresql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	logging "github.com/google/logger"
	"github.com/lib/pq"
)

func TestGetMetricsClearsLightweightQueriesWhenFullCollectionFails(t *testing.T) {
	logger := *logging.Init("postgresql-full-query-failure-test", false, false, io.Discard)
	defer logger.Close()
	recorder := &pgExplainRecordingDriver{queryError: errors.New("full query collection failed")}
	driverName := "releem_pg_full_query_collection_failure_test"
	sql.Register(driverName, recorder)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	previousDB := models.DB
	models.DB = db
	defer func() { models.DB = previousDB }()

	gatherer := &DBCollectQueriesOptimization{
		logger:        logger,
		configuration: &config.Config{},
		capabilities: &PostgresCapabilities{detect: func(context.Context, *sql.DB) (PostgresCapabilitySnapshot, error) {
			return PostgresCapabilitySnapshot{
				PgStatStatementsRelation: `"public"."pg_stat_statements"`,
				TimingColumn:             "total_exec_time",
				HasRows:                  true,
			}, nil
		}},
	}
	metrics := &models.Metrics{}
	metrics.DB.Queries = []models.MetricGroupValue{{"queryid": "42", "calls": uint64(1)}}

	if err := gatherer.GetMetrics(metrics); err != nil {
		t.Fatal(err)
	}
	if metrics.DB.Queries != nil {
		t.Fatalf("failed full collection must not retain lightweight queries: %#v", metrics.DB.Queries)
	}
}

func TestPostgresqlExplainableStatementsMatchPostgresqlCommands(t *testing.T) {
	for query, want := range map[string]bool{
		"SELECT * FROM orders":                             true,
		"  with recent AS (SELECT 1) SELECT * FROM recent": true,
		"/* app=checkout */ SELECT * FROM orders":          true,
		"-- trace\nUPDATE orders SET status = 'done'":      true,
		"/* SELECT */ VACUUM orders":                       false,
		"TABLE orders":                                     true,
		"DELETE FROM orders WHERE id = 1":                  true,
		"INSERT INTO orders(id) VALUES (1)":                true,
		"UPDATE orders SET status = 'done'":                true,
		"EXPLAIN SELECT * FROM orders":                     false,
		"PREPARE q AS SELECT * FROM orders":                false,
		"VACUUM orders":                                    false,
		"":                                                 false,
	} {
		if got := isPgExplainableStatement(query); got != want {
			t.Fatalf("isPgExplainableStatement(%q) = %v, want %v", query, got, want)
		}
	}
}

func TestContainsUnquotedPgParameterUsesPostgresqlLexing(t *testing.T) {
	for query, want := range map[string]bool{
		"SELECT * FROM orders WHERE id = $1":        true,
		`SELECT E'it\'s' FROM orders WHERE id = $1`: true,
		"SELECT '$1'":                 false,
		"SELECT 1 /* $1 */":           false,
		"SELECT 1 -- $1\n":            false,
		"SELECT $tag$literal $1$tag$": false,
		`SELECT "$1" FROM orders`:     false,
	} {
		if got := containsUnquotedPgParameter(query); got != want {
			t.Fatalf("containsUnquotedPgParameter(%q) = %v, want %v", query, got, want)
		}
	}
}

func TestPostgresqlExplainCollectsIndependentSuccessfulQuotas(t *testing.T) {
	logger := *logging.Init("postgresql-explain-success-quota-test", false, false, io.Discard)
	defer logger.Close()

	details := make(map[string]PostgresQueryDetail, 205)
	for i := 0; i < 205; i++ {
		queryID := fmt.Sprintf("%03d", i)
		detail := PostgresQueryDetail{
			PostgresQueryStats: PostgresQueryStats{Datname: "app", QueryID: queryID},
			Query:              "SELECT 1",
		}
		if i < 105 {
			detail.TotalExecTimeUS = float64(205 - i)
		} else {
			detail.MeanExecTimeUS = float64(i)
		}
		details[pgDigestKey("app", queryID)] = detail
	}

	recorder := &pgExplainRecordingDriver{}
	sql.Register("releem_pg_explain_success_quota_test", recorder)
	db, err := sql.Open("releem_pg_explain_success_quota_test", "")
	if err != nil {
		t.Fatal(err)
	}
	state := newPgExplainCollectionState()
	defer state.close()
	state.connect = func(*config.Config, logging.Logger, string) *sql.DB { return db }
	state.executeExplain = func(_ *sql.DB, queryID, _ string, _ bool, _ logging.Logger) (string, error) {
		if queryID < "005" {
			return "", errors.New("expected explain failure")
		}
		return "{}", nil
	}

	totalSuccesses := collectExplainDetails(details, "total_exec_time_us", true, logger, &config.Config{}, state)
	meanSuccesses := collectExplainDetails(details, "mean_exec_time_us", true, logger, &config.Config{}, state)
	if totalSuccesses != 100 || meanSuccesses != 100 {
		t.Fatalf("success quotas: total=%d mean=%d", totalSuccesses, meanSuccesses)
	}

	explained := 0
	for _, detail := range details {
		if detail.Explain != "" {
			explained++
		}
	}
	if explained != 200 {
		t.Fatalf("expected 200 unique plans, got %d", explained)
	}
}

func TestPostgresqlExplainAttemptsAreDeduplicatedOnlyWithinCollection(t *testing.T) {
	first := newPgExplainCollectionState()
	if !first.tryBeginAttempt("app\x0042") {
		t.Fatalf("first EXPLAIN attempt should be allowed")
	}
	if first.tryBeginAttempt("app\x0042") {
		t.Fatalf("same query should not be attempted twice within one collection")
	}
	second := newPgExplainCollectionState()
	if !second.tryBeginAttempt("app\x0042") {
		t.Fatalf("a new payload collection must retry the query")
	}
}

func TestPostgresqlExplainCollectionStateKeepsOnlyCurrentDatabaseConnection(t *testing.T) {
	recorder := &pgExplainRecordingDriver{}
	sql.Register("releem_pg_explain_connection_state_test", recorder)
	dbA, err := sql.Open("releem_pg_explain_connection_state_test", "a")
	if err != nil {
		t.Fatal(err)
	}
	dbB, err := sql.Open("releem_pg_explain_connection_state_test", "b")
	if err != nil {
		t.Fatal(err)
	}
	if err := dbA.Ping(); err != nil {
		t.Fatal(err)
	}
	if err := dbB.Ping(); err != nil {
		t.Fatal(err)
	}

	state := newPgExplainCollectionState()
	opens := 0
	dbs := map[string]*sql.DB{"db_a": dbA, "db_b": dbB}
	connect := func() *sql.DB {
		opens++
		return dbs[map[int]string{1: "db_a", 2: "db_b"}[opens]]
	}

	state.connection("db_a", connect)
	state.connection("db_b", connect)
	if opens != 2 {
		t.Fatalf("database switch should open the second handle, got %d opens", opens)
	}
	if err := dbA.Ping(); err == nil {
		t.Fatalf("previous database handle must be closed when switching databases")
	}
	state.close()
	if err := dbB.Ping(); err == nil {
		t.Fatalf("current database handle must be closed with collection state")
	}
}

func TestPostgresqlExplainCollectionStateCachesFailedDatabaseForCurrentCollection(t *testing.T) {
	state := newPgExplainCollectionState()
	attempts := 0

	connect := func() *sql.DB {
		attempts++
		return nil
	}

	if db := state.connection("app", connect); db != nil {
		t.Fatalf("failed connection should return nil")
	}
	if db := state.connection("app", connect); db != nil {
		t.Fatalf("repeated failed connection should return nil")
	}
	if attempts != 1 {
		t.Fatalf("failed database should be attempted once per collection, got %d attempts", attempts)
	}
}

func TestPostgresqlExplainPreservesOriginalQuotedIdentifiers(t *testing.T) {
	recorder := &pgExplainRecordingDriver{}
	sql.Register("releem_pg_explain_quotes_test", recorder)
	db, err := sql.Open("releem_pg_explain_quotes_test", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	logger := *logging.Init("postgresql-explain-quotes-test", false, false, io.Discard)
	query := `SELECT "OrderID" FROM "SalesOrders"`
	_, _ = ExecuteExplain(db, "42", query, true, logger)

	explains := recorder.queriesWithPrefix("EXPLAIN (FORMAT JSON)")
	if len(explains) != 1 {
		t.Fatalf("EXPLAIN should execute the original query once, got %#v", explains)
	}
	if explains[0] != "EXPLAIN (FORMAT JSON) "+query {
		t.Fatalf("EXPLAIN must preserve PostgreSQL quoted identifiers, got %q", explains[0])
	}
	if !recordedQueryContains(recorder.queries, `SET LOCAL search_path = "pg_catalog"`) {
		t.Fatalf("direct EXPLAIN must use a catalog-only baseline search_path: %#v", recorder.queries)
	}
}

func TestPostgresqlParameterizedExplainDoesNotFallBackToDirectExplain(t *testing.T) {
	recorder := &pgExplainRecordingDriver{failPrepare: true}
	sql.Register("releem_pg_explain_prepared_test", recorder)
	db, err := sql.Open("releem_pg_explain_prepared_test", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	logger := *logging.Init("postgresql-explain-prepared-test", false, false, io.Discard)
	_, err = ExecuteExplain(db, "42", "SELECT * FROM orders WHERE id = $1", true, logger)
	if err == nil {
		t.Fatalf("prepared-statement failure should be returned")
	}
	if explains := recorder.queriesWithPrefix("EXPLAIN (FORMAT JSON) SELECT"); len(explains) != 0 {
		t.Fatalf("parameterized query must not fall back to direct EXPLAIN: %#v", explains)
	}
}

func TestPostgresqlParameterizedExplainNormalizesPgStatStatementsTypedLiterals(t *testing.T) {
	recorder := &pgExplainRecordingDriver{}
	sql.Register("releem_pg_explain_typed_literals_test", recorder)
	db, err := sql.Open("releem_pg_explain_typed_literals_test", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	logger := *logging.Init("postgresql-explain-typed-literals-test", false, false, io.Discard)
	query := "SELECT * FROM events WHERE created_at >= timestamp $2 AND event_date = date $3"
	_, _ = ExecuteExplain(db, "42", query, true, logger)

	prepares := recorder.queriesWithPrefix("PREPARE ")
	if len(prepares) != 1 {
		t.Fatalf("expected one prepared statement, got %#v", prepares)
	}
	want := "PREPARE releem_42 AS SELECT * FROM events WHERE created_at >= $2::timestamp AND event_date = $3::date"
	if prepares[0] != want {
		t.Fatalf("prepared EXPLAIN query = %q, want %q", prepares[0], want)
	}
}

func TestNormalizePgStatStatementsTypedParametersSkipsQuotedTextAndComments(t *testing.T) {
	query := `SELECT 'date $1', "timestamp $2", $$date $3$$, /* timestamp $4 */ date $5`
	want := `SELECT 'date $1', "timestamp $2", $$date $3$$, /* timestamp $4 */ $5::date`
	if got := normalizePgStatStatementsTypedParameters(query); got != want {
		t.Fatalf("normalizePgStatStatementsTypedParameters() = %q, want %q", got, want)
	}
}

func TestPostgresqlParameterizedExplainCleansUpSessionAfterExplainError(t *testing.T) {
	recorder := &pgExplainRecordingDriver{queryError: fmt.Errorf("explain failed")}
	sql.Register("releem_pg_explain_cleanup_test", recorder)
	db, err := sql.Open("releem_pg_explain_cleanup_test", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	logger := *logging.Init("postgresql-explain-cleanup-test", false, false, io.Discard)
	_, err = ExecuteExplain(db, "42", "SELECT * FROM orders WHERE id = $1", true, logger)
	if err == nil {
		t.Fatalf("EXPLAIN error should be returned")
	}

	for _, expected := range []string{
		"SET plan_cache_mode = force_generic_plan",
		"DEALLOCATE PREPARE releem_42",
		"RESET plan_cache_mode",
	} {
		found := false
		for _, query := range recorder.queries {
			if query == expected {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("prepared EXPLAIN must execute cleanup query %q after failure: %#v", expected, recorder.queries)
		}
	}
}

func TestPostgresqlParameterizedExplainLooksUpCandidateSchemas(t *testing.T) {
	recorder := &pgExplainRecordingDriver{prepareError: fmt.Errorf(`pq: relation "orders" does not exist`)}
	sql.Register("releem_pg_explain_prepared_no_search_path_test", recorder)
	db, err := sql.Open("releem_pg_explain_prepared_no_search_path_test", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	logger := *logging.Init("postgresql-explain-prepared-no-search-path-test", false, false, io.Discard)
	_, err = ExecuteExplain(db, "42", "SELECT * FROM orders WHERE id = $1", true, logger)
	if err == nil || !strings.Contains(err.Error(), `relation "orders" does not exist`) {
		t.Fatalf("original undefined relation error should be returned, got %v", err)
	}
	if !recordedQueryContains(recorder.queries, "FROM pg_namespace") {
		t.Fatalf("undefined relations should trigger candidate schema lookup: %#v", recorder.queries)
	}
	if !recordedQueryContains(recorder.queries, `SET search_path = "pg_catalog"`) {
		t.Fatalf("prepared EXPLAIN must use a catalog-only baseline search_path: %#v", recorder.queries)
	}
}

func TestPostgresqlExplainErrorClassificationUsesSQLState(t *testing.T) {
	undefinedRelation := &pq.Error{Code: "42P01", Message: "localized undefined relation"}
	if !isUndefinedRelationError(undefinedRelation) {
		t.Fatalf("undefined relation must be detected from SQLSTATE: %v", undefinedRelation)
	}

	insufficientPrivilege := &pq.Error{Code: "42501", Message: "localized insufficient privilege"}
	if !isExplainPermissionError(insufficientPrivilege) {
		t.Fatalf("permission errors must be detected from SQLSTATE: %v", insufficientPrivilege)
	}
}

func TestPostgresqlExplainLooksUpCandidateSchemas(t *testing.T) {
	recorder := &pgExplainRecordingDriver{queryError: fmt.Errorf(`pq: relation "orders" does not exist`)}
	sql.Register("releem_pg_explain_no_search_path_test", recorder)
	db, err := sql.Open("releem_pg_explain_no_search_path_test", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	logger := *logging.Init("postgresql-explain-no-search-path-test", false, false, io.Discard)
	query := `SELECT "OrderID" FROM orders`
	_, err = ExecuteExplain(db, "42", query, true, logger)
	if err == nil || !strings.Contains(err.Error(), `relation "orders" does not exist`) {
		t.Fatalf("original undefined relation error should be returned, got %v", err)
	}
	explains := recorder.queriesWithPrefix("EXPLAIN (FORMAT JSON)")
	if len(explains) != 1 || explains[0] != "EXPLAIN (FORMAT JSON) "+query {
		t.Fatalf("EXPLAIN must execute the original statement exactly once: %#v", explains)
	}
	if !recordedQueryContains(recorder.queries, "FROM pg_namespace") {
		t.Fatalf("undefined relations should trigger candidate schema lookup: %#v", recorder.queries)
	}
}

func recordedQueryContains(queries []string, fragment string) bool {
	for _, query := range queries {
		if strings.Contains(query, fragment) {
			return true
		}
	}
	return false
}

func TestPostgresqlExplainCandidateSchemasRequireUniqueSuccess(t *testing.T) {
	attempted := []string{}
	explain, err := resolvePgExplainCandidateSchemas([]string{"app", "archive"}, func(schema string) (string, error) {
		attempted = append(attempted, schema)
		if schema == "app" {
			return `[{"Plan":{"Node Type":"Seq Scan"}}]`, nil
		}
		return "", fmt.Errorf(`pq: relation "orders" does not exist`)
	})
	if err != nil || explain == "" {
		t.Fatalf("one successful schema should return its plan, got explain=%q err=%v", explain, err)
	}
	if !reflect.DeepEqual([]string{"app", "archive"}, attempted) {
		t.Fatalf("all candidates must be checked for ambiguity, got %#v", attempted)
	}

	_, err = resolvePgExplainCandidateSchemas([]string{"app", "archive"}, func(string) (string, error) {
		return `[{"Plan":{"Node Type":"Seq Scan"}}]`, nil
	})
	if err == nil || err.Error() != pgExplainAmbiguousSchemaError {
		t.Fatalf("multiple successful schemas must fail deterministically, got %v", err)
	}
}

func TestPostgresqlExplainSupportsMerge(t *testing.T) {
	if !isPgExplainableStatement("MERGE INTO target USING source ON false WHEN NOT MATCHED THEN INSERT DEFAULT VALUES") {
		t.Fatalf("PostgreSQL 15+ MERGE statements should be eligible for EXPLAIN")
	}
}

type pgExplainRecordingDriver struct {
	queries      []string
	failPrepare  bool
	prepareError error
	queryError   error
	queryRows    driver.Rows
	closes       int
}

func (d *pgExplainRecordingDriver) Open(string) (driver.Conn, error) {
	return &pgExplainRecordingConn{driver: d}, nil
}

func (d *pgExplainRecordingDriver) queriesWithPrefix(prefix string) []string {
	var matched []string
	for _, query := range d.queries {
		if strings.HasPrefix(query, prefix) {
			matched = append(matched, query)
		}
	}
	return matched
}

type pgExplainRecordingConn struct {
	driver *pgExplainRecordingDriver
}

func (c *pgExplainRecordingConn) Prepare(string) (driver.Stmt, error) {
	return &pgExplainRecordingStmt{conn: c}, nil
}

func (c *pgExplainRecordingConn) PrepareContext(_ context.Context, query string) (driver.Stmt, error) {
	return &pgExplainRecordingStmt{conn: c, query: query}, nil
}

func (c *pgExplainRecordingConn) Close() error {
	c.driver.closes++
	return nil
}

func (c *pgExplainRecordingConn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("unexpected transaction")
}

func (c *pgExplainRecordingConn) BeginTx(_ context.Context, _ driver.TxOptions) (driver.Tx, error) {
	return pgExplainRecordingTx{}, nil
}

func (c *pgExplainRecordingConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.driver.queries = append(c.driver.queries, query)
	if c.driver.failPrepare && strings.HasPrefix(query, "PREPARE ") {
		return nil, fmt.Errorf("prepare failed")
	}
	if c.driver.prepareError != nil && strings.HasPrefix(query, "PREPARE ") {
		return nil, c.driver.prepareError
	}
	return driver.RowsAffected(0), nil
}

func (c *pgExplainRecordingConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.driver.queries = append(c.driver.queries, query)
	if c.driver.queryRows != nil {
		return c.driver.queryRows, nil
	}
	if query == "SELECT COALESCE(cardinality(parameter_types), 0) FROM pg_prepared_statements WHERE name = $1" {
		return &pgExplainRows{
			columns: []string{"coalesce"},
			rows:    [][]driver.Value{{int64(1)}},
		}, nil
	}
	if c.driver.queryError != nil {
		return nil, c.driver.queryError
	}
	return nil, fmt.Errorf("query failed")
}

type pgExplainRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (r *pgExplainRows) Columns() []string {
	return r.columns
}

func (r *pgExplainRows) Close() error {
	return nil
}

func (r *pgExplainRows) Next(dest []driver.Value) error {
	if r.index >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.index])
	r.index++
	return nil
}

type pgExplainRecordingStmt struct {
	conn  *pgExplainRecordingConn
	query string
}

func (s *pgExplainRecordingStmt) Close() error {
	return nil
}

func (s *pgExplainRecordingStmt) NumInput() int {
	return -1
}

func (s *pgExplainRecordingStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.conn.ExecContext(context.Background(), s.query, namedValuesFromValues(args))
}

func (s *pgExplainRecordingStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.conn.QueryContext(context.Background(), s.query, namedValuesFromValues(args))
}

func (s *pgExplainRecordingStmt) ExecContext(_ context.Context, args []driver.NamedValue) (driver.Result, error) {
	return s.conn.ExecContext(context.Background(), s.query, args)
}

func (s *pgExplainRecordingStmt) QueryContext(_ context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.conn.QueryContext(context.Background(), s.query, args)
}

func namedValuesFromValues(values []driver.Value) []driver.NamedValue {
	named := make([]driver.NamedValue, 0, len(values))
	for i, value := range values {
		named = append(named, driver.NamedValue{Ordinal: i + 1, Value: value})
	}
	return named
}

type pgExplainRecordingTx struct{}

func (pgExplainRecordingTx) Commit() error   { return nil }
func (pgExplainRecordingTx) Rollback() error { return nil }

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
