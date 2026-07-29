package utils

import (
	"database/sql"
	"fmt"

	"github.com/Releem/mysqlconfigurer/config"
	logging "github.com/google/logger"
)

// ExplainCollectionState caches the current database handle, deduplicates EXPLAIN
// attempts, and records per-database connection failures for one collection pass.
type ExplainCollectionState struct {
	attempted       map[string]struct{}
	database        string
	db              *sql.DB
	failedDatabases map[string]string
	Connect         func(*config.Config, logging.Logger, string) (*sql.DB, error)
}

func NewExplainCollectionState() *ExplainCollectionState {
	return &ExplainCollectionState{
		attempted: make(map[string]struct{}),
		Connect:   ConnectionDatabaseErr,
	}
}

func (state *ExplainCollectionState) Close() {
	if state == nil {
		return
	}
	if state.db != nil {
		_ = state.db.Close()
	}
	state.database = ""
	state.db = nil
}

func (state *ExplainCollectionState) TryBeginAttempt(cacheKey string) bool {
	if state == nil || cacheKey == "" {
		return false
	}
	if _, exists := state.attempted[cacheKey]; exists {
		return false
	}
	state.attempted[cacheKey] = struct{}{}
	return true
}

func (state *ExplainCollectionState) DatabaseFailed(database string) bool {
	if state == nil || state.failedDatabases == nil {
		return false
	}
	_, failed := state.failedDatabases[database]
	return failed
}

func (state *ExplainCollectionState) FailedReason(database string) string {
	if state == nil || state.failedDatabases == nil {
		return ""
	}
	return state.failedDatabases[database]
}

func (state *ExplainCollectionState) MarkDatabaseFailed(database, reason string) {
	if state == nil {
		return
	}
	if state.failedDatabases == nil {
		state.failedDatabases = make(map[string]string)
	}
	if reason == "" {
		reason = "connection failed"
	}
	state.failedDatabases[database] = reason
}

func (state *ExplainCollectionState) Connection(database string, connect func() (*sql.DB, error)) (*sql.DB, error) {
	if state == nil {
		return nil, fmt.Errorf("explain collection state is nil")
	}
	if state.DatabaseFailed(database) {
		return nil, fmt.Errorf("%s", state.FailedReason(database))
	}
	if state.database == database && state.db != nil {
		return state.db, nil
	}
	state.Close()
	db, err := connect()
	if db == nil {
		reason := "connection failed"
		if err != nil {
			reason = err.Error()
		}
		state.MarkDatabaseFailed(database, reason)
		if err == nil {
			err = fmt.Errorf("%s", reason)
		}
		return nil, err
	}
	state.database = database
	state.db = db
	return state.db, nil
}

func ConnectionFailedExplainError(reason string) string {
	if reason == "" {
		reason = "connection failed"
	}
	return "connection_failed: " + reason
}
