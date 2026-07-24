package postgresql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	u "github.com/Releem/mysqlconfigurer/utils"
	logging "github.com/google/logger"
	"github.com/lib/pq"
)

type postgresSchemaMetric interface {
	metricGroupValue() models.MetricGroupValue
}

type PostgresTable struct {
	Database, Schema, Name, Type, Relkind string
	EstimatedRows                         uint64
	TableSizeBytes, IndexSizeBytes        uint64
	TotalSizeBytes, ModSinceAnalyze       uint64
	LastAnalyze, LastAutoAnalyze          *string
}

func (table PostgresTable) metricGroupValue() models.MetricGroupValue {
	return models.MetricGroupValue{
		"table_catalog":       table.Database,
		"table_schema":        table.Schema,
		"table_name":          table.Name,
		"table_type":          table.Type,
		"relkind":             table.Relkind,
		"estimated_rows":      table.EstimatedRows,
		"table_size_bytes":    table.TableSizeBytes,
		"index_size_bytes":    table.IndexSizeBytes,
		"total_size_bytes":    table.TotalSizeBytes,
		"n_mod_since_analyze": table.ModSinceAnalyze,
		"last_analyze":        nullableStringValue(table.LastAnalyze),
		"last_autoanalyze":    nullableStringValue(table.LastAutoAnalyze),
	}
}

type PostgresColumn struct {
	Database, Schema, Table, Name, Default, DataType string
	OrdinalPosition                                  int
	IsNullable, IsIdentity, IsGenerated              bool
	CharacterMaximumLength, NumericPrecision         *int64
	NumericScale                                     *int64
	GenerationExpression                             *string
}

func (column PostgresColumn) metricGroupValue() models.MetricGroupValue {
	return models.MetricGroupValue{
		"table_catalog":            column.Database,
		"table_schema":             column.Schema,
		"table_name":               column.Table,
		"column_name":              column.Name,
		"ordinal_position":         column.OrdinalPosition,
		"column_default":           nullableNonEmptyString(column.Default),
		"is_nullable":              column.IsNullable,
		"data_type":                column.DataType,
		"character_maximum_length": nullableInt64Value(column.CharacterMaximumLength),
		"numeric_precision":        nullableInt64Value(column.NumericPrecision),
		"numeric_scale":            nullableInt64Value(column.NumericScale),
		"is_identity":              column.IsIdentity,
		"is_generated":             column.IsGenerated,
		"generation_expression":    nullableStringValue(column.GenerationExpression),
	}
}

type PostgresIndex struct {
	Database, Schema, Table, Name, AccessMethod string
	Keys                                        []PostgresIndexKey
	IncludeColumns                              []string
	Predicate, Definition                       string
	IsUnique, IsPrimary, IsValid, IsReady       bool
	IsExclusion, NullsNotDistinct               bool
	IsPartitioned, IsAttachedPartition          bool
	IsClustered, IsReplicaIdentity              bool
	RelationKind                                string
	IdxScan                                     uint64
	LastIdxScan, StatsReset                     *string
}

type PostgresIndexKey struct {
	Position           int    `json:"position"`
	ColumnName         string `json:"column_name"`
	Expression         string `json:"expression"`
	Collation          string `json:"collation"`
	Opclass            string `json:"opclass"`
	CollationSchema    string `json:"collation_schema"`
	OpclassSchema      string `json:"opclass_schema"`
	CollationIsDefault bool   `json:"collation_is_default"`
	OpclassIsDefault   bool   `json:"opclass_is_default"`
	Descending         bool   `json:"descending"`
	NullsFirst         bool   `json:"nulls_first"`
	Definition         string `json:"definition"`
}

func (key PostgresIndexKey) metricGroupValue() models.MetricGroupValue {
	return models.MetricGroupValue{
		"position":             key.Position,
		"column_name":          nullableNonEmptyString(key.ColumnName),
		"expression":           nullableNonEmptyString(key.Expression),
		"collation":            nullableNonEmptyString(key.Collation),
		"opclass":              nullableNonEmptyString(key.Opclass),
		"collation_schema":     nullableNonEmptyString(key.CollationSchema),
		"opclass_schema":       nullableNonEmptyString(key.OpclassSchema),
		"collation_is_default": key.CollationIsDefault,
		"opclass_is_default":   key.OpclassIsDefault,
		"descending":           key.Descending,
		"nulls_first":          key.NullsFirst,
		"definition":           key.Definition,
	}
}

func (index PostgresIndex) metricGroupValue() models.MetricGroupValue {
	keys := make([]models.MetricGroupValue, 0, len(index.Keys))
	for _, key := range index.Keys {
		keys = append(keys, key.metricGroupValue())
	}
	return models.MetricGroupValue{
		"table_catalog":         index.Database,
		"table_schema":          index.Schema,
		"table_name":            index.Table,
		"index_name":            index.Name,
		"access_method":         index.AccessMethod,
		"keys":                  keys,
		"include_columns":       index.IncludeColumns,
		"predicate":             nullableNonEmptyString(index.Predicate),
		"definition":            index.Definition,
		"is_unique":             index.IsUnique,
		"is_primary":            index.IsPrimary,
		"is_valid":              index.IsValid,
		"is_ready":              index.IsReady,
		"is_exclusion":          index.IsExclusion,
		"nulls_not_distinct":    index.NullsNotDistinct,
		"is_partitioned":        index.IsPartitioned,
		"is_attached_partition": index.IsAttachedPartition,
		"is_clustered":          index.IsClustered,
		"is_replica_identity":   index.IsReplicaIdentity,
		"relation_kind":         index.RelationKind,
		"idx_scan":              index.IdxScan,
		"last_idx_scan":         nullableStringValue(index.LastIdxScan),
		"stats_reset":           nullableStringValue(index.StatsReset),
	}
}

type PostgresReferentialConstraint struct {
	Database, Schema, Name, UniqueSchema, UniqueName string
	MatchOption, UpdateRule, DeleteRule              string
	Table, ReferencedSchema, ReferencedTable         string
}

func (constraint PostgresReferentialConstraint) metricGroupValue() models.MetricGroupValue {
	return models.MetricGroupValue{
		"constraint_catalog":        constraint.Database,
		"constraint_schema":         constraint.Schema,
		"constraint_name":           constraint.Name,
		"unique_constraint_catalog": constraint.Database,
		"unique_constraint_schema":  constraint.UniqueSchema,
		"unique_constraint_name":    constraint.UniqueName,
		"match_option":              constraint.MatchOption,
		"update_rule":               constraint.UpdateRule,
		"delete_rule":               constraint.DeleteRule,
		"table_catalog":             constraint.Database,
		"table_name":                constraint.Table,
		"referenced_table_catalog":  constraint.Database,
		"referenced_table_schema":   constraint.ReferencedSchema,
		"referenced_table_name":     constraint.ReferencedTable,
	}
}

type PostgresKeyColumnUsage struct {
	Database, Schema, Constraint, TableSchema, Table, Column string
	OrdinalPosition, PositionInUniqueConstraint              int
	ReferencedSchema, ReferencedTable, ReferencedColumn      string
}

func (usage PostgresKeyColumnUsage) metricGroupValue() models.MetricGroupValue {
	return models.MetricGroupValue{
		"constraint_catalog":            usage.Database,
		"constraint_schema":             usage.Schema,
		"constraint_name":               usage.Constraint,
		"table_catalog":                 usage.Database,
		"table_schema":                  usage.TableSchema,
		"table_name":                    usage.Table,
		"column_name":                   usage.Column,
		"ordinal_position":              usage.OrdinalPosition,
		"position_in_unique_constraint": usage.PositionInUniqueConstraint,
		"referenced_table_catalog":      usage.Database,
		"referenced_table_schema":       usage.ReferencedSchema,
		"referenced_table_name":         usage.ReferencedTable,
		"referenced_column_name":        usage.ReferencedColumn,
	}
}

type PostgresSequence struct {
	Database, Schema, Name, DataType            string
	StartValue, MinValue, MaxValue, IncrementBy string
	Cycle                                       bool
	CacheSize, LastValue                        *string
	OwnedTableSchema, OwnedTable, OwnedColumn   *string
	OwnedColumnDataType                         *string
}

func (sequence PostgresSequence) metricGroupValue() models.MetricGroupValue {
	return models.MetricGroupValue{
		"sequence_catalog":       sequence.Database,
		"sequence_schema":        sequence.Schema,
		"sequence_name":          sequence.Name,
		"data_type":              sequence.DataType,
		"start_value":            sequence.StartValue,
		"min_value":              sequence.MinValue,
		"max_value":              sequence.MaxValue,
		"increment_by":           sequence.IncrementBy,
		"cycle":                  sequence.Cycle,
		"cache_size":             nullableStringValue(sequence.CacheSize),
		"last_value":             nullableStringValue(sequence.LastValue),
		"owned_table_schema":     nullableStringValue(sequence.OwnedTableSchema),
		"owned_table":            nullableStringValue(sequence.OwnedTable),
		"owned_column":           nullableStringValue(sequence.OwnedColumn),
		"owned_column_data_type": nullableStringValue(sequence.OwnedColumnDataType),
	}
}

type postgresSchemaSectionCollector struct {
	name    string
	collect func(context.Context, *sql.DB, string, int) ([]postgresSchemaMetric, error)
}

type postgresSchemaCollectionError struct {
	sections []string
}

const postgresDatabaseConnectionFailureSection = "__database_connection__"

func (err *postgresSchemaCollectionError) Error() string {
	return fmt.Sprintf("PostgreSQL schema sections failed: %v", err.sections)
}

// func collectDatabaseSchema(configuration *config.Config, logger logging.Logger, database string, serverVersionNum int, metrics *models.Metrics) error {

func CollectDbSchema(configuration *config.Config, logger logging.Logger, database string, knownServerVersion int, metrics *models.Metrics) error {
	db := u.ConnectionDatabase(configuration, logger, database)
	if db == nil {
		return &postgresSchemaCollectionError{
			sections: []string{postgresDatabaseConnectionFailureSection},
		}
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	serverVersionNum := 120000
	if knownServerVersion > 0 {
		serverVersionNum = knownServerVersion
	} else if detectedVersion, err := detectPostgresServerVersion(ctx, db); err != nil {
		logger.Error("Unable to detect PostgreSQL server version for schema collection: ", err)
	} else {
		serverVersionNum = detectedVersion
	}
	failedSections := collectPostgresSchemaSections(ctx, db, database, serverVersionNum, logger, metrics, defaultPostgresSchemaCollectors())
	if len(failedSections) > 0 {
		return &postgresSchemaCollectionError{sections: failedSections}
	}
	return nil
}

func collectPostgresSchemaSections(ctx context.Context, db *sql.DB, database string, serverVersionNum int, logger logging.Logger, metrics *models.Metrics, collectors []postgresSchemaSectionCollector) []string {
	if metrics.DB.DatabaseSchema == nil {
		metrics.DB.DatabaseSchema = make(map[string][]models.MetricGroupValue)
	}
	failedSections := make([]string, 0)
	for _, collector := range collectors {
		rows, err := collector.collect(ctx, db, database, serverVersionNum)
		if err != nil {
			logger.Error("Unable to collect PostgreSQL schema section ", collector.name, " for database ", database, ": ", err)
			failedSections = append(failedSections, collector.name)
			continue
		}
		if _, exists := metrics.DB.DatabaseSchema[collector.name]; !exists {
			metrics.DB.DatabaseSchema[collector.name] = []models.MetricGroupValue{}
		}
		for _, row := range rows {
			metrics.DB.DatabaseSchema[collector.name] = append(metrics.DB.DatabaseSchema[collector.name], row.metricGroupValue())
		}
	}
	return failedSections
}

func defaultPostgresSchemaCollectors() []postgresSchemaSectionCollector {
	return []postgresSchemaSectionCollector{
		{name: "information_schema_tables", collect: collectPostgresTables},
		{name: "information_schema_columns", collect: collectPostgresColumns},
		{name: "information_schema_indexes", collect: collectPostgresIndexes},
		{name: "information_schema_referential_constraints", collect: collectPostgresReferentialConstraints},
		{name: "information_schema_key_column_usage", collect: collectPostgresKeyColumnUsages},
		{name: "pg_sequences", collect: collectPostgresSequences},
	}
}

func collectPostgresTables(ctx context.Context, db *sql.DB, database string, _ int) ([]postgresSchemaMetric, error) {
	query := fmt.Sprintf(`
SELECT t.table_schema, t.table_name, t.table_type, c.relkind::text,
	GREATEST(COALESCE(c.reltuples, 0), 0)::bigint,
	COALESCE(pg_table_size(c.oid), 0)::bigint,
	COALESCE(pg_indexes_size(c.oid), 0)::bigint,
	COALESCE(pg_total_relation_size(c.oid), 0)::bigint,
	COALESCE(s.n_mod_since_analyze, 0)::bigint,
	COALESCE(s.last_analyze::text, ''), COALESCE(s.last_autoanalyze::text, '')
FROM information_schema.tables t
JOIN pg_namespace n ON n.nspname = t.table_schema
JOIN pg_class c ON c.relname = t.table_name AND c.relnamespace = n.oid AND %s
LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid
WHERE t.table_catalog = $1 AND %s
ORDER BY t.table_schema, t.table_name`, pgTableRelkindPredicate("c.relkind"), pgUserSchemaPredicate("t.table_schema"))
	rows, err := queryContextRows(ctx, db, query, []interface{}{database}, func(rows *sql.Rows) (PostgresTable, error) {
		result := PostgresTable{Database: database}
		var lastAnalyze, lastAutoAnalyze string
		err := rows.Scan(&result.Schema, &result.Name, &result.Type, &result.Relkind, &result.EstimatedRows, &result.TableSizeBytes, &result.IndexSizeBytes, &result.TotalSizeBytes, &result.ModSinceAnalyze, &lastAnalyze, &lastAutoAnalyze)
		result.LastAnalyze = stringPointer(lastAnalyze)
		result.LastAutoAnalyze = stringPointer(lastAutoAnalyze)
		return result, err
	})
	return schemaMetricValues(rows), err
}

func collectPostgresColumns(ctx context.Context, db *sql.DB, database string, serverVersionNum int) ([]postgresSchemaMetric, error) {
	query := postgresColumnsQuery(serverVersionNum)
	rows, err := queryContextRows(ctx, db, query, []interface{}{database}, func(rows *sql.Rows) (PostgresColumn, error) {
		result := PostgresColumn{Database: database}
		var maxLength, precision, scale sql.NullInt64
		var generationExpression string
		err := rows.Scan(&result.Schema, &result.Table, &result.Name, &result.OrdinalPosition, &result.Default, &result.IsNullable, &result.DataType, &maxLength, &precision, &scale, &result.IsIdentity, &result.IsGenerated, &generationExpression)
		result.CharacterMaximumLength = nullInt64Pointer(maxLength)
		result.NumericPrecision = nullInt64Pointer(precision)
		result.NumericScale = nullInt64Pointer(scale)
		result.GenerationExpression = stringPointer(generationExpression)
		return result, err
	})
	return schemaMetricValues(rows), err
}

func postgresColumnsQuery(serverVersionNum int) string {
	// Identity columns are supported from PostgreSQL 10 and generated columns from PostgreSQL 12.
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

func collectPostgresIndexes(ctx context.Context, db *sql.DB, database string, serverVersionNum int) ([]postgresSchemaMetric, error) {
	query := postgresStructuredIndexQuery(serverVersionNum)
	rows, err := queryContextRows(ctx, db, query, nil, func(rows *sql.Rows) (PostgresIndex, error) {
		result := PostgresIndex{Database: database}
		var keysJSON, predicate, lastIdxScan, statsReset string
		err := rows.Scan(&result.Schema, &result.Table, &result.Name, &result.AccessMethod, &keysJSON, pq.Array(&result.IncludeColumns), &predicate, &result.Definition, &result.IsUnique, &result.IsPrimary, &result.IsValid, &result.IsReady, &result.IsExclusion, &result.NullsNotDistinct, &result.IsPartitioned, &result.IsAttachedPartition, &result.IsClustered, &result.IsReplicaIdentity, &result.RelationKind, &result.IdxScan, &lastIdxScan, &statsReset)
		if err == nil {
			err = json.Unmarshal([]byte(keysJSON), &result.Keys)
		}
		result.Predicate = predicate
		result.LastIdxScan = stringPointer(lastIdxScan)
		result.StatsReset = stringPointer(statsReset)
		return result, err
	})
	return schemaMetricValues(rows), err
}

func postgresStructuredIndexQuery(serverVersionNum int) string {
	// PostgreSQL 11 introduced INCLUDE indexes and split key attributes into indnkeyatts.
	keyAttributeCount := "idx.indnatts"
	if serverVersionNum >= 110000 {
		keyAttributeCount = "idx.indnkeyatts"
	}
	nullsNotDistinct := "false"
	if serverVersionNum >= 150000 {
		nullsNotDistinct = "idx.indnullsnotdistinct"
	}
	lastIdxScan := "''"
	if serverVersionNum >= 160000 {
		lastIdxScan = "COALESCE(stats.last_idx_scan::text, '')"
	}
	return fmt.Sprintf(`
SELECT namespace.nspname, table_class.relname, index_class.relname, access_method.amname,
	COALESCE((
		SELECT jsonb_agg(jsonb_build_object(
			'position', key_info.position,
			'column_name', CASE WHEN key_info.attnum = 0 THEN NULL ELSE attribute.attname END,
			'expression', CASE WHEN key_info.attnum = 0 THEN pg_get_indexdef(idx.indexrelid, key_info.position::integer, true) ELSE NULL END,
				'collation', collation_meta.collname,
				'opclass', opclass_meta.opcname,
				'collation_schema', collation_namespace.nspname,
				'opclass_schema', opclass_namespace.nspname,
			'collation_is_default', CASE
				WHEN COALESCE(collation_info.oid, 0) = 0 THEN true
				WHEN key_info.attnum <> 0 THEN collation_info.oid = attribute.attcollation
				ELSE false
			END,
			'opclass_is_default', COALESCE(opclass_meta.opcdefault, false),
			'descending', (COALESCE(option_info.option, 0) & 1) = 1,
			'nulls_first', (COALESCE(option_info.option, 0) & 2) = 2,
			'definition', pg_get_indexdef(idx.indexrelid, key_info.position::integer, true)
		) ORDER BY key_info.position)
		FROM unnest(idx.indkey) WITH ORDINALITY AS key_info(attnum, position)
		LEFT JOIN pg_attribute attribute ON attribute.attrelid = idx.indrelid AND attribute.attnum = key_info.attnum
		LEFT JOIN unnest(idx.indcollation) WITH ORDINALITY AS collation_info(oid, position) ON collation_info.position = key_info.position
		LEFT JOIN pg_collation collation_meta ON collation_meta.oid = collation_info.oid
		LEFT JOIN pg_namespace collation_namespace ON collation_namespace.oid = collation_meta.collnamespace
		LEFT JOIN unnest(idx.indclass) WITH ORDINALITY AS opclass_info(oid, position) ON opclass_info.position = key_info.position
		LEFT JOIN pg_opclass opclass_meta ON opclass_meta.oid = opclass_info.oid
		LEFT JOIN pg_namespace opclass_namespace ON opclass_namespace.oid = opclass_meta.opcnamespace
		LEFT JOIN unnest(idx.indoption) WITH ORDINALITY AS option_info(option, position) ON option_info.position = key_info.position
		WHERE key_info.position <= %s
	), '[]'::jsonb)::text,
	ARRAY(
		SELECT attribute.attname
		FROM unnest(idx.indkey) WITH ORDINALITY AS key_info(attnum, position)
		JOIN pg_attribute attribute ON attribute.attrelid = idx.indrelid AND attribute.attnum = key_info.attnum
		WHERE key_info.position > %s
		ORDER BY key_info.position
	),
	COALESCE(pg_get_expr(idx.indpred, idx.indrelid, true), ''), pg_get_indexdef(idx.indexrelid),
	idx.indisunique, idx.indisprimary, idx.indisvalid, idx.indisready, idx.indisexclusion,
	%s, index_class.relkind = 'I',
	EXISTS (SELECT 1 FROM pg_inherits inherited WHERE inherited.inhrelid = idx.indexrelid),
	idx.indisclustered, idx.indisreplident, index_class.relkind::text,
	COALESCE(stats.idx_scan, 0)::bigint, %s,
	COALESCE(database_stats.stats_reset::text, '')
FROM pg_index idx
JOIN pg_class index_class ON index_class.oid = idx.indexrelid
JOIN pg_class table_class ON table_class.oid = idx.indrelid
JOIN pg_namespace namespace ON namespace.oid = table_class.relnamespace
JOIN pg_am access_method ON access_method.oid = index_class.relam
LEFT JOIN pg_stat_user_indexes stats ON stats.indexrelid = idx.indexrelid
LEFT JOIN pg_stat_database database_stats ON database_stats.datname = current_database()
WHERE %s
ORDER BY namespace.nspname, table_class.relname, index_class.relname`,
		keyAttributeCount,
		keyAttributeCount,
		nullsNotDistinct,
		lastIdxScan,
		pgUserSchemaPredicate("namespace.nspname"),
	)
}

func collectPostgresReferentialConstraints(ctx context.Context, db *sql.DB, database string, _ int) ([]postgresSchemaMetric, error) {
	rows, err := queryContextRows(ctx, db, pgReferentialConstraintsQuery(), []interface{}{database}, func(rows *sql.Rows) (PostgresReferentialConstraint, error) {
		result := PostgresReferentialConstraint{Database: database}
		err := rows.Scan(&result.Schema, &result.Name, &result.UniqueSchema, &result.UniqueName, &result.MatchOption, &result.UpdateRule, &result.DeleteRule, &result.Table, &result.ReferencedTable)
		result.ReferencedSchema = result.UniqueSchema
		return result, err
	})
	return schemaMetricValues(rows), err
}

func collectPostgresKeyColumnUsages(ctx context.Context, db *sql.DB, database string, _ int) ([]postgresSchemaMetric, error) {
	rows, err := queryContextRows(ctx, db, pgKeyColumnUsageQuery(), []interface{}{database}, func(rows *sql.Rows) (PostgresKeyColumnUsage, error) {
		result := PostgresKeyColumnUsage{Database: database}
		err := rows.Scan(&result.Schema, &result.Constraint, &result.TableSchema, &result.Table, &result.Column, &result.OrdinalPosition, &result.PositionInUniqueConstraint, &result.ReferencedSchema, &result.ReferencedTable, &result.ReferencedColumn)
		return result, err
	})
	return schemaMetricValues(rows), err
}

func collectPostgresSequences(ctx context.Context, db *sql.DB, database string, serverVersionNum int) ([]postgresSchemaMetric, error) {
	if !pgSupportsSequencesView(serverVersionNum) {
		return nil, nil
	}
	rows, err := queryContextRows(ctx, db, pgSequenceSchemaQuery(), nil, func(rows *sql.Rows) (PostgresSequence, error) {
		result := PostgresSequence{Database: database}
		var cacheSize, lastValue, ownedSchema, ownedTable, ownedColumn, ownedType string
		err := rows.Scan(&result.Schema, &result.Name, &result.DataType, &result.StartValue, &result.MinValue, &result.MaxValue, &result.IncrementBy, &result.Cycle, &cacheSize, &lastValue, &ownedSchema, &ownedTable, &ownedColumn, &ownedType)
		result.CacheSize = nullMarkerStringPointer(cacheSize)
		result.LastValue = nullMarkerStringPointer(lastValue)
		result.OwnedTableSchema = nullMarkerStringPointer(ownedSchema)
		result.OwnedTable = nullMarkerStringPointer(ownedTable)
		result.OwnedColumn = nullMarkerStringPointer(ownedColumn)
		result.OwnedColumnDataType = nullMarkerStringPointer(ownedType)
		return result, err
	})
	return schemaMetricValues(rows), err
}

func schemaMetricValues[T postgresSchemaMetric](values []T) []postgresSchemaMetric {
	result := make([]postgresSchemaMetric, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func nullMarkerStringPointer(value string) *string {
	if value == "" || value == "NULL" {
		return nil
	}
	return &value
}

func nullInt64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

func nullableStringValue(value *string) interface{} {
	if value == nil {
		return nil
	}
	return *value
}

func nullableNonEmptyString(value string) interface{} {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt64Value(value *int64) interface{} {
	if value == nil {
		return nil
	}
	return *value
}

func pgSupportsSequencesView(serverVersionNum int) bool {
	return serverVersionNum >= 100000
}

func pgUserSchemaPredicate(schemaNameExpr string) string {
	return fmt.Sprintf("%s NOT IN ('information_schema', 'pg_catalog') AND %s NOT LIKE 'pg_toast%%' AND %s NOT LIKE 'pg_temp_%%'", schemaNameExpr, schemaNameExpr, schemaNameExpr)
}

func pgTableRelkindPredicate(relkindExpr string) string {
	return fmt.Sprintf("%s IN ('r', 'p', 'v', 'm', 'f')", relkindExpr)
}

func pgReferentialConstraintsQuery() string {
	return fmt.Sprintf(`
		SELECT
			child_ns.nspname AS constraint_schema,
			con.conname AS constraint_name,
			referenced_ns.nspname AS unique_constraint_schema,
			COALESCE(unique_con.conname, referenced_index.relname, 'NULL') AS unique_constraint_name,
			CASE con.confmatchtype
				WHEN 'f' THEN 'FULL'
				WHEN 'p' THEN 'PARTIAL'
				ELSE 'NONE'
			END AS match_option,
			CASE con.confupdtype
				WHEN 'a' THEN 'NO ACTION'
				WHEN 'r' THEN 'RESTRICT'
				WHEN 'c' THEN 'CASCADE'
				WHEN 'n' THEN 'SET NULL'
				WHEN 'd' THEN 'SET DEFAULT'
			END AS update_rule,
			CASE con.confdeltype
				WHEN 'a' THEN 'NO ACTION'
				WHEN 'r' THEN 'RESTRICT'
				WHEN 'c' THEN 'CASCADE'
				WHEN 'n' THEN 'SET NULL'
				WHEN 'd' THEN 'SET DEFAULT'
			END AS delete_rule,
			child.relname AS table_name,
			referenced.relname AS referenced_table_name
		FROM pg_constraint con
		JOIN pg_class child ON child.oid = con.conrelid
		JOIN pg_namespace child_ns ON child_ns.oid = child.relnamespace
		JOIN pg_class referenced ON referenced.oid = con.confrelid
		JOIN pg_namespace referenced_ns ON referenced_ns.oid = referenced.relnamespace
		LEFT JOIN pg_class referenced_index ON referenced_index.oid = con.conindid
		LEFT JOIN pg_constraint unique_con
			ON unique_con.conindid = con.conindid
			AND unique_con.conrelid = con.confrelid
			AND unique_con.contype IN ('p', 'u')
		WHERE con.contype = 'f'
			AND current_database() = $1
			AND %s
		ORDER BY child_ns.nspname, child.relname, con.conname`, pgUserSchemaPredicate("child_ns.nspname"))
}

func pgKeyColumnUsageQuery() string {
	return fmt.Sprintf(`
		SELECT
			child_ns.nspname AS constraint_schema,
			con.conname AS constraint_name,
			child_ns.nspname AS table_schema,
			child.relname AS table_name,
			child_column.attname AS column_name,
			key_position.position::text AS ordinal_position,
			COALESCE(
				array_position(unique_con.conkey, con.confkey[key_position.position]),
				key_position.position
			)::text AS position_in_unique_constraint,
			referenced_ns.nspname AS referenced_table_schema,
			referenced.relname AS referenced_table_name,
			referenced_column.attname AS referenced_column_name
		FROM pg_constraint con
		JOIN pg_class child ON child.oid = con.conrelid
		JOIN pg_namespace child_ns ON child_ns.oid = child.relnamespace
		JOIN pg_class referenced ON referenced.oid = con.confrelid
		JOIN pg_namespace referenced_ns ON referenced_ns.oid = referenced.relnamespace
		LEFT JOIN pg_constraint unique_con
			ON unique_con.conindid = con.conindid
			AND unique_con.conrelid = con.confrelid
			AND unique_con.contype IN ('p', 'u')
		JOIN LATERAL generate_subscripts(con.conkey, 1) AS key_position(position) ON true
		JOIN pg_attribute child_column
			ON child_column.attrelid = con.conrelid
			AND child_column.attnum = con.conkey[key_position.position]
		JOIN pg_attribute referenced_column
			ON referenced_column.attrelid = con.confrelid
			AND referenced_column.attnum = con.confkey[key_position.position]
		WHERE con.contype = 'f'
			AND current_database() = $1
			AND %s
		ORDER BY child_ns.nspname, child.relname, con.conname, key_position.position`, pgUserSchemaPredicate("child_ns.nspname"))
}

func pgSequenceSchemaQuery() string {
	return fmt.Sprintf(`
		SELECT
			s.schemaname,
			s.sequencename,
			COALESCE(s.data_type::text, 'NULL') AS data_type,
			COALESCE(s.start_value::text, 'NULL') AS start_value,
			COALESCE(s.min_value::text, 'NULL') AS min_value,
			COALESCE(s.max_value::text, 'NULL') AS max_value,
			COALESCE(s.increment_by::text, 'NULL') AS increment_by,
			COALESCE(s.cycle::text, 'false') AS cycle,
			COALESCE(s.cache_size::text, 'NULL') AS cache_size,
			COALESCE(s.last_value::text, 'NULL') AS last_value,
			COALESCE(owned_table_ns.nspname, 'NULL') AS owned_table_schema,
			COALESCE(owned_table.relname, 'NULL') AS owned_table_name,
			COALESCE(owned_column.attname, 'NULL') AS owned_column_name,
			COALESCE(format_type(owned_column.atttypid, owned_column.atttypmod), 'NULL') AS owned_column_data_type
		FROM pg_sequences s
		LEFT JOIN pg_namespace sequence_ns
			ON sequence_ns.nspname = s.schemaname
		LEFT JOIN pg_class sequence_class
			ON sequence_class.relnamespace = sequence_ns.oid
			AND sequence_class.relname = s.sequencename
			AND sequence_class.relkind = 'S'
			LEFT JOIN pg_depend sequence_owner_dep
				ON sequence_owner_dep.classid = 'pg_class'::regclass
				AND sequence_owner_dep.refclassid = 'pg_class'::regclass
				AND sequence_owner_dep.objid = sequence_class.oid
			AND sequence_owner_dep.objsubid = 0
			AND sequence_owner_dep.deptype IN ('a', 'i')
		LEFT JOIN pg_class owned_table
			ON owned_table.oid = sequence_owner_dep.refobjid
		LEFT JOIN pg_namespace owned_table_ns
			ON owned_table_ns.oid = owned_table.relnamespace
		LEFT JOIN pg_attribute owned_column
			ON owned_column.attrelid = owned_table.oid
			AND owned_column.attnum = sequence_owner_dep.refobjsubid
		WHERE %s
		ORDER BY s.schemaname, s.sequencename`, pgUserSchemaPredicate("s.schemaname"))
}
