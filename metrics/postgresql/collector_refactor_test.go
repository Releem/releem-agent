package postgresql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	u "github.com/Releem/mysqlconfigurer/utils"
	logging "github.com/google/logger"
)

func TestPostgresCapabilitiesCachesOnlySuccessfulDetection(t *testing.T) {
	calls := 0
	capabilities := &PostgresCapabilities{
		detect: func(context.Context, *sql.DB) (PostgresCapabilitySnapshot, error) {
			calls++
			if calls == 1 {
				return PostgresCapabilitySnapshot{}, errors.New("temporary probe failure")
			}
			return PostgresCapabilitySnapshot{
				ServerVersionNum:         140000,
				PgStatStatementsRelation: `"monitoring"."pg_stat_statements"`,
				PgStatStatementsColumns:  map[string]struct{}{"queryid": {}, "rows": {}, "total_exec_time": {}},
				TimingColumn:             "total_exec_time",
				HasRows:                  true,
				SupportsPlanCacheMode:    true,
			}, nil
		},
	}

	if _, err := capabilities.Resolve(context.Background(), nil); err == nil {
		t.Fatal("first transient capability error should be returned")
	}
	first, err := capabilities.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := capabilities.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("only successful detection should be cached, got %d calls", calls)
	}
	if !reflect.DeepEqual(first, second) || !first.HasRows || first.TimingColumn != "total_exec_time" {
		t.Fatalf("unexpected cached capabilities: %#v %#v", first, second)
	}
}

func TestPostgresCapabilitiesInvalidationForcesDetection(t *testing.T) {
	calls := 0
	capabilities := &PostgresCapabilities{
		detect: func(context.Context, *sql.DB) (PostgresCapabilitySnapshot, error) {
			calls++
			return PostgresCapabilitySnapshot{
				ServerVersionNum:         120000,
				PgStatStatementsRelation: `"public"."pg_stat_statements"`,
				PgStatStatementsColumns:  map[string]struct{}{"total_time": {}},
				TimingColumn:             "total_time",
				SupportsPlanCacheMode:    true,
			}, nil
		},
	}

	if _, err := capabilities.Resolve(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	capabilities.Invalidate()
	if _, err := capabilities.Resolve(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("invalidation should force detection, got %d calls", calls)
	}
}

func TestPostgresCapabilitiesCachesOnlySuccessfulServerVersion(t *testing.T) {
	calls := 0
	capabilities := &PostgresCapabilities{
		detectServerVersion: func(context.Context, *sql.DB) (int, error) {
			calls++
			if calls == 1 {
				return 0, errors.New("temporary version probe failure")
			}
			return 160000, nil
		},
	}
	if _, err := capabilities.ServerVersion(context.Background(), nil); err == nil {
		t.Fatal("failed server-version detection must not be cached")
	}
	for i := 0; i < 2; i++ {
		version, err := capabilities.ServerVersion(context.Background(), nil)
		if err != nil || version != 160000 {
			t.Fatalf("unexpected server version result: version=%d err=%v", version, err)
		}
	}
	if calls != 2 {
		t.Fatalf("successful server-version detection should be cached, got %d probes", calls)
	}
}

func TestPostgresCapabilitiesRequireCorePgStatStatementsColumns(t *testing.T) {
	columns := map[string]struct{}{
		"dbid": {}, "queryid": {}, "query": {}, "total_exec_time": {},
	}
	if err := validatePgStatStatementsColumns(columns); err == nil || !strings.Contains(err.Error(), "calls") {
		t.Fatalf("missing core PGSS column should fail detection, got %v", err)
	}
	columns["calls"] = struct{}{}
	if err := validatePgStatStatementsColumns(columns); err != nil {
		t.Fatalf("rows must remain optional: %v", err)
	}
}

func TestPostgresQueryProfilesUseNativeFields(t *testing.T) {
	stats := PostgresQueryStats{
		Datname:         "app",
		QueryID:         "42",
		Calls:           3,
		TotalExecTimeUS: 9000,
		MeanExecTimeUS:  3000,
		Rows:            12,
	}
	lightweight := stats.metricGroupValue()
	for key, expected := range map[string]interface{}{
		"datname":            "app",
		"queryid":            "42",
		"calls":              uint64(3),
		"total_exec_time_us": float64(9000),
		"mean_exec_time_us":  float64(3000),
		"rows":               uint64(12),
	} {
		if lightweight[key] != expected {
			t.Fatalf("lightweight field %s: expected %#v, got %#v", key, expected, lightweight[key])
		}
	}
	for _, forbidden := range []string{"query", "query_text", "SUM_ROWS_SENT", "total_exec_time_ms", "mean_exec_time_ms"} {
		if _, ok := lightweight[forbidden]; ok {
			t.Fatalf("lightweight PostgreSQL payload must not include %s: %#v", forbidden, lightweight)
		}
	}

	detail := PostgresQueryDetail{PostgresQueryStats: stats, Query: "select 1", Explain: "{}", ExplainError: ""}
	full := detail.metricGroupValue()
	if full["query"] != "select 1" || full["explain"] != "{}" {
		t.Fatalf("full profile should include query and explain: %#v", full)
	}
	if _, ok := full["explain_error"]; ok {
		t.Fatalf("empty optional explain_error should be omitted: %#v", full)
	}
}

func TestPostgresQueryPayloadModes(t *testing.T) {
	row := PostgresQueryDetail{
		PostgresQueryStats: PostgresQueryStats{
			Datname: "app", QueryID: "42", Calls: 3,
			TotalExecTimeUS: 9000, MeanExecTimeUS: 3000, Rows: 12,
		},
		Query: "select 1",
	}
	legacy := postgresQueryDetailMetricsForMode([]PostgresQueryDetail{row}, false)[0]
	legacyKeys := map[string]struct{}{
		"datname": {}, "queryid": {}, "calls": {}, "query": {}, "query_text": {},
		"total_exec_time_us": {}, "mean_exec_time_us": {},
	}
	if len(legacy) != len(legacyKeys) {
		t.Fatalf("legacy payload keys: got %#v, want %#v", legacy, legacyKeys)
	}
	for key := range legacy {
		if _, ok := legacyKeys[key]; !ok {
			t.Fatalf("legacy payload contains unexpected key %s: %#v", key, legacy)
		}
	}
	for key, want := range map[string]interface{}{
		"query": "select 1", "query_text": "select 1",
		"total_exec_time_us": float64(9000), "mean_exec_time_us": float64(3000),
	} {
		if legacy[key] != want {
			t.Fatalf("legacy %s: got %#v want %#v", key, legacy[key], want)
		}
	}
	for _, key := range []string{"rows", "SUM_ROWS_SENT", "total_exec_time_ms", "mean_exec_time_ms"} {
		if _, ok := legacy[key]; ok {
			t.Fatalf("legacy payload contains %s", key)
		}
	}
	native := postgresQueryDetailMetricsForMode([]PostgresQueryDetail{row}, true)[0]
	if native["rows"] != uint64(12) || native["total_exec_time_us"] != float64(9000) {
		t.Fatalf("unexpected native payload: %#v", native)
	}
}

func TestSerializedMetricsModelHasNoPostgresFormatEnvelopeFields(t *testing.T) {
	databaseMetricsType := reflect.TypeOf(models.Metrics{}).Field(1).Type
	for _, field := range []string{"QueriesFormat", "DatabaseSchemaFormat"} {
		if _, found := databaseMetricsType.FieldByName(field); found {
			t.Fatalf("serialized metrics model must not contain %s", field)
		}
	}
}

func TestDBMetricsBasePreservesLegacyLightweightQueryPayload(t *testing.T) {
	driverName := "releem_pg_base_legacy_lightweight_payload_test"
	sql.Register(driverName, pgBaseLegacyLightweightDriver{})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	previousDB := models.DB
	models.DB = db
	defer func() { models.DB = previousDB }()

	logger := *logging.Init("postgresql-base-legacy-lightweight-test", false, false, io.Discard)
	defer logger.Close()
	capabilities := &PostgresCapabilities{detect: func(context.Context, *sql.DB) (PostgresCapabilitySnapshot, error) {
		return PostgresCapabilitySnapshot{
			ServerVersionNum:         140000,
			PgStatStatementsRelation: `"public"."pg_stat_statements"`,
			TimingColumn:             "total_exec_time",
			HasRows:                  true,
		}, nil
	}}
	gatherer := NewDBMetricsBaseGatherer(logger, &config.Config{}, capabilities)
	metrics := &models.Metrics{}

	if err := gatherer.GetMetrics(metrics); err != nil {
		t.Fatal(err)
	}
	if len(metrics.DB.Queries) != 1 {
		t.Fatalf("base gatherer query payload: %#v", metrics.DB.Queries)
	}
	query := metrics.DB.Queries[0]
	want := models.MetricGroupValue{
		"datname":            "app",
		"queryid":            "42",
		"calls":              uint64(3),
		"total_exec_time_us": float64(9000),
		"mean_exec_time_us":  float64(3000),
	}
	if !reflect.DeepEqual(query, want) {
		t.Fatalf("base gatherer must preserve legacy lightweight query fields: got %#v want %#v", query, want)
	}
}

type pgBaseLegacyLightweightDriver struct{}

func (pgBaseLegacyLightweightDriver) Open(string) (driver.Conn, error) {
	return pgBaseLegacyLightweightConn{}, nil
}

type pgBaseLegacyLightweightConn struct{}

func (pgBaseLegacyLightweightConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepared statement")
}

func (pgBaseLegacyLightweightConn) Close() error {
	return nil
}

func (pgBaseLegacyLightweightConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}

func (pgBaseLegacyLightweightConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(query, "FROM pg_stat_"):
		return &pgExplainRows{columns: []string{"metric"}}, nil
	case strings.Contains(query, "pg_postmaster_start_time"):
		return &pgExplainRows{columns: []string{"uptime", "timestamp"}, rows: [][]driver.Value{{"10", "20"}}}, nil
	case strings.Contains(query, "FROM pg_database"):
		return &pgExplainRows{columns: []string{"datname"}, rows: [][]driver.Value{{"app"}}}, nil
	case strings.Contains(query, "COUNT(*) FROM"):
		return &pgExplainRows{columns: []string{"count"}, rows: [][]driver.Value{{int64(1)}}}, nil
	case strings.Contains(query, `FROM "public"."pg_stat_statements" s`):
		return &pgExplainRows{columns: []string{"datname", "queryid", "calls", "total_exec_time_us", "mean_exec_time_us", "rows"}, rows: [][]driver.Value{{"app", "42", int64(3), float64(9000), float64(3000), int64(12)}}}, nil
	case strings.Contains(query, "FROM pg_stat_activity"):
		return &pgExplainRows{columns: []string{"pid"}}, nil
	default:
		return nil, errors.New("unexpected query: " + query)
	}
}

func TestQueryContextRowsReturnsPartialValuesOnScanError(t *testing.T) {
	driverName := "releem_pg_partial_scan_helper_test"
	recorder := &pgExplainRecordingDriver{
		queryRows: &pgExplainRows{
			columns: []string{"value"},
			rows: [][]driver.Value{
				{int64(1)},
				{"not an integer"},
			},
		},
	}
	sql.Register(driverName, recorder)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	values, err := queryContextRows(context.Background(), db, "SELECT value", nil, func(rows *sql.Rows) (int64, error) {
		var value int64
		err := rows.Scan(&value)
		return value, err
	})
	if err == nil {
		t.Fatal("terminal scan error should be returned")
	}
	if !reflect.DeepEqual(values, []int64{1}) {
		t.Fatalf("successfully scanned prefix should be retained, got %#v", values)
	}
}

func TestPostgresGatherersShareCapabilities(t *testing.T) {
	logger := logging.Logger{}
	configuration := &config.Config{}
	capabilities := NewPostgresCapabilities(logger)

	base := NewDBMetricsBaseGatherer(logger, configuration, capabilities)
	full := NewDBCollectQueriesOptimization(logger, configuration, capabilities)
	if base.capabilities != capabilities || full.capabilities != capabilities {
		t.Fatal("PostgreSQL gatherers must share the capabilities instance created by main")
	}
}

func TestPgStatStatementsProfilesHaveDistinctProjection(t *testing.T) {
	capabilities := PostgresCapabilitySnapshot{
		PgStatStatementsRelation: `"monitoring"."pg_stat_statements"`,
		TimingColumn:             "total_exec_time",
		HasRows:                  true,
	}
	lightweight := pgStatStatementsQuery(capabilities, postgresQueryLightweight)
	full := pgStatStatementsQuery(capabilities, postgresQueryFull)

	if strings.Contains(lightweight, "min(s.query)") {
		t.Fatalf("lightweight profile must not collect SQL text: %s", lightweight)
	}
	if !strings.Contains(full, "min(s.query) AS query") {
		t.Fatalf("full profile must collect SQL text: %s", full)
	}
	for _, query := range []string{lightweight, full} {
		for _, forbidden := range []string{"query_text", "SUM_ROWS_SENT", "_ms"} {
			if strings.Contains(query, forbidden) {
				t.Fatalf("PostgreSQL query must not expose legacy field %s: %s", forbidden, query)
			}
		}
	}
}

func TestPostgresSchemaMetricsUseNativeLowerCaseFields(t *testing.T) {
	table := PostgresTable{
		Database:        "app",
		Schema:          "public",
		Name:            "orders",
		Type:            "BASE TABLE",
		Relkind:         "r",
		EstimatedRows:   42,
		TableSizeBytes:  4096,
		IndexSizeBytes:  1024,
		TotalSizeBytes:  5120,
		ModSinceAnalyze: 7,
	}
	metric := table.metricGroupValue()
	for key, expected := range map[string]interface{}{
		"table_catalog":       "app",
		"table_schema":        "public",
		"table_name":          "orders",
		"table_type":          "BASE TABLE",
		"relkind":             "r",
		"estimated_rows":      uint64(42),
		"table_size_bytes":    uint64(4096),
		"index_size_bytes":    uint64(1024),
		"total_size_bytes":    uint64(5120),
		"n_mod_since_analyze": uint64(7),
	} {
		if metric[key] != expected {
			t.Fatalf("native table field %s: expected %#v, got %#v", key, expected, metric[key])
		}
	}
	for _, forbidden := range []string{"TABLE_CATALOG", "ENGINE", "TABLE_ROWS", "AVG_ROW_LENGTH", "DATA_LENGTH"} {
		if _, ok := metric[forbidden]; ok {
			t.Fatalf("PostgreSQL table payload must not contain MySQL field %s: %#v", forbidden, metric)
		}
	}
}

func TestPostgresIndexIsOneStructuredObject(t *testing.T) {
	index := PostgresIndex{
		Database:     "app",
		Schema:       "public",
		Table:        "users",
		Name:         "idx_users_lower_email_active",
		AccessMethod: "btree",
		Keys: []PostgresIndexKey{{
			Position:   1,
			Expression: "lower(email)",
			Opclass:    "text_ops",
			Descending: true,
			NullsFirst: true,
		}},
		IncludeColumns:      []string{"created_at"},
		Predicate:           "active",
		Definition:          "CREATE INDEX ...",
		IsValid:             true,
		IsReady:             true,
		IsAttachedPartition: true,
		IdxScan:             5,
	}
	metric := index.metricGroupValue()
	keys, ok := metric["keys"].([]models.MetricGroupValue)
	if !ok || len(keys) != 1 || keys[0]["expression"] != "lower(email)" || keys[0]["descending"] != true || keys[0]["nulls_first"] != true || !reflect.DeepEqual(metric["include_columns"], []string{"created_at"}) {
		t.Fatalf("index keys and INCLUDE columns must remain structured: %#v", metric)
	}
	if metric["table_catalog"] != "app" || metric["table_schema"] != "public" || metric["table_name"] != "users" || metric["index_name"] != "idx_users_lower_email_active" {
		t.Fatalf("index catalog identity must use lower-case PostgreSQL names: %#v", metric)
	}
	if metric["predicate"] != "active" || metric["is_attached_partition"] != true {
		t.Fatalf("partial/attached index identity must be preserved: %#v", metric)
	}
	for _, forbidden := range []string{"SEQ_IN_INDEX", "NON_UNIQUE", "COLUMN_NAME", "PG_INDKEY"} {
		if _, ok := metric[forbidden]; ok {
			t.Fatalf("structured PostgreSQL index must not contain row-shaped field %s: %#v", forbidden, metric)
		}
	}
}

func TestPostgresSchemaSectionsKeepSuccessfulSectionsAroundFailure(t *testing.T) {
	logger := *logging.Init("postgres-schema-sections-test", false, false, io.Discard)
	defer logger.Close()
	metrics := &models.Metrics{}
	collectors := []postgresSchemaSectionCollector{
		{
			name: "information_schema_tables",
			collect: func(context.Context, *sql.DB, string, int) ([]postgresSchemaMetric, error) {
				return []postgresSchemaMetric{
					PostgresTable{Database: "app", Schema: "public", Name: "orders"},
				}, nil
			},
		},
		{
			name: "information_schema_indexes",
			collect: func(context.Context, *sql.DB, string, int) ([]postgresSchemaMetric, error) {
				return []postgresSchemaMetric{
					PostgresIndex{Database: "app", Schema: "public", Table: "orders", Name: "idx_orders_status"},
				}, errors.New("terminal index scan error")
			},
		},
		{
			name: "information_schema_columns",
			collect: func(context.Context, *sql.DB, string, int) ([]postgresSchemaMetric, error) {
				return []postgresSchemaMetric{
					PostgresColumn{Database: "app", Schema: "public", Table: "orders", Name: "id"},
				}, nil
			},
		},
	}

	failedSections := collectPostgresSchemaSections(context.Background(), nil, "app", 140000, logger, metrics, collectors)
	if !reflect.DeepEqual(failedSections, []string{"information_schema_indexes"}) {
		t.Fatalf("unexpected failed sections: %#v", failedSections)
	}
	if len(metrics.DB.DatabaseSchema["information_schema_tables"]) != 1 {
		t.Fatalf("a successful section before a failure must remain available: %#v", metrics.DB.DatabaseSchema)
	}
	if rows, ok := metrics.DB.DatabaseSchema["information_schema_indexes"]; ok {
		t.Fatalf("a failed section must not be added to DatabaseSchema (including partial rows): %#v", rows)
	}
	if len(metrics.DB.DatabaseSchema["information_schema_columns"]) != 1 {
		t.Fatalf("a successful section after a failure must remain available: %#v", metrics.DB.DatabaseSchema)
	}
}

func TestPostgresSchemaSectionsAppendRowsAcrossDatabases(t *testing.T) {
	logger := *logging.Init("postgres-schema-multiple-databases-test", false, false, io.Discard)
	defer logger.Close()
	metrics := &models.Metrics{}
	collectors := []postgresSchemaSectionCollector{
		{
			name: "information_schema_tables",
			collect: func(_ context.Context, _ *sql.DB, database string, _ int) ([]postgresSchemaMetric, error) {
				return []postgresSchemaMetric{
					PostgresTable{Database: database, Schema: "public", Name: "orders"},
				}, nil
			},
		},
	}

	for _, database := range []string{"app", "analytics"} {
		failedSections := collectPostgresSchemaSections(context.Background(), nil, database, 140000, logger, metrics, collectors)
		if len(failedSections) != 0 {
			t.Fatalf("schema collection for %s failed: %#v", database, failedSections)
		}
	}

	tables := metrics.DB.DatabaseSchema["information_schema_tables"]
	if len(tables) != 2 {
		t.Fatalf("successful rows from each database must be appended: %#v", tables)
	}
	if tables[0]["table_catalog"] != "app" || tables[1]["table_catalog"] != "analytics" {
		t.Fatalf("database identity must be preserved while appending rows: %#v", tables)
	}
}

func TestPostgresSchemaSectionsPublishSuccessfulEmptySection(t *testing.T) {
	logger := *logging.Init("postgres-schema-empty-section-test", false, false, io.Discard)
	defer logger.Close()
	metrics := &models.Metrics{}
	collectors := []postgresSchemaSectionCollector{
		{
			name: "information_schema_indexes",
			collect: func(context.Context, *sql.DB, string, int) ([]postgresSchemaMetric, error) {
				return nil, nil
			},
		},
	}

	collectPostgresSchemaSections(context.Background(), nil, "app", 140000, logger, metrics, collectors)
	if rows, ok := metrics.DB.DatabaseSchema["information_schema_indexes"]; !ok || len(rows) != 0 {
		t.Fatalf("a successful empty section must be published explicitly: %#v", metrics.DB.DatabaseSchema)
	}
}

func TestAppendUniquePostgresSchemaSections(t *testing.T) {
	got := appendUniquePostgresSchemaSections(
		[]string{"information_schema_tables"},
		"information_schema_indexes",
		"information_schema_tables",
		"information_schema_indexes",
	)
	want := []string{"information_schema_tables", "information_schema_indexes"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("failed schema sections = %#v, want %#v", got, want)
	}
}

func TestCollectExplainDetailsUsesTypedQueryText(t *testing.T) {
	logger := *logging.Init("postgres-explain-typed-test", false, false, io.Discard)
	defer logger.Close()
	details := map[string]PostgresQueryDetail{
		pgDigestKey("app", "42"): {
			PostgresQueryStats: PostgresQueryStats{Datname: "app", QueryID: "42", TotalExecTimeUS: 10000},
			Query:              "VACUUM orders",
		},
	}
	state := u.NewExplainCollectionState()
	defer state.Close()

	collectExplainDetails(details, "total_exec_time_us", true, logger, &config.Config{}, state)
	if got := details[pgDigestKey("app", "42")]; got.Query != "VACUUM orders" || got.Explain != "" || got.ExplainError != "" {
		t.Fatalf("unsupported statement should remain unchanged: %#v", got)
	}
}

func TestStructuredIndexQueryAggregatesKeysAndIncludeColumns(t *testing.T) {
	query := postgresStructuredIndexQuery(160000)
	for _, fragment := range []string{
		"unnest(idx.indkey) WITH ORDINALITY",
		"jsonb_build_object",
		"'column_name'",
		"'expression'",
		"'opclass'",
		"'collation'",
		"'opclass_schema'",
		"'collation_schema'",
		"'opclass_is_default'",
		"'collation_is_default'",
		"'descending'",
		"'nulls_first'",
		"key_info.position <= idx.indnkeyatts",
		"key_info.position > idx.indnkeyatts",
		"pg_get_indexdef(idx.indexrelid, key_info.position::integer, true)",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("structured index query must contain %q: %s", fragment, query)
		}
	}
	if strings.Contains(query, "idx.indkey[position]") {
		t.Fatalf("int2vector must not be subscripted as a one-based array: %s", query)
	}
	if strings.Contains(postgresStructuredIndexQuery(120000), "idx.indnullsnotdistinct") {
		t.Fatal("PG12 index query must not reference PG15 indnullsnotdistinct")
	}
	if !strings.Contains(query, "stats.last_idx_scan") {
		t.Fatal("PG16+ index query should collect last_idx_scan")
	}
}

func TestPostgresColumnsQueryMatchesServerVersion(t *testing.T) {
	tests := []struct {
		version int
		want    []string
		omit    []string
	}{
		{
			version: 90500,
			want:    []string{"false AS is_identity", "false AS is_generated", "'' AS generation_expression"},
			omit:    []string{"is_identity = 'YES'", "is_generated <> 'NEVER'", "COALESCE(generation_expression, '')"},
		},
		{
			version: 100000,
			want:    []string{"is_identity = 'YES'", "false AS is_generated", "'' AS generation_expression"},
			omit:    []string{"is_generated <> 'NEVER'", "COALESCE(generation_expression, '')"},
		},
		{
			version: 120000,
			want:    []string{"is_identity = 'YES'", "is_generated <> 'NEVER'", "COALESCE(generation_expression, '')"},
		},
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
