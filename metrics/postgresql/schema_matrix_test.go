package postgresql

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	logging "github.com/google/logger"
)

func TestPostgresIndexSerializationMatrix(t *testing.T) {
	ordinaryKey := PostgresIndexKey{Position: 1, ColumnName: "customer_id", Definition: "customer_id"}
	expressionKey := PostgresIndexKey{Position: 1, Expression: "lower(email)", Definition: "lower(email)"}
	tests := []struct {
		name      string
		index     PostgresIndex
		key       PostgresIndexKey
		wantValid bool
		wantReady bool
	}{
		{name: "ordinary", key: ordinaryKey, wantValid: true, wantReady: true},
		{name: "expression", key: expressionKey, wantValid: true, wantReady: true},
		{name: "partial", index: PostgresIndex{Predicate: "active"}, key: ordinaryKey, wantValid: true, wantReady: true},
		{name: "include", index: PostgresIndex{IncludeColumns: []string{"created_at"}}, key: ordinaryKey, wantValid: true, wantReady: true},
		{name: "descending_nulls_first", key: PostgresIndexKey{Position: 1, ColumnName: "created_at", Descending: true, NullsFirst: true, Definition: "created_at DESC NULLS FIRST"}, wantValid: true, wantReady: true},
		{name: "custom_opclass", key: PostgresIndexKey{Position: 1, ColumnName: "email", Opclass: "text_pattern_ops", Definition: "email text_pattern_ops"}, wantValid: true, wantReady: true},
		{name: "custom_collation", key: PostgresIndexKey{Position: 1, ColumnName: "email", Collation: "case_sensitive", Definition: "email COLLATE case_sensitive"}, wantValid: true, wantReady: true},
		{name: "unique", index: PostgresIndex{IsUnique: true}, key: ordinaryKey, wantValid: true, wantReady: true},
		{name: "invalid", key: ordinaryKey, wantValid: false, wantReady: true},
		{name: "unready", key: ordinaryKey, wantValid: true, wantReady: false},
		{name: "exclusion", index: PostgresIndex{IsExclusion: true}, key: ordinaryKey, wantValid: true, wantReady: true},
		{name: "partitioned", index: PostgresIndex{IsPartitioned: true, RelationKind: "p"}, key: ordinaryKey, wantValid: true, wantReady: true},
		{name: "attached_partition", index: PostgresIndex{IsAttachedPartition: true}, key: ordinaryKey, wantValid: true, wantReady: true},
		{name: "clustered", index: PostgresIndex{IsClustered: true}, key: ordinaryKey, wantValid: true, wantReady: true},
		{name: "replica_identity", index: PostgresIndex{IsReplicaIdentity: true}, key: ordinaryKey, wantValid: true, wantReady: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			index := test.index
			index.Database = "app"
			index.Schema = "public"
			index.Table = "orders"
			index.Name = "idx_orders_contract"
			index.AccessMethod = "btree"
			index.Definition = "CREATE INDEX idx_orders_contract ON public.orders"
			index.Keys = []PostgresIndexKey{test.key}
			index.IsValid = test.wantValid
			index.IsReady = test.wantReady

			metric := index.metricGroupValue()
			assertExactPostgresMetricKeys(t, metric,
				"table_catalog", "table_schema", "table_name", "index_name", "access_method", "keys", "include_columns", "predicate", "definition",
				"is_unique", "is_primary", "is_valid", "is_ready", "is_exclusion", "nulls_not_distinct", "is_partitioned", "is_attached_partition", "is_clustered", "is_replica_identity", "relation_kind", "idx_scan", "last_idx_scan", "stats_reset",
			)
			wantKeys := []models.MetricGroupValue{test.key.metricGroupValue()}
			if !reflect.DeepEqual(metric["keys"], wantKeys) {
				t.Fatalf("serialized index keys: got %#v want %#v", metric["keys"], wantKeys)
			}
			for key, want := range map[string]interface{}{
				"table_catalog": "app", "table_schema": "public", "table_name": "orders", "index_name": "idx_orders_contract",
				"predicate": nullableNonEmptyString(test.index.Predicate), "include_columns": test.index.IncludeColumns,
				"is_unique": test.index.IsUnique, "is_valid": index.IsValid, "is_ready": index.IsReady,
				"is_exclusion": test.index.IsExclusion, "is_partitioned": test.index.IsPartitioned,
				"is_attached_partition": test.index.IsAttachedPartition, "is_clustered": test.index.IsClustered,
				"is_replica_identity": test.index.IsReplicaIdentity, "relation_kind": test.index.RelationKind,
			} {
				if got := metric[key]; !reflect.DeepEqual(got, want) {
					t.Fatalf("serialized index field %q: got %#v want %#v", key, got, want)
				}
			}
		})
	}
}

func TestPostgresIndexKeyPreservesCollationAndOpclassNamespaces(t *testing.T) {
	key := PostgresIndexKey{
		Position:        1,
		ColumnName:      "email",
		Collation:       "case_sensitive",
		CollationSchema: "custom",
		Opclass:         "text_pattern_ops",
		OpclassSchema:   "custom",
		Definition:      "email COLLATE custom.case_sensitive custom.text_pattern_ops",
	}
	metric := key.metricGroupValue()
	if metric["collation_schema"] != "custom" || metric["opclass_schema"] != "custom" {
		t.Fatalf("catalog namespaces were not serialized: %#v", metric)
	}
}

func TestPostgresSchemaFailureContinuationMatrix(t *testing.T) {
	logger := *logging.Init("postgres-schema-failure-matrix-test", false, false, io.Discard)
	defer logger.Close()
	metrics := &models.Metrics{}
	collectors := []postgresSchemaSectionCollector{
		{name: "information_schema_tables", collect: func(context.Context, *sql.DB, string, int) ([]postgresSchemaMetric, error) {
			return []postgresSchemaMetric{PostgresTable{Database: "app", Schema: "public", Name: "orders"}}, nil
		}},
		{name: "information_schema_indexes", collect: func(context.Context, *sql.DB, string, int) ([]postgresSchemaMetric, error) {
			return nil, errors.New("permission denied")
		}},
		{name: "information_schema_columns", collect: func(context.Context, *sql.DB, string, int) ([]postgresSchemaMetric, error) {
			return []postgresSchemaMetric{PostgresColumn{Database: "app", Schema: "public", Table: "orders", Name: "id"}}, nil
		}},
	}

	if failed := collectPostgresSchemaSections(context.Background(), nil, "app", 160000, logger, metrics, collectors); !reflect.DeepEqual(failed, []string{"information_schema_indexes"}) {
		t.Fatalf("failed schema sections = %#v", failed)
	}
	if len(metrics.DB.DatabaseSchema["information_schema_tables"]) != 1 || len(metrics.DB.DatabaseSchema["information_schema_columns"]) != 1 {
		t.Fatalf("successful sections on both sides of failure must remain: %#v", metrics.DB.DatabaseSchema)
	}
	if rows, exists := metrics.DB.DatabaseSchema["information_schema_indexes"]; exists {
		t.Fatalf("failed section must not be added to DatabaseSchema: %#v", rows)
	}

	failedMetadata := appendUniquePostgresSchemaSections(nil, "information_schema_indexes")
	failedMetadata = appendUniquePostgresSchemaSections(failedMetadata, "information_schema_indexes")
	wantMetadata := []string{"information_schema_indexes"}
	if !reflect.DeepEqual(failedMetadata, wantMetadata) {
		t.Fatalf("schema failure metadata must deduplicate section names: got %#v want %#v", failedMetadata, wantMetadata)
	}
}

func TestCollectDbSchemaConnectionFailureIsTypedAndContextual(t *testing.T) {
	logger := *logging.Init("postgres-schema-connection-failure-test", false, false, io.Discard)
	defer logger.Close()

	err := CollectDbSchema(&config.Config{
		PgUser: "monitor", PgPassword: "secret", PgHost: "127.0.0.1", PgPort: "invalid",
	}, logger, "app", 160000, &models.Metrics{})
	if err == nil {
		t.Fatal("database connection failure must be returned")
	}
	var collectionError *postgresSchemaCollectionError
	if !errors.As(err, &collectionError) {
		t.Fatalf("database connection failure must use postgresSchemaCollectionError, got %T: %v", err, err)
	}
	if !reflect.DeepEqual(collectionError.sections, []string{"app:__database_connection__"}) {
		t.Fatalf("database connection failure context: %#v", collectionError)
	}
	if !strings.Contains(err.Error(), "app:__database_connection__") {
		t.Fatalf("database connection failure message must retain context: %v", err)
	}
}

func TestPostgresGetMetricsCollectsScopedSchemaFailuresPerExecution(t *testing.T) {
	logger := *logging.Init("postgres-schema-get-metrics-failure-test", false, false, io.Discard)
	defer logger.Close()

	configuration := &config.Config{
		QueryOptimization: true,
		PgUser:            "monitor",
		PgPassword:        "secret",
		PgHost:            "127.0.0.1",
		PgPort:            "invalid",
	}
	capabilities := &PostgresCapabilities{detect: func(context.Context, *sql.DB) (PostgresCapabilitySnapshot, error) {
		return PostgresCapabilitySnapshot{}, errors.New("pg_stat_statements unavailable in schema test")
	}}
	gatherer := NewDBCollectQueriesOptimization(logger, configuration, capabilities)

	metrics := &models.Metrics{}
	metrics.DB.Metrics.Databases = []string{"app", "analytics"}
	if err := gatherer.GetMetrics(metrics); err != nil {
		t.Fatalf("GetMetrics returned an error: %v", err)
	}

	wantFailures := []string{
		"app:__database_connection__",
		"analytics:__database_connection__",
	}
	if !reflect.DeepEqual(metrics.DB.FailedDatabaseSchema, wantFailures) {
		t.Fatalf("scoped schema failures: got %#v want %#v", metrics.DB.FailedDatabaseSchema, wantFailures)
	}
}

func TestPostgresSchemaFailureWireShapeKeepsUnscopedStringsCompatible(t *testing.T) {
	failures := appendUniquePostgresSchemaSections(nil, "information_schema_indexes")
	if !reflect.DeepEqual(failures, []string{"information_schema_indexes"}) {
		t.Fatalf("legacy failure list must remain simple strings: %#v", failures)
	}
}

func TestPostgresGetMetricsDoesNotEmitScopedSchemaFailuresWhenOptimizationDisabled(t *testing.T) {
	logger := *logging.Init("postgres-schema-disabled-payload-test", false, false, io.Discard)
	defer logger.Close()
	capabilities := &PostgresCapabilities{detect: func(context.Context, *sql.DB) (PostgresCapabilitySnapshot, error) {
		return PostgresCapabilitySnapshot{}, errors.New("pg_stat_statements unavailable in schema test")
	}}
	gatherer := NewDBCollectQueriesOptimization(logger, &config.Config{QueryOptimization: false}, capabilities)
	metrics := &models.Metrics{}
	metrics.DB.Metrics.Databases = []string{"app"}

	if err := gatherer.GetMetrics(metrics); err != nil {
		t.Fatal(err)
	}
	if metrics.DB.FailedDatabaseSchema != nil && len(metrics.DB.FailedDatabaseSchema) != 0 {
		t.Fatalf("normal monitoring must remain schema-failure-marker-free: failures=%#v", metrics.DB.FailedDatabaseSchema)
	}
}

func TestPostgresGetMetricsPublishesAllEmptySchemaSectionsWhenNoDatabaseIsSelected(t *testing.T) {
	logger := *logging.Init("postgres-schema-empty-selection-test", false, false, io.Discard)
	defer logger.Close()
	capabilities := &PostgresCapabilities{detect: func(context.Context, *sql.DB) (PostgresCapabilitySnapshot, error) {
		return PostgresCapabilitySnapshot{}, errors.New("pg_stat_statements unavailable in schema test")
	}}
	gatherer := NewDBCollectQueriesOptimization(logger, &config.Config{QueryOptimization: true}, capabilities)
	metrics := &models.Metrics{}

	if err := gatherer.GetMetrics(metrics); err != nil {
		t.Fatal(err)
	}

	if len(metrics.DB.DatabaseSchema) != 0 {
		t.Fatalf("schema sections should stay absent when no databases are selected: %#v", metrics.DB.DatabaseSchema)
	}
}
