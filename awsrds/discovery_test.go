package awsrds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
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
	globalOutput    *rds.DescribeGlobalClustersOutput
	globalErr       error

	instancePages map[describePageKey]*rds.DescribeDBInstancesOutput
	instanceErrs  map[describePageKey]error
	clusterPages  map[describePageKey]*rds.DescribeDBClustersOutput
	clusterErrs   map[describePageKey]error
	globalPages   map[describePageKey]*rds.DescribeGlobalClustersOutput
	globalErrs    map[describePageKey]error

	describeInstanceIDs     []string
	describeInstanceRegions []string
	describeClusterIDs      []string
	describeGlobalIDs       []string
}

type describePageKey struct {
	identifier string
	marker     string
}

type discoveryHTTPClient func(*http.Request) (*http.Response, error)

func (f discoveryHTTPClient) Do(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestARNLookupUsesRegionalEndpointAndSigningRegion(t *testing.T) {
	client := rds.NewFromConfig(aws.Config{
		Region: "us-east-1",
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, nil
		}),
		HTTPClient: discoveryHTTPClient(func(request *http.Request) (*http.Response, error) {
			if request.URL.Host != "rds.eu-west-1.amazonaws.com" {
				t.Errorf("endpoint = %s", request.URL.Host)
			}
			if !strings.Contains(request.Header.Get("Authorization"), "/eu-west-1/rds/aws4_request") {
				t.Error("request signed for wrong region")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`<DescribeDBInstancesResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><DescribeDBInstancesResult><DBInstances/></DescribeDBInstancesResult></DescribeDBInstancesResponse>`))}, nil
		}),
	})
	if _, err := describeDBInstancePages(context.Background(), client, "arn:aws:rds:eu-west-1:123456789012:db:orders"); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeClient) DescribeDBInstances(_ context.Context, input *rds.DescribeDBInstancesInput, options ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	settings := rds.Options{}
	for _, option := range options {
		option(&settings)
	}
	f.describeInstanceRegions = append(f.describeInstanceRegions, settings.Region)
	identifier := aws.ToString(input.DBInstanceIdentifier)
	key := describePageKey{identifier: identifier, marker: aws.ToString(input.Marker)}
	f.describeInstanceIDs = append(f.describeInstanceIDs, identifier)
	if output, ok := f.instancePages[key]; ok {
		return output, f.instanceErrs[key]
	}
	return f.instancesOutput, f.instancesErr
}

func TestDiscoverCrossRegionReplicas(t *testing.T) {
	fixture := loadDiscoveryFixture(t, "rds_read_replica.json")
	target := &fixture.DBInstances[0]
	source := &fixture.DBInstances[1]
	replica := &fixture.DBInstances[2]
	source.DBInstanceArn = aws.String(strings.Replace(aws.ToString(source.DBInstanceArn), "us-east-1", "eu-west-1", 1))
	replica.DBInstanceArn = aws.String(strings.Replace(aws.ToString(replica.DBInstanceArn), "us-east-1", "us-west-2", 1))
	target.ReadReplicaSourceDBInstanceIdentifier = source.DBInstanceArn
	target.ReadReplicaDBInstanceIdentifiers = []string{aws.ToString(replica.DBInstanceArn)}
	source.ReadReplicaDBInstanceIdentifiers = []string{aws.ToString(target.DBInstanceArn)}
	replica.ReadReplicaSourceDBInstanceIdentifier = target.DBInstanceArn
	client := fakeClientFromFixture(fixture)
	for _, instance := range fixture.DBInstances {
		client.instancePages[describePageKey{identifier: aws.ToString(instance.DBInstanceArn)}] = &rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{instance}}
	}
	got, err := DiscoverInstance(context.Background(), client, fixture.TargetIdentifier)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReadReplicaSource.Region != "eu-west-1" || len(got.ReadReplicas) != 1 || got.ReadReplicas[0].Region != "us-west-2" {
		t.Fatalf("wrong related instances: %+v", got)
	}
	if !equalStrings(client.describeInstanceRegions, []string{"", "eu-west-1", "us-west-2"}) {
		t.Fatalf("request regions: %v", client.describeInstanceRegions)
	}
	// An identically named instance in another account must never be accepted.
	wrong := *source
	wrong.DBInstanceArn = aws.String(strings.Replace(aws.ToString(source.DBInstanceArn), "123456789012", "999999999999", 1))
	client.instancePages[describePageKey{identifier: aws.ToString(source.DBInstanceArn)}] = &rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{wrong}}
	if _, err := DiscoverInstance(context.Background(), client, fixture.TargetIdentifier); err == nil {
		t.Fatal("accepted wrong account")
	}
}

func TestDiscoverGlobalAPIFailurePreservesTarget(t *testing.T) {
	fixture := loadDiscoveryFixture(t, "aurora_global_primary.json")
	client := fakeClientFromFixture(fixture)
	client.globalPages = nil
	client.globalErr = errors.New("AccessDenied: rds:DescribeGlobalClusters")
	got, err := DiscoverInstance(context.Background(), client, fixture.TargetIdentifier)
	if err != nil {
		t.Fatal(err)
	}
	if got.Endpoint == "" || got.DBInstanceIdentifier != fixture.TargetIdentifier || len(got.ClusterMembers) != 2 || !got.TopologyIncomplete || got.GlobalClusterIdentifier != "orders-global" {
		t.Fatalf("lost local metadata: %+v", got)
	}
	if len(got.GlobalClusterMembers) != 0 || got.GlobalClusterARN != "" {
		t.Fatal("published unverified global topology")
	}
}

func (f *fakeClient) DescribeDBClusters(_ context.Context, input *rds.DescribeDBClustersInput, _ ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error) {
	identifier := aws.ToString(input.DBClusterIdentifier)
	key := describePageKey{identifier: identifier, marker: aws.ToString(input.Marker)}
	f.describeClusterIDs = append(f.describeClusterIDs, identifier)
	if output, ok := f.clusterPages[key]; ok {
		return output, f.clusterErrs[key]
	}
	return f.clustersOutput, f.clustersErr
}

func (f *fakeClient) DescribeGlobalClusters(_ context.Context, input *rds.DescribeGlobalClustersInput, _ ...func(*rds.Options)) (*rds.DescribeGlobalClustersOutput, error) {
	identifier := aws.ToString(input.GlobalClusterIdentifier)
	key := describePageKey{identifier: identifier, marker: aws.ToString(input.Marker)}
	f.describeGlobalIDs = append(f.describeGlobalIDs, identifier)
	if output, ok := f.globalPages[key]; ok {
		return output, f.globalErrs[key]
	}
	return f.globalOutput, f.globalErr
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

func TestDiscoverInstanceForApply(t *testing.T) {
	t.Parallel()

	const (
		mysqlID     = "orders-mysql"
		writerID    = "orders-reader-labelled"
		readerID    = "analytics-writer-labelled"
		clusterID   = "orders-cluster"
		analyticsID = "analytics-cluster"
	)

	tests := []struct {
		name              string
		identifier        string
		instancesOutput   *rds.DescribeDBInstancesOutput
		instancesErr      error
		clustersOutput    *rds.DescribeDBClustersOutput
		clustersErr       error
		want              Metadata
		wantErrContains   string
		wantInstanceCalls []string
		wantClusterCalls  []string
		wantGlobalCalls   []string
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
				DBClusterMembers: []types.DBClusterMember{{
					DBInstanceIdentifier:          aws.String(writerID),
					DBClusterParameterGroupStatus: aws.String("applying"),
					IsClusterWriter:               aws.Bool(true),
				}},
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
				ClusterMembers: []ClusterMember{{
					DBInstanceIdentifier:          writerID,
					DBInstanceResourceID:          "db-resource-writer",
					DBInstanceClass:               "db.r7g.xlarge",
					Endpoint:                      "writer.example",
					InstanceStatus:                "available",
					IsClusterWriter:               true,
					DBClusterParameterGroupStatus: "applying",
				}},
				IsClusterWriter: true,
				InstanceStatus:  "available",
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
				DBClusterMembers: []types.DBClusterMember{{
					DBInstanceIdentifier:          aws.String(readerID),
					DBClusterParameterGroupStatus: aws.String("in-sync"),
					IsClusterWriter:               aws.Bool(false),
				}},
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
				ClusterMembers: []ClusterMember{{
					DBInstanceIdentifier:          readerID,
					DBInstanceResourceID:          "db-resource-reader",
					DBInstanceClass:               "db.serverless",
					Endpoint:                      "reader.example",
					EndpointPort:                  5433,
					InstanceStatus:                "backing-up",
					IsServerlessV2:                true,
					DBClusterParameterGroupStatus: "in-sync",
				}},
				IsServerlessV2: true,
				InstanceStatus: "backing-up",
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

			got, err := DiscoverInstanceForApply(context.Background(), client, tt.identifier)
			if tt.wantErrContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Fatalf("DiscoverInstanceForApply() error = %v, want containing %q", err, tt.wantErrContains)
				}
			} else {
				if err != nil {
					t.Fatalf("DiscoverInstanceForApply() unexpected error: %v", err)
				}
				if !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("DiscoverInstanceForApply() = %#v, want %#v", got, tt.want)
				}
			}

			wantInstanceCalls := tt.wantInstanceCalls
			if wantInstanceCalls == nil {
				wantInstanceCalls = []string{tt.identifier}
			}
			if !equalStrings(client.describeInstanceIDs, wantInstanceCalls) {
				t.Fatalf("DescribeDBInstances calls = %v, want %v", client.describeInstanceIDs, wantInstanceCalls)
			}
			if !equalStrings(client.describeClusterIDs, tt.wantClusterCalls) {
				t.Fatalf("DescribeDBClusters calls = %v, want %v", client.describeClusterIDs, tt.wantClusterCalls)
			}
			if !equalStrings(client.describeGlobalIDs, tt.wantGlobalCalls) {
				t.Fatalf("DescribeGlobalClusters calls = %v, want %v", client.describeGlobalIDs, tt.wantGlobalCalls)
			}
		})
	}
}

func TestDiscoverInstanceAcceptsCaseVariantIdentifier(t *testing.T) {
	t.Parallel()

	fixture := loadDiscoveryFixture(t, "rds_multi_az.json")
	client := &fakeClient{instancesOutput: &rds.DescribeDBInstancesOutput{
		DBInstances: []types.DBInstance{fixture.DBInstances[0]},
	}}
	identifier := strings.ToUpper(fixture.TargetIdentifier)

	got, err := DiscoverInstance(context.Background(), client, identifier)
	if err != nil {
		t.Fatalf("DiscoverInstance(%q) unexpected error: %v", identifier, err)
	}
	if got.DBInstanceIdentifier != fixture.TargetIdentifier {
		t.Errorf("DiscoverInstance(%q) DBInstanceIdentifier = %q, want %q", identifier, got.DBInstanceIdentifier, fixture.TargetIdentifier)
	}
	if !equalStrings(client.describeInstanceIDs, []string{identifier}) {
		t.Errorf("DiscoverInstance(%q) DescribeDBInstances calls = %v, want original case variant", identifier, client.describeInstanceIDs)
	}
}

func TestDiscoverInstanceAcceptsDBInstanceARN(t *testing.T) {
	t.Parallel()

	fixture := loadDiscoveryFixture(t, "rds_multi_az.json")
	client := &fakeClient{instancesOutput: &rds.DescribeDBInstancesOutput{
		DBInstances: []types.DBInstance{fixture.DBInstances[0]},
	}}
	identifier := aws.ToString(fixture.DBInstances[0].DBInstanceArn)

	got, err := DiscoverInstance(context.Background(), client, identifier)
	if err != nil {
		t.Fatalf("DiscoverInstance(%q) unexpected error: %v", identifier, err)
	}
	if got.DBInstanceIdentifier != fixture.TargetIdentifier {
		t.Errorf("DiscoverInstance(%q) DBInstanceIdentifier = %q, want %q", identifier, got.DBInstanceIdentifier, fixture.TargetIdentifier)
	}
	if !equalStrings(client.describeInstanceIDs, []string{identifier}) {
		t.Errorf("DiscoverInstance(%q) DescribeDBInstances calls = %v, want original ARN", identifier, client.describeInstanceIDs)
	}
}

func TestDiscoverInstanceAuroraRequiresExactlyOneLocalWriter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		writerCount int
	}{
		{name: "no writer", writerCount: 0},
		{name: "two writers", writerCount: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fixture := loadDiscoveryFixture(t, "aurora_global_primary.json")
			for i := range fixture.DBClusters[0].DBClusterMembers {
				fixture.DBClusters[0].DBClusterMembers[i].IsClusterWriter = aws.Bool(i < tt.writerCount)
			}
			client := fakeClientFromFixture(fixture)

			_, err := DiscoverInstance(context.Background(), client, fixture.TargetIdentifier)
			want := fmt.Sprintf("has %d local writers", tt.writerCount)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("DiscoverInstance(%q) error = %v, want containing %q", fixture.TargetIdentifier, err, want)
			}
		})
	}
}

func TestDiscoverInstanceRequiresStableTopologyIdentities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		fixtureName     string
		mutate          func(*discoveryFixture)
		wantErrContains string
	}{
		{
			name:        "target ARN",
			fixtureName: "rds_multi_az.json",
			mutate: func(fixture *discoveryFixture) {
				fixture.DBInstances[0].DBInstanceArn = nil
			},
			wantErrContains: "has no ARN",
		},
		{
			name:        "target resource ID",
			fixtureName: "rds_multi_az.json",
			mutate: func(fixture *discoveryFixture) {
				fixture.DBInstances[0].DbiResourceId = nil
			},
			wantErrContains: "has no resource ID",
		},
		{
			name:        "target region",
			fixtureName: "rds_multi_az.json",
			mutate: func(fixture *discoveryFixture) {
				value := strings.Replace(aws.ToString(fixture.DBInstances[0].DBInstanceArn), ":us-east-1:", "::", 1)
				fixture.DBInstances[0].DBInstanceArn = aws.String(value)
			},
			wantErrContains: "ARN has no region",
		},
		{
			name:        "Aurora member ARN",
			fixtureName: "aurora_global_primary.json",
			mutate: func(fixture *discoveryFixture) {
				fixture.DBInstances[1].DBInstanceArn = nil
			},
			wantErrContains: "has no ARN",
		},
		{
			name:        "Aurora member resource ID",
			fixtureName: "aurora_global_primary.json",
			mutate: func(fixture *discoveryFixture) {
				fixture.DBInstances[1].DbiResourceId = nil
			},
			wantErrContains: "has no resource ID",
		},
		{
			name:        "Aurora member region",
			fixtureName: "aurora_global_primary.json",
			mutate: func(fixture *discoveryFixture) {
				value := strings.Replace(aws.ToString(fixture.DBInstances[1].DBInstanceArn), ":us-east-1:", "::", 1)
				fixture.DBInstances[1].DBInstanceArn = aws.String(value)
			},
			wantErrContains: "ARN has no region",
		},
		{
			name:        "Aurora cluster ARN",
			fixtureName: "aurora_global_primary.json",
			mutate: func(fixture *discoveryFixture) {
				fixture.DBClusters[0].DBClusterArn = nil
			},
			wantErrContains: "has no ARN",
		},
		{
			name:        "Aurora cluster resource ID",
			fixtureName: "aurora_global_primary.json",
			mutate: func(fixture *discoveryFixture) {
				fixture.DBClusters[0].DbClusterResourceId = nil
			},
			wantErrContains: "has no resource ID",
		},
		{
			name:        "global cluster ARN",
			fixtureName: "aurora_global_primary.json",
			mutate: func(fixture *discoveryFixture) {
				fixture.GlobalClusters[0].GlobalClusterArn = nil
			},
			wantErrContains: "has no ARN",
		},
		{
			name:        "global cluster resource ID",
			fixtureName: "aurora_global_primary.json",
			mutate: func(fixture *discoveryFixture) {
				fixture.GlobalClusters[0].GlobalClusterResourceId = nil
			},
			wantErrContains: "has no resource ID",
		},
		{
			name:        "read-replica source ARN",
			fixtureName: "rds_read_replica.json",
			mutate: func(fixture *discoveryFixture) {
				fixture.DBInstances[1].DBInstanceArn = nil
			},
			wantErrContains: "has no ARN",
		},
		{
			name:        "read-replica source resource ID",
			fixtureName: "rds_read_replica.json",
			mutate: func(fixture *discoveryFixture) {
				fixture.DBInstances[1].DbiResourceId = nil
			},
			wantErrContains: "has no resource ID",
		},
		{
			name:        "read-replica source region",
			fixtureName: "rds_read_replica.json",
			mutate: func(fixture *discoveryFixture) {
				value := strings.Replace(aws.ToString(fixture.DBInstances[1].DBInstanceArn), ":us-east-1:", "::", 1)
				fixture.DBInstances[1].DBInstanceArn = aws.String(value)
			},
			wantErrContains: "ARN has no region",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fixture := loadDiscoveryFixture(t, tt.fixtureName)
			tt.mutate(&fixture)
			client := fakeClientFromFixture(fixture)

			_, err := DiscoverInstance(context.Background(), client, fixture.TargetIdentifier)
			if err == nil || !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Fatalf("DiscoverInstance(%q) error = %v, want containing %q", fixture.TargetIdentifier, err, tt.wantErrContains)
			}
		})
	}
}

func TestDiscoverInstanceAuroraProvisionedMembers(t *testing.T) {
	t.Parallel()

	const (
		clusterID = "orders-provisioned"
		readerID  = "orders-provisioned-reader"
		writerID  = "orders-provisioned-writer"
	)
	client := &fakeClient{
		instancePages: map[describePageKey]*rds.DescribeDBInstancesOutput{
			{identifier: readerID}: {DBInstances: []types.DBInstance{{
				DBInstanceIdentifier: aws.String(readerID),
				DBInstanceArn:        aws.String("arn:aws:rds:us-east-1:123456789012:db:orders-provisioned-reader"),
				DbiResourceId:        aws.String("db-reader-resource"),
				DBInstanceClass:      aws.String("db.r7g.large"),
				DBClusterIdentifier:  aws.String(clusterID),
				DBInstanceStatus:     aws.String("available"),
				Engine:               aws.String("aurora-mysql"),
				Endpoint:             &types.Endpoint{Address: aws.String("reader.internal"), Port: aws.Int32(3306)},
			}}},
			{identifier: writerID}: {DBInstances: []types.DBInstance{{
				DBInstanceIdentifier: aws.String(writerID),
				DBInstanceArn:        aws.String("arn:aws:rds:us-east-1:123456789012:db:orders-provisioned-writer"),
				DbiResourceId:        aws.String("db-writer-resource"),
				DBInstanceClass:      aws.String("db.r7g.xlarge"),
				DBClusterIdentifier:  aws.String(clusterID),
				DBInstanceStatus:     aws.String("available"),
				Engine:               aws.String("aurora-mysql"),
				Endpoint:             &types.Endpoint{Address: aws.String("writer.internal"), Port: aws.Int32(3306)},
			}}},
		},
		clusterPages: map[describePageKey]*rds.DescribeDBClustersOutput{
			{identifier: clusterID}: {
				Marker: aws.String("cluster-next"),
			},
			{identifier: clusterID, marker: "cluster-next"}: {DBClusters: []types.DBCluster{{
				DBClusterIdentifier:     aws.String(clusterID),
				DBClusterArn:            aws.String("arn:aws:rds:us-east-1:123456789012:cluster:orders-provisioned"),
				DbClusterResourceId:     aws.String("cluster-resource"),
				DBClusterParameterGroup: aws.String("orders-cluster-pg"),
				Endpoint:                aws.String("cluster-writer.internal"),
				ReaderEndpoint:          aws.String("cluster-reader.internal"),
				EngineMode:              aws.String("provisioned"),
				DBClusterMembers: []types.DBClusterMember{
					{DBInstanceIdentifier: aws.String(writerID), IsClusterWriter: aws.Bool(true), PromotionTier: aws.Int32(0), DBClusterParameterGroupStatus: aws.String("in-sync")},
					{DBInstanceIdentifier: aws.String(readerID), IsClusterWriter: aws.Bool(false), PromotionTier: aws.Int32(2), DBClusterParameterGroupStatus: aws.String("in-sync")},
				},
			}}},
		},
	}

	got, err := DiscoverInstance(context.Background(), client, readerID)
	if err != nil {
		t.Fatalf("DiscoverInstance(%q) unexpected error: %v", readerID, err)
	}
	if got.Region != "us-east-1" || got.Partition != "aws" {
		t.Errorf("DiscoverInstance(%q) region/partition = %q/%q, want us-east-1/aws", readerID, got.Region, got.Partition)
	}
	if got.DBClusterARN != "arn:aws:rds:us-east-1:123456789012:cluster:orders-provisioned" || got.DBClusterResourceID != "cluster-resource" {
		t.Errorf("DiscoverInstance(%q) cluster identity = %q/%q, want fixture ARN/resource ID", readerID, got.DBClusterARN, got.DBClusterResourceID)
	}
	if got.ClusterEndpoint != "cluster-writer.internal" || got.ClusterReaderEndpoint != "cluster-reader.internal" {
		t.Errorf("DiscoverInstance(%q) cluster endpoints = %q/%q, want writer/reader endpoints", readerID, got.ClusterEndpoint, got.ClusterReaderEndpoint)
	}
	if got.PromotionTier != 2 || got.IsClusterWriter {
		t.Errorf("DiscoverInstance(%q) role = writer:%t tier:%d, want writer:false tier:2", readerID, got.IsClusterWriter, got.PromotionTier)
	}
	wantMembers := []ClusterMember{
		{
			DBInstanceIdentifier:          writerID,
			DBInstanceARN:                 "arn:aws:rds:us-east-1:123456789012:db:orders-provisioned-writer",
			DBInstanceResourceID:          "db-writer-resource",
			DBInstanceClass:               "db.r7g.xlarge",
			Endpoint:                      "writer.internal",
			EndpointPort:                  3306,
			InstanceStatus:                "available",
			IsClusterWriter:               true,
			PromotionTier:                 0,
			DBClusterParameterGroupStatus: "in-sync",
		},
		{
			DBInstanceIdentifier:          readerID,
			DBInstanceARN:                 "arn:aws:rds:us-east-1:123456789012:db:orders-provisioned-reader",
			DBInstanceResourceID:          "db-reader-resource",
			DBInstanceClass:               "db.r7g.large",
			Endpoint:                      "reader.internal",
			EndpointPort:                  3306,
			InstanceStatus:                "available",
			IsClusterWriter:               false,
			PromotionTier:                 2,
			DBClusterParameterGroupStatus: "in-sync",
		},
	}
	if !reflect.DeepEqual(got.ClusterMembers, wantMembers) {
		t.Errorf("DiscoverInstance(%q) ClusterMembers = %#v, want %#v", readerID, got.ClusterMembers, wantMembers)
	}
	if !equalStrings(client.describeInstanceIDs, []string{readerID, writerID}) {
		t.Errorf("DiscoverInstance(%q) DescribeDBInstances calls = %v, want target and writer", readerID, client.describeInstanceIDs)
	}
	if !equalStrings(client.describeClusterIDs, []string{clusterID, clusterID}) {
		t.Errorf("DiscoverInstance(%q) DescribeDBClusters calls = %v, want both pages", readerID, client.describeClusterIDs)
	}
}

func TestDescribeDBInstancePagesFollowsPagination(t *testing.T) {
	t.Parallel()

	client := &fakeClient{instancePages: map[describePageKey]*rds.DescribeDBInstancesOutput{
		{identifier: "orders"}: {
			Marker: aws.String("instance-next"),
		},
		{identifier: "orders", marker: "instance-next"}: {
			DBInstances: []types.DBInstance{{DBInstanceIdentifier: aws.String("orders")}},
		},
	}}

	instances, err := describeDBInstancePages(context.Background(), client, "orders")
	if err != nil {
		t.Fatalf("describeDBInstancePages() error = %v", err)
	}
	if len(instances) != 1 || aws.ToString(instances[0].DBInstanceIdentifier) != "orders" {
		t.Fatalf("describeDBInstancePages() = %#v, want paginated orders instance", instances)
	}
	if !equalStrings(client.describeInstanceIDs, []string{"orders", "orders"}) {
		t.Fatalf("DescribeDBInstances calls = %v, want both pages", client.describeInstanceIDs)
	}
}

func TestDescribeGlobalClusterPagesFollowsPagination(t *testing.T) {
	t.Parallel()

	client := &fakeClient{globalPages: map[describePageKey]*rds.DescribeGlobalClustersOutput{
		{identifier: "orders-global"}: {
			Marker: aws.String("global-next"),
		},
		{identifier: "orders-global", marker: "global-next"}: {
			GlobalClusters: []types.GlobalCluster{{GlobalClusterIdentifier: aws.String("orders-global")}},
		},
	}}

	clusters, err := describeGlobalClusterPages(context.Background(), client, "orders-global")
	if err != nil {
		t.Fatalf("describeGlobalClusterPages() error = %v", err)
	}
	if len(clusters) != 1 || aws.ToString(clusters[0].GlobalClusterIdentifier) != "orders-global" {
		t.Fatalf("describeGlobalClusterPages() = %#v, want paginated orders-global cluster", clusters)
	}
	if !equalStrings(client.describeGlobalIDs, []string{"orders-global", "orders-global"}) {
		t.Fatalf("DescribeGlobalClusters calls = %v, want both pages", client.describeGlobalIDs)
	}
}

func TestDescribeTopologyPagesRejectRepeatedMarkers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(*fakeClient) error
	}{
		{
			name: "DB instance",
			run: func(client *fakeClient) error {
				client.instancePages = map[describePageKey]*rds.DescribeDBInstancesOutput{
					{identifier: "orders"}:                   {Marker: aws.String("repeat")},
					{identifier: "orders", marker: "repeat"}: {Marker: aws.String("repeat")},
				}
				_, err := describeDBInstancePages(context.Background(), client, "orders")
				return err
			},
		},
		{
			name: "DB cluster",
			run: func(client *fakeClient) error {
				client.clusterPages = map[describePageKey]*rds.DescribeDBClustersOutput{
					{identifier: "orders"}:                   {Marker: aws.String("repeat")},
					{identifier: "orders", marker: "repeat"}: {Marker: aws.String("repeat")},
				}
				_, err := describeDBClusterPages(context.Background(), client, "orders")
				return err
			},
		},
		{
			name: "global cluster",
			run: func(client *fakeClient) error {
				client.globalPages = map[describePageKey]*rds.DescribeGlobalClustersOutput{
					{identifier: "orders-global"}:                   {Marker: aws.String("repeat")},
					{identifier: "orders-global", marker: "repeat"}: {Marker: aws.String("repeat")},
				}
				_, err := describeGlobalClusterPages(context.Background(), client, "orders-global")
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.run(&fakeClient{})
			if err == nil || !strings.Contains(err.Error(), `repeated marker "repeat"`) {
				t.Fatalf("describe pages error = %v, want repeated-marker rejection", err)
			}
		})
	}
}

func TestDiscoverInstanceAuroraServerlessV2(t *testing.T) {
	t.Parallel()

	const (
		clusterID  = "orders-serverless"
		instanceID = "orders-serverless-writer"
	)
	client := &fakeClient{
		instancePages: map[describePageKey]*rds.DescribeDBInstancesOutput{
			{identifier: instanceID}: {DBInstances: []types.DBInstance{{
				DBInstanceIdentifier: aws.String(instanceID),
				DBInstanceArn:        aws.String("arn:aws:rds:us-east-1:123456789012:db:orders-serverless-writer"),
				DbiResourceId:        aws.String("db-orders-serverless-writer"),
				DBInstanceClass:      aws.String("db.serverless"),
				DBClusterIdentifier:  aws.String(clusterID),
				DBInstanceStatus:     aws.String("available"),
				Engine:               aws.String("aurora-mysql"),
				Endpoint:             &types.Endpoint{Address: aws.String("serverless-writer.internal"), Port: aws.Int32(3306)},
			}}},
		},
		clusterPages: map[describePageKey]*rds.DescribeDBClustersOutput{
			{identifier: clusterID}: {DBClusters: []types.DBCluster{{
				DBClusterIdentifier: aws.String(clusterID),
				DBClusterArn:        aws.String("arn:aws:rds:us-east-1:123456789012:cluster:orders-serverless"),
				DbClusterResourceId: aws.String("cluster-orders-serverless"),
				EngineMode:          aws.String("provisioned"),
				ServerlessV2ScalingConfiguration: &types.ServerlessV2ScalingConfigurationInfo{
					MinCapacity:           aws.Float64(0),
					MaxCapacity:           aws.Float64(16),
					SecondsUntilAutoPause: aws.Int32(600),
				},
				DBClusterMembers: []types.DBClusterMember{{
					DBInstanceIdentifier: aws.String(instanceID),
					IsClusterWriter:      aws.Bool(true),
					PromotionTier:        aws.Int32(0),
				}},
			}}},
		},
	}

	got, err := DiscoverInstance(context.Background(), client, instanceID)
	if err != nil {
		t.Fatalf("DiscoverInstance(%q) unexpected error: %v", instanceID, err)
	}
	wantScaling := ServerlessV2ScalingConfiguration{MinCapacity: 0, MaxCapacity: 16, SecondsUntilAutoPause: 600}
	if !got.IsServerlessV2 || !got.HasServerlessV2ScalingConfiguration || got.ServerlessV2ScalingConfiguration != wantScaling {
		t.Errorf("DiscoverInstance(%q) Serverless v2 = member:%t configured:%t scaling:%#v, want true/true/%#v", instanceID, got.IsServerlessV2, got.HasServerlessV2ScalingConfiguration, got.ServerlessV2ScalingConfiguration, wantScaling)
	}
	if got.EngineMode != "provisioned" {
		t.Errorf("DiscoverInstance(%q) EngineMode = %q, want provisioned for Serverless v2", instanceID, got.EngineMode)
	}
	if len(got.ClusterMembers) != 1 || !got.ClusterMembers[0].IsServerlessV2 {
		t.Errorf("DiscoverInstance(%q) ClusterMembers = %#v, want one Serverless v2 member", instanceID, got.ClusterMembers)
	}
}

func TestDiscoverInstanceAuroraGlobalPrimary(t *testing.T) {
	t.Parallel()

	fixture := loadDiscoveryFixture(t, "aurora_global_primary.json")
	client := fakeClientFromFixture(fixture)
	got, err := DiscoverInstance(context.Background(), client, fixture.TargetIdentifier)
	if err != nil {
		t.Fatalf("DiscoverInstance(%q) unexpected error: %v", fixture.TargetIdentifier, err)
	}

	assertGlobalMetadata(t, fixture.TargetIdentifier, got, "us-east-1", "arn:aws:rds:us-east-1:123456789012:cluster:global-primary", true)
	if !equalStrings(client.describeGlobalIDs, []string{"orders-global"}) {
		t.Errorf("DiscoverInstance(%q) DescribeGlobalClusters calls = %v, want [orders-global]", fixture.TargetIdentifier, client.describeGlobalIDs)
	}
}

func TestDiscoverInstanceAuroraGlobalSecondary(t *testing.T) {
	t.Parallel()

	fixture := loadDiscoveryFixture(t, "aurora_global_secondary.json")
	client := fakeClientFromFixture(fixture)
	got, err := DiscoverInstance(context.Background(), client, fixture.TargetIdentifier)
	if err != nil {
		t.Fatalf("DiscoverInstance(%q) unexpected error: %v", fixture.TargetIdentifier, err)
	}

	assertGlobalMetadata(t, fixture.TargetIdentifier, got, "us-west-2", "arn:aws:rds:us-east-1:123456789012:cluster:global-primary", false)
}

func TestDiscoverInstanceRejectsGlobalMemberWithoutARNRegion(t *testing.T) {
	t.Parallel()

	fixture := loadDiscoveryFixture(t, "aurora_global_primary.json")
	fixture.GlobalClusters[0].GlobalClusterMembers[1].DBClusterArn = aws.String("arn:aws:rds::123456789012:cluster:global-secondary")
	fixture.GlobalClusters[0].GlobalClusterMembers[0].Readers = []string{"arn:aws:rds::123456789012:cluster:global-secondary"}
	client := fakeClientFromFixture(fixture)

	_, err := DiscoverInstance(context.Background(), client, fixture.TargetIdentifier)
	if err == nil || !strings.Contains(err.Error(), "cluster ARN has no region") {
		t.Fatalf("DiscoverInstance(%q) error = %v, want missing cluster ARN region", fixture.TargetIdentifier, err)
	}
}

func TestDiscoverInstanceRDSReadReplica(t *testing.T) {
	t.Parallel()

	fixture := loadDiscoveryFixture(t, "rds_read_replica.json")
	client := fakeClientFromFixture(fixture)
	got, err := DiscoverInstance(context.Background(), client, fixture.TargetIdentifier)
	if err != nil {
		t.Fatalf("DiscoverInstance(%q) unexpected error: %v", fixture.TargetIdentifier, err)
	}

	if got.ReadReplicaSourceDBInstanceIdentifier != "orders-primary" || !got.HasReadReplicaSource {
		t.Errorf("DiscoverInstance(%q) source = %q present:%t, want orders-primary/true", fixture.TargetIdentifier, got.ReadReplicaSourceDBInstanceIdentifier, got.HasReadReplicaSource)
	}
	wantSource := RelatedDBInstance{
		DBInstanceIdentifier: "orders-primary",
		DBInstanceARN:        "arn:aws:rds:us-east-1:123456789012:db:orders-primary",
		DBInstanceResourceID: "db-primary-resource",
		Endpoint:             "orders-primary.internal",
		EndpointPort:         3306,
		Engine:               "mysql",
		InstanceStatus:       "available",
		Partition:            "aws",
		Region:               "us-east-1",
		MultiAZ:              true,
	}
	if got.ReadReplicaSource != wantSource {
		t.Errorf("DiscoverInstance(%q) ReadReplicaSource = %#v, want %#v", fixture.TargetIdentifier, got.ReadReplicaSource, wantSource)
	}
	wantReplicas := []RelatedDBInstance{{
		DBInstanceIdentifier: "orders-replica-2",
		DBInstanceARN:        "arn:aws:rds:us-east-1:123456789012:db:orders-replica-2",
		DBInstanceResourceID: "db-replica-2-resource",
		Endpoint:             "orders-replica-2.internal",
		EndpointPort:         3306,
		Engine:               "mysql",
		InstanceStatus:       "available",
		Partition:            "aws",
		Region:               "us-east-1",
	}}
	if !reflect.DeepEqual(got.ReadReplicas, wantReplicas) {
		t.Errorf("DiscoverInstance(%q) ReadReplicas = %#v, want %#v", fixture.TargetIdentifier, got.ReadReplicas, wantReplicas)
	}
	if !equalStrings(got.ReadReplicaDBInstanceIdentifiers, []string{"orders-replica-2"}) {
		t.Errorf("DiscoverInstance(%q) ReadReplicaDBInstanceIdentifiers = %v, want [orders-replica-2]", fixture.TargetIdentifier, got.ReadReplicaDBInstanceIdentifiers)
	}
	if !equalStrings(client.describeInstanceIDs, []string{fixture.TargetIdentifier, "orders-primary", "orders-replica-2"}) {
		t.Errorf("DiscoverInstance(%q) DescribeDBInstances calls = %v, want target/source/child", fixture.TargetIdentifier, client.describeInstanceIDs)
	}
	if len(client.describeClusterIDs) != 0 {
		t.Errorf("DiscoverInstance(%q) DescribeDBClusters calls = %v, want none for ordinary RDS", fixture.TargetIdentifier, client.describeClusterIDs)
	}
}

func TestDiscoverInstanceRDSMultiAZ(t *testing.T) {
	t.Parallel()

	fixture := loadDiscoveryFixture(t, "rds_multi_az.json")
	client := fakeClientFromFixture(fixture)
	got, err := DiscoverInstance(context.Background(), client, fixture.TargetIdentifier)
	if err != nil {
		t.Fatalf("DiscoverInstance(%q) unexpected error: %v", fixture.TargetIdentifier, err)
	}
	if !got.MultiAZ {
		t.Errorf("DiscoverInstance(%q) MultiAZ = false, want true", fixture.TargetIdentifier)
	}
	if got.Region != "us-east-1" {
		t.Errorf("DiscoverInstance(%q) Region = %q, want us-east-1 from ARN", fixture.TargetIdentifier, got.Region)
	}
	if len(got.ClusterMembers) != 0 || got.HasReadReplicaSource || len(got.ReadReplicas) != 0 {
		t.Errorf("DiscoverInstance(%q) optional relations = cluster:%v source:%t replicas:%v, want absent", fixture.TargetIdentifier, got.ClusterMembers, got.HasReadReplicaSource, got.ReadReplicas)
	}
	if len(client.describeClusterIDs) != 0 || len(client.describeGlobalIDs) != 0 {
		t.Errorf("DiscoverInstance(%q) cluster/global calls = %v/%v, want none", fixture.TargetIdentifier, client.describeClusterIDs, client.describeGlobalIDs)
	}
}

type discoveryFixture struct {
	TargetIdentifier string
	DBInstances      []types.DBInstance
	DBClusters       []types.DBCluster
	GlobalClusters   []types.GlobalCluster
}

func loadDiscoveryFixture(t *testing.T, name string) discoveryFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read discovery fixture %q: %v", name, err)
	}
	var fixture discoveryFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode discovery fixture %q: %v", name, err)
	}
	return fixture
}

func fakeClientFromFixture(fixture discoveryFixture) *fakeClient {
	client := &fakeClient{
		instancePages: make(map[describePageKey]*rds.DescribeDBInstancesOutput, len(fixture.DBInstances)),
		clusterPages:  make(map[describePageKey]*rds.DescribeDBClustersOutput, len(fixture.DBClusters)),
		globalPages:   make(map[describePageKey]*rds.DescribeGlobalClustersOutput, len(fixture.GlobalClusters)),
	}
	for _, instance := range fixture.DBInstances {
		identifier := aws.ToString(instance.DBInstanceIdentifier)
		client.instancePages[describePageKey{identifier: identifier}] = &rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{instance}}
	}
	for _, cluster := range fixture.DBClusters {
		identifier := aws.ToString(cluster.DBClusterIdentifier)
		client.clusterPages[describePageKey{identifier: identifier}] = &rds.DescribeDBClustersOutput{DBClusters: []types.DBCluster{cluster}}
	}
	for _, cluster := range fixture.GlobalClusters {
		identifier := aws.ToString(cluster.GlobalClusterIdentifier)
		client.globalPages[describePageKey{identifier: identifier}] = &rds.DescribeGlobalClustersOutput{GlobalClusters: []types.GlobalCluster{cluster}}
	}
	return client
}

func assertGlobalMetadata(t *testing.T, identifier string, got Metadata, wantRegion, wantPrimaryARN string, wantLocalWriter bool) {
	t.Helper()
	if got.Region != wantRegion {
		t.Errorf("DiscoverInstance(%q) Region = %q, want %q", identifier, got.Region, wantRegion)
	}
	if got.GlobalClusterIdentifier != "orders-global" || got.GlobalClusterARN != "arn:aws:rds::123456789012:global-cluster:orders-global" || got.GlobalClusterResourceID != "cluster-global-resource" {
		t.Errorf("DiscoverInstance(%q) global identity = %q/%q/%q, want fixture identifier/ARN/resource ID", identifier, got.GlobalClusterIdentifier, got.GlobalClusterARN, got.GlobalClusterResourceID)
	}
	if got.GlobalClusterPrimaryDBClusterARN != wantPrimaryARN || got.GlobalClusterPrimaryRegion != "us-east-1" {
		t.Errorf("DiscoverInstance(%q) global primary = %q/%q, want %q/us-east-1", identifier, got.GlobalClusterPrimaryDBClusterARN, got.GlobalClusterPrimaryRegion, wantPrimaryARN)
	}
	wantMembers := []GlobalClusterMember{
		{
			DBClusterIdentifier:         "global-primary",
			DBClusterARN:                "arn:aws:rds:us-east-1:123456789012:cluster:global-primary",
			Region:                      "us-east-1",
			IsWriter:                    true,
			SynchronizationStatus:       "connected",
			GlobalWriteForwardingStatus: "enabled",
		},
		{
			DBClusterIdentifier:         "global-secondary",
			DBClusterARN:                "arn:aws:rds:us-west-2:123456789012:cluster:global-secondary",
			Region:                      "us-west-2",
			IsWriter:                    false,
			SynchronizationStatus:       "connected",
			GlobalWriteForwardingStatus: "disabled",
		},
	}
	if !reflect.DeepEqual(got.GlobalClusterMembers, wantMembers) {
		t.Errorf("DiscoverInstance(%q) GlobalClusterMembers = %#v, want %#v", identifier, got.GlobalClusterMembers, wantMembers)
	}
	localWriter := false
	for _, member := range got.GlobalClusterMembers {
		if member.DBClusterARN == got.DBClusterARN {
			localWriter = member.IsWriter
		}
	}
	if localWriter != wantLocalWriter {
		t.Errorf("DiscoverInstance(%q) local global writer = %t, want %t", identifier, localWriter, wantLocalWriter)
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
