package mysql

import (
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/Releem/mysqlconfigurer/models"
	mysqldriver "github.com/go-sql-driver/mysql"
	logging "github.com/google/logger"
)

func TestCollectDbSchemaSerializesMySQLAndMariaDBMetadataMatrix(t *testing.T) {
	tests := []struct {
		name  string
		query func(string) (driver.Rows, error)
		want  map[string]map[string]interface{}
	}{
		{
			name: "signed auto increment",
			query: func(query string) (driver.Rows, error) {
				switch {
				case query == mysqlTableSchemaSelectQuery:
					return mysqlSchemaRows([]driver.Value{"app", "orders", "BASE TABLE", "InnoDB", "Dynamic", "1", "16", "0", "16", "0", "utf8mb4", "0", "2147483647"}), nil
				case strings.Contains(query, "FROM information_schema.columns"):
					return mysqlSchemaRows([]driver.Value{"app", "orders", "id", "1", "NULL", "NO", "int", "NULL", "10", "0", "NULL", "NULL", "int(11)", "PRI", "auto_increment", "NULL"}), nil
				}
				return mysqlEmptySchemaRows(), nil
			},
			want: map[string]map[string]interface{}{
				"information_schema_tables":  {"TABLE_SCHEMA": "app", "TABLE_NAME": "orders", "AUTO_INCREMENT": "2147483647"},
				"information_schema_columns": {"TABLE_SCHEMA": "app", "TABLE_NAME": "orders", "COLUMN_NAME": "id", "COLUMN_TYPE": "int(11)", "EXTRA": "auto_increment"},
			},
		},
		{
			name: "unsigned auto increment",
			query: func(query string) (driver.Rows, error) {
				switch {
				case query == mysqlTableSchemaSelectQuery:
					return mysqlSchemaRows([]driver.Value{"app", "events", "BASE TABLE", "InnoDB", "Dynamic", "1", "16", "0", "16", "0", "utf8mb4", "0", "4294967295"}), nil
				case strings.Contains(query, "FROM information_schema.columns"):
					return mysqlSchemaRows([]driver.Value{"app", "events", "id", "1", "NULL", "NO", "int", "NULL", "10", "0", "NULL", "NULL", "int(10) unsigned", "PRI", "auto_increment", "NULL"}), nil
				}
				return mysqlEmptySchemaRows(), nil
			},
			want: map[string]map[string]interface{}{
				"information_schema_tables":  {"TABLE_SCHEMA": "app", "TABLE_NAME": "events", "AUTO_INCREMENT": "4294967295"},
				"information_schema_columns": {"TABLE_SCHEMA": "app", "TABLE_NAME": "events", "COLUMN_NAME": "id", "COLUMN_TYPE": "int(10) unsigned", "EXTRA": "auto_increment"},
			},
		},
		{
			name: "generated column",
			query: func(query string) (driver.Rows, error) {
				if strings.Contains(query, "FROM information_schema.columns") {
					return mysqlSchemaRows([]driver.Value{"app", "users", "email_normalized", "2", "NULL", "YES", "varchar", "255", "NULL", "NULL", "utf8mb4", "utf8mb4_bin", "varchar(255)", "", "STORED GENERATED", "lower(`email`)"}), nil
				}
				return mysqlEmptySchemaRows(), nil
			},
			want: map[string]map[string]interface{}{
				"information_schema_columns": {"TABLE_SCHEMA": "app", "TABLE_NAME": "users", "COLUMN_NAME": "email_normalized", "EXTRA": "STORED GENERATED", "GENERATION_EXPRESSION": "lower(`email`)"},
			},
		},
		{
			name:  "functional index",
			query: mysqlSchemaIndexMatrixQuery([]driver.Value{"app", "users", "idx_email_lower", "1", "1", "NULL", "A", "12", "NULL", "NULL", "YES", "BTREE", "lower(`email`)", "YES"}),
			want: map[string]map[string]interface{}{
				"information_schema_indexes": {"TABLE_SCHEMA": "app", "TABLE_NAME": "users", "INDEX_NAME": "idx_email_lower", "COLUMN_NAME": "NULL", "NULLABLE": "YES", "EXPRESSION": "lower(`email`)"},
			},
		},
		{
			name:  "prefix index",
			query: mysqlSchemaIndexMatrixQuery([]driver.Value{"app", "users", "idx_email_prefix", "1", "1", "email", "A", "12", "16", "NULL", "YES", "BTREE", "NULL", "YES"}),
			want: map[string]map[string]interface{}{
				"information_schema_indexes": {"TABLE_SCHEMA": "app", "TABLE_NAME": "users", "INDEX_NAME": "idx_email_prefix", "SUB_PART": "16", "NULLABLE": "YES", "IS_VISIBLE": "YES"},
			},
		},
		{
			name:  "invisible index",
			query: mysqlSchemaIndexMatrixQuery([]driver.Value{"app", "orders", "idx_customer", "1", "1", "customer_id", "A", "12", "NULL", "NULL", "YES", "BTREE", "NULL", "NO"}),
			want: map[string]map[string]interface{}{
				"information_schema_indexes": {"TABLE_SCHEMA": "app", "TABLE_NAME": "orders", "INDEX_NAME": "idx_customer", "INDEX_TYPE": "BTREE", "IS_VISIBLE": "NO"},
			},
		},
		{
			name:  "fulltext index",
			query: mysqlSchemaIndexMatrixQuery([]driver.Value{"app", "articles", "idx_body", "1", "1", "body", "NULL", "12", "NULL", "NULL", "YES", "FULLTEXT", "NULL", "YES"}),
			want: map[string]map[string]interface{}{
				"information_schema_indexes": {"TABLE_SCHEMA": "app", "TABLE_NAME": "articles", "INDEX_NAME": "idx_body", "INDEX_TYPE": "FULLTEXT", "COLUMN_NAME": "body"},
			},
		},
		{
			name:  "spatial index",
			query: mysqlSchemaIndexMatrixQuery([]driver.Value{"app", "places", "idx_location", "1", "1", "location", "A", "12", "NULL", "NULL", "YES", "SPATIAL", "NULL", "YES"}),
			want: map[string]map[string]interface{}{
				"information_schema_indexes": {"TABLE_SCHEMA": "app", "TABLE_NAME": "places", "INDEX_NAME": "idx_location", "INDEX_TYPE": "SPATIAL", "COLUMN_NAME": "location"},
			},
		},
		{
			name: "foreign key metadata",
			query: func(query string) (driver.Rows, error) {
				switch {
				case strings.Contains(query, "FROM information_schema.REFERENTIAL_CONSTRAINTS"):
					return mysqlSchemaRows([]driver.Value{"app", "fk_orders_customer", "app", "PRIMARY", "NONE", "CASCADE", "RESTRICT", "orders", "customers"}), nil
				case strings.Contains(query, "FROM information_schema.KEY_COLUMN_USAGE"):
					return mysqlSchemaRows([]driver.Value{"app", "fk_orders_customer", "app", "orders", "customer_id", "1", "1", "app", "customers", "id"}), nil
				case strings.Contains(query, "FROM information_schema.TABLE_CONSTRAINTS"):
					return mysqlSchemaRows([]driver.Value{"app", "fk_orders_customer", "app", "orders", "FOREIGN KEY"}), nil
				}
				return mysqlEmptySchemaRows(), nil
			},
			want: map[string]map[string]interface{}{
				"information_schema_referential_constraints": {"CONSTRAINT_SCHEMA": "app", "TABLE_NAME": "orders", "CONSTRAINT_NAME": "fk_orders_customer", "REFERENCED_TABLE_NAME": "customers"},
				"information_schema_key_column_usage":        {"TABLE_SCHEMA": "app", "TABLE_NAME": "orders", "COLUMN_NAME": "customer_id", "CONSTRAINT_NAME": "fk_orders_customer", "REFERENCED_COLUMN_NAME": "id"},
				"information_schema_table_constraints":       {"TABLE_SCHEMA": "app", "TABLE_NAME": "orders", "CONSTRAINT_NAME": "fk_orders_customer", "CONSTRAINT_TYPE": "FOREIGN KEY"},
			},
		},
		{
			name: "mariadb missing and nullable index metadata",
			query: func(query string) (driver.Rows, error) {
				switch query {
				case mysqlIndexSchemaSelectQuery:
					return nil, errors.New("Unknown column 'EXPRESSION'")
				case mysqlIndexSchemaSelectQueryVisibility:
					return nil, errors.New("Unknown column 'IS_VISIBLE'")
				case mysqlIndexSchemaSelectQueryMariaDB:
					return mysqlSchemaRows([]driver.Value{"app", "events", "idx_payload", "1", "1", "payload", "NULL", "NULL", "NULL", "NULL", "NULL", "BTREE", "NULL", "NO"}), nil
				}
				return mysqlEmptySchemaRows(), nil
			},
			want: map[string]map[string]interface{}{
				"information_schema_indexes": {"TABLE_SCHEMA": "app", "TABLE_NAME": "events", "INDEX_NAME": "idx_payload", "CARDINALITY": "NULL", "SUB_PART": "NULL", "NULLABLE": "NULL", "EXPRESSION": "NULL", "IS_VISIBLE": "NO"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setMysqlSchemaTestDB(t, test.query)

			metrics := &models.Metrics{}
			metrics.DB.DatabaseSchema = make(map[string][]models.MetricGroupValue)
			logger := *logging.Init("mysql-schema-matrix-test", false, false, io.Discard)
			if err := CollectDbSchema("app", logger, metrics); err != nil {
				t.Fatalf("CollectDbSchema returned an error: %v", err)
			}

			for section, fields := range test.want {
				rows := metrics.DB.DatabaseSchema[section]
				if len(rows) != 1 {
					t.Fatalf("%s row count = %d, want exactly 1; payload=%#v", section, len(rows), metrics.DB.DatabaseSchema)
				}
				for key, want := range fields {
					if got := rows[0][key]; got != want {
						t.Fatalf("%s[%q] = %#v, want %#v; row=%#v", section, key, got, want, rows[0])
					}
				}
			}
		})
	}
}

func TestCollectDbSchemaSerializesLegacyColumnFallbackExactly(t *testing.T) {
	columnQueries := 0
	setMysqlSchemaTestDB(t, func(query string) (driver.Rows, error) {
		if !strings.Contains(query, "FROM information_schema.columns") {
			return mysqlEmptySchemaRows(), nil
		}
		columnQueries++
		if strings.Contains(query, "GENERATION_EXPRESSION") {
			return nil, &mysqldriver.MySQLError{Number: 1054, Message: "Unknown column 'GENERATION_EXPRESSION'"}
		}
		return mysqlSchemaRows([]driver.Value{
			"legacy_app", "orders", "id", "1", "NULL", "NO", "bigint", "NULL",
			"20", "0", "NULL", "NULL", "bigint unsigned", "PRI", "auto_increment",
		}), nil
	})

	metrics := &models.Metrics{}
	metrics.DB.DatabaseSchema = make(map[string][]models.MetricGroupValue)
	logger := *logging.Init("mysql-schema-legacy-column-test", false, false, io.Discard)
	if err := CollectDbSchema("legacy_app", logger, metrics); err != nil {
		t.Fatalf("CollectDbSchema returned an error: %v", err)
	}

	rows := metrics.DB.DatabaseSchema["information_schema_columns"]
	if len(rows) != 1 {
		t.Fatalf("legacy columns row count = %d, want exactly 1; payload=%#v", len(rows), metrics.DB.DatabaseSchema)
	}
	want := models.MetricGroupValue{
		"TABLE_SCHEMA": "legacy_app", "TABLE_NAME": "orders", "COLUMN_NAME": "id",
		"ORDINAL_POSITION": "1", "COLUMN_DEFAULT": "NULL", "IS_NULLABLE": "NO",
		"DATA_TYPE": "bigint", "CHARACTER_MAXIMUM_LENGTH": "NULL", "NUMERIC_PRECISION": "20",
		"NUMERIC_SCALE": "0", "CHARACTER_SET_NAME": "NULL", "COLLATION_NAME": "NULL",
		"COLUMN_TYPE": "bigint unsigned", "COLUMN_KEY": "PRI", "EXTRA": "auto_increment",
		"GENERATION_EXPRESSION": "NULL",
	}
	if !reflect.DeepEqual(rows[0], want) {
		t.Fatalf("legacy column row = %#v, want %#v", rows[0], want)
	}
	if columnQueries != 2 {
		t.Fatalf("column query count = %d, want modern query plus one legacy fallback", columnQueries)
	}
}

func mysqlSchemaIndexMatrixQuery(values []driver.Value) func(string) (driver.Rows, error) {
	return func(query string) (driver.Rows, error) {
		if query == mysqlIndexSchemaSelectQuery {
			return mysqlSchemaRows(values), nil
		}
		return mysqlEmptySchemaRows(), nil
	}
}

func TestCollectDbSchemaContinuesAfterInaccessibleSchema(t *testing.T) {
	setMysqlSchemaTestDB(t, func(query string) (driver.Rows, error) {
		if query == mysqlTableSchemaSelectQuery {
			return mysqlSchemaRows([]driver.Value{"app", "orders", "BASE TABLE", "InnoDB", "Dynamic", "1", "16", "0", "16", "0", "utf8mb4", "0", "1"}), nil
		}
		return mysqlEmptySchemaRows(), nil
	})

	metrics := &models.Metrics{}
	metrics.DB.DatabaseSchema = make(map[string][]models.MetricGroupValue)
	logger := *logging.Init("mysql-schema-inaccessible-test", false, false, io.Discard)
	if err := CollectDbSchema("app", logger, metrics); err != nil {
		t.Fatalf("CollectDbSchema(app) returned an error: %v", err)
	}

	setMysqlSchemaTestDB(t, func(query string) (driver.Rows, error) {
		if query == mysqlTableSchemaSelectQuery {
			return nil, errors.New("SELECT command denied to user for table 'tables'")
		}
		return mysqlEmptySchemaRows(), nil
	})
	if err := CollectDbSchema("restricted", logger, metrics); err != nil {
		t.Fatalf("inaccessible schema should be recorded without aborting collection: %v", err)
	}

	rows := metrics.DB.DatabaseSchema["information_schema_tables"]
	if len(rows) != 1 || rows[0]["TABLE_SCHEMA"] != "app" {
		t.Fatalf("accessible schema payload was discarded: %#v", rows)
	}
	if got, want := metrics.DB.FailedDatabaseSchema, []string{"information_schema_tables"}; !equalStringSlices(got, want) {
		t.Fatalf("failed sections = %#v, want %#v", got, want)
	}
}

func TestCollectDbSchemaReportsManagedServicePermissionFailures(t *testing.T) {
	tests := []struct {
		name    string
		match   string
		section string
	}{
		{"statistics", "FROM information_schema.statistics", "information_schema_indexes"},
		{"referential constraints", "FROM information_schema.REFERENTIAL_CONSTRAINTS", "information_schema_referential_constraints"},
		{"key column usage", "FROM information_schema.KEY_COLUMN_USAGE", "information_schema_key_column_usage"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setMysqlSchemaTestDB(t, func(query string) (driver.Rows, error) {
				if strings.Contains(query, test.match) {
					return nil, errors.New("command denied to user on managed service")
				}
				return mysqlEmptySchemaRows(), nil
			})

			metrics := &models.Metrics{}
			metrics.DB.DatabaseSchema = make(map[string][]models.MetricGroupValue)
			logger := *logging.Init("mysql-schema-managed-permissions-test", false, false, io.Discard)
			if err := CollectDbSchema("app", logger, metrics); err != nil {
				t.Fatalf("managed-service permission failure should not abort collection: %v", err)
			}
			if got, want := metrics.DB.FailedDatabaseSchema, []string{test.section}; !equalStringSlices(got, want) {
				t.Fatalf("failed sections = %#v, want %#v", got, want)
			}
		})
	}
}

func equalStringSlices(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
