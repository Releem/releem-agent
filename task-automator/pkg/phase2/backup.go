package phase2

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
)

// BackupMethod represents the type of backup to perform
type BackupMethod string

const (
	BackupNone       BackupMethod = "none"
	BackupMysqldump  BackupMethod = "mysqldump"
	BackupXtrabackup BackupMethod = "xtrabackup"
)

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

func (e *Executor) backupWithMysqldump(options ExecuteOptions) (string, error) {
	if options.Config == nil {
		return "", fmt.Errorf("config is required for backup")
	}

	configuration := options.Config
	host := configuration.MysqlHost
	port := configuration.MysqlPort
	user := configuration.MysqlUser
	password := configuration.MysqlPassword
	if host == "" {
		return "", fmt.Errorf("mysql_host is required for backup")
	}

	tableInfo, err := resolvedExecutionTarget(options)
	if err != nil {
		return "", err
	}

	// Use config values
	mysqldump := configuration.MysqldumpPath
	if mysqldump == "" {
		mysqldump = "mysqldump"
	}

	// Generate timestamp prefix in YYMMDDHHMMSS format
	timestamp := time.Now().Format("060102150405")
	backupPath := fmt.Sprintf("%s/%s_%s_%s.sql", configuration.BackupDir, timestamp, tableInfo.Database, tableInfo.Table)

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
		safeArgs := redactCommandArgs(args, password)
		e.debugf(options, "[DEBUG] mysqldump command: %s", mysqldump)
		e.debugf(options, "[DEBUG] mysqldump args: %s", strings.Join(safeArgs, " "))
	}

	output, err := cmd.CombinedOutput()
	e.logExternalCommandOutput(options, "mysqldump", "backup", output, err)

	if err != nil {
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

	configuration := options.Config
	host := configuration.MysqlHost
	port := configuration.MysqlPort
	user := configuration.MysqlUser
	password := configuration.MysqlPassword
	if host == "" {
		return "", fmt.Errorf("mysql_host is required for backup")
	}

	tableInfo, err := resolvedExecutionTarget(options)
	if err != nil {
		return "", err
	}

	// Use config values
	xtrabackup := configuration.XtrabackupPath
	if xtrabackup == "" {
		xtrabackup = "xtrabackup"
	}

	// Generate timestamp prefix in YYMMDDHHMMSS format
	timestamp := time.Now().Format("060102150405")
	// Create a unique backup directory for this table
	backupDir := fmt.Sprintf("%s/%s_xtrabackup_%s_%s", configuration.BackupDir, timestamp, tableInfo.Database, tableInfo.Table)

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
		safeArgs := redactCommandArgs(backupArgs, password)
		e.debugf(options, "[DEBUG] xtrabackup backup command: %s", xtrabackup)
		e.debugf(options, "[DEBUG] xtrabackup backup args: %s", strings.Join(safeArgs, " "))
	}

	cmd := exec.Command(xtrabackup, backupArgs...)
	output, err := cmd.CombinedOutput()
	e.logExternalCommandOutput(options, "xtrabackup", "backup", output, err)

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
	e.logExternalCommandOutput(options, "xtrabackup", "prepare", output, err)

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
