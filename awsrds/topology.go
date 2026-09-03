package awsrds

import (
	"sort"
	"strings"

	"github.com/Releem/mysqlconfigurer/models"
	agenttopology "github.com/Releem/mysqlconfigurer/topology"
	logging "github.com/google/logger"
)

// MetadataSnapshot returns the most recently discovered metadata and whether it
// is complete for the current report.
type MetadataSnapshot func() (Metadata, bool)

type reportTopologyMetadata struct {
	metadata Metadata
	complete bool
}

type topologyRelationsGatherer struct{}

var _ models.MetricsGatherer = (*topologyRelationsGatherer)(nil)

// NewTopologyRelationsGatherer merges report-local provider relations after
// native database topology. Discovery failures are logged by the enhanced
// metrics gatherer.
func NewTopologyRelationsGatherer(_ logging.Logger) models.MetricsGatherer {
	return &topologyRelationsGatherer{}
}

func (g *topologyRelationsGatherer) GetMetrics(metrics *models.Metrics) error {
	if metrics == nil || metrics.DB.Topology == nil {
		return nil
	}

	metadata, complete, ok := reportMetadata(metrics)
	if !ok {
		return nil
	}
	mergeTopologyRelations(metrics.DB.Topology, BuildTopologyRelations(metadata), complete)
	return nil
}

// AttachReportMetadata stores an immutable provider snapshot on one collection
// context for the later topology relation gatherer.
func AttachReportMetadata(metrics *models.Metrics, metadata Metadata, complete bool) {
	if metrics == nil {
		return
	}
	metrics.Internal.AWSRDS = reportTopologyMetadata{
		metadata: metadata.Clone(),
		complete: complete,
	}
}

func reportMetadata(metrics *models.Metrics) (Metadata, bool, bool) {
	if metrics == nil {
		return Metadata{}, false, false
	}
	snapshot, ok := metrics.Internal.AWSRDS.(reportTopologyMetadata)
	if !ok {
		return Metadata{}, false, false
	}
	return snapshot.metadata.Clone(), snapshot.complete, true
}

// Clone returns metadata whose slice storage is independent of the receiver.
func (m Metadata) Clone() Metadata {
	cloned := m
	cloned.ClusterMembers = append([]ClusterMember(nil), m.ClusterMembers...)
	cloned.GlobalClusterMembers = append([]GlobalClusterMember(nil), m.GlobalClusterMembers...)
	cloned.ReadReplicaDBInstanceIdentifiers = append([]string(nil), m.ReadReplicaDBInstanceIdentifiers...)
	cloned.ReadReplicas = append([]RelatedDBInstance(nil), m.ReadReplicas...)
	return cloned
}

// BuildTopologyRelations builds provider-authoritative memberships for the
// reporting DB instance. Full discovery validates the identities consumed here.
func BuildTopologyRelations(metadata Metadata) []models.MetricGroupValue {
	relations := make([]models.MetricGroupValue, 0, 3)
	if metadata.IsAurora() {
		if relation, ok := buildAuroraClusterRelation(metadata); ok {
			relations = append(relations, relation)
		}
		if relation, ok := buildAuroraGlobalRelation(metadata); ok {
			relations = append(relations, relation)
		}
		return normalizeRelations(relations)
	}

	if relation, ok := buildRDSReplicaUpstreamRelation(metadata); ok {
		relations = append(relations, relation)
	}
	if relation, ok := buildRDSReplicaSourceRelation(metadata); ok {
		relations = append(relations, relation)
	}
	if relation, ok := buildRDSMultiAZRelation(metadata); ok {
		relations = append(relations, relation)
	}
	return normalizeRelations(relations)
}

func buildAuroraClusterRelation(metadata Metadata) (models.MetricGroupValue, bool) {
	groupKey := providerGroupKey("aurora", metadata.DBClusterResourceID)
	local, localOK := clusterMember(metadata.ClusterMembers, metadata.DBInstanceIdentifier)
	writer, writerOK := clusterWriter(metadata.ClusterMembers)
	memberKey := providerMemberKey(metadata.DBInstanceARN, metadata.DBInstanceResourceID)
	primaryMemberKey := providerMemberKey(writer.DBInstanceARN, writer.DBInstanceResourceID)
	if groupKey == "" || !localOK || !writerOK || memberKey == "" || primaryMemberKey == "" {
		return nil, false
	}

	available := instanceAvailable(local.InstanceStatus)
	parentGroupKey := nullableProviderGroupKey("aurora-global", metadata.GlobalClusterResourceID)
	relation := baseProviderRelation(
		"aurora_cluster",
		groupKey,
		memberKey,
		local.Endpoint,
		local.EndpointPort,
		primaryMemberKey,
		writer.Endpoint,
		writer.EndpointPort,
	)
	relation["Role"] = "replica"
	if local.IsClusterWriter {
		relation["Role"] = "primary"
	}
	relation["ParentGroupKey"] = parentGroupKey
	relation["IsWriter"] = available && local.IsClusterWriter
	relation["IsReader"] = available
	relation["ReadOnly"] = !local.IsClusterWriter
	relation["ReplicationState"] = instanceReplicationState(local.InstanceStatus)
	facts := models.MetricGroupValue{
		"DBClusterIdentifier":           metadata.DBClusterIdentifier,
		"DBClusterARN":                  metadata.DBClusterARN,
		"DBClusterResourceID":           metadata.DBClusterResourceID,
		"DBClusterParameterGroup":       metadata.DBClusterParameterGroup,
		"DBClusterParameterGroupStatus": local.DBClusterParameterGroupStatus,
		"ClusterEndpoint":               metadata.ClusterEndpoint,
		"ClusterReaderEndpoint":         metadata.ClusterReaderEndpoint,
		"Engine":                        metadata.Engine,
		"EngineMode":                    metadata.EngineMode,
		"InstanceClass":                 local.DBInstanceClass,
		"InstanceStatus":                local.InstanceStatus,
		"PromotionTier":                 int64(local.PromotionTier),
		"IsServerlessV2":                local.IsServerlessV2,
	}
	if metadata.HasServerlessV2ScalingConfiguration {
		facts["HasServerlessV2ScalingConfiguration"] = true
		facts["ServerlessV2MinCapacity"] = metadata.ServerlessV2ScalingConfiguration.MinCapacity
		facts["ServerlessV2MaxCapacity"] = metadata.ServerlessV2ScalingConfiguration.MaxCapacity
		facts["ServerlessV2SecondsUntilAutoPause"] = int64(metadata.ServerlessV2ScalingConfiguration.SecondsUntilAutoPause)
	}
	relation["Facts"] = facts
	return relation, true
}

func buildAuroraGlobalRelation(metadata Metadata) (models.MetricGroupValue, bool) {
	groupKey := providerGroupKey("aurora-global", metadata.GlobalClusterResourceID)
	local, localOK := globalClusterMember(metadata.GlobalClusterMembers, metadata.DBClusterARN)
	primary, primaryOK := globalClusterWriter(metadata.GlobalClusterMembers)
	memberKey := providerMemberKey(metadata.DBInstanceARN, metadata.DBInstanceResourceID)
	if groupKey == "" || !localOK || !primaryOK || memberKey == "" {
		return nil, false
	}

	state := globalReplicationState(local.SynchronizationStatus)
	available := instanceAvailable(metadata.InstanceStatus) && readableReplicationState(state)
	isPrimaryCluster := local.IsWriter
	relation := baseProviderRelation(
		"aurora_global_database",
		groupKey,
		memberKey,
		metadata.Endpoint,
		metadata.EndpointPort,
		"",
		"",
		0,
	)
	relation["Role"] = "replica_cluster_member"
	if isPrimaryCluster {
		relation["Role"] = "primary_cluster_member"
	} else {
		relation["ParentGroupKey"] = nullableString(primary.DBClusterARN)
	}
	relation["IsWriter"] = available && isPrimaryCluster && metadata.IsClusterWriter
	relation["IsReader"] = available
	relation["ReadOnly"] = !(isPrimaryCluster && metadata.IsClusterWriter)
	relation["ReplicationState"] = state
	relation["Facts"] = models.MetricGroupValue{
		"GlobalClusterIdentifier":     metadata.GlobalClusterIdentifier,
		"GlobalClusterARN":            metadata.GlobalClusterARN,
		"GlobalClusterResourceID":     metadata.GlobalClusterResourceID,
		"RegionalDBClusterIdentifier": local.DBClusterIdentifier,
		"RegionalDBClusterARN":        local.DBClusterARN,
		"Region":                      local.Region,
		"PrimaryDBClusterARN":         primary.DBClusterARN,
		"PrimaryRegion":               primary.Region,
		"SynchronizationStatus":       local.SynchronizationStatus,
		"GlobalWriteForwardingStatus": local.GlobalWriteForwardingStatus,
		"InstanceStatus":              metadata.InstanceStatus,
	}
	return relation, true
}

func buildRDSReplicaUpstreamRelation(metadata Metadata) (models.MetricGroupValue, bool) {
	if !metadata.HasReadReplicaSource {
		return nil, false
	}
	source := metadata.ReadReplicaSource
	groupKey := providerGroupKey("rds-replica", source.DBInstanceResourceID)
	memberKey := providerMemberKey(metadata.DBInstanceARN, metadata.DBInstanceResourceID)
	primaryMemberKey := providerMemberKey(source.DBInstanceARN, source.DBInstanceResourceID)
	if groupKey == "" || memberKey == "" || primaryMemberKey == "" {
		return nil, false
	}

	relation := baseProviderRelation(
		"rds_read_replica",
		groupKey,
		memberKey,
		metadata.Endpoint,
		metadata.EndpointPort,
		primaryMemberKey,
		source.Endpoint,
		source.EndpointPort,
	)
	relation["Role"] = "replica"
	relation["IsReader"] = instanceAvailable(metadata.InstanceStatus)
	relation["ReadOnly"] = true
	relation["Facts"] = rdsReplicaFacts(metadata, "upstream")
	return relation, true
}

func buildRDSReplicaSourceRelation(metadata Metadata) (models.MetricGroupValue, bool) {
	if len(metadata.ReadReplicas) == 0 {
		return nil, false
	}
	groupKey := providerGroupKey("rds-replica", metadata.DBInstanceResourceID)
	memberKey := providerMemberKey(metadata.DBInstanceARN, metadata.DBInstanceResourceID)
	if groupKey == "" || memberKey == "" {
		return nil, false
	}

	available := instanceAvailable(metadata.InstanceStatus)
	relation := baseProviderRelation(
		"rds_read_replica",
		groupKey,
		memberKey,
		metadata.Endpoint,
		metadata.EndpointPort,
		memberKey,
		metadata.Endpoint,
		metadata.EndpointPort,
	)
	relation["Role"] = "primary"
	relation["IsWriter"] = available && !metadata.HasReadReplicaSource
	relation["IsReader"] = available
	relation["ReadOnly"] = metadata.HasReadReplicaSource
	relation["Facts"] = rdsReplicaFacts(metadata, "downstream")
	return relation, true
}

func buildRDSMultiAZRelation(metadata Metadata) (models.MetricGroupValue, bool) {
	if !metadata.MultiAZ {
		return nil, false
	}
	groupKey := providerGroupKey("rds-multi-az", metadata.DBInstanceResourceID)
	memberKey := providerMemberKey(metadata.DBInstanceARN, metadata.DBInstanceResourceID)
	if groupKey == "" || memberKey == "" {
		return nil, false
	}

	available := instanceAvailable(metadata.InstanceStatus)
	relation := baseProviderRelation(
		"rds_multi_az",
		groupKey,
		memberKey,
		metadata.Endpoint,
		metadata.EndpointPort,
		memberKey,
		metadata.Endpoint,
		metadata.EndpointPort,
	)
	relation["Role"] = "primary"
	relation["IsWriter"] = available && !metadata.HasReadReplicaSource
	relation["IsReader"] = available
	relation["ReadOnly"] = metadata.HasReadReplicaSource
	relation["Facts"] = models.MetricGroupValue{
		"ManagedStandby":       true,
		"DBInstanceIdentifier": metadata.DBInstanceIdentifier,
		"DBInstanceARN":        metadata.DBInstanceARN,
		"DBInstanceResourceID": metadata.DBInstanceResourceID,
		"Engine":               metadata.Engine,
		"InstanceStatus":       metadata.InstanceStatus,
		"Region":               metadata.Region,
	}
	return relation, true
}

func baseProviderRelation(typeName, groupKey, memberKey, memberHost string, memberPort int32,
	primaryMemberKey, primaryHost string, primaryPort int32) models.MetricGroupValue {
	return models.MetricGroupValue{
		"Type":                  typeName,
		"Role":                  "",
		"GroupKey":              groupKey,
		"MemberKey":             memberKey,
		"MemberHost":            nullableString(memberHost),
		"MemberPort":            nullablePort(memberPort),
		"PrimaryMemberKey":      nullableString(primaryMemberKey),
		"PrimaryHost":           nullableString(primaryHost),
		"PrimaryPort":           nullablePort(primaryPort),
		"ParentGroupKey":        nil,
		"IsWriter":              false,
		"IsReader":              false,
		"ReadOnly":              false,
		"SuperReadOnly":         false,
		"ReplicationLagSeconds": nil,
		"ReplicationState":      "unknown",
		"Facts":                 models.MetricGroupValue{},
	}
}

func rdsReplicaFacts(metadata Metadata, direction string) models.MetricGroupValue {
	facts := models.MetricGroupValue{
		"Direction":                        direction,
		"DBInstanceIdentifier":             metadata.DBInstanceIdentifier,
		"DBInstanceARN":                    metadata.DBInstanceARN,
		"DBInstanceResourceID":             metadata.DBInstanceResourceID,
		"Engine":                           metadata.Engine,
		"InstanceStatus":                   metadata.InstanceStatus,
		"Region":                           metadata.Region,
		"ReadReplicaDBInstanceIdentifiers": append([]string(nil), metadata.ReadReplicaDBInstanceIdentifiers...),
	}
	if metadata.HasReadReplicaSource {
		facts["SourceDBInstanceIdentifier"] = metadata.ReadReplicaSource.DBInstanceIdentifier
		facts["SourceDBInstanceARN"] = metadata.ReadReplicaSource.DBInstanceARN
		facts["SourceDBInstanceResourceID"] = metadata.ReadReplicaSource.DBInstanceResourceID
	}
	return facts
}

func clusterMember(members []ClusterMember, identifier string) (ClusterMember, bool) {
	for _, member := range members {
		if strings.EqualFold(member.DBInstanceIdentifier, identifier) {
			return member, true
		}
	}
	return ClusterMember{}, false
}

func clusterWriter(members []ClusterMember) (ClusterMember, bool) {
	var writer ClusterMember
	found := false
	for _, member := range members {
		if !member.IsClusterWriter {
			continue
		}
		if found {
			return ClusterMember{}, false
		}
		writer = member
		found = true
	}
	return writer, found
}

func globalClusterMember(members []GlobalClusterMember, clusterARN string) (GlobalClusterMember, bool) {
	for _, member := range members {
		if strings.EqualFold(member.DBClusterARN, clusterARN) {
			return member, true
		}
	}
	return GlobalClusterMember{}, false
}

func globalClusterWriter(members []GlobalClusterMember) (GlobalClusterMember, bool) {
	var writer GlobalClusterMember
	found := false
	for _, member := range members {
		if !member.IsWriter {
			continue
		}
		if found {
			return GlobalClusterMember{}, false
		}
		writer = member
		found = true
	}
	return writer, found
}

func providerGroupKey(namespace, resourceID string) string {
	if strings.TrimSpace(resourceID) == "" {
		return ""
	}
	return agenttopology.CompositeKey(namespace, []string{resourceID})
}

func nullableProviderGroupKey(namespace, resourceID string) interface{} {
	return nullableString(providerGroupKey(namespace, resourceID))
}

func providerMemberKey(instanceARN, resourceID string) string {
	identity := strings.TrimSpace(instanceARN)
	if identity == "" {
		identity = strings.TrimSpace(resourceID)
	}
	if identity == "" {
		return ""
	}
	if len(identity) <= agenttopology.MaxKeyLength {
		return identity
	}
	return agenttopology.CompositeKey("rds-instance", []string{identity})
}

func nullableString(value string) interface{} {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullablePort(port int32) interface{} {
	if port <= 0 {
		return nil
	}
	return int64(port)
}

func instanceAvailable(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "available")
}

func instanceReplicationState(status string) string {
	if instanceAvailable(status) {
		return "healthy"
	}
	return "unknown"
}

func globalReplicationState(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "connected":
		return "healthy"
	case "pending-resync":
		return "lagging"
	default:
		return "unknown"
	}
}

func readableReplicationState(state string) bool {
	return state == "healthy" || state == "lagging"
}

func mergeTopologyRelations(topology models.MetricGroupValue, providerRelations []models.MetricGroupValue, providerComplete bool) {
	_, nativeComplete := topology["Relations"]
	facts := topologyFacts(topology)
	existing := relationSlice(facts["Relations"])
	if len(existing) == 0 {
		existing = relationSlice(topology["Relations"])
	}
	combined := make([]models.MetricGroupValue, 0, len(existing)+len(providerRelations))
	combined = append(combined, existing...)
	combined = append(combined, providerRelations...)
	combined = normalizeRelations(combined)
	facts["Relations"] = combined
	topology["Facts"] = facts

	if providerComplete && nativeComplete {
		topology["Relations"] = combined
		return
	}
	delete(topology, "Relations")
}

func topologyFacts(topology models.MetricGroupValue) models.MetricGroupValue {
	switch facts := topology["Facts"].(type) {
	case models.MetricGroupValue:
		return cloneMetricGroup(facts)
	case map[string]interface{}:
		return cloneMetricGroup(models.MetricGroupValue(facts))
	default:
		return models.MetricGroupValue{}
	}
}

func relationSlice(value interface{}) []models.MetricGroupValue {
	switch relations := value.(type) {
	case []models.MetricGroupValue:
		cloned := make([]models.MetricGroupValue, 0, len(relations))
		for _, relation := range relations {
			cloned = append(cloned, cloneMetricGroup(relation))
		}
		return cloned
	case []map[string]interface{}:
		cloned := make([]models.MetricGroupValue, 0, len(relations))
		for _, relation := range relations {
			cloned = append(cloned, cloneMetricGroup(models.MetricGroupValue(relation)))
		}
		return cloned
	default:
		return nil
	}
}

func normalizeRelations(relations []models.MetricGroupValue) []models.MetricGroupValue {
	type relationIdentity struct {
		typeName  string
		groupKey  string
		memberKey string
	}

	normalized := make([]models.MetricGroupValue, 0, len(relations))
	seen := make(map[relationIdentity]struct{}, len(relations))
	for _, relation := range relations {
		cloned := cloneMetricGroup(relation)
		typeName, typeOK := cloned["Type"].(string)
		groupKey, groupOK := cloned["GroupKey"].(string)
		memberKey, memberOK := cloned["MemberKey"].(string)
		identity := relationIdentity{
			typeName:  strings.TrimSpace(typeName),
			groupKey:  strings.TrimSpace(groupKey),
			memberKey: strings.TrimSpace(memberKey),
		}
		if !typeOK || !groupOK || !memberOK || identity.typeName == "" || identity.groupKey == "" || identity.memberKey == "" {
			continue
		}
		if _, exists := seen[identity]; exists {
			continue
		}
		seen[identity] = struct{}{}
		cloned["Type"] = identity.typeName
		cloned["GroupKey"] = identity.groupKey
		cloned["MemberKey"] = identity.memberKey
		normalized = append(normalized, cloned)
	}

	sort.Slice(normalized, func(left, right int) bool {
		leftType := normalized[left]["Type"].(string)
		rightType := normalized[right]["Type"].(string)
		if leftType != rightType {
			return leftType < rightType
		}
		leftGroup := normalized[left]["GroupKey"].(string)
		rightGroup := normalized[right]["GroupKey"].(string)
		if leftGroup != rightGroup {
			return leftGroup < rightGroup
		}
		return normalized[left]["MemberKey"].(string) < normalized[right]["MemberKey"].(string)
	})
	return normalized
}

func cloneMetricGroup(input models.MetricGroupValue) models.MetricGroupValue {
	output := make(models.MetricGroupValue, len(input))
	for key, value := range input {
		output[key] = cloneMetricValue(value)
	}
	return output
}

func cloneMetricValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case models.MetricGroupValue:
		return cloneMetricGroup(typed)
	case map[string]interface{}:
		return cloneMetricGroup(models.MetricGroupValue(typed))
	case []models.MetricGroupValue:
		return relationSlice(typed)
	case []map[string]interface{}:
		return relationSlice(typed)
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = cloneMetricValue(item)
		}
		return cloned
	default:
		return value
	}
}
