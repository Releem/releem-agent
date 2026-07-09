package postgresql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Releem/mysqlconfigurer/models"
	u "github.com/Releem/mysqlconfigurer/utils"
	"github.com/hashicorp/go-version"

	"github.com/Releem/mysqlconfigurer/config"
	logging "github.com/google/logger"
)

type DBCollectQueriesOptimization struct {
	logger        logging.Logger
	configuration *config.Config
}

func NewDBCollectQueriesOptimization(logger logging.Logger, configuration *config.Config) *DBCollectQueriesOptimization {
	return &DBCollectQueriesOptimization{
		logger:        logger,
		configuration: configuration,
	}
}

func (DBCollectQueriesOptimization *DBCollectQueriesOptimization) GetMetrics(metrics *models.Metrics) error {
	defer u.HandlePanic(DBCollectQueriesOptimization.configuration, DBCollectQueriesOptimization.logger)

	if !models.PgStatStatementsEnabled {
		DBCollectQueriesOptimization.logger.Info("pg_stat_statements extension is not installed, skipping query collection")
		metrics.DB.Queries = nil
		return nil
	}

	var queryid, datname, query string
	var calls int
	var rowsSent uint64
	var total_exec_time, mean_exec_time float64
	output_digest := make(map[string]models.MetricGroupValue)
	var output []models.MetricGroupValue

	ver_current, _ := version.NewVersion(metrics.DB.Info["Version"].(string))
	ver_plan_cache_mode, _ := version.NewVersion("12")
	supportsParameterizedExplain := !ver_current.LessThan(ver_plan_cache_mode)
	supportsRows := DetectPgStatStatementsSupportsRows(models.DB, DBCollectQueriesOptimization.logger)
	pgStatStatements := PgStatStatementsQuery(ver_current, supportsRows)

	// Collect query statistics from pg_stat_statements
	rows, err := models.DB.Query(pgStatStatements)

	if err != nil {
		if err != sql.ErrNoRows {
			DBCollectQueriesOptimization.logger.Error(err)
		}
	} else {
		defer rows.Close()
		for rows.Next() {
			err := rows.Scan(&datname, &queryid, &query, &calls, &total_exec_time, &mean_exec_time, &rowsSent)
			if err != nil {
				DBCollectQueriesOptimization.logger.Error(err)
				return err
			}
			queryid = normalizePgQueryID(queryid)

			// Convert to microseconds for compatibility with MySQL metrics
			total_exec_time_us := total_exec_time * 1000
			mean_exec_time_us := mean_exec_time * 1000
			output_digest[datname+queryid] = pgQueryMetric(datname, queryid, query, calls, total_exec_time_us, mean_exec_time_us, rowsSent)
		}
	}

	if DBCollectQueriesOptimization.configuration.QueryOptimization {
		CollectExplain(output_digest, "total_exec_time_us", supportsParameterizedExplain, DBCollectQueriesOptimization.logger, DBCollectQueriesOptimization.configuration)
		CollectExplain(output_digest, "mean_exec_time_us", supportsParameterizedExplain, DBCollectQueriesOptimization.logger, DBCollectQueriesOptimization.configuration)
	}
	for _, value := range output_digest {
		output = append(output, value)
	}
	metrics.DB.Queries = output

	if !DBCollectQueriesOptimization.configuration.QueryOptimization {
		return nil
	}

	metrics.DB.DatabaseSchema = make(map[string][]models.MetricGroupValue)
	i := 0
	for _, database := range metrics.DB.Metrics.Databases {
		if u.IsSchemaNameExclude(database, DBCollectQueriesOptimization.configuration.DatabasesQueryOptimization) {
			continue
		}
		if err := collectDatabaseSchema(DBCollectQueriesOptimization.configuration, DBCollectQueriesOptimization.logger, database, metrics); err != nil {
			DBCollectQueriesOptimization.logger.Error(err)
			continue
		}

		i += 1
		if i%25 == 0 {
			time.Sleep(3 * time.Second)
		}
	}
	DBCollectQueriesOptimization.logger.V(5).Info("collectMetrics ", metrics.DB.Queries)
	DBCollectQueriesOptimization.logger.V(5).Info("collectMetrics ", metrics.DB.DatabaseSchema)

	return nil
}

func pgQueryMetric(datname, queryid, query string, calls int, totalExecTimeUs, meanExecTimeUs float64, rowsSent uint64) models.MetricGroupValue {
	metric := pgQueryMetricLatency(datname, queryid, calls, totalExecTimeUs, meanExecTimeUs, rowsSent)
	metric["query"] = query
	metric["query_text"] = query
	return metric
}

func pgQueryMetricLatency(datname, queryid string, calls int, totalExecTimeUs, meanExecTimeUs float64, rowsSent uint64) models.MetricGroupValue {
	return models.MetricGroupValue{
		"datname":            datname,
		"queryid":            queryid,
		"calls":              calls,
		"total_exec_time_us": totalExecTimeUs,
		"mean_exec_time_us":  meanExecTimeUs,
		"SUM_ROWS_SENT":      rowsSent,
	}
}

func collectDatabaseSchema(configuration *config.Config, logger logging.Logger, database string, metrics *models.Metrics) error {
	db := u.ConnectionDatabase(configuration, logger, database)
	if db == nil {
		return fmt.Errorf("failed to connect to database %s", database)
	}
	defer db.Close()
	return CollectDbSchema(db, database, logger, metrics)
}

type pgTableSchemaMetricInput struct {
	TABLE_CATALOG       string
	TABLE_SCHEMA        string
	TABLE_NAME          string
	TABLE_TYPE          string
	ENGINE              string
	TABLE_ROWS          string
	AVG_ROW_LENGTH      string
	DATA_LENGTH         string
	INDEX_LENGTH        string
	TABLE_COLLATION     string
	TABLE_SIZE_BYTES    string
	N_MOD_SINCE_ANALYZE string
	LAST_ANALYZE        string
	LAST_AUTOANALYZE    string
}

func pgTableSchemaMetric(input pgTableSchemaMetricInput) models.MetricGroupValue {
	return models.MetricGroupValue{
		"TABLE_CATALOG":       input.TABLE_CATALOG,
		"TABLE_SCHEMA":        input.TABLE_SCHEMA,
		"TABLE_NAME":          input.TABLE_NAME,
		"TABLE_TYPE":          input.TABLE_TYPE,
		"ENGINE":              input.ENGINE,
		"ROW_FORMAT":          "NULL",
		"TABLE_ROWS":          input.TABLE_ROWS,
		"AVG_ROW_LENGTH":      input.AVG_ROW_LENGTH,
		"MAX_DATA_LENGTH":     "NULL",
		"DATA_LENGTH":         input.DATA_LENGTH,
		"INDEX_LENGTH":        input.INDEX_LENGTH,
		"TABLE_COLLATION":     input.TABLE_COLLATION,
		"DATA_FREE":           "0",
		"TABLE_SIZE_BYTES":    input.TABLE_SIZE_BYTES,
		"N_MOD_SINCE_ANALYZE": input.N_MOD_SINCE_ANALYZE,
		"LAST_ANALYZE":        input.LAST_ANALYZE,
		"LAST_AUTOANALYZE":    input.LAST_AUTOANALYZE,
	}
}

func pgColumnSchemaMetric(tableCatalog, tableSchema, tableName, columnName, ordinalPosition, columnDefault, isNullable, dataType, characterMaximumLength, numericPrecision, numericScale, characterSetName string) models.MetricGroupValue {
	return models.MetricGroupValue{
		"TABLE_CATALOG":            tableCatalog,
		"TABLE_SCHEMA":             tableSchema,
		"TABLE_NAME":               tableName,
		"COLUMN_NAME":              columnName,
		"ORDINAL_POSITION":         ordinalPosition,
		"COLUMN_DEFAULT":           columnDefault,
		"IS_NULLABLE":              isNullable,
		"DATA_TYPE":                dataType,
		"CHARACTER_MAXIMUM_LENGTH": characterMaximumLength,
		"NUMERIC_PRECISION":        numericPrecision,
		"NUMERIC_SCALE":            numericScale,
		"CHARACTER_SET_NAME":       characterSetName,
		"COLLATION_NAME":           "NULL",
		"COLUMN_TYPE":              dataType,
		"COLUMN_KEY":               "NULL",
		"EXTRA":                    "NULL",
		"GENERATION_EXPRESSION":    "NULL",
	}
}

type pgIndexSchemaMetricInput struct {
	TABLE_CATALOG     string
	TABLE_SCHEMA      string
	TABLE_NAME        string
	INDEX_NAME        string
	NON_UNIQUE        string
	SEQ_IN_INDEX      string
	COLUMN_NAME       string
	COLLATION         string
	CARDINALITY       string
	SUB_PART          string
	PACKED            string
	NULLABLE          string
	INDEX_TYPE        string
	EXPRESSION        string
	PREDICATE         string
	PG_RELAM          string
	PG_INDKEY         string
	PG_INDCLASS       string
	PG_INDCOLLATION   string
	PG_INDOPTION      string
	PG_INDNKEYATTS    string
	PG_INDNATTS       string
	PG_INDEXPRS       string
	PG_INDPRED        string
	PG_INDISVALID     string
	PG_INDISREADY     string
	PG_INDISEXCLUSION string
	LAST_IDX_SCAN     string
}

func pgIndexSchemaMetric(input pgIndexSchemaMetricInput) models.MetricGroupValue {
	expression := normalizePgAbsentValue(input.EXPRESSION)
	predicate := normalizePgAbsentValue(input.PREDICATE)
	columnName := input.COLUMN_NAME
	if columnName == "NULL" && expression != "" {
		columnName = expression
	}
	return models.MetricGroupValue{
		"TABLE_CATALOG":     input.TABLE_CATALOG,
		"TABLE_SCHEMA":      input.TABLE_SCHEMA,
		"TABLE_NAME":        input.TABLE_NAME,
		"INDEX_NAME":        input.INDEX_NAME,
		"NON_UNIQUE":        input.NON_UNIQUE,
		"SEQ_IN_INDEX":      input.SEQ_IN_INDEX,
		"COLUMN_NAME":       columnName,
		"COLLATION":         input.COLLATION,
		"CARDINALITY":       input.CARDINALITY,
		"SUB_PART":          input.SUB_PART,
		"PACKED":            input.PACKED,
		"NULLABLE":          input.NULLABLE,
		"INDEX_TYPE":        input.INDEX_TYPE,
		"EXPRESSION":        expression,
		"PREDICATE":         predicate,
		"PG_RELAM":          input.PG_RELAM,
		"PG_INDKEY":         input.PG_INDKEY,
		"PG_INDCLASS":       input.PG_INDCLASS,
		"PG_INDCOLLATION":   input.PG_INDCOLLATION,
		"PG_INDOPTION":      input.PG_INDOPTION,
		"PG_INDNKEYATTS":    input.PG_INDNKEYATTS,
		"PG_INDNATTS":       input.PG_INDNATTS,
		"PG_INDEXPRS":       input.PG_INDEXPRS,
		"PG_INDPRED":        input.PG_INDPRED,
		"PG_INDISVALID":     input.PG_INDISVALID,
		"PG_INDISREADY":     input.PG_INDISREADY,
		"PG_INDISEXCLUSION": input.PG_INDISEXCLUSION,
		"LAST_IDX_SCAN":     input.LAST_IDX_SCAN,
	}
}

func pgSequenceSchemaMetric(tableCatalog, sequenceSchema, sequenceName, lastValue, maxValue, incrementBy string) models.MetricGroupValue {
	return models.MetricGroupValue{
		"TABLE_CATALOG":   tableCatalog,
		"SEQUENCE_SCHEMA": sequenceSchema,
		"SEQUENCE_NAME":   sequenceName,
		"LAST_VALUE":      lastValue,
		"MAX_VALUE":       maxValue,
		"INCREMENT_BY":    incrementBy,
	}
}

func commitPgDatabaseSchema(metrics *models.Metrics, partial map[string][]models.MetricGroupValue) {
	if metrics.DB.DatabaseSchema == nil {
		metrics.DB.DatabaseSchema = make(map[string][]models.MetricGroupValue)
	}
	for key, values := range partial {
		metrics.DB.DatabaseSchema[key] = append(metrics.DB.DatabaseSchema[key], values...)
	}
}

func pgIndexVectorExpression(column string) string {
	return column + "::text"
}

func pgIndexSchemaQuery(database string) string {
	_ = database
	return pgIndexSchemaQueryForVersion(180000)
}

func pgIndexSchemaQueryForVersion(serverVersionNum int) string {
	indnkeyatts, indnatts := pgIndexAttributeCountExpressions(serverVersionNum)
	return fmt.Sprintf(`
		SELECT
			n.nspname AS table_schema,
			tc.relname AS table_name,
			ic.relname AS index_name,
			CASE WHEN idx.indisunique THEN '0' ELSE '1' END AS non_unique,
			key_info.seq_in_index::text AS seq_in_index,
			COALESCE(
				a.attname,
				CASE
					WHEN key_info.attnum <= 0 THEN pg_get_indexdef(idx.indexrelid, key_info.seq_in_index::integer, true)
					ELSE NULL
				END,
				'NULL'
			) AS column_name,
			'NULL' AS collation,
			COALESCE(sui.idx_scan::text, '0') AS cardinality,
			'NULL' AS sub_part,
			'NULL' AS packed,
			CASE
				WHEN key_info.attnum > 0 AND a.attnotnull THEN 'NO'
				ELSE 'YES'
			END AS nullable,
			am.amname AS index_type,
			COALESCE(
				CASE
					WHEN key_info.attnum <= 0 THEN pg_get_indexdef(idx.indexrelid, key_info.seq_in_index::integer, true)
					ELSE NULL
				END,
				''
			) AS expression,
			COALESCE(pg_get_expr(idx.indpred, idx.indrelid, true), '') AS predicate,
			am.amname AS pg_relam,
			%s AS pg_indkey,
			%s AS pg_indclass,
			%s AS pg_indcollation,
			%s AS pg_indoption,
			%s AS pg_indnkeyatts,
			%s AS pg_indnatts,
			COALESCE(idx.indexprs::text, '') AS pg_indexprs,
			COALESCE(idx.indpred::text, '') AS pg_indpred,
			idx.indisvalid::text AS pg_indisvalid,
			idx.indisready::text AS pg_indisready,
			idx.indisexclusion::text AS pg_indisexclusion,
			%s AS last_idx_scan
		FROM pg_index idx
		JOIN pg_class ic ON ic.oid = idx.indexrelid
		JOIN pg_class tc ON tc.oid = idx.indrelid
		JOIN pg_namespace n ON n.oid = tc.relnamespace
		JOIN pg_am am ON am.oid = ic.relam
		LEFT JOIN pg_stat_user_indexes sui ON sui.indexrelid = idx.indexrelid
		CROSS JOIN LATERAL unnest(idx.indkey) WITH ORDINALITY AS key_info(attnum, seq_in_index)
		LEFT JOIN pg_attribute a ON a.attrelid = idx.indrelid AND a.attnum = key_info.attnum AND key_info.attnum > 0
		WHERE %s
			AND current_database() = $1
			AND key_info.seq_in_index <= %s
		ORDER BY n.nspname, tc.relname, ic.relname, key_info.seq_in_index`,
		pgIndexVectorExpression("idx.indkey"),
		pgIndexVectorExpression("idx.indclass"),
		pgIndexVectorExpression("idx.indcollation"),
		pgIndexVectorExpression("idx.indoption"),
		indnkeyatts,
		indnatts,
		pgIndexLastScanExpression(serverVersionNum),
		pgUserSchemaPredicate("n.nspname"),
		pgIndexKeyAttributeLimitExpression(serverVersionNum),
	)
}

func pgIndexAttributeCountExpressions(serverVersionNum int) (string, string) {
	if serverVersionNum >= 110000 {
		return "idx.indnkeyatts::text", "idx.indnatts::text"
	}
	countExpression := "array_length(idx.indkey::int2[], 1)::text"
	return countExpression, countExpression
}

func pgIndexKeyAttributeLimitExpression(serverVersionNum int) string {
	if serverVersionNum >= 110000 {
		return "idx.indnkeyatts"
	}
	return "array_length(idx.indkey::int2[], 1)"
}

func pgSupportsSequencesView(serverVersionNum int) bool {
	return serverVersionNum >= 100000
}

func pgIndexLastScanExpression(serverVersionNum int) string {
	if serverVersionNum >= 160000 {
		return "COALESCE(sui.last_idx_scan::text, 'NULL')"
	}
	return "'NULL'"
}

func pgUserSchemaPredicate(schemaNameExpr string) string {
	return fmt.Sprintf("%s NOT IN ('information_schema', 'pg_catalog') AND %s NOT LIKE 'pg_toast%%' AND %s NOT LIKE 'pg_temp_%%'", schemaNameExpr, schemaNameExpr, schemaNameExpr)
}

func pgTableRelkindPredicate(relkindExpr string) string {
	return fmt.Sprintf("%s IN ('r', 'p', 'v', 'm', 'f')", relkindExpr)
}

func pgServerVersionNum(db *sql.DB, logger logging.Logger) int {
	var versionString string
	if err := db.QueryRow("SHOW server_version_num").Scan(&versionString); err != nil {
		logger.Error(err)
		return 0
	}
	versionNum, err := strconv.Atoi(versionString)
	if err != nil {
		logger.Error(err)
		return 0
	}
	return versionNum
}

func CollectDbSchema(db *sql.DB, database string, logger logging.Logger, metrics *models.Metrics) error {
	if db == nil {
		return fmt.Errorf("database connection is nil for %s", database)
	}
	serverVersionNum := pgServerVersionNum(db, logger)
	partial := make(map[string][]models.MetricGroupValue)

	// Collect table information from information_schema
	var information_schema_table pgTableSchemaMetricInput

	tableSchemaQuery := fmt.Sprintf(`
		SELECT
			t.table_schema,
			t.table_name,
			t.table_type,
			'HEAP' AS engine,
			COALESCE(s.n_live_tup::text, '0') AS table_rows,
			COALESCE(
				CASE
					WHEN COALESCE(s.n_live_tup, 0) > 0
					THEN (pg_table_size(c.oid) / NULLIF(s.n_live_tup, 0))::bigint::text
					ELSE '0'
				END,
				'0'
			) AS avg_row_length,
			COALESCE(pg_table_size(c.oid)::text, '0') AS data_length,
			COALESCE(pg_indexes_size(c.oid)::text, '0') AS index_length,
			'NULL' AS table_collation,
			COALESCE(pg_total_relation_size(c.oid)::text, '0') AS table_size_bytes,
			COALESCE(s.n_mod_since_analyze::text, '0') AS n_mod_since_analyze,
			COALESCE(s.last_analyze::text, 'NULL') AS last_analyze,
			COALESCE(s.last_autoanalyze::text, 'NULL') AS last_autoanalyze
		FROM information_schema.tables t
		LEFT JOIN pg_namespace n ON n.nspname = t.table_schema
		LEFT JOIN pg_class c ON c.relname = t.table_name AND c.relnamespace = n.oid AND %s
		LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid
		WHERE t.table_catalog = $1
			AND %s
		ORDER BY t.table_schema, t.table_name`, pgTableRelkindPredicate("c.relkind"), pgUserSchemaPredicate("t.table_schema"))
	rows, err := db.Query(tableSchemaQuery, database)
	if err != nil {
		logger.Error(err)
		return err
	}
	for rows.Next() {
		err := rows.Scan(&information_schema_table.TABLE_SCHEMA, &information_schema_table.TABLE_NAME, &information_schema_table.TABLE_TYPE, &information_schema_table.ENGINE, &information_schema_table.TABLE_ROWS, &information_schema_table.AVG_ROW_LENGTH, &information_schema_table.DATA_LENGTH, &information_schema_table.INDEX_LENGTH, &information_schema_table.TABLE_COLLATION, &information_schema_table.TABLE_SIZE_BYTES, &information_schema_table.N_MOD_SINCE_ANALYZE, &information_schema_table.LAST_ANALYZE, &information_schema_table.LAST_AUTOANALYZE)
		if err != nil {
			rows.Close()
			logger.Error(err)
			return err
		}
		information_schema_table.TABLE_CATALOG = database
		partial["information_schema_tables"] = append(
			partial["information_schema_tables"],
			pgTableSchemaMetric(information_schema_table))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		logger.Error(err)
		return err
	}
	if err := rows.Close(); err != nil {
		logger.Error(err)
		return err
	}

	// Collect column information
	type information_schema_column_type struct {
		TABLE_SCHEMA             string
		TABLE_NAME               string
		COLUMN_NAME              string
		ORDINAL_POSITION         string
		COLUMN_DEFAULT           string
		IS_NULLABLE              string
		DATA_TYPE                string
		CHARACTER_MAXIMUM_LENGTH string
		NUMERIC_PRECISION        string
		NUMERIC_SCALE            string
		CHARACTER_SET_NAME       string
	}
	var information_schema_column information_schema_column_type

	columnSchemaQuery := fmt.Sprintf(`
		SELECT
			table_schema,
			table_name,
			column_name,
			ordinal_position::text,
			COALESCE(column_default, 'NULL') AS column_default,
			is_nullable,
			data_type,
			COALESCE(character_maximum_length::text, 'NULL') AS character_maximum_length,
			COALESCE(numeric_precision::text, 'NULL') AS numeric_precision,
			COALESCE(numeric_scale::text, 'NULL') AS numeric_scale,
			'NULL' AS character_set_name
		FROM information_schema.columns 
		WHERE table_catalog = $1
			AND %s
		ORDER BY table_schema, table_name, ordinal_position`, pgUserSchemaPredicate("table_schema"))
	rows, err = db.Query(columnSchemaQuery, database)
	if err != nil {
		logger.Error(err)
		return err
	}
	for rows.Next() {
		err := rows.Scan(&information_schema_column.TABLE_SCHEMA, &information_schema_column.TABLE_NAME,
			&information_schema_column.COLUMN_NAME, &information_schema_column.ORDINAL_POSITION,
			&information_schema_column.COLUMN_DEFAULT, &information_schema_column.IS_NULLABLE, &information_schema_column.DATA_TYPE,
			&information_schema_column.CHARACTER_MAXIMUM_LENGTH, &information_schema_column.NUMERIC_PRECISION,
			&information_schema_column.NUMERIC_SCALE, &information_schema_column.CHARACTER_SET_NAME)
		if err != nil {
			rows.Close()
			logger.Error(err)
			return err
		}
		partial["information_schema_columns"] = append(
			partial["information_schema_columns"],
			pgColumnSchemaMetric(database, information_schema_column.TABLE_SCHEMA, information_schema_column.TABLE_NAME, information_schema_column.COLUMN_NAME, information_schema_column.ORDINAL_POSITION, information_schema_column.COLUMN_DEFAULT, information_schema_column.IS_NULLABLE, information_schema_column.DATA_TYPE, information_schema_column.CHARACTER_MAXIMUM_LENGTH, information_schema_column.NUMERIC_PRECISION, information_schema_column.NUMERIC_SCALE, information_schema_column.CHARACTER_SET_NAME))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		logger.Error(err)
		return err
	}
	if err := rows.Close(); err != nil {
		logger.Error(err)
		return err
	}

	var information_schema_index pgIndexSchemaMetricInput
	rows, err = db.Query(pgIndexSchemaQueryForVersion(serverVersionNum), database)
	if err != nil {
		logger.Error(err)
		return err
	}
	for rows.Next() {
		err := rows.Scan(&information_schema_index.TABLE_SCHEMA, &information_schema_index.TABLE_NAME,
			&information_schema_index.INDEX_NAME, &information_schema_index.NON_UNIQUE,
			&information_schema_index.SEQ_IN_INDEX, &information_schema_index.COLUMN_NAME,
			&information_schema_index.COLLATION, &information_schema_index.CARDINALITY,
			&information_schema_index.SUB_PART, &information_schema_index.PACKED,
			&information_schema_index.NULLABLE, &information_schema_index.INDEX_TYPE,
			&information_schema_index.EXPRESSION, &information_schema_index.PREDICATE,
			&information_schema_index.PG_RELAM, &information_schema_index.PG_INDKEY,
			&information_schema_index.PG_INDCLASS, &information_schema_index.PG_INDCOLLATION,
			&information_schema_index.PG_INDOPTION, &information_schema_index.PG_INDNKEYATTS,
			&information_schema_index.PG_INDNATTS, &information_schema_index.PG_INDEXPRS,
			&information_schema_index.PG_INDPRED, &information_schema_index.PG_INDISVALID,
			&information_schema_index.PG_INDISREADY, &information_schema_index.PG_INDISEXCLUSION,
			&information_schema_index.LAST_IDX_SCAN)
		if err != nil {
			rows.Close()
			logger.Error(err)
			return err
		}
		information_schema_index.TABLE_CATALOG = database
		partial["information_schema_indexes"] = append(
			partial["information_schema_indexes"],
			pgIndexSchemaMetric(information_schema_index))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		logger.Error(err)
		return err
	}
	if err := rows.Close(); err != nil {
		logger.Error(err)
		return err
	}

	type pg_sequence_type struct {
		SEQUENCE_SCHEMA string
		SEQUENCE_NAME   string
		LAST_VALUE      string
		MAX_VALUE       string
		INCREMENT_BY    string
	}
	var pg_sequence pg_sequence_type
	if !pgSupportsSequencesView(serverVersionNum) {
		commitPgDatabaseSchema(metrics, partial)
		logger.V(5).Info("skip pg_sequences collection because PostgreSQL server_version_num=", serverVersionNum)
		logger.V(5).Info("collectMetrics ", metrics.DB.DatabaseSchema)
		return nil
	}
	sequenceQuery := fmt.Sprintf(`
		SELECT
			s.schemaname,
			s.sequencename,
			COALESCE(s.last_value::text, '0') AS last_value,
			COALESCE(s.max_value::text, '0') AS max_value,
			COALESCE(s.increment_by::text, '1') AS increment_by
		FROM pg_sequences s
		WHERE %s
		ORDER BY s.schemaname, s.sequencename`, pgUserSchemaPredicate("s.schemaname"))
	rows, err = db.Query(sequenceQuery)
	if err != nil {
		logger.Error(err)
		return err
	}
	for rows.Next() {
		err := rows.Scan(&pg_sequence.SEQUENCE_SCHEMA, &pg_sequence.SEQUENCE_NAME, &pg_sequence.LAST_VALUE, &pg_sequence.MAX_VALUE, &pg_sequence.INCREMENT_BY)
		if err != nil {
			rows.Close()
			logger.Error(err)
			return err
		}
		partial["pg_sequences"] = append(
			partial["pg_sequences"],
			pgSequenceSchemaMetric(database, pg_sequence.SEQUENCE_SCHEMA, pg_sequence.SEQUENCE_NAME, pg_sequence.LAST_VALUE, pg_sequence.MAX_VALUE, pg_sequence.INCREMENT_BY))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		logger.Error(err)
		return err
	}
	if err := rows.Close(); err != nil {
		logger.Error(err)
		return err
	}

	commitPgDatabaseSchema(metrics, partial)
	logger.V(5).Info("collectMetrics ", metrics.DB.DatabaseSchema)

	return nil
}

// PostgreSQL version of CollectionExplain - uses EXPLAIN (FORMAT JSON)
func CollectExplain(digests map[string]models.MetricGroupValue, field_sorting string, supportsParameterizedExplain bool, logger logging.Logger, configuration *config.Config) {
	var schema_name_conn string
	var searchPathSchemas []string
	var i int
	var db *sql.DB

	pairs := make([][2]interface{}, 0, len(digests))
	for k, v := range digests {
		pairs = append(pairs, [2]interface{}{k, v[field_sorting]})
	}
	//logger.Println(pairs)
	// Sort slice based on values
	sort.Slice(pairs, func(i, j int) bool {
		return int(pairs[i][1].(float64)) > int(pairs[j][1].(float64))
	})

	for _, p := range pairs {
		k := p[0].(string)
		if i > 100 {
			break
		}

		if digests[k]["datname"].(string) == "postgres" ||
			digests[k]["datname"].(string) == "template0" ||
			digests[k]["datname"].(string) == "template1" ||
			digests[k]["datname"].(string) == "NULL" ||
			!(strings.Contains(digests[k]["query_text"].(string), "SELECT ") || strings.Contains(digests[k]["query_text"].(string), "select ")) ||
			digests[k]["explain"] != nil {
			continue
		}
		if digests[k]["query_text"].(string) == "" {
			continue
		}
		if strings.Contains(digests[k]["query_text"].(string), "EXPLAIN (FORMAT JSON)") {
			continue
		}
		if strings.Contains(digests[k]["query_text"].(string), "PREPARE") {
			continue
		}
		if u.IsSchemaNameExclude(digests[k]["datname"].(string), configuration.DatabasesQueryOptimization) {
			continue
		}

		if schema_name_conn != digests[k]["datname"].(string) {
			if db != nil {
				db.Close()
				db = nil
			}
			db = u.ConnectionDatabase(configuration, logger, digests[k]["datname"].(string))
			if db == nil {
				logger.Error("Connection to database failed: ", digests[k]["datname"].(string))
				continue
			}
			schema_name_conn = digests[k]["datname"].(string)
			searchPathSchemas = fetchPgUserSchemas(db, logger)
		}
		query_explain, err := ExecuteExplainWithSearchPath(db, digests[k]["queryid"].(string), digests[k]["query_text"].(string), supportsParameterizedExplain, searchPathSchemas, logger)
		if err != nil {
			digests[k]["explain_error"] = err.Error()
		}
		if query_explain != "" {
			logger.Info(i, " OK")
			logger.Info("queryText: ", digests[k]["query_text"].(string))
			digests[k]["explain"] = query_explain
			i = i + 1
		}
	}
	if db != nil {
		db.Close()
	}
}

func ExecuteExplain(db *sql.DB, queryId string, queryText string, supportsParameterizedExplain bool, logger logging.Logger) (string, error) {
	return ExecuteExplainWithSearchPath(db, queryId, queryText, supportsParameterizedExplain, nil, logger)
}

func ExecuteExplainWithSearchPath(db *sql.DB, queryId string, queryText string, supportsParameterizedExplain bool, searchPathSchemas []string, logger logging.Logger) (string, error) {
	var explain, query_text string
	var explain_error error
	explain_error = nil

	if supportsParameterizedExplain && containsUnquotedPgParameter(queryText) {
		explainPrepared, errPrepared := executePreparedExplain(db, queryId, queryText)
		if errPrepared != nil {
			logger.Error("Explain prepared statement error: ", errPrepared, "; queryText: ", queryText)
			if isExplainPermissionError(errPrepared) {
				return explainPrepared, errors.New("need_grant_permission")
			}
			if shouldRetryPreparedExplainWithSearchPath(errPrepared, searchPathSchemas) {
				searchPathPrepared, searchPathErr := executePreparedExplainWithSearchPath(db, queryId, queryText, searchPathSchemas)
				if searchPathErr != nil {
					logger.Error("Explain prepared search_path retry error: ", searchPathErr, "; queryText: ", queryText)
					if isExplainPermissionError(searchPathErr) {
						return searchPathPrepared, errors.New("need_grant_permission")
					}
					explain_error = searchPathErr
				} else {
					return searchPathPrepared, nil
				}
			} else {
				explain_error = errPrepared
			}
		} else {
			return explainPrepared, nil
		}
	}

	//Try exec EXPLAIN for origin query
	err := db.QueryRow("EXPLAIN (FORMAT JSON) " + queryText).Scan(&explain)
	if err != nil {
		logger.Error("Explain Error: ", err)
		if isExplainPermissionError(err) {
			explain_error = errors.New("need_grant_permission")
			return explain, explain_error
		} else {
			explain_error = err
		}
	} else {
		return explain, explain_error
	}

	//Try exec EXPLAIN for  query with replace "\"" on "'"
	query_text = strings.Replace(queryText, "\"", "'", -1)
	err_1 := db.QueryRow("EXPLAIN (FORMAT JSON) " + query_text).Scan(&explain)
	if err_1 != nil {
		logger.Error("Explain Error: ", err_1)
		if isExplainPermissionError(err_1) {
			explain_error = errors.New("need_grant_permission")
			return explain, explain_error
		} else {
			explain_error = err_1
		}
	} else {
		return explain, explain_error
	}

	//Try exec EXPLAIN for  query with replace "\"" on "`"
	query_text = strings.Replace(queryText, "\"", "`", -1)
	err_2 := db.QueryRow("EXPLAIN (FORMAT JSON) " + query_text).Scan(&explain)
	if err_2 != nil {
		logger.Error("Explain Error: ", err_2)
		if isExplainPermissionError(err_2) {
			explain_error = errors.New("need_grant_permission")
			return explain, explain_error
		} else {
			explain_error = err_2
		}
	} else {
		return explain, explain_error
	}

	if isUndefinedRelationError(explain_error) {
		searchPathExplain, searchPathErr := executeExplainWithSearchPath(db, queryText, searchPathSchemas)
		if searchPathErr != nil {
			logger.Error("Explain search_path retry error: ", searchPathErr)
			if isExplainPermissionError(searchPathErr) {
				explain_error = errors.New("need_grant_permission")
				return searchPathExplain, explain_error
			}
			explain_error = searchPathErr
		} else if searchPathExplain != "" {
			return searchPathExplain, nil
		}
	}

	return explain, explain_error
}

func normalizePgAbsentValue(value string) string {
	if strings.TrimSpace(strings.ToLower(value)) == "null" {
		return ""
	}
	return value
}

func fetchPgUserSchemas(db *sql.DB, logger logging.Logger) []string {
	if db == nil {
		return nil
	}

	query := fmt.Sprintf(
		"SELECT nspname FROM pg_namespace WHERE %s ORDER BY CASE WHEN nspname = 'public' THEN 1 ELSE 0 END, nspname",
		pgUserSchemaPredicate("nspname"),
	)
	rows, err := db.Query(query)
	if err != nil {
		logger.Error("Error collecting PostgreSQL schemas for EXPLAIN search_path retry: ", err)
		return nil
	}
	defer rows.Close()

	schemas := []string{}
	for rows.Next() {
		var schema string
		if err = rows.Scan(&schema); err != nil {
			logger.Error("Error scanning PostgreSQL schema for EXPLAIN search_path retry: ", err)
			return schemas
		}
		schemas = append(schemas, schema)
	}
	return schemas
}

func executeExplainWithSearchPath(db *sql.DB, queryText string, schemas []string) (string, error) {
	searchPath, ok := pgSearchPathList(schemas)
	if !ok {
		return "", fmt.Errorf("no non-public schemas available for search_path retry")
	}

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	var lastErr error
	for _, query := range pgExplainQueryVariants(queryText) {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return "", err
		}

		if _, err = tx.ExecContext(ctx, "SET LOCAL search_path = "+searchPath); err == nil {
			var explain string
			err = tx.QueryRowContext(ctx, "EXPLAIN (FORMAT JSON) "+query).Scan(&explain)
			_ = tx.Rollback()
			if err == nil {
				return explain, nil
			}
		} else {
			_ = tx.Rollback()
		}

		lastErr = err
		if isExplainPermissionError(err) {
			return "", err
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("search_path retry did not run")
	}
	return "", lastErr
}

func pgExplainQueryVariants(queryText string) []string {
	return []string{
		queryText,
		strings.Replace(queryText, "\"", "'", -1),
		strings.Replace(queryText, "\"", "`", -1),
	}
}

func isUndefinedRelationError(err error) bool {
	if err == nil {
		return false
	}
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "relation") && strings.Contains(errText, "does not exist")
}

func pgSearchPathList(schemas []string) (string, bool) {
	quoted := []string{}
	hasNonPublicSchema := false
	for _, schema := range schemas {
		schema = strings.TrimSpace(schema)
		if schema == "" {
			continue
		}
		if strings.ToLower(schema) != "public" {
			hasNonPublicSchema = true
		}
		quoted = append(quoted, `"`+strings.Replace(schema, `"`, `""`, -1)+`"`)
	}
	if !hasNonPublicSchema || len(quoted) == 0 {
		return "", false
	}
	return strings.Join(quoted, ","), true
}

func isExplainPermissionError(err error) bool {
	if err == nil {
		return false
	}
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "select command denied to user") ||
		strings.Contains(errText, "access denied for user") ||
		strings.Contains(errText, "permission denied")
}

func containsUnquotedPgParameter(query string) bool {
	inSingleQuote := false
	for i := 0; i < len(query); i++ {
		ch := query[i]
		if ch == '\'' {
			if inSingleQuote && i+1 < len(query) && query[i+1] == '\'' {
				i++
				continue
			}
			inSingleQuote = !inSingleQuote
			continue
		}
		if inSingleQuote || ch != '$' || i+1 >= len(query) {
			continue
		}
		if query[i+1] < '0' || query[i+1] > '9' {
			continue
		}
		return true
	}
	return false
}

func executePreparedExplain(db *sql.DB, queryId string, queryText string) (string, error) {
	return executePreparedExplainWithSessionSettings(db, queryId, queryText, "")
}

func executePreparedExplainWithSearchPath(db *sql.DB, queryId string, queryText string, schemas []string) (string, error) {
	searchPath, ok := pgSearchPathList(schemas)
	if !ok {
		return "", fmt.Errorf("no non-public schemas available for prepared search_path retry")
	}
	return executePreparedExplainWithSessionSettings(db, queryId, queryText, searchPath)
}

func executePreparedExplainWithSessionSettings(db *sql.DB, queryId string, queryText string, searchPath string) (string, error) {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	stmtName := "releem_" + queryId

	if _, err = conn.ExecContext(ctx, "SET plan_cache_mode = force_generic_plan"); err != nil {
		return "", err
	}
	defer conn.ExecContext(ctx, "RESET plan_cache_mode")
	if searchPath != "" {
		if _, err = conn.ExecContext(ctx, "SET search_path = "+searchPath); err != nil {
			return "", err
		}
		defer conn.ExecContext(ctx, "RESET search_path")
	}
	query := fmt.Sprintf("PREPARE %s AS %s", stmtName, queryText)
	if _, err = conn.ExecContext(ctx, query); err != nil {
		return "", err
	}
	defer conn.ExecContext(ctx, "DEALLOCATE PREPARE "+stmtName)

	var paramsCount int
	if err = conn.QueryRowContext(ctx, "SELECT COALESCE(cardinality(parameter_types), 0) FROM pg_prepared_statements WHERE name = $1", stmtName).Scan(&paramsCount); err != nil {
		return "", err
	}

	executeQuery := "EXPLAIN (FORMAT JSON) EXECUTE " + stmtName
	if paramsCount > 0 {
		nullParams := strings.TrimRight(strings.Repeat("NULL,", paramsCount), ",")
		executeQuery += "(" + nullParams + ")"
	}

	var explain string
	if err = conn.QueryRowContext(ctx, executeQuery).Scan(&explain); err != nil {
		return "", err
	}
	return explain, nil
}

func shouldRetryPreparedExplainWithSearchPath(err error, schemas []string) bool {
	if !isUndefinedRelationError(err) {
		return false
	}
	_, ok := pgSearchPathList(schemas)
	return ok
}

func normalizePgQueryID(queryID string) string {
	trimmed := strings.TrimSpace(queryID)
	if trimmed == "" {
		return queryID
	}

	if signedValue, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return strconv.FormatUint(uint64(signedValue), 10)
	}

	if unsignedValue, err := strconv.ParseUint(trimmed, 10, 64); err == nil {
		return strconv.FormatUint(unsignedValue, 10)
	}

	return queryID
}
