package phase2

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/go-sql-driver/mysql"
	"github.com/shirou/gopsutil/v4/disk"
)

// BackupMethod represents the type of backup to perform
type BackupMethod string

const (
	BackupNone       BackupMethod = "none"
	BackupMysqldump  BackupMethod = "mysqldump"
	BackupXtrabackup BackupMethod = "xtrabackup"
)

var errOnlineDDLUnsupported = errors.New("online DDL is unsupported")

const onlineDDLCleanupTimeout = 5 * time.Second

var (
	alterOnlineDDLClausePattern = regexp.MustCompile(`(?i)^(?:ALGORITHM|LOCK)\b`)
	createIndexWaitPattern      = regexp.MustCompile(`(?i)\b(?:WAIT\s+\d+|NOWAIT)\b`)
	referenceKeywordPattern     = regexp.MustCompile(`(?i)\bREFERENCES\b`)
	renameOperationPattern      = regexp.MustCompile(`(?i)\bRENAME\b`)
	renameMemberPattern         = regexp.MustCompile(`(?i)^RENAME\s+(?:COLUMN|INDEX|KEY)\b`)
	exchangePartitionPattern    = regexp.MustCompile(`(?i)\bEXCHANGE\s+PARTITION\b`)
)

// Executor handles Phase 2 schema change execution
type Logger interface {
	Infof(format string, v ...interface{})
	Errorf(format string, v ...interface{})
}

type Executor struct {
	conn       *sql.DB
	logger     Logger
	runCommand func(name string, args ...string) ([]byte, error)
}

// NewExecutor creates a new executor instance
func NewExecutor(conn *sql.DB, logger Logger) *Executor {
	return &Executor{conn: conn, logger: logger, runCommand: runCombinedOutput}
}

func runCombinedOutput(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

func (e *Executor) combinedOutput(name string, args ...string) ([]byte, error) {
	if e.runCommand != nil {
		return e.runCommand(name, args...)
	}
	return runCombinedOutput(name, args...)
}

// ExecuteOptions contains options for schema change execution
type ExecuteOptions struct {
	SQL          string
	TableName    string
	Target       *TableInfo
	BackupMethod BackupMethod
	OkPTOSC      bool
	OkOnlineDDL  bool
	Config       *config.Config // Configuration with paths and directories
	Debug        bool           // Enable debug output (print commands and outputs)
}

// ExecuteResult represents the result of Phase 2 execution
type ExecuteResult struct {
	BackupPerformed bool
	BackupPath      string
	ChangeExecuted  bool
	MethodUsed      string
	Warnings        []string
	Errors          []string
}

type executionStage struct {
	executor *Executor
	options  ExecuteOptions
	name     string
	started  time.Time
	finished bool
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
	message := fmt.Sprintf("Schema change check: stage=%s status=%s table=%s", stage, status, table)
	if status != "started" {
		message += fmt.Sprintf(" duration_ms=%d", duration.Milliseconds())
	}
	if reason = sanitizeExecutionLogReason(options, reason); reason != "" {
		message += fmt.Sprintf(" reason=%q", reason)
	}
	if status == "failed" {
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
	if options.Config != nil && options.Config.MysqlPassword != "" {
		reason = strings.ReplaceAll(reason, options.Config.MysqlPassword, "***")
	}
	return reason
}

// Execute performs Phase 2 schema change execution
func (e *Executor) Execute(options ExecuteOptions) (*ExecuteResult, error) {
	result := &ExecuteResult{
		Warnings: []string{},
		Errors:   []string{},
	}

	targetStage := e.beginStage(options, "target_validation")
	if options.Target == nil && strings.TrimSpace(options.TableName) == "" {
		targetStage.finish("failed", "table name is required")
		return nil, fmt.Errorf("table name is required for schema change execution")
	}
	qualifiedSQL, resolvedTarget, err := e.validateDDLTarget(options.SQL, options.TableName, options.Target)
	if err != nil {
		targetStage.finish("failed", err.Error())
		return nil, err
	}
	canonicalTable := fmt.Sprintf("`%s`.`%s`", escapeIdent(resolvedTarget.Database), escapeIdent(resolvedTarget.Table))
	options.SQL = qualifiedSQL
	options.TableName = canonicalTable
	options.Target = &resolvedTarget
	targetStage.options = options
	targetStage.finish("passed", "SQL target matches analyzed target")

	policyStage := e.beginStage(options, "execution_policy")
	policyStage.finish("passed", fmt.Sprintf("ok_online_ddl=%t ok_pt_osc=%t backup_method=%s", options.OkOnlineDDL, options.OkPTOSC, options.BackupMethod))

	planStage := e.beginStage(options, "execution_plan_validation")
	if err := validateExecutionPlan(options); err != nil {
		planStage.finish("failed", err.Error())
		return nil, fmt.Errorf("schema change execution plan is invalid: %w", err)
	}
	planStage.finish("passed", "")

	// Validate datadir filesystem headroom before attempting any schema change.
	if options.Config == nil || !options.Config.DisableSpaceChecks {
		capacityStage := e.beginStage(options, "datadir_capacity")
		if err := e.checkDataDirFilesystemCapacity(options); err != nil {
			capacityStage.finish("failed", err.Error())
			return nil, err
		}
		capacityStage.finish("passed", "")
	} else {
		stage := e.beginStage(options, "datadir_capacity")
		stage.finish("skipped", "space checks are disabled")
	}

	// 2.1. Perform backup if specified
	if options.BackupMethod != BackupNone {
		backupStage := e.beginStage(options, "backup")
		backupPath, err := e.performBackup(options)
		if err != nil {
			backupStage.finish("failed", err.Error())
			return nil, fmt.Errorf("backup failed: %w", err)
		}
		backupStage.finish("passed", fmt.Sprintf("method=%s", options.BackupMethod))
		result.BackupPerformed = true
		result.BackupPath = backupPath
	} else {
		stage := e.beginStage(options, "backup")
		stage.finish("skipped", "backup is not required")
	}

	// 2.3. Execute using Online DDL if allowed
	if options.OkOnlineDDL {
		methodStage := e.beginStage(options, "online_ddl")
		if err := e.executeWithOnlineDDL(options, result); err != nil {
			methodStage.finish("failed", err.Error())
			if options.OkPTOSC && isOnlineDDLUnsupported(err) {
				fallbackStage := e.beginStage(options, "ptosc_fallback")
				fallbackStage.finish("passed", err.Error())
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("Online DDL unavailable; used pt-online-schema-change: %v", err))
				if e.logger != nil {
					e.logger.Infof("Online DDL not possible for table %s (%v); falling back to pt-online-schema-change", options.TableName, err)
				}
				ptoscStage := e.beginStage(options, "ptosc")
				if ptErr := e.executeWithPTOSC(options); ptErr != nil {
					ptoscStage.finish("failed", ptErr.Error())
					return nil, fmt.Errorf("pt-online-schema-change execution failed: %w", ptErr)
				}
				ptoscStage.finish("passed", "fallback completed")
				result.ChangeExecuted = true
				result.MethodUsed = "pt-online-schema-change"
				completedStage := e.beginStage(options, "execution_complete")
				completedStage.finish("passed", "method=pt-online-schema-change fallback=true")
				return result, nil
			}
			return nil, fmt.Errorf("schema change execution failed: %w", err)
		}
		methodStage.finish("passed", "")
		// <<<<< TEST ONLINE DDL AGAINST EMPTY TABLE with SAME ENGINE AND SCHEMA
		result.ChangeExecuted = true
		result.MethodUsed = "Online DDL"
		completedStage := e.beginStage(options, "execution_complete")
		completedStage.finish("passed", "method=Online DDL")
		return result, nil
	} else if options.OkPTOSC {
		stage := e.beginStage(options, "online_ddl")
		stage.finish("skipped", "Platform analysis did not allow Online DDL")
		ptoscStage := e.beginStage(options, "ptosc")
		if err := e.executeWithPTOSC(options); err != nil {
			ptoscStage.finish("failed", err.Error())
			return nil, fmt.Errorf("pt-online-schema-change execution failed: %w", err)
		}
		ptoscStage.finish("passed", "")
		result.ChangeExecuted = true
		result.MethodUsed = "pt-online-schema-change"
		completedStage := e.beginStage(options, "execution_complete")
		completedStage.finish("passed", "method=pt-online-schema-change")
		return result, nil
	} else {
		stage := e.beginStage(options, "execution_method")
		stage.finish("failed", "neither Online DDL nor pt-online-schema-change is allowed")
		result.ChangeExecuted = false
		return nil, fmt.Errorf("schema change could not be executed")

	}
}

func validateExecutionPlan(options ExecuteOptions) error {
	if options.OkOnlineDDL {
		if _, err := buildOnlineDDLSQL(options.SQL); err == nil {
			return nil
		} else if !options.OkPTOSC || !isOnlineDDLUnsupported(err) {
			return err
		}
		_, err := buildPTOSCAlterSQL(options.SQL)
		return err
	}
	if options.OkPTOSC {
		_, err := buildPTOSCAlterSQL(options.SQL)
		return err
	}
	return fmt.Errorf("neither Online DDL nor pt-online-schema-change is allowed")
}

func (e *Executor) validateDDLTarget(sql, configuredTable string, suppliedTarget *TableInfo) (string, TableInfo, error) {
	masked := maskSQLStringsAndComments(sql, false)
	match := alterTableTargetPattern.FindStringSubmatchIndex(masked)
	if len(match) < 4 {
		match = createIndexTargetPattern.FindStringSubmatchIndex(masked)
	}
	if len(match) < 4 {
		return "", TableInfo{}, fmt.Errorf("cannot validate DDL target; expected ALTER TABLE or CREATE INDEX")
	}

	ddlReference := strings.TrimSpace(sql[match[2]:match[3]])
	var currentDatabase string
	getCurrentDB := func() (string, error) {
		if currentDatabase != "" {
			return currentDatabase, nil
		}
		if e.conn == nil {
			return "", fmt.Errorf("database connection is required to resolve an unqualified configured table name")
		}
		if err := e.conn.QueryRow("SELECT DATABASE()").Scan(&currentDatabase); err != nil {
			return "", fmt.Errorf("failed to get current database: %w", err)
		}
		if currentDatabase == "" {
			return "", fmt.Errorf("no current database selected")
		}
		return currentDatabase, nil
	}

	var configuredTarget TableInfo
	if suppliedTarget != nil {
		configuredTarget = *suppliedTarget
		if strings.TrimSpace(configuredTarget.Database) == "" || strings.TrimSpace(configuredTarget.Table) == "" {
			return "", TableInfo{}, fmt.Errorf("structured schema change target requires database and table")
		}
	} else {
		var err error
		configuredTarget, err = ParseTableName(configuredTable, getCurrentDB)
		if err != nil {
			return "", TableInfo{}, fmt.Errorf("invalid configured table %q: %w", configuredTable, err)
		}
	}
	ddlDatabase, ddlTable, ddlQualified, err := parseTableReference(ddlReference)
	if err != nil {
		return "", TableInfo{}, fmt.Errorf("invalid DDL target %q: %w", ddlReference, err)
	}
	ddlTarget := TableInfo{Database: ddlDatabase, Table: ddlTable}
	if !ddlQualified {
		ddlTarget.Database = configuredTarget.Database
	}

	targetsEqual, err := e.tableTargetsEqual(ddlTarget, configuredTarget)
	if err != nil {
		return "", TableInfo{}, err
	}
	if !targetsEqual {
		return "", TableInfo{}, fmt.Errorf(
			"DDL target %s.%s does not match configured table %s.%s",
			ddlTarget.Database, ddlTarget.Table, configuredTarget.Database, configuredTarget.Table,
		)
	}

	canonicalTarget := fmt.Sprintf("`%s`.`%s`", escapeIdent(configuredTarget.Database), escapeIdent(configuredTarget.Table))
	qualifiedSQL, err := rewriteDDLTargetTable(sql, canonicalTarget)
	if err != nil {
		return "", TableInfo{}, fmt.Errorf("failed to qualify validated DDL target: %w", err)
	}
	if err := rejectUnsafeMultiObjectAlter(qualifiedSQL); err != nil {
		return "", TableInfo{}, err
	}
	qualifiedSQL, err = qualifyUnqualifiedReferences(qualifiedSQL, configuredTarget.Database)
	if err != nil {
		return "", TableInfo{}, err
	}
	return qualifiedSQL, configuredTarget, nil
}

func resolvedExecutionTarget(options ExecuteOptions) (TableInfo, error) {
	if options.Target == nil {
		return TableInfo{}, fmt.Errorf("resolved schema change target is required")
	}
	target := *options.Target
	if strings.TrimSpace(target.Database) == "" || strings.TrimSpace(target.Table) == "" {
		return TableInfo{}, fmt.Errorf("resolved schema change target requires database and table")
	}
	return target, nil
}

func rejectUnsafeMultiObjectAlter(sql string) error {
	masked := maskSQLStringsAndComments(sql, false)
	if !alterTableTargetPattern.MatchString(masked) {
		return nil
	}
	topLevelTail, err := onlineDDLTopLevelTail(sql)
	if err != nil {
		return err
	}
	for _, rename := range renameOperationPattern.FindAllStringIndex(topLevelTail, -1) {
		if !renameMemberPattern.MatchString(topLevelTail[rename[0]:]) {
			return fmt.Errorf("ALTER TABLE table rename is not supported because preflight cannot isolate the destination table")
		}
	}
	if exchangePartitionPattern.MatchString(topLevelTail) {
		return fmt.Errorf("ALTER TABLE EXCHANGE PARTITION is not supported because preflight cannot isolate the secondary table")
	}
	return nil
}

func qualifyUnqualifiedReferences(sql, defaultSchema string) (string, error) {
	keywordMasked := maskSQLStringsAndComments(sql, true)
	targetMasked := maskSQLStringsAndComments(sql, false)
	keywords := referenceKeywordPattern.FindAllStringIndex(keywordMasked, -1)
	if len(keywords) == 0 {
		return sql, nil
	}

	result := sql
	for i := len(keywords) - 1; i >= 0; i-- {
		keywordEnd := keywords[i][1]
		match := leadingTableReferencePattern.FindStringSubmatchIndex(targetMasked[keywordEnd:])
		if len(match) < 4 {
			return "", fmt.Errorf("could not parse table following REFERENCES")
		}
		targetStart := keywordEnd + match[2]
		targetEnd := keywordEnd + match[3]
		_, table, qualified, err := parseTableReference(sql[targetStart:targetEnd])
		if err != nil {
			return "", fmt.Errorf("invalid REFERENCES target: %w", err)
		}
		if qualified {
			continue
		}
		qualifiedTarget := fmt.Sprintf("`%s`.`%s`", escapeIdent(defaultSchema), escapeIdent(table))
		result = result[:targetStart] + qualifiedTarget + result[targetEnd:]
	}
	return result, nil
}

func (e *Executor) tableTargetsEqual(first, second TableInfo) (bool, error) {
	if first == second {
		return true, nil
	}
	if !strings.EqualFold(first.Database, second.Database) || !strings.EqualFold(first.Table, second.Table) {
		return false, nil
	}
	if e.conn == nil {
		return false, fmt.Errorf("database connection is required to check case-insensitive table names")
	}

	var lowerCaseTableNames int
	if err := e.conn.QueryRow("SELECT @@lower_case_table_names").Scan(&lowerCaseTableNames); err != nil {
		return false, fmt.Errorf("failed to read lower_case_table_names: %w", err)
	}
	return lowerCaseTableNames == 1 || lowerCaseTableNames == 2, nil
}

func (e *Executor) performBackup(options ExecuteOptions) (string, error) {
	if options.Config == nil {
		return "", fmt.Errorf("config is required for backup")
	}
	filesystemStage := e.beginStage(options, "backup_filesystem")
	usage, err := prepareBackupFilesystem(options.Config.BackupDir, !options.Config.DisableSpaceChecks)
	if err != nil {
		filesystemStage.finish("failed", err.Error())
		return "", err
	}
	filesystemStage.finish("passed", "backup directory is ready")

	// Check disk space before performing backup
	if !options.Config.DisableSpaceChecks {
		capacityStage := e.beginStage(options, "backup_capacity")
		if err := e.checkDiskSpace(options, usage); err != nil {
			capacityStage.finish("failed", err.Error())
			if e.logger != nil {
				e.logger.Errorf("disk space check failed for table %s: %v", options.TableName, err)
			}
			return "", err
		}
		capacityStage.finish("passed", "")
	} else {
		stage := e.beginStage(options, "backup_capacity")
		stage.finish("skipped", "space checks are disabled")
	}

	switch options.BackupMethod {
	case BackupMysqldump:
		return e.backupWithMysqldump(options)
	case BackupXtrabackup:
		return e.backupWithXtrabackup(options)
	default:
		return "", fmt.Errorf("unsupported backup method: %s", options.BackupMethod)
	}
}

func prepareBackupFilesystem(path string, inspectUsage bool) (*disk.UsageStat, error) {
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, fmt.Errorf("failed to create backup directory: %w", err)
	}
	if !inspectUsage {
		return nil, nil
	}
	usage, err := disk.Usage(path)
	if err != nil {
		return nil, fmt.Errorf("failed to check disk space: %w", err)
	}
	return usage, nil
}

func (e *Executor) checkDataDirFilesystemCapacity(options ExecuteOptions) error {
	tableInfo, err := resolvedExecutionTarget(options)
	if err != nil {
		return fmt.Errorf("failed to parse table name: %w", err)
	}

	var dataDirVar, dataDir sql.NullString
	if err := e.conn.QueryRow("SHOW VARIABLES LIKE 'datadir'").Scan(&dataDirVar, &dataDir); err != nil {
		return fmt.Errorf("failed to resolve datadir: %w", err)
	}
	if !dataDir.Valid || strings.TrimSpace(dataDir.String) == "" {
		return fmt.Errorf("datadir is empty")
	}

	var dataLength, indexLength sql.NullInt64
	err = e.conn.QueryRow(`
			SELECT DATA_LENGTH, INDEX_LENGTH
			FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
	`, tableInfo.Database, tableInfo.Table).Scan(&dataLength, &indexLength)
	if err != nil {
		return fmt.Errorf("failed to get table size for %s.%s: %w", tableInfo.Database, tableInfo.Table, err)
	}

	tableSizeBytes := int64(0)
	if dataLength.Valid {
		tableSizeBytes += dataLength.Int64
	}
	if indexLength.Valid {
		tableSizeBytes += indexLength.Int64
	}

	usage, err := disk.Usage(dataDir.String)
	if err != nil {
		return fmt.Errorf("failed to check datadir filesystem capacity: %w", err)
	}

	totalBytes := int64(usage.Total)
	freeBytes := int64(usage.Free)
	usedBytes := totalBytes - freeBytes
	if totalBytes <= 0 {
		return fmt.Errorf("invalid datadir filesystem size")
	}

	freeRatio := float64(freeBytes) / float64(totalBytes)
	usedRatioAfter := float64(usedBytes+tableSizeBytes) / float64(totalBytes)

	// Condition 1: filesystem must have >10% free space.
	if freeRatio <= 0.10 {
		return fmt.Errorf("insufficient datadir free space: %.2f%% free, required >10%%", freeRatio*100.0)
	}
	// Condition 2: current used + table size must stay <= 90% used.
	if usedRatioAfter > 0.90 {
		return fmt.Errorf("insufficient datadir capacity for table change: projected usage %.2f%% exceeds 90%% limit", usedRatioAfter*100.0)
	}

	if options.Debug {
		e.debugf(options, "[DEBUG] datadir path: %s", dataDir.String)
		e.debugf(options, "[DEBUG] table size estimate: %.2f MB", float64(tableSizeBytes)/(1024*1024))
		e.debugf(options, "[DEBUG] datadir free space: %.2f%%", freeRatio*100.0)
		e.debugf(options, "[DEBUG] projected datadir usage after change: %.2f%%", usedRatioAfter*100.0)
	}

	return nil
}

func (e *Executor) backupWithMysqldump(options ExecuteOptions) (string, error) {
	if options.Config == nil {
		return "", fmt.Errorf("config is required for backup")
	}

	host := options.Config.MysqlHost
	port := options.Config.MysqlPort
	user := options.Config.MysqlUser
	password := options.Config.MysqlPassword
	if host == "" {
		return "", fmt.Errorf("mysql_host is required for backup")
	}

	tableInfo, err := resolvedExecutionTarget(options)
	if err != nil {
		return "", err
	}

	// Use config values
	mysqldump := options.Config.MysqldumpPath
	if mysqldump == "" {
		mysqldump = "mysqldump"
	}

	// Generate timestamp prefix in YYMMDDHHMMSS format
	timestamp := time.Now().Format("060102150405")
	backupPath := fmt.Sprintf("%s/%s_%s_%s.sql", options.Config.BackupDir, timestamp, tableInfo.Database, tableInfo.Table)

	args := buildMysqldumpConnectionArgs(host, port, user, password)
	args = append(args,
		tableInfo.Database,
		tableInfo.Table,
		"--single-transaction",
		"--quick",
		"--lock-tables=false",
		"-r", backupPath,
	)

	cmd := exec.Command(mysqldump, args...)

	if options.Debug {
		// Mask password in debug output
		safeArgs := make([]string, len(args))
		copy(safeArgs, args)
		// Find and mask password argument (-p followed by password)
		for i, arg := range safeArgs {
			if strings.HasPrefix(arg, "-p") && len(arg) > 2 {
				safeArgs[i] = "-p***"
			}
		}
		e.debugf(options, "[DEBUG] mysqldump command: %s", mysqldump)
		e.debugf(options, "[DEBUG] mysqldump args: %s", strings.Join(safeArgs, " "))
	}

	output, err := cmd.CombinedOutput()
	if options.Debug {
		if len(output) > 0 {
			e.debugf(options, "[DEBUG] mysqldump output:\n%s", string(output))
		}
	}

	if err != nil {
		if options.Debug {
			e.debugf(options, "[DEBUG] mysqldump error: %v", err)
		}
		return "", fmt.Errorf("mysqldump failed: %w", err)
	}

	return backupPath, nil
}

func buildMysqldumpConnectionArgs(host, port, user, password string) []string {
	args := []string{"-u", user, "-p" + password}
	if strings.HasPrefix(host, "/") {
		return append([]string{"--socket=" + host}, args...)
	}
	return append([]string{"-h", host, "-P", port}, args...)
}

func buildXtrabackupConnectionArgs(host, port, user, password string) []string {
	args := []string{
		"--user=" + user,
		"--password=" + password,
	}
	if strings.HasPrefix(host, "/") {
		return append(args, "--socket="+host)
	}
	return append(args, "--host="+host, "--port="+port)
}

func (e *Executor) backupWithXtrabackup(options ExecuteOptions) (string, error) {
	if options.Config == nil {
		return "", fmt.Errorf("config is required for backup")
	}

	host := options.Config.MysqlHost
	port := options.Config.MysqlPort
	user := options.Config.MysqlUser
	password := options.Config.MysqlPassword
	if host == "" {
		return "", fmt.Errorf("mysql_host is required for backup")
	}

	tableInfo, err := resolvedExecutionTarget(options)
	if err != nil {
		return "", err
	}

	// Use config values
	xtrabackup := options.Config.XtrabackupPath
	if xtrabackup == "" {
		xtrabackup = "xtrabackup"
	}

	// Generate timestamp prefix in YYMMDDHHMMSS format
	timestamp := time.Now().Format("060102150405")
	// Create a unique backup directory for this table
	backupDir := fmt.Sprintf("%s/%s_xtrabackup_%s_%s", options.Config.BackupDir, timestamp, tableInfo.Database, tableInfo.Table)

	// Step 1: Take backup of the table using --tables option.
	// xtrabackup treats --tables as a regex, so escape metacharacters and anchor
	// both sides to match only the exact database.table name.
	tableName := fmt.Sprintf("%s.%s", tableInfo.Database, tableInfo.Table)
	tableSpec := "^" + regexp.QuoteMeta(tableName) + "$"

	backupArgs := []string{
		"--backup",
		"--ftwrl-wait-timeout=15",
		"--tables=" + tableSpec,
		"--target-dir=" + backupDir,
	}
	backupArgs = append(backupArgs, buildXtrabackupConnectionArgs(host, port, user, password)...)

	if options.Debug {
		// Mask password in debug output
		safeArgs := make([]string, len(backupArgs))
		copy(safeArgs, backupArgs)
		for i, arg := range safeArgs {
			if strings.HasPrefix(arg, "--password=") {
				safeArgs[i] = "--password=***"
			}
		}
		e.debugf(options, "[DEBUG] xtrabackup backup command: %s", xtrabackup)
		e.debugf(options, "[DEBUG] xtrabackup backup args: %s", strings.Join(safeArgs, " "))
	}

	cmd := exec.Command(xtrabackup, backupArgs...)
	output, err := cmd.CombinedOutput()
	if options.Debug {
		if len(output) > 0 {
			e.debugf(options, "[DEBUG] xtrabackup backup output:\n%s", string(output))
		}
		if err != nil {
			e.debugf(options, "[DEBUG] xtrabackup backup error: %v", err)
		}
	}

	if err != nil {
		return "", fmt.Errorf("xtrabackup backup failed: %w: %s", err, string(output))
	}

	// Step 2: Prepare the backup with --export option
	// This prepares the backup and exports table metadata for transportable tablespace
	prepareArgs := []string{
		"--prepare",
		"--export",
		"--target-dir=" + backupDir,
	}

	if options.Debug {
		e.debugf(options, "[DEBUG] xtrabackup prepare command: %s", xtrabackup)
		e.debugf(options, "[DEBUG] xtrabackup prepare args: %s", strings.Join(prepareArgs, " "))
	}

	cmd = exec.Command(xtrabackup, prepareArgs...)
	output, err = cmd.CombinedOutput()
	if options.Debug {
		if len(output) > 0 {
			e.debugf(options, "[DEBUG] xtrabackup prepare output:\n%s", string(output))
		}
		if err != nil {
			e.debugf(options, "[DEBUG] xtrabackup prepare error: %v", err)
		}
	}

	if err != nil {
		return "", fmt.Errorf("xtrabackup prepare failed: %w: %s", err, string(output))
	}

	// Return the backup directory path
	// The table files (.ibd and .cfg) will be in backupDir/database/table.*
	return backupDir, nil
}

// checkDiskSpace checks if there's enough disk space for the backup
func (e *Executor) checkDiskSpace(options ExecuteOptions, usage *disk.UsageStat) error {
	var estimatedSize int64

	tableInfo, err := resolvedExecutionTarget(options)
	if err != nil {
		return fmt.Errorf("failed to parse table name: %w", err)
	}

	// Estimate backup size based on method
	switch options.BackupMethod {
	case BackupMysqldump:
		sizeMB, err := e.estimateMysqldumpSize(tableInfo.Database, tableInfo.Table)
		if err != nil {
			return fmt.Errorf("failed to estimate backup size: %w", err)
		}
		estimatedSize = int64(sizeMB * 1024 * 1024) // Convert MB to bytes
	case BackupXtrabackup:
		// Xtrabackup backs up entire database, so we estimate based on database size
		sizeMB, err := e.estimateXtrabackupSize(tableInfo.Database)
		if err != nil {
			return fmt.Errorf("failed to estimate backup size: %w", err)
		}
		estimatedSize = int64(sizeMB * 1024 * 1024) // Convert MB to bytes
	default:
		return nil // No backup, no space check needed
	}

	// Add buffer percentage to estimated size
	bufferPercent := options.Config.BackupSpaceBuffer
	if bufferPercent == 0 {
		bufferPercent = 20.0 // Fallback to 20% if not configured
	}
	requiredSize := estimatedSize + int64(float64(estimatedSize)*bufferPercent/100.0)

	if usage == nil {
		return fmt.Errorf("backup filesystem usage is required for disk space check")
	}

	availableBytes := int64(usage.Free)

	if options.Debug {
		e.debugf(options, "[DEBUG] Estimated backup size: %.2f MB", float64(estimatedSize)/(1024*1024))
		e.debugf(options, "[DEBUG] Required space (with %.1f%% buffer): %.2f MB", bufferPercent, float64(requiredSize)/(1024*1024))
		e.debugf(options, "[DEBUG] Available disk space: %.2f MB", float64(availableBytes)/(1024*1024))
	}

	if availableBytes < requiredSize {
		return fmt.Errorf("insufficient disk space: required %.2f MB (with %.1f%% buffer), available %.2f MB",
			float64(requiredSize)/(1024*1024),
			bufferPercent,
			float64(availableBytes)/(1024*1024))
	}

	return nil
}

// estimateMysqldumpSize estimates the size of a mysqldump backup for a specific table
func (e *Executor) estimateMysqldumpSize(dbName, tableName string) (float64, error) {
	var dataLength, indexLength sql.NullInt64
	err := e.conn.QueryRow(`
		SELECT DATA_LENGTH, INDEX_LENGTH
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
	`, dbName, tableName).Scan(&dataLength, &indexLength)
	if err != nil {
		return 0, err
	}

	var totalBytes int64
	if dataLength.Valid {
		totalBytes += dataLength.Int64
	}
	if indexLength.Valid {
		totalBytes += indexLength.Int64
	}

	if totalBytes == 0 {
		return 0.1, nil // Return a minimal size estimate if table size is 0 or NULL
	}

	// mysqldump typically produces 1.5-2x the table size due to SQL format overhead
	estimatedSizeMB := float64(totalBytes) * 2.0 / (1024 * 1024)

	return estimatedSizeMB, nil
}

// estimateXtrabackupSize estimates the size of an xtrabackup for the entire database
func (e *Executor) estimateXtrabackupSize(dbName string) (float64, error) {
	var totalBytes sql.NullInt64
	err := e.conn.QueryRow(`
		SELECT SUM(DATA_LENGTH + INDEX_LENGTH)
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ?
	`, dbName).Scan(&totalBytes)
	if err != nil {
		return 0, err
	}

	if !totalBytes.Valid || totalBytes.Int64 == 0 {
		return 0.1, nil // Return a minimal size estimate if database size is 0 or NULL
	}

	// Xtrabackup includes all tables, indexes, and some overhead
	// Estimate ~1.2x the database size
	estimatedSizeMB := float64(totalBytes.Int64) * 1.2 / (1024 * 1024)

	return estimatedSizeMB, nil
}

func (e *Executor) executeWithPTOSC(options ExecuteOptions) error {
	// Perform dry-run first
	dryRunStage := e.beginStage(options, "ptosc_dry_run")
	if err := e.dryRunPTOSC(options); err != nil {
		dryRunStage.finish("failed", err.Error())
		return fmt.Errorf("pt-online-schema-change dry-run failed: %w", err)
	}
	dryRunStage.finish("passed", "")

	// Execute actual change
	executionStage := e.beginStage(options, "ptosc_execution")
	err := e.runPTOSC(options)
	if err != nil {
		executionStage.finish("failed", err.Error())
		return err
	}
	executionStage.finish("passed", "")
	return nil
}

func (e *Executor) dryRunPTOSC(options ExecuteOptions) error {
	if options.Config == nil {
		return fmt.Errorf("config is required for pt-online-schema-change")
	}

	ptosc := options.Config.PTOSCPath
	if ptosc == "" {
		ptosc = "pt-online-schema-change"
	}

	host := options.Config.MysqlHost
	port := options.Config.MysqlPort
	user := options.Config.MysqlUser
	password := options.Config.MysqlPassword
	if host == "" {
		return fmt.Errorf("mysql_host is required for pt-online-schema-change")
	}

	tableInfo, err := resolvedExecutionTarget(options)
	if err != nil {
		return err
	}

	alterSQL, err := buildPTOSCAlterSQL(options.SQL)
	if err != nil {
		return err
	}

	args := []string{
		"--dry-run",
		buildPTOSCDSN(host, port, user, password, tableInfo.Database, tableInfo.Table),
		fmt.Sprintf("--alter=%s", alterSQL),
	}

	if options.Debug {
		// Mask password in debug output
		safeArgs := make([]string, len(args))
		copy(safeArgs, args)
		for i, arg := range safeArgs {
			// Mask password in connection string (p=password)
			if strings.Contains(arg, ",p=") {
				parts := strings.Split(arg, ",p=")
				if len(parts) == 2 {
					// Extract everything after p= and before next comma
					rest := strings.Split(parts[1], ",")
					if len(rest) > 0 {
						safeArgs[i] = parts[0] + ",p=***," + strings.Join(rest[1:], ",")
					} else {
						safeArgs[i] = parts[0] + ",p=***"
					}
				} else if strings.HasPrefix(arg, "p=") {
					rest := strings.SplitN(arg, ",", 2)
					safeArgs[i] = "p=***"
					if len(rest) > 1 {
						safeArgs[i] += "," + rest[1]
					}
				}
			}
		}
		e.debugf(options, "[DEBUG] pt-online-schema-change dry-run command: %s", ptosc)
		e.debugf(options, "[DEBUG] pt-online-schema-change dry-run args: %s", strings.Join(safeArgs, " "))
	}

	output, err := e.combinedOutput(ptosc, args...)
	e.debugf(options, "[DEBUG] pt-online-schema-change dry-run output:\n%s", string(output))
	if err != nil {
		e.debugf(options, "[DEBUG] pt-online-schema-change dry-run error: %v", err)
	}

	if err != nil {
		return fmt.Errorf("pt-online-schema-change dry-run failed: %s", string(output))
	}

	return nil
}

func (e *Executor) runPTOSC(options ExecuteOptions) error {
	if options.Config == nil {
		return fmt.Errorf("config is required for pt-online-schema-change")
	}

	ptosc := options.Config.PTOSCPath
	if ptosc == "" {
		ptosc = "pt-online-schema-change"
	}

	host := options.Config.MysqlHost
	port := options.Config.MysqlPort
	user := options.Config.MysqlUser
	password := options.Config.MysqlPassword
	if host == "" {
		return fmt.Errorf("mysql_host is required for pt-online-schema-change")
	}

	tableInfo, err := resolvedExecutionTarget(options)
	if err != nil {
		return err
	}

	alterSQL, err := buildPTOSCAlterSQL(options.SQL)
	if err != nil {
		return err
	}

	args := []string{
		"--execute",
		buildPTOSCDSN(host, port, user, password, tableInfo.Database, tableInfo.Table),
		fmt.Sprintf("--alter=%s", alterSQL),
	}

	if options.Debug {
		// Mask password in debug output
		safeArgs := make([]string, len(args))
		copy(safeArgs, args)
		for i, arg := range safeArgs {
			// Mask password in connection string (p=password)
			if strings.Contains(arg, ",p=") {
				parts := strings.Split(arg, ",p=")
				if len(parts) == 2 {
					// Extract everything after p= and before next comma
					rest := strings.Split(parts[1], ",")
					if len(rest) > 0 {
						safeArgs[i] = parts[0] + ",p=***," + strings.Join(rest[1:], ",")
					} else {
						safeArgs[i] = parts[0] + ",p=***"
					}
				} else if strings.HasPrefix(arg, "p=") {
					rest := strings.SplitN(arg, ",", 2)
					safeArgs[i] = "p=***"
					if len(rest) > 1 {
						safeArgs[i] += "," + rest[1]
					}
				}
			}
		}
		e.debugf(options, "[DEBUG] pt-online-schema-change execute command: %s", ptosc)
		e.debugf(options, "[DEBUG] pt-online-schema-change execute args: %s", strings.Join(safeArgs, " "))
	}

	output, err := e.combinedOutput(ptosc, args...)
	e.debugf(options, "[DEBUG] pt-online-schema-change execute output:\n%s", string(output))
	if err != nil {
		e.debugf(options, "[DEBUG] pt-online-schema-change execute error: %v", err)
	}

	if err != nil {
		return fmt.Errorf("pt-online-schema-change failed: %s", string(output))
	}

	return nil
}

func buildPTOSCDSN(host, port, user, password, database, table string) string {
	parts := make([]string, 0, 6)
	if strings.HasPrefix(host, "/") {
		parts = append(parts, "S="+host)
	} else {
		parts = append(parts, "h="+host, "P="+port)
	}
	parts = append(parts, "u="+user, "p="+password, "D="+database, "t="+table)
	return strings.Join(parts, ",")
}

func buildPTOSCAlterSQL(sql string) (string, error) {
	if containsExecutableSQLComment(sql) {
		return "", fmt.Errorf("executable SQL comments are not allowed in pt-online-schema-change input")
	}
	if containsAmbiguousBackslashQuote(sql) {
		return "", fmt.Errorf("backslash-escaped quotes are not allowed in pt-online-schema-change input because their meaning depends on sql_mode")
	}

	statement, _ := splitDDLStatementSuffix(sql)
	if statement == "" {
		return "", fmt.Errorf("empty SQL statement")
	}
	if strings.Contains(maskSQLStringsAndComments(statement, true), ";") {
		return "", fmt.Errorf("multiple SQL statements are not allowed in pt-online-schema-change input")
	}

	masked := maskSQLStringsAndComments(statement, false)
	if match := alterTableTargetPattern.FindStringSubmatchIndex(masked); len(match) >= 4 {
		modifiers := strings.Fields(masked[:match[2]])
		for i, modifier := range modifiers {
			if strings.EqualFold(modifier, "IGNORE") {
				return "", fmt.Errorf("ALTER IGNORE cannot be represented safely by pt-online-schema-change")
			}
			if strings.EqualFold(modifier, "IF") && i+1 < len(modifiers) && strings.EqualFold(modifiers[i+1], "EXISTS") {
				return "", fmt.Errorf("ALTER TABLE IF EXISTS cannot be represented safely by pt-online-schema-change")
			}
		}
		return stripAlterOnlineDDLClauses(statement[match[3]:])
	}

	match := createIndexPTOSCPattern.FindStringSubmatchIndex(masked)
	if len(match) < 6 {
		return "", fmt.Errorf("unsupported DDL for pt-online-schema-change; expected ALTER TABLE or CREATE INDEX")
	}
	definition := strings.TrimSpace(statement[match[2]:match[3]])
	tail := stripCreateIndexOnlineDDLClauses(statement[match[5]:])
	if tail == "" || !strings.HasPrefix(strings.TrimSpace(tail), "(") {
		return "", fmt.Errorf("invalid CREATE INDEX column definition")
	}
	if createIndexWaitPattern.MatchString(maskTopLevelSQL(tail)) {
		return "", fmt.Errorf("CREATE INDEX WAIT/NOWAIT cannot be represented safely by pt-online-schema-change")
	}
	return "ADD " + definition + " " + strings.TrimSpace(tail), nil
}

func stripAlterOnlineDDLClauses(body string) (string, error) {
	masked := maskSQLStringsAndComments(body, true)
	start := 0
	depth := 0
	kept := make([]string, 0, 4)

	appendClause := func(end int) {
		rawClause := strings.TrimSpace(body[start:end])
		maskedClause := strings.TrimSpace(masked[start:end])
		if rawClause != "" && !alterOnlineDDLClausePattern.MatchString(maskedClause) {
			kept = append(kept, rawClause)
		}
	}

	for i := range masked {
		switch masked[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				appendClause(i)
				start = i + 1
			}
		}
	}
	appendClause(len(body))

	if len(kept) == 0 {
		return "", fmt.Errorf("ALTER TABLE contains no change supported by pt-online-schema-change")
	}
	return strings.Join(kept, ", "), nil
}

func stripCreateIndexOnlineDDLClauses(tail string) string {
	masked := []byte(maskTopLevelSQL(tail))

	clausePattern := regexp.MustCompile(`(?i)\b(?:ALGORITHM|LOCK)\b\s*(?:=\s*|\s+)(?:DEFAULT|INPLACE|COPY|INSTANT|NOCOPY|NONE|SHARED|EXCLUSIVE)\b`)
	spans := clausePattern.FindAllStringIndex(string(masked), -1)
	if len(spans) == 0 {
		return strings.TrimSpace(tail)
	}

	var result strings.Builder
	last := 0
	for _, span := range spans {
		result.WriteString(tail[last:span[0]])
		last = span[1]
	}
	result.WriteString(tail[last:])
	return strings.TrimSpace(result.String())
}

func maskTopLevelSQL(sql string) string {
	masked := []byte(maskSQLStringsAndComments(sql, true))
	depth := 0
	for i := range masked {
		switch masked[i] {
		case '(':
			depth++
			masked[i] = ' '
		case ')':
			if depth > 0 {
				depth--
			}
			masked[i] = ' '
		default:
			if depth > 0 {
				masked[i] = ' '
			}
		}
	}
	return string(masked)
}

func isOnlineDDLUnsupported(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errOnlineDDLUnsupported) {
		return true
	}

	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		switch mysqlErr.Number {
		case 1845, 1846, 1847, 1848, 1849, 1850, 1851, 1852, 1853, 1854, 1855, 1856, 1857,
			1861, 3060, 3103, 3178, 3187, 4083, 4157, 4158:
			return true
		}
	}

	message := strings.ToLower(err.Error())
	if strings.Contains(message, "try algorithm=copy") {
		return true
	}
	for _, qualifier := range []string{"algorithm=inplace", "algorithm=nocopy", "algorithm=instant", "lock=none"} {
		if strings.Contains(message, qualifier) &&
			(strings.Contains(message, "not supported") || strings.Contains(message, "unsupported")) {
			return true
		}
	}
	return false
}

func (e *Executor) executeWithOnlineDDL(options ExecuteOptions, result *ExecuteResult) error {
	prepareStage := e.beginStage(options, "online_ddl_prepare")
	tableInfo, err := resolvedExecutionTarget(options)
	if err != nil {
		prepareStage.finish("failed", err.Error())
		return fmt.Errorf("failed to parse table name: %w", err)
	}

	testSchema := ""
	if options.Config != nil {
		testSchema = options.Config.OnlineDDLTestSchema
	}
	if strings.TrimSpace(testSchema) == "" {
		prepareStage.finish("failed", "test schema is required")
		return fmt.Errorf("test schema is required for online DDL preflight")
	}

	testTableName := buildOnlineDDLTestTableName(tableInfo.Database, tableInfo.Table, time.Now().UnixNano())
	testTableRef := fmt.Sprintf("`%s`.`%s`", escapeIdent(testSchema), escapeIdent(testTableName))
	sourceTableRef := fmt.Sprintf("`%s`.`%s`", escapeIdent(tableInfo.Database), escapeIdent(tableInfo.Table))

	finalSQL, err := buildOnlineDDLSQL(options.SQL)
	if err != nil {
		prepareStage.finish("failed", err.Error())
		return err
	}
	testSQL, err := rewriteDDLTargetTable(finalSQL, testTableRef)
	if err != nil {
		prepareStage.finish("failed", err.Error())
		return fmt.Errorf("failed to prepare test DDL SQL: %w", err)
	}
	prepareStage.finish("passed", "Online DDL and scratch statements are ready")

	var scratchCleanupErr error
	sessionStage := e.beginStage(options, "online_ddl_session")
	executionErr, cleanupErr := withPinnedLockWaitTimeout(
		context.Background(), e.conn, 20,
		func(ctx context.Context, conn *sql.Conn) (callbackErr error) {
			schemaStage := e.beginStage(options, "online_ddl_scratch_schema")
			if _, callbackErr = conn.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", escapeIdent(testSchema))); callbackErr != nil {
				schemaStage.finish("failed", callbackErr.Error())
				return fmt.Errorf("failed to create test schema %s: %w", testSchema, callbackErr)
			}
			schemaStage.finish("passed", "")

			tableStage := e.beginStage(options, "online_ddl_scratch_table")
			if _, callbackErr = conn.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s LIKE %s", testTableRef, sourceTableRef)); callbackErr != nil {
				tableStage.finish("failed", callbackErr.Error())
				return fmt.Errorf("failed to create test table %s: %w", testTableRef, callbackErr)
			}
			tableStage.finish("passed", "")
			defer func() {
				cleanupStage := e.beginStage(options, "online_ddl_scratch_cleanup")
				if dropErr := cleanupOnlineDDLTestTable(conn, testTableRef, onlineDDLCleanupTimeout); dropErr != nil {
					scratchCleanupErr = fmt.Errorf("failed to drop Online DDL test table %s: %w", testTableRef, dropErr)
					cleanupStage.finish("failed", dropErr.Error())
					return
				}
				cleanupStage.finish("passed", "")
			}()

			e.debugf(options, "[DEBUG] Online DDL preflight SQL (test table):\n%s", testSQL)
			preflightStage := e.beginStage(options, "online_ddl_preflight")
			if _, callbackErr = conn.ExecContext(ctx, testSQL); callbackErr != nil {
				preflightStage.finish("failed", callbackErr.Error())
				return fmt.Errorf("online DDL preflight failed on test table %s: %w", testTableRef, callbackErr)
			}
			preflightStage.finish("passed", "")

			e.debugf(options, "[DEBUG] Executing Online DDL statement:\n%s", finalSQL)
			productionStage := e.beginStage(options, "online_ddl_production")
			_, callbackErr = conn.ExecContext(ctx, finalSQL)
			if callbackErr != nil {
				productionStage.finish("failed", callbackErr.Error())
			} else {
				productionStage.finish("passed", "")
			}
			return callbackErr
		},
	)
	if executionErr != nil {
		sessionStage.finish("failed", executionErr.Error())
	} else if cleanupErr != nil {
		sessionStage.finish("failed", cleanupErr.Error())
	} else {
		sessionStage.finish("passed", "lock_wait_timeout restored")
	}
	if scratchCleanupErr != nil {
		result.Warnings = append(result.Warnings, scratchCleanupErr.Error())
		if e.logger != nil {
			e.logger.Errorf("Online DDL scratch cleanup failed for table %s: %v", options.TableName, scratchCleanupErr)
		}
	}
	if cleanupErr != nil {
		result.Warnings = append(result.Warnings, cleanupErr.Error())
		if e.logger != nil {
			e.logger.Errorf("Online DDL session cleanup failed for table %s: %v", options.TableName, cleanupErr)
		}
	}
	err = executionErr
	if err != nil {
		result.Warnings = append(result.Warnings, err.Error())
		e.debugf(options, "[DEBUG] Online DDL execution error: %v", err)
	} else {
		e.debugf(options, "[DEBUG] Online DDL execution successful")
	}

	if err != nil {
		if isOnlineDDLUnsupported(err) {
			result.Warnings = append(result.Warnings,
				"Online DDL not supported for this operation")
			return err
		}
		return err
	}

	return nil
}

func buildOnlineDDLTestTableName(schema, table string, nonce int64) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", schema, table, nonce)))
	return fmt.Sprintf("_releem_ddl_test_%x", digest[:16])
}

func executeOnlineDDLOnPinnedConn(ctx context.Context, db *sql.DB, ddl string, lockWaitTimeout int64) (executionErr, cleanupErr error) {
	return withPinnedLockWaitTimeout(ctx, db, lockWaitTimeout, func(ctx context.Context, conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, ddl)
		return err
	})
}

func withPinnedLockWaitTimeout(
	ctx context.Context,
	db *sql.DB,
	lockWaitTimeout int64,
	callback func(context.Context, *sql.Conn) error,
) (executionErr, cleanupErr error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire SQL connection: %w", err), nil
	}
	defer conn.Close()

	var previousTimeout int64
	if err := conn.QueryRowContext(ctx, "SELECT @@SESSION.lock_wait_timeout").Scan(&previousTimeout); err != nil {
		return fmt.Errorf("failed to read session lock_wait_timeout: %w", err), nil
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("SET SESSION lock_wait_timeout = %d", lockWaitTimeout)); err != nil {
		return fmt.Errorf("failed to set session lock_wait_timeout: %w", err), nil
	}

	defer func() {
		restoreCtx, cancel := context.WithTimeout(context.Background(), onlineDDLCleanupTimeout)
		defer cancel()
		_, restoreErr := conn.ExecContext(restoreCtx, fmt.Sprintf("SET SESSION lock_wait_timeout = %d", previousTimeout))
		if restoreErr != nil {
			cleanupErr = fmt.Errorf("failed to restore session lock_wait_timeout: %w", restoreErr)
			// Never return a session with the reduced timeout to the shared pool.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()

	executionErr = callback(ctx, conn)
	return executionErr, cleanupErr
}

func cleanupOnlineDDLTestTable(conn *sql.Conn, tableRef string, timeout time.Duration) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	_, err := conn.ExecContext(cleanupCtx, fmt.Sprintf("DROP TABLE IF EXISTS %s", tableRef))
	if err != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	return err
}

func buildOnlineDDLSQL(sql string) (string, error) {
	if containsExecutableSQLComment(sql) {
		return "", fmt.Errorf("executable SQL comments are not allowed in Online DDL")
	}
	if containsAmbiguousBackslashQuote(sql) {
		return "", fmt.Errorf("backslash-escaped quotes are not allowed in Online DDL because their meaning depends on sql_mode")
	}
	statement, suffix := splitDDLStatementSuffix(sql)
	if statement == "" {
		return "", fmt.Errorf("empty SQL statement")
	}
	if strings.Contains(maskSQLStringsAndComments(statement, true), ";") {
		return "", fmt.Errorf("multiple SQL statements are not allowed in Online DDL")
	}

	topLevelTail, err := onlineDDLTopLevelTail(statement)
	if err != nil {
		return "", err
	}
	algorithm, hasAlgorithm, err := onlineDDLClauseValue(topLevelTail, "ALGORITHM")
	if err != nil {
		return "", err
	}
	lock, hasLock, err := onlineDDLClauseValue(topLevelTail, "LOCK")
	if err != nil {
		return "", err
	}
	if hasAlgorithm && algorithm != "INPLACE" && algorithm != "INSTANT" && algorithm != "NOCOPY" {
		if algorithm == "COPY" || algorithm == "DEFAULT" {
			return "", fmt.Errorf("%w: unsafe Online DDL algorithm %s; expected INPLACE, INSTANT, or NOCOPY", errOnlineDDLUnsupported, algorithm)
		}
		return "", fmt.Errorf("unsafe Online DDL algorithm %s; expected INPLACE, INSTANT, or NOCOPY", algorithm)
	}
	if hasLock && lock != "NONE" {
		if lock == "DEFAULT" || lock == "SHARED" || lock == "EXCLUSIVE" {
			return "", fmt.Errorf("%w: unsafe Online DDL lock %s; expected NONE", errOnlineDDLUnsupported, lock)
		}
		return "", fmt.Errorf("unsafe Online DDL lock %s; expected NONE", lock)
	}
	if hasAlgorithm && hasLock {
		return statement + normalizeDDLSuffix(suffix), nil
	}

	separator, err := getOnlineDDLClauseSeparator(statement)
	if err != nil {
		return "", err
	}

	if !hasAlgorithm {
		statement += separator + "ALGORITHM=INPLACE"
	}
	if !hasLock {
		statement += separator + "LOCK=NONE"
	}

	return statement + normalizeDDLSuffix(suffix), nil
}

func splitDDLStatementSuffix(sql string) (string, string) {
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return "", ""
	}

	commentMasked := maskSQLComments(sql)
	lastCodeEnd := len(strings.TrimRightFunc(commentMasked, unicode.IsSpace))
	if lastCodeEnd == 0 {
		return "", ""
	}

	semicolonIndex := -1
	statementEnd := lastCodeEnd
	if commentMasked[lastCodeEnd-1] == ';' {
		semicolonIndex = lastCodeEnd - 1
		statementEnd = len(strings.TrimRightFunc(commentMasked[:semicolonIndex], unicode.IsSpace))
		if statementEnd == 0 {
			return "", ""
		}
	}

	suffix := sql[statementEnd:]
	if semicolonIndex >= 0 {
		suffix = sql[statementEnd:semicolonIndex] + sql[semicolonIndex+1:]
	}
	return strings.TrimSpace(sql[:statementEnd]), strings.TrimSpace(suffix)
}

func normalizeDDLSuffix(suffix string) string {
	if suffix == "" {
		return ""
	}
	if unicode.IsSpace(rune(suffix[0])) {
		return suffix
	}
	return " " + suffix
}

func onlineDDLTopLevelTail(sql string) (string, error) {
	targetMasked := maskSQLStringsAndComments(sql, false)
	match := alterTableTargetPattern.FindStringSubmatchIndex(targetMasked)
	if len(match) < 4 {
		match = createIndexTargetPattern.FindStringSubmatchIndex(targetMasked)
	}
	if len(match) < 4 {
		return "", fmt.Errorf("unsupported DDL for online clauses; expected ALTER TABLE or CREATE INDEX variant")
	}

	masked := []byte(maskSQLStringsAndComments(sql, true))
	depth := 0
	for i := match[3]; i < len(masked); i++ {
		switch masked[i] {
		case '(':
			depth++
			masked[i] = ' '
		case ')':
			if depth > 0 {
				depth--
			}
			masked[i] = ' '
		default:
			if depth > 0 {
				masked[i] = ' '
			}
		}
	}
	return string(masked[match[3]:]), nil
}

func onlineDDLClauseValue(topLevelTail, clause string) (string, bool, error) {
	knownValues := "DEFAULT|INPLACE|COPY|INSTANT|NOCOPY"
	if clause == "LOCK" {
		knownValues = "DEFAULT|NONE|SHARED|EXCLUSIVE"
	}

	assignmentPattern := regexp.MustCompile("(?i)\\b" + clause + "\\s*=\\s*([A-Z_]+)")
	assignmentStartPattern := regexp.MustCompile("(?i)\\b" + clause + "\\s*=")
	spacePattern := regexp.MustCompile("(?i)\\b" + clause + "\\s+(" + knownValues + ")\\b")
	assignmentStarts := assignmentStartPattern.FindAllStringIndex(topLevelTail, -1)
	assignments := assignmentPattern.FindAllStringSubmatch(topLevelTail, -1)
	spaces := spacePattern.FindAllStringSubmatch(topLevelTail, -1)
	if len(assignmentStarts) != len(assignments) {
		return "", false, fmt.Errorf("invalid Online DDL clause %s", clause)
	}
	if len(assignments)+len(spaces) == 0 {
		return "", false, nil
	}
	if len(assignments)+len(spaces) > 1 {
		return "", false, fmt.Errorf("duplicate Online DDL clause %s", clause)
	}
	if len(assignments) == 1 {
		return strings.ToUpper(assignments[0][1]), true, nil
	}
	return strings.ToUpper(spaces[0][1]), true, nil
}

func getOnlineDDLClauseSeparator(sql string) (string, error) {
	masked := maskSQLStringsAndComments(sql, false)
	switch {
	case alterTableTargetPattern.MatchString(masked):
		// ALTER TABLE appends options as table_options separated by commas.
		return ", ", nil
	case createIndexTargetPattern.MatchString(masked):
		// CREATE INDEX forms use whitespace before ALGORITHM/LOCK clauses.
		return " ", nil
	default:
		return "", fmt.Errorf("unsupported DDL for online clauses; expected ALTER TABLE or CREATE INDEX variant")
	}
}

func rewriteDDLTargetTable(sql, newTableRef string) (string, error) {
	masked := maskSQLStringsAndComments(sql, false)
	if match := alterTableTargetPattern.FindStringSubmatchIndex(masked); len(match) >= 4 {
		return sql[:match[2]] + newTableRef + sql[match[3]:], nil
	}

	// Column list may follow the table ref immediately (e.g. ON `db`.`tbl`(`col`)).
	// No trailing \b: a word boundary is absent between `)` and `(`.
	if match := createIndexTargetPattern.FindStringSubmatchIndex(masked); len(match) >= 4 {
		return sql[:match[2]] + newTableRef + sql[match[3]:], nil
	}

	return "", fmt.Errorf("could not locate target table in DDL statement")
}

func escapeIdent(id string) string {
	return strings.ReplaceAll(id, "`", "``")
}

func (e *Executor) debugf(options ExecuteOptions, format string, args ...interface{}) {
	if !options.Debug {
		return
	}
	if e.logger != nil {
		e.logger.Infof(format, args...)
		return
	}
	fmt.Printf(format+"\n", args...)
}
