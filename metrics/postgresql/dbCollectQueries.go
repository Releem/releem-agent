package postgresql

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Releem/mysqlconfigurer/models"
	u "github.com/Releem/mysqlconfigurer/utils"

	"github.com/Releem/mysqlconfigurer/config"
	logging "github.com/google/logger"
)

type DBCollectQueriesOptimization struct {
	logger        logging.Logger
	configuration *config.Config
	capabilities  *PostgresCapabilities
}

func NewDBCollectQueriesOptimization(logger logging.Logger, configuration *config.Config, capabilities *PostgresCapabilities) *DBCollectQueriesOptimization {
	return &DBCollectQueriesOptimization{
		logger:        logger,
		configuration: configuration,
		capabilities:  capabilities,
	}
}

func (DBCollectQueriesOptimization *DBCollectQueriesOptimization) GetMetrics(metrics *models.Metrics) error {
	defer u.HandlePanic(DBCollectQueriesOptimization.configuration, DBCollectQueriesOptimization.logger)
	capabilities := DBCollectQueriesOptimization.capabilities

	capabilitySnapshot, capabilityErr := capabilities.Resolve(context.Background(), models.DB)
	serverVersionNum := capabilitySnapshot.ServerVersionNum
	metrics.DB.Queries = nil
	if capabilityErr != nil {
		DBCollectQueriesOptimization.logger.Info("pg_stat_statements is unavailable, skipping query collection: ", capabilityErr)
	} else {
		statementRows, err := collectPostgresQueryDetails(context.Background(), models.DB, capabilitySnapshot)
		if err != nil {
			capabilities.InvalidateForError(err)
			if err != sql.ErrNoRows {
				DBCollectQueriesOptimization.logger.Error(err)
			}
		} else {
			outputDigest := postgresQueryDetailMap(statementRows)
			if DBCollectQueriesOptimization.configuration.QueryOptimization {
				func() {
					explainState := u.NewExplainCollectionState()
					defer explainState.Close()
					collectExplainDetails(outputDigest, "total_exec_time_us", capabilitySnapshot.SupportsPlanCacheMode, DBCollectQueriesOptimization.logger, DBCollectQueriesOptimization.configuration, explainState)
					collectExplainDetails(outputDigest, "mean_exec_time_us", capabilitySnapshot.SupportsPlanCacheMode, DBCollectQueriesOptimization.logger, DBCollectQueriesOptimization.configuration, explainState)
				}()
			}
			metrics.DB.Queries = postgresQueryDetailMetricsForMode(sortedPostgresQueryDetails(outputDigest), DBCollectQueriesOptimization.configuration.QueryOptimization)
		}
	}

	if !DBCollectQueriesOptimization.configuration.QueryOptimization {
		return nil
	}

	metrics.DB.DatabaseSchema = make(map[string][]models.MetricGroupValue)
	i := 0
	failedSchemaSections := []string{}
	for _, database := range metrics.DB.Metrics.Databases {
		if u.IsSchemaNameExclude(database, DBCollectQueriesOptimization.configuration.DatabasesQueryOptimization) {
			continue
		}
		err := CollectDbSchema(DBCollectQueriesOptimization.configuration, DBCollectQueriesOptimization.logger, database, serverVersionNum, metrics)
		if err != nil {
			DBCollectQueriesOptimization.logger.Error(err)
			var collectionError *postgresSchemaCollectionError
			if errors.As(err, &collectionError) {
				failedSchemaSections = appendUniquePostgresSchemaSections(failedSchemaSections, collectionError.sections...)
			}
		}
		i += 1
		if i%25 == 0 {
			time.Sleep(3 * time.Second)
		}
	}
	metrics.DB.FailedDatabaseSchema = failedSchemaSections
	DBCollectQueriesOptimization.logger.V(5).Info("collectMetrics ", metrics.DB.Queries)
	DBCollectQueriesOptimization.logger.V(5).Info("collectMetrics ", metrics.DB.DatabaseSchema)

	return nil
}

func appendUniquePostgresSchemaSections(existing []string, sections ...string) []string {
	seen := make(map[string]struct{}, len(existing)+len(sections))
	for _, section := range existing {
		seen[section] = struct{}{}
	}
	for _, section := range sections {
		if _, ok := seen[section]; ok {
			continue
		}
		existing = append(existing, section)
		seen[section] = struct{}{}
	}
	return existing
}

func pgDigestKey(datname, queryid string) string {
	return datname + "\x00" + queryid
}
