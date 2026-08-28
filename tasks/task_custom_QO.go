package tasks

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/metrics/mysql"
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/Releem/mysqlconfigurer/utils"

	logging "github.com/google/logger"
)

type QueryExplainTaskInput struct {
	SchemaName          string `json:"schema_name"`
	QueryText           string `json:"query_text"`
	QueryOptimizationID int    `json:"id"`
}

type QueryExplainTaskResult struct {
	Schema                  map[string][]models.MetricGroupValue `json:"schema"`
	Explain                 string                               `json:"explain"`
	EventsStatementsHistory models.MetricGroupValue              `json:"events_statements_history"`
}

type queryExplainDatabaseConnector func(*config.Config, logging.Logger, string) (*sql.DB, error)

func executeQueryExplain(connect queryExplainDatabaseConnector, configuration *config.Config, logger logging.Logger, schemaName string, queryText string) (string, error) {
	db, err := connect(configuration, logger, schemaName)
	if err != nil {
		return "", err
	}
	if db == nil {
		return "", fmt.Errorf("connect to database %q: nil database handle", schemaName)
	}
	defer db.Close()
	return mysql.ExecuteExplain(db, queryText, logger)
}

func ProcessQueryExplainTask(task_details string, logger logging.Logger, configuration *config.Config, metrics *models.Metrics) (int, int, string, string) {
	var task_exit_code, task_status int = 0, 1
	var task_output, task_error string

	// Parse JSON from Task Details
	var inputs []QueryExplainTaskInput
	err := json.Unmarshal([]byte(task_details), &inputs)
	if err != nil {
		logger.Error("Failed to parse task task_details JSON: ", err)
		task_exit_code = 2
		task_status = 4
		task_error = task_error + "Failed to collect data for optimizing your query. Please write us in the chat or send an email to hello@releem.com.\nWe will do our best to resolve the issue."
		task_output = task_output + fmt.Sprintf("Error parsing JSON: %v\n", err)
		return task_exit_code, task_status, task_output, task_error
	} else {
		task_output = task_output + "Successfully parsed task_details JSON\n"
	}

	if len(inputs) == 0 {
		logger.Error("task_details array is empty")
		task_exit_code = 2
		task_status = 4
		task_error = task_error + "Failed to collect data for optimizing your query. Please write us in the chat or send an email to hello@releem.com.\nWe will do our best to resolve the issue."
		task_output = task_output + "Error: task_details must contain at least one item\n"
		return task_exit_code, task_status, task_output, task_error
	} else {
		task_output = task_output + "task_details array is not empty\n"
	}

	metrics.DB.DatabaseSchema = make(map[string][]models.MetricGroupValue)
	metrics.DB.Queries = make([]models.MetricGroupValue, 0, len(inputs))
	collectedSchemas := make(map[string]struct{})

	for _, input := range inputs {
		if input.SchemaName == "" || input.QueryText == "" || strings.TrimSpace(input.SchemaName) == "" || strings.TrimSpace(input.QueryText) == "" {
			logger.Error("schema_name or query_text is empty")
			task_exit_code = 3
			task_status = 4
			task_error = task_error + "The Database name and SQL query are required."
			task_output = task_output + "Failed to parse data:\nThe Database name and SQL query are required\n"
			return task_exit_code, task_status, task_output, task_error
		} else {
			task_output = task_output + "schema_name and query_text are not empty\n"
		}
		// Check that input.SchemaName exists in metrics.DB.Metrics.Databases
		found := false
		for _, dbName := range metrics.DB.Metrics.Databases {
			if dbName == input.SchemaName {
				found = true
				break
			}
		}
		if !found {
			logger.Error("schema_name does not exist in metrics.DB.Metrics.Databases: ", input.SchemaName)
			task_exit_code = 4
			task_status = 4
			task_error = task_error + fmt.Sprintf("The `%s` database does not exist.\nPlease check the database name and try again.\n", input.SchemaName)
			task_output = task_output + fmt.Sprintf("Failed to collect schema for the `%s` database:\nThe `%s` database does not exist\n", input.SchemaName, input.SchemaName)
			return task_exit_code, task_status, task_output, task_error
		}
		query_data := models.MetricGroupValue{
			"schema_name": input.SchemaName,
			"query_text":  input.QueryText,
			"avg_time_us": 0,
			"calls":       0,
			"sum_time_us": 0,
		}
		if _, ok := collectedSchemas[input.SchemaName]; !ok {
			// Collect schema
			err = mysql.CollectDbSchema(input.SchemaName, logger, metrics)
			if err != nil {
				logger.Error("Failed to collect schema: ", err)
				task_exit_code = 5
				task_status = 4
				task_error = task_error + fmt.Sprintf("Failed to collect schema for the `%s` database:\n%v\nPlease write us in the chat or send an email to hello@releem.com.\nWe will do our best to resolve the issue.", input.SchemaName, err)
				task_output = task_output + fmt.Sprintf("Failed to collect schema for the `%s` database:\n%v\n", input.SchemaName, err)
				return task_exit_code, task_status, task_output, task_error
			} else {
				collectedSchemas[input.SchemaName] = struct{}{}
				task_output = task_output + fmt.Sprintf("Successfully collected schema for schema_name: %s\n", input.SchemaName)
			}
		}

		// // Get THREAD_ID before executing EXPLAIN (using the same connection)
		// var threadID uint64
		// err = db.QueryRow("SELECT THREAD_ID FROM performance_schema.threads WHERE PROCESSLIST_ID = CONNECTION_ID()").Scan(&threadID)
		// if err != nil {
		// 	logger.Error("Failed to get THREAD_ID: ", err)
		// 	task_exit_code = 8
		// 	task_status = 4
		// 	task_output = fmt.Sprintf("Error getting THREAD_ID: %v\n", err)
		// 	return task_exit_code, task_status, task_output, task_error
		// }
		// logger.Info("THREAD_ID: ", threadID)

		// Connect to the database and execute EXPLAIN
		explainResult, explain_error := executeQueryExplain(utils.ConnectionDatabaseErr, configuration, logger, input.SchemaName, input.QueryText)
		// explainResult, err := executeExplain(db, input.QueryText, logger)
		if explainResult != "" {
			query_data["explain"] = explainResult
			task_output = task_output + fmt.Sprintf("Successfully executed EXPLAIN for QueryOptimizationID: %d\n", input.QueryOptimizationID)
		} else if explain_error != nil {
			logger.Errorf("Failed to execute EXPLAIN for QueryOptimizationID: %d: %v\n", input.QueryOptimizationID, explain_error)
			query_data["explain_error"] = explain_error.Error()
			task_status = 4
			if strings.Contains(explain_error.Error(), "need_grant_permission") {
				task_exit_code = 6
				task_error = task_error + "MySQL 'releem' user lacks required permissions.\nPlease grant the necessary permissions to the user and try again.\n"
				task_output = task_output + fmt.Sprintf("Need grant permission for QueryOptimizationID: %d\n", input.QueryOptimizationID)
			} else {
				task_exit_code = 7
				task_error = task_error + fmt.Sprintf("Failed to execute EXPLAIN:\n%v\nPlease check the query and try again.\n", explain_error)
				task_output = task_output + fmt.Sprintf("Failed to execute EXPLAIN for QueryOptimizationID: %d: %v\n", input.QueryOptimizationID, explain_error)
			}
		}

		// // Get last row from events_statements_history for THREAD_ID
		// var Digest string
		// err = db.QueryRow("SELECT IFNULL(DIGEST, 'NULL') as digest FROM performance_schema.events_statements_history WHERE THREAD_ID = ? AND EVENT_NAME = 'statement/sql/select' ORDER BY EVENT_ID DESC LIMIT 1", threadID).Scan(&Digest)
		// if err != nil {
		// 	logger.Error("Failed to get digest hash: ", err)
		// 	task_exit_code = 8
		// 	task_status = 4
		// 	task_output = fmt.Sprintf("Failed to get digest hash: %v\n", err)
		// 	return task_exit_code, task_status, task_output, task_error
		// }
		// defer rows.Close()

		// for rows.Next() {
		// 	err := rows.Scan(&Digest, &DigestText)
		// 	if err != nil {
		// 		logger.Error("Failed to scan rows: ", err)
		// 	}
		// 	logger.Info("Digest: ", Digest, " DigestText: ", DigestText)
		// }

		// Build result
		metrics.DB.Queries = append(metrics.DB.Queries, query_data)
	}

	return task_exit_code, task_status, task_output, task_error
}

// func getEventsStatementsHistory(db *sql.DB, threadID uint64, logger logging.Logger) (models.MetricGroupValue, error) {
// 	// Use SELECT * to get all columns dynamically
// 	var Digest string
// 	err := db.QueryRow("SELECT DIGEST FROM performance_schema.events_statements_history WHERE THREAD_ID = ? ORDER BY EVENT_ID DESC LIMIT 1", threadID).Scan(&Digest)
// 	if err != nil {
// 		return nil, err
// 	}
// 	defer rows.Close()

// 	if !rows.Next() {
// 		return models.MetricGroupValue{}, nil
// 	}

// 	// Get column names
// 	cols, err := rows.Columns()
// 	if err != nil {
// 		return nil, err
// 	}

// 	// Prepare values
// 	values := make([]interface{}, len(cols))
// 	ptrs := make([]interface{}, len(cols))
// 	for i := range values {
// 		ptrs[i] = &values[i]
// 	}

// 	err = rows.Scan(ptrs...)
// 	if err != nil {
// 		return nil, err
// 	}

// 	// Build result map
// 	result := make(models.MetricGroupValue)
// 	for i, col := range cols {
// 		v := values[i]
// 		switch vv := v.(type) {
// 		case []byte:
// 			result[col] = string(vv)
// 		case nil:
// 			result[col] = nil
// 		default:
// 			result[col] = vv
// 		}
// 	}

// 	return result, nil
// }
