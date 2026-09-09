package awsrds

import (
	"github.com/Releem/mysqlconfigurer/models"
	logging "github.com/google/logger"
)

// MetadataSnapshot returns the most recently discovered metadata and whether all sources
// are complete for the current report.
type MetadataSnapshot func() (Metadata, bool)

type reportTopologyMetadata struct {
	metadata Metadata
	fresh    bool
}

type topologyRelationsGatherer struct{}

var _ models.MetricsGatherer = (*topologyRelationsGatherer)(nil)

// NewTopologyRelationsGatherer publishes report-local raw provider facts alongside
// native database facts. Discovery failures are logged by the enhanced
// metrics gatherer.
func NewTopologyRelationsGatherer(_ logging.Logger) models.MetricsGatherer {
	return &topologyRelationsGatherer{}
}

func (g *topologyRelationsGatherer) GetMetrics(metrics *models.Metrics) error {
	if metrics == nil {
		return nil
	}

	metadata, fresh, ok := reportMetadata(metrics)
	if !ok {
		return nil
	}
	if metadata.TopologyFacts == nil {
		return nil
	}
	facts := metadata.TopologyFacts.clone()
	if !fresh {
		for source := range facts.Sources {
			facts.Sources[source] = "error"
		}
	}

	if metrics.DB.TopologyFacts == nil {
		metrics.DB.TopologyFacts = models.MetricGroupValue{"Version": 1}
	}
	metrics.DB.TopologyFacts["AWS"] = facts
	return nil
}

// AttachReportMetadata stores an immutable provider snapshot on one collection
// context for the later raw facts gatherer; fresh is false when a refresh failed.
func AttachReportMetadata(metrics *models.Metrics, metadata Metadata, fresh bool) {
	if metrics == nil {
		return
	}
	metrics.Internal.AWSRDS = reportTopologyMetadata{
		metadata: metadata.Clone(),
		fresh:    fresh,
	}
}

func reportMetadata(metrics *models.Metrics) (Metadata, bool, bool) {
	if metrics == nil {
		return Metadata{}, false, false
	}
	snapshot, ok := metrics.Internal.AWSRDS.(reportTopologyMetadata)
	if !ok {
		return Metadata{}, false, false
	}
	return snapshot.metadata.Clone(), snapshot.fresh, true
}

// Clone returns metadata whose slice storage is independent of the receiver.
func (m Metadata) Clone() Metadata {
	cloned := m
	cloned.TopologyFacts = m.TopologyFacts.clone()
	cloned.ClusterMembers = append([]ClusterMember(nil), m.ClusterMembers...)
	cloned.GlobalClusterMembers = append([]GlobalClusterMember(nil), m.GlobalClusterMembers...)
	cloned.ReadReplicaDBInstanceIdentifiers = append([]string(nil), m.ReadReplicaDBInstanceIdentifiers...)
	cloned.ReadReplicas = append([]RelatedDBInstance(nil), m.ReadReplicas...)
	return cloned
}
