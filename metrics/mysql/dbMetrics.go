package mysql

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/Releem/mysqlconfigurer/utils"
	logging "github.com/google/logger"
)

type DBMetricsBaseGatherer struct {
	logger        logging.Logger
	configuration *config.Config
}

func NewDBMetricsBaseGatherer(logger logging.Logger, configuration *config.Config) *DBMetricsBaseGatherer {
	return &DBMetricsBaseGatherer{
		logger:        logger,
		configuration: configuration,
	}
}

func (DBMetricsBase *DBMetricsBaseGatherer) GetMetrics(metrics *models.Metrics) error {
	defer utils.HandlePanic(DBMetricsBase.configuration, DBMetricsBase.logger)
	// Mysql Status
	output := make(models.MetricGroupValue)
	{
		var row models.MetricValue
		rows, err := models.DB.Query("SHOW STATUS")

		if err != nil {
			DBMetricsBase.logger.Error(err)
			return err
		}
		for rows.Next() {
			err := rows.Scan(&row.Name, &row.Value)
			if err != nil {
				DBMetricsBase.logger.Error(err)
				return err
			}
			output[row.Name] = row.Value
		}
		rows.Close()

		rows, err = models.DB.Query("SHOW GLOBAL STATUS")
		if err != nil {
			DBMetricsBase.logger.Error(err)
			return err
		}
		for rows.Next() {
			err := rows.Scan(&row.Name, &row.Value)
			if err != nil {
				DBMetricsBase.logger.Error(err)
				return err
			}
			output[row.Name] = row.Value
		}
		metrics.DB.Metrics.Status = output
		rows.Close()
	}
	//status innodb engine
	{
		var engine, name, status string
		err := models.DB.QueryRow("show engine innodb status").Scan(&engine, &name, &status)
		if err != nil {
			DBMetricsBase.logger.Error(err)
		} else {
			metrics.DB.Metrics.InnoDBEngineStatus = status
		}
	}
	//list of databases
	{
		var database string
		var output []string
		rows, err := models.DB.Query("SELECT table_schema FROM INFORMATION_SCHEMA.tables group BY table_schema")
		if err != nil {
			DBMetricsBase.logger.Error(err)
			return err
		}
		for rows.Next() {
			err := rows.Scan(&database)
			if err != nil {
				DBMetricsBase.logger.Error(err)
				return err
			}
			output = append(output, database)
		}
		rows.Close()
		metrics.DB.Metrics.Databases = output
	}
	//Total table
	{
		var row uint64
		err := models.DB.QueryRow("SELECT COUNT(*) as count FROM information_schema.tables").Scan(&row)
		if err != nil {
			DBMetricsBase.logger.Error(err)
			return err
		}
		metrics.DB.Metrics.TotalTables = row
	}
	// count of queries latency
	{
		var count_events_statements_summary_by_digest uint64

		err := models.DB.QueryRow("SELECT count(*) FROM performance_schema.events_statements_summary_by_digest").Scan(&count_events_statements_summary_by_digest)
		if err != nil {
			if err != sql.ErrNoRows {
				DBMetricsBase.logger.Error(err)
			}
		} else {
			metrics.DB.Metrics.CountQueriesLatency = count_events_statements_summary_by_digest
		}
	}
	metrics.DB.Metrics.CountEnabledEventsStatementsConsumers = models.CountEnabledConsumers
	DBMetricsBase.logger.V(5).Info("CollectMetrics DBMetricsBase ", metrics.DB.Metrics)

	return nil
}

type DBMetricsGatherer struct {
	logger        logging.Logger
	configuration *config.Config
}

func NewDBMetricsGatherer(logger logging.Logger, configuration *config.Config) *DBMetricsGatherer {
	return &DBMetricsGatherer{
		logger:        logger,
		configuration: configuration,
	}
}

func (DBMetrics *DBMetricsGatherer) GetMetrics(metrics *models.Metrics) error {
	defer utils.HandlePanic(DBMetrics.configuration, DBMetrics.logger)

	// Latency
	{
		var output []models.MetricGroupValue
		var schema_name, query_id string
		var calls, avg_time_us, sum_time_us int

		rows, err := models.DB.Query("SELECT IFNULL(schema_name, 'NULL') as schema_name, IFNULL(digest, 'NULL') as query_id, count_star as calls, round(avg_timer_wait/1000000, 0) as avg_time_us, round(SUM_TIMER_WAIT/1000000, 0) as sum_time_us FROM performance_schema.events_statements_summary_by_digest")
		if err != nil {
			if err != sql.ErrNoRows {
				DBMetrics.logger.Error(err)
			}
		} else {
			defer rows.Close()
			for rows.Next() {
				err := rows.Scan(&schema_name, &query_id, &calls, &avg_time_us, &sum_time_us)
				if err != nil {
					DBMetrics.logger.Error(err)
					return err
				}
				output = append(output, models.MetricGroupValue{"schema_name": schema_name, "query_id": query_id, "calls": calls, "avg_time_us": avg_time_us, "sum_time_us": sum_time_us})
			}
		}
		metrics.DB.Queries = output

		// if len(output) != 0 {
		// 	totalQueryCount := len(output)
		// 	dictQueryCount := make(map[int]int)
		// 	listAvgTimeDistinct := make([]int, 0)
		// 	listAvgTime := make([]int, 0)

		// 	for _, query := range output {
		// 		avgTime := query["avg_time_us"].(int)
		// 		if !contains(listAvgTimeDistinct, avgTime) {
		// 			listAvgTimeDistinct = append(listAvgTimeDistinct, avgTime)
		// 		}
		// 		listAvgTime = append(listAvgTime, avgTime)
		// 	}
		// 	sort.Sort(sort.Reverse(sort.IntSlice(listAvgTimeDistinct)))

		// 	for _, avgTime1 := range listAvgTime {
		// 		for _, avgTime2 := range listAvgTimeDistinct {
		// 			if avgTime2 >= avgTime1 {
		// 				if _, ok := dictQueryCount[avgTime2]; !ok {
		// 					dictQueryCount[avgTime2] = 1
		// 				} else {
		// 					dictQueryCount[avgTime2]++
		// 				}
		// 			} else {
		// 				break
		// 			}
		// 		}
		// 	}

		// 	latency := 0
		// 	for _, avgTime := range listAvgTimeDistinct {
		// 		if float64(dictQueryCount[avgTime])/float64(totalQueryCount) <= 0.95 {
		// 			break
		// 		}
		// 		latency = avgTime
		// 	}

		// 	metrics.DB.Metrics.Latency = strconv.Itoa(latency)
		// } else {
		// 	metrics.DB.Metrics.Latency = ""
		// }
	}
	// ProcessList
	{

		var total_info_length uint64
		total_info_length = 0
		information_schema_processlist_fields := []string{"ID", "USER", "HOST", "DB", "COMMAND", "TIME", "STATE", "INFO"}
		rows, err := models.DB.Query("SHOW FULL PROCESSLIST")

		if err != nil {
			DBMetrics.logger.Error(err)
		} else {
			defer rows.Close()

			cols, err := rows.Columns()
			if err != nil {
				DBMetrics.logger.Error(err)
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
					DBMetrics.logger.Error(err)
					return err
				}

				row := make(map[string]any, len(cols))
				for i, col := range cols {
					col_upper := strings.ToUpper(col)
					if str_contains(information_schema_processlist_fields, col_upper) {
						v := values[i]

						// Драйвер MySQL часто отдаёт []byte для текстов и чисел.
						// Преобразуем []byte → string для удобства (если нужно).
						switch vv := v.(type) {
						case []byte:
							row[col_upper] = string(vv)
						case nil:
							row[col_upper] = "NULL"
						default:
							row[col_upper] = vv // может быть nil, time.Time, int64, float64, bool и т.д.
						}
						if col_upper == "INFO" {
							total_info_length = total_info_length + uint64(len(row[col_upper].(string)))
						}
					}
				}
				out = append(out, row)
			}
			// Convert []map[string]any to []models.MetricGroupValue
			processListConverted := make([]models.MetricGroupValue, len(out))
			for i, row := range out {
				processListConverted[i] = models.MetricGroupValue(row)
			}
			metrics.DB.Metrics.ProcessList = processListConverted

			// Limit total INFO field size to prevent memory issues
			const maxTotalSize = 32 * 1024 * 1024 // 32MB
			const minInfoLength = 64              // Minimum INFO length to preserve
			process_list_info_limit_length := 65536

			for total_info_length > maxTotalSize && process_list_info_limit_length >= minInfoLength {
				total_info_length = 0
				for i := range metrics.DB.Metrics.ProcessList {
					infoStr := metrics.DB.Metrics.ProcessList[i]["INFO"].(string)
					if len(infoStr) > process_list_info_limit_length {
						metrics.DB.Metrics.ProcessList[i]["INFO"] = infoStr[:process_list_info_limit_length]
					}
					total_info_length += uint64(len(metrics.DB.Metrics.ProcessList[i]["INFO"].(string)))
				}
				process_list_info_limit_length = process_list_info_limit_length / 2

				// Safety check to prevent infinite loop
				if process_list_info_limit_length < minInfoLength {
					DBMetrics.logger.Warning("INFO truncation reached minimum length, stopping truncation")
					break
				}
			}

		}
	}

	DBMetrics.logger.V(5).Info("CollectMetrics DBMetrics ", metrics.DB.Metrics)

	return nil
}

type DBMetricsConfigGatherer struct {
	logger         logging.Logger
	configuration  *config.Config
	tableSizeCache *tableSizeCache
	now            func() time.Time
}

func NewDBMetricsConfigGatherer(logger logging.Logger, configuration *config.Config) *DBMetricsConfigGatherer {
	return &DBMetricsConfigGatherer{
		logger:         logger,
		configuration:  configuration,
		tableSizeCache: &tableSizeCache{},
		now:            time.Now,
	}
}

func (DBMetricsConfig *DBMetricsConfigGatherer) GetMetrics(metrics *models.Metrics) error {
	defer utils.HandlePanic(DBMetricsConfig.configuration, DBMetricsConfig.logger)

	//Stat mysql Engine
	{
		support, err := DBMetricsConfig.collectEngineSupport()
		if err != nil {
			DBMetricsConfig.logger.Error(err)
			return err
		}

		ttl := tableSizeCacheTTLFromSeconds(DBMetricsConfig.configuration.TableSizeCacheTTL)
		result, err := DBMetricsConfig.tableSizeCache.getOrRefresh(
			tableSizeCacheInput{
				totalTables:    metrics.DB.Metrics.TotalTables,
				effectiveRAM:   effectiveTableSizeRAM(metrics),
				tableThreshold: DBMetricsConfig.configuration.TableSizeCacheTableThreshold,
				ramMultiplier:  DBMetricsConfig.configuration.TableSizeCacheRAMMultiplier,
				ttl:            ttl,
				now:            DBMetricsConfig.now(),
			},
			func() (map[string]models.MetricGroupValue, error) {
				return DBMetricsConfig.collectEngineTableSizes(metrics.DB.Metrics.Databases)
			},
		)
		if err != nil {
			DBMetricsConfig.logger.Error(err)
			return err
		}
		if result.usedStale {
			DBMetricsConfig.logger.Warningf("using stale table size metrics after refresh error: %v", result.refreshErr)
		}

		metrics.DB.Metrics.Engine = mergeEngineMetrics(support, result.engine)
		myisam := metrics.DB.Metrics.Engine["MyISAM"]
		if myisam == nil {
			metrics.DB.Metrics.TotalMyisamIndexes = 0
		} else {
			metrics.DB.Metrics.TotalMyisamIndexes = tableSizeUint64(myisam["Index Size"])
		}
	}
	DBMetricsConfig.logger.V(5).Info("CollectMetrics DBMetricsConfig ", metrics.DB.Metrics)

	return nil
}

func (DBMetricsConfig *DBMetricsConfigGatherer) collectEngineSupport() (map[string]models.MetricGroupValue, error) {
	var engineDB, engineEnabled string
	output := make(map[string]models.MetricGroupValue)

	rows, err := models.DB.Query("SELECT ENGINE,SUPPORT FROM information_schema.ENGINES ORDER BY ENGINE ASC")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		err := rows.Scan(&engineDB, &engineEnabled)
		if err != nil {
			return nil, errors.Join(err, rows.Err(), rows.Close())
		}
		output[engineDB] = models.MetricGroupValue{"Enabled": engineEnabled}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}

	return output, nil
}

func (DBMetricsConfig *DBMetricsConfigGatherer) collectEngineTableSizes(databases []string) (map[string]models.MetricGroupValue, error) {
	var engineDB string
	var size, count, dataSize, indexSize uint64
	engineMetrics := make(map[string]models.MetricGroupValue)

	for i, database := range databases {
		rows, err := models.DB.Query(`SELECT ENGINE, IFNULL(SUM(DATA_LENGTH+INDEX_LENGTH), 0), IFNULL(COUNT(ENGINE), 0), IFNULL(SUM(DATA_LENGTH), 0), IFNULL(SUM(INDEX_LENGTH), 0) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND ENGINE IS NOT NULL  GROUP BY ENGINE ORDER BY ENGINE ASC`, database)
		if err != nil {
			return nil, err
		}
		// A scan failure must be reported as a refresh error: a partial
		// snapshot would otherwise be cached for the whole TTL.
		var firstRowErr error
		for rows.Next() {
			err := rows.Scan(&engineDB, &size, &count, &dataSize, &indexSize)
			if err != nil {
				DBMetricsConfig.logger.Error(err)
				if firstRowErr == nil {
					firstRowErr = err
				}
				continue
			}
			if engineMetrics[engineDB]["Table Number"] == nil {
				engineMetrics[engineDB] = models.MetricGroupValue{"Table Number": uint64(0), "Total Size": uint64(0), "Data Size": uint64(0), "Index Size": uint64(0)}
			}
			engineMetrics[engineDB]["Table Number"] = engineMetrics[engineDB]["Table Number"].(uint64) + count
			engineMetrics[engineDB]["Total Size"] = engineMetrics[engineDB]["Total Size"].(uint64) + size
			engineMetrics[engineDB]["Data Size"] = engineMetrics[engineDB]["Data Size"].(uint64) + dataSize
			engineMetrics[engineDB]["Index Size"] = engineMetrics[engineDB]["Index Size"].(uint64) + indexSize
		}
		if err := errors.Join(firstRowErr, rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
		if (i+1)%25 == 0 {
			time.Sleep(3 * time.Second)
		}
	}

	return engineMetrics, nil
}

func mergeEngineMetrics(support, sizes map[string]models.MetricGroupValue) map[string]models.MetricGroupValue {
	output := make(map[string]models.MetricGroupValue, len(support))
	for engine, supportMetrics := range support {
		sizeMetrics := sizes[engine]
		if sizeMetrics == nil {
			sizeMetrics = models.MetricGroupValue{"Table Number": uint64(0), "Total Size": uint64(0), "Data Size": uint64(0), "Index Size": uint64(0)}
		}
		output[engine] = utils.MapJoin(supportMetrics, sizeMetrics)
	}
	return output
}

func str_contains(slice []string, element string) bool {
	for _, v := range slice {
		if v == element {
			return true
		}
	}
	return false
}
