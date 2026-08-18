package awsrds

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
)

// Metadata contains the instance and cluster topology attached to one RDS DB
// instance.
type Metadata struct {
	DBInstanceIdentifier    string
	DBInstanceResourceID    string
	DBInstanceClass         string
	Endpoint                string
	EndpointPort            int32
	Engine                  string
	EngineMode              string
	DBParameterGroup        string
	DBParameterGroupStatus  string
	DBClusterIdentifier     string
	DBClusterParameterGroup string
	IsClusterWriter         bool
	IsServerlessV2          bool
	InstanceStatus          string
}

// DiscoverInstance looks up one configured DB instance and, for Aurora only,
// resolves its cluster attachment and current writer role.
func DiscoverInstance(ctx context.Context, client Client, identifier string) (Metadata, error) {
	if client == nil {
		return Metadata{}, fmt.Errorf("discover DB instance %q: RDS client is nil", identifier)
	}

	output, err := client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(identifier),
	})
	if err != nil {
		return Metadata{}, fmt.Errorf("describe DB instance %q: %w", identifier, err)
	}

	instances := dbInstances(output)
	switch len(instances) {
	case 0:
		return Metadata{}, fmt.Errorf("DB instance %q not found", identifier)
	case 1:
		// Continue below.
	default:
		return Metadata{}, fmt.Errorf("DB instance %q returned %d matches", identifier, len(instances))
	}

	instance := instances[0]
	metadata := metadataFromInstance(instance)
	if !metadata.IsAurora() {
		return metadata, nil
	}

	if metadata.DBClusterIdentifier == "" {
		return Metadata{}, fmt.Errorf("Aurora DB instance %q has no DB cluster identifier", identifier)
	}

	clusterOutput, err := client.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{
		DBClusterIdentifier: aws.String(metadata.DBClusterIdentifier),
	})
	if err != nil {
		return Metadata{}, fmt.Errorf("describe DB cluster %q: %w", metadata.DBClusterIdentifier, err)
	}

	clusters := dbClusters(clusterOutput)
	switch len(clusters) {
	case 0:
		return Metadata{}, fmt.Errorf("DB cluster %q not found", metadata.DBClusterIdentifier)
	case 1:
		// Continue below.
	default:
		return Metadata{}, fmt.Errorf("DB cluster %q returned %d matches", metadata.DBClusterIdentifier, len(clusters))
	}

	cluster := clusters[0]
	matchingMembers := membersForInstance(cluster.DBClusterMembers, metadata.DBInstanceIdentifier)
	switch len(matchingMembers) {
	case 0:
		return Metadata{}, fmt.Errorf("DB instance %q is not a member of DB cluster %q", metadata.DBInstanceIdentifier, metadata.DBClusterIdentifier)
	case 1:
		// Continue below.
	default:
		return Metadata{}, fmt.Errorf("DB instance %q has %d memberships in DB cluster %q", metadata.DBInstanceIdentifier, len(matchingMembers), metadata.DBClusterIdentifier)
	}

	metadata.EngineMode = aws.ToString(cluster.EngineMode)
	metadata.DBClusterParameterGroup = aws.ToString(cluster.DBClusterParameterGroup)
	metadata.IsClusterWriter = aws.ToBool(matchingMembers[0].IsClusterWriter)

	return metadata, nil
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
	case engine == "mariadb" || engine == "mysql" || m.IsAurora():
		return "mysql"
	default:
		return ""
	}
}

// ApplyEndpoint assigns the discovered endpoint to the host and, when AWS
// supplies one, port fields for the matching database engine.
func (m Metadata) ApplyEndpoint(configuration *config.Config) {
	if configuration == nil {
		return
	}

	switch m.DatabaseType() {
	case "mysql":
		configuration.MysqlHost = m.Endpoint
		if m.EndpointPort > 0 {
			configuration.MysqlPort = strconv.FormatInt(int64(m.EndpointPort), 10)
		}
	case "postgresql":
		configuration.PgHost = m.Endpoint
		if m.EndpointPort > 0 {
			configuration.PgPort = strconv.FormatInt(int64(m.EndpointPort), 10)
		}
	}
}

func metadataFromInstance(instance types.DBInstance) Metadata {
	metadata := Metadata{
		DBInstanceIdentifier: aws.ToString(instance.DBInstanceIdentifier),
		DBInstanceResourceID: aws.ToString(instance.DbiResourceId),
		DBInstanceClass:      aws.ToString(instance.DBInstanceClass),
		Engine:               aws.ToString(instance.Engine),
		DBClusterIdentifier:  aws.ToString(instance.DBClusterIdentifier),
		InstanceStatus:       aws.ToString(instance.DBInstanceStatus),
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

	return metadata
}

func dbInstances(output *rds.DescribeDBInstancesOutput) []types.DBInstance {
	if output == nil {
		return nil
	}
	return output.DBInstances
}

func dbClusters(output *rds.DescribeDBClustersOutput) []types.DBCluster {
	if output == nil {
		return nil
	}
	return output.DBClusters
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
