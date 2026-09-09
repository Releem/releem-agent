package awsrds

import (
	"context"
	"encoding/json"
	"errors"
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
	describeClusterRegions  []string
	describeGlobalIDs       []string
	describeGlobalRegions   []string
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
	if len(got.TopologyFacts.Instances) != 2 || !strings.Contains(got.TopologyFacts.Instances[0].DBInstanceArn, ":eu-west-1:") || !strings.Contains(got.TopologyFacts.Instances[1].DBInstanceArn, ":us-west-2:") {
		t.Fatalf("wrong related instances: %+v", got)
	}
	if !equalStrings(client.describeInstanceRegions, []string{"", "eu-west-1", "us-west-2"}) {
		t.Fatalf("request regions: %v", client.describeInstanceRegions)
	}
	// An identically named instance in another account must never be accepted.
	wrong := *source
	wrong.DBInstanceArn = aws.String(strings.Replace(aws.ToString(source.DBInstanceArn), "123456789012", "999999999999", 1))
	client.instancePages[describePageKey{identifier: aws.ToString(source.DBInstanceArn)}] = &rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{wrong}}
	if got, err := DiscoverInstance(context.Background(), client, fixture.TargetIdentifier); err != nil || got.TopologyFacts.Sources["Source"] != "error" {
		t.Fatal("wrong-account optional source was accepted or target monitoring failed")
	}
}

func TestARNTargetRegionPropagatesToBareRDSRelations(t *testing.T) {
	fixture := loadDiscoveryFixture(t, "rds_read_replica.json")
	target := fixture.DBInstances[0]
	targetARN := strings.Replace(aws.ToString(target.DBInstanceArn), "us-east-1", "us-west-2", 1)
	target.DBInstanceArn = aws.String(targetARN)
	for i := 1; i < len(fixture.DBInstances); i++ {
		fixture.DBInstances[i].DBInstanceArn = aws.String(strings.Replace(aws.ToString(fixture.DBInstances[i].DBInstanceArn), "us-east-1", "us-west-2", 1))
	}
	client := fakeClientFromFixture(fixture)
	client.instancePages[describePageKey{identifier: targetARN}] = &rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{target}}

	got, err := DiscoverInstance(context.Background(), client, targetARN)
	if err != nil {
		t.Fatal(err)
	}
	if got.Region != "us-west-2" || !equalStrings(client.describeInstanceRegions, []string{"us-west-2", "us-west-2", "us-west-2"}) {
		t.Fatalf("metadata region %q, request regions %v", got.Region, client.describeInstanceRegions)
	}
}

func TestARNTargetRegionPropagatesToBareAuroraRelations(t *testing.T) {
	fixture := loadDiscoveryFixture(t, "aurora_global_primary.json")
	target := fixture.DBInstances[0]
	targetARN := strings.Replace(aws.ToString(target.DBInstanceArn), "us-east-1", "us-west-2", 1)
	target.DBInstanceArn = aws.String(targetARN)
	for i := range fixture.DBInstances {
		fixture.DBInstances[i].DBInstanceArn = aws.String(strings.Replace(aws.ToString(fixture.DBInstances[i].DBInstanceArn), "us-east-1", "us-west-2", 1))
	}
	fixture.DBInstances[0] = target
	fixture.DBClusters[0].DBClusterArn = aws.String(strings.Replace(aws.ToString(fixture.DBClusters[0].DBClusterArn), "us-east-1", "us-west-2", 1))
	for i := range fixture.GlobalClusters[0].GlobalClusterMembers {
		if strings.Contains(aws.ToString(fixture.GlobalClusters[0].GlobalClusterMembers[i].DBClusterArn), "global-primary") {
			fixture.GlobalClusters[0].GlobalClusterMembers[i].DBClusterArn = fixture.DBClusters[0].DBClusterArn
		}
	}
	client := fakeClientFromFixture(fixture)
	client.instancePages[describePageKey{identifier: targetARN}] = &rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{target}}

	got, err := DiscoverInstance(context.Background(), client, targetARN)
	if err != nil {
		t.Fatal(err)
	}
	if got.Region != "us-west-2" {
		t.Fatalf("metadata region = %q", got.Region)
	}
	for _, region := range client.describeInstanceRegions {
		if region != "us-west-2" {
			t.Fatalf("instance request regions = %v", client.describeInstanceRegions)
		}
	}
	if !equalStrings(client.describeClusterRegions, []string{"us-west-2"}) || !equalStrings(client.describeGlobalRegions, []string{"us-west-2"}) {
		t.Fatalf("cluster/global request regions = %v/%v", client.describeClusterRegions, client.describeGlobalRegions)
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
	if got.Endpoint == "" || got.DBInstanceIdentifier != fixture.TargetIdentifier || len(got.TopologyFacts.Cluster.DBClusterMembers) != 2 || !got.TopologyIncomplete || got.GlobalClusterIdentifier != "orders-global" {
		t.Fatalf("lost local metadata: %+v", got)
	}
	if len(got.GlobalClusterMembers) != 0 || got.GlobalClusterARN != "" {
		t.Fatal("published unverified global topology")
	}
}

func (f *fakeClient) DescribeDBClusters(_ context.Context, input *rds.DescribeDBClustersInput, options ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error) {
	settings := rds.Options{}
	for _, option := range options {
		option(&settings)
	}
	f.describeClusterRegions = append(f.describeClusterRegions, settings.Region)
	identifier := aws.ToString(input.DBClusterIdentifier)
	key := describePageKey{identifier: identifier, marker: aws.ToString(input.Marker)}
	f.describeClusterIDs = append(f.describeClusterIDs, identifier)
	if output, ok := f.clusterPages[key]; ok {
		return output, f.clusterErrs[key]
	}
	return f.clustersOutput, f.clustersErr
}

func (f *fakeClient) DescribeGlobalClusters(_ context.Context, input *rds.DescribeGlobalClustersInput, options ...func(*rds.Options)) (*rds.DescribeGlobalClustersOutput, error) {
	settings := rds.Options{}
	for _, option := range options {
		option(&settings)
	}
	f.describeGlobalRegions = append(f.describeGlobalRegions, settings.Region)
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
