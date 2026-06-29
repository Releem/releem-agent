package postgresql

import (
	"database/sql"

	logging "github.com/google/logger"
	"github.com/hashicorp/go-version"
)

var PG_STAT_VIEWS = []string{
	"pg_stat_archiver", "pg_stat_bgwriter", "pg_stat_database",
	"pg_stat_database_conflicts", "pg_stat_checkpointer",
}

var PG_STAT_PER_DB_VIEWS = []string{
	"pg_stat_user_tables", "pg_statio_user_tables",
	"pg_stat_user_indexes", "pg_statio_user_indexes",
}

var PG_STAT_STATEMENTS = `
SELECT
	COALESCE(d.datname, 'NULL') as datname,
	s.queryid as queryid,
	min(s.query) as query,
	sum(s.calls) AS calls,
	sum(s.total_exec_time) AS total_exec_time,
	COALESCE(sum(s.total_exec_time) / NULLIF(sum(s.calls), 0), 0) AS mean_exec_time,
	sum(s.rows) AS rows_sent
FROM pg_stat_statements s
LEFT JOIN pg_database d ON d.oid = s.dbid
GROUP BY d.datname, s.queryid
`

var PG_STAT_STATEMENTS_OLD_VERSION = `
SELECT
	COALESCE(d.datname, 'NULL') as datname,
	s.queryid::text as queryid,
	min(s.query) as query,
	sum(s.calls) AS calls,
	sum(s.total_time) AS total_exec_time,
	COALESCE(sum(s.total_time) / NULLIF(sum(s.calls), 0), 0) AS mean_exec_time,
	sum(s.rows) AS rows_sent
FROM pg_stat_statements s
LEFT JOIN pg_database d ON d.oid = s.dbid
GROUP BY d.datname, s.queryid
`

var PG_STAT_STATEMENTS_NO_ROWS = `
SELECT
	COALESCE(d.datname, 'NULL') as datname,
	s.queryid as queryid,
	min(s.query) as query,
	sum(s.calls) AS calls,
	sum(s.total_exec_time) AS total_exec_time,
	COALESCE(sum(s.total_exec_time) / NULLIF(sum(s.calls), 0), 0) AS mean_exec_time,
	0::bigint AS rows_sent
FROM pg_stat_statements s
LEFT JOIN pg_database d ON d.oid = s.dbid
GROUP BY d.datname, s.queryid
`

var PG_STAT_STATEMENTS_OLD_VERSION_NO_ROWS = `
SELECT
	COALESCE(d.datname, 'NULL') as datname,
	s.queryid::text as queryid,
	min(s.query) as query,
	sum(s.calls) AS calls,
	sum(s.total_time) AS total_exec_time,
	COALESCE(sum(s.total_time) / NULLIF(sum(s.calls), 0), 0) AS mean_exec_time,
	0::bigint AS rows_sent
FROM pg_stat_statements s
LEFT JOIN pg_database d ON d.oid = s.dbid
GROUP BY d.datname, s.queryid
`

func DetectPgStatStatementsSupportsRows(db *sql.DB, logger logging.Logger) bool {
	var exists bool
	err := db.QueryRow(`
		SELECT EXISTS (
			SELECT 1
			FROM pg_attribute a
			JOIN pg_class c ON c.oid = a.attrelid
			WHERE c.relname = 'pg_stat_statements'
				AND a.attname = 'rows'
				AND NOT a.attisdropped
		)`).Scan(&exists)
	if err != nil {
		logger.Error("Error checking pg_stat_statements rows column: ", err)
		return false
	}
	return exists
}

func PgStatStatementsQuery(pgVersion *version.Version, supportsRows bool) string {
	useOldTiming := pgVersion.LessThan(version.Must(version.NewVersion("13")))
	if useOldTiming {
		if supportsRows {
			return PG_STAT_STATEMENTS_OLD_VERSION
		}
		return PG_STAT_STATEMENTS_OLD_VERSION_NO_ROWS
	}
	if supportsRows {
		return PG_STAT_STATEMENTS
	}
	return PG_STAT_STATEMENTS_NO_ROWS
}
