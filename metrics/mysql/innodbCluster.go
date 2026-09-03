package mysql

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/Releem/mysqlconfigurer/models"
)

const (
	innodbMetadataSchema        = "mysql_innodb_cluster_metadata"
	clusterSetChannelName       = "clusterset_replication"
	metadataSchemaQuery         = `SELECT SCHEMA_NAME FROM information_schema.schemata WHERE SCHEMA_NAME = ?`
	metadataColumnsQuery        = `SELECT TABLE_NAME, COLUMN_NAME FROM information_schema.columns WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME, ORDINAL_POSITION`
	metadataSchemaVersion       = "schema_version"
	metadataV1Clusters          = "clusters"
	metadataV1ReplicaSets       = "replicasets"
	metadataV1Instances         = "instances"
	metadataV2Clusters          = "v2_gr_clusters"
	metadataV2Instances         = "v2_instances"
	metadataV2ClusterSets       = "v2_cs_clustersets"
	metadataV2ClusterSetMembers = "v2_cs_members"
)

// InnoDBMetadata is a generation-neutral snapshot of MySQL Shell metadata.
type InnoDBMetadata struct {
	SchemaVersion string
	Clusters      []InnoDBCluster
	Instances     []InnoDBInstance
	ClusterSets   []InnoDBClusterSet
	Complete      bool
}

// InnoDBCluster describes one Group Replication cluster managed by MySQL Shell.
type InnoDBCluster struct {
	ID           string
	MetadataID   string
	Name         string
	GroupName    string
	PrimaryMode  string
	ClusterSetID string
	ReplicaSetID string
}

// InnoDBInstance identifies one instance registered in an InnoDB Cluster.
type InnoDBInstance struct {
	ID           string
	ClusterID    string
	ReplicaSetID string
	ServerUUID   string
	Label        string
	Address      string
}

// InnoDBClusterSet describes one cluster membership in a ClusterSet.
type InnoDBClusterSet struct {
	ID               string
	Name             string
	ClusterID        string
	PrimaryClusterID string
	Role             string
	ChannelName      string
	ReplicationState string
	Invalidated      bool
}

type topologyRowsQuery func(query string, args ...any) ([]map[string]string, error)

// DiscoverInnoDBMetadata maps supported MySQL Shell metadata generations.
func DiscoverInnoDBMetadata(query topologyRowsQuery) (InnoDBMetadata, error) {
	metadata := InnoDBMetadata{
		Clusters:    []InnoDBCluster{},
		Instances:   []InnoDBInstance{},
		ClusterSets: []InnoDBClusterSet{},
	}
	if query == nil {
		return metadata, fmt.Errorf("inspect InnoDB metadata schema: query function is nil")
	}

	schemaRows, err := query(metadataSchemaQuery, innodbMetadataSchema)
	if err != nil {
		return metadata, fmt.Errorf("check InnoDB metadata schema: %w", err)
	}
	if len(schemaRows) == 0 {
		metadata.Complete = true
		return metadata, nil
	}

	columnRows, err := query(metadataColumnsQuery, innodbMetadataSchema)
	if err != nil {
		return metadata, fmt.Errorf("inspect InnoDB metadata schema columns: %w", err)
	}
	columns := metadataColumns(columnRows)
	if err := requireMetadataColumns(columns, metadataSchemaVersion, "major", "minor", "patch"); err != nil {
		return metadata, err
	}

	versionRows, err := query(metadataSelect(metadataSchemaVersion, []string{"major", "minor", "patch"}))
	if err != nil {
		return metadata, fmt.Errorf("read InnoDB metadata schema version: %w", err)
	}
	if len(versionRows) != 1 {
		return metadata, fmt.Errorf("read InnoDB metadata schema version: got %d rows, want 1", len(versionRows))
	}
	version := normalizeStringRow(versionRows[0])
	major := version["major"]
	metadata.SchemaVersion = strings.Join([]string{major, version["minor"], version["patch"]}, ".")

	switch major {
	case "1":
		err = discoverInnoDBMetadataV1(query, columns, &metadata)
	case "2":
		err = discoverInnoDBMetadataV2(query, columns, &metadata)
	default:
		err = fmt.Errorf("unsupported InnoDB metadata schema version %q", metadata.SchemaVersion)
	}
	if err != nil {
		return metadata, err
	}

	sortInnoDBMetadata(&metadata)
	metadata.Complete = true
	return metadata, nil
}

func discoverInnoDBMetadataV1(query topologyRowsQuery, columns map[string]map[string]struct{}, metadata *InnoDBMetadata) error {
	if err := requireMetadataColumns(columns, metadataV1Clusters, "cluster_id", "cluster_name", "default_replicaset"); err != nil {
		return err
	}
	if err := requireMetadataColumns(columns, metadataV1ReplicaSets, "replicaset_id", "cluster_id", "replicaset_type", "topology_type", "active", "attributes"); err != nil {
		return err
	}
	if err := requireMetadataColumns(columns, metadataV1Instances, "instance_id", "replicaset_id", "mysql_server_uuid", "instance_name", "addresses"); err != nil {
		return err
	}

	clusterRows, err := query(metadataSelect(metadataV1Clusters, []string{"cluster_id", "cluster_name", "default_replicaset"}))
	if err != nil {
		return fmt.Errorf("read InnoDB metadata 1.x clusters: %w", err)
	}
	replicaSetRows, err := query(metadataSelect(metadataV1ReplicaSets, []string{"replicaset_id", "cluster_id", "replicaset_type", "topology_type", "active", "attributes"}))
	if err != nil {
		return fmt.Errorf("read InnoDB metadata 1.x replicasets: %w", err)
	}
	instanceRows, err := query(metadataSelect(metadataV1Instances, []string{"instance_id", "replicaset_id", "mysql_server_uuid", "instance_name", "addresses"}))
	if err != nil {
		return fmt.Errorf("read InnoDB metadata 1.x instances: %w", err)
	}

	clustersByMetadataID := make(map[string]map[string]string, len(clusterRows))
	for _, rawRow := range clusterRows {
		row := normalizeStringRow(rawRow)
		clustersByMetadataID[row["cluster_id"]] = row
	}

	clusterIDByReplicaSet := make(map[string]string, len(replicaSetRows))
	for _, rawRow := range replicaSetRows {
		row := normalizeStringRow(rawRow)
		if !strings.EqualFold(row["replicaset_type"], "gr") || !truthy(row["active"]) {
			continue
		}
		clusterRow, ok := clustersByMetadataID[row["cluster_id"]]
		if !ok || clusterRow["default_replicaset"] != row["replicaset_id"] {
			continue
		}
		groupID := metadataJSONField(row["attributes"], "group_replication_group_name")
		if groupID == "" {
			groupID = CompositeTopologyKey("innodb-cluster", []string{row["cluster_id"], clusterRow["cluster_name"]})
		}
		metadata.Clusters = append(metadata.Clusters, InnoDBCluster{
			ID:           groupID,
			MetadataID:   row["cluster_id"],
			Name:         clusterRow["cluster_name"],
			GroupName:    groupID,
			PrimaryMode:  row["topology_type"],
			ReplicaSetID: row["replicaset_id"],
		})
		clusterIDByReplicaSet[row["replicaset_id"]] = groupID
	}

	for _, rawRow := range instanceRows {
		row := normalizeStringRow(rawRow)
		clusterID := clusterIDByReplicaSet[row["replicaset_id"]]
		if clusterID == "" {
			continue
		}
		metadata.Instances = append(metadata.Instances, InnoDBInstance{
			ID:           row["instance_id"],
			ClusterID:    clusterID,
			ReplicaSetID: row["replicaset_id"],
			ServerUUID:   row["mysql_server_uuid"],
			Label:        row["instance_name"],
			Address:      firstNonEmpty(metadataJSONField(row["addresses"], "mysqlClassic"), row["instance_name"]),
		})
	}
	return nil
}

func discoverInnoDBMetadataV2(query topologyRowsQuery, columns map[string]map[string]struct{}, metadata *InnoDBMetadata) error {
	if err := requireMetadataColumns(columns, metadataV2Clusters, "cluster_type", "primary_mode", "cluster_id", "cluster_name", "group_name"); err != nil {
		return err
	}
	if err := requireMetadataColumns(columns, metadataV2Instances, "instance_id", "cluster_id", "label", "mysql_server_uuid", "address", "endpoint"); err != nil {
		return err
	}

	clusterColumns := presentMetadataColumns(columns, metadataV2Clusters, []string{"cluster_type", "primary_mode", "cluster_id", "cluster_name", "group_name", "clusterset_id"})
	clusterRows, err := query(metadataSelectWhere(metadataV2Clusters, clusterColumns, "cluster_type = ?"), "gr")
	if err != nil {
		return fmt.Errorf("read InnoDB metadata 2.x clusters: %w", err)
	}
	instanceRows, err := query(metadataSelect(metadataV2Instances, []string{"instance_id", "cluster_id", "label", "mysql_server_uuid", "address", "endpoint"}))
	if err != nil {
		return fmt.Errorf("read InnoDB metadata 2.x instances: %w", err)
	}

	for _, rawRow := range clusterRows {
		row := normalizeStringRow(rawRow)
		if !strings.EqualFold(row["cluster_type"], "gr") {
			continue
		}
		metadata.Clusters = append(metadata.Clusters, InnoDBCluster{
			ID:           row["cluster_id"],
			MetadataID:   row["cluster_id"],
			Name:         row["cluster_name"],
			GroupName:    row["group_name"],
			PrimaryMode:  row["primary_mode"],
			ClusterSetID: row["clusterset_id"],
		})
	}
	for _, rawRow := range instanceRows {
		row := normalizeStringRow(rawRow)
		metadata.Instances = append(metadata.Instances, InnoDBInstance{
			ID:         row["instance_id"],
			ClusterID:  row["cluster_id"],
			ServerUUID: row["mysql_server_uuid"],
			Label:      row["label"],
			Address:    firstNonEmpty(row["endpoint"], row["address"]),
		})
	}

	versionParts := strings.Split(metadata.SchemaVersion, ".")
	minorVersion, err := strconv.Atoi(versionParts[1])
	if err != nil {
		return fmt.Errorf("unsupported InnoDB metadata schema version %q", metadata.SchemaVersion)
	}
	hasClusterSets := metadataTableExists(columns, metadataV2ClusterSets) || metadataTableExists(columns, metadataV2ClusterSetMembers)
	if !hasClusterSets && minorVersion == 0 {
		return nil
	}
	if err := requireMetadataColumns(columns, metadataV2ClusterSets, "clusterset_id", "domain_name"); err != nil {
		return err
	}
	if err := requireMetadataColumns(columns, metadataV2ClusterSetMembers, "clusterset_id", "cluster_id", "member_role", "master_cluster_id", "invalidated"); err != nil {
		return err
	}

	setRows, err := query(metadataSelect(metadataV2ClusterSets, []string{"clusterset_id", "domain_name"}))
	if err != nil {
		return fmt.Errorf("read InnoDB metadata 2.x ClusterSets: %w", err)
	}
	memberRows, err := query(metadataSelect(metadataV2ClusterSetMembers, []string{"clusterset_id", "cluster_id", "member_role", "master_cluster_id", "invalidated"}))
	if err != nil {
		return fmt.Errorf("read InnoDB metadata 2.x ClusterSet members: %w", err)
	}
	setNames := make(map[string]string, len(setRows))
	for _, rawRow := range setRows {
		row := normalizeStringRow(rawRow)
		setNames[row["clusterset_id"]] = row["domain_name"]
	}
	primaryClusters := authoritativeClusterSetPrimaries(memberRows)
	for _, rawRow := range memberRows {
		row := normalizeStringRow(rawRow)
		setID := row["clusterset_id"]
		role := clusterSetRole(row["member_role"])
		metadata.ClusterSets = append(metadata.ClusterSets, InnoDBClusterSet{
			ID:               setID,
			Name:             setNames[setID],
			ClusterID:        row["cluster_id"],
			PrimaryClusterID: primaryClusters[setID],
			Role:             role,
			ChannelName:      clusterSetChannelName,
			ReplicationState: "unknown",
			Invalidated:      truthy(row["invalidated"]),
		})
	}
	return nil
}

// BuildInnoDBRelations adds semantic cluster relations to live physical facts.
func BuildInnoDBRelations(base models.MetricGroupValue, metadata InnoDBMetadata) []models.MetricGroupValue {
	clusters := make(map[string]InnoDBCluster, len(metadata.Clusters))
	for _, cluster := range metadata.Clusters {
		if cluster.ID != "" {
			clusters[cluster.ID] = cluster
		}
	}
	instance, ok := currentInnoDBInstance(base, metadata.Instances, clusters)
	if !ok {
		return []models.MetricGroupValue{}
	}
	cluster := clusters[instance.ClusterID]
	clusterRelation := buildInnoDBClusterRelation(base, metadata.SchemaVersion, cluster, instance)
	relations := []models.MetricGroupValue{clusterRelation}

	if clusterSet, ok := currentInnoDBClusterSet(cluster.ID, metadata.ClusterSets); ok {
		relations = append(relations, buildInnoDBClusterSetRelation(clusterRelation, metadata.SchemaVersion, clusterSet))
	}
	return relations
}

func buildInnoDBClusterRelation(base models.MetricGroupValue, version string, cluster InnoDBCluster, instance InnoDBInstance) models.MetricGroupValue {
	memberKey := innodbMemberKey(base, instance)
	memberHost, memberPort, memberPortOK := topologyEndpoint(base, instance.Address)
	state := topologyString(base, "ReplicationState")
	if topologyString(base, "Type") != "group_replication" || state == "" {
		state = "unknown"
	}
	isHealthy := state == "healthy"
	isMultiPrimary := strings.EqualFold(cluster.PrimaryMode, "mm") || strings.EqualFold(cluster.PrimaryMode, "multi-primary")
	role := "secondary"
	if isMultiPrimary {
		role = "multi_primary"
	} else if topologyString(base, "Role") == "primary" {
		role = "primary"
	}

	primaryMemberKey := base["PrimaryMemberKey"]
	primaryHost := base["PrimaryHost"]
	primaryPort := base["PrimaryPort"]
	if isMultiPrimary {
		primaryMemberKey = nil
		primaryHost = nil
		primaryPort = nil
	} else if role == "primary" && isHealthy && firstNonEmpty(stringValue(primaryMemberKey), memberKey) == memberKey {
		if stringValue(primaryMemberKey) == "" {
			primaryMemberKey = memberKey
		}
		if stringValue(primaryHost) == "" {
			primaryHost = nullableString(memberHost)
		}
		if _, ok := optionalInt64(stringValue(primaryPort)); !ok {
			primaryPort = nullableInt64(memberPort, memberPortOK)
		}
	}
	isWriterRole := role == "primary" || role == "multi_primary"

	return models.MetricGroupValue{
		"Type":                  "innodb_cluster",
		"Role":                  role,
		"GroupKey":              cluster.ID,
		"MemberKey":             memberKey,
		"MemberHost":            nullableString(memberHost),
		"MemberPort":            nullableInt64(memberPort, memberPortOK),
		"PrimaryMemberKey":      primaryMemberKey,
		"PrimaryHost":           primaryHost,
		"PrimaryPort":           primaryPort,
		"ParentGroupKey":        nil,
		"IsWriter":              isHealthy && isWriterRole && truthy(stringValue(base["IsWriter"])),
		"IsReader":              isHealthy && truthy(stringValue(base["IsReader"])),
		"ReadOnly":              truthy(stringValue(base["ReadOnly"])),
		"SuperReadOnly":         truthy(stringValue(base["SuperReadOnly"])),
		"ReplicationLagSeconds": nil,
		"ReplicationState":      state,
		"Facts": models.MetricGroupValue{
			"MetadataVersion":   version,
			"MetadataClusterID": cluster.MetadataID,
			"ClusterName":       cluster.Name,
			"GroupName":         cluster.GroupName,
			"PrimaryMode":       cluster.PrimaryMode,
			"ClusterSetID":      nullableString(cluster.ClusterSetID),
			"InstanceID":        instance.ID,
			"InstanceLabel":     instance.Label,
			"InstanceAddress":   instance.Address,
			"ServerUUID":        instance.ServerUUID,
		},
	}
}

func withClusterSetReplicationState(metadata InnoDBMetadata, replicaStatuses []map[string]interface{}) InnoDBMetadata {
	state, ok := clusterSetReplicationState(replicaStatuses)
	if !ok {
		return metadata
	}
	metadata.ClusterSets = append([]InnoDBClusterSet(nil), metadata.ClusterSets...)
	for index := range metadata.ClusterSets {
		if metadata.ClusterSets[index].Role == "replica_cluster" && !metadata.ClusterSets[index].Invalidated {
			metadata.ClusterSets[index].ReplicationState = state
		}
	}
	return metadata
}

func clusterSetReplicationState(replicaStatuses []map[string]interface{}) (string, bool) {
	var selected asyncReplicaChannel
	found := false
	for _, replicaStatus := range replicaStatuses {
		row := normalizeKeys(replicaStatus)
		channelName := firstNonEmpty(firstString(row, "channel_name"), firstString(row, "connection_name"))
		if !strings.EqualFold(channelName, clusterSetChannelName) {
			continue
		}
		candidate := analyzeAsyncReplicaChannel(replicaStatus)
		if !found || preferAsyncReplicaChannel(candidate, selected) {
			selected = candidate
			found = true
		}
	}
	return selected.state, found
}

func innodbMemberKey(base models.MetricGroupValue, instance InnoDBInstance) string {
	memberKey := firstNonEmpty(topologyString(base, "MemberKey"), instance.ServerUUID)
	if memberKey != "" {
		if len(memberKey) <= maxTopologyKeyLength {
			return memberKey
		}
		return CompositeTopologyKey("innodb-member", []string{memberKey})
	}
	fallback := firstNonEmpty(instance.Address, instance.ID)
	if fallback == "" {
		return ""
	}
	return CompositeTopologyKey("innodb-member", []string{fallback})
}

func buildInnoDBClusterSetRelation(clusterRelation models.MetricGroupValue, version string, clusterSet InnoDBClusterSet) models.MetricGroupValue {
	relation := topologyRelation(clusterRelation)
	relation["Type"] = "innodb_clusterset"
	relation["GroupKey"] = clusterSet.ID
	relation["Role"] = clusterSet.Role
	relation["ParentGroupKey"] = nil
	if clusterSet.Role == "replica_cluster" {
		relation["ParentGroupKey"] = nullableString(clusterSet.PrimaryClusterID)
		relation["PrimaryMemberKey"] = nil
		relation["PrimaryHost"] = nil
		relation["PrimaryPort"] = nil
		relation["IsWriter"] = false
		if topologyString(clusterRelation, "ReplicationState") == "healthy" {
			relation["ReplicationState"] = firstNonEmpty(clusterSet.ReplicationState, "unknown")
		}
	}
	if clusterSet.Role != "primary_cluster" {
		relation["IsWriter"] = false
	}
	if clusterSet.PrimaryClusterID == "" || (clusterSet.Role == "primary_cluster" && clusterSet.PrimaryClusterID != clusterSet.ClusterID) {
		relation["PrimaryMemberKey"] = nil
		relation["PrimaryHost"] = nil
		relation["PrimaryPort"] = nil
		relation["IsWriter"] = false
	}
	relation["Facts"] = models.MetricGroupValue{
		"MetadataVersion":  version,
		"ClusterSetID":     clusterSet.ID,
		"ClusterSetName":   clusterSet.Name,
		"ClusterID":        clusterSet.ClusterID,
		"PrimaryClusterID": nullableString(clusterSet.PrimaryClusterID),
		"ChannelName":      clusterSet.ChannelName,
		"ChannelState":     clusterSet.ReplicationState,
	}
	return relation
}

func currentInnoDBInstance(base models.MetricGroupValue, instances []InnoDBInstance, clusters map[string]InnoDBCluster) (InnoDBInstance, bool) {
	memberKey := topologyString(base, "MemberKey")
	memberHost := topologyString(base, "MemberHost")
	memberPort := topologyString(base, "MemberPort")
	groupKey := topologyString(base, "GroupKey")
	var fallback InnoDBInstance
	hasFallback := false
	for _, instance := range instances {
		cluster, exists := clusters[instance.ClusterID]
		if !exists {
			continue
		}
		matchesMember := memberKey != "" && strings.EqualFold(memberKey, instance.ServerUUID)
		matchesMember = matchesMember || (memberKey != "" && memberKey == instance.ID)
		matchesMember = matchesMember || (memberHost != "" && endpointMatches(instance.Address, memberHost, memberPort))
		if !matchesMember {
			continue
		}
		if groupKey != "" && (strings.EqualFold(groupKey, cluster.GroupName) || groupKey == cluster.ID) {
			return instance, true
		}
		if !hasFallback {
			fallback = instance
			hasFallback = true
		}
	}
	return fallback, hasFallback
}

func currentInnoDBClusterSet(clusterID string, clusterSets []InnoDBClusterSet) (InnoDBClusterSet, bool) {
	for _, clusterSet := range clusterSets {
		if clusterSet.ClusterID == clusterID && !clusterSet.Invalidated && clusterSet.Role != "" {
			return clusterSet, true
		}
	}
	return InnoDBClusterSet{}, false
}

func authoritativeClusterSetPrimaries(rows []map[string]string) map[string]string {
	primaryIDs := make(map[string]string)
	counts := make(map[string]int)
	for _, rawRow := range rows {
		row := normalizeStringRow(rawRow)
		if truthy(row["invalidated"]) || !strings.EqualFold(row["member_role"], "PRIMARY") {
			continue
		}
		setID := row["clusterset_id"]
		counts[setID]++
		primaryIDs[setID] = row["cluster_id"]
	}
	for setID, count := range counts {
		if count != 1 {
			delete(primaryIDs, setID)
		}
	}
	return primaryIDs
}

func metadataColumns(rows []map[string]string) map[string]map[string]struct{} {
	columns := make(map[string]map[string]struct{})
	for _, rawRow := range rows {
		row := normalizeStringRow(rawRow)
		table := row["table_name"]
		column := row["column_name"]
		if table == "" || column == "" {
			continue
		}
		if columns[table] == nil {
			columns[table] = make(map[string]struct{})
		}
		columns[table][column] = struct{}{}
	}
	return columns
}

func requireMetadataColumns(columns map[string]map[string]struct{}, table string, required ...string) error {
	available, ok := columns[table]
	if !ok {
		return fmt.Errorf("unsupported InnoDB metadata schema shape: missing %s", table)
	}
	for _, column := range required {
		if _, ok := available[column]; !ok {
			return fmt.Errorf("unsupported InnoDB metadata schema shape: %s is missing %s", table, column)
		}
	}
	return nil
}

func presentMetadataColumns(columns map[string]map[string]struct{}, table string, allowed []string) []string {
	present := make([]string, 0, len(allowed))
	for _, column := range allowed {
		if _, ok := columns[table][column]; ok {
			present = append(present, column)
		}
	}
	return present
}

func metadataTableExists(columns map[string]map[string]struct{}, table string) bool {
	_, ok := columns[table]
	return ok
}

func metadataSelect(table string, columns []string) string {
	return metadataSelectWhere(table, columns, "")
}

func metadataSelectWhere(table string, columns []string, where string) string {
	quoted := make([]string, len(columns))
	for index, column := range columns {
		quoted[index] = "`" + column + "`"
	}
	query := fmt.Sprintf("SELECT %s FROM `%s`.`%s`", strings.Join(quoted, ", "), innodbMetadataSchema, table)
	if where != "" {
		query += " WHERE " + where
	}
	return query
}

func normalizeStringRow(row map[string]string) map[string]string {
	normalized := make(map[string]string, len(row))
	for key, value := range row {
		normalized[strings.ToLower(key)] = strings.TrimSpace(value)
	}
	return normalized
}

func metadataJSONField(value string, key string) string {
	if value == "" {
		return ""
	}
	fields := map[string]any{}
	if err := json.Unmarshal([]byte(value), &fields); err != nil {
		return ""
	}
	return stringValue(fields[key])
}

func clusterSetRole(value string) string {
	switch strings.ToUpper(value) {
	case "PRIMARY":
		return "primary_cluster"
	case "REPLICA":
		return "replica_cluster"
	default:
		return ""
	}
}

func topologyEndpoint(base models.MetricGroupValue, fallback string) (string, int64, bool) {
	host := topologyString(base, "MemberHost")
	port, portOK := optionalInt64(topologyString(base, "MemberPort"))
	if host != "" {
		return host, port, portOK
	}
	fallbackHost, fallbackPort := splitMetadataEndpoint(fallback)
	parsedPort, parsedPortOK := optionalInt64(fallbackPort)
	return fallbackHost, parsedPort, parsedPortOK
}

func endpointMatches(address string, host string, port string) bool {
	addressHost, addressPort := splitMetadataEndpoint(address)
	return strings.EqualFold(addressHost, host) && (port == "" || addressPort == "" || port == addressPort)
}

func splitMetadataEndpoint(value string) (string, string) {
	value = strings.TrimSpace(value)
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		value = parsed.Host
	}
	if host, port, err := net.SplitHostPort(value); err == nil {
		return strings.Trim(host, "[]"), port
	}
	if index := strings.LastIndex(value, ":"); index > 0 && !strings.Contains(value[index+1:], ":") {
		if _, err := strconv.ParseUint(value[index+1:], 10, 16); err == nil {
			return strings.Trim(value[:index], "[]"), value[index+1:]
		}
	}
	return strings.Trim(value, "[]"), ""
}

func sortInnoDBMetadata(metadata *InnoDBMetadata) {
	sort.Slice(metadata.Clusters, func(left int, right int) bool {
		return metadata.Clusters[left].ID < metadata.Clusters[right].ID
	})
	sort.Slice(metadata.Instances, func(left int, right int) bool {
		if metadata.Instances[left].ClusterID != metadata.Instances[right].ClusterID {
			return metadata.Instances[left].ClusterID < metadata.Instances[right].ClusterID
		}
		return metadata.Instances[left].ID < metadata.Instances[right].ID
	})
	sort.Slice(metadata.ClusterSets, func(left int, right int) bool {
		if metadata.ClusterSets[left].ID != metadata.ClusterSets[right].ID {
			return metadata.ClusterSets[left].ID < metadata.ClusterSets[right].ID
		}
		return metadata.ClusterSets[left].ClusterID < metadata.ClusterSets[right].ClusterID
	})
}
