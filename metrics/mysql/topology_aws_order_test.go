package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"github.com/Releem/mysqlconfigurer/awsrds"
	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	drivermysql "github.com/go-sql-driver/mysql"
	logging "github.com/google/logger"
	"io"
	"testing"
)

func TestSQLAndAWSCollectionOrder(t *testing.T) {
	db := sql.OpenDB(factsConnector{query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
		return nil, &drivermysql.MySQLError{Number: 1146}
	}})
	previous := models.DB
	models.DB = db
	t.Cleanup(func() { models.DB = previous; db.Close() })
	logger := *logging.Init("collection-order-test", false, false, io.Discard)
	sqlCollector := NewDBTopologyGatherer(logger, &config.Config{})
	awsCollector := awsrds.NewTopologyRelationsGatherer(logger)
	for _, awsFirst := range []bool{false, true} {
		metrics := &models.Metrics{}
		metrics.DB.Conf.Variables = models.MetricGroupValue{"version": "8.0.40"}
		awsrds.AttachReportMetadata(metrics, awsrds.Metadata{TopologyFacts: &awsrds.AWSFacts{Version: 1, Sources: map[string]string{"Target": "ok"}}}, true)
		if awsFirst {
			_ = awsCollector.GetMetrics(metrics)
		}
		if err := sqlCollector.GetMetrics(metrics); err != nil {
			t.Fatal(err)
		}
		if !awsFirst {
			_ = awsCollector.GetMetrics(metrics)
		}
		if metrics.DB.TopologyFacts["AWS"].(*awsrds.AWSFacts).Sources["Target"] != "ok" || metrics.DB.TopologyFacts["Version"] != 1 || metrics.DB.TopologyFacts["ReplicaStatus"] == nil {
			t.Fatal("collection order lost facts")
		}
	}
}
