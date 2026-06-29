package postgresql

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
