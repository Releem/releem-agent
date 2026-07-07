package mysql

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/Releem/mysqlconfigurer/utils"
	logging "github.com/google/logger"
)

type TopologyFacts struct {
	Variables     map[string]interface{}
	Status        map[string]interface{}
	ReplicaStatus []map[string]interface{}
	GroupMembers  []map[string]interface{}
}

type DBTopologyGatherer struct {
	logger        logging.Logger
	configuration *config.Config
}

type topologyRows interface {
	Columns() ([]string, error)
	Next() bool
	Scan(dest ...interface{}) error
	Err() error
}

type asyncReplicaChannel struct {
	primaryHost      string
	primaryMemberKey string
	lagValue         int64
	lagOK            bool
	state            string
	severity         int
	facts            models.MetricGroupValue
}

func NewDBTopologyGatherer(logger logging.Logger, configuration *config.Config) *DBTopologyGatherer {
	return &DBTopologyGatherer{logger: logger, configuration: configuration}
}

func (g *DBTopologyGatherer) GetMetrics(metrics *models.Metrics) error {
	defer utils.HandlePanic(g.configuration, g.logger)

	replicaStatus := g.queryOptionalRows("SHOW REPLICA STATUS")
	if len(replicaStatus) == 0 {
		replicaStatus = g.queryOptionalRows("SHOW SLAVE STATUS")
	}

	groupMembers := g.queryGroupReplicationMembers()

	metrics.DB.Topology = BuildTopologyFromFacts(TopologyFacts{
		Variables:     mapFromMetricGroup(metrics.DB.Conf.Variables),
		Status:        mapFromMetricGroup(metrics.DB.Metrics.Status),
		ReplicaStatus: replicaStatus,
		GroupMembers:  groupMembers,
	})
	g.logger.V(5).Info("CollectMetrics DBTopology ", metrics.DB.Topology)
	return nil
}

func (g *DBTopologyGatherer) queryOptionalRows(query string) []map[string]interface{} {
	rows, err := models.DB.Query(query)
	if err != nil {
		if err != sql.ErrNoRows {
			g.logger.V(5).Info("Topology query skipped: ", err)
		}
		return nil
	}
	defer rows.Close()

	return scanTopologyRows(rows, g.logger)
}

func (g *DBTopologyGatherer) queryGroupReplicationMembers() []map[string]interface{} {
	groupMembers := g.queryOptionalRows(`
		SELECT MEMBER_ID, MEMBER_HOST, MEMBER_PORT, MEMBER_STATE, MEMBER_ROLE
		FROM performance_schema.replication_group_members`)
	if len(groupMembers) > 0 {
		return groupMembers
	}

	return g.queryOptionalRows(`
		SELECT MEMBER_ID, MEMBER_HOST, MEMBER_PORT, MEMBER_STATE
		FROM performance_schema.replication_group_members`)
}

func BuildTopologyFromFacts(facts TopologyFacts) models.MetricGroupValue {
	variables := normalizeKeys(facts.Variables)
	status := normalizeKeys(facts.Status)

	readOnly := truthy(firstString(variables, "read_only"))
	superReadOnly := truthy(firstString(variables, "super_read_only"))
	memberKey := firstNonEmpty(
		firstString(variables, "server_uuid"),
		firstString(variables, "server_id"),
	)

	topology := models.MetricGroupValue{
		"Type":                  "standalone",
		"Role":                  "primary",
		"GroupKey":              memberKey,
		"MemberKey":             memberKey,
		"PrimaryMemberKey":      nil,
		"PrimaryHost":           nil,
		"IsWriter":              !readOnly && !superReadOnly,
		"IsReader":              true,
		"ReadOnly":              readOnly,
		"SuperReadOnly":         superReadOnly,
		"ReplicationLagSeconds": nil,
		"ReplicationState":      "healthy",
		"Facts":                 models.MetricGroupValue{},
	}

	if isGalera(variables, status) {
		return buildGaleraTopology(topology, variables, status)
	}
	if isGroupReplication(variables, facts.GroupMembers) {
		return buildGroupReplicationTopology(topology, variables, facts.GroupMembers, readOnly, superReadOnly)
	}
	if len(facts.ReplicaStatus) > 0 {
		return buildAsyncReplicaTopology(topology, variables, facts.ReplicaStatus)
	}

	return topology
}

func buildAsyncReplicaTopology(topology models.MetricGroupValue, variables map[string]string, replicaStatuses []map[string]interface{}) models.MetricGroupValue {
	channels := make([]models.MetricGroupValue, 0, len(replicaStatuses))
	selected := asyncReplicaChannel{state: "unknown", severity: asyncReplicationStateSeverity("unknown")}
	for idx, replicaStatus := range replicaStatuses {
		channel := analyzeAsyncReplicaChannel(replicaStatus)
		channels = append(channels, channel.facts)
		if idx == 0 || channel.severity > selected.severity {
			selected = channel
		}
	}

	topology["Type"] = "async_replication"
	topology["Role"] = "replica"
	topology["GroupKey"] = firstNonEmpty(selected.primaryMemberKey, selected.primaryHost, firstString(variables, "server_uuid"))
	topology["PrimaryMemberKey"] = nullableString(selected.primaryMemberKey)
	topology["PrimaryHost"] = nullableString(selected.primaryHost)
	topology["IsWriter"] = false
	if selected.lagOK {
		topology["ReplicationLagSeconds"] = selected.lagValue
	}
	topology["ReplicationState"] = selected.state
	topology["Facts"] = models.MetricGroupValue{
		"ReplicaStatus":   selected.facts,
		"ReplicaChannels": channels,
	}
	return topology
}

func analyzeAsyncReplicaChannel(replicaStatus map[string]interface{}) asyncReplicaChannel {
	row := normalizeKeys(replicaStatus)
	primaryHost := firstNonEmpty(firstString(row, "source_host"), firstString(row, "master_host"))
	primaryMemberKey := firstNonEmpty(firstString(row, "source_uuid"), firstString(row, "master_uuid"))
	lagValue, lagOK := optionalInt64(firstNonEmpty(firstString(row, "seconds_behind_source"), firstString(row, "seconds_behind_master")))
	ioRunning := strings.EqualFold(firstNonEmpty(firstString(row, "replica_io_running"), firstString(row, "slave_io_running")), "Yes")
	sqlRunning := strings.EqualFold(firstNonEmpty(firstString(row, "replica_sql_running"), firstString(row, "slave_sql_running")), "Yes")

	state := "unknown"
	if ioRunning && sqlRunning {
		state = "healthy"
		if lagOK && lagValue > 0 {
			state = "lagging"
		}
	} else if !ioRunning || !sqlRunning {
		state = "stopped"
	}

	return asyncReplicaChannel{
		primaryHost:      primaryHost,
		primaryMemberKey: primaryMemberKey,
		lagValue:         lagValue,
		lagOK:            lagOK,
		state:            state,
		severity:         asyncReplicationStateSeverity(state),
		facts:            selectReplicaStatusFacts(replicaStatus),
	}
}

func buildGroupReplicationTopology(topology models.MetricGroupValue, variables map[string]string, members []map[string]interface{}, readOnly bool, superReadOnly bool) models.MetricGroupValue {
	memberKey := firstNonEmpty(firstString(variables, "server_uuid"), topologyString(topology, "MemberKey"))
	groupKey := firstNonEmpty(firstString(variables, "group_replication_group_name"), memberKey)
	singlePrimary := !strings.EqualFold(firstString(variables, "group_replication_single_primary_mode"), "OFF")
	role := "member"
	state := "unknown"
	primaryMemberKey := ""
	primaryHost := ""

	for _, rawMember := range members {
		member := normalizeKeys(rawMember)
		memberID := firstString(member, "member_id")
		memberRole := strings.ToUpper(firstString(member, "member_role"))
		if memberRole == "PRIMARY" && primaryMemberKey == "" {
			primaryMemberKey = memberID
			primaryHost = firstString(member, "member_host")
		}
		if memberID == memberKey {
			state = groupMemberState(firstString(member, "member_state"))
			if !singlePrimary || memberRole == "PRIMARY" {
				role = "primary"
			} else if memberRole == "SECONDARY" {
				role = "replica"
			}
		}
	}
	if !singlePrimary {
		role = "multi_primary"
	} else if role == "member" {
		if !readOnly && !superReadOnly {
			role = "primary"
			primaryMemberKey = memberKey
		} else if readOnly || superReadOnly {
			role = "replica"
		}
	}

	topology["Type"] = "group_replication"
	topology["Role"] = role
	topology["GroupKey"] = groupKey
	topology["PrimaryMemberKey"] = nullableString(primaryMemberKey)
	topology["PrimaryHost"] = nullableString(primaryHost)
	topology["IsWriter"] = (role == "primary" || role == "multi_primary") && !readOnly && !superReadOnly
	topology["ReplicationState"] = state
	topology["Facts"] = models.MetricGroupValue{"GroupMembers": members}
	return topology
}

func buildGaleraTopology(topology models.MetricGroupValue, variables map[string]string, status map[string]string) models.MetricGroupValue {
	readOnly := truthy(firstString(variables, "read_only"))
	superReadOnly := truthy(firstString(variables, "super_read_only"))
	groupKey := firstNonEmpty(
		firstString(variables, "wsrep_cluster_state_uuid"),
		firstString(status, "wsrep_cluster_state_uuid"),
		topologyString(topology, "MemberKey"),
	)
	memberKey := firstNonEmpty(
		firstString(variables, "wsrep_node_uuid"),
		firstString(status, "wsrep_local_state_uuid"),
		topologyString(topology, "MemberKey"),
	)
	state := "unknown"
	if truthy(firstString(status, "wsrep_ready")) &&
		truthy(firstString(status, "wsrep_connected")) &&
		strings.EqualFold(firstString(status, "wsrep_cluster_status"), "Primary") &&
		strings.EqualFold(firstString(status, "wsrep_local_state_comment"), "Synced") {
		state = "healthy"
	} else if firstString(status, "wsrep_ready") != "" || firstString(status, "wsrep_cluster_status") != "" {
		state = "error"
	}

	topology["Type"] = "galera_cluster"
	topology["Role"] = "multi_primary"
	topology["GroupKey"] = groupKey
	topology["MemberKey"] = memberKey
	topology["PrimaryMemberKey"] = nil
	topology["PrimaryHost"] = nil
	topology["IsWriter"] = !readOnly && !superReadOnly && state == "healthy"
	topology["ReplicationState"] = state
	topology["Facts"] = models.MetricGroupValue{"Variables": selectPrefixed(variables, "wsrep_"), "Status": selectPrefixed(status, "wsrep_")}
	return topology
}

func scanTopologyRows(rows topologyRows, logger logging.Logger) []map[string]interface{} {
	cols, err := rows.Columns()
	if err != nil {
		logger.Error(err)
		return nil
	}

	result := []map[string]interface{}{}
	for rows.Next() {
		values := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			logger.Error(err)
			return nil
		}
		row := make(map[string]interface{}, len(cols))
		for i, col := range cols {
			switch value := values[i].(type) {
			case []byte:
				row[col] = string(value)
			default:
				row[col] = value
			}
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		logger.Error(err)
		return nil
	}
	return result
}

func mapFromMetricGroup(input models.MetricGroupValue) map[string]interface{} {
	output := make(map[string]interface{}, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func normalizeKeys(input map[string]interface{}) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[strings.ToLower(key)] = strings.TrimSpace(stringValue(value))
	}
	return output
}

func isGalera(variables map[string]string, status map[string]string) bool {
	return truthy(firstString(variables, "wsrep_on")) ||
		firstString(variables, "wsrep_cluster_state_uuid") != "" ||
		firstString(status, "wsrep_cluster_status") != ""
}

func isGroupReplication(variables map[string]string, members []map[string]interface{}) bool {
	return firstString(variables, "group_replication_group_name") != "" || len(members) > 0
}

func groupMemberState(state string) string {
	if strings.EqualFold(state, "ONLINE") {
		return "healthy"
	}
	if state == "" {
		return "unknown"
	}
	return "error"
}

func asyncReplicationStateSeverity(state string) int {
	switch state {
	case "stopped":
		return 3
	case "lagging":
		return 2
	case "unknown":
		return 1
	default:
		return 0
	}
}

func truthy(value string) bool {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "1", "ON", "YES", "TRUE", "PRIMARY", "SYNCED":
		return true
	default:
		return false
	}
}

func optionalInt64(value string) (int64, bool) {
	if value == "" || strings.EqualFold(value, "NULL") {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func firstString(values map[string]string, key string) string {
	return values[strings.ToLower(key)]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func nullableString(value string) interface{} {
	if value == "" {
		return nil
	}
	return value
}

func topologyString(topology models.MetricGroupValue, key string) string {
	return stringValue(topology[key])
}

func stringValue(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func selectReplicaStatusFacts(values map[string]interface{}) models.MetricGroupValue {
	selected := models.MetricGroupValue{}
	for _, key := range []string{
		"Channel_Name",
		"Connection_name",
		"Source_Host",
		"Master_Host",
		"Source_UUID",
		"Master_UUID",
		"Replica_IO_Running",
		"Slave_IO_Running",
		"Replica_SQL_Running",
		"Slave_SQL_Running",
		"Seconds_Behind_Source",
		"Seconds_Behind_Master",
	} {
		if value, ok := values[key]; ok {
			selected[key] = value
		}
	}
	return selected
}

func selectPrefixed(values map[string]string, prefix string) models.MetricGroupValue {
	selected := models.MetricGroupValue{}
	for key, value := range values {
		if strings.HasPrefix(key, prefix) {
			selected[key] = value
		}
	}
	return selected
}
