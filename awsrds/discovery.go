package awsrds

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsarn "github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
)

// ClusterMember is one addressable DB instance declared by an RDS DB cluster.
type ClusterMember struct {
	DBInstanceIdentifier          string
	DBInstanceARN                 string
	DBInstanceResourceID          string
	DBInstanceClass               string
	Endpoint                      string
	EndpointPort                  int32
	InstanceStatus                string
	IsClusterWriter               bool
	IsServerlessV2                bool
	PromotionTier                 int32
	DBClusterParameterGroupStatus string
}

// GlobalClusterMember is one regional DB cluster declared by an Aurora Global
// Database. Region is parsed from DBClusterARN.
type GlobalClusterMember struct {
	DBClusterIdentifier         string
	DBClusterARN                string
	Region                      string
	IsWriter                    bool
	SynchronizationStatus       string
	GlobalWriteForwardingStatus string
}

// RelatedDBInstance is an explicitly referenced ordinary RDS read-replica
// source or child. It contains only typed fields needed to build relations.
type RelatedDBInstance struct {
	DBInstanceIdentifier string
	DBInstanceARN        string
	DBInstanceResourceID string
	Endpoint             string
	EndpointPort         int32
	Engine               string
	InstanceStatus       string
	Partition            string
	Region               string
	MultiAZ              bool
}

// ServerlessV2ScalingConfiguration records the configured Aurora capacity
// bounds. HasServerlessV2ScalingConfiguration distinguishes absence from zero,
// which is a valid minimum capacity.
type ServerlessV2ScalingConfiguration struct {
	MinCapacity           float64
	MaxCapacity           float64
	SecondsUntilAutoPause int32
}

// Metadata contains the authoritative instance, cluster, global-cluster, and
// ordinary RDS replication topology attached to one RDS DB instance.
type Metadata struct {
	Partition string
	Region    string

	DBInstanceIdentifier                  string
	DBInstanceARN                         string
	DBInstanceResourceID                  string
	DBInstanceClass                       string
	Endpoint                              string
	EndpointPort                          int32
	Engine                                string
	EngineMode                            string
	DBParameterGroup                      string
	DBParameterGroupStatus                string
	DBClusterIdentifier                   string
	DBClusterARN                          string
	DBClusterResourceID                   string
	DBClusterParameterGroup               string
	DBClusterParameterGroupStatus         string
	ClusterEndpoint                       string
	ClusterReaderEndpoint                 string
	ClusterMembers                        []ClusterMember
	IsClusterWriter                       bool
	PromotionTier                         int32
	IsServerlessV2                        bool
	HasServerlessV2ScalingConfiguration   bool
	ServerlessV2ScalingConfiguration      ServerlessV2ScalingConfiguration
	InstanceStatus                        string
	GlobalClusterIdentifier               string
	GlobalClusterARN                      string
	GlobalClusterResourceID               string
	GlobalClusterPrimaryDBClusterARN      string
	GlobalClusterPrimaryRegion            string
	GlobalClusterMembers                  []GlobalClusterMember
	ReadReplicaSourceDBInstanceIdentifier string
	ReadReplicaDBInstanceIdentifiers      []string
	HasReadReplicaSource                  bool
	ReadReplicaSource                     RelatedDBInstance
	ReadReplicas                          []RelatedDBInstance
	MultiAZ                               bool
}

// DiscoverInstance resolves one configured DB instance and every topology
// object explicitly referenced by the RDS APIs.
func DiscoverInstance(ctx context.Context, client Client, identifier string) (Metadata, error) {
	if client == nil {
		return Metadata{}, fmt.Errorf("discover DB instance %q: RDS client is nil", identifier)
	}

	instance, err := describeDBInstance(ctx, client, identifier)
	if err != nil {
		return Metadata{}, err
	}
	metadata, err := metadataFromInstance(instance)
	if err != nil {
		return Metadata{}, err
	}
	if metadata.IsAurora() {
		return discoverAuroraInstance(ctx, client, instance, metadata)
	}
	return discoverRDSReadReplicas(ctx, client, instance, metadata)
}

func discoverAuroraInstance(ctx context.Context, client Client, target types.DBInstance, metadata Metadata) (Metadata, error) {
	if metadata.DBClusterIdentifier == "" {
		return Metadata{}, fmt.Errorf("Aurora DB instance %q has no DB cluster identifier", metadata.DBInstanceIdentifier)
	}

	cluster, err := describeDBCluster(ctx, client, metadata.DBClusterIdentifier)
	if err != nil {
		return Metadata{}, err
	}
	metadata.DBClusterARN = aws.ToString(cluster.DBClusterArn)
	metadata.DBClusterResourceID = aws.ToString(cluster.DbClusterResourceId)
	metadata.EngineMode = aws.ToString(cluster.EngineMode)
	metadata.DBClusterParameterGroup = aws.ToString(cluster.DBClusterParameterGroup)
	metadata.ClusterEndpoint = aws.ToString(cluster.Endpoint)
	metadata.ClusterReaderEndpoint = aws.ToString(cluster.ReaderEndpoint)
	if err := mergeMetadataLocation(&metadata, metadata.DBClusterARN, "cluster", metadata.DBClusterIdentifier); err != nil {
		return Metadata{}, fmt.Errorf("DB cluster %q: %w", metadata.DBClusterIdentifier, err)
	}

	matchingMembers := membersForInstance(cluster.DBClusterMembers, metadata.DBInstanceIdentifier)
	switch len(matchingMembers) {
	case 0:
		return Metadata{}, fmt.Errorf("DB instance %q is not a member of DB cluster %q", metadata.DBInstanceIdentifier, metadata.DBClusterIdentifier)
	case 1:
		// Continue below.
	default:
		return Metadata{}, fmt.Errorf("DB instance %q has %d memberships in DB cluster %q", metadata.DBInstanceIdentifier, len(matchingMembers), metadata.DBClusterIdentifier)
	}

	seenMembers := make(map[string]struct{}, len(cluster.DBClusterMembers))
	for _, declaredMember := range cluster.DBClusterMembers {
		memberID := aws.ToString(declaredMember.DBInstanceIdentifier)
		if memberID == "" {
			return Metadata{}, fmt.Errorf("DB cluster %q declares a member without a DB instance identifier", metadata.DBClusterIdentifier)
		}
		if _, exists := seenMembers[memberID]; exists {
			return Metadata{}, fmt.Errorf("DB cluster %q declares DB instance %q more than once", metadata.DBClusterIdentifier, memberID)
		}
		seenMembers[memberID] = struct{}{}

		memberInstance := target
		if memberID != metadata.DBInstanceIdentifier {
			memberInstance, err = describeDBInstance(ctx, client, memberID)
			if err != nil {
				return Metadata{}, fmt.Errorf("resolve member of DB cluster %q: %w", metadata.DBClusterIdentifier, err)
			}
		}
		if memberClusterID := aws.ToString(memberInstance.DBClusterIdentifier); memberClusterID != metadata.DBClusterIdentifier {
			return Metadata{}, fmt.Errorf("DB cluster %q declares DB instance %q, but the instance declares cluster %q", metadata.DBClusterIdentifier, memberID, memberClusterID)
		}

		memberMetadata, err := metadataFromInstance(memberInstance)
		if err != nil {
			return Metadata{}, fmt.Errorf("resolve member of DB cluster %q: %w", metadata.DBClusterIdentifier, err)
		}
		if err := validateMemberLocation(metadata, memberMetadata, memberID); err != nil {
			return Metadata{}, err
		}
		metadata.ClusterMembers = append(metadata.ClusterMembers, ClusterMember{
			DBInstanceIdentifier:          memberMetadata.DBInstanceIdentifier,
			DBInstanceARN:                 memberMetadata.DBInstanceARN,
			DBInstanceResourceID:          memberMetadata.DBInstanceResourceID,
			DBInstanceClass:               memberMetadata.DBInstanceClass,
			Endpoint:                      memberMetadata.Endpoint,
			EndpointPort:                  memberMetadata.EndpointPort,
			InstanceStatus:                memberMetadata.InstanceStatus,
			IsClusterWriter:               aws.ToBool(declaredMember.IsClusterWriter),
			IsServerlessV2:                memberMetadata.IsServerlessV2,
			PromotionTier:                 aws.ToInt32(declaredMember.PromotionTier),
			DBClusterParameterGroupStatus: aws.ToString(declaredMember.DBClusterParameterGroupStatus),
		})
	}

	metadata.DBClusterParameterGroupStatus = aws.ToString(matchingMembers[0].DBClusterParameterGroupStatus)
	metadata.IsClusterWriter = aws.ToBool(matchingMembers[0].IsClusterWriter)
	metadata.PromotionTier = aws.ToInt32(matchingMembers[0].PromotionTier)
	if cluster.ServerlessV2ScalingConfiguration != nil {
		metadata.HasServerlessV2ScalingConfiguration = true
		metadata.ServerlessV2ScalingConfiguration = ServerlessV2ScalingConfiguration{
			MinCapacity:           aws.ToFloat64(cluster.ServerlessV2ScalingConfiguration.MinCapacity),
			MaxCapacity:           aws.ToFloat64(cluster.ServerlessV2ScalingConfiguration.MaxCapacity),
			SecondsUntilAutoPause: aws.ToInt32(cluster.ServerlessV2ScalingConfiguration.SecondsUntilAutoPause),
		}
	}

	if globalID := aws.ToString(cluster.GlobalClusterIdentifier); globalID != "" {
		if err := discoverGlobalCluster(ctx, client, cluster, &metadata, globalID); err != nil {
			return Metadata{}, err
		}
	}
	return metadata, nil
}

func discoverGlobalCluster(ctx context.Context, client Client, localCluster types.DBCluster, metadata *Metadata, globalID string) error {
	globalCluster, err := describeGlobalCluster(ctx, client, globalID)
	if err != nil {
		return err
	}
	metadata.GlobalClusterIdentifier = globalID
	metadata.GlobalClusterARN = aws.ToString(globalCluster.GlobalClusterArn)
	metadata.GlobalClusterResourceID = aws.ToString(globalCluster.GlobalClusterResourceId)
	if err := mergeMetadataPartition(metadata, metadata.GlobalClusterARN, "global-cluster", globalID); err != nil {
		return fmt.Errorf("global cluster %q: %w", globalID, err)
	}

	localClusterARN := aws.ToString(localCluster.DBClusterArn)
	if localClusterARN == "" {
		return fmt.Errorf("DB cluster %q belongs to global cluster %q but has no ARN", metadata.DBClusterIdentifier, globalID)
	}

	seenMembers := make(map[string]GlobalClusterMember, len(globalCluster.GlobalClusterMembers))
	localMemberships := 0
	primaryMembers := 0
	for _, declaredMember := range globalCluster.GlobalClusterMembers {
		clusterARN := aws.ToString(declaredMember.DBClusterArn)
		identity, err := parseRDSARN(clusterARN, "cluster")
		if err != nil {
			return fmt.Errorf("global cluster %q member: %w", globalID, err)
		}
		if metadata.Partition != "" && identity.Partition != metadata.Partition {
			return fmt.Errorf("global cluster %q member %q uses partition %q, want %q", globalID, identity.Identifier, identity.Partition, metadata.Partition)
		}
		if _, exists := seenMembers[clusterARN]; exists {
			return fmt.Errorf("global cluster %q declares DB cluster ARN %q more than once", globalID, clusterARN)
		}
		member := GlobalClusterMember{
			DBClusterIdentifier:         identity.Identifier,
			DBClusterARN:                clusterARN,
			Region:                      identity.Region,
			IsWriter:                    aws.ToBool(declaredMember.IsWriter),
			SynchronizationStatus:       string(declaredMember.SynchronizationStatus),
			GlobalWriteForwardingStatus: string(declaredMember.GlobalWriteForwardingStatus),
		}
		seenMembers[clusterARN] = member
		metadata.GlobalClusterMembers = append(metadata.GlobalClusterMembers, member)
		if clusterARN == localClusterARN {
			localMemberships++
			if metadata.Region != "" && identity.Region != metadata.Region {
				return fmt.Errorf("DB cluster %q region %q conflicts with global cluster membership region %q", metadata.DBClusterIdentifier, metadata.Region, identity.Region)
			}
		}
		if member.IsWriter {
			primaryMembers++
			metadata.GlobalClusterPrimaryDBClusterARN = member.DBClusterARN
			metadata.GlobalClusterPrimaryRegion = member.Region
		}
	}

	if localMemberships != 1 {
		return fmt.Errorf("DB cluster %q has %d memberships in global cluster %q", metadata.DBClusterIdentifier, localMemberships, globalID)
	}
	if primaryMembers != 1 {
		return fmt.Errorf("global cluster %q has %d primary DB clusters", globalID, primaryMembers)
	}
	if metadata.GlobalClusterPrimaryRegion == "" {
		return fmt.Errorf("global cluster %q primary DB cluster ARN has no region", globalID)
	}

	for _, declaredMember := range globalCluster.GlobalClusterMembers {
		for _, readerARN := range declaredMember.Readers {
			reader, exists := seenMembers[readerARN]
			if !exists {
				return fmt.Errorf("global cluster %q references reader DB cluster ARN %q without a membership", globalID, readerARN)
			}
			if reader.IsWriter {
				return fmt.Errorf("global cluster %q references primary DB cluster %q as a reader", globalID, reader.DBClusterIdentifier)
			}
		}
	}
	return nil
}

func discoverRDSReadReplicas(ctx context.Context, client Client, target types.DBInstance, metadata Metadata) (Metadata, error) {
	metadata.ReadReplicaSourceDBInstanceIdentifier = aws.ToString(target.ReadReplicaSourceDBInstanceIdentifier)
	metadata.ReadReplicaDBInstanceIdentifiers = append([]string(nil), target.ReadReplicaDBInstanceIdentifiers...)

	if sourceID := metadata.ReadReplicaSourceDBInstanceIdentifier; sourceID != "" {
		if sourceID == metadata.DBInstanceIdentifier {
			return Metadata{}, fmt.Errorf("DB instance %q declares itself as its read-replica source", metadata.DBInstanceIdentifier)
		}
		source, err := describeDBInstance(ctx, client, sourceID)
		if err != nil {
			return Metadata{}, fmt.Errorf("resolve read-replica source for DB instance %q: %w", metadata.DBInstanceIdentifier, err)
		}
		if !containsString(source.ReadReplicaDBInstanceIdentifiers, metadata.DBInstanceIdentifier) {
			return Metadata{}, fmt.Errorf("DB instance %q declares source %q, but the source does not declare it as a read replica", metadata.DBInstanceIdentifier, sourceID)
		}
		related, err := relatedDBInstance(source)
		if err != nil {
			return Metadata{}, fmt.Errorf("resolve read-replica source for DB instance %q: %w", metadata.DBInstanceIdentifier, err)
		}
		metadata.HasReadReplicaSource = true
		metadata.ReadReplicaSource = related
	}

	seenReplicas := make(map[string]struct{}, len(metadata.ReadReplicaDBInstanceIdentifiers))
	for _, replicaID := range metadata.ReadReplicaDBInstanceIdentifiers {
		if replicaID == "" {
			return Metadata{}, fmt.Errorf("DB instance %q declares a read replica without an identifier", metadata.DBInstanceIdentifier)
		}
		if replicaID == metadata.DBInstanceIdentifier {
			return Metadata{}, fmt.Errorf("DB instance %q declares itself as a read replica", metadata.DBInstanceIdentifier)
		}
		if _, exists := seenReplicas[replicaID]; exists {
			return Metadata{}, fmt.Errorf("DB instance %q declares read replica %q more than once", metadata.DBInstanceIdentifier, replicaID)
		}
		seenReplicas[replicaID] = struct{}{}

		replica, err := describeDBInstance(ctx, client, replicaID)
		if err != nil {
			return Metadata{}, fmt.Errorf("resolve read replica of DB instance %q: %w", metadata.DBInstanceIdentifier, err)
		}
		if sourceID := aws.ToString(replica.ReadReplicaSourceDBInstanceIdentifier); sourceID != metadata.DBInstanceIdentifier {
			return Metadata{}, fmt.Errorf("DB instance %q declares read replica %q, but the replica declares source %q", metadata.DBInstanceIdentifier, replicaID, sourceID)
		}
		related, err := relatedDBInstance(replica)
		if err != nil {
			return Metadata{}, fmt.Errorf("resolve read replica of DB instance %q: %w", metadata.DBInstanceIdentifier, err)
		}
		metadata.ReadReplicas = append(metadata.ReadReplicas, related)
	}
	return metadata, nil
}

// LogFields returns a bounded discovery summary that omits endpoints, ARNs,
// account IDs, resource IDs, raw API payloads, and credentials.
func (m Metadata) LogFields() map[string]interface{} {
	return map[string]interface{}{
		"partition":                         m.Partition,
		"region":                            m.Region,
		"db_instance_identifier":            m.DBInstanceIdentifier,
		"db_instance_class":                 m.DBInstanceClass,
		"engine":                            m.Engine,
		"engine_mode":                       m.EngineMode,
		"db_parameter_group":                m.DBParameterGroup,
		"db_parameter_group_status":         m.DBParameterGroupStatus,
		"db_cluster_identifier":             m.DBClusterIdentifier,
		"db_cluster_parameter_group":        m.DBClusterParameterGroup,
		"db_cluster_parameter_group_status": m.DBClusterParameterGroupStatus,
		"cluster_member_count":              len(m.ClusterMembers),
		"is_cluster_writer":                 m.IsClusterWriter,
		"is_serverless_v2":                  m.IsServerlessV2,
		"serverless_v2_min_capacity":        m.ServerlessV2ScalingConfiguration.MinCapacity,
		"serverless_v2_max_capacity":        m.ServerlessV2ScalingConfiguration.MaxCapacity,
		"instance_status":                   m.InstanceStatus,
		"global_cluster_identifier":         m.GlobalClusterIdentifier,
		"global_cluster_member_count":       len(m.GlobalClusterMembers),
		"has_read_replica_source":           m.HasReadReplicaSource,
		"read_replica_count":                len(m.ReadReplicas),
		"multi_az":                          m.MultiAZ,
	}
}

// IsAurora reports whether the discovered engine is Aurora-compatible.
func (m Metadata) IsAurora() bool {
	engine := strings.ToLower(m.Engine)
	return engine == "aurora" || strings.HasPrefix(engine, "aurora-")
}

// DatabaseType maps an RDS engine to the database type used by the Agent.
// Aurora classification defers to IsAurora's prefix check so a future Aurora
// engine identifier is classified consistently by both methods.
func (m Metadata) DatabaseType() string {
	engine := strings.ToLower(m.Engine)
	switch {
	case strings.Contains(engine, "postgres"):
		return "postgresql"
	case engine == "aurora" || strings.Contains(engine, "mariadb") || strings.Contains(engine, "mysql"):
		return "mysql"
	default:
		return ""
	}
}

func describeDBInstance(ctx context.Context, client Client, identifier string) (types.DBInstance, error) {
	instances, err := describeDBInstancePages(ctx, client, identifier)
	if err != nil {
		return types.DBInstance{}, fmt.Errorf("describe DB instance %q: %w", identifier, err)
	}
	switch len(instances) {
	case 0:
		return types.DBInstance{}, fmt.Errorf("DB instance %q not found", identifier)
	case 1:
		// Continue below.
	default:
		return types.DBInstance{}, fmt.Errorf("DB instance %q returned %d matches", identifier, len(instances))
	}
	actualID := aws.ToString(instances[0].DBInstanceIdentifier)
	if actualID != identifier {
		return types.DBInstance{}, fmt.Errorf("DB instance %q returned identity %q", identifier, actualID)
	}
	return instances[0], nil
}

func describeDBCluster(ctx context.Context, client Client, identifier string) (types.DBCluster, error) {
	clusters, err := describeDBClusterPages(ctx, client, identifier)
	if err != nil {
		return types.DBCluster{}, fmt.Errorf("describe DB cluster %q: %w", identifier, err)
	}
	switch len(clusters) {
	case 0:
		return types.DBCluster{}, fmt.Errorf("DB cluster %q not found", identifier)
	case 1:
		// Continue below.
	default:
		return types.DBCluster{}, fmt.Errorf("DB cluster %q returned %d matches", identifier, len(clusters))
	}
	actualID := aws.ToString(clusters[0].DBClusterIdentifier)
	if actualID != identifier {
		return types.DBCluster{}, fmt.Errorf("DB cluster %q returned identity %q", identifier, actualID)
	}
	return clusters[0], nil
}

func describeGlobalCluster(ctx context.Context, client Client, identifier string) (types.GlobalCluster, error) {
	clusters, err := describeGlobalClusterPages(ctx, client, identifier)
	if err != nil {
		return types.GlobalCluster{}, fmt.Errorf("describe global cluster %q: %w", identifier, err)
	}
	switch len(clusters) {
	case 0:
		return types.GlobalCluster{}, fmt.Errorf("global cluster %q not found", identifier)
	case 1:
		// Continue below.
	default:
		return types.GlobalCluster{}, fmt.Errorf("global cluster %q returned %d matches", identifier, len(clusters))
	}
	actualID := aws.ToString(clusters[0].GlobalClusterIdentifier)
	if actualID != identifier {
		return types.GlobalCluster{}, fmt.Errorf("global cluster %q returned identity %q", identifier, actualID)
	}
	return clusters[0], nil
}

func describeDBInstancePages(ctx context.Context, client Client, identifier string) ([]types.DBInstance, error) {
	input := &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(identifier)}
	var instances []types.DBInstance
	seenMarkers := make(map[string]struct{})
	for {
		output, err := client.DescribeDBInstances(ctx, input)
		if err != nil {
			return nil, err
		}
		if output == nil {
			return instances, nil
		}
		instances = append(instances, output.DBInstances...)
		marker, err := nextMarker(output.Marker, seenMarkers)
		if err != nil {
			return nil, err
		}
		if marker == "" {
			return instances, nil
		}
		input.Marker = aws.String(marker)
	}
}

func describeDBClusterPages(ctx context.Context, client Client, identifier string) ([]types.DBCluster, error) {
	input := &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(identifier)}
	var clusters []types.DBCluster
	seenMarkers := make(map[string]struct{})
	for {
		output, err := client.DescribeDBClusters(ctx, input)
		if err != nil {
			return nil, err
		}
		if output == nil {
			return clusters, nil
		}
		clusters = append(clusters, output.DBClusters...)
		marker, err := nextMarker(output.Marker, seenMarkers)
		if err != nil {
			return nil, err
		}
		if marker == "" {
			return clusters, nil
		}
		input.Marker = aws.String(marker)
	}
}

func describeGlobalClusterPages(ctx context.Context, client Client, identifier string) ([]types.GlobalCluster, error) {
	input := &rds.DescribeGlobalClustersInput{GlobalClusterIdentifier: aws.String(identifier)}
	var clusters []types.GlobalCluster
	seenMarkers := make(map[string]struct{})
	for {
		output, err := client.DescribeGlobalClusters(ctx, input)
		if err != nil {
			return nil, err
		}
		if output == nil {
			return clusters, nil
		}
		clusters = append(clusters, output.GlobalClusters...)
		marker, err := nextMarker(output.Marker, seenMarkers)
		if err != nil {
			return nil, err
		}
		if marker == "" {
			return clusters, nil
		}
		input.Marker = aws.String(marker)
	}
}

func nextMarker(markerPointer *string, seen map[string]struct{}) (string, error) {
	marker := aws.ToString(markerPointer)
	if marker == "" {
		return "", nil
	}
	if _, exists := seen[marker]; exists {
		return "", fmt.Errorf("repeated marker %q", marker)
	}
	seen[marker] = struct{}{}
	return marker, nil
}

type rdsARNIdentity struct {
	Partition  string
	Region     string
	Identifier string
}

func parseRDSARN(value, resourceType string) (rdsARNIdentity, error) {
	if value == "" {
		return rdsARNIdentity{}, fmt.Errorf("missing %s ARN", resourceType)
	}
	parsed, err := awsarn.Parse(value)
	if err != nil {
		return rdsARNIdentity{}, fmt.Errorf("parse %s ARN: %w", resourceType, err)
	}
	if parsed.Service != "rds" {
		return rdsARNIdentity{}, fmt.Errorf("%s ARN uses service %q, want rds", resourceType, parsed.Service)
	}
	prefix := resourceType + ":"
	if !strings.HasPrefix(parsed.Resource, prefix) || len(parsed.Resource) == len(prefix) {
		return rdsARNIdentity{}, fmt.Errorf("%s ARN has resource %q", resourceType, parsed.Resource)
	}
	if resourceType != "global-cluster" && parsed.Region == "" {
		return rdsARNIdentity{}, fmt.Errorf("%s ARN has no region", resourceType)
	}
	return rdsARNIdentity{
		Partition:  parsed.Partition,
		Region:     parsed.Region,
		Identifier: strings.TrimPrefix(parsed.Resource, prefix),
	}, nil
}

func mergeMetadataLocation(metadata *Metadata, value, resourceType, identifier string) error {
	if value == "" {
		return nil
	}
	identity, err := parseRDSARN(value, resourceType)
	if err != nil {
		return err
	}
	if identity.Identifier != identifier {
		return fmt.Errorf("%s ARN identifies %q, want %q", resourceType, identity.Identifier, identifier)
	}
	if metadata.Partition != "" && metadata.Partition != identity.Partition {
		return fmt.Errorf("%s ARN partition %q conflicts with %q", resourceType, identity.Partition, metadata.Partition)
	}
	if metadata.Region != "" && identity.Region != "" && metadata.Region != identity.Region {
		return fmt.Errorf("%s ARN region %q conflicts with %q", resourceType, identity.Region, metadata.Region)
	}
	if metadata.Partition == "" {
		metadata.Partition = identity.Partition
	}
	if metadata.Region == "" {
		metadata.Region = identity.Region
	}
	return nil
}

func mergeMetadataPartition(metadata *Metadata, value, resourceType, identifier string) error {
	if value == "" {
		return nil
	}
	identity, err := parseRDSARN(value, resourceType)
	if err != nil {
		return err
	}
	if identity.Identifier != identifier {
		return fmt.Errorf("%s ARN identifies %q, want %q", resourceType, identity.Identifier, identifier)
	}
	if metadata.Partition != "" && metadata.Partition != identity.Partition {
		return fmt.Errorf("%s ARN partition %q conflicts with %q", resourceType, identity.Partition, metadata.Partition)
	}
	if metadata.Partition == "" {
		metadata.Partition = identity.Partition
	}
	return nil
}

func validateMemberLocation(cluster Metadata, member Metadata, memberID string) error {
	if cluster.Partition != "" && member.Partition != "" && cluster.Partition != member.Partition {
		return fmt.Errorf("DB cluster %q member %q uses partition %q, want %q", cluster.DBClusterIdentifier, memberID, member.Partition, cluster.Partition)
	}
	if cluster.Region != "" && member.Region != "" && cluster.Region != member.Region {
		return fmt.Errorf("DB cluster %q member %q uses region %q, want %q", cluster.DBClusterIdentifier, memberID, member.Region, cluster.Region)
	}
	return nil
}

func metadataFromInstance(instance types.DBInstance) (Metadata, error) {
	metadata := Metadata{
		DBInstanceIdentifier: aws.ToString(instance.DBInstanceIdentifier),
		DBInstanceARN:        aws.ToString(instance.DBInstanceArn),
		DBInstanceResourceID: aws.ToString(instance.DbiResourceId),
		DBInstanceClass:      aws.ToString(instance.DBInstanceClass),
		Engine:               aws.ToString(instance.Engine),
		DBClusterIdentifier:  aws.ToString(instance.DBClusterIdentifier),
		InstanceStatus:       aws.ToString(instance.DBInstanceStatus),
		MultiAZ:              aws.ToBool(instance.MultiAZ),
	}
	if instance.Endpoint != nil {
		metadata.Endpoint = aws.ToString(instance.Endpoint.Address)
		metadata.EndpointPort = aws.ToInt32(instance.Endpoint.Port)
	}
	if len(instance.DBParameterGroups) > 0 {
		metadata.DBParameterGroup = aws.ToString(instance.DBParameterGroups[0].DBParameterGroupName)
		metadata.DBParameterGroupStatus = aws.ToString(instance.DBParameterGroups[0].ParameterApplyStatus)
	}
	metadata.IsServerlessV2 = metadata.DBInstanceClass == "db.serverless"
	if err := mergeMetadataLocation(&metadata, metadata.DBInstanceARN, "db", metadata.DBInstanceIdentifier); err != nil {
		return Metadata{}, fmt.Errorf("DB instance %q: %w", metadata.DBInstanceIdentifier, err)
	}
	return metadata, nil
}

func relatedDBInstance(instance types.DBInstance) (RelatedDBInstance, error) {
	metadata, err := metadataFromInstance(instance)
	if err != nil {
		return RelatedDBInstance{}, err
	}
	return RelatedDBInstance{
		DBInstanceIdentifier: metadata.DBInstanceIdentifier,
		DBInstanceARN:        metadata.DBInstanceARN,
		DBInstanceResourceID: metadata.DBInstanceResourceID,
		Endpoint:             metadata.Endpoint,
		EndpointPort:         metadata.EndpointPort,
		Engine:               metadata.Engine,
		InstanceStatus:       metadata.InstanceStatus,
		Partition:            metadata.Partition,
		Region:               metadata.Region,
		MultiAZ:              metadata.MultiAZ,
	}, nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func membersForInstance(members []types.DBClusterMember, identifier string) []types.DBClusterMember {
	matching := make([]types.DBClusterMember, 0, 1)
	for _, member := range members {
		if aws.ToString(member.DBInstanceIdentifier) == identifier {
			matching = append(matching, member)
		}
	}
	return matching
}
