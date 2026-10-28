package postgresql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"

	"github.com/Releem/mysqlconfigurer/models"
)

type PostgresQueryStats struct {
	Datname         string
	QueryID         string
	PgQueryID       string
	TopLevel        sql.NullBool
	Calls           uint64
	TotalExecTimeUS float64
	MeanExecTimeUS  float64
}

type PostgresQueryDetail struct {
	PostgresQueryStats
	Query        string
	Rows         uint64
	Explain      string
	ExplainError string
}

type postgresQueryProfile uint8

const (
	postgresQueryLightweight postgresQueryProfile = iota
	postgresQueryFull
)

func (stats PostgresQueryStats) metricGroupValue() models.MetricGroupValue {
	metric := models.MetricGroupValue{
		"datname":            stats.Datname,
		"queryid":            stats.QueryID,
		"calls":              stats.Calls,
		"total_exec_time_us": stats.TotalExecTimeUS,
		"mean_exec_time_us":  stats.MeanExecTimeUS,
	}
	if stats.PgQueryID != "" {
		metric["pg_queryid"] = stats.PgQueryID
		metric["toplevel"] = nil
		if stats.TopLevel.Valid {
			metric["toplevel"] = stats.TopLevel.Bool
		}
	}
	return metric
}

func (detail PostgresQueryDetail) metricGroupValue() models.MetricGroupValue {
	metric := detail.PostgresQueryStats.metricGroupValue()
	metric["query"] = detail.Query
	metric["rows"] = detail.Rows
	if detail.Explain != "" {
		metric["explain"] = detail.Explain
	}
	if detail.ExplainError != "" {
		metric["explain_error"] = detail.ExplainError
	}
	return metric
}

func pgStatStatementsQuery(capabilities PostgresCapabilitySnapshot, profile postgresQueryProfile) string {
	queryColumn := ""
	rowsExpression := ""
	topLevelExpression := "NULL::boolean"
	groupBy := "s.dbid, d.datname, s.queryid"
	if _, exists := capabilities.PgStatStatementsColumns["toplevel"]; exists {
		topLevelExpression = "s.toplevel"
		groupBy += ", s.toplevel"
	}
	if profile == postgresQueryFull {
		queryColumn = "\n\tmin(s.query) AS query,"

		rowsExpression = ",\n\t0::bigint AS rows"
		if capabilities.HasRows {
			rowsExpression = ",\n\tsum(s.rows)::bigint AS rows"
		}
	}
	return fmt.Sprintf(`
SELECT
	COALESCE(d.datname, 'NULL') AS datname,
	s.dbid::text AS dbid,
	s.queryid::text AS pg_queryid,
	%s AS toplevel,%s
	sum(s.calls)::bigint AS calls,
	(sum(s.%s) * 1000)::double precision AS total_exec_time_us,
	(COALESCE(sum(s.%s) / NULLIF(sum(s.calls), 0), 0) * 1000)::double precision AS mean_exec_time_us%s
FROM %s s
LEFT JOIN pg_database d ON d.oid = s.dbid
WHERE s.queryid IS NOT NULL
GROUP BY %s`, topLevelExpression, queryColumn, capabilities.TimingColumn, capabilities.TimingColumn, rowsExpression, capabilities.PgStatStatementsRelation, groupBy)
}

func collectPostgresQueryStats(ctx context.Context, db *sql.DB, capabilities PostgresCapabilitySnapshot) ([]PostgresQueryStats, error) {
	return queryContextRows(ctx, db, pgStatStatementsQuery(capabilities, postgresQueryLightweight), nil, func(rows *sql.Rows) (PostgresQueryStats, error) {
		var result PostgresQueryStats
		var databaseID string
		err := rows.Scan(&result.Datname, &databaseID, &result.PgQueryID, &result.TopLevel, &result.Calls, &result.TotalExecTimeUS, &result.MeanExecTimeUS)
		result.QueryID = postgresQueryIdentity(databaseID, result.PgQueryID, result.TopLevel)
		return result, err
	})
}

func collectPostgresQueryDetails(ctx context.Context, db *sql.DB, capabilities PostgresCapabilitySnapshot) ([]PostgresQueryDetail, error) {
	return queryContextRows(ctx, db, pgStatStatementsQuery(capabilities, postgresQueryFull), nil, func(rows *sql.Rows) (PostgresQueryDetail, error) {
		var result PostgresQueryDetail
		var databaseID string
		err := rows.Scan(&result.Datname, &databaseID, &result.PgQueryID, &result.TopLevel, &result.Query, &result.Calls, &result.TotalExecTimeUS, &result.MeanExecTimeUS, &result.Rows)
		result.QueryID = postgresQueryIdentity(databaseID, result.PgQueryID, result.TopLevel)
		return result, err
	})
}

func postgresQueryStatsMetrics(rows []PostgresQueryStats) []models.MetricGroupValue {
	result := make([]models.MetricGroupValue, 0, len(rows))
	for _, row := range rows {
		result = append(result, row.metricGroupValue())
	}
	return result
}

func postgresQueryDetailMetrics(rows []PostgresQueryDetail) []models.MetricGroupValue {
	result := make([]models.MetricGroupValue, 0, len(rows))
	for _, row := range rows {
		result = append(result, row.metricGroupValue())
	}
	return result
}

func postgresQueryDetailMap(rows []PostgresQueryDetail) map[string]PostgresQueryDetail {
	result := make(map[string]PostgresQueryDetail, len(rows))
	for _, row := range rows {
		result[pgDigestKey(row.Datname, row.QueryID)] = row
	}
	return result
}

func sortedPostgresQueryDetails(rows map[string]PostgresQueryDetail) []PostgresQueryDetail {
	result := make([]PostgresQueryDetail, 0, len(rows))
	for _, row := range rows {
		result = append(result, row)
	}
	return result
}

func normalizePgQueryID(queryID string) string {
	trimmed := strings.TrimSpace(queryID)
	if trimmed == "" {
		return queryID
	}
	if signedValue, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return strconv.FormatUint(uint64(signedValue), 10)
	}
	if unsignedValue, err := strconv.ParseUint(trimmed, 10, 64); err == nil {
		return strconv.FormatUint(unsignedValue, 10)
	}
	return queryID
}

// postgresQueryIdentity separates execution levels while keeping one identity
// across roles. Its encoding is versioned and shared by both collection profiles.
func postgresQueryIdentity(databaseID, queryID string, topLevel sql.NullBool) string {
	level := "unknown"
	if topLevel.Valid {
		level = strconv.FormatBool(topLevel.Bool)
	}
	identity := "pgss:v1:" + databaseID + ":" + normalizePgQueryID(queryID) + ":" + level
	digest := sha256.Sum256([]byte(identity))
	return strconv.FormatUint(binary.BigEndian.Uint64(digest[:8]), 10)
}
