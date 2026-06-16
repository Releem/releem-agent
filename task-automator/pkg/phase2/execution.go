package phase2

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/Releem/mysqlconfigurer/config"
)

// BackupMethod represents the type of backup to perform
type BackupMethod string

const (
	BackupNone       BackupMethod = "none"
	BackupMysqldump  BackupMethod = "mysqldump"
	BackupXtrabackup BackupMethod = "xtrabackup"
)

// Executor handles Phase 2 schema change execution
type Logger interface {
	Infof(format string, v ...interface{})
	Errorf(format string, v ...interface{})
}

type Executor struct {
	conn   *sql.DB
	logger Logger
}

// NewExecutor creates a new executor instance
func NewExecutor(conn *sql.DB, logger Logger) *Executor {
	return &Executor{conn: conn, logger: logger}
}

// ExecuteOptions contains options for schema change execution
type ExecuteOptions struct {
	SQL          string
	TableName    string
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

// Execute performs Phase 2 schema change execution
func (e *Executor) Execute(options ExecuteOptions) (*ExecuteResult, error) {
	result := &ExecuteResult{
		Warnings: []string{},
		Errors:   []string{},
	}

	if strings.TrimSpace(options.TableName) == "" {
		return nil, fmt.Errorf("table name is required for schema change execution")
	}

	// Validate datadir filesystem headroom before attempting any schema change.
	if options.Config == nil || !options.Config.DisableSpaceChecks {
		if err := e.checkDataDirFilesystemCapacity(options); err != nil {
			return nil, err
		}
	}

	// 2.1. Perform backup if specified
	if options.BackupMethod != BackupNone {
		backupPath, err := e.performBackup(options)
		if err != nil {
			return nil, fmt.Errorf("backup failed: %w", err)
		}
		result.BackupPerformed = true
		result.BackupPath = backupPath
	}

	// 2.3. Execute using Online DDL if allowed
	if options.OkOnlineDDL {
		if err := e.executeWithOnlineDDL(options, result); err != nil {
			// Fall back to pt-online-schema-change when Online DDL preflight
			// signals that an in-place algorithm is unsupported (the MySQL
			// error suggests "Try ALGORITHM=COPY") and pt-osc is allowed.
			if options.OkPTOSC && strings.Contains(err.Error(), "Try ALGORITHM=COPY") {
				if e.logger != nil {
					e.logger.Infof("Online DDL not possible for table %s (%v); falling back to pt-online-schema-change", options.TableName, err)
				}
				if ptErr := e.executeWithPTOSC(options); ptErr != nil {
					return nil, fmt.Errorf("pt-online-schema-change execution failed: %w", ptErr)
				}
				result.ChangeExecuted = true
				result.MethodUsed = "pt-online-schema-change"
				return result, nil
			}
			return nil, fmt.Errorf("%w", err)
		}
		// <<<<< TEST ONLINE DDL AGAINST EMPTY TABLE with SAME ENGINE AND SCHEMA
		result.ChangeExecuted = true
		result.MethodUsed = "Online DDL"
		return result, nil
	} else if options.OkPTOSC {
		if err := e.executeWithPTOSC(options); err != nil {
			return nil, fmt.Errorf("pt-online-schema-change execution failed: %w", err)
		}
		result.ChangeExecuted = true
		result.MethodUsed = "pt-online-schema-change"
		return result, nil
	} else {
		result.ChangeExecuted = false
		return nil, fmt.Errorf("schema change could not be executed")

	}
}

func (e *Executor) performBackup(options ExecuteOptions) (string, error) {
	// Check disk space before performing backup
	if options.Config == nil || !options.Config.DisableSpaceChecks {
		if err := e.checkDiskSpace(options); err != nil {
			if e.logger != nil {
				e.logger.Errorf("disk space check failed for table %s: %v", options.TableName, err)
			}
			return "", err
		}
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

func (e *Executor) checkDataDirFilesystemCapacity(options ExecuteOptions) error {
	tableInfo, err := ParseTableName(options.TableName, func() (string, error) {
		var db string
		err := e.conn.QueryRow("SELECT DATABASE()").Scan(&db)
		return db, err
	})
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

	var stat syscall.Statfs_t
	if err := syscall.Statfs(dataDir.String, &stat); err != nil {
		return fmt.Errorf("failed to check datadir filesystem capacity: %w", err)
	}

	totalBytes := int64(stat.Blocks) * int64(stat.Bsize)
	freeBytes := int64(stat.Bavail) * int64(stat.Bsize)
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

	// Parse table name
	tableInfo, err := ParseTableName(options.TableName, func() (string, error) {
		var db string
		err := e.conn.QueryRow("SELECT DATABASE()").Scan(&db)
		return db, err
	})
	if err != nil {
		return "", err
	}

	// Use config values
	mysqldump := options.Config.MysqldumpPath
	if mysqldump == "" {
		mysqldump = "mysqldump"
	}

	// Ensure backup directory exists
	if err := os.MkdirAll(options.Config.BackupDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create backup directory: %w", err)
	}

	// Generate timestamp prefix in YYMMDDHHMMSS format
	timestamp := time.Now().Format("060102150405")
	backupPath := fmt.Sprintf("%s/%s_%s_%s.sql", options.Config.BackupDir, timestamp, tableInfo.Database, tableInfo.Table)

	args := []string{
		"-h", host,
		"-P", port,
		"-u", user,
		"-p" + password,
		tableInfo.Database,
		tableInfo.Table,
		"--single-transaction",
		"--quick",
		"--lock-tables=false",
		"-r", backupPath,
	}

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

	// Parse table name
	tableInfo, err := ParseTableName(options.TableName, func() (string, error) {
		var db string
		err := e.conn.QueryRow("SELECT DATABASE()").Scan(&db)
		return db, err
	})
	if err != nil {
		return "", err
	}

	// Use config values
	xtrabackup := options.Config.XtrabackupPath
	if xtrabackup == "" {
		xtrabackup = "xtrabackup"
	}

	// Ensure backup directory exists
	if err := os.MkdirAll(options.Config.BackupDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create backup directory: %w", err)
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
func (e *Executor) checkDiskSpace(options ExecuteOptions) error {
	var estimatedSize int64

	// Parse table name to get database and table
	tableInfo, err := ParseTableName(options.TableName, func() (string, error) {
		var db string
		err := e.conn.QueryRow("SELECT DATABASE()").Scan(&db)
		return db, err
	})
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

	// Check available space in backup directory
	var stat syscall.Statfs_t
	err = syscall.Statfs(options.Config.BackupDir, &stat)
	if err != nil {
		return fmt.Errorf("failed to check disk space: %w", err)
	}

	// Calculate available space (blocks * block size)
	availableBytes := int64(stat.Bavail) * int64(stat.Bsize)

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
	if err := e.dryRunPTOSC(options); err != nil {
		return fmt.Errorf("pt-online-schema-change dry-run failed: %w", err)
	}

	// Execute actual change
	return e.runPTOSC(options)
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

	tableInfo, err := ParseTableName(options.TableName, func() (string, error) {
		var db string
		err := e.conn.QueryRow("SELECT DATABASE()").Scan(&db)
		return db, err
	})
	if err != nil {
		return err
	}

	// Extract ALTER statement
	alterSQL := ExtractAlterStatement(options.SQL)
	if alterSQL == "" {
		alterSQL = options.SQL
	}

	args := []string{
		"--dry-run",
		fmt.Sprintf("h=%s,P=%s,u=%s,p=%s,D=%s,t=%s", host, port, user, password, tableInfo.Database, tableInfo.Table),
		fmt.Sprintf("--alter=%s", alterSQL),
	}

	cmd := exec.Command(ptosc, args...)

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

	output, err := cmd.CombinedOutput()
	if options.Debug {
		e.debugf(options, "[DEBUG] pt-online-schema-change dry-run output:\n%s", string(output))
		if err != nil {
			e.debugf(options, "[DEBUG] pt-online-schema-change dry-run error: %v", err)
		}
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

	tableInfo, err := ParseTableName(options.TableName, func() (string, error) {
		var db string
		err := e.conn.QueryRow("SELECT DATABASE()").Scan(&db)
		return db, err
	})
	if err != nil {
		return err
	}

	alterSQL := ExtractAlterStatement(options.SQL)
	if alterSQL == "" {
		alterSQL = options.SQL
	}

	args := []string{
		"--execute",
		fmt.Sprintf("h=%s,P=%s,u=%s,p=%s,D=%s,t=%s", host, port, user, password, tableInfo.Database, tableInfo.Table),
		fmt.Sprintf("--alter=%s", alterSQL),
	}

	cmd := exec.Command(ptosc, args...)

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

	output, err := cmd.CombinedOutput()
	if options.Debug {
		e.debugf(options, "[DEBUG] pt-online-schema-change execute output:\n%s", string(output))
		if err != nil {
			e.debugf(options, "[DEBUG] pt-online-schema-change execute error: %v", err)
		}
	}

	if err != nil {
		return fmt.Errorf("pt-online-schema-change failed: %s", string(output))
	}

	return nil
}

func (e *Executor) executeWithOnlineDDL(options ExecuteOptions, result *ExecuteResult) error {
	tableInfo, err := ParseTableName(options.TableName, func() (string, error) {
		var db string
		err := e.conn.QueryRow("SELECT DATABASE()").Scan(&db)
		return db, err
	})
	if err != nil {
		return fmt.Errorf("failed to parse table name: %w", err)
	}

	testSchema := ""
	if options.Config != nil {
		testSchema = options.Config.OnlineDDLTestSchema
	}
	if strings.TrimSpace(testSchema) == "" {
		return fmt.Errorf("test schema is required for online DDL preflight")
	}

	testTableName := fmt.Sprintf("_releem_ddl_test_%s_%d", tableInfo.Table, time.Now().UnixNano())
	testTableRef := fmt.Sprintf("`%s`.`%s`", escapeIdent(testSchema), escapeIdent(testTableName))
	sourceTableRef := fmt.Sprintf("`%s`.`%s`", escapeIdent(tableInfo.Database), escapeIdent(tableInfo.Table))

	if _, err = e.conn.Exec(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", escapeIdent(testSchema))); err != nil {
		return fmt.Errorf("failed to create test schema %s: %w", testSchema, err)
	}
	if _, err = e.conn.Exec(fmt.Sprintf("CREATE TABLE %s LIKE %s", testTableRef, sourceTableRef)); err != nil {
		return fmt.Errorf("failed to create test table %s: %w", testTableRef, err)
	}
	defer func() {
		_, _ = e.conn.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", testTableRef))
	}()

	finalSQL, err := buildOnlineDDLSQL(options.SQL)
	if err != nil {
		return err
	}
	testSQL, err := rewriteDDLTargetTable(finalSQL, testTableRef)
	if err != nil {
		return fmt.Errorf("failed to prepare test DDL SQL: %w", err)
	}

	if options.Debug {
		e.debugf(options, "[DEBUG] Online DDL preflight SQL (test table):\n%s", testSQL)
	}
	if _, err = e.conn.Exec(testSQL); err != nil {
		return fmt.Errorf("online DDL preflight failed on test table %s: %w", testTableRef, err)
	}

	if options.Debug {
		e.debugf(options, "[DEBUG] Executing Online DDL statement:\n%s", finalSQL)
	}
	if _, err = e.conn.Exec("SET SESSION lock_wait_timeout = 20"); err != nil {
		return fmt.Errorf("failed to set session lock_wait_timeout: %w", err)
	}
	_, err = e.conn.Exec(finalSQL)
	if options.Debug {
		if err != nil {
			e.debugf(options, "[DEBUG] Online DDL execution error: %v", err)
		} else {
			e.debugf(options, "[DEBUG] Online DDL execution successful")
		}
	}

	if err != nil {
		// Check if error is due to Online DDL not being supported
		errStr := err.Error()
		if strings.Contains(errStr, "ALGORITHM=INPLACE") ||
			strings.Contains(errStr, "LOCK=NONE") ||
			strings.Contains(errStr, "ALGORITHM=INPLACE is not supported") {
			result.Warnings = append(result.Warnings,
				"Online DDL not supported for this operation")
			return err
		}
		return err
	}

	return nil
}

func buildOnlineDDLSQL(sql string) (string, error) {
	sql = strings.TrimSpace(strings.TrimSuffix(sql, ";"))
	if sql == "" {
		return "", fmt.Errorf("empty SQL statement")
	}

	upperSQL := strings.ToUpper(sql)
	hasAlgorithm := strings.Contains(upperSQL, "ALGORITHM=")
	hasLock := strings.Contains(upperSQL, "LOCK=")
	if hasAlgorithm && hasLock {
		return sql, nil
	}

	separator, err := getOnlineDDLClauseSeparator(upperSQL)
	if err != nil {
		return "", err
	}

	if !hasAlgorithm {
		sql += separator + "ALGORITHM=INPLACE"
	}
	if !hasLock {
		sql += separator + "LOCK=NONE"
	}

	return sql, nil
}

func getOnlineDDLClauseSeparator(upperSQL string) (string, error) {
	switch {
	case strings.HasPrefix(upperSQL, "ALTER TABLE "):
		// ALTER TABLE appends options as table_options separated by commas.
		return ", ", nil
	case strings.HasPrefix(upperSQL, "CREATE INDEX "),
		strings.HasPrefix(upperSQL, "CREATE UNIQUE INDEX "),
		strings.HasPrefix(upperSQL, "CREATE FULLTEXT INDEX "),
		strings.HasPrefix(upperSQL, "CREATE SPATIAL INDEX "),
		strings.HasPrefix(upperSQL, "CREATE OR REPLACE INDEX "),
		strings.HasPrefix(upperSQL, "CREATE INDEX IF NOT EXISTS "):
		// CREATE INDEX forms use whitespace before ALGORITHM/LOCK clauses.
		return " ", nil
	default:
		return "", fmt.Errorf("unsupported DDL for online clauses; expected ALTER TABLE or CREATE INDEX variant")
	}
}

func rewriteDDLTargetTable(sql, newTableRef string) (string, error) {
	reAlter := regexp.MustCompile("(?i)^\\s*ALTER\\s+TABLE\\s+((`[^`]+`|[A-Za-z0-9_]+)(\\.(?:`[^`]+`|[A-Za-z0-9_]+))?)")
	if m := reAlter.FindStringSubmatch(sql); len(m) > 1 {
		return strings.Replace(sql, m[1], newTableRef, 1), nil
	}

	// Column list may follow the table ref immediately (e.g. ON `db`.`tbl`(`col`)).
	// No trailing \b: a word boundary is absent between `)` and `(`.
	reCreateIdx := regexp.MustCompile("(?i)\\bON\\s+((`[^`]+`|[A-Za-z0-9_]+)(\\.(?:`[^`]+`|[A-Za-z0-9_]+))?)")
	if m := reCreateIdx.FindStringSubmatch(sql); len(m) > 1 {
		return strings.Replace(sql, m[1], newTableRef, 1), nil
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
