package mysql

import (
	"errors"
	"strings"

	"github.com/Releem/mysqlconfigurer/models"
	drivermysql "github.com/go-sql-driver/mysql"
)

// collectInnoDBFacts selects version-compatible columns without interpreting memberships.
func collectInnoDBFacts(query topologyRowsQuery) (models.MetricGroupValue, string) {
	tables := map[string][]map[string]string{}
	facts := models.MetricGroupValue{"SchemaVersion": "", "Tables": tables}
	schema, err := query("SELECT SCHEMA_NAME FROM information_schema.schemata WHERE SCHEMA_NAME = ?", "mysql_innodb_cluster_metadata")
	if err != nil {
		return facts, "error"
	}
	if len(schema) == 0 {
		_, err = query("SHOW TABLES FROM `mysql_innodb_cluster_metadata`")
		var mysqlErr *drivermysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1049 {
			return facts, "unsupported"
		}
		if err != nil {
			return facts, "error"
		}
	}
	columns, err := query("SELECT TABLE_NAME, COLUMN_NAME FROM information_schema.columns WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME, ORDINAL_POSITION", "mysql_innodb_cluster_metadata")
	if err != nil {
		return facts, "error"
	}
	available := map[string]map[string]bool{}
	for _, row := range columns {
		table, column := row["TABLE_NAME"], row["COLUMN_NAME"]
		if table == "" {
			table = row["table_name"]
		}
		if column == "" {
			column = row["column_name"]
		}
		if available[table] == nil {
			available[table] = map[string]bool{}
		}
		available[table][column] = true
	}
	read := func(table string, names []string, optional ...string) bool {
		for _, name := range names {
			if !available[table][name] {
				return false
			}
		}
		selected := append([]string{}, names...)
		for _, name := range optional {
			if available[table][name] {
				selected = append(selected, name)
			}
		}
		quoted := make([]string, len(selected))
		for i, name := range selected {
			quoted[i] = "`" + name + "`"
		}
		rows, readErr := query("SELECT " + strings.Join(quoted, ", ") + " FROM `mysql_innodb_cluster_metadata`.`" + table + "`")
		if readErr != nil {
			return false
		}
		if rows == nil {
			rows = []map[string]string{}
		}
		tables[table] = rows
		return true
	}
	if !read("schema_version", []string{"major", "minor", "patch"}) || len(tables["schema_version"]) != 1 {
		return facts, "error"
	}
	version := tables["schema_version"][0]
	facts["SchemaVersion"] = version["major"] + "." + version["minor"] + "." + version["patch"]
	switch version["major"] {
	case "1":
		if !read("clusters", []string{"cluster_id", "cluster_name", "default_replicaset"}) ||
			!read("replicasets", []string{"replicaset_id", "cluster_id", "replicaset_type", "topology_type", "active", "attributes"}) ||
			!read("instances", []string{"instance_id", "replicaset_id", "mysql_server_uuid", "instance_name", "addresses"}) {
			return facts, "error"
		}
	case "2":
		if !read("v2_gr_clusters", []string{"cluster_type", "primary_mode", "cluster_id", "cluster_name", "group_name"}, "clusterset_id") ||
			!read("v2_instances", []string{"instance_id", "cluster_id", "label", "mysql_server_uuid", "address", "endpoint"}) {
			return facts, "error"
		}
		if version["minor"] != "0" || available["v2_cs_clustersets"] != nil || available["v2_cs_members"] != nil {
			if !read("v2_cs_clustersets", []string{"clusterset_id", "domain_name"}) ||
				!read("v2_cs_members", []string{"clusterset_id", "cluster_id", "member_role", "master_cluster_id", "invalidated"}) {
				return facts, "error"
			}
		}
	default:
		return facts, "error"
	}
	return facts, "ok"
}
