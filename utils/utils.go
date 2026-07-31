// Example of a daemon with echo service
package utils

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/Releem/mysqlconfigurer/config"
	e "github.com/Releem/mysqlconfigurer/errors"
	"github.com/Releem/mysqlconfigurer/models"
	_ "github.com/go-sql-driver/mysql"
	logging "github.com/google/logger"
	_ "github.com/lib/pq"
	"github.com/pkg/errors"
)

func ProcessRepeaters(metrics *models.Metrics, repeaters models.MetricsRepeater,
	configuration *config.Config, logger logging.Logger, Mode models.ModeType) string {
	defer HandlePanic(configuration, logger)

	result, err := repeaters.ProcessMetrics(configuration, *metrics, Mode)
	if err != nil {
		logger.Error("Repeater failed ", err)
	}
	return result
}

func CollectMetrics(gatherers []models.MetricsGatherer, logger logging.Logger, configuration *config.Config) *models.Metrics {
	defer HandlePanic(configuration, logger)
	var metrics models.Metrics
	for _, g := range gatherers {
		err := g.GetMetrics(&metrics)
		if err != nil {
			logger.Error("Problem getting metrics from gatherer")
			return nil
		}
	}
	return &metrics
}

func HandlePanic(configuration *config.Config, logger logging.Logger) {
	if r := recover(); r != nil {
		err := errors.WithStack(fmt.Errorf("%v", r))
		logger.Infof("%+v", err)
		sender := e.NewReleemErrorsRepeater(configuration, logger)
		sender.ProcessErrors(fmt.Sprintf("%+v", err))
	}
}

func MapJoin(map1, map2 models.MetricGroupValue) models.MetricGroupValue {
	for k, v := range map2 {
		map1[k] = v
	}
	return map1
}

func IsPath(path string, logger logging.Logger) bool {
	result_path := strings.Index(path, "/")
	logger.Info("DEBUG: IsPath ", path, " ", result_path)
	if result_path == 0 {
		return true
	} else {
		return false
	}
}

func ConnectionDatabase(configuration *config.Config, logger logging.Logger, DBname string) *sql.DB {
	db, _ := ConnectionDatabaseErr(configuration, logger, DBname)
	return db
}

func ConnectionDatabaseErr(configuration *config.Config, logger logging.Logger, DBname string) (*sql.DB, error) {
	dbType := configuration.GetDatabaseType()

	switch dbType {
	case "postgresql":
		if DBname == "" {
			DBname = "postgres"
		}
		return ConnectionPostgreSQLErr(configuration, logger, DBname)
	case "mysql":
		fallthrough
	default:
		if DBname == "" {
			DBname = "mysql"
		}
		return ConnectionMySQLErr(configuration, logger, DBname)
	}
}

func ConnectionMySQL(configuration *config.Config, logger logging.Logger, DBname string) *sql.DB {
	db, _ := ConnectionMySQLErr(configuration, logger, DBname)
	return db
}

func ConnectionMySQLErr(configuration *config.Config, logger logging.Logger, DBname string) (*sql.DB, error) {
	configurationSnapshot := configuration.Snapshot()
	configuration = &configurationSnapshot
	var db *sql.DB
	var err error
	var TypeConnection string
	dsn_params := "?interpolateParams=true"

	if IsPath(configuration.MysqlHost, logger) {
		db, err = sql.Open("mysql", configuration.MysqlUser+":"+configuration.MysqlPassword+"@unix("+configuration.MysqlHost+")/"+DBname+dsn_params)
		TypeConnection = "unix"

	} else {
		if configuration.MysqlSslMode {
			dsn_params = dsn_params + "&tls=skip-verify"
		}
		db, err = sql.Open("mysql", configuration.MysqlUser+":"+configuration.MysqlPassword+"@tcp("+configuration.MysqlHost+":"+configuration.MysqlPort+")/"+DBname+dsn_params)
		TypeConnection = "tcp"
	}
	if err != nil {
		logger.Error("Connection opening to failed ", err)
		return nil, err
	}

	err = db.Ping()
	if err != nil {
		switch TypeConnection {
		case "unix":
			logger.Error("Connection failed to DB ", DBname, " via unix socket ", configuration.MysqlHost, ", error: ", err.Error())
		case "tcp":
			logger.Error("Connection failed to DB ", DBname, " via tcp ", configuration.MysqlHost, ", error: ", err.Error())
		}
		_ = db.Close()
		return nil, err
	} else {
		switch TypeConnection {
		case "unix":
			logger.Info("Connection successful to DB ", DBname, " via unix socket ", configuration.MysqlHost)
		case "tcp":
			logger.Info("Connection successful to DB ", DBname, " via tcp ", configuration.MysqlHost)
		}
	}
	return db, nil
}

func ConnectionPostgreSQL(configuration *config.Config, logger logging.Logger, DBname string) *sql.DB {
	db, _ := ConnectionPostgreSQLErr(configuration, logger, DBname)
	return db
}

func ConnectionPostgreSQLErr(configuration *config.Config, logger logging.Logger, DBname string) (*sql.DB, error) {
	configurationSnapshot := configuration.Snapshot()
	configuration = &configurationSnapshot
	db, err := sql.Open("postgres", postgresqlConnectionString(configuration, DBname))
	if err != nil {
		logger.Error("PostgreSQL connection opening failed ", err)
		return nil, err
	}

	if err = db.Ping(); err != nil {
		logger.Error("PostgreSQL connection failed to DB ", DBname, " via tcp ", configuration.PgHost, ":", configuration.PgPort, ", error: ", err.Error())
		_ = db.Close()
		return nil, err
	}
	logger.Info("PostgreSQL connection successful to DB ", DBname, " via tcp ", configuration.PgHost, ":", configuration.PgPort)
	return db, nil
}

func postgresqlConnectionString(configuration *config.Config, database string) string {
	sslmode := "disable"
	if configuration.PgSslMode {
		sslmode = "require"
	}
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		quotePostgresqlDSNValue(configuration.PgHost),
		quotePostgresqlDSNValue(configuration.PgPort),
		quotePostgresqlDSNValue(configuration.PgUser),
		quotePostgresqlDSNValue(configuration.PgPassword),
		quotePostgresqlDSNValue(database),
		quotePostgresqlDSNValue(sslmode),
	)
}

func quotePostgresqlDSNValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `'`, `\'`)
	return `'` + value + `'`
}

func EnableEventsStatementsConsumers(configuration *config.Config, logger logging.Logger, uptime_str string) {
	uptime, err := strconv.Atoi(uptime_str)
	if err != nil {
		logger.Error(err)
	}
	if configuration.QueryOptimization && uptime < 120 {
		err := models.DB.QueryRow("SELECT count(name) FROM performance_schema.setup_consumers WHERE enabled = 'YES' AND name LIKE 'events_statements_%' AND name != 'events_statements_cpu'").Scan(&models.CountEnabledConsumers)
		if err != nil {
			logger.Error(err)
		}
		logger.Info("DEBUG: Found enabled performance_schema statements consumers: ", models.CountEnabledConsumers)
		if models.CountEnabledConsumers < 3 && configuration.InstanceType == "aws/rds" {
			_, err := models.DB.Query("CALL releem.enable_events_statements_consumers()")
			if err != nil {
				logger.Error("Failed to enable events_statements consumers", err)
			} else {
				logger.Info("Enable events_statements_consumers")
			}
			err = models.DB.QueryRow("SELECT count(name) FROM performance_schema.setup_consumers WHERE enabled = 'YES' AND name LIKE 'events_statements_%' AND name != 'events_statements_cpu'").Scan(&models.CountEnabledConsumers)
			if err != nil {
				logger.Error(err)
			}
			logger.Info("DEBUG: Found enabled performance_schema statements consumers: ", models.CountEnabledConsumers)
		}
	}
}

func GetStrategyCollectionSampleQueries(configuration *config.Config, logger logging.Logger, uptime_str string) {
	// Only applicable to MySQL
	if configuration.GetDatabaseType() == "mysql" {
		EnableEventsStatementsConsumers(configuration, logger, uptime_str)
	}
	if models.CountEnabledConsumers >= 2 {
		configuration.CollectSampleQueriesPeriod = 10 // 10 seconds
	} else if models.CountEnabledConsumers > 0 {
		configuration.CollectSampleQueriesPeriod = 1 // 1 second
	} else if models.CountEnabledConsumers == 0 {
		configuration.CollectSampleQueriesPeriod = 600 // 10 minutes
	}
}

// IsSchemaNameExclude checks if a schema name should be excluded from query optimization
func IsSchemaNameExclude(SchemaName string, DatabasesQueryOptimization string) bool {
	if DatabasesQueryOptimization == "" {
		return false
	}
	for _, DbName := range strings.Split(DatabasesQueryOptimization, `,`) {
		if SchemaName == DbName {
			return false
		}
	}
	return true
}

func ScanRows(rows *sql.Rows, logger logging.Logger) []models.MetricGroupValue {

	cols, err := rows.Columns()
	if err != nil {
		logger.Error(err)
	}
	var out []map[string]any

	for rows.Next() {
		// Готовим приёмники под каждую колонку
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		err := rows.Scan(ptrs...)
		if err != nil {
			logger.Error(err)
			return nil
		}

		row := make(map[string]any, len(cols))
		for i, col := range cols {
			v := values[i]
			switch vv := v.(type) {
			case []byte:
				row[col] = string(vv)
			case nil:
				row[col] = nil
			default:
				row[col] = vv // может быть nil, time.Time, int64, float64, bool и т.д.
			}

		}
		out = append(out, row)
	}
	// Convert []map[string]any to []models.MetricGroupValue
	processListConverted := make([]models.MetricGroupValue, len(out))
	for i, row := range out {
		processListConverted[i] = models.MetricGroupValue(row)
	}
	return processListConverted
}

func DBVersionFileName() string {
	switch runtime.GOOS {
	case "windows":
		return "\\DB_Version.txt"
	default: // для Linux и других UNIX-подобных систем
		return "/db_version"
	}
}

func MergeJSONStrings(leftJSON, rightJSON string, key string) (string, error) {
	if leftJSON == "" {
		return rightJSON, nil
	}
	if rightJSON == "" {
		return leftJSON, nil
	}

	var leftValue interface{}
	if err := json.Unmarshal([]byte(leftJSON), &leftValue); err != nil {
		return "", err
	}
	var rightValue interface{}
	if err := json.Unmarshal([]byte(rightJSON), &rightValue); err != nil {
		return "", err
	}

	switch leftTyped := leftValue.(type) {
	case map[string]interface{}:
		if rightTyped, ok := rightValue.(map[string]interface{}); ok {
			if key != "" {
				leftTyped[key] = rightTyped
			} else {
				for key, value := range rightTyped {
					leftTyped[key] = value
				}
			}
			mergedBytes, err := json.Marshal(leftTyped)
			if err != nil {
				return "", err
			}
			return string(mergedBytes), nil
		}
	case []interface{}:
		if rightTyped, ok := rightValue.([]interface{}); ok {
			mergedBytes, err := json.Marshal(append(leftTyped, rightTyped...))
			if err != nil {
				return "", err
			}
			return string(mergedBytes), nil
		} else if rightTyped, ok := rightValue.(map[string]interface{}); ok {
			for i, k := range leftTyped {
				typedItem, ok := k.(map[string]interface{})
				if !ok {
					continue
				}
				if key != "" {
					typedItem[key] = rightTyped
				} else {
					for key, value := range rightTyped {
						typedItem[key] = value
					}
				}
				leftTyped[i] = typedItem
			}
			mergedBytes, err := json.Marshal(leftTyped)
			if err != nil {
				return "", err
			}
			return string(mergedBytes), nil
		}
	}

	mergedBytes, err := json.Marshal([]interface{}{leftValue, rightValue})
	if err != nil {
		return "", err
	}
	return string(mergedBytes), nil
}

func ConvertUptimeToStr(Status models.MetricGroupValue) string {
	// Safely get uptime value - use "0" as default if not available
	var uptime_str string = "0"
	if uptime_val, exists := Status["Uptime"]; exists && uptime_val != nil {
		if uptime_str_val, ok := uptime_val.(string); ok {
			uptime_str = uptime_str_val
		}
	}
	return uptime_str
}
