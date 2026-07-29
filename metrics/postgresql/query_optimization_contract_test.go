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
	u "github.com/Releem/mysqlconfigurer/utils"
	logging "github.com/google/logger"
)

func TestPgStatStatementsCapabilityMatrix(t *testing.T) {
	tests := []struct {
		name         string
		timingColumn string
		hasRows      bool
		wantRowsExpr string
	}{
		{name: "pg12_total_time_without_rows", timingColumn: "total_time", hasRows: false, wantRowsExpr: "0::bigint AS rows"},
		{name: "pg14_total_exec_time_with_rows", timingColumn: "total_exec_time", hasRows: true, wantRowsExpr: "sum(s.rows)::bigint AS rows"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query := pgStatStatementsQuery(PostgresCapabilitySnapshot{
				TimingColumn:             test.timingColumn,
				HasRows:                  test.hasRows,
				PgStatStatementsRelation: `"public"."pg_stat_statements"`,
			}, postgresQueryFull)

			for _, want := range []string{
				test.timingColumn,
				test.wantRowsExpr,
				fmt.Sprintf("(sum(s.%s) * 1000)::double precision AS total_exec_time_us", test.timingColumn),
				fmt.Sprintf("(COALESCE(sum(s.%s) / NULLIF(sum(s.calls), 0), 0) * 1000)::double precision AS mean_exec_time_us", test.timingColumn),
				"min(s.query) AS query",
			} {
				if !strings.Contains(query, want) {
					t.Fatalf("pg_stat_statements query missing %q: %s", want, query)
				}
			}
		})
	}
}

func TestCollectPostgresQueryDetailsSkipsNullQueryIDAndPreservesValidRows(t *testing.T) {
	driverName := "releem_pg_null_queryid_contract_test"
	sql.Register(driverName, pgNullQueryIDDriver{})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	details, err := collectPostgresQueryDetails(context.Background(), db, PostgresCapabilitySnapshot{
		TimingColumn:             "total_exec_time",
		HasRows:                  true,
		PgStatStatementsRelation: `"public"."pg_stat_statements"`,
	})
	if err != nil {
		t.Fatalf("a NULL pg_stat_statements queryid must not discard valid rows: %v", err)
	}

	want := []PostgresQueryDetail{
		{PostgresQueryStats: PostgresQueryStats{Datname: "app", QueryID: "42", Calls: 3, TotalExecTimeUS: 9000, MeanExecTimeUS: 3000, TotalExecTimeMS: 9000, MeanExecTimeMS: 3000, Rows: 12}, Query: "SELECT 42"},
		{PostgresQueryStats: PostgresQueryStats{Datname: "analytics", QueryID: "84", Calls: 2, TotalExecTimeUS: 4000, MeanExecTimeUS: 2000, TotalExecTimeMS: 4000, MeanExecTimeMS: 2000, Rows: 8}, Query: "SELECT 84"},
	}
	if !reflect.DeepEqual(details, want) {
		t.Fatalf("valid pg_stat_statements rows: got %#v want %#v", details, want)
	}
}

func TestPostgresQueryPayloadContractPreservesMicrosecondsAndLegacyShape(t *testing.T) {
	row := PostgresQueryDetail{
		PostgresQueryStats: PostgresQueryStats{
			Datname: "app", QueryID: "42", Calls: 3,
			TotalExecTimeUS: 9000, MeanExecTimeUS: 3000, Rows: 12,
		},
		Query: "SELECT 42",
	}

	legacy := postgresQueryDetailMetricsForMode([]PostgresQueryDetail{row}, false)[0]
	assertExactPostgresMetricKeys(t, legacy, "datname", "queryid", "query", "query_text", "calls", "total_exec_time_us", "mean_exec_time_us")
	if legacy["total_exec_time_us"] != float64(9000) || legacy["mean_exec_time_us"] != float64(3000) {
		t.Fatalf("legacy PostgreSQL timing must remain native microseconds: %#v", legacy)
	}

	for _, forbidden := range []string{"rows", "total_exec_time_ms", "mean_exec_time_ms", "format", "queries_format", "postgresql_format"} {
		if _, exists := legacy[forbidden]; exists {
			t.Fatalf("regular monitoring payload must remain legacy and marker-free; found %q in %#v", forbidden, legacy)
		}
	}

	optimization := postgresQueryDetailMetricsForMode([]PostgresQueryDetail{row}, true)[0]
	assertExactPostgresMetricKeys(t, optimization, "datname", "queryid", "query", "calls", "total_exec_time_us", "mean_exec_time_us", "rows")
	if optimization["rows"] != uint64(12) || optimization["total_exec_time_us"] != float64(9000) || optimization["mean_exec_time_us"] != float64(3000) {
		t.Fatalf("query-optimization payload must retain native PostgreSQL fields: %#v", optimization)
	}
}

func TestPostgresExplainQuotaMatrixCountsOnlySuccessfulUniquePlans(t *testing.T) {
	logger := *logging.Init("postgres-explain-quota-matrix-test", false, false, io.Discard)
	defer logger.Close()
	details := make(map[string]PostgresQueryDetail, 210)
	for i := 0; i < 210; i++ {
		queryID := fmt.Sprintf("%03d", i)
		query := "SELECT 1"
		switch {
		case i <= 4:
			query = "SELECT 1 /*fail*/"
		case i <= 9:
			query = "SELECT 1 /*empty*/"
		}
		detail := PostgresQueryDetail{
			PostgresQueryStats: PostgresQueryStats{
				Datname: "app", QueryID: queryID,
				TotalExecTimeUS: float64(210 - i),
				MeanExecTimeUS:  float64(i),
			},
			Query: query,
		}
		details[pgDigestKey(detail.Datname, queryID)] = detail
	}

	state := u.NewExplainCollectionState()
	defer state.Close()
	driverName := "releem_pg_explain_quota_contract_test"
	recorder := &pgExplainRecordingDriver{
		explainHandler: func(query string) (driver.Rows, error) {
			if strings.Contains(query, "/*fail*/") {
				return nil, errors.New("expected EXPLAIN failure")
			}
			plan := "{}"
			if strings.Contains(query, "/*empty*/") {
				plan = ""
			}
			return &pgExplainRows{
				columns: []string{"QUERY PLAN"},
				rows:    [][]driver.Value{{plan}},
			}, nil
		},
	}
	sql.Register(driverName, recorder)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	state.Connect = func(*config.Config, logging.Logger, string) (*sql.DB, error) { return db, nil }

	if got := collectExplainDetails(details, "total_exec_time_us", true, logger, &config.Config{}, state); got != 100 {
		t.Fatalf("total-time successful EXPLAIN quota = %d, want 100", got)
	}
	if got := collectExplainDetails(details, "mean_exec_time_us", true, logger, &config.Config{}, state); got != 100 {
		t.Fatalf("mean-time successful EXPLAIN quota = %d, want 100", got)
	}

	successes := 0
	for _, detail := range details {
		if detail.Explain != "" {
			successes++
		}
	}
	if successes != 200 {
		t.Fatalf("two rankings must produce 200 unique successful plans; got %d", successes)
	}
	newCollection := u.NewExplainCollectionState()
	if !newCollection.TryBeginAttempt(pgDigestKey("app", "010")) {
		t.Fatal("a new collection must reset EXPLAIN attempt state")
	}
}

func TestPostgresExplainCachesConnectionFailurePerDatabaseAndContinues(t *testing.T) {
	logger := *logging.Init("postgres-explain-connection-failure-matrix-test", false, false, io.Discard)
	defer logger.Close()
	details := map[string]PostgresQueryDetail{
		pgDigestKey("blocked", "1"): {PostgresQueryStats: PostgresQueryStats{Datname: "blocked", QueryID: "1", TotalExecTimeUS: 3}, Query: "SELECT 1"},
		pgDigestKey("blocked", "2"): {PostgresQueryStats: PostgresQueryStats{Datname: "blocked", QueryID: "2", TotalExecTimeUS: 2}, Query: "SELECT 2"},
		pgDigestKey("app", "3"):     {PostgresQueryStats: PostgresQueryStats{Datname: "app", QueryID: "3", TotalExecTimeUS: 1}, Query: "SELECT 3"},
	}

	state := u.NewExplainCollectionState()
	defer state.Close()
	driverName := "releem_pg_explain_connection_failure_contract_test"
	sql.Register(driverName, &pgExplainRecordingDriver{
		explainHandler: func(string) (driver.Rows, error) {
			return &pgExplainRows{
				columns: []string{"QUERY PLAN"},
				rows:    [][]driver.Value{{"{}"}},
			}, nil
		},
	})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	connects := map[string]int{}
	state.Connect = func(_ *config.Config, _ logging.Logger, database string) (*sql.DB, error) {
		connects[database]++
		if database == "blocked" {
			return nil, errors.New("dial tcp: connection refused")
		}
		return db, nil
	}

	if got := collectExplainDetails(details, "total_exec_time_us", true, logger, &config.Config{}, state); got != 1 {
		t.Fatalf("successful plans from reachable databases = %d, want 1", got)
	}
	if connects["blocked"] != 1 || connects["app"] != 1 {
		t.Fatalf("connection attempts = %#v, want one attempt per database", connects)
	}
	wantExplainError := "connection_failed: dial tcp: connection refused"
	for _, queryID := range []string{"1", "2"} {
		if detail := details[pgDigestKey("blocked", queryID)]; detail.ExplainError != wantExplainError {
			t.Fatalf("blocked query %s must retain connection failure metadata: %#v, want %q", queryID, detail, wantExplainError)
		}
	}
}

func assertExactPostgresMetricKeys(t *testing.T, metric map[string]interface{}, expected ...string) {
	t.Helper()
	got := make(map[string]struct{}, len(metric))
	for key := range metric {
		got[key] = struct{}{}
	}
	for _, key := range expected {
		if _, exists := got[key]; !exists {
			t.Fatalf("missing metric key %q in %#v", key, metric)
		}
		delete(got, key)
	}
	if len(got) != 0 {
		t.Fatalf("unexpected metric keys in %#v", metric)
	}
}

type pgNullQueryIDDriver struct{}

func (pgNullQueryIDDriver) Open(string) (driver.Conn, error) {
	return pgNullQueryIDConn{}, nil
}

type pgNullQueryIDConn struct{}

func (pgNullQueryIDConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepared statement")
}

func (pgNullQueryIDConn) Close() error { return nil }

func (pgNullQueryIDConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}

func (pgNullQueryIDConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, "WHERE s.queryid IS NOT NULL") {
		return &pgNullQueryIDRows{rows: [][]driver.Value{
			{"app", nil, "SELECT discarded", int64(1), float64(1000), float64(1000), int64(1)},
			{"app", "42", "SELECT 42", int64(3), float64(9000), float64(3000), int64(12)},
			{"analytics", "84", "SELECT 84", int64(2), float64(4000), float64(2000), int64(8)},
		}}, nil
	}
	return &pgNullQueryIDRows{rows: [][]driver.Value{
		{"app", "42", "SELECT 42", int64(3), float64(9000), float64(3000), int64(12)},
		{"analytics", "84", "SELECT 84", int64(2), float64(4000), float64(2000), int64(8)},
	}}, nil
}

type pgNullQueryIDRows struct {
	rows  [][]driver.Value
	index int
}

func (r *pgNullQueryIDRows) Columns() []string {
	return []string{"datname", "queryid", "query", "calls", "total_exec_time_us", "mean_exec_time_us", "rows"}
}

func (r *pgNullQueryIDRows) Close() error { return nil }

func (r *pgNullQueryIDRows) Next(dest []driver.Value) error {
	if r.index >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.index])
	r.index++
	return nil
}
