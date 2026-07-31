package system

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
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
			gatherer := NewAWSRDSEnhancedMetricsGatherer(logger, tt.metadata, client, &config.Config{})
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

func TestAWSRDSEnhancedMetricsGathererRefreshesMetadataForEveryReport(t *testing.T) {
	fixture, err := os.ReadFile("../../awsrds/testdata/aurora_mysql_writer.json")
	if err != nil {
		t.Fatalf("read enhanced-monitoring fixture: %v", err)
	}

	client, requestedStream := testCloudWatchLogsClient(t, fixture)
	logger := *logging.Init("aws-rds-enhanced-metrics-refresh-test", false, false, io.Discard)
	configuration := &config.Config{MysqlHost: "startup.example"}
	reports := []awsrds.Metadata{
		{
			DBInstanceIdentifier:    "orders-1",
			DBInstanceResourceID:    "db-resource-orders-1",
			DBInstanceClass:         "db.r7g.large",
			Endpoint:                "orders-writer.example",
			Engine:                  "aurora-mysql",
			EngineMode:              "provisioned",
			DBParameterGroup:        "orders-instance-pg",
			DBClusterIdentifier:     "orders-cluster",
			DBClusterParameterGroup: "orders-cluster-pg",
			IsClusterWriter:         true,
		},
		{
			DBInstanceIdentifier:    "orders-1",
			DBInstanceResourceID:    "db-resource-orders-1",
			DBInstanceClass:         "db.r7g.large",
			Endpoint:                "orders-reader.example",
			Engine:                  "aurora-mysql",
			EngineMode:              "provisioned",
			DBParameterGroup:        "orders-instance-pg",
			DBClusterIdentifier:     "orders-cluster",
			DBClusterParameterGroup: "orders-cluster-pg",
			IsClusterWriter:         false,
		},
	}
	discoveryCalls := 0
	discover := func(context.Context) (awsrds.Metadata, error) {
		discoveryCalls++
		if discoveryCalls > len(reports) {
			return awsrds.Metadata{}, errors.New("discovery unavailable")
		}
		return reports[discoveryCalls-1], nil
	}
	gatherer := NewAWSRDSEnhancedMetricsGatherer(
		logger,
		awsrds.Metadata{IsClusterWriter: false},
		client,
		configuration,
		discover,
	)

	first := &models.Metrics{}
	if err := gatherer.GetMetrics(first); err != nil {
		t.Fatalf("first GetMetrics() error = %v", err)
	}
	firstHost := first.System.Info["Host"].(models.MetricGroupValue)
	if firstHost["IsClusterWriter"] != true {
		t.Fatalf("first IsClusterWriter = %#v, want true", firstHost["IsClusterWriter"])
	}
	if configuration.MysqlHost != "orders-writer.example" {
		t.Fatalf("first MySQL host = %q, want refreshed writer endpoint", configuration.MysqlHost)
	}
	if got := <-requestedStream; got != "db-resource-orders-1" {
		t.Fatalf("first CloudWatch stream = %q, want refreshed resource ID", got)
	}

	second := &models.Metrics{}
	if err := gatherer.GetMetrics(second); err != nil {
		t.Fatalf("second GetMetrics() error = %v", err)
	}
	secondHost := second.System.Info["Host"].(models.MetricGroupValue)
	if secondHost["IsClusterWriter"] != false {
		t.Fatalf("second IsClusterWriter = %#v, want false after failover", secondHost["IsClusterWriter"])
	}
	if configuration.MysqlHost != "orders-reader.example" {
		t.Fatalf("second MySQL host = %q, want refreshed reader endpoint", configuration.MysqlHost)
	}
	if got := <-requestedStream; got != "db-resource-orders-1" {
		t.Fatalf("second CloudWatch stream = %q, want one consistent refreshed resource ID", got)
	}

	failed := &models.Metrics{}
	failed.System.Info = models.MetricGroupValue{"sentinel": "unchanged"}
	if err := gatherer.GetMetrics(failed); err == nil {
		t.Fatal("third GetMetrics() error = nil, want discovery failure")
	}
	if failed.System.Info["sentinel"] != "unchanged" || len(failed.System.Info) != 1 {
		t.Fatalf("failed report metadata = %#v, want untouched sentinel", failed.System.Info)
	}
	if configuration.MysqlHost != "orders-reader.example" {
		t.Fatalf("host after failed discovery = %q, want last complete endpoint", configuration.MysqlHost)
	}
	if discoveryCalls != 3 {
		t.Fatalf("discovery calls = %d, want one per report", discoveryCalls)
	}
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
