package postgresql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	logging "github.com/google/logger"
	"github.com/lib/pq"
)

type PostgresCapabilitySnapshot struct {
	ServerVersionNum             int
	PgStatStatementsRelation     string
	PgStatStatementsInfoRelation string
	PgStatStatementsColumns      map[string]struct{}
	TimingColumn                 string
	HasRows                      bool
	SupportsPlanCacheMode        bool
}

type postgresCapabilityDetector func(context.Context, *sql.DB) (PostgresCapabilitySnapshot, error)

type PostgresCapabilities struct {
	mu                  sync.RWMutex
	cached              *PostgresCapabilitySnapshot
	detect              postgresCapabilityDetector
	versionMu           sync.Mutex
	serverVersionNum    int
	detectServerVersion func(context.Context, *sql.DB) (int, error)
	logger              logging.Logger
}

func NewPostgresCapabilities(logger logging.Logger) *PostgresCapabilities {
	capabilities := &PostgresCapabilities{logger: logger}
	capabilities.detect = capabilities.detectFromDatabase
	capabilities.detectServerVersion = detectPostgresServerVersion
	return capabilities
}

func (capabilities *PostgresCapabilities) ServerVersion(ctx context.Context, db *sql.DB) (int, error) {
	capabilities.versionMu.Lock()
	defer capabilities.versionMu.Unlock()
	if capabilities.serverVersionNum != 0 {
		return capabilities.serverVersionNum, nil
	}
	detect := capabilities.detectServerVersion
	if detect == nil {
		detect = detectPostgresServerVersion
	}
	serverVersionNum, err := detect(ctx, db)
	if err != nil {
		return 0, err
	}
	capabilities.serverVersionNum = serverVersionNum
	return serverVersionNum, nil
}

func (capabilities *PostgresCapabilities) Resolve(ctx context.Context, db *sql.DB) (PostgresCapabilitySnapshot, error) {
	capabilities.mu.RLock()
	if capabilities.cached != nil {
		result := clonePostgresCapabilitySnapshot(*capabilities.cached)
		capabilities.mu.RUnlock()
		return result, nil
	}
	capabilities.mu.RUnlock()

	capabilities.mu.Lock()
	defer capabilities.mu.Unlock()
	if capabilities.cached != nil {
		return clonePostgresCapabilitySnapshot(*capabilities.cached), nil
	}
	detect := capabilities.detect
	if detect == nil {
		detect = capabilities.detectFromDatabase
	}
	detected, err := detect(ctx, db)
	if err != nil {
		return PostgresCapabilitySnapshot{}, err
	}
	stored := clonePostgresCapabilitySnapshot(detected)
	capabilities.cached = &stored
	return clonePostgresCapabilitySnapshot(stored), nil
}

func (capabilities *PostgresCapabilities) Invalidate() {
	capabilities.mu.Lock()
	capabilities.cached = nil
	capabilities.mu.Unlock()
}

func (capabilities *PostgresCapabilities) InvalidateForError(err error) {
	if isPostgresCapabilityInvalidatingError(err) {
		capabilities.Invalidate()
	}
}

func clonePostgresCapabilitySnapshot(snapshot PostgresCapabilitySnapshot) PostgresCapabilitySnapshot {
	result := snapshot
	result.PgStatStatementsColumns = make(map[string]struct{}, len(snapshot.PgStatStatementsColumns))
	for column := range snapshot.PgStatStatementsColumns {
		result.PgStatStatementsColumns[column] = struct{}{}
	}
	return result
}

func (capabilities *PostgresCapabilities) detectFromDatabase(ctx context.Context, db *sql.DB) (PostgresCapabilitySnapshot, error) {
	if db == nil {
		return PostgresCapabilitySnapshot{}, fmt.Errorf("postgres capabilities require a database connection")
	}

	var result PostgresCapabilitySnapshot
	serverVersionNum, err := capabilities.ServerVersion(ctx, db)
	if err != nil {
		return PostgresCapabilitySnapshot{}, fmt.Errorf("detect PostgreSQL server version: %w", err)
	}
	result.ServerVersionNum = serverVersionNum

	relation, err := detectPgStatStatementsNamedRelationContext(ctx, db, "pg_stat_statements")
	if err != nil {
		return PostgresCapabilitySnapshot{}, fmt.Errorf("detect pg_stat_statements relation: %w", err)
	}
	result.PgStatStatementsRelation = relation

	infoRelation, err := detectPgStatStatementsNamedRelationContext(ctx, db, "pg_stat_statements_info")
	if err == nil {
		result.PgStatStatementsInfoRelation = infoRelation
	} else if err != sql.ErrNoRows {
		return PostgresCapabilitySnapshot{}, fmt.Errorf("detect pg_stat_statements_info relation: %w", err)
	}

	rows, err := db.QueryContext(ctx, "SELECT * FROM "+relation+" LIMIT 0")
	if err != nil {
		return PostgresCapabilitySnapshot{}, fmt.Errorf("inspect pg_stat_statements columns: %w", err)
	}
	columns, columnsErr := rows.Columns()
	closeErr := rows.Close()
	if columnsErr != nil {
		return PostgresCapabilitySnapshot{}, fmt.Errorf("inspect pg_stat_statements columns: %w", columnsErr)
	}
	if closeErr != nil {
		return PostgresCapabilitySnapshot{}, fmt.Errorf("close pg_stat_statements inspection: %w", closeErr)
	}
	result.PgStatStatementsColumns = make(map[string]struct{}, len(columns))
	for _, column := range columns {
		result.PgStatStatementsColumns[strings.ToLower(column)] = struct{}{}
	}
	if err := validatePgStatStatementsColumns(result.PgStatStatementsColumns); err != nil {
		return PostgresCapabilitySnapshot{}, err
	}

	if _, ok := result.PgStatStatementsColumns["total_exec_time"]; ok {
		result.TimingColumn = "total_exec_time"
	} else if _, ok := result.PgStatStatementsColumns["total_time"]; ok {
		result.TimingColumn = "total_time"
	} else {
		return PostgresCapabilitySnapshot{}, fmt.Errorf("pg_stat_statements has neither total_exec_time nor total_time")
	}
	_, result.HasRows = result.PgStatStatementsColumns["rows"]
	result.SupportsPlanCacheMode = result.ServerVersionNum >= 120000
	return result, nil
}

func validatePgStatStatementsColumns(columns map[string]struct{}) error {
	for _, required := range []string{"dbid", "queryid", "query", "calls"} {
		if _, ok := columns[required]; !ok {
			return fmt.Errorf("pg_stat_statements is missing required column %s", required)
		}
	}
	return nil
}

func detectPostgresServerVersion(ctx context.Context, db *sql.DB) (int, error) {
	if db == nil {
		return 0, fmt.Errorf("PostgreSQL server version requires a database connection")
	}
	var serverVersionNum int
	if err := db.QueryRowContext(ctx, "SHOW server_version_num").Scan(&serverVersionNum); err != nil {
		return 0, err
	}
	return serverVersionNum, nil
}

func detectPgStatStatementsNamedRelationContext(ctx context.Context, db *sql.DB, relationName string) (string, error) {
	var schema, relation string
	err := db.QueryRowContext(ctx, `
		SELECT namespace.nspname, relation.relname
		FROM pg_extension extension
		JOIN pg_namespace namespace ON namespace.oid = extension.extnamespace
		JOIN pg_class relation ON relation.relnamespace = namespace.oid
		WHERE extension.extname = 'pg_stat_statements'
			AND relation.relname = $1`, relationName).Scan(&schema, &relation)
	if err != nil {
		return "", err
	}
	return pgStatStatementsQualifiedRelation(schema, relation), nil
}

func isPostgresCapabilityInvalidatingError(err error) bool {
	var pgError *pq.Error
	if !errors.As(err, &pgError) {
		return false
	}
	return pgError.Code == "42P01" || pgError.Code == "42703"
}
