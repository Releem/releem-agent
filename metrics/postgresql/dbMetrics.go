package postgresql

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/Releem/mysqlconfigurer/utils"
	u "github.com/Releem/mysqlconfigurer/utils"
	logging "github.com/google/logger"
)

type DBMetricsBaseGatherer struct {
	logger        logging.Logger
	configuration *config.Config
	capabilities  *PostgresCapabilities
}

func NewDBMetricsBaseGatherer(logger logging.Logger, configuration *config.Config, capabilities *PostgresCapabilities) *DBMetricsBaseGatherer {
	return &DBMetricsBaseGatherer{
		logger:        logger,
		configuration: configuration,
		capabilities:  capabilities,
	}
}

type DBMetricsConfigGatherer struct {
	logger        logging.Logger
	configuration *config.Config
}

type pgDatabaseConnection interface {
	Query(string, ...interface{}) (*sql.Rows, error)
	QueryRow(string, ...interface{}) *sql.Row
	Close() error
}

func forEachPGDatabase(databases []string, connect func(string) pgDatabaseConnection, collect func(string, pgDatabaseConnection) error, logError func(string, error)) {
	for _, database := range databases {
		db := connect(database)
		if db == nil {
			continue
		}
		err := collect(database, db)
		closeErr := db.Close()
		if err != nil && logError != nil {
			logError(database, err)
		}
		if closeErr != nil && logError != nil {
			logError(database, closeErr)
		}
	}
}

func addPGDatabaseTableCount(total *uint64, scan func(*uint64) error) error {
	var tablesCount uint64
	if err := scan(&tablesCount); err != nil {
		return err
	}
	*total += tablesCount
	return nil
}

func NewDBMetricsConfigGatherer(logger logging.Logger, configuration *config.Config) *DBMetricsConfigGatherer {
	return &DBMetricsConfigGatherer{
		logger:        logger,
		configuration: configuration,
	}
}

func (DBMetricsBase *DBMetricsBaseGatherer) GetMetrics(metrics *models.Metrics) error {
	defer utils.HandlePanic(DBMetricsBase.configuration, DBMetricsBase.logger)
	{
		pg_stat := make(models.MetricGroupValue)
		// ver_current, _ := version.NewVersion(metrics.DB.Info["Version"].(string))
		// ver_postgresql, _ := version.NewVersion("9.4")
		// Collect DBMS internal metrics
		// pgStatViews := PG_STAT_VIEWS
		// if ver_current.LessThan(ver_postgresql) {
		// 	pgStatViews = PG_STAT_VIEWS_OLD_VERSION
		// }
		for _, view := range PG_STAT_VIEWS {
			rows, err := models.DB.Query(`
			SELECT * FROM ` + view)
			if err != nil {
				if !strings.Contains(err.Error(), "relation \""+view+"\" does not exist") {
					DBMetricsBase.logger.Error(err)
				}
			} else {
				defer rows.Close()
				var existing []models.MetricGroupValue
				if val, ok := pg_stat[view].([]models.MetricGroupValue); ok {
					existing = val
				}
				pg_stat[view] = append(existing, utils.ScanRows(rows, DBMetricsBase.logger)...)
			}
		}

		// PostgreSQL Uptime Statistics
		{
			var uptime, timestamp string
			err := models.DB.QueryRow("SELECT EXTRACT(EPOCH FROM (now() - pg_postmaster_start_time()))::bigint AS uptime, EXTRACT(EPOCH FROM (now()) )::bigint AS timestamp").Scan(&uptime, &timestamp)
			if err != nil {
				DBMetricsBase.logger.Error(err)
			}
			pg_stat["Uptime"] = uptime
			pg_stat["timestamp"] = timestamp
		}
		metrics.DB.Metrics.Status = pg_stat
	}
	// List of databases
	{
		var database string
		var output []string
		rows, err := models.DB.Query("SELECT datname FROM pg_database WHERE datistemplate = false ORDER BY datname")
		if err != nil {
			DBMetricsBase.logger.Error(err)
			return err
		}
		defer rows.Close()

		for rows.Next() {
			err := rows.Scan(&database)
			if err != nil {
				DBMetricsBase.logger.Error(err)
				return err
			}
			output = append(output, database)
		}
		metrics.DB.Metrics.Databases = output
	}

	// Query latency from pg_stat_statements if available
	{
		capabilities, err := DBMetricsBase.capabilities.Resolve(context.Background(), models.DB)
		if err != nil {
			DBMetricsBase.logger.Error("Unable to collect pg_stat_statements capabilities: ", err)
			metrics.DB.Queries = nil
		} else {
			var dealloc uint64
			var stats_reset string

			if capabilities.PgStatStatementsInfoRelation != "" {
				err = models.DB.QueryRow("SELECT dealloc, stats_reset FROM "+capabilities.PgStatStatementsInfoRelation).Scan(&dealloc, &stats_reset)
				if err != nil && err != sql.ErrNoRows {
					DBMetricsBase.logger.Error(err)
				}
			}
			metrics.DB.Metrics.Status["pg_stat_statements_info"] = models.MetricGroupValue{
				"dealloc":     dealloc,
				"stats_reset": stats_reset,
			}

			var count_statements uint64

			err = models.DB.QueryRow("SELECT COUNT(*) FROM " + capabilities.PgStatStatementsRelation).Scan(&count_statements)
			if err != nil {
				if err != sql.ErrNoRows {
					DBMetricsBase.logger.Error(err)
				}
			}
			metrics.DB.Metrics.CountQueriesLatency = count_statements

			statementRows, err := collectPostgresQueryStats(context.Background(), models.DB, capabilities)
			if err != nil {
				DBMetricsBase.capabilities.InvalidateForError(err)
				if err != sql.ErrNoRows {
					DBMetricsBase.logger.Error(err)
				}
			} else {
				metrics.DB.Queries = postgresQueryStatsLegacyMetrics(statementRows)
			}
		}
	}

	// Process list from pg_stat_activity
	{
		var output []models.MetricGroupValue
		rows, err := models.DB.Query(`
			SELECT pid,
			datname,
			usename,
			COALESCE(host(client_addr), 'local') || COALESCE(':' || client_port::text, '') AS client_address,
			application_name,
			backend_type,
			state,
			CONCAT_WS(' ', wait_event_type::text, wait_event::text) AS wait_event,
			EXTRACT(EPOCH FROM (now() - query_start))::bigint AS query_time,
			EXTRACT(EPOCH FROM (now() - xact_start))::bigint AS tx_time,
			EXTRACT(EPOCH FROM (now() - state_change))::bigint AS state_time,
			query
			FROM pg_stat_activity
			WHERE state IS NOT NULL
			ORDER BY pid`)
		if err != nil {
			if err != sql.ErrNoRows {
				DBMetricsBase.logger.Error(err)
			}
		} else {
			defer rows.Close()
			output = append(output, utils.ScanRows(rows, DBMetricsBase.logger)...)
		}

		metrics.DB.Metrics.ProcessList = output
	}

	DBMetricsBase.logger.V(5).Info("CollectMetrics DBMetricsBase ", metrics.DB.Metrics)

	return nil
}

func (DBMetricsConfig *DBMetricsConfigGatherer) GetMetrics(metrics *models.Metrics) error {
	defer utils.HandlePanic(DBMetricsConfig.configuration, DBMetricsConfig.logger)

	output := make(map[string]models.MetricGroupValue)
	var total_tables, size, count uint64
	var table_type string
	i := 0

	// // Total tables count
	// err := models.DB.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema NOT IN ('information_schema', 'pg_catalog')").Scan(&row)
	// if err != nil {
	// 	DBMetricsConfig.logger.Error(err)
	// }
	// total_tables += row

	// // PostgreSQL table engine statistics (PostgreSQL doesn't have engines like MySQL, but we can collect table types)
	// // Switch to each database to get table statistics
	// rows, err := models.DB.Query(`
	// 				SELECT
	// 					t.table_type,
	// 					COUNT(*) as table_count,
	// 					COALESCE(SUM(pg_total_relation_size(c.oid)), 0) as total_size
	// 				FROM information_schema.tables t
	// 				LEFT JOIN pg_class c ON c.relname = t.table_name
	// 				LEFT JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = t.table_schema
	// 				WHERE t.table_schema NOT IN ('information_schema', 'pg_catalog')
	// 				GROUP BY t.table_type`)

	// if err != nil {
	// 	DBMetricsConfig.logger.Error(err)
	// } else {
	// 	for rows.Next() {
	// 		err := rows.Scan(&table_type, &count, &size)
	// 		if err != nil {
	// 			DBMetricsConfig.logger.Error(err)
	// 		}
	// 		if output[table_type] == nil {
	// 			output[table_type] = models.MetricGroupValue{"Table Number": uint64(0), "Total Size": uint64(0)}
	// 		}
	// 		output[table_type]["Table Number"] = output[table_type]["Table Number"].(uint64) + count
	// 		output[table_type]["Total Size"] = output[table_type]["Total Size"].(uint64) + size
	// 	}
	// 	rows.Close()
	// }

	forEachPGDatabase(metrics.DB.Metrics.Databases, func(database string) pgDatabaseConnection {
		// if database == "postgres" {
		// 	continue
		// }
		// Switch to each database to get table statistics
		db := u.ConnectionDatabase(DBMetricsConfig.configuration, DBMetricsConfig.logger, database)
		if db == nil {
			DBMetricsConfig.logger.Error("Connection to database failed: ", database)
			return nil
		}
		return db
	}, func(database string, db pgDatabaseConnection) error {
		for _, view := range PG_STAT_PER_DB_VIEWS {
			rows, err := db.Query(`
			SELECT * FROM ` + view)
			if err != nil {
				return err
			}
			var existing []models.MetricGroupValue
			if val, ok := metrics.DB.Metrics.Status[view].([]models.MetricGroupValue); ok {
				existing = val
			}
			metrics.DB.Metrics.Status[view] = append(existing, utils.ScanRows(rows, DBMetricsConfig.logger)...)
			rows.Close()
		}
		// Total tables count
		err := addPGDatabaseTableCount(&total_tables, func(tablesCount *uint64) error {
			return db.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema NOT IN ('information_schema', 'pg_catalog')").Scan(tablesCount)
		})
		if err != nil {
			DBMetricsConfig.logger.Error(err)
		}

		// PostgreSQL table engine statistics (PostgreSQL doesn't have engines like MySQL, but we can collect table types)
		rows, err := db.Query(`
				SELECT 
					t.table_type,
					COUNT(*) as table_count,
					COALESCE(SUM(pg_total_relation_size(c.oid)), 0) as total_size
				FROM information_schema.tables t
				LEFT JOIN pg_class c ON c.relname = t.table_name
				LEFT JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = t.table_schema
				WHERE t.table_schema NOT IN ('information_schema', 'pg_catalog')
				GROUP BY t.table_type`)

		if err != nil {
			DBMetricsConfig.logger.Error(err)
		} else {
			for rows.Next() {
				err := rows.Scan(&table_type, &count, &size)
				if err != nil {
					DBMetricsConfig.logger.Error(err)
					continue
				}
				if output[table_type] == nil {
					output[table_type] = models.MetricGroupValue{"Table Number": uint64(0), "Total Size": uint64(0)}
				}
				output[table_type]["Table Number"] = output[table_type]["Table Number"].(uint64) + count
				output[table_type]["Total Size"] = output[table_type]["Total Size"].(uint64) + size
			}
			rows.Close()
		}
		i += 1
		if i%25 == 0 {
			time.Sleep(3 * time.Second)
		}
		return nil
	}, func(_ string, err error) {
		DBMetricsConfig.logger.Error(err)
	})
	metrics.DB.Metrics.Engine = output
	metrics.DB.Metrics.TotalTables = total_tables

	DBMetricsConfig.logger.V(5).Info("CollectMetrics DBMetricsConfig ", metrics.DB.Metrics)
	return nil
}
