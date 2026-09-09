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
	// TopologyIncomplete preserves usable target metadata when optional API discovery fails.
	TopologyIncomplete bool
	TopologyFacts      *AWSFacts
	Partition          string
	Region             string

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
	metadata, err := metadataFromInstance(instance, false)
	if err != nil {
		return Metadata{}, err
	}
	return collectAWSFacts(ctx, client, instance, metadata), nil
}

// DiscoverInstanceForApply resolves only the target DB instance and, for
// Aurora, its attached DB cluster and target membership. Recommendation apply
// must not depend on unrelated topology members or optional relationships.
func DiscoverInstanceForApply(ctx context.Context, client Client, identifier string) (Metadata, error) {
	if client == nil {
		return Metadata{}, fmt.Errorf("discover DB instance %q for apply: RDS client is nil", identifier)
	}

	instance, err := describeDBInstance(ctx, client, identifier)
	if err != nil {
		return Metadata{}, err
	}
	metadata, err := metadataFromInstance(instance, false)
	if err != nil {
		return Metadata{}, err
	}
	if !metadata.IsAurora() {
		return metadata, nil
	}
	return discoverAuroraInstanceForApply(ctx, client, metadata)
}

func discoverAuroraInstanceForApply(ctx context.Context, client Client, metadata Metadata) (Metadata, error) {
	if metadata.DBClusterIdentifier == "" {
		return Metadata{}, fmt.Errorf("Aurora DB instance %q has no DB cluster identifier", metadata.DBInstanceIdentifier)
	}

	cluster, err := describeDBClusterInRegion(ctx, client, metadata.DBClusterIdentifier, metadata.Region)
	if err != nil {
		return Metadata{}, err
	}
	if err := populateClusterMetadata(&metadata, cluster, false); err != nil {
		return Metadata{}, err
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

	member := matchingMembers[0]
	metadata.DBClusterParameterGroupStatus = aws.ToString(member.DBClusterParameterGroupStatus)
	metadata.IsClusterWriter = aws.ToBool(member.IsClusterWriter)
	metadata.PromotionTier = aws.ToInt32(member.PromotionTier)
	metadata.ClusterMembers = []ClusterMember{clusterMemberFromMetadata(metadata, member)}
	return metadata, nil
}

func (m Metadata) LogFields() map[string]interface{} {
	fields := map[string]interface{}{
		"topology_incomplete":               m.TopologyIncomplete,
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
		"is_cluster_writer":                 m.IsClusterWriter,
		"is_serverless_v2":                  m.IsServerlessV2,
		"serverless_v2_min_capacity":        m.ServerlessV2ScalingConfiguration.MinCapacity,
		"serverless_v2_max_capacity":        m.ServerlessV2ScalingConfiguration.MaxCapacity,
		"instance_status":                   m.InstanceStatus,
		"global_cluster_identifier":         m.GlobalClusterIdentifier,
		"multi_az":                          m.MultiAZ,
	}
	if m.TopologyFacts != nil {
		for _, source := range []string{"Target", "Cluster", "GlobalCluster", "Peers", "Source", "Children"} {
			status := m.TopologyFacts.Sources[source]
			if status != "ok" && status != "unsupported" && status != "error" {
				status = "error"
			}
			fields["source_"+source] = status
		}
	}
	return fields
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
	return describeDBInstanceInRegion(ctx, client, identifier, "")
}

func describeDBInstanceInRegion(ctx context.Context, client Client, identifier, region string) (types.DBInstance, error) {
	expectedID, err := identifierFromRequest(identifier, "db")
	if err != nil {
		return types.DBInstance{}, fmt.Errorf("describe DB instance %q: %w", identifier, err)
	}
	instances, err := describeDBInstancePagesInRegion(ctx, client, identifier, region)
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
	if !strings.EqualFold(actualID, expectedID) {
		return types.DBInstance{}, fmt.Errorf("DB instance %q returned identity %q", identifier, actualID)
	}
	if strings.HasPrefix(identifier, "arn:") && !matchesDBInstanceReference(identifier, instances[0], instances[0]) {
		return types.DBInstance{}, fmt.Errorf("DB instance %q returned a different ARN identity", identifier)
	}
	return instances[0], nil
}

func describeDBCluster(ctx context.Context, client Client, identifier string) (types.DBCluster, error) {
	return describeDBClusterInRegion(ctx, client, identifier, "")
}

func describeDBClusterInRegion(ctx context.Context, client Client, identifier, region string) (types.DBCluster, error) {
	expectedID, err := identifierFromRequest(identifier, "cluster")
	if err != nil {
		return types.DBCluster{}, fmt.Errorf("describe DB cluster %q: %w", identifier, err)
	}
	clusters, err := describeDBClusterPagesInRegion(ctx, client, identifier, region)
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
	if !strings.EqualFold(actualID, expectedID) {
		return types.DBCluster{}, fmt.Errorf("DB cluster %q returned identity %q", identifier, actualID)
	}
	return clusters[0], nil
}

func describeGlobalCluster(ctx context.Context, client Client, identifier string) (types.GlobalCluster, error) {
	return describeGlobalClusterInRegion(ctx, client, identifier, "")
}

func describeGlobalClusterInRegion(ctx context.Context, client Client, identifier, region string) (types.GlobalCluster, error) {
	expectedID, err := identifierFromRequest(identifier, "global-cluster")
	if err != nil {
		return types.GlobalCluster{}, fmt.Errorf("describe global cluster %q: %w", identifier, err)
	}
	clusters, err := describeGlobalClusterPagesInRegion(ctx, client, identifier, region)
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
	if !strings.EqualFold(actualID, expectedID) {
		return types.GlobalCluster{}, fmt.Errorf("global cluster %q returned identity %q", identifier, actualID)
	}
	return clusters[0], nil
}

func describeDBInstancePages(ctx context.Context, client Client, identifier string) ([]types.DBInstance, error) {
	return describeDBInstancePagesInRegion(ctx, client, identifier, "")
}

func describeDBInstancePagesInRegion(ctx context.Context, client Client, identifier, region string) ([]types.DBInstance, error) {
	var options []func(*rds.Options)
	if strings.HasPrefix(identifier, "arn:") {
		identity, err := parseRDSARN(identifier, "db")
		if err != nil {
			return nil, err
		}
		region = identity.Region
	}
	if region != "" {
		options = append(options, func(o *rds.Options) { o.Region = region })
	}
	input := &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(identifier)}
	var instances []types.DBInstance
	seenMarkers := make(map[string]struct{})
	for {
		output, err := client.DescribeDBInstances(ctx, input, options...)
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
	return describeDBClusterPagesInRegion(ctx, client, identifier, "")
}

func describeDBClusterPagesInRegion(ctx context.Context, client Client, identifier, region string) ([]types.DBCluster, error) {
	var options []func(*rds.Options)
	if strings.HasPrefix(identifier, "arn:") {
		identity, err := parseRDSARN(identifier, "cluster")
		if err != nil {
			return nil, err
		}
		region = identity.Region
	}
	if region != "" {
		options = append(options, func(o *rds.Options) { o.Region = region })
	}
	input := &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(identifier)}
	var clusters []types.DBCluster
	seenMarkers := make(map[string]struct{})
	for {
		output, err := client.DescribeDBClusters(ctx, input, options...)
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
	return describeGlobalClusterPagesInRegion(ctx, client, identifier, "")
}

func describeGlobalClusterPagesInRegion(ctx context.Context, client Client, identifier, region string) ([]types.GlobalCluster, error) {
	var options []func(*rds.Options)
	if region != "" {
		options = append(options, func(o *rds.Options) { o.Region = region })
	}
	input := &rds.DescribeGlobalClustersInput{GlobalClusterIdentifier: aws.String(identifier)}
	var clusters []types.GlobalCluster
	seenMarkers := make(map[string]struct{})
	for {
		output, err := client.DescribeGlobalClusters(ctx, input, options...)
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
	AccountID  string
	Identifier string
}

func identifierFromRequest(value, resourceType string) (string, error) {
	if !awsarn.IsARN(value) {
		return value, nil
	}
	identity, err := parseRDSARN(value, resourceType)
	if err != nil {
		return "", err
	}
	return identity.Identifier, nil
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
	if parsed.Partition == "" {
		return rdsARNIdentity{}, fmt.Errorf("%s ARN has no partition", resourceType)
	}
	if parsed.AccountID == "" {
		return rdsARNIdentity{}, fmt.Errorf("%s ARN has no account ID", resourceType)
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
		AccountID:  parsed.AccountID,
		Identifier: strings.TrimPrefix(parsed.Resource, prefix),
	}, nil
}

func sameRDSIdentity(left, right rdsARNIdentity) bool {
	return left.Partition == right.Partition &&
		left.Region == right.Region &&
		left.AccountID == right.AccountID &&
		strings.EqualFold(left.Identifier, right.Identifier)
}

func mergeMetadataLocation(metadata *Metadata, value, resourceType, identifier string) error {
	if value == "" {
		return nil
	}
	identity, err := parseRDSARN(value, resourceType)
	if err != nil {
		return err
	}
	if !strings.EqualFold(identity.Identifier, identifier) {
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

func populateClusterMetadata(metadata *Metadata, cluster types.DBCluster, requireStableIdentity bool) error {
	metadata.DBClusterARN = aws.ToString(cluster.DBClusterArn)
	metadata.DBClusterResourceID = aws.ToString(cluster.DbClusterResourceId)
	metadata.EngineMode = aws.ToString(cluster.EngineMode)
	metadata.DBClusterParameterGroup = aws.ToString(cluster.DBClusterParameterGroup)
	metadata.ClusterEndpoint = aws.ToString(cluster.Endpoint)
	metadata.ClusterReaderEndpoint = aws.ToString(cluster.ReaderEndpoint)
	if requireStableIdentity && metadata.DBClusterARN == "" {
		return fmt.Errorf("DB cluster %q has no ARN", metadata.DBClusterIdentifier)
	}
	if requireStableIdentity && metadata.DBClusterResourceID == "" {
		return fmt.Errorf("DB cluster %q has no resource ID", metadata.DBClusterIdentifier)
	}
	if err := mergeMetadataLocation(metadata, metadata.DBClusterARN, "cluster", metadata.DBClusterIdentifier); err != nil {
		return fmt.Errorf("DB cluster %q: %w", metadata.DBClusterIdentifier, err)
	}
	if cluster.ServerlessV2ScalingConfiguration != nil {
		metadata.HasServerlessV2ScalingConfiguration = true
		metadata.ServerlessV2ScalingConfiguration = ServerlessV2ScalingConfiguration{
			MinCapacity:           aws.ToFloat64(cluster.ServerlessV2ScalingConfiguration.MinCapacity),
			MaxCapacity:           aws.ToFloat64(cluster.ServerlessV2ScalingConfiguration.MaxCapacity),
			SecondsUntilAutoPause: aws.ToInt32(cluster.ServerlessV2ScalingConfiguration.SecondsUntilAutoPause),
		}
	}
	return nil
}

func clusterMemberFromMetadata(metadata Metadata, member types.DBClusterMember) ClusterMember {
	return ClusterMember{
		DBInstanceIdentifier:          metadata.DBInstanceIdentifier,
		DBInstanceARN:                 metadata.DBInstanceARN,
		DBInstanceResourceID:          metadata.DBInstanceResourceID,
		DBInstanceClass:               metadata.DBInstanceClass,
		Endpoint:                      metadata.Endpoint,
		EndpointPort:                  metadata.EndpointPort,
		InstanceStatus:                metadata.InstanceStatus,
		IsClusterWriter:               aws.ToBool(member.IsClusterWriter),
		IsServerlessV2:                metadata.IsServerlessV2,
		PromotionTier:                 aws.ToInt32(member.PromotionTier),
		DBClusterParameterGroupStatus: aws.ToString(member.DBClusterParameterGroupStatus),
	}
}

func metadataFromInstance(instance types.DBInstance, requireStableIdentity bool) (Metadata, error) {
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
	if requireStableIdentity && metadata.DBInstanceARN == "" {
		return Metadata{}, fmt.Errorf("DB instance %q has no ARN", metadata.DBInstanceIdentifier)
	}
	if requireStableIdentity && metadata.DBInstanceResourceID == "" {
		return Metadata{}, fmt.Errorf("DB instance %q has no resource ID", metadata.DBInstanceIdentifier)
	}
	if err := mergeMetadataLocation(&metadata, metadata.DBInstanceARN, "db", metadata.DBInstanceIdentifier); err != nil {
		return Metadata{}, fmt.Errorf("DB instance %q: %w", metadata.DBInstanceIdentifier, err)
	}
	if requireStableIdentity && metadata.Region == "" {
		return Metadata{}, fmt.Errorf("DB instance %q has no region", metadata.DBInstanceIdentifier)
	}
	return metadata, nil
}

func containsStringFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}

func membersForInstance(members []types.DBClusterMember, identifier string) []types.DBClusterMember {
	matching := make([]types.DBClusterMember, 0, 1)
	for _, member := range members {
		if strings.EqualFold(aws.ToString(member.DBInstanceIdentifier), identifier) {
			matching = append(matching, member)
		}
	}
	return matching
}

func matchesDBInstanceReference(reference string, target, declaring types.DBInstance) bool {
	identity, err := parseRDSARN(reference, "db")
	if !strings.HasPrefix(reference, "arn:") {
		identity, err = parseRDSARN(aws.ToString(declaring.DBInstanceArn), "db")
		identity.Identifier = reference
	}
	want, wantErr := parseRDSARN(aws.ToString(target.DBInstanceArn), "db")
	return err == nil && wantErr == nil && sameRDSIdentity(identity, want)
}
