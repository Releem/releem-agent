package postgresql

import (
	"database/sql"
	"sort"

	"github.com/Releem/mysqlconfigurer/config"
	u "github.com/Releem/mysqlconfigurer/utils"
	logging "github.com/google/logger"
)

func collectExplainDetails(details map[string]PostgresQueryDetail, fieldSorting string, supportsParameterizedExplain bool, logger logging.Logger, configuration *config.Config, state *pgExplainCollectionState) int {
	keys := make([]string, 0, len(details))
	for key := range details {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return postgresQueryDetailSortValue(details[keys[i]], fieldSorting) > postgresQueryDetailSortValue(details[keys[j]], fieldSorting)
	})

	successful := 0
	for _, key := range keys {
		if successful >= maxPgExplainSuccessesPerRanking {
			break
		}
		detail := details[key]
		database := detail.Datname
		if database == "template0" || database == "template1" || database == "NULL" ||
			!isPgExplainableStatement(detail.Query) || detail.Explain != "" || detail.ExplainError != "" {
			continue
		}
		if u.IsSchemaNameExclude(database, configuration.DatabasesQueryOptimization) {
			continue
		}
		if state.databaseFailed(database) {
			detail.ExplainError = "connection_failed"
			details[key] = detail
			continue
		}
		if !state.tryBeginAttempt(pgDigestKey(database, detail.QueryID)) {
			continue
		}

		db := state.connection(database, func() *sql.DB {
			return state.connect(configuration, logger, database)
		})
		if db == nil {
			logger.Error("Connection to database failed: ", database)
			detail.ExplainError = "connection_failed"
			details[key] = detail
			continue
		}
		explain, err := state.executeExplain(db, detail.QueryID, detail.Query, supportsParameterizedExplain, logger)
		if err != nil {
			detail.ExplainError = err.Error()
		}
		if explain != "" {
			detail.Explain = explain
			logger.Info(successful, " OK")
			successful++
		}
		details[key] = detail
	}

	return successful
}

func postgresQueryDetailSortValue(detail PostgresQueryDetail, field string) float64 {
	if field == "mean_exec_time_us" {
		return detail.MeanExecTimeUS
	}
	return detail.TotalExecTimeUS
}

const maxPgExplainSuccessesPerRanking = 100

type pgExplainCollectionState struct {
	attempted       map[string]struct{}
	database        string
	db              *sql.DB
	failedDatabases map[string]struct{}
	connect         func(*config.Config, logging.Logger, string) *sql.DB
	executeExplain  func(*sql.DB, string, string, bool, logging.Logger) (string, error)
}

func newPgExplainCollectionState() *pgExplainCollectionState {
	return &pgExplainCollectionState{
		attempted:      make(map[string]struct{}),
		connect:        u.ConnectionDatabase,
		executeExplain: ExecuteExplain,
	}
}

func (state *pgExplainCollectionState) close() {
	if state.db != nil {
		_ = state.db.Close()
	}
	state.database = ""
	state.db = nil
}

func (state *pgExplainCollectionState) tryBeginAttempt(cacheKey string) bool {
	if cacheKey == "" {
		return false
	}
	if _, exists := state.attempted[cacheKey]; exists {
		return false
	}
	state.attempted[cacheKey] = struct{}{}
	return true
}

func (state *pgExplainCollectionState) databaseFailed(database string) bool {
	_, failed := state.failedDatabases[database]
	return failed
}

func (state *pgExplainCollectionState) connection(database string, connect func() *sql.DB) *sql.DB {
	if state.databaseFailed(database) {
		return nil
	}
	if state.database == database && state.db != nil {
		return state.db
	}
	state.close()
	db := connect()
	if db == nil {
		if state.failedDatabases == nil {
			state.failedDatabases = make(map[string]struct{})
		}
		state.failedDatabases[database] = struct{}{}
		return nil
	}
	state.database = database
	state.db = db
	return state.db
}
