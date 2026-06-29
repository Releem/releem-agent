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
	var rowsSent int64
	var total_exec_time, mean_exec_time float64
	output_digest := make(map[string]models.MetricGroupValue)
	var output []models.MetricGroupValue

	ver_current, _ := version.NewVersion(metrics.DB.Info["Version"].(string))
	ver_postgresql, _ := version.NewVersion("13")
	ver_plan_cache_mode, _ := version.NewVersion("12")
	supportsParameterizedExplain := !ver_current.LessThan(ver_plan_cache_mode)
	// Collect DBMS internal metrics
	pgStatStatements := PG_STAT_STATEMENTS
	if ver_current.LessThan(ver_postgresql) {
		pgStatStatements = PG_STAT_STATEMENTS_OLD_VERSION
	}

	// Collect query statistics from pg_stat_statements
	rows, err := models.DB.Query(pgStatStatements)

	if err != nil {
		DBCollectQueriesOptimization.logger.Error(err)
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
		db := u.ConnectionDatabase(DBCollectQueriesOptimization.configuration, DBCollectQueriesOptimization.logger, database)
		if db == nil {
			return fmt.Errorf("failed to connect to database %s", database)
		}
		err := CollectDbSchema(db, database, DBCollectQueriesOptimization.logger, metrics)
		db.Close()
		if err != nil {
			return err
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

func pgQueryMetric(datname, queryid, query string, calls int, totalExecTimeUs, meanExecTimeUs float64, rowsSent int64) models.MetricGroupValue {
	metric := pgQueryMetricLatency(datname, queryid, calls, totalExecTimeUs, meanExecTimeUs, rowsSent)
	metric["query"] = query
	metric["query_text"] = query
	return metric
}

func pgQueryMetricLatency(datname, queryid string, calls int, totalExecTimeUs, meanExecTimeUs float64, rowsSent int64) models.MetricGroupValue {
	return models.MetricGroupValue{
		"datname":            datname,
		"queryid":            queryid,
		"calls":              calls,
		"total_exec_time_us": totalExecTimeUs,
		"mean_exec_time_us":  meanExecTimeUs,
		"SUM_ROWS_SENT":      rowsSent,
	}
}

func pgTableSchemaMetric(tableSchema, tableName, tableType, engine, tableRows, avgRowLength, dataLength, indexLength, tableCollation string) models.MetricGroupValue {
	return models.MetricGroupValue{
		"TABLE_SCHEMA":    tableSchema,
		"TABLE_NAME":      tableName,
		"TABLE_TYPE":      tableType,
		"ENGINE":          engine,
		"ROW_FORMAT":      "NULL",
		"TABLE_ROWS":      tableRows,
		"AVG_ROW_LENGTH":  avgRowLength,
		"MAX_DATA_LENGTH": "NULL",
		"DATA_LENGTH":     dataLength,
		"INDEX_LENGTH":    indexLength,
		"TABLE_COLLATION": tableCollation,
		"DATA_FREE":       "0",
	}
}

func pgColumnSchemaMetric(tableSchema, tableName, columnName, ordinalPosition, columnDefault, isNullable, dataType, characterMaximumLength, numericPrecision, numericScale, characterSetName string) models.MetricGroupValue {
	return models.MetricGroupValue{
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

func pgIndexSchemaMetric(tableSchema, tableName, indexName, nonUnique, seqInIndex, columnName, collation, cardinality, subPart, packed, nullable, indexType, expression, predicate string) models.MetricGroupValue {
	if columnName == "NULL" && expression != "NULL" {
		columnName = expression
	}
	return models.MetricGroupValue{
		"TABLE_SCHEMA": tableSchema,
		"TABLE_NAME":   tableName,
		"INDEX_NAME":   indexName,
		"NON_UNIQUE":   nonUnique,
		"SEQ_IN_INDEX": seqInIndex,
		"COLUMN_NAME":  columnName,
		"COLLATION":    collation,
		"CARDINALITY":  cardinality,
		"SUB_PART":     subPart,
		"PACKED":       packed,
		"NULLABLE":     nullable,
		"INDEX_TYPE":   indexType,
		"EXPRESSION":   expression,
		"PREDICATE":    predicate,
	}
}

func CollectDbSchema(db *sql.DB, database string, logger logging.Logger, metrics *models.Metrics) error {
	if db == nil {
		return fmt.Errorf("database connection is nil for %s", database)
	}

	// Collect table information from information_schema
	type information_schema_table_type struct {
		TABLE_SCHEMA    string
		TABLE_NAME      string
		TABLE_TYPE      string
		ENGINE          string
		TABLE_ROWS      string
		AVG_ROW_LENGTH  string
		DATA_LENGTH     string
		INDEX_LENGTH    string
		TABLE_COLLATION string
	}
	var information_schema_table information_schema_table_type

	rows, err := db.Query(`
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
			'NULL' AS table_collation
		FROM information_schema.tables t
		LEFT JOIN pg_namespace n ON n.nspname = t.table_schema
		LEFT JOIN pg_class c ON c.relname = t.table_name AND c.relnamespace = n.oid AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
		LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid
		WHERE t.table_catalog = $1
			AND t.table_schema NOT IN ('information_schema', 'pg_catalog')
		ORDER BY t.table_schema, t.table_name`, database)
	if err != nil {
		logger.Error(err)
		return err
	} else {
		defer rows.Close()
		for rows.Next() {
			err := rows.Scan(&information_schema_table.TABLE_SCHEMA, &information_schema_table.TABLE_NAME, &information_schema_table.TABLE_TYPE, &information_schema_table.ENGINE, &information_schema_table.TABLE_ROWS, &information_schema_table.AVG_ROW_LENGTH, &information_schema_table.DATA_LENGTH, &information_schema_table.INDEX_LENGTH, &information_schema_table.TABLE_COLLATION)
			if err != nil {
				logger.Error(err)
				return err
			}
			metrics.DB.DatabaseSchema["information_schema_tables"] = append(
				metrics.DB.DatabaseSchema["information_schema_tables"],
				pgTableSchemaMetric(information_schema_table.TABLE_SCHEMA, information_schema_table.TABLE_NAME, information_schema_table.TABLE_TYPE, information_schema_table.ENGINE, information_schema_table.TABLE_ROWS, information_schema_table.AVG_ROW_LENGTH, information_schema_table.DATA_LENGTH, information_schema_table.INDEX_LENGTH, information_schema_table.TABLE_COLLATION))
		}
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

	rows, err = db.Query(`
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
			AND table_schema NOT IN ('information_schema', 'pg_catalog')
		ORDER BY table_schema, table_name, ordinal_position`, database)
	if err != nil {
		logger.Error(err)
		return err
	} else {
		defer rows.Close()
		for rows.Next() {
			err := rows.Scan(&information_schema_column.TABLE_SCHEMA, &information_schema_column.TABLE_NAME,
				&information_schema_column.COLUMN_NAME, &information_schema_column.ORDINAL_POSITION,
				&information_schema_column.COLUMN_DEFAULT, &information_schema_column.IS_NULLABLE, &information_schema_column.DATA_TYPE,
				&information_schema_column.CHARACTER_MAXIMUM_LENGTH, &information_schema_column.NUMERIC_PRECISION,
				&information_schema_column.NUMERIC_SCALE, &information_schema_column.CHARACTER_SET_NAME)
			if err != nil {
				logger.Error(err)
				return err
			}
			metrics.DB.DatabaseSchema["information_schema_columns"] = append(
				metrics.DB.DatabaseSchema["information_schema_columns"],
				pgColumnSchemaMetric(information_schema_column.TABLE_SCHEMA, information_schema_column.TABLE_NAME, information_schema_column.COLUMN_NAME, information_schema_column.ORDINAL_POSITION, information_schema_column.COLUMN_DEFAULT, information_schema_column.IS_NULLABLE, information_schema_column.DATA_TYPE, information_schema_column.CHARACTER_MAXIMUM_LENGTH, information_schema_column.NUMERIC_PRECISION, information_schema_column.NUMERIC_SCALE, information_schema_column.CHARACTER_SET_NAME))
		}
	}

	type information_schema_index_type struct {
		TABLE_SCHEMA string
		TABLE_NAME   string
		INDEX_NAME   string
		NON_UNIQUE   string
		SEQ_IN_INDEX string
		COLUMN_NAME  string
		COLLATION    string
		CARDINALITY  string
		SUB_PART     string
		PACKED       string
		NULLABLE     string
		INDEX_TYPE   string
		EXPRESSION   string
		PREDICATE    string
	}
	var information_schema_index information_schema_index_type
	rows, err = db.Query(`
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
			'NULL' AS cardinality,
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
				'NULL'
			) AS expression,
			COALESCE(pg_get_expr(idx.indpred, idx.indrelid, true), 'NULL') AS predicate
		FROM pg_index idx
		JOIN pg_class ic ON ic.oid = idx.indexrelid
		JOIN pg_class tc ON tc.oid = idx.indrelid
		JOIN pg_namespace n ON n.oid = tc.relnamespace
		JOIN pg_am am ON am.oid = ic.relam
		CROSS JOIN LATERAL unnest(idx.indkey) WITH ORDINALITY AS key_info(attnum, seq_in_index)
		LEFT JOIN pg_attribute a ON a.attrelid = idx.indrelid AND a.attnum = key_info.attnum AND key_info.attnum > 0
		WHERE n.nspname NOT IN ('information_schema', 'pg_catalog')
			AND current_database() = $1
		ORDER BY n.nspname, tc.relname, ic.relname, key_info.seq_in_index`, database)
	if err != nil {
		logger.Error(err)
		return err
	} else {
		defer rows.Close()
		for rows.Next() {
			err := rows.Scan(&information_schema_index.TABLE_SCHEMA, &information_schema_index.TABLE_NAME,
				&information_schema_index.INDEX_NAME, &information_schema_index.NON_UNIQUE,
				&information_schema_index.SEQ_IN_INDEX, &information_schema_index.COLUMN_NAME,
				&information_schema_index.COLLATION, &information_schema_index.CARDINALITY,
				&information_schema_index.SUB_PART, &information_schema_index.PACKED,
				&information_schema_index.NULLABLE, &information_schema_index.INDEX_TYPE,
				&information_schema_index.EXPRESSION, &information_schema_index.PREDICATE)
			if err != nil {
				logger.Error(err)
				return err
			}
			metrics.DB.DatabaseSchema["information_schema_indexes"] = append(
				metrics.DB.DatabaseSchema["information_schema_indexes"],
				pgIndexSchemaMetric(information_schema_index.TABLE_SCHEMA, information_schema_index.TABLE_NAME, information_schema_index.INDEX_NAME, information_schema_index.NON_UNIQUE, information_schema_index.SEQ_IN_INDEX, information_schema_index.COLUMN_NAME, information_schema_index.COLLATION, information_schema_index.CARDINALITY, information_schema_index.SUB_PART, information_schema_index.PACKED, information_schema_index.NULLABLE, information_schema_index.INDEX_TYPE, information_schema_index.EXPRESSION, information_schema_index.PREDICATE))
		}
	}

	logger.V(5).Info("collectMetrics ", metrics.DB.DatabaseSchema)

	return nil
}

// PostgreSQL version of CollectionExplain - uses EXPLAIN (FORMAT JSON)
func CollectExplain(digests map[string]models.MetricGroupValue, field_sorting string, supportsParameterizedExplain bool, logger logging.Logger, configuration *config.Config) {
	var schema_name_conn string
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
			}
			db = u.ConnectionDatabase(configuration, logger, digests[k]["datname"].(string))
			defer db.Close()
			schema_name_conn = digests[k]["datname"].(string)
		}
		query_explain, err := ExecuteExplain(db, digests[k]["queryid"].(string), digests[k]["query_text"].(string), supportsParameterizedExplain, logger)
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
}

func ExecuteExplain(db *sql.DB, queryId string, queryText string, supportsParameterizedExplain bool, logger logging.Logger) (string, error) {
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
			explain_error = errPrepared
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

	return explain, explain_error
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
