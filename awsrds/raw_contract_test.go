package awsrds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	logging "github.com/google/logger"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAWSRawSharedFixtures(t *testing.T) {
	for _, name := range []string{"aurora_global_primary.json", "aurora_global_secondary.json", "aurora_serverless_v2.json", "rds_read_replica.json", "rds_multi_az.json"} {
		t.Run(name, func(t *testing.T) {
			source := name
			if name == "aurora_serverless_v2.json" {
				source = "aurora_global_primary.json"
			}
			fixture := loadDiscoveryFixture(t, source)
			if name == "aurora_serverless_v2.json" {
				fixture.DBInstances[0].DBInstanceClass = aws.String("db.serverless")
				fixture.DBClusters[0].GlobalClusterIdentifier = nil
				fixture.DBClusters[0].ServerlessV2ScalingConfiguration = &types.ServerlessV2ScalingConfigurationInfo{MinCapacity: aws.Float64(0), MaxCapacity: aws.Float64(16), SecondsUntilAutoPause: aws.Int32(900)}
			}
			got, err := DiscoverInstance(context.Background(), fakeClientFromFixture(fixture), fixture.TargetIdentifier)
			if err != nil {
				t.Fatal(err)
			}
			if got.TopologyIncomplete {
				t.Fatal("complete fixture marked partial")
			}
			encoded, err := json.Marshal(got.TopologyFacts)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"GroupKey", "PrimaryMemberKey", "Password", "MasterUsername", "TagList", "GlobalClusterPrimary", "IsReader", "Role"} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("forbidden field %s", forbidden)
				}
			}
			if os.Getenv("PRINT_AWS_FACTS") == "1" {
				fmt.Printf("AWS_FIXTURE %s %s\n", name, encoded)
				return
			}
			want, err := os.ReadFile(filepath.Join("testdata", "facts", name))
			if err != nil {
				t.Fatal(err)
			}
			var actual, expected any
			if json.Unmarshal(encoded, &actual) != nil || json.Unmarshal(want, &expected) != nil || !reflect.DeepEqual(actual, expected) {
				t.Fatal("wire fixture drift")
			}
		})
	}
}

func TestAWSOptionalPermissionFailures(t *testing.T) {
	for _, source := range []string{"Cluster", "GlobalCluster", "Peers", "Source", "Children"} {
		t.Run(source, func(t *testing.T) {
			name := "aurora_global_primary.json"
			if source == "Source" || source == "Children" {
				name = "rds_read_replica.json"
			}
			fixture := loadDiscoveryFixture(t, name)
			client := fakeClientFromFixture(fixture)
			denied := errors.New("AccessDenied: synthetic secret must not be on wire")
			switch source {
			case "Cluster":
				client.clusterPages = nil
				client.clustersErr = denied
			case "GlobalCluster":
				client.globalPages = nil
				client.globalErr = denied
			default:
				index := 1
				if source == "Children" {
					index = 2
				}
				key := describePageKey{identifier: aws.ToString(fixture.DBInstances[index].DBInstanceIdentifier)}
				client.instanceErrs = map[describePageKey]error{key: denied}
			}
			got, err := DiscoverInstance(context.Background(), client, fixture.TargetIdentifier)
			if err != nil {
				t.Fatal(err)
			}
			if got.Endpoint == "" || !got.TopologyIncomplete || got.TopologyFacts.Sources[source] != "error" || got.TopologyFacts.Sources["Target"] != "ok" {
				t.Fatal("partial source poisoned target or completion")
			}
			if source == "Source" || source == "Children" {
				if len(got.TopologyFacts.Instances) != 1 {
					t.Fatal("lost successful sibling lookup")
				}
			}
		})
	}
}

func TestAWSReportFactsAreImmutableAndPreserveNative(t *testing.T) {
	fixture := loadDiscoveryFixture(t, "aurora_global_secondary.json")
	metadata, err := DiscoverInstance(context.Background(), fakeClientFromFixture(fixture), fixture.TargetIdentifier)
	if err != nil {
		t.Fatal(err)
	}
	first, second := &models.Metrics{}, &models.Metrics{}
	first.DB.TopologyFacts = models.MetricGroupValue{"Version": 1, "ReplicaStatus": "sentinel"}
	AttachReportMetadata(first, metadata, true)
	metadata.TopologyFacts.GlobalCluster.GlobalClusterMembers[0].Readers[0] = "changed"
	metadata.TopologyFacts.Sources["GlobalCluster"] = "error"
	AttachReportMetadata(second, metadata, false)
	gatherer := NewTopologyRelationsGatherer(*logging.Init("facts-test", false, false, io.Discard))
	_ = gatherer.GetMetrics(second)
	_ = gatherer.GetMetrics(first)
	if first.DB.TopologyFacts["ReplicaStatus"] != "sentinel" {
		t.Fatal("overwrote native facts")
	}
	if first.DB.TopologyFacts["AWS"].(*AWSFacts).Sources["GlobalCluster"] != "ok" || second.DB.TopologyFacts["AWS"].(*AWSFacts).Sources["GlobalCluster"] != "error" {
		t.Fatal("cross-report contamination")
	}
	if first.DB.Topology != nil || second.DB.Topology != nil {
		t.Fatal("Agent constructed semantic topology")
	}
}
