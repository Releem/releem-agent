package postgresql

import "strings"

func pgStatStatementsQualifiedRelation(schema, relation string) string {
	return quotePgIdentifier(schema) + "." + quotePgIdentifier(relation)
}

func quotePgIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

var PG_STAT_VIEWS = []string{
	"pg_stat_archiver", "pg_stat_bgwriter", "pg_stat_database",
	"pg_stat_database_conflicts", "pg_stat_checkpointer",
}

var PG_STAT_PER_DB_VIEWS = []string{
	"pg_stat_user_tables", "pg_statio_user_tables",
	"pg_stat_user_indexes", "pg_statio_user_indexes",
}
