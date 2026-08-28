package mysql

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
	mysqldriver "github.com/go-sql-driver/mysql"
	logging "github.com/google/logger"
)

func TestMysqlFailedDatabaseSchemaResetsAndRecordsOnlyFinalFailures(t *testing.T) {
	metrics := &models.Metrics{}
	metrics.DB.FailedDatabaseSchema = []string{"stale"}

	resetFailedDatabaseSchema(metrics)
	recordFailedDatabaseSchema(metrics, "information_schema_indexes")
	recordFailedDatabaseSchema(metrics, "information_schema_indexes")

	if got, want := metrics.DB.FailedDatabaseSchema, []string{"information_schema_indexes"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failed schema sections = %#v, want %#v", got, want)
	}
}

func TestMysqlFailedDatabaseSchemaDoesNotRecordSuccessfulFallback(t *testing.T) {
	metrics := &models.Metrics{}
	resetFailedDatabaseSchema(metrics)

	if got := metrics.DB.FailedDatabaseSchema; len(got) != 0 {
		t.Fatalf("successful fallback must not be marked failed: %#v", got)
	}
}

func TestMysqlSchemaTableQueryCollectsAutoIncrementValue(t *testing.T) {
	if !strings.Contains(mysqlTableSchemaSelectQuery, "AUTO_INCREMENT") {
		t.Fatalf("mysql table schema query should collect AUTO_INCREMENT for exhaustion checks: %s", mysqlTableSchemaSelectQuery)
	}
}

func TestMysqlSchemaIndexQueryCollectsVisibility(t *testing.T) {
	if !strings.Contains(mysqlIndexSchemaSelectQuery, "IS_VISIBLE") {
		t.Fatalf("mysql index schema query should collect IS_VISIBLE for index identity checks: %s", mysqlIndexSchemaSelectQuery)
	}
}

func TestMysqlSchemaIndexFallbacksPreserveVisibilitySemantics(t *testing.T) {
	queries := mysqlIndexSchemaSelectQueries()
	if len(queries) != 4 {
		t.Fatalf("expected modern MySQL, visibility-only, MariaDB, and legacy index queries, got %d", len(queries))
	}
	if !strings.Contains(queries[0], "EXPRESSION") || !strings.Contains(queries[0], "IS_VISIBLE") {
		t.Fatalf("modern MySQL query should collect expressions and visibility: %s", queries[0])
	}
	if strings.Contains(queries[1], "IFNULL(EXPRESSION") || !strings.Contains(queries[1], "IS_VISIBLE") {
		t.Fatalf("visibility-only MySQL query should avoid the EXPRESSION column: %s", queries[1])
	}
	if !strings.Contains(queries[2], "IGNORED") || !strings.Contains(queries[2], "THEN 'NO'") {
		t.Fatalf("MariaDB query should map IGNORED indexes to invisible indexes: %s", queries[2])
	}
	if !strings.Contains(queries[3], "'NULL' AS EXPRESSION") || !strings.Contains(queries[3], "'YES' AS IS_VISIBLE") {
		t.Fatalf("legacy query should provide stable defaults for the common scan shape: %s", queries[3])
	}
}

func TestMysqlUnknownColumnFallbackUsesDriverErrorCode(t *testing.T) {
	localized := &mysqldriver.MySQLError{Number: 1054, Message: "colonne inconnue"}
	if !isMysqlUnknownColumnError(localized) {
		t.Fatalf("MySQL error 1054 should trigger compatibility fallback regardless of message locale")
	}
	wrapped := fmt.Errorf("metadata query failed: %w", localized)
	if !isMysqlUnknownColumnError(wrapped) {
		t.Fatalf("wrapped MySQL error 1054 should trigger compatibility fallback")
	}
	if isMysqlUnknownColumnError(&mysqldriver.MySQLError{Number: 1142, Message: "command denied"}) {
		t.Fatalf("operational errors must not trigger compatibility fallback")
	}
}

func TestCollectDbSchemaMarksFailedSectionAndContinues(t *testing.T) {
	queriedReferentialConstraints := false
	setMysqlSchemaTestDB(t, func(query string) (driver.Rows, error) {
		switch {
		case query == mysqlTableSchemaSelectQuery:
			return nil, errors.New("table metadata denied")
		case strings.Contains(query, "FROM information_schema.columns"):
			return mysqlSchemaRows([]driver.Value{
				"app", "orders", "id", "1", "NULL", "NO", "bigint", "NULL",
				"20", "0", "NULL", "NULL", "bigint", "PRI", "auto_increment", "NULL",
			}), nil
		case strings.Contains(query, "FROM information_schema.REFERENTIAL_CONSTRAINTS"):
			queriedReferentialConstraints = true
		}
		return mysqlEmptySchemaRows(), nil
	})

	metrics := &models.Metrics{}
	metrics.DB.DatabaseSchema = make(map[string][]models.MetricGroupValue)
	logger := *logging.Init("mysql-schema-failure-test", false, false, io.Discard)

	if err := CollectDbSchema("app", logger, metrics); err != nil {
		t.Fatalf("ordinary schema query failures should be tolerated: %v", err)
	}
	if got, want := metrics.DB.FailedDatabaseSchema, []string{"information_schema_tables"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected failed schema sections: got %#v want %#v", got, want)
	}
	if len(metrics.DB.DatabaseSchema["information_schema_columns"]) != 1 {
		t.Fatalf("successful columns section should remain in DatabaseSchema: %#v", metrics.DB.DatabaseSchema)
	}
	if !queriedReferentialConstraints {
		t.Fatal("schema collection should attempt sections after the failed table section")
	}
}

func TestCollectDbSchemaDoesNotMarkSuccessfulCompatibilityFallback(t *testing.T) {
	columnQueries := 0
	indexQueries := 0
	setMysqlSchemaTestDB(t, func(query string) (driver.Rows, error) {
		switch {
		case strings.Contains(query, "FROM information_schema.columns"):
			columnQueries++
			if strings.Contains(query, "GENERATION_EXPRESSION") {
				return nil, &mysqldriver.MySQLError{Number: 1054, Message: "unknown column"}
			}
			return mysqlSchemaRows([]driver.Value{
				"app", "orders", "id", "1", "NULL", "NO", "bigint", "NULL",
				"20", "0", "NULL", "NULL", "bigint", "PRI", "auto_increment",
			}), nil
		case strings.Contains(query, "FROM information_schema.statistics"):
			indexQueries++
			if query != mysqlIndexSchemaSelectQueryLegacy {
				return nil, &mysqldriver.MySQLError{Number: 1054, Message: "unknown column"}
			}
			return mysqlSchemaRows([]driver.Value{
				"app", "orders", "PRIMARY", "0", "1", "id", "A", "1", "NULL",
				"NULL", "", "BTREE", "NULL", "YES",
			}), nil
		}
		return mysqlEmptySchemaRows(), nil
	})

	metrics := &models.Metrics{}
	metrics.DB.DatabaseSchema = make(map[string][]models.MetricGroupValue)
	logger := *logging.Init("mysql-schema-fallback-test", false, false, io.Discard)

	if err := CollectDbSchema("app", logger, metrics); err != nil {
		t.Fatalf("CollectDbSchema returned an error: %v", err)
	}
	if len(metrics.DB.FailedDatabaseSchema) != 0 {
		t.Fatalf("successful compatibility fallbacks must not be marked failed: %v", metrics.DB.FailedDatabaseSchema)
	}
	if columnQueries != 2 || indexQueries != len(mysqlIndexSchemaSelectQueries()) {
		t.Fatalf("unexpected fallback query counts: columns=%d indexes=%d", columnQueries, indexQueries)
	}
	if len(metrics.DB.DatabaseSchema["information_schema_columns"]) != 1 || len(metrics.DB.DatabaseSchema["information_schema_indexes"]) != 1 {
		t.Fatalf("fallback rows should be published: %#v", metrics.DB.DatabaseSchema)
	}
}

func TestCollectDbSchemaDoesNotFallbackAfterOperationalColumnError(t *testing.T) {
	columnQueries := 0
	queriedReferentialConstraints := false
	setMysqlSchemaTestDB(t, func(query string) (driver.Rows, error) {
		switch {
		case strings.Contains(query, "FROM information_schema.columns"):
			columnQueries++
			return nil, errors.New("columns metadata denied")
		case strings.Contains(query, "FROM information_schema.REFERENTIAL_CONSTRAINTS"):
			queriedReferentialConstraints = true
		}
		return mysqlEmptySchemaRows(), nil
	})

	metrics := &models.Metrics{}
	metrics.DB.DatabaseSchema = make(map[string][]models.MetricGroupValue)
	logger := *logging.Init("mysql-schema-column-operational-error-test", false, false, io.Discard)

	if err := CollectDbSchema("app", logger, metrics); err != nil {
		t.Fatalf("ordinary column query failures should be tolerated: %v", err)
	}
	if columnQueries != 1 {
		t.Fatalf("legacy columns query must not run after an operational error, queries=%d", columnQueries)
	}
	if got, want := metrics.DB.FailedDatabaseSchema, []string{"information_schema_columns"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failed schema sections = %#v, want %#v", got, want)
	}
	if !queriedReferentialConstraints {
		t.Fatal("schema collection should continue after the failed columns query")
	}
}

func TestCollectDbSchemaDoesNotFallbackAfterOperationalIndexError(t *testing.T) {
	indexQueries := 0
	setMysqlSchemaTestDB(t, func(query string) (driver.Rows, error) {
		if strings.Contains(query, "FROM information_schema.statistics") {
			indexQueries++
			if indexQueries == 1 {
				return nil, &mysqldriver.MySQLError{Number: 1142, Message: "command denied"}
			}
		}
		return mysqlEmptySchemaRows(), nil
	})

	metrics := &models.Metrics{}
	metrics.DB.DatabaseSchema = make(map[string][]models.MetricGroupValue)
	logger := *logging.Init("mysql-schema-index-operational-error-test", false, false, io.Discard)

	if err := CollectDbSchema("app", logger, metrics); err != nil {
		t.Fatalf("ordinary index query failures should be tolerated: %v", err)
	}
	if indexQueries != 1 {
		t.Fatalf("compatibility fallback should not run after an operational error, queries=%d", indexQueries)
	}
	want := []string{"information_schema_indexes"}
	if got := metrics.DB.FailedDatabaseSchema; !reflect.DeepEqual(got, want) {
		t.Fatalf("failed schema sections = %#v, want %#v", got, want)
	}
}

func TestCollectDbSchemaMarksRowsAndCloseFailures(t *testing.T) {
	tests := []struct {
		name string
		rows driver.Rows
	}{
		{name: "rows error", rows: &mysqlSchemaTestRows{columns: mysqlSchemaColumnNames(13), nextErr: errors.New("stream interrupted")}},
		{name: "close error", rows: &mysqlSchemaTestRows{columns: mysqlSchemaColumnNames(13), closeErr: errors.New("close failed")}},
		{name: "scan error", rows: mysqlSchemaRows([]driver.Value{"too", "few"})},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queriedColumns := false
			setMysqlSchemaTestDB(t, func(query string) (driver.Rows, error) {
				if query == mysqlTableSchemaSelectQuery {
					return test.rows, nil
				}
				if strings.Contains(query, "FROM information_schema.columns") {
					queriedColumns = true
				}
				return mysqlEmptySchemaRows(), nil
			})

			metrics := &models.Metrics{}
			metrics.DB.DatabaseSchema = make(map[string][]models.MetricGroupValue)
			logger := *logging.Init("mysql-schema-row-failure-test", false, false, io.Discard)

			if err := CollectDbSchema("app", logger, metrics); err == nil {
				t.Fatalf("CollectDbSchema should return the %s after attempting all sections", test.name)
			}
			if got, want := metrics.DB.FailedDatabaseSchema, []string{"information_schema_tables"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("unexpected failed schema sections: got %#v want %#v", got, want)
			}
			if !queriedColumns {
				t.Fatalf("columns section was not attempted after %s", test.name)
			}
		})
	}
}

func TestCollectDbSchemaRecordsRemainingSectionFailures(t *testing.T) {
	setMysqlSchemaTestDB(t, func(query string) (driver.Rows, error) {
		switch {
		case strings.Contains(query, "FROM information_schema.statistics"):
			return nil, &mysqldriver.MySQLError{Number: 1054, Message: "unknown column"}
		case strings.Contains(query, "FROM information_schema.REFERENTIAL_CONSTRAINTS"):
			return nil, errors.New("referential constraints denied")
		case strings.Contains(query, "FROM information_schema.KEY_COLUMN_USAGE"):
			return nil, errors.New("key column usage denied")
		case strings.Contains(query, "FROM information_schema.TABLE_CONSTRAINTS"):
			return nil, errors.New("table constraints denied")
		case strings.Contains(query, "FROM information_schema.TRIGGERS"):
			return nil, errors.New("triggers denied")
		default:
			return mysqlEmptySchemaRows(), nil
		}
	})

	metrics := &models.Metrics{}
	metrics.DB.DatabaseSchema = make(map[string][]models.MetricGroupValue)
	logger := *logging.Init("mysql-schema-remaining-failures-test", false, false, io.Discard)

	if err := CollectDbSchema("app", logger, metrics); err != nil {
		t.Fatalf("ordinary schema query failures should be tolerated: %v", err)
	}
	want := []string{
		"information_schema_indexes",
		"information_schema_referential_constraints",
		"information_schema_key_column_usage",
		"information_schema_table_constraints",
		"information_schema_triggers",
	}
	if got := metrics.DB.FailedDatabaseSchema; !reflect.DeepEqual(got, want) {
		t.Fatalf("failed schema sections = %#v, want %#v", got, want)
	}
}

func TestCollectDbSchemaDoesNotPublishPartialFailedSection(t *testing.T) {
	queriedColumns := false
	setMysqlSchemaTestDB(t, func(query string) (driver.Rows, error) {
		if query == mysqlTableSchemaSelectQuery {
			return &mysqlSchemaTestRows{
				columns: mysqlSchemaColumnNames(13),
				values: [][]driver.Value{{
					"app", "orders", "BASE TABLE", "InnoDB", "Dynamic", "1", "16",
					"0", "16384", "0", "utf8mb4", "0", "2",
				}},
				nextErr: errors.New("stream interrupted"),
			}, nil
		}
		if strings.Contains(query, "FROM information_schema.columns") {
			queriedColumns = true
		}
		return mysqlEmptySchemaRows(), nil
	})

	metrics := &models.Metrics{}
	metrics.DB.DatabaseSchema = make(map[string][]models.MetricGroupValue)
	logger := *logging.Init("mysql-schema-partial-section-test", false, false, io.Discard)

	if err := CollectDbSchema("app", logger, metrics); err == nil {
		t.Fatal("CollectDbSchema should report the row stream failure")
	}
	if rows, exists := metrics.DB.DatabaseSchema["information_schema_tables"]; exists {
		t.Fatalf("partial/failed section must not be added to DatabaseSchema: %#v", rows)
	}
	if !queriedColumns {
		t.Fatal("columns section should still be attempted after the table row stream fails")
	}
}

func TestCollectDbSchemaPreservesSuccessfulDatabasesWhenSectionFails(t *testing.T) {
	tableQueries := 0
	thirdRowsCloseCount := 0
	setMysqlSchemaTestDB(t, func(query string) (driver.Rows, error) {
		if query != mysqlTableSchemaSelectQuery {
			return mysqlEmptySchemaRows(), nil
		}
		tableQueries++
		switch tableQueries {
		case 1:
			return mysqlSchemaRows([]driver.Value{
				"first", "orders", "BASE TABLE", "InnoDB", "Dynamic", "1", "16",
				"0", "16384", "0", "utf8mb4", "0", "2",
			}), nil
		case 2:
			return nil, errors.New("table metadata denied")
		case 3:
			return &mysqlSchemaTestRows{
				columns: mysqlSchemaColumnNames(13),
				values: [][]driver.Value{{
					"third", "orders", "BASE TABLE", "InnoDB", "Dynamic", "1", "16",
					"0", "16384", "0", "utf8mb4", "0", "2",
				}},
				closeCount: &thirdRowsCloseCount,
			}, nil
		default:
			return nil, fmt.Errorf("unexpected table query %d", tableQueries)
		}
	})

	metrics := &models.Metrics{}
	metrics.DB.DatabaseSchema = make(map[string][]models.MetricGroupValue)
	logger := *logging.Init("mysql-schema-multi-database-failure-test", false, false, io.Discard)

	for _, database := range []string{"first", "second", "third"} {
		if err := CollectDbSchema(database, logger, metrics); err != nil {
			t.Fatalf("CollectDbSchema(%q) returned an error: %v", database, err)
		}
	}
	section := "information_schema_tables"
	if got, want := metrics.DB.FailedDatabaseSchema, []string{section}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failed schema sections = %#v, want %#v", got, want)
	}
	gotTables := metrics.DB.DatabaseSchema[section]
	if len(gotTables) != 2 {
		t.Fatalf("successful databases must remain in the schema snapshot: %#v", gotTables)
	}
	if got, want := []interface{}{gotTables[0]["TABLE_SCHEMA"], gotTables[1]["TABLE_SCHEMA"]}, []interface{}{"first", "third"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("collected table schemas = %#v, want %#v", got, want)
	}
	if tableQueries != 3 || thirdRowsCloseCount != 1 {
		t.Fatalf("later database must still be collected and closed: queries=%d closes=%d", tableQueries, thirdRowsCloseCount)
	}
}

func TestCollectDbSchemaPublishesSuccessfulEmptySections(t *testing.T) {
	setMysqlSchemaTestDB(t, func(string) (driver.Rows, error) {
		return mysqlEmptySchemaRows(), nil
	})

	metrics := &models.Metrics{}
	metrics.DB.DatabaseSchema = make(map[string][]models.MetricGroupValue)
	logger := *logging.Init("mysql-schema-empty-sections-test", false, false, io.Discard)

	if err := CollectDbSchema("app", logger, metrics); err != nil {
		t.Fatalf("CollectDbSchema returned an error: %v", err)
	}
	for _, section := range []string{
		"information_schema_tables",
		"information_schema_columns",
		"information_schema_indexes",
		"information_schema_referential_constraints",
		"information_schema_key_column_usage",
		"information_schema_table_constraints",
		"information_schema_triggers",
	} {
		rows, exists := metrics.DB.DatabaseSchema[section]
		if !exists || len(rows) != 0 {
			t.Fatalf("successful empty section %q must be published as empty, got exists=%v rows=%#v", section, exists, rows)
		}
	}
}

func TestGetMetricsResetsFailedDatabaseSchema(t *testing.T) {
	setMysqlSchemaTestDB(t, func(string) (driver.Rows, error) {
		return mysqlEmptySchemaRows(), nil
	})

	metrics := &models.Metrics{}
	metrics.DB.FailedDatabaseSchema = []string{"stale_section"}
	metrics.DB.Metrics.Databases = []string{"app"}
	logger := *logging.Init("mysql-schema-reset-test", false, false, io.Discard)
	gatherer := NewDBCollectQueriesOptimization(logger, &config.Config{QueryOptimization: true})

	if err := gatherer.GetMetrics(metrics); err != nil {
		t.Fatalf("GetMetrics returned an error: %v", err)
	}
	if len(metrics.DB.FailedDatabaseSchema) != 0 {
		t.Fatalf("GetMetrics should reset stale schema failures: %v", metrics.DB.FailedDatabaseSchema)
	}
}

func TestCollectIndexUsageSchemaReportsQueryFailure(t *testing.T) {
	setMysqlSchemaTestDB(t, func(string) (driver.Rows, error) {
		return nil, errors.New("index usage denied")
	})

	metrics := &models.Metrics{}
	metrics.DB.DatabaseSchema = make(map[string][]models.MetricGroupValue)
	logger := *logging.Init("mysql-index-usage-query-failure-test", false, false, io.Discard)

	if err := CollectIndexUsageSchema(logger, metrics); err != nil {
		t.Fatalf("ordinary index usage query failures should be tolerated: %v", err)
	}
	want := []string{"performance_schema_table_io_waits_summary_by_index_usage"}
	if got := metrics.DB.FailedDatabaseSchema; !reflect.DeepEqual(got, want) {
		t.Fatalf("failed schema sections = %#v, want %#v", got, want)
	}
}

func TestCollectIndexUsageSchemaDoesNotPublishPartialFailedSection(t *testing.T) {
	values := make([]driver.Value, 39)
	for i := range values {
		values[i] = "0"
	}
	setMysqlSchemaTestDB(t, func(string) (driver.Rows, error) {
		return &mysqlSchemaTestRows{
			columns: mysqlSchemaColumnNames(len(values)),
			values:  [][]driver.Value{values},
			nextErr: errors.New("stream interrupted"),
		}, nil
	})

	metrics := &models.Metrics{}
	metrics.DB.DatabaseSchema = make(map[string][]models.MetricGroupValue)
	logger := *logging.Init("mysql-index-usage-partial-test", false, false, io.Discard)

	if err := CollectIndexUsageSchema(logger, metrics); err == nil {
		t.Fatal("CollectIndexUsageSchema should return the row stream failure")
	}
	section := "performance_schema_table_io_waits_summary_by_index_usage"
	if rows, exists := metrics.DB.DatabaseSchema[section]; exists {
		t.Fatalf("partial/failed section must not be added to DatabaseSchema: %#v", rows)
	}
	if got, want := metrics.DB.FailedDatabaseSchema, []string{section}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failed schema sections = %#v, want %#v", got, want)
	}
}

func TestCollectIndexUsageSchemaPublishesSuccessfulEmptySection(t *testing.T) {
	setMysqlSchemaTestDB(t, func(string) (driver.Rows, error) {
		return mysqlEmptySchemaRows(), nil
	})

	metrics := &models.Metrics{}
	metrics.DB.DatabaseSchema = make(map[string][]models.MetricGroupValue)
	logger := *logging.Init("mysql-index-usage-empty-section-test", false, false, io.Discard)

	if err := CollectIndexUsageSchema(logger, metrics); err != nil {
		t.Fatalf("CollectIndexUsageSchema returned an error: %v", err)
	}
	section := "performance_schema_table_io_waits_summary_by_index_usage"
	rows, exists := metrics.DB.DatabaseSchema[section]
	if !exists || len(rows) != 0 {
		t.Fatalf("successful empty section must be published as empty, got exists=%v rows=%#v", exists, rows)
	}
}

type mysqlSchemaTestConnector struct {
	query func(string) (driver.Rows, error)
}

func (connector *mysqlSchemaTestConnector) Connect(context.Context) (driver.Conn, error) {
	return &mysqlSchemaTestConn{query: connector.query}, nil
}

func (connector *mysqlSchemaTestConnector) Driver() driver.Driver { return mysqlSchemaTestDriver{} }

type mysqlSchemaTestDriver struct{}

func (mysqlSchemaTestDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("mysql schema test driver requires a connector")
}

type mysqlSchemaTestConn struct {
	query func(string) (driver.Rows, error)
}

func (connection *mysqlSchemaTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported by the mysql schema test driver")
}

func (connection *mysqlSchemaTestConn) Close() error { return nil }

func (connection *mysqlSchemaTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported by the mysql schema test driver")
}

func (connection *mysqlSchemaTestConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	return connection.query(query)
}

type mysqlSchemaTestRows struct {
	columns    []string
	values     [][]driver.Value
	nextErr    error
	closeErr   error
	closeCount *int
	position   int
}

func (rows *mysqlSchemaTestRows) Columns() []string { return rows.columns }

func (rows *mysqlSchemaTestRows) Close() error {
	if rows.closeCount != nil {
		(*rows.closeCount)++
	}
	return rows.closeErr
}

func (rows *mysqlSchemaTestRows) Next(destination []driver.Value) error {
	if rows.position < len(rows.values) {
		copy(destination, rows.values[rows.position])
		rows.position++
		return nil
	}
	if rows.nextErr != nil {
		err := rows.nextErr
		rows.nextErr = nil
		return err
	}
	return io.EOF
}

func setMysqlSchemaTestDB(t *testing.T, query func(string) (driver.Rows, error)) {
	t.Helper()
	previousDB := models.DB
	database := sql.OpenDB(&mysqlSchemaTestConnector{query: query})
	models.DB = database
	t.Cleanup(func() {
		database.Close()
		models.DB = previousDB
	})
}

func mysqlEmptySchemaRows() driver.Rows {
	return &mysqlSchemaTestRows{columns: []string{"unused"}}
}

func mysqlSchemaRows(values ...[]driver.Value) driver.Rows {
	columnCount := 1
	if len(values) != 0 {
		columnCount = len(values[0])
	}
	return &mysqlSchemaTestRows{columns: mysqlSchemaColumnNames(columnCount), values: values}
}

func mysqlSchemaColumnNames(count int) []string {
	columns := make([]string, count)
	for i := range columns {
		columns[i] = fmt.Sprintf("column_%d", i)
	}
	return columns
}
