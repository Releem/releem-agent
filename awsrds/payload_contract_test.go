package awsrds_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Releem/mysqlconfigurer/awsrds"
	"github.com/Releem/mysqlconfigurer/config"
	metricspkg "github.com/Releem/mysqlconfigurer/metrics"
	"github.com/Releem/mysqlconfigurer/metrics/system"
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logging "github.com/google/logger"
)

type contractPayload struct {
	System struct {
		Info struct {
			Host map[string]any
		}
	}
	DB struct {
		Conf struct {
			Variables map[string]any
		}
	}
	ReleemAgent struct {
		Conf map[string]any
	}
}

type contractCase struct {
	metadata awsrds.Metadata
	config   config.Config
	capacity float64
}

func TestAuroraPayloadContract(t *testing.T) {
	cases := map[string]contractCase{
		"aurora_mysql_provisioned_writer.json":      newContractCase("orders-writer", "db-resource-mysql-writer", "db.r7g.large", "aurora-mysql", "orders-instance-pg", "orders-cluster", "orders-cluster-pg", true, 0),
		"aurora_mysql_provisioned_reader.json":      newContractCase("orders-reader", "db-resource-mysql-reader", "db.r7g.large", "aurora-mysql", "orders-instance-pg", "orders-cluster", "orders-cluster-pg", false, 0),
		"aurora_mysql_serverless_writer.json":       newContractCase("orders-serverless-writer", "db-resource-mysql-serverless-writer", "db.serverless", "aurora-mysql", "orders-serverless-instance-pg", "orders-serverless-cluster", "orders-serverless-cluster-pg", true, 8),
		"aurora_mysql_serverless_reader.json":       newContractCase("orders-serverless-reader", "db-resource-mysql-serverless-reader", "db.serverless", "aurora-mysql", "orders-serverless-instance-pg", "orders-serverless-cluster", "orders-serverless-cluster-pg", false, 4),
		"aurora_postgresql_provisioned_writer.json": newContractCase("analytics-writer", "db-resource-pg-writer", "db.r7g.large", "aurora-postgresql", "analytics-instance-pg", "analytics-cluster", "analytics-cluster-pg", true, 0),
		"aurora_postgresql_provisioned_reader.json": newContractCase("analytics-reader", "db-resource-pg-reader", "db.r7g.large", "aurora-postgresql", "analytics-instance-pg", "analytics-cluster", "analytics-cluster-pg", false, 0),
		"aurora_postgresql_serverless_writer.json":  newContractCase("analytics-serverless-writer", "db-resource-pg-serverless-writer", "db.serverless", "aurora-postgresql", "analytics-serverless-instance-pg", "analytics-serverless-cluster", "analytics-serverless-cluster-pg", true, 8),
		"aurora_postgresql_serverless_reader.json":  newContractCase("analytics-serverless-reader", "db-resource-pg-serverless-reader", "db.serverless", "aurora-postgresql", "analytics-serverless-instance-pg", "analytics-serverless-cluster", "analytics-serverless-cluster-pg", false, 4),
	}

	wantNames := make([]string, 0, len(cases)+2)
	for name := range cases {
		wantNames = append(wantNames, name)
	}
	wantNames = append(wantNames, "legacy_aurora_mysql.json", "legacy_aurora_postgresql.json")
	sort.Strings(wantNames)

	entries, err := os.ReadDir(filepath.Join("testdata", "payloads"))
	if err != nil {
		t.Fatalf("read payload fixture directory: %v", err)
	}
	gotNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			gotNames = append(gotNames, entry.Name())
		}
	}
	sort.Strings(gotNames)
	if fmt.Sprint(gotNames) != fmt.Sprint(wantNames) {
		t.Fatalf("payload fixtures = %v, want exactly %v", gotNames, wantNames)
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			payload := readContractPayload(t, name)
			assertSecretFree(t, payload)
			assertExactKeys(t, "Host", payload.System.Info.Host, []string{
				"DBClusterIdentifier",
				"DBClusterParameterGroup",
				"DBInstanceClass",
				"DBInstanceIdentifier",
				"DBInstanceResourceID",
				"DBParameterGroup",
				"Engine",
				"EngineMode",
				"IsClusterWriter",
				"ServerlessDatabaseCapacity",
			})
			assertExactKeys(t, "ReleemAgent.Conf", payload.ReleemAgent.Conf, []string{
				"AwsRDSClusterParameterGroup",
				"AwsRDSDB",
				"AwsRDSParameterGroup",
			})

			emittedHost, emittedConf := emitContractFields(t, tc)
			assertFieldsEqual(t, "Host", payload.System.Info.Host, emittedHost)
			assertFieldsEqual(t, "ReleemAgent.Conf", payload.ReleemAgent.Conf, emittedConf)
		})
	}

	for _, name := range []string{"legacy_aurora_mysql.json", "legacy_aurora_postgresql.json"} {
		t.Run(name, func(t *testing.T) {
			payload := readContractPayload(t, name)
			assertSecretFree(t, payload)
			if len(payload.System.Info.Host) != 0 {
				t.Fatalf("legacy Host = %v, want no additive Aurora fields", payload.System.Info.Host)
			}
			assertExactKeys(t, "legacy ReleemAgent.Conf", payload.ReleemAgent.Conf, []string{"AwsRDSParameterGroup"})
			if _, ok := payload.DB.Conf.Variables["aurora_version"]; !ok {
				t.Fatal("legacy DB.Conf.Variables must contain aurora_version")
			}
		})
	}
}

func newContractCase(identifier, resourceID, instanceClass, engine, instanceGroup, clusterID, clusterGroup string, writer bool, capacity float64) contractCase {
	return contractCase{
		metadata: awsrds.Metadata{
			DBInstanceIdentifier:    identifier,
			DBInstanceResourceID:    resourceID,
			DBInstanceClass:         instanceClass,
			Engine:                  engine,
			EngineMode:              "provisioned",
			DBParameterGroup:        instanceGroup,
			DBClusterIdentifier:     clusterID,
			DBClusterParameterGroup: clusterGroup,
			IsClusterWriter:         writer,
		},
		config: config.Config{
			AwsRDSDB:                    identifier,
			AwsRDSParameterGroup:        instanceGroup,
			AwsRDSClusterParameterGroup: clusterGroup,
		},
		capacity: capacity,
	}
}

func readContractPayload(t *testing.T, name string) contractPayload {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "payloads", name))
	if err != nil {
		t.Fatalf("read payload fixture %q: %v", name, err)
	}
	var payload contractPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode payload fixture %q: %v", name, err)
	}
	return payload
}

func emitContractFields(t *testing.T, tc contractCase) (map[string]any, map[string]any) {
	t.Helper()
	logger := *logging.Init("aurora-payload-contract-test", false, false, io.Discard)
	metrics := &models.Metrics{}
	client := contractCloudWatchClient(t, tc.capacity)
	if err := system.NewAWSRDSEnhancedMetricsGatherer(logger, client, &tc.config, func(context.Context) (awsrds.Metadata, error) {
		return tc.metadata, nil
	}).GetMetrics(metrics); err != nil {
		t.Fatalf("emit Host metrics: %v", err)
	}
	if err := metricspkg.NewAgentMetricsGatherer(logger, &tc.config).GetMetrics(metrics); err != nil {
		t.Fatalf("emit ReleemAgent.Conf: %v", err)
	}

	host, ok := metrics.System.Info["Host"].(models.MetricGroupValue)
	if !ok {
		t.Fatalf("emitted Host = %#v, want MetricGroupValue", metrics.System.Info["Host"])
	}
	conf := map[string]any{
		"AwsRDSDB":                    metrics.ReleemAgent.Conf.AwsRDSDB,
		"AwsRDSParameterGroup":        metrics.ReleemAgent.Conf.AwsRDSParameterGroup,
		"AwsRDSClusterParameterGroup": metrics.ReleemAgent.Conf.AwsRDSClusterParameterGroup,
	}
	return host, conf
}

func contractCloudWatchClient(t *testing.T, capacity float64) *cloudwatchlogs.Client {
	t.Helper()
	client := contractHTTPClient{do: func(*http.Request) (*http.Response, error) {
		message, err := json.Marshal(map[string]any{"serverlessDatabaseCapacity": capacity})
		if err != nil {
			return nil, err
		}
		var response bytes.Buffer
		if err := json.NewEncoder(&response).Encode(map[string]any{
			"events": []map[string]any{{"message": string(message)}},
		}); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/x-amz-json-1.1"}},
			Body:       io.NopCloser(&response),
		}, nil
	}}
	return cloudwatchlogs.New(cloudwatchlogs.Options{
		BaseEndpoint: aws.String("https://cloudwatchlogs.test"),
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient:   client,
	})
}

type contractHTTPClient struct {
	do func(*http.Request) (*http.Response, error)
}

func (client contractHTTPClient) Do(request *http.Request) (*http.Response, error) {
	return client.do(request)
}

func assertExactKeys(t *testing.T, label string, values map[string]any, want []string) {
	t.Helper()
	got := make([]string, 0, len(values))
	for key := range values {
		got = append(got, key)
	}
	sort.Strings(got)
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s fields = %v, want exactly %v", label, got, want)
	}
}

func assertFieldsEqual(t *testing.T, label string, fixture, emitted map[string]any) {
	t.Helper()
	for key, want := range fixture {
		if got := emitted[key]; got != want {
			t.Errorf("%s.%s = %#v, want emitted value %#v", label, key, want, got)
		}
	}
}

func assertSecretFree(t *testing.T, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode fixture for secret-field check: %v", err)
	}
	normalized := strings.ToLower(string(data))
	for _, forbidden := range []string{"apikey", "credential", "password", "secret", "token"} {
		if strings.Contains(normalized, forbidden) {
			t.Errorf("fixture contains forbidden secret field fragment %q", forbidden)
		}
	}
}
