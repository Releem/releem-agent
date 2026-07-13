package tasks

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/Releem/mysqlconfigurer/task-automator/pkg/phase2"
	logging "github.com/google/logger"
)

type taskRepeaterStub struct {
	taskPayload string
	statuses    []models.Task
}

func (r *taskRepeaterStub) ProcessMetrics(_ models.MetricContext, metrics models.Metrics, mode models.ModeType) (string, error) {
	if mode.Name == "Task" && mode.Type == "Get" {
		return r.taskPayload, nil
	}
	if mode.Name == "Task" && mode.Type == "Status" {
		r.statuses = append(r.statuses, metrics.ReleemAgent.Tasks)
	}
	return "", nil
}

func TestProcessTaskSkipsTaskType6WhenDisabled(t *testing.T) {
	logger := *logging.Init("tasks-test", true, false, io.Discard)
	repeater := &taskRepeaterStub{
		taskPayload: `{"task_id":42,"task_type_id":6,"task_details":"{\"statements\":[{\"schema_name\":\"app\",\"ddl_statement\":\"ALTER TABLE app.users ADD COLUMN c INT\",\"analysis_results\":{\"schema_name\":\"app\",\"table_name\":\"users\",\"syntax_valid\":true,\"storage_engine\":\"InnoDB\",\"ok_online_ddl\":true,\"ok_pt_osc\":false,\"ok_online_physical_backup\":true,\"ok_pitr\":true}}]}"}`,
	}
	cfg := &config.Config{}

	ProcessTask(repeater, nil, logger, cfg)

	if len(repeater.statuses) != 2 {
		t.Fatalf("status updates = %d, want 2", len(repeater.statuses))
	}

	wantDetails := taskDetailsFromPayload(t, repeater.taskPayload)
	if repeater.statuses[0].Details != wantDetails {
		t.Fatalf("started status task_details = %q, want original task_details", repeater.statuses[0].Details)
	}
	if repeater.statuses[1].Details != wantDetails {
		t.Fatalf("final status task_details = %q, want original task_details", repeater.statuses[1].Details)
	}

	finalStatus := repeater.statuses[1]
	if finalStatus.ExitCode != 10 {
		t.Fatalf("final exit code = %d, want 10", finalStatus.ExitCode)
	}
	if finalStatus.Status != 4 {
		t.Fatalf("final status = %d, want 4", finalStatus.Status)
	}
	want := "schema change execution is disabled by config"
	if !strings.Contains(finalStatus.Output, want) {
		t.Fatalf("final output = %q, want skip reason %q", finalStatus.Output, want)
	}
	if !strings.Contains(finalStatus.Error, want) {
		t.Fatalf("final error = %q, want skip reason %q", finalStatus.Error, want)
	}
}

func TestProcessTaskRunsTaskType6WhenEnabled(t *testing.T) {
	logger := *logging.Init("tasks-test", true, false, io.Discard)
	repeater := &taskRepeaterStub{
		taskPayload: `{"task_id":42,"task_type_id":6,"task_details":"not-json"}`,
	}
	cfg := &config.Config{EnableExecDDL: true}

	ProcessTask(repeater, nil, logger, cfg)

	if len(repeater.statuses) != 2 {
		t.Fatalf("status updates = %d, want 2", len(repeater.statuses))
	}

	wantDetails := taskDetailsFromPayload(t, repeater.taskPayload)
	if repeater.statuses[0].Details != wantDetails {
		t.Fatalf("started status task_details = %q, want original task_details", repeater.statuses[0].Details)
	}
	if repeater.statuses[1].Details != wantDetails {
		t.Fatalf("final status task_details = %q, want original task_details", repeater.statuses[1].Details)
	}

	finalStatus := repeater.statuses[1]
	if finalStatus.ExitCode != 2 {
		t.Fatalf("final exit code = %d, want 2 from ApplySchemaChanges validation", finalStatus.ExitCode)
	}
	if finalStatus.Status != 4 {
		t.Fatalf("final status = %d, want 4", finalStatus.Status)
	}
	if !strings.Contains(finalStatus.Error, "taskdetails JSON") {
		t.Fatalf("final error = %q, want validation detail", finalStatus.Error)
	}

	var rawTask map[string]any
	if err := json.Unmarshal([]byte(repeater.taskPayload), &rawTask); err != nil {
		t.Fatalf("task payload must stay valid JSON: %v", err)
	}
}

func TestApplySchemaChangesSetsDetailedTaskErrorByExitCode(t *testing.T) {
	logger := *logging.Init("tasks-test", true, false, io.Discard)

	tests := []struct {
		name           string
		details        string
		cfg            *config.Config
		wantExitCode   int
		wantErrorText  string
		wantOutputText string
	}{
		{
			name:          "invalid payload",
			details:       "not-json",
			cfg:           &config.Config{},
			wantExitCode:  2,
			wantErrorText: "taskdetails JSON must be an object with a statements array",
		},
		{
			name:          "empty schema change list",
			details:       `{"statements":[]}`,
			cfg:           &config.Config{},
			wantExitCode:  3,
			wantErrorText: "empty schema change list",
		},
		{
			name:          "syntax invalid without extracted target",
			details:       `{"statements":[{"schema_name":"app","ddl_statement":"ALTER TABLE","analysis_results":{"schema_name":null,"table_name":null,"syntax_valid":false,"syntax_error":"expected table name","ok_online_ddl":false,"ok_pt_osc":false}}]}`,
			cfg:           &config.Config{},
			wantExitCode:  4,
			wantErrorText: "syntax validation failed: expected table name",
		},
		{
			name: "syntax validation failed",
			details: taskType6Details(`{
				"syntax_valid": false,
				"syntax_error": "near ADDD",
				"ok_online_ddl": true,
				"ok_pt_osc": false,
				"ok_pitr": true,
				"ok_online_physical_backup": true
			}`),
			cfg:           &config.Config{},
			wantExitCode:  4,
			wantErrorText: "Statement 0 skipped: syntax validation failed: near ADDD",
		},
		{
			name:          "target table missing from metadata",
			details:       `{"statements":[{"schema_name":"app","ddl_statement":"CREATE INDEX idx_missing ON app.missing_table(c)","analysis_results":{"schema_name":"app","table_name":"missing_table","syntax_valid":true,"storage_engine":"","ok_online_ddl":false,"ok_pt_osc":false,"ok_pitr":true,"ok_online_physical_backup":false}}]}`,
			cfg:           &config.Config{},
			wantExitCode:  5,
			wantErrorText: "Statement 0 skipped: target table app.missing_table was not found in metadata",
		},
		{
			name: "no safe execution method",
			details: taskType6Details(`{
				"syntax_valid": true,
				"ok_online_ddl": false,
				"ok_pt_osc": false,
				"ok_pitr": true,
				"ok_online_physical_backup": true
			}`),
			cfg:           &config.Config{},
			wantExitCode:  5,
			wantErrorText: "Statement 0 skipped: cannot be executed without blocking the table",
		},
		{
			name: "backup requires PITR",
			details: taskType6DetailsWithBackup(`{
				"syntax_valid": true,
				"ok_online_ddl": true,
				"ok_pt_osc": false,
				"ok_pitr": false,
				"ok_online_physical_backup": true
			}`),
			cfg:           &config.Config{},
			wantExitCode:  6,
			wantErrorText: "Statement 0 skipped: Point-in-time recovery is not possible",
		},
		{
			name: "execution failure includes executor detail",
			details: taskType6Details(`{
				"syntax_valid": true,
				"ok_online_ddl": true,
				"ok_pt_osc": false,
				"ok_pitr": true,
				"ok_online_physical_backup": true
			}`),
			cfg:            &config.Config{DisableSpaceChecks: true},
			wantExitCode:   7,
			wantErrorText:  "schema change execution failed: test schema is required for online DDL preflight",
			wantOutputText: "Statement 0 failed: schema change execution failed: test schema is required for online DDL preflight",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exitCode, status, output, taskError := ApplySchemaChanges(logger, tt.cfg, tt.details, 1023)

			if exitCode != tt.wantExitCode {
				t.Fatalf("exit code = %d, want %d", exitCode, tt.wantExitCode)
			}
			if status != 4 {
				t.Fatalf("status = %d, want 4", status)
			}
			if !strings.Contains(taskError, tt.wantErrorText) {
				t.Fatalf("task error = %q, want to contain %q", taskError, tt.wantErrorText)
			}
			wantOutputText := tt.wantOutputText
			if wantOutputText == "" {
				wantOutputText = tt.wantErrorText
			}
			if !strings.Contains(output, wantOutputText) {
				t.Fatalf("task output = %q, want to contain %q", output, wantOutputText)
			}
		})
	}
}

func TestFormatSchemaChangeExecutionResult(t *testing.T) {
	result := &phase2.ExecuteResult{
		MethodUsed: "pt-online-schema-change",
		Warnings:   []string{"Online DDL unavailable", "session cleanup failed"},
	}

	got := formatSchemaChangeExecutionResult(2, result)
	for _, want := range []string{
		"Statement 2 method: pt-online-schema-change\n",
		"Statement 2 warning: Online DDL unavailable\n",
		"Statement 2 warning: session cleanup failed\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatSchemaChangeExecutionResult() = %q, want %q", got, want)
		}
	}
}

func TestApplySchemaChangesIncludesExecutorMethodAndWarnings(t *testing.T) {
	logger := *logging.Init("tasks-test", true, false, io.Discard)
	originalFactory := newSchemaChangeExecutor
	var capturedOptions phase2.ExecuteOptions
	newSchemaChangeExecutor = func(logging.Logger) schemaChangeExecutor {
		return schemaChangeExecutorStub{
			result: &phase2.ExecuteResult{
				ChangeExecuted: true,
				MethodUsed:     "pt-online-schema-change",
				Warnings:       []string{"Online DDL unavailable; used pt-online-schema-change"},
			},
			onExecute: func(options phase2.ExecuteOptions) {
				capturedOptions = options
			},
		}
	}
	defer func() { newSchemaChangeExecutor = originalFactory }()

	exitCode, status, output, taskError := ApplySchemaChanges(
		logger,
		&config.Config{DisableSpaceChecks: true},
		taskType6Details(`{
			"syntax_valid": true,
			"ok_online_ddl": true,
			"ok_pt_osc": true,
			"ok_pitr": true,
			"ok_online_physical_backup": true
		}`),
		1023,
	)
	if exitCode != 0 || status != 1 || taskError != "" {
		t.Fatalf("ApplySchemaChanges() = exit %d status %d error %q", exitCode, status, taskError)
	}
	for _, want := range []string{
		"Statement 0 method: pt-online-schema-change",
		"Statement 0 warning: Online DDL unavailable; used pt-online-schema-change",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("ApplySchemaChanges() output = %q, want %q", output, want)
		}
	}
	if capturedOptions.Target == nil || *capturedOptions.Target != (phase2.TableInfo{Database: "app", Table: "users"}) {
		t.Fatalf("executor target = %#v, want structured Platform target", capturedOptions.Target)
	}
	if capturedOptions.TaskID != 1023 || capturedOptions.StatementIndex != 0 {
		t.Fatalf("executor correlation = task %d statement %d, want task 1023 statement 0", capturedOptions.TaskID, capturedOptions.StatementIndex)
	}
}

type schemaChangeExecutorStub struct {
	result    *phase2.ExecuteResult
	err       error
	onExecute func(phase2.ExecuteOptions)
}

func (s schemaChangeExecutorStub) Execute(options phase2.ExecuteOptions) (*phase2.ExecuteResult, error) {
	if s.onExecute != nil {
		s.onExecute(options)
	}
	return s.result, s.err
}

func taskType6Details(analysisJSON string) string {
	return taskType6DetailsWithBackupFlag(analysisJSON, false)
}

func taskType6DetailsWithBackup(analysisJSON string) string {
	return taskType6DetailsWithBackupFlag(analysisJSON, true)
}

func taskType6DetailsWithBackupFlag(analysisJSON string, preChangeBackup bool) string {
	return `{"statements":[{"schema_name":"app","ddl_statement":"ALTER TABLE app.users ADD COLUMN c INT","analysis_results":{"schema_name":"app","table_name":"users","storage_engine":"InnoDB",` +
		strings.Trim(strings.TrimSpace(analysisJSON), "{}") +
		`},"pre_change_bkp":` + boolLiteral(preChangeBackup) + `}]}`
}

func boolLiteral(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func taskDetailsFromPayload(t *testing.T, payload string) string {
	t.Helper()

	var rawTask struct {
		Details string `json:"task_details"`
	}
	if err := json.Unmarshal([]byte(payload), &rawTask); err != nil {
		t.Fatalf("task payload must stay valid JSON: %v", err)
	}
	return rawTask.Details
}
