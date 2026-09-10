package system

import (
	"context"
	"errors"
	"github.com/Releem/mysqlconfigurer/awsrds"
	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	logging "github.com/google/logger"
	"io"
	"testing"
)

func TestRawSourceRefreshCompleteness(t *testing.T) {
	logger := *logging.Init("raw-refresh-test", false, false, io.Discard)
	metadata := awsrds.Metadata{TopologyIncomplete: true, TopologyFacts: &awsrds.AWSFacts{Version: 1, Sources: map[string]string{"Target": "ok", "Cluster": "ok", "Peers": "error"}}}
	fail := false
	gatherer := NewAWSRDSEnhancedMetricsGatherer(logger, nil, &config.Config{}, metadata, func(context.Context) (awsrds.Metadata, error) {
		if fail {
			return awsrds.Metadata{}, errors.New("permission denied")
		}
		return metadata, nil
	})
	collector := awsrds.NewTopologyRelationsGatherer(logger)
	for _, failed := range []bool{false, true, false} {
		fail = failed
		reportMetadata, fresh := gatherer.metadataForReport(context.Background())
		if fresh == failed {
			t.Fatal("incorrect freshness")
		}
		_, complete := gatherer.MetadataSnapshot()
		if complete {
			t.Fatal("optional error reported complete")
		}
		report := &models.Metrics{}
		awsrds.AttachReportMetadata(report, reportMetadata, fresh)
		_ = collector.GetMetrics(report)
		sources := report.DB.Topology["AWS"].(*awsrds.AWSFacts).Sources
		expected := "ok"
		if failed {
			expected = "error"
		}
		if sources["Target"] != expected || sources["Cluster"] != expected || sources["Peers"] != "error" {
			t.Fatalf("source freshness lost: %v", sources)
		}
	}
	metadata.TopologyIncomplete = false
	metadata.TopologyFacts.Sources["Peers"] = "ok"
	_, _ = gatherer.metadataForReport(context.Background())
	_, complete := gatherer.MetadataSnapshot()
	if !complete {
		t.Fatal("successful refresh retained stale failure")
	}
}
