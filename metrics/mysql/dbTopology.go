package mysql

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/Releem/mysqlconfigurer/utils"
	drivermysql "github.com/go-sql-driver/mysql"
	logging "github.com/google/logger"
)

const topologyQueryTimeout = 10 * time.Second
const maxTopologyRows = 4096

type topologyRowsQuery func(string, ...any) ([]map[string]string, error)

type DBTopologyGatherer struct {
	logger        logging.Logger
	configuration *config.Config
}

func NewDBTopologyGatherer(logger logging.Logger, configuration *config.Config) *DBTopologyGatherer {
	return &DBTopologyGatherer{logger: logger, configuration: configuration}
}

func (g *DBTopologyGatherer) GetMetrics(metrics *models.Metrics) error {
	defer utils.HandlePanic(g.configuration, g.logger)
	variables := make(map[string]string)
	for key, value := range metrics.DB.Conf.Variables {
		variables[strings.ToLower(key)] = fmt.Sprint(value)
	}
	metrics.DB.Topology = collectTopologyFacts(variables, queryTopologyStringRows)
	return nil
}

// collectTopologyFacts serializes observations, not roles or relation decisions.
func collectTopologyFacts(variables map[string]string, query topologyRowsQuery) models.MetricGroupValue {
	replica, replicaStatus := collectTopologyQuery(query, replicaStatusQueries(variables))
	groups := []map[string]string{}
	groupStatus := "unsupported"
	metadata := models.MetricGroupValue{"SchemaVersion": "", "Tables": map[string][]map[string]string{}}
	metadataStatus := "unsupported"
	if !isMariaDB(variables) {
		groups, groupStatus = collectTopologyQuery(query, []string{
			"SELECT MEMBER_ID, MEMBER_HOST, MEMBER_PORT, MEMBER_STATE, MEMBER_ROLE FROM performance_schema.replication_group_members",
			"SELECT MEMBER_ID, MEMBER_HOST, MEMBER_PORT, MEMBER_STATE FROM performance_schema.replication_group_members",
		})
		metadata, metadataStatus = collectInnoDBFacts(query)
	}
	return models.MetricGroupValue{
		"Version": 1, "ReplicaStatus": filterReplicaRows(replica), "GroupMembers": groups,
		"InnoDBMetadata": metadata,
		"Sources":        map[string]string{"ReplicaStatus": replicaStatus, "GroupMembers": groupStatus, "InnoDBMetadata": metadataStatus},
	}
}

func replicaStatusQueries(variables map[string]string) []string {
	if isMariaDB(variables) {
		return []string{"SHOW ALL REPLICAS STATUS", "SHOW ALL SLAVES STATUS", "SHOW REPLICA STATUS", "SHOW SLAVE STATUS"}
	}
	return []string{"SHOW REPLICA STATUS", "SHOW SLAVE STATUS"}
}

func isMariaDB(variables map[string]string) bool {
	return strings.Contains(strings.ToLower(variables["version"]+" "+variables["version_comment"]), "mariadb")
}

func collectTopologyQuery(query topologyRowsQuery, statements []string) ([]map[string]string, string) {
	for _, statement := range statements {
		rows, err := query(statement)
		if err == nil {
			if rows == nil {
				rows = []map[string]string{}
			}
			return rows, "ok"
		}
		if !unsupportedTopologyQuery(err) {
			return []map[string]string{}, "error"
		}
	}
	return []map[string]string{}, "unsupported"
}

func unsupportedTopologyQuery(err error) bool {
	var mysqlErr *drivermysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	switch mysqlErr.Number {
	case 1049, 1054, 1064, 1109, 1146:
		return true
	}
	return false
}

func queryTopologyStringRows(query string, args ...any) ([]map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), topologyQueryTimeout)
	defer cancel()
	rows, err := models.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := []map[string]string{}
	for rows.Next() {
		if len(result) >= maxTopologyRows {
			return nil, fmt.Errorf("topology query exceeds %d rows", maxTopologyRows)
		}
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row := make(map[string]string, len(columns))
		for i, col := range columns {
			switch v := values[i].(type) {
			case nil:
				row[col] = ""
			case []byte:
				row[col] = string(v)
			default:
				row[col] = fmt.Sprint(v)
			}
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// Keep only replication observations; SHOW REPLICA STATUS also returns user names and SQL errors.
func filterReplicaRows(rows []map[string]string) []map[string]string {
	allowed := map[string]bool{}
	for _, key := range []string{"channel_name", "connection_name", "source_host", "master_host", "source_port", "master_port", "source_uuid", "master_uuid", "source_server_id", "master_server_id", "replica_io_running", "slave_io_running", "replica_sql_running", "slave_sql_running", "seconds_behind_source", "seconds_behind_master"} {
		allowed[key] = true
	}
	result := []map[string]string{}
	for _, row := range rows {
		selected := map[string]string{}
		for key, value := range row {
			if allowed[strings.ToLower(key)] {
				selected[key] = value
			}
		}
		result = append(result, selected)
	}
	return result
}
