package awsrds

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
)

type fakeClient struct {
	instancesOutput *rds.DescribeDBInstancesOutput
	instancesErr    error
	clustersOutput  *rds.DescribeDBClustersOutput
	clustersErr     error

	describeInstanceIDs []string
	describeClusterIDs  []string
}

func (f *fakeClient) DescribeDBInstances(_ context.Context, input *rds.DescribeDBInstancesInput, _ ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	f.describeInstanceIDs = append(f.describeInstanceIDs, aws.ToString(input.DBInstanceIdentifier))
	return f.instancesOutput, f.instancesErr
}

func (f *fakeClient) DescribeDBClusters(_ context.Context, input *rds.DescribeDBClustersInput, _ ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error) {
	f.describeClusterIDs = append(f.describeClusterIDs, aws.ToString(input.DBClusterIdentifier))
	return f.clustersOutput, f.clustersErr
}

func (f *fakeClient) DescribeDBParameters(context.Context, *rds.DescribeDBParametersInput, ...func(*rds.Options)) (*rds.DescribeDBParametersOutput, error) {
	return nil, errors.New("unexpected DescribeDBParameters call")
}

func (f *fakeClient) DescribeDBClusterParameters(context.Context, *rds.DescribeDBClusterParametersInput, ...func(*rds.Options)) (*rds.DescribeDBClusterParametersOutput, error) {
	return nil, errors.New("unexpected DescribeDBClusterParameters call")
}

func (f *fakeClient) ModifyDBParameterGroup(context.Context, *rds.ModifyDBParameterGroupInput, ...func(*rds.Options)) (*rds.ModifyDBParameterGroupOutput, error) {
	return nil, errors.New("unexpected ModifyDBParameterGroup call")
}

func (f *fakeClient) ModifyDBClusterParameterGroup(context.Context, *rds.ModifyDBClusterParameterGroupInput, ...func(*rds.Options)) (*rds.ModifyDBClusterParameterGroupOutput, error) {
	return nil, errors.New("unexpected ModifyDBClusterParameterGroup call")
}

func TestDiscoverInstance(t *testing.T) {
	t.Parallel()

	const (
		mysqlID     = "orders-mysql"
		writerID    = "orders-reader-labelled"
		readerID    = "analytics-writer-labelled"
		clusterID   = "orders-cluster"
		analyticsID = "analytics-cluster"
	)

	tests := []struct {
		name             string
		identifier       string
		instancesOutput  *rds.DescribeDBInstancesOutput
		instancesErr     error
		clustersOutput   *rds.DescribeDBClustersOutput
		clustersErr      error
		want             Metadata
		wantErrContains  string
		wantClusterCalls []string
	}{
		{
			name:       "ordinary RDS MySQL captures instance metadata without cluster discovery",
			identifier: mysqlID,
			instancesOutput: &rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{{
				DBInstanceIdentifier: aws.String(mysqlID),
				DbiResourceId:        aws.String("db-resource-mysql"),
				DBInstanceClass:      aws.String("db.m7g.large"),
				Endpoint:             &types.Endpoint{Address: aws.String("orders-mysql.example"), Port: aws.Int32(3307)},
				Engine:               aws.String("mysql"),
				DBClusterIdentifier:  aws.String("ordinary-multi-az-cluster"),
				DBInstanceStatus:     aws.String("available"),
				DBParameterGroups: []types.DBParameterGroupStatus{{
					DBParameterGroupName: aws.String("mysql-custom"),
					ParameterApplyStatus: aws.String("in-sync"),
				}},
			}}},
			want: Metadata{
				DBInstanceIdentifier:   mysqlID,
				DBInstanceResourceID:   "db-resource-mysql",
				DBInstanceClass:        "db.m7g.large",
				Endpoint:               "orders-mysql.example",
				EndpointPort:           3307,
				Engine:                 "mysql",
				DBParameterGroup:       "mysql-custom",
				DBParameterGroupStatus: "in-sync",
				DBClusterIdentifier:    "ordinary-multi-az-cluster",
				InstanceStatus:         "available",
			},
		},
		{
			name:       "Aurora MySQL resolves the writer from its matching member",
			identifier: writerID,
			instancesOutput: &rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{{
				DBInstanceIdentifier: aws.String(writerID),
				DbiResourceId:        aws.String("db-resource-writer"),
				DBInstanceClass:      aws.String("db.r7g.xlarge"),
				Endpoint:             &types.Endpoint{Address: aws.String("writer.example")},
				Engine:               aws.String("aurora-mysql"),
				DBClusterIdentifier:  aws.String(clusterID),
				DBInstanceStatus:     aws.String("available"),
				DBParameterGroups: []types.DBParameterGroupStatus{{
					DBParameterGroupName: aws.String("aurora-instance-custom"),
					ParameterApplyStatus: aws.String("applying"),
				}},
			}}},
			clustersOutput: &rds.DescribeDBClustersOutput{DBClusters: []types.DBCluster{{
				DBClusterIdentifier:     aws.String(clusterID),
				DBClusterParameterGroup: aws.String("aurora-cluster-custom"),
				EngineMode:              aws.String("provisioned"),
				DBClusterMembers: []types.DBClusterMember{
					{DBInstanceIdentifier: aws.String("orders-other-member"), IsClusterWriter: aws.Bool(false)},
					{
						DBInstanceIdentifier:          aws.String(writerID),
						DBClusterParameterGroupStatus: aws.String("applying"),
						IsClusterWriter:               aws.Bool(true),
					},
				},
			}}},
			want: Metadata{
				DBInstanceIdentifier:          writerID,
				DBInstanceResourceID:          "db-resource-writer",
				DBInstanceClass:               "db.r7g.xlarge",
				Endpoint:                      "writer.example",
				Engine:                        "aurora-mysql",
				EngineMode:                    "provisioned",
				DBParameterGroup:              "aurora-instance-custom",
				DBParameterGroupStatus:        "applying",
				DBClusterIdentifier:           clusterID,
				DBClusterParameterGroup:       "aurora-cluster-custom",
				DBClusterParameterGroupStatus: "applying",
				IsClusterWriter:               true,
				InstanceStatus:                "available",
			},
			wantClusterCalls: []string{clusterID},
		},
		{
			name:       "Aurora PostgreSQL resolves a serverless reader from its matching member",
			identifier: readerID,
			instancesOutput: &rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{{
				DBInstanceIdentifier: aws.String(readerID),
				DbiResourceId:        aws.String("db-resource-reader"),
				DBInstanceClass:      aws.String("db.serverless"),
				Endpoint:             &types.Endpoint{Address: aws.String("reader.example"), Port: aws.Int32(5433)},
				Engine:               aws.String("aurora-postgresql"),
				DBClusterIdentifier:  aws.String(analyticsID),
				DBInstanceStatus:     aws.String("backing-up"),
				DBParameterGroups: []types.DBParameterGroupStatus{{
					DBParameterGroupName: aws.String("aurora-pg-instance"),
					ParameterApplyStatus: aws.String("in-sync"),
				}},
			}}},
			clustersOutput: &rds.DescribeDBClustersOutput{DBClusters: []types.DBCluster{{
				DBClusterIdentifier:     aws.String(analyticsID),
				DBClusterParameterGroup: aws.String("aurora-pg-cluster"),
				EngineMode:              aws.String("provisioned"),
				DBClusterMembers: []types.DBClusterMember{
					{DBInstanceIdentifier: aws.String("analytics-other-member"), IsClusterWriter: aws.Bool(true)},
					{
						DBInstanceIdentifier:          aws.String(readerID),
						DBClusterParameterGroupStatus: aws.String("in-sync"),
						IsClusterWriter:               aws.Bool(false),
					},
				},
			}}},
			want: Metadata{
				DBInstanceIdentifier:          readerID,
				DBInstanceResourceID:          "db-resource-reader",
				DBInstanceClass:               "db.serverless",
				Endpoint:                      "reader.example",
				EndpointPort:                  5433,
				Engine:                        "aurora-postgresql",
				EngineMode:                    "provisioned",
				DBParameterGroup:              "aurora-pg-instance",
				DBParameterGroupStatus:        "in-sync",
				DBClusterIdentifier:           analyticsID,
				DBClusterParameterGroup:       "aurora-pg-cluster",
				DBClusterParameterGroupStatus: "in-sync",
				IsServerlessV2:                true,
				InstanceStatus:                "backing-up",
			},
			wantClusterCalls: []string{analyticsID},
		},
		{
			name:            "missing instance",
			identifier:      "missing-db",
			instancesOutput: &rds.DescribeDBInstancesOutput{},
			wantErrContains: `DB instance "missing-db" not found`,
		},
		{
			name:       "duplicate instance result",
			identifier: "duplicate-db",
			instancesOutput: &rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{
				{DBInstanceIdentifier: aws.String("duplicate-db")},
				{DBInstanceIdentifier: aws.String("duplicate-db")},
			}},
			wantErrContains: `DB instance "duplicate-db" returned 2 matches`,
		},
		{
			name:       "missing cluster",
			identifier: writerID,
			instancesOutput: &rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{{
				DBInstanceIdentifier: aws.String(writerID),
				Engine:               aws.String("aurora-mysql"),
				DBClusterIdentifier:  aws.String(clusterID),
			}}},
			clustersOutput:   &rds.DescribeDBClustersOutput{},
			wantErrContains:  `DB cluster "orders-cluster" not found`,
			wantClusterCalls: []string{clusterID},
		},
		{
			name:       "duplicate cluster result",
			identifier: writerID,
			instancesOutput: &rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{{
				DBInstanceIdentifier: aws.String(writerID),
				Engine:               aws.String("aurora-mysql"),
				DBClusterIdentifier:  aws.String(clusterID),
			}}},
			clustersOutput: &rds.DescribeDBClustersOutput{DBClusters: []types.DBCluster{
				{DBClusterIdentifier: aws.String(clusterID)},
				{DBClusterIdentifier: aws.String(clusterID)},
			}},
			wantErrContains:  `DB cluster "orders-cluster" returned 2 matches`,
			wantClusterCalls: []string{clusterID},
		},
		{
			name:       "missing cluster member",
			identifier: writerID,
			instancesOutput: &rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{{
				DBInstanceIdentifier: aws.String(writerID),
				Engine:               aws.String("aurora-mysql"),
				DBClusterIdentifier:  aws.String(clusterID),
			}}},
			clustersOutput: &rds.DescribeDBClustersOutput{DBClusters: []types.DBCluster{{
				DBClusterIdentifier: aws.String(clusterID),
				DBClusterMembers: []types.DBClusterMember{{
					DBInstanceIdentifier: aws.String("another-instance"),
					IsClusterWriter:      aws.Bool(true),
				}},
			}}},
			wantErrContains:  `DB instance "orders-reader-labelled" is not a member of DB cluster "orders-cluster"`,
			wantClusterCalls: []string{clusterID},
		},
		{
			name:       "duplicate matching cluster member",
			identifier: writerID,
			instancesOutput: &rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{{
				DBInstanceIdentifier: aws.String(writerID),
				Engine:               aws.String("aurora-mysql"),
				DBClusterIdentifier:  aws.String(clusterID),
			}}},
			clustersOutput: &rds.DescribeDBClustersOutput{DBClusters: []types.DBCluster{{
				DBClusterIdentifier: aws.String(clusterID),
				DBClusterMembers: []types.DBClusterMember{
					{DBInstanceIdentifier: aws.String(writerID), IsClusterWriter: aws.Bool(true)},
					{DBInstanceIdentifier: aws.String(writerID), IsClusterWriter: aws.Bool(false)},
				},
			}}},
			wantErrContains:  `DB instance "orders-reader-labelled" has 2 memberships in DB cluster "orders-cluster"`,
			wantClusterCalls: []string{clusterID},
		},
		{
			name:       "nil optional AWS fields are safe",
			identifier: "nil-fields-db",
			instancesOutput: &rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{{
				DBInstanceIdentifier: aws.String("nil-fields-db"),
				Engine:               aws.String("mysql"),
				Endpoint:             &types.Endpoint{},
				DBParameterGroups:    []types.DBParameterGroupStatus{{}},
			}}},
			want: Metadata{DBInstanceIdentifier: "nil-fields-db", Engine: "mysql"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &fakeClient{
				instancesOutput: tt.instancesOutput,
				instancesErr:    tt.instancesErr,
				clustersOutput:  tt.clustersOutput,
				clustersErr:     tt.clustersErr,
			}

			got, err := DiscoverInstance(context.Background(), client, tt.identifier)
			if tt.wantErrContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Fatalf("DiscoverInstance() error = %v, want containing %q", err, tt.wantErrContains)
				}
			} else {
				if err != nil {
					t.Fatalf("DiscoverInstance() unexpected error: %v", err)
				}
				if got != tt.want {
					t.Fatalf("DiscoverInstance() = %#v, want %#v", got, tt.want)
				}
			}

			if len(client.describeInstanceIDs) != 1 || client.describeInstanceIDs[0] != tt.identifier {
				t.Fatalf("DescribeDBInstances calls = %v, want [%q]", client.describeInstanceIDs, tt.identifier)
			}
			if !equalStrings(client.describeClusterIDs, tt.wantClusterCalls) {
				t.Fatalf("DescribeDBClusters calls = %v, want %v", client.describeClusterIDs, tt.wantClusterCalls)
			}
		})
	}
}

func TestMetadataHelpers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		metadata         Metadata
		wantAurora       bool
		wantDatabaseType string
	}{
		{name: "ordinary MySQL", metadata: Metadata{Engine: "mysql", Endpoint: "mysql.example", EndpointPort: 3307}, wantDatabaseType: "mysql"},
		{name: "ordinary MariaDB", metadata: Metadata{Engine: "mariadb", Endpoint: "maria.example"}, wantDatabaseType: "mysql"},
		{name: "Aurora MySQL", metadata: Metadata{Engine: "aurora-mysql", Endpoint: "aurora-mysql.example"}, wantAurora: true, wantDatabaseType: "mysql"},
		{name: "legacy Aurora engine", metadata: Metadata{Engine: "aurora", Endpoint: "aurora.example"}, wantAurora: true, wantDatabaseType: "mysql"},
		{name: "ordinary PostgreSQL", metadata: Metadata{Engine: "postgres", Endpoint: "postgres.example", EndpointPort: 5433}, wantDatabaseType: "postgresql"},
		{name: "Aurora PostgreSQL", metadata: Metadata{Engine: "aurora-postgresql", Endpoint: "aurora-pg.example"}, wantAurora: true, wantDatabaseType: "postgresql"},
		{name: "unsupported engine", metadata: Metadata{Engine: "oracle-ee", Endpoint: "oracle.example"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.metadata.IsAurora(); got != tt.wantAurora {
				t.Fatalf("Metadata.IsAurora() = %t, want %t", got, tt.wantAurora)
			}
			if got := tt.metadata.DatabaseType(); got != tt.wantDatabaseType {
				t.Fatalf("Metadata.DatabaseType() = %q, want %q", got, tt.wantDatabaseType)
			}
		})
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
