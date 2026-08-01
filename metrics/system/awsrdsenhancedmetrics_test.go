package system

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Releem/mysqlconfigurer/awsrds"
	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/Releem/mysqlconfigurer/utils"
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
			gatherer := NewAWSRDSEnhancedMetricsGatherer(logger, client, &config.Config{}, tt.metadata, func(context.Context) (awsrds.Metadata, error) {
				return tt.metadata, nil
			})
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
	writerMetadata := awsrds.Metadata{
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
	}
	readerMetadata := writerMetadata
	readerMetadata.Endpoint = "orders-reader.example"
	readerMetadata.IsClusterWriter = false
	recoveredWriterMetadata := writerMetadata
	recoveredWriterMetadata.DBInstanceResourceID = "db-resource-orders-2"
	recoveredWriterMetadata.Endpoint = "orders-writer-2.example"

	startupMetadata := awsrds.Metadata{
		DBInstanceIdentifier: "orders-1",
		DBInstanceResourceID: "db-resource-startup",
		Engine:               "aurora-mysql",
		IsClusterWriter:      true,
	}
	reports := []struct {
		metadata awsrds.Metadata
		err      error
	}{
		{metadata: writerMetadata},
		{metadata: readerMetadata},
		{err: errors.New("discovery unavailable")},
		{metadata: recoveredWriterMetadata},
	}
	discoveryCalls := 0
	gatherer := NewAWSRDSEnhancedMetricsGatherer(
		logger,
		client,
		configuration,
		startupMetadata,
		func(context.Context) (awsrds.Metadata, error) {
			report := reports[discoveryCalls]
			discoveryCalls++
			return report.metadata, report.err
		},
	)
	if got := configuration.MysqlHost; got != "startup.example" {
		t.Fatalf("MysqlHost = %q, want startup endpoint unchanged", got)
	}

	first := &models.Metrics{}
	if err := gatherer.GetMetrics(first); err != nil {
		t.Fatalf("first GetMetrics() error = %v", err)
	}
	firstHost := first.System.Info["Host"].(models.MetricGroupValue)
	if firstHost["IsClusterWriter"] != true {
		t.Fatalf("first IsClusterWriter = %#v, want true", firstHost["IsClusterWriter"])
	}
	if configuration.MysqlHost != "startup.example" {
		t.Fatalf("first MySQL host = %q, want startup endpoint unchanged", configuration.MysqlHost)
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
	if configuration.MysqlHost != "startup.example" {
		t.Fatalf("second MySQL host = %q, want startup endpoint unchanged", configuration.MysqlHost)
	}
	if got := <-requestedStream; got != "db-resource-orders-1" {
		t.Fatalf("second CloudWatch stream = %q, want one consistent refreshed resource ID", got)
	}

	fallback := &models.Metrics{}
	if err := gatherer.GetMetrics(fallback); err != nil {
		t.Fatalf("fallback GetMetrics() error = %v", err)
	}
	fallbackHost := fallback.System.Info["Host"].(models.MetricGroupValue)
	if fallbackHost["IsClusterWriter"] != false {
		t.Fatalf("fallback IsClusterWriter = %#v, want cached reader role", fallbackHost["IsClusterWriter"])
	}
	if got := <-requestedStream; got != readerMetadata.DBInstanceResourceID {
		t.Fatalf("fallback CloudWatch stream = %q, want %q", got, readerMetadata.DBInstanceResourceID)
	}

	recovered := &models.Metrics{}
	if err := gatherer.GetMetrics(recovered); err != nil {
		t.Fatalf("recovered GetMetrics() error = %v", err)
	}
	recoveredHost := recovered.System.Info["Host"].(models.MetricGroupValue)
	if recoveredHost["IsClusterWriter"] != true {
		t.Fatalf("recovered IsClusterWriter = %#v, want refreshed writer role", recoveredHost["IsClusterWriter"])
	}
	if got := <-requestedStream; got != recoveredWriterMetadata.DBInstanceResourceID {
		t.Fatalf("recovered CloudWatch stream = %q, want %q", got, recoveredWriterMetadata.DBInstanceResourceID)
	}
	if configuration.MysqlHost != "startup.example" {
		t.Fatalf("MySQL host = %q, want startup endpoint unchanged", configuration.MysqlHost)
	}
	if discoveryCalls != 4 {
		t.Fatalf("discovery calls = %d, want one per report", discoveryCalls)
	}
}

type countingMetricsGatherer struct {
	calls int
}

func (g *countingMetricsGatherer) GetMetrics(*models.Metrics) error {
	g.calls++
	return nil
}

func TestAWSRDSEnhancedMetricsDiscoveryFallbackKeepsCollectionAlive(t *testing.T) {
	fixture, err := os.ReadFile("../../awsrds/testdata/aurora_mysql_writer.json")
	if err != nil {
		t.Fatalf("read enhanced-monitoring fixture: %v", err)
	}
	client, requestedStream := testCloudWatchLogsClient(t, fixture)
	logger := *logging.Init("aws-rds-fallback-collection-test", false, false, io.Discard)
	configuration := &config.Config{}
	initial := testRDSMetadata("orders-1", "db-resource-cached", "db.r7g.large", "aurora-mysql", "instance-pg", "orders", "cluster-pg", "provisioned", true)
	gatherer := NewAWSRDSEnhancedMetricsGatherer(logger, client, configuration, initial, func(context.Context) (awsrds.Metadata, error) {
		return awsrds.Metadata{}, errors.New("discovery unavailable")
	})
	following := &countingMetricsGatherer{}

	metrics := utils.CollectMetrics([]models.MetricsGatherer{gatherer, following}, logger, configuration)
	if metrics == nil {
		t.Fatal("CollectMetrics() = nil, want report built from cached metadata")
	}
	if following.calls != 1 {
		t.Fatalf("following gatherer calls = %d, want 1", following.calls)
	}
	if got := <-requestedStream; got != initial.DBInstanceResourceID {
		t.Fatalf("CloudWatch stream = %q, want cached %q", got, initial.DBInstanceResourceID)
	}
}

func TestAWSRDSEnhancedMetricsDiscoveryTimeoutUsesCachedMetadata(t *testing.T) {
	fixture, err := os.ReadFile("../../awsrds/testdata/aurora_mysql_writer.json")
	if err != nil {
		t.Fatalf("read enhanced-monitoring fixture: %v", err)
	}
	client, requestedStream := testCloudWatchLogsClient(t, fixture)
	logger := *logging.Init("aws-rds-discovery-timeout-test", false, false, io.Discard)
	configuration := &config.Config{}
	initial := testRDSMetadata("orders-1", "db-resource-cached", "db.r7g.large", "aurora-mysql", "instance-pg", "orders", "cluster-pg", "provisioned", true)
	discoveryResult := make(chan error, 1)
	gatherer := NewAWSRDSEnhancedMetricsGatherer(logger, client, configuration, initial, func(ctx context.Context) (awsrds.Metadata, error) {
		<-ctx.Done()
		discoveryResult <- ctx.Err()
		return awsrds.Metadata{}, ctx.Err()
	})
	gatherer.metadataDiscoveryTimeout = 20 * time.Millisecond

	started := time.Now()
	metrics := &models.Metrics{}
	if err := gatherer.GetMetrics(metrics); err != nil {
		t.Fatalf("GetMetrics() error = %v, want cached-metadata report", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("GetMetrics() elapsed = %v, want bounded discovery fallback", elapsed)
	}
	if err := <-discoveryResult; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("discovery context error = %v, want deadline exceeded", err)
	}
	if got := <-requestedStream; got != initial.DBInstanceResourceID {
		t.Fatalf("CloudWatch stream = %q, want cached %q", got, initial.DBInstanceResourceID)
	}
	host := metrics.System.Info["Host"].(models.MetricGroupValue)
	if host["DBInstanceResourceID"] != initial.DBInstanceResourceID {
		t.Fatalf("Host DBInstanceResourceID = %#v, want cached %q", host["DBInstanceResourceID"], initial.DBInstanceResourceID)
	}
}

func TestAWSRDSEnhancedMetricsMetadataCacheIsConcurrentSafe(t *testing.T) {
	logger := *logging.Init("aws-rds-metadata-cache-race-test", false, false, io.Discard)
	initial := awsrds.Metadata{DBInstanceResourceID: "initial"}
	refreshed := awsrds.Metadata{DBInstanceResourceID: "refreshed"}
	var calls atomic.Int64
	gatherer := NewAWSRDSEnhancedMetricsGatherer(logger, nil, &config.Config{}, initial, func(context.Context) (awsrds.Metadata, error) {
		if calls.Add(1)%2 == 0 {
			return awsrds.Metadata{}, errors.New("discovery unavailable")
		}
		return refreshed, nil
	})

	const workers = 64
	results := make(chan string, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			results <- gatherer.metadataForReport(context.Background()).DBInstanceResourceID
		}()
	}
	group.Wait()
	close(results)
	for resourceID := range results {
		if resourceID != "initial" && resourceID != "refreshed" {
			t.Fatalf("metadata resource ID = %q, want a complete cached snapshot", resourceID)
		}
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
