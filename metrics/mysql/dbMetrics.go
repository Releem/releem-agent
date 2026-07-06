package mysql

import (
	"database/sql"
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
	logger        logging.Logger
	configuration *config.Config
}

func NewDBMetricsConfigGatherer(logger logging.Logger, configuration *config.Config) *DBMetricsConfigGatherer {
	return &DBMetricsConfigGatherer{
		logger:        logger,
		configuration: configuration,
	}
}

func (DBMetricsConfig *DBMetricsConfigGatherer) GetMetrics(metrics *models.Metrics) error {
	defer utils.HandlePanic(DBMetricsConfig.configuration, DBMetricsConfig.logger)

	//Stat mysql Engine
	{
		var engine_db, engineenabled string
		var size, count, dsize, isize uint64
		output := make(map[string]models.MetricGroupValue)
		engine_elem := make(map[string]models.MetricGroupValue)

		rows, err := models.DB.Query("SELECT ENGINE,SUPPORT FROM information_schema.ENGINES ORDER BY ENGINE ASC")
		if err != nil {
			DBMetricsConfig.logger.Error(err)
			return err
		}
		for rows.Next() {
			err := rows.Scan(&engine_db, &engineenabled)
			if err != nil {
				DBMetricsConfig.logger.Error(err)
				return err
			}
			output[engine_db] = models.MetricGroupValue{"Enabled": engineenabled}
			engine_elem[engine_db] = models.MetricGroupValue{"Table Number": uint64(0), "Total Size": uint64(0), "Data Size": uint64(0), "Index Size": uint64(0)}
		}
		rows.Close()
		i := 0
		for _, database := range metrics.DB.Metrics.Databases {
			rows, err = models.DB.Query(`SELECT ENGINE, IFNULL(SUM(DATA_LENGTH+INDEX_LENGTH), 0), IFNULL(COUNT(ENGINE), 0), IFNULL(SUM(DATA_LENGTH), 0), IFNULL(SUM(INDEX_LENGTH), 0) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND ENGINE IS NOT NULL  GROUP BY ENGINE ORDER BY ENGINE ASC`, database)
			if err != nil {
				DBMetricsConfig.logger.Error(err)
				return err
			}
			for rows.Next() {
				err := rows.Scan(&engine_db, &size, &count, &dsize, &isize)
				if err != nil {
					DBMetricsConfig.logger.Error(err)
					continue
				}
				if engine_elem[engine_db]["Table Number"] == nil {
					engine_elem[engine_db] = models.MetricGroupValue{"Table Number": uint64(0), "Total Size": uint64(0), "Data Size": uint64(0), "Index Size": uint64(0)}
				}
				engine_elem[engine_db]["Table Number"] = engine_elem[engine_db]["Table Number"].(uint64) + count
				engine_elem[engine_db]["Total Size"] = engine_elem[engine_db]["Total Size"].(uint64) + size
				engine_elem[engine_db]["Data Size"] = engine_elem[engine_db]["Data Size"].(uint64) + dsize
				engine_elem[engine_db]["Index Size"] = engine_elem[engine_db]["Index Size"].(uint64) + isize
			}
			rows.Close()
			i += 1
			if i%25 == 0 {
				time.Sleep(3 * time.Second)
			}
		}
		for k := range output {
			output[k] = utils.MapJoin(output[k], engine_elem[k])
		}

		metrics.DB.Metrics.Engine = output
		if metrics.DB.Metrics.Engine["MyISAM"] == nil {
			metrics.DB.Metrics.TotalMyisamIndexes = 0
		} else {
			metrics.DB.Metrics.TotalMyisamIndexes = metrics.DB.Metrics.Engine["MyISAM"]["Index Size"].(uint64)
		}
	}
	DBMetricsConfig.logger.V(5).Info("CollectMetrics DBMetricsConfig ", metrics.DB.Metrics)

	return nil
}

func str_contains(slice []string, element string) bool {
	for _, v := range slice {
		if v == element {
			return true
		}
	}
	return false
}
