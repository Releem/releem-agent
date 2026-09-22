package tasks

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/Releem/mysqlconfigurer/utils"
	logging "github.com/google/logger"
)

func ApplyConfLocal(metrics *models.Metrics, repeaters models.MetricsRepeater, gatherers []models.MetricsGatherer, logger logging.Logger, configuration *config.Config) (int, int, string) {
	var task_exit_code, task_status int
	var task_output string

	result_data := models.MetricGroupValue{}
	// flush_queries := []string{"flush status", "flush statistic"}
	need_restart := false
	need_privileges := false
	need_flush := false
	error_exist := false

	recommend_var := utils.ProcessRepeaters(metrics, repeaters, configuration, logger, models.ModeType{Name: "Configurations", Type: "GetJson", ApplyMode: "dynamic"})
	err := json.Unmarshal([]byte(recommend_var), &result_data)
	if err != nil {
		logger.Error(err)
	}

	operations := localDynamicApplyQueries(configuration.GetDatabaseType(), result_data, metrics.DB.Conf.Variables)
	for _, operation := range operations {
		for _, name := range operation.parameterNames {
			logger.Infof("%s: %v -> %v", name, metrics.DB.Conf.Variables[name], result_data[name])
		}

		_, err := models.DB.Exec(operation.query)
		if err != nil {
			logger.Error(err)
			task_output = task_output + err.Error()
			if strings.Contains(err.Error(), "is a read only variable") || strings.Contains(err.Error(), "innodb_log_file_size must be at least") {
				need_restart = true
			} else if strings.Contains(err.Error(), "Access denied") || strings.Contains(err.Error(), "permission denied") {
				need_privileges = true
			} else {
				error_exist = true
			}
		} else {
			need_flush = true
		}
	}
	logger.Info(need_flush, need_restart, need_privileges, error_exist)
	if error_exist {
		task_exit_code = 8
		task_status = 4
	} else {
		// if need_flush {
		// 	for _, query := range flush_queries {
		// 		_, err := config.DB.Exec(query)
		// 		if err != nil {
		// 			taskStruct.Output = taskStruct.Output + err.Error()
		// 			logger.Error(err)
		// 			// if exiterr, ok := err.(*exec.ExitError); ok {
		// 			// 	taskStruct.ExitCode = exiterr.ExitCode()
		// 			// } else {
		// 			// 	taskStruct.ExitCode = 999
		// 			// }
		// 		}
		// 		// } else {
		// 		// 	taskStruct.ExitCode = 0
		// 		// }
		// 	}
		// }
		if need_privileges {
			task_exit_code = 9
			task_status = 4
		} else if need_restart {
			task_exit_code = 10
			task_status = 1
		} else {
			task_exit_code = 0
			task_status = 1
		}
	}
	time.Sleep(10 * time.Second)

	return task_exit_code, task_status, task_output
}

type localDynamicApplyOperation struct {
	query          string
	parameterNames []string
}

func localDynamicApplyQueries(databaseType string, recommendations, current models.MetricGroupValue) []localDynamicApplyOperation {
	changed := make([]string, 0, len(recommendations))
	for name, value := range recommendations {
		if configurationValueString(value) == configurationValueString(current[name]) {
			continue
		}
		changed = append(changed, name)
	}
	if len(changed) == 0 {
		return nil
	}
	sort.Strings(changed)
	if databaseType == "postgresql" {
		return []localDynamicApplyOperation{{
			query:          "SELECT pg_reload_conf()",
			parameterNames: changed,
		}}
	}

	operations := make([]localDynamicApplyOperation, 0, len(changed))
	for _, name := range changed {
		operations = append(operations, localDynamicApplyOperation{
			query:          "set global " + name + "=" + configurationValueString(recommendations[name]),
			parameterNames: []string{name},
		})
	}
	return operations
}

func configurationValueString(value interface{}) string {
	if setting, ok := value.(map[string]interface{}); ok {
		value = setting["setting"]
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
