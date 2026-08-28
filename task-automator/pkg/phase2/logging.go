package phase2

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type Logger interface {
	Infof(format string, v ...interface{})
	Errorf(format string, v ...interface{})
}

type executionStage struct {
	executor *Executor
	options  ExecuteOptions
	name     string
	started  time.Time
	finished bool
}

type executionStageError struct {
	stage string
	err   error
}

func (e *executionStageError) Error() string { return e.err.Error() }
func (e *executionStageError) Unwrap() error { return e.err }

func wrapExecutionStageError(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &executionStageError{stage: stage, err: err}
}

func executionStageFailureReason(err error) string {
	var stageErr *executionStageError
	if errors.As(err, &stageErr) && stageErr.stage != "" {
		return "failed_stage=" + stageErr.stage
	}
	return err.Error()
}

func (e *Executor) beginStage(options ExecuteOptions, name string) *executionStage {
	stage := &executionStage{executor: e, options: options, name: name, started: time.Now()}
	e.logStage(options, name, "started", 0, "")
	return stage
}

func (s *executionStage) finish(status, reason string) {
	if s == nil || s.finished {
		return
	}
	s.finished = true
	s.executor.logStage(s.options, s.name, status, time.Since(s.started), reason)
}

func (e *Executor) logStage(options ExecuteOptions, stage, status string, duration time.Duration, reason string) {
	if e.logger == nil {
		return
	}
	table := executionLogTable(options)
	message := fmt.Sprintf(
		"Schema change check: task_id=%d statement_index=%d stage=%s status=%s table=%s",
		options.TaskID, options.StatementIndex, stage, status, table,
	)
	if status != "started" {
		message += fmt.Sprintf(" duration_ms=%d", duration.Milliseconds())
	}
	if reason = sanitizeExecutionLogReason(options, reason); reason != "" {
		message += fmt.Sprintf(" reason=%q", reason)
	}
	if status == "failed" && !strings.HasPrefix(reason, "failed_stage=") {
		e.logger.Errorf("%s", message)
		return
	}
	e.logger.Infof("%s", message)
}

func executionLogTable(options ExecuteOptions) string {
	if options.Target != nil && options.Target.Database != "" && options.Target.Table != "" {
		return options.Target.Database + "." + options.Target.Table
	}
	if table := strings.Trim(options.TableName, " `"); table != "" {
		return strings.ReplaceAll(table, "`.`", ".")
	}
	return "unknown"
}

func sanitizeExecutionLogReason(options ExecuteOptions, reason string) string {
	reason = strings.Join(strings.Fields(reason), " ")
	return redactExecutionLogText(options, reason)
}

func redactExecutionLogText(options ExecuteOptions, value string) string {
	if options.Config == nil || options.Config.MysqlPassword == "" {
		return value
	}
	return strings.ReplaceAll(value, options.Config.MysqlPassword, "***")
}

func redactCommandArgs(args []string, password string) []string {
	redacted := append([]string(nil), args...)
	if password == "" {
		return redacted
	}
	for i := range redacted {
		redacted[i] = strings.ReplaceAll(redacted[i], password, "***")
	}
	return redacted
}

func (e *Executor) logExternalCommandOutput(options ExecuteOptions, tool, phase string, output []byte, commandErr error) {
	if len(output) == 0 || (!options.Debug && commandErr == nil) {
		return
	}
	message := fmt.Sprintf(
		"Schema change command output: task_id=%d statement_index=%d tool=%s phase=%s output:\n%s",
		options.TaskID, options.StatementIndex, tool, phase,
		redactExecutionLogText(options, string(output)),
	)
	if e.logger != nil {
		if commandErr != nil {
			e.logger.Errorf("%s", message)
		} else {
			e.logger.Infof("%s", message)
		}
		return
	}
	if options.Debug || commandErr != nil {
		fmt.Println(message)
	}
}

func (e *Executor) debugf(options ExecuteOptions, format string, args ...interface{}) {
	if !options.Debug {
		return
	}
	message := strings.TrimPrefix(fmt.Sprintf(format, args...), "[DEBUG] ")
	message = fmt.Sprintf(
		"[DEBUG] task_id=%d statement_index=%d %s",
		options.TaskID, options.StatementIndex, redactExecutionLogText(options, message),
	)
	if e.logger != nil {
		e.logger.Infof("%s", message)
		return
	}
	fmt.Println(message)
}
