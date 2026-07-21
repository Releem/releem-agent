package postgresql

import (
	"database/sql"
	"sort"

	"github.com/Releem/mysqlconfigurer/config"
	u "github.com/Releem/mysqlconfigurer/utils"
	logging "github.com/google/logger"
)

func collectExplainDetails(details map[string]PostgresQueryDetail, fieldSorting string, supportsParameterizedExplain bool, logger logging.Logger, configuration *config.Config, state *u.ExplainCollectionState) int {
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
		if state.DatabaseFailed(database) {
			detail.ExplainError = u.ConnectionFailedExplainError(state.FailedReason(database))
			details[key] = detail
			continue
		}
		if !state.TryBeginAttempt(pgDigestKey(database, detail.QueryID)) {
			continue
		}

		db, err := state.Connection(database, func() (*sql.DB, error) {
			return state.Connect(configuration, logger, database)
		})
		if db == nil {
			logger.Error("Connection to database failed: ", database, " ", err)
			detail.ExplainError = u.ConnectionFailedExplainError(state.FailedReason(database))
			details[key] = detail
			continue
		}
		explain, err := ExecuteExplain(db, detail.QueryID, detail.Query, supportsParameterizedExplain, logger)
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
