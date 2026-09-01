package system

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Releem/mysqlconfigurer/awsrds"
	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logging "github.com/google/logger"
)

func TestAWSRDSEnhancedMetricsGathererPublishesRDSMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		fixture            string
		metadata           awsrds.Metadata
		wantClusterID      string
		wantClusterGroup   string
		wantEngineMode     string
		wantWriter         bool
		wantCapacity       float64
		wantTimestamp      time.Time
		wantUptime         string
		wantInstanceClass  string
		wantParameterGroup string
	}{
		{
			name:               "Aurora MySQL writer",
			fixture:            "aurora_mysql_writer.json",
			metadata:           testRDSMetadata("orders-writer", "db-resource-mysql-writer", "db.r7g.xlarge", "aurora-mysql", "orders-instance-pg", "orders-cluster", "orders-cluster-pg", "provisioned", true),
			wantClusterID:      "orders-cluster",
			wantClusterGroup:   "orders-cluster-pg",
			wantEngineMode:     "provisioned",
			wantWriter:         true,
			wantTimestamp:      time.Date(2026, 7, 31, 8, 9, 10, 0, time.UTC),
			wantUptime:         "5 days, 04:03:02",
			wantInstanceClass:  "db.r7g.xlarge",
			wantParameterGroup: "orders-instance-pg",
		},
		{
			name:               "Aurora MySQL reader preserves serverless capacity",
			fixture:            "aurora_mysql_reader.json",
			metadata:           testRDSMetadata("orders-reader", "db-resource-mysql-reader", "db.serverless", "aurora-mysql", "orders-instance-pg", "orders-cluster", "orders-cluster-pg", "provisioned", false),
			wantClusterID:      "orders-cluster",
			wantClusterGroup:   "orders-cluster-pg",
			wantEngineMode:     "provisioned",
			wantCapacity:       8.5,
			wantTimestamp:      time.Date(2026, 7, 31, 8, 9, 11, 0, time.UTC),
			wantUptime:         "2 days, 01:02:03",
			wantInstanceClass:  "db.serverless",
			wantParameterGroup: "orders-instance-pg",
		},
		{
			name:               "Aurora PostgreSQL writer",
			fixture:            "aurora_postgresql_writer.json",
			metadata:           testRDSMetadata("analytics-writer", "db-resource-pg-writer", "db.r7g.2xlarge", "aurora-postgresql", "analytics-instance-pg", "analytics-cluster", "analytics-cluster-pg", "provisioned", true),
			wantClusterID:      "analytics-cluster",
			wantClusterGroup:   "analytics-cluster-pg",
			wantEngineMode:     "provisioned",
			wantWriter:         true,
			wantTimestamp:      time.Date(2026, 7, 31, 8, 9, 12, 0, time.UTC),
			wantUptime:         "10 days, 00:00:01",
			wantInstanceClass:  "db.r7g.2xlarge",
			wantParameterGroup: "analytics-instance-pg",
		},
		{
			name:               "Aurora PostgreSQL reader preserves serverless capacity",
			fixture:            "aurora_postgresql_reader.json",
			metadata:           testRDSMetadata("analytics-reader", "db-resource-pg-reader", "db.serverless", "aurora-postgresql", "analytics-instance-pg", "analytics-cluster", "analytics-cluster-pg", "provisioned", false),
			wantClusterID:      "analytics-cluster",
			wantClusterGroup:   "analytics-cluster-pg",
			wantEngineMode:     "provisioned",
			wantCapacity:       4.5,
			wantTimestamp:      time.Date(2026, 7, 31, 8, 9, 13, 0, time.UTC),
			wantUptime:         "1 day, 02:03:04",
			wantInstanceClass:  "db.serverless",
			wantParameterGroup: "analytics-instance-pg",
		},
		{
			name:               "ordinary RDS keeps empty cluster metadata",
			fixture:            "aurora_mysql_writer.json",
			metadata:           testRDSMetadata("orders-rds", "db-resource-rds", "db.m7g.large", "mysql", "orders-rds-pg", "", "", "", false),
			wantTimestamp:      time.Date(2026, 7, 31, 8, 9, 10, 0, time.UTC),
			wantUptime:         "5 days, 04:03:02",
			wantInstanceClass:  "db.m7g.large",
			wantParameterGroup: "orders-rds-pg",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fixture, err := os.ReadFile("../../awsrds/testdata/" + tt.fixture)
			if err != nil {
				t.Fatalf("read enhanced-monitoring fixture: %v", err)
			}

			client, requestedStream := testCloudWatchLogsClient(t, fixture)
			logger := *logging.Init("aws-rds-enhanced-metrics-test", false, false, io.Discard)
			gatherer := NewAWSRDSEnhancedMetricsGatherer(
				logger,
				client,
				&config.Config{},
				tt.metadata,
				func(context.Context) (awsrds.Metadata, error) { return tt.metadata, nil },
			)
			metrics := &models.Metrics{}
			if err := gatherer.GetMetrics(metrics); err != nil {
				t.Fatalf("GetMetrics() error = %v", err)
			}

			host, ok := metrics.System.Info["Host"].(models.MetricGroupValue)
			if !ok {
				t.Fatalf("System.Info.Host = %#v, want MetricGroupValue", metrics.System.Info["Host"])
			}

			want := models.MetricGroupValue{
				"InstanceType":               "aws/rds",
				"platform":                   "aws",
				"platformVersion":            "rds " + tt.metadata.Engine,
				"Timestamp":                  tt.wantTimestamp,
				"Uptime":                     tt.wantUptime,
				"Engine":                     tt.metadata.Engine,
				"Version":                    float64(1),
				"ServerlessDatabaseCapacity": tt.wantCapacity,
				"DBInstanceIdentifier":       tt.metadata.DBInstanceIdentifier,
				"DBInstanceResourceID":       tt.metadata.DBInstanceResourceID,
				"DBInstanceClass":            tt.wantInstanceClass,
				"DBParameterGroup":           tt.wantParameterGroup,
				"DBClusterIdentifier":        tt.wantClusterID,
				"DBClusterParameterGroup":    tt.wantClusterGroup,
				"IsClusterWriter":            tt.wantWriter,
				"EngineMode":                 tt.wantEngineMode,
			}
			assertMetricGroupEqual(t, host, want)

			if got := <-requestedStream; got != tt.metadata.DBInstanceResourceID {
				t.Fatalf("CloudWatch log stream = %q, want DB instance resource ID %q", got, tt.metadata.DBInstanceResourceID)
			}
		})
	}
}

func TestAWSRDSEnhancedMetricsGathererRefreshesTopologyBeforeEveryReport(t *testing.T) {
	fixture, err := os.ReadFile("../../awsrds/testdata/aurora_mysql_reader.json")
	if err != nil {
		t.Fatalf("read enhanced-monitoring fixture: %v", err)
	}

	reader := testRDSMetadata("orders-1", "db-resource-orders", "db.r7g.large", "aurora-mysql", "instance-pg", "orders", "cluster-pg", "provisioned", false)
	writer := reader
	writer.IsClusterWriter = true
	discovered := []awsrds.Metadata{reader, writer}
	discoveryCalls := 0
	discover := func(ctx context.Context) (awsrds.Metadata, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("metadata discoverer context has no deadline")
		}
		metadata := discovered[discoveryCalls]
		discoveryCalls++
		return metadata, nil
	}

	client, requestedStream := testCloudWatchLogsClient(t, fixture)
	logger := *logging.Init("aws-rds-enhanced-metrics-refresh-test", false, false, io.Discard)
	gatherer := NewAWSRDSEnhancedMetricsGatherer(logger, client, &config.Config{}, reader, discover)

	for report, wantWriter := range []bool{false, true} {
		metrics := &models.Metrics{}
		if err := gatherer.GetMetrics(metrics); err != nil {
			t.Fatalf("GetMetrics(report=%d) error = %v", report+1, err)
		}
		if got := <-requestedStream; got != reader.DBInstanceResourceID {
			t.Fatalf("GetMetrics(report=%d) CloudWatch stream = %q, want %q", report+1, got, reader.DBInstanceResourceID)
		}
		host, ok := metrics.System.Info["Host"].(models.MetricGroupValue)
		if !ok {
			t.Fatalf("GetMetrics(report=%d) Host = %#v, want MetricGroupValue", report+1, metrics.System.Info["Host"])
		}
		if got := host["IsClusterWriter"]; got != wantWriter {
			t.Errorf("GetMetrics(report=%d) IsClusterWriter = %#v, want %v", report+1, got, wantWriter)
		}
	}
	if discoveryCalls != 2 {
		t.Errorf("GetMetrics() metadata discovery calls = %d, want 2", discoveryCalls)
	}
}

func TestAWSRDSEnhancedMetricsGathererFallsBackToCachedTopology(t *testing.T) {
	fixture, err := os.ReadFile("../../awsrds/testdata/aurora_mysql_writer.json")
	if err != nil {
		t.Fatalf("read enhanced-monitoring fixture: %v", err)
	}

	cached := testRDSMetadata("orders-1", "db-resource-orders", "db.r7g.large", "aurora-mysql", "instance-pg", "orders", "cluster-pg", "provisioned", true)
	discover := func(ctx context.Context) (awsrds.Metadata, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("metadata discoverer context has no deadline")
		} else if remaining := time.Until(deadline); remaining <= 0 || remaining > time.Second {
			t.Errorf("metadata discovery deadline remaining = %s, want within one second", remaining)
		}
		return awsrds.Metadata{}, errors.New("RDS discovery unavailable")
	}

	client, requestedStream := testCloudWatchLogsClient(t, fixture)
	logger := *logging.Init("aws-rds-enhanced-metrics-cache-test", false, false, io.Discard)
	gatherer := NewAWSRDSEnhancedMetricsGatherer(logger, client, &config.Config{}, cached, discover)
	gatherer.metadataDiscoveryTimeout = time.Second

	metrics := &models.Metrics{}
	if err := gatherer.GetMetrics(metrics); err != nil {
		t.Fatalf("GetMetrics(discovery failure) error = %v", err)
	}
	if got := <-requestedStream; got != cached.DBInstanceResourceID {
		t.Fatalf("GetMetrics(discovery failure) CloudWatch stream = %q, want cached %q", got, cached.DBInstanceResourceID)
	}
	host, ok := metrics.System.Info["Host"].(models.MetricGroupValue)
	if !ok {
		t.Fatalf("GetMetrics(discovery failure) Host = %#v, want MetricGroupValue", metrics.System.Info["Host"])
	}
	if got := host["IsClusterWriter"]; got != true {
		t.Errorf("GetMetrics(discovery failure) IsClusterWriter = %#v, want cached true", got)
	}
}

func TestLogAWSRDSDiscoveryRedactsSensitiveMetadata(t *testing.T) {
	const endpointSecret = "private-orders-writer.example"
	const resourceIDSecret = "db-resource-secret"
	metadata := testRDSMetadata("orders-1", resourceIDSecret, "db.r7g.large", "aurora-mysql", "instance-pg", "orders", "cluster-pg", "provisioned", true)
	metadata.Endpoint = endpointSecret

	var output bytes.Buffer
	logger := logging.Init("aws-rds-discovery-log-test", false, false, &output)
	LogAWSRDSDiscovery(*logger, "startup", metadata)

	event := awsRDSDiscoveryLogEvent(t, output.String())
	if event["source"] != "startup" {
		t.Errorf("LogAWSRDSDiscovery() source = %#v, want startup", event["source"])
	}
	topology, ok := event["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("LogAWSRDSDiscovery() metadata = %#v, want object", event["metadata"])
	}
	if topology["db_instance_identifier"] != "orders-1" || topology["db_cluster_identifier"] != "orders" || topology["is_cluster_writer"] != true {
		t.Errorf("LogAWSRDSDiscovery() metadata = %#v, want safe orders-1/orders writer topology", topology)
	}
	for _, redacted := range []string{"endpoint", "endpoint_port", "db_instance_resource_id"} {
		if _, exists := topology[redacted]; exists {
			t.Errorf("LogAWSRDSDiscovery() metadata exposes %q: %#v", redacted, topology)
		}
	}
	for _, secret := range []string{endpointSecret, resourceIDSecret} {
		if strings.Contains(output.String(), secret) {
			t.Errorf("LogAWSRDSDiscovery() output contains sensitive value %q: %s", secret, output.String())
		}
	}
}

func awsRDSDiscoveryLogEvent(t *testing.T, output string) map[string]interface{} {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		start := strings.Index(line, "{")
		if start < 0 {
			continue
		}
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(line[start:]), &event); err == nil && event["event"] == "aws_rds_discovery" {
			return event
		}
	}
	t.Fatalf("LogAWSRDSDiscovery() event not found in %q", output)
	return nil
}

func testRDSMetadata(identifier, resourceID, instanceClass, engine, parameterGroup, clusterID, clusterGroup, engineMode string, writer bool) awsrds.Metadata {
	return awsrds.Metadata{
		DBInstanceIdentifier:    identifier,
		DBInstanceResourceID:    resourceID,
		DBInstanceClass:         instanceClass,
		Engine:                  engine,
		DBParameterGroup:        parameterGroup,
		DBClusterIdentifier:     clusterID,
		DBClusterParameterGroup: clusterGroup,
		EngineMode:              engineMode,
		IsClusterWriter:         writer,
	}
}

func testCloudWatchLogsClient(t *testing.T, fixture []byte) (*cloudwatchlogs.Client, <-chan string) {
	t.Helper()

	requestedStream := make(chan string, 1)
	httpClient := testHTTPClient{do: func(r *http.Request) (*http.Response, error) {
		var input struct {
			LogStreamName string `json:"logStreamName"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			return nil, err
		}
		requestedStream <- input.LogStreamName

		var response bytes.Buffer
		if err := json.NewEncoder(&response).Encode(map[string]interface{}{
			"events": []map[string]interface{}{{
				"ingestionTime": 1,
				"message":       string(fixture),
				"timestamp":     1,
			}},
		}); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/x-amz-json-1.1"}},
			Body:       io.NopCloser(&response),
		}, nil
	}}

	client := cloudwatchlogs.New(cloudwatchlogs.Options{
		BaseEndpoint: aws.String("https://cloudwatchlogs.test"),
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient:   httpClient,
	})
	return client, requestedStream
}

type testHTTPClient struct {
	do func(*http.Request) (*http.Response, error)
}

func (c testHTTPClient) Do(request *http.Request) (*http.Response, error) {
	return c.do(request)
}

func assertMetricGroupEqual(t *testing.T, got, want models.MetricGroupValue) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("metric keys = %v, want exactly %v", got, want)
	}
	for key, wantValue := range want {
		if gotValue := got[key]; gotValue != wantValue {
			t.Errorf("metric %q = %#v, want %#v", key, gotValue, wantValue)
		}
	}
}
