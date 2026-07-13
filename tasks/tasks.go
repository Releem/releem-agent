package tasks

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/Releem/mysqlconfigurer/task-automator/pkg/phase2"
	"github.com/Releem/mysqlconfigurer/utils"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	logging "github.com/google/logger"
)

type schemaChangeExecutor interface {
	Execute(phase2.ExecuteOptions) (*phase2.ExecuteResult, error)
}

var newSchemaChangeExecutor = func(logger logging.Logger) schemaChangeExecutor {
	return phase2.NewExecutor(models.DB, &logger)
}

func ProcessTaskFunc(repeaters models.MetricsRepeater, gatherers []models.MetricsGatherer, logger logging.Logger, configuration *config.Config) func() {
	return func() {
		ProcessTask(repeaters, gatherers, logger, configuration)
	}
}

func ProcessTask(repeaters models.MetricsRepeater, gatherers []models.MetricsGatherer, logger logging.Logger, configuration *config.Config) {
	defer utils.HandlePanic(configuration, logger)
	var TaskStruct *models.Task
	var task_output string

	metrics := utils.CollectMetrics(gatherers, logger, configuration)
	RepeaterResponse := utils.ProcessRepeaters(metrics, repeaters, configuration, logger, models.ModeType{Name: "Task", Type: "Get"})
	logger.Infof("Task details: %s", RepeaterResponse)

	err := json.Unmarshal([]byte(RepeaterResponse), &TaskStruct)
	if err != nil {
		logger.Error("Failed to parse task description JSON: ", err)
		return
	}
	if TaskStruct == nil {
		return
	}

	startedTask := *TaskStruct
	startedTask.Status = 3
	metrics.ReleemAgent.Tasks = startedTask
	utils.ProcessRepeaters(metrics, repeaters, configuration, logger, models.ModeType{Name: "Task", Type: "Status"})
	logger.Infof(" * Task with id - %d and type id - %d is being started...", TaskStruct.ID, TaskStruct.TypeID)

	switch TaskStruct.TypeID {
	case 0:
		TaskStruct.ExitCode, TaskStruct.Status, task_output = execTaskCommand(taskApplyManualCommand(runtime.GOOS, configuration.ReleemDir), logger)
		TaskStruct.Output = TaskStruct.Output + task_output

		if TaskStruct.ExitCode == 7 {
			var rollback_exit_code int
			rollback_exit_code, _, task_output = execTaskCommand(taskRollbackCommand(runtime.GOOS, configuration.ReleemDir), logger)
			TaskStruct.Output = TaskStruct.Output + task_output
			logger.Info(" * Task rollbacked with code ", rollback_exit_code)
		}

	case 1:
		TaskStruct.ExitCode, TaskStruct.Status, task_output = execTaskCommand(taskGenerateConfigCommand(runtime.GOOS, configuration.ReleemDir), logger)
		TaskStruct.Output = TaskStruct.Output + task_output
	case 2:
		TaskStruct.ExitCode, TaskStruct.Status, task_output = execTaskCommand(taskUpdateCommand(runtime.GOOS, configuration.ReleemDir), logger)
		TaskStruct.Output = TaskStruct.Output + task_output
	case 3:
		TaskStruct.ExitCode, TaskStruct.Status, task_output = execTaskCommand(taskQueriesOptimizationCommand(runtime.GOOS, configuration.ReleemDir), logger)
		TaskStruct.Output = TaskStruct.Output + task_output
	case 4:
		switch configuration.InstanceType {
		case "aws/rds":
			TaskStruct.ExitCode, TaskStruct.Status, task_output = ApplyConfAwsRds(repeaters, gatherers, logger, configuration, types.ApplyMethodImmediate)
			TaskStruct.Output = TaskStruct.Output + task_output
			if TaskStruct.ExitCode == 0 {
				TaskStruct.ExitCode, TaskStruct.Status, task_output = ApplyConfAwsRds(repeaters, gatherers, logger, configuration, types.ApplyMethodPendingReboot)
				TaskStruct.Output = TaskStruct.Output + task_output
			}
		case "gcp/cloudsql":
			TaskStruct.ExitCode, TaskStruct.Status, task_output = ApplyConfGcpCloudSQL(repeaters, gatherers, logger, configuration)
			TaskStruct.Output = TaskStruct.Output + task_output
		case "azure/mysql":
			TaskStruct.ExitCode, TaskStruct.Status, task_output = ApplyConfAzureMySQL(repeaters, gatherers, logger, configuration, false)
			TaskStruct.Output = TaskStruct.Output + task_output

		default:
			TaskStruct.ExitCode, TaskStruct.Status, task_output = execTaskCommand(taskApplyAutomaticCommand(runtime.GOOS, configuration.ReleemDir, false), logger)
			TaskStruct.Output = TaskStruct.Output + task_output
			if TaskStruct.ExitCode == 7 {
				var rollback_exit_code int
				rollback_exit_code, _, task_output = execTaskCommand(taskRollbackCommand(runtime.GOOS, configuration.ReleemDir), logger)
				TaskStruct.Output = TaskStruct.Output + task_output
				logger.Info(" * Task rollbacked with code ", rollback_exit_code)
			}

			if TaskStruct.ExitCode == 0 {
				TaskStruct.ExitCode, TaskStruct.Status, task_output = ApplyConfLocal(metrics, repeaters, gatherers, logger, configuration)
				TaskStruct.Output = TaskStruct.Output + task_output
			}
		}

	case 5:
		switch configuration.InstanceType {
		case "aws/rds":
			TaskStruct.ExitCode, TaskStruct.Status, task_output = ApplyConfAwsRds(repeaters, gatherers, logger, configuration, types.ApplyMethodPendingReboot)
			TaskStruct.Output = TaskStruct.Output + task_output
		case "gcp/cloudsql":
			TaskStruct.ExitCode, TaskStruct.Status, task_output = ApplyConfGcpCloudSQL(repeaters, gatherers, logger, configuration)
			TaskStruct.Output = TaskStruct.Output + task_output
		case "azure/mysql":
			TaskStruct.ExitCode, TaskStruct.Status, task_output = ApplyConfAzureMySQL(repeaters, gatherers, logger, configuration, true)
			TaskStruct.Output = TaskStruct.Output + task_output

		default:
			TaskStruct.ExitCode, TaskStruct.Status, task_output = execTaskCommand(taskApplyAutomaticCommand(runtime.GOOS, configuration.ReleemDir, true), logger)
			TaskStruct.Output = TaskStruct.Output + task_output
			if TaskStruct.ExitCode == 7 {
				var rollback_exit_code int
				rollback_exit_code, _, task_output = execTaskCommand(taskRollbackCommand(runtime.GOOS, configuration.ReleemDir), logger)
				TaskStruct.Output = TaskStruct.Output + task_output
				logger.Info(" * Task rollbacked with code ", rollback_exit_code)
			}
		}

	case 6:
		if configuration == nil || !configuration.EnableExecDDL {
			TaskStruct.ExitCode = 10
			TaskStruct.Status = 4
			errMsg := "Task skipped: schema change execution is disabled by config (enable_exec_ddl=false)."
			TaskStruct.Output = TaskStruct.Output + errMsg + "\n"
			TaskStruct.Error = errMsg
			logger.Info(errMsg)
			break
		}
		TaskStruct.ExitCode, TaskStruct.Status, TaskStruct.Output, TaskStruct.Error = ApplySchemaChanges(
			logger, configuration, TaskStruct.Details, TaskStruct.ID,
		)

	case 7:
		TaskStruct.ExitCode, TaskStruct.Status, TaskStruct.Output, TaskStruct.Error = ProcessQueryExplainTask(
			TaskStruct.Details, logger, configuration, metrics)
		if TaskStruct.ExitCode == 0 {
			metrics.ReleemAgent.Tasks = *TaskStruct
			RepeaterResponse := utils.ProcessRepeaters(metrics, repeaters, configuration, logger, models.ModeType{Name: "TaskByName", Type: "custom_queries_optimization"})

			mergedJSON, err := utils.MergeJSONStrings(TaskStruct.Details, RepeaterResponse, "custom_queries_optimization_response")
			if err != nil {
				logger.Error("Failed to merge task_details JSON: ", err)
			} else {
				TaskStruct.Details = mergedJSON
			}
		}
	default:
		TaskStruct.ExitCode = 4 // unknown task type
		TaskStruct.Status = 4
	}

	time.Sleep(10 * time.Second)
	metrics = utils.CollectMetrics(gatherers, logger, configuration)
	logger.Infof(" * Task with id - %d and type id - %d completed with code %d", TaskStruct.ID, TaskStruct.TypeID, TaskStruct.ExitCode)

	metrics.ReleemAgent.Tasks = *TaskStruct
	utils.ProcessRepeaters(metrics, repeaters, configuration, logger, models.ModeType{Name: "Task", Type: "Status"})
}

func ApplySchemaChanges(logger logging.Logger, configuration *config.Config, taskdetails string, taskID int) (int, int, string, string) {
	var task_exit_code, task_status int = 0, 1
	var task_output, task_error string
	var backupMethod phase2.BackupMethod

	fail := func(exitCode int, outputMsg string, errorMsg string) (int, int, string, string) {
		task_exit_code = exitCode
		task_status = 4
		if outputMsg != "" {
			task_output += outputMsg
		}
		if errorMsg != "" {
			task_error = errorMsg
		} else if outputMsg != "" {
			task_error = outputMsg
		}
		return task_exit_code, task_status, task_output, task_error
	}

	type schemaChangeAnalysisResults struct {
		SchemaName             string      `json:"schema_name"`
		TableName              string      `json:"table_name"`
		SyntaxValid            bool        `json:"syntax_valid"`
		SyntaxError            interface{} `json:"syntax_error"`
		StorageEngine          string      `json:"storage_engine"`
		OKOnlineDDL            bool        `json:"ok_online_ddl"`
		OKPTOSC                bool        `json:"ok_pt_osc"`
		OKOnlinePhysicalBackup bool        `json:"ok_online_physical_backup"`
		OKPTR                  bool        `json:"ok_pitr"`
	}

	type schemaChangeTaskDetail struct {
		SchemaName      string                      `json:"schema_name"`
		DDLStatement    string                      `json:"ddl_statement"`
		AnalysisResults schemaChangeAnalysisResults `json:"analysis_results"`
		PreChangeBackup bool                        `json:"pre_change_bkp"`
	}

	type TaskDetails struct {
		Type       string                   `json:"type"`
		ID         int                      `json:"id"`
		Statements []schemaChangeTaskDetail `json:"statements"`
	}

	parseTaskDetails := func() (TaskDetails, error) {
		var details TaskDetails
		if err := json.Unmarshal([]byte(taskdetails), &details); err != nil {
			logger.Error("Failed to parse task_details JSON: ", err)
			return TaskDetails{}, fmt.Errorf("taskdetails JSON must be an object with a statements array")
		}

		for i, item := range details.Statements {
			if strings.TrimSpace(item.SchemaName) == "" {
				return TaskDetails{}, fmt.Errorf("taskdetails[%d].schema_name is required", i)
			}

			ddl := strings.TrimSpace(item.DDLStatement)
			if ddl == "" {
				return TaskDetails{}, fmt.Errorf("taskdetails[%d].ddl_statement is required", i)
			}

			if item.AnalysisResults.SyntaxValid {
				analysisSchema := strings.TrimSpace(item.AnalysisResults.SchemaName)
				if analysisSchema == "" {
					return TaskDetails{}, fmt.Errorf("taskdetails[%d].analysis_results.schema_name is required", i)
				}
				if strings.TrimSpace(item.AnalysisResults.TableName) == "" {
					return TaskDetails{}, fmt.Errorf("taskdetails[%d].analysis_results.table_name is required", i)
				}
			}
		}

		return details, nil
	}

	details, err := parseTaskDetails()
	if err != nil {
		logger.Error(err)
		return fail(2, err.Error(), "")
	}
	if len(details.Statements) == 0 {
		logger.Error("Invalid task_details: empty schema change list.")
		return fail(3, "Invalid task_details: empty schema change list.", "")
	}

	logger.Info("* Executing schema changes...")
	executor := newSchemaChangeExecutor(logger)
	executed := 0
	for i, item := range details.Statements {
		backupMethod = phase2.BackupNone
		analysis := item.AnalysisResults
		statement := strings.TrimSpace(item.DDLStatement)
		target := phase2.TableInfo{
			Database: strings.TrimSpace(analysis.SchemaName),
			Table:    strings.TrimSpace(analysis.TableName),
		}

		if !analysis.SyntaxValid {
			errMsg := "syntax validation failed"
			if analysis.SyntaxError != nil {
				errMsg = fmt.Sprintf("syntax validation failed: %v", analysis.SyntaxError)
			}
			logger.Errorf("Statement %d skipped: %s\n", i, errMsg)
			return fail(4, fmt.Sprintf("Statement %d skipped: %s\n", i, errMsg), "")
		}

		if strings.TrimSpace(analysis.StorageEngine) == "" {
			errMsg := fmt.Sprintf(
				"Statement %d skipped: target table %s.%s was not found in metadata\n",
				i, target.Database, target.Table,
			)
			logger.Error(errMsg)
			return fail(5, errMsg, "")
		}

		if !analysis.OKOnlineDDL && !analysis.OKPTOSC {
			logger.Errorf("Statement %d skipped: cannot be executed without blocking the table\n", i)
			return fail(5, fmt.Sprintf("Statement %d skipped: cannot be executed without blocking the table\n", i), "")
		}

		if !strings.EqualFold(strings.TrimSpace(analysis.StorageEngine), "InnoDB") {
			task_output += fmt.Sprintf("Statement %d warning: storage engine is %s\n", i, analysis.StorageEngine)
		}

		if item.PreChangeBackup {
			logger.Infof("Statement %d note: Pre-change backup is required\n", i)
			task_output += fmt.Sprintf("Statement %d note: Pre-change backup is required\n", i)

			if !analysis.OKPTR {
				logger.Infof("Statement %d note: Point-in-time recovery is not possible - will not proceed with the schema change\n", i)
				return fail(6, fmt.Sprintf("Statement %d skipped: Point-in-time recovery is not possible - will not proceed with the schema change\n", i), "")
			}

			if !analysis.OKOnlinePhysicalBackup {
				logger.Infof("Statement %d note: online physical backup is not possible - doing a logical backup\n", i)
				task_output += fmt.Sprintf("Statement %d note: online physical backup is not possible - doing a logical backup\n", i)
				backupMethod = phase2.BackupMysqldump
			} else {
				backupMethod = phase2.BackupXtrabackup
			}
		}

		executionResult, err := executor.Execute(phase2.ExecuteOptions{
			TaskID:         taskID,
			StatementIndex: i,
			SQL:            statement,
			Target:         &target,
			BackupMethod:   backupMethod,
			OkPTOSC:        analysis.OKPTOSC,
			OkOnlineDDL:    analysis.OKOnlineDDL,
			Config:         configuration,
			Debug:          configuration.Debug,
		})
		if err != nil {
			logger.Infof("* Statement %d failed: %s\n", i, err.Error())
			return fail(7, fmt.Sprintf("Statement %d failed: %s\n", i, err.Error()), err.Error())

		}

		executed++
		task_output += formatSchemaChangeExecutionResult(i, executionResult)
		logger.Info("* Schema changes execution successful for statement ", i)
		task_output += fmt.Sprintf("Statement %d successful: %s\n", i, statement)
	}

	if executed == 0 && len(details.Statements) > 0 {
		logger.Info("No schema changes were executed.")
		return fail(8, "No schema changes were executed.\n", "")
	}

	return task_exit_code, task_status, task_output, task_error
}

func formatSchemaChangeExecutionResult(statementIndex int, result *phase2.ExecuteResult) string {
	if result == nil {
		return ""
	}

	var output strings.Builder
	if result.MethodUsed != "" {
		fmt.Fprintf(&output, "Statement %d method: %s\n", statementIndex, result.MethodUsed)
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(&output, "Statement %d warning: %s\n", statementIndex, warning)
	}
	return output.String()
}
