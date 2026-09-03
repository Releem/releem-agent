package mysql

import (
	"crypto/sha256"
	"fmt"
	"sort"
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
	// RelationDiscoveryComplete is nil for direct fact fixtures, which remain
	// complete for compatibility. The live gatherer always sets it.
	RelationDiscoveryComplete *bool
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
	primaryPort      int64
	primaryPortOK    bool
	primaryMemberKey string
	lagValue         int64
	lagOK            bool
	state            string
	severity         int
	sortKey          string
	facts            models.MetricGroupValue
}

func NewDBTopologyGatherer(logger logging.Logger, configuration *config.Config) *DBTopologyGatherer {
	return &DBTopologyGatherer{logger: logger, configuration: configuration}
}

func (g *DBTopologyGatherer) GetMetrics(metrics *models.Metrics) error {
	defer utils.HandlePanic(g.configuration, g.logger)

	variables := mapFromMetricGroup(metrics.DB.Conf.Variables)
	replicaStatus, replicaStatusComplete := g.queryReplicaStatus(normalizeKeys(variables))
	groupMembers, groupMembersComplete := g.queryGroupReplicationMembers()
	relationDiscoveryComplete := replicaStatusComplete && groupMembersComplete

	metrics.DB.Topology = BuildTopologyFromFacts(TopologyFacts{
		Variables:                 variables,
		Status:                    mapFromMetricGroup(metrics.DB.Metrics.Status),
		ReplicaStatus:             replicaStatus,
		GroupMembers:              groupMembers,
		RelationDiscoveryComplete: &relationDiscoveryComplete,
	})
	g.logger.V(5).Info("CollectMetrics DBTopology ", metrics.DB.Topology)
	return nil
}

func (g *DBTopologyGatherer) queryOptionalRows(query string) ([]map[string]interface{}, bool) {
	rows, err := models.DB.Query(query)
	if err != nil {
		g.logger.V(5).Info("Topology query skipped: ", err)
		return nil, false
	}
	defer rows.Close()

	return scanTopologyRows(rows, g.logger)
}

func (g *DBTopologyGatherer) queryReplicaStatus(variables map[string]string) ([]map[string]interface{}, bool) {
	return firstSupportedTopologyQueryResult(replicaStatusQueries(variables), g.queryOptionalRows)
}

func firstSupportedTopologyQuery(queries []string, queryRows func(string) ([]map[string]interface{}, bool)) []map[string]interface{} {
	rows, _ := firstSupportedTopologyQueryResult(queries, queryRows)
	return rows
}

func firstSupportedTopologyQueryResult(queries []string, queryRows func(string) ([]map[string]interface{}, bool)) ([]map[string]interface{}, bool) {
	for _, query := range queries {
		rows, supported := queryRows(query)
		if supported {
			return rows, true
		}
	}
	return nil, false
}

func replicaStatusQueries(variables map[string]string) []string {
	vendor := strings.ToLower(firstString(variables, "version") + " " + firstString(variables, "version_comment"))
	if strings.Contains(vendor, "mariadb") {
		return []string{
			"SHOW ALL REPLICAS STATUS",
			"SHOW ALL SLAVES STATUS",
			"SHOW REPLICA STATUS",
			"SHOW SLAVE STATUS",
		}
	}
	return []string{"SHOW REPLICA STATUS", "SHOW SLAVE STATUS"}
}

func (g *DBTopologyGatherer) queryGroupReplicationMembers() ([]map[string]interface{}, bool) {
	groupMembers, supported := g.queryOptionalRows(`
		SELECT MEMBER_ID, MEMBER_HOST, MEMBER_PORT, MEMBER_STATE, MEMBER_ROLE
		FROM performance_schema.replication_group_members`)
	if supported {
		return groupMembers, true
	}

	groupMembers, supported = g.queryOptionalRows(`
		SELECT MEMBER_ID, MEMBER_HOST, MEMBER_PORT, MEMBER_STATE
		FROM performance_schema.replication_group_members`)
	return groupMembers, supported
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
	memberHost := firstString(variables, "hostname")
	memberPort, memberPortOK := optionalInt64(firstString(variables, "port"))

	topology := models.MetricGroupValue{
		"Type":                  "standalone",
		"Role":                  "primary",
		"GroupKey":              memberKey,
		"MemberKey":             memberKey,
		"MemberHost":            nullableString(memberHost),
		"MemberPort":            nullableInt64(memberPort, memberPortOK),
		"PrimaryMemberKey":      nil,
		"PrimaryHost":           nil,
		"PrimaryPort":           nil,
		"IsWriter":              !readOnly && !superReadOnly,
		"IsReader":              topologyIsReader("healthy"),
		"ReadOnly":              readOnly,
		"SuperReadOnly":         superReadOnly,
		"ReplicationLagSeconds": nil,
		"ReplicationState":      "healthy",
		"Facts":                 models.MetricGroupValue{},
	}

	var selected models.MetricGroupValue
	relations := make([]models.MetricGroupValue, 0, 3)
	if isGalera(variables, status) {
		galera := buildGaleraTopology(cloneMetricGroup(topology), variables, status)
		selected = galera
		relations = append(relations, topologyRelation(galera))
	}
	if isGroupReplication(variables, facts.GroupMembers) {
		groupReplication := buildGroupReplicationTopology(cloneMetricGroup(topology), variables, status, facts.GroupMembers, readOnly, superReadOnly)
		if selected == nil {
			selected = groupReplication
		}
		relations = append(relations, topologyRelation(groupReplication))
	}
	if len(facts.ReplicaStatus) > 0 {
		asyncReplication := buildAsyncReplicaTopology(cloneMetricGroup(topology), variables, facts.ReplicaStatus)
		if selected == nil {
			selected = asyncReplication
		}
		asyncRelation := topologyRelation(asyncReplication)
		if topologyString(asyncRelation, "MemberKey") == "" && selected != nil {
			asyncRelation["MemberKey"] = topologyString(selected, "MemberKey")
		}
		relations = append(relations, asyncRelation)
	}

	if selected == nil {
		selected = topology
		relations = append(relations, topologyRelation(topology))
	}
	AttachTopologyRelations(selected, relations, topologyRelationsComplete(facts))
	return selected
}

func topologyRelationsComplete(facts TopologyFacts) bool {
	return facts.RelationDiscoveryComplete == nil || *facts.RelationDiscoveryComplete
}

func buildAsyncReplicaTopology(topology models.MetricGroupValue, variables map[string]string, replicaStatuses []map[string]interface{}) models.MetricGroupValue {
	channels := make([]asyncReplicaChannel, 0, len(replicaStatuses))
	channelFacts := make([]models.MetricGroupValue, 0, len(replicaStatuses))
	selected := asyncReplicaChannel{state: "unknown", severity: asyncReplicationStateSeverity("unknown")}
	for idx, replicaStatus := range replicaStatuses {
		channel := analyzeAsyncReplicaChannel(replicaStatus)
		channels = append(channels, channel)
		channelFacts = append(channelFacts, channel.facts)
		if idx == 0 || preferAsyncReplicaChannel(channel, selected) {
			selected = channel
		}
	}

	topology["Type"] = "async_replication"
	topology["Role"] = "replica"
	topology["GroupKey"] = asyncReplicaGroupKey(channels, firstString(variables, "server_uuid"), selected)
	topology["PrimaryMemberKey"] = nullableString(selected.primaryMemberKey)
	topology["PrimaryHost"] = nullableString(selected.primaryHost)
	topology["PrimaryPort"] = nullableInt64(selected.primaryPort, selected.primaryPortOK)
	topology["IsWriter"] = false
	topology["IsReader"] = topologyIsReader(selected.state)
	if selected.lagOK {
		topology["ReplicationLagSeconds"] = selected.lagValue
	}
	topology["ReplicationState"] = selected.state
	topology["Facts"] = models.MetricGroupValue{
		"ReplicaStatus":   selected.facts,
		"ReplicaChannels": channelFacts,
	}
	return topology
}

func analyzeAsyncReplicaChannel(replicaStatus map[string]interface{}) asyncReplicaChannel {
	row := normalizeKeys(replicaStatus)
	primaryHost := firstNonEmpty(firstString(row, "source_host"), firstString(row, "master_host"))
	primaryPort, primaryPortOK := optionalInt64(firstNonEmpty(firstString(row, "source_port"), firstString(row, "master_port")))
	primaryMemberKey := firstNonEmpty(
		firstString(row, "source_uuid"),
		firstString(row, "master_uuid"),
		firstString(row, "source_server_id"),
		firstString(row, "master_server_id"),
	)
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

	channel := asyncReplicaChannel{
		primaryHost:      primaryHost,
		primaryPort:      primaryPort,
		primaryPortOK:    primaryPortOK,
		primaryMemberKey: primaryMemberKey,
		lagValue:         lagValue,
		lagOK:            lagOK,
		state:            state,
		severity:         asyncReplicationStateSeverity(state),
		facts:            selectReplicaStatusFacts(replicaStatus),
	}
	channel.sortKey = asyncSourceIdentity(channel)
	return channel
}

func buildGroupReplicationTopology(topology models.MetricGroupValue, variables map[string]string, status map[string]string, members []map[string]interface{}, readOnly bool, superReadOnly bool) models.MetricGroupValue {
	memberKey := firstNonEmpty(firstString(variables, "server_uuid"), topologyString(topology, "MemberKey"))
	groupKey := firstNonEmpty(firstString(variables, "group_replication_group_name"), memberKey)
	singlePrimary := !strings.EqualFold(firstString(variables, "group_replication_single_primary_mode"), "OFF")
	role := "member"
	state := "unknown"
	primaryMemberKey := ""
	primaryHost := ""
	var primaryPort int64
	primaryPortOK := false
	memberHosts := make(map[string]string, len(members))
	memberPorts := make(map[string]int64, len(members))

	for _, rawMember := range members {
		member := normalizeKeys(rawMember)
		memberID := firstString(member, "member_id")
		memberRole := strings.ToUpper(firstString(member, "member_role"))
		memberHosts[memberID] = firstString(member, "member_host")
		if memberPort, ok := optionalInt64(firstString(member, "member_port")); ok {
			memberPorts[memberID] = memberPort
		}
		if memberRole == "PRIMARY" && primaryMemberKey == "" {
			primaryMemberKey = memberID
			primaryHost = memberHosts[memberID]
			primaryPort, primaryPortOK = memberPorts[memberID]
		}
		if memberID == memberKey {
			topology["MemberHost"] = nullableString(memberHosts[memberID])
			memberPort, ok := memberPorts[memberID]
			topology["MemberPort"] = nullableInt64(memberPort, ok)
			state = groupMemberState(firstString(member, "member_state"))
			if !singlePrimary || memberRole == "PRIMARY" {
				role = "primary"
			} else if memberRole == "SECONDARY" {
				role = "replica"
			}
		}
	}
	if singlePrimary && primaryMemberKey == "" {
		primaryMemberKey = firstString(status, "group_replication_primary_member")
		primaryHost = memberHosts[primaryMemberKey]
		primaryPort, primaryPortOK = memberPorts[primaryMemberKey]
	}
	if !singlePrimary {
		role = "multi_primary"
		primaryMemberKey = ""
		primaryHost = ""
		primaryPort, primaryPortOK = 0, false
	} else if role == "member" {
		if primaryMemberKey != "" && memberKey == primaryMemberKey {
			role = "primary"
		} else if primaryMemberKey != "" {
			role = "replica"
		} else if !readOnly && !superReadOnly {
			role = "primary"
			primaryMemberKey = memberKey
			primaryHost = memberHosts[memberKey]
			primaryPort, primaryPortOK = memberPorts[memberKey]
		} else {
			role = "replica"
		}
	}

	topology["Type"] = "group_replication"
	topology["Role"] = role
	topology["GroupKey"] = groupKey
	topology["PrimaryMemberKey"] = nullableString(primaryMemberKey)
	topology["PrimaryHost"] = nullableString(primaryHost)
	topology["PrimaryPort"] = nullableInt64(primaryPort, primaryPortOK)
	topology["IsWriter"] = (role == "primary" || role == "multi_primary") && !readOnly && !superReadOnly && state == "healthy"
	topology["IsReader"] = topologyIsReader(state)
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
		firstString(status, "wsrep_node_uuid"),
		firstString(variables, "wsrep_node_name"),
		firstString(status, "wsrep_node_name"),
		firstString(variables, "wsrep_node_address"),
		firstString(status, "wsrep_node_address"),
		topologyString(topology, "MemberKey"),
		firstString(status, "wsrep_local_state_uuid"),
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
	topology["IsReader"] = topologyIsReader(state)
	topology["ReplicationState"] = state
	topology["Facts"] = models.MetricGroupValue{"Variables": selectPrefixed(variables, "wsrep_"), "Status": selectPrefixed(status, "wsrep_")}
	return topology
}

func scanTopologyRows(rows topologyRows, logger logging.Logger) ([]map[string]interface{}, bool) {
	cols, err := rows.Columns()
	if err != nil {
		logger.Error(err)
		return nil, false
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
			return nil, false
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
		return nil, false
	}
	return result, true
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

func topologyIsReader(replicationState string) bool {
	switch replicationState {
	case "healthy", "lagging":
		return true
	default:
		return false
	}
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

func preferAsyncReplicaChannel(candidate asyncReplicaChannel, selected asyncReplicaChannel) bool {
	if candidate.severity != selected.severity {
		return candidate.severity > selected.severity
	}
	if candidate.state == "lagging" && selected.state == "lagging" && candidate.lagOK && selected.lagOK && candidate.lagValue != selected.lagValue {
		return candidate.lagValue > selected.lagValue
	}
	if candidate.sortKey == "" {
		return false
	}
	if selected.sortKey == "" {
		return true
	}
	return candidate.sortKey < selected.sortKey
}

func asyncReplicaGroupKey(channels []asyncReplicaChannel, serverUUID string, selected asyncReplicaChannel) string {
	if len(channels) <= 1 {
		return firstNonEmpty(selected.primaryMemberKey, selected.primaryHost, serverUUID)
	}

	keys := make([]string, 0, len(channels))
	seen := map[string]struct{}{}
	for _, channel := range channels {
		key := asyncSourceIdentity(channel)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return firstNonEmpty(serverUUID, selected.primaryMemberKey, selected.primaryHost)
	}
	sort.Strings(keys)
	if len(keys) == 1 {
		return keys[0]
	}
	digest := sha256.Sum256([]byte(strings.Join(keys, "\x00")))
	return fmt.Sprintf("multi-source:%x", digest)
}

func asyncSourceIdentity(channel asyncReplicaChannel) string {
	memberKey := strings.TrimSpace(channel.primaryMemberKey)
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(channel.primaryHost), "."))
	if memberKey == "" {
		return endpointIdentity(host, channel.primaryPort, channel.primaryPortOK)
	}
	if _, err := strconv.ParseUint(memberKey, 10, 64); err != nil || host == "" {
		return memberKey
	}
	return "server-id:" + memberKey + "@" + endpointIdentity(host, channel.primaryPort, channel.primaryPortOK)
}

func endpointIdentity(host string, port int64, portOK bool) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	if portOK && port >= 1 && port <= 65535 {
		return host + ":" + strconv.FormatInt(port, 10)
	}
	return host
}

func cloneMetricGroup(input models.MetricGroupValue) models.MetricGroupValue {
	output := make(models.MetricGroupValue, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func topologyRelation(topology models.MetricGroupValue) models.MetricGroupValue {
	relation := cloneMetricGroup(topology)
	if facts, ok := topology["Facts"].(models.MetricGroupValue); ok {
		relation["Facts"] = cloneMetricGroup(facts)
	}
	return relation
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

func nullableInt64(value int64, ok bool) interface{} {
	if !ok {
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
		"Source_Port",
		"Master_Port",
		"Source_UUID",
		"Master_UUID",
		"Source_Server_Id",
		"Master_Server_Id",
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
