package awsrds

import (
	"io"
	"reflect"
	"testing"

	"github.com/Releem/mysqlconfigurer/models"
	logging "github.com/google/logger"
)

func TestBuildTopologyRelationsAuroraProvisioned(t *testing.T) {
	metadata := testAuroraTopologyMetadata()

	relations := BuildTopologyRelations(metadata)
	if len(relations) != 1 {
		t.Fatalf("BuildTopologyRelations(provisioned Aurora) length = %d, want 1: %#v", len(relations), relations)
	}
	relation := relations[0]
	want := models.MetricGroupValue{
		"Type":                  "aurora_cluster",
		"Role":                  "replica",
		"GroupKey":              "aurora:cluster-orders-resource",
		"MemberKey":             "arn:aws:rds:us-east-1:123456789012:db:orders-reader",
		"MemberHost":            "orders-reader.internal",
		"MemberPort":            int64(3306),
		"PrimaryMemberKey":      "arn:aws:rds:us-east-1:123456789012:db:orders-writer",
		"PrimaryHost":           "orders-writer.internal",
		"PrimaryPort":           int64(3306),
		"ParentGroupKey":        nil,
		"IsWriter":              false,
		"IsReader":              true,
		"ReadOnly":              true,
		"SuperReadOnly":         false,
		"ReplicationLagSeconds": nil,
		"ReplicationState":      "healthy",
	}
	assertRelationFields(t, relation, want)

	facts := relationFacts(t, relation)
	wantFacts := models.MetricGroupValue{
		"DBClusterIdentifier":           "orders",
		"DBClusterARN":                  "arn:aws:rds:us-east-1:123456789012:cluster:orders",
		"DBClusterResourceID":           "cluster-orders-resource",
		"DBClusterParameterGroup":       "orders-cluster-pg",
		"DBClusterParameterGroupStatus": "in-sync",
		"ClusterEndpoint":               "orders.cluster.internal",
		"ClusterReaderEndpoint":         "orders-ro.cluster.internal",
		"Engine":                        "aurora-mysql",
		"EngineMode":                    "provisioned",
		"InstanceClass":                 "db.r7g.large",
		"InstanceStatus":                "available",
		"PromotionTier":                 int64(2),
		"IsServerlessV2":                false,
	}
	assertRelationFields(t, facts, wantFacts)

	metadata.InstanceStatus = "rebooting"
	metadata.ClusterMembers[1].InstanceStatus = "rebooting"
	relation = relationByType(t, BuildTopologyRelations(metadata), "aurora_cluster")
	if relation["ReplicationState"] != "unknown" || relation["IsWriter"] != false || relation["IsReader"] != false {
		t.Errorf("BuildTopologyRelations(unavailable Aurora) state/availability = %#v/%#v/%#v, want unknown/false/false", relation["ReplicationState"], relation["IsWriter"], relation["IsReader"])
	}
}

func TestIncompleteProviderMetadataOmitsAuthoritativeRelations(t *testing.T) {
	metadata := testAuroraTopologyMetadata()
	metadata.TopologyIncomplete = true
	metrics := &models.Metrics{}
	metrics.DB.Topology = models.MetricGroupValue{"Relations": []models.MetricGroupValue{}}
	AttachReportMetadata(metrics, metadata, true)
	if err := (&topologyRelationsGatherer{}).GetMetrics(metrics); err != nil {
		t.Fatal(err)
	}
	if _, ok := metrics.DB.Topology["Relations"]; ok {
		t.Fatal("incomplete topology marked authoritative")
	}
	if len(relationSlice(topologyFacts(metrics.DB.Topology)["Relations"])) == 0 {
		t.Fatal("local relations lost")
	}
}

func TestBuildTopologyRelationsAuroraServerlessV2(t *testing.T) {
	metadata := testAuroraTopologyMetadata()
	metadata.DBInstanceClass = "db.serverless"
	metadata.IsServerlessV2 = true
	metadata.HasServerlessV2ScalingConfiguration = true
	metadata.ServerlessV2ScalingConfiguration = ServerlessV2ScalingConfiguration{
		MinCapacity:           0,
		MaxCapacity:           16,
		SecondsUntilAutoPause: 900,
	}
	metadata.ClusterMembers[1].DBInstanceClass = "db.serverless"
	metadata.ClusterMembers[1].IsServerlessV2 = true

	relation := relationByType(t, BuildTopologyRelations(metadata), "aurora_cluster")
	if relation["Type"] != "aurora_cluster" || relation["GroupKey"] != "aurora:cluster-orders-resource" {
		t.Errorf("BuildTopologyRelations(Serverless v2) type/group = %#v/%#v, want aurora_cluster/aurora:cluster-orders-resource", relation["Type"], relation["GroupKey"])
	}
	facts := relationFacts(t, relation)
	wantFacts := models.MetricGroupValue{
		"InstanceClass":                       "db.serverless",
		"IsServerlessV2":                      true,
		"HasServerlessV2ScalingConfiguration": true,
		"ServerlessV2MinCapacity":             float64(0),
		"ServerlessV2MaxCapacity":             float64(16),
		"ServerlessV2SecondsUntilAutoPause":   int64(900),
	}
	assertRelationFields(t, facts, wantFacts)
}

func TestBuildTopologyRelationsAuroraGlobalDatabase(t *testing.T) {
	metadata := testAuroraTopologyMetadata()
	metadata.DBInstanceIdentifier = "orders-secondary-writer"
	metadata.DBInstanceARN = "arn:aws:rds:us-west-2:123456789012:db:orders-secondary-writer"
	metadata.DBInstanceResourceID = "db-orders-secondary-writer"
	metadata.DBInstanceClass = "db.r7g.large"
	metadata.Endpoint = "orders-secondary-writer.internal"
	metadata.Region = "us-west-2"
	metadata.DBClusterIdentifier = "orders-secondary"
	metadata.DBClusterARN = "arn:aws:rds:us-west-2:123456789012:cluster:orders-secondary"
	metadata.DBClusterResourceID = "cluster-orders-secondary-resource"
	metadata.ClusterEndpoint = "orders-secondary.cluster.internal"
	metadata.ClusterReaderEndpoint = "orders-secondary-ro.cluster.internal"
	metadata.ClusterMembers = []ClusterMember{{
		DBInstanceIdentifier:          metadata.DBInstanceIdentifier,
		DBInstanceARN:                 metadata.DBInstanceARN,
		DBInstanceResourceID:          metadata.DBInstanceResourceID,
		DBInstanceClass:               metadata.DBInstanceClass,
		Endpoint:                      metadata.Endpoint,
		EndpointPort:                  metadata.EndpointPort,
		InstanceStatus:                "available",
		IsClusterWriter:               true,
		PromotionTier:                 0,
		DBClusterParameterGroupStatus: "in-sync",
	}}
	metadata.IsClusterWriter = true
	metadata.PromotionTier = 0
	metadata.GlobalClusterIdentifier = "orders-global"
	metadata.GlobalClusterARN = "arn:aws:rds::123456789012:global-cluster:orders-global"
	metadata.GlobalClusterResourceID = "cluster-orders-global-resource"
	metadata.GlobalClusterPrimaryDBClusterARN = "arn:aws:rds:us-east-1:123456789012:cluster:orders-primary"
	metadata.GlobalClusterPrimaryRegion = "us-east-1"
	metadata.GlobalClusterMembers = []GlobalClusterMember{
		{
			DBClusterIdentifier:         "orders-primary",
			DBClusterARN:                metadata.GlobalClusterPrimaryDBClusterARN,
			Region:                      "us-east-1",
			IsWriter:                    true,
			SynchronizationStatus:       "connected",
			GlobalWriteForwardingStatus: "enabled",
		},
		{
			DBClusterIdentifier:         metadata.DBClusterIdentifier,
			DBClusterARN:                metadata.DBClusterARN,
			Region:                      metadata.Region,
			SynchronizationStatus:       "connected",
			GlobalWriteForwardingStatus: "disabled",
		},
	}

	relations := BuildTopologyRelations(metadata)
	if len(relations) != 2 {
		t.Fatalf("BuildTopologyRelations(Global Database) length = %d, want Aurora and global relations: %#v", len(relations), relations)
	}
	relation := relationByType(t, relations, "aurora_global_database")
	want := models.MetricGroupValue{
		"Role":                  "replica_cluster_member",
		"GroupKey":              "aurora-global:cluster-orders-global-resource",
		"MemberKey":             metadata.DBInstanceARN,
		"MemberHost":            metadata.Endpoint,
		"MemberPort":            int64(3306),
		"PrimaryMemberKey":      nil,
		"PrimaryHost":           nil,
		"PrimaryPort":           nil,
		"ParentGroupKey":        metadata.GlobalClusterPrimaryDBClusterARN,
		"IsWriter":              false,
		"IsReader":              true,
		"ReadOnly":              true,
		"SuperReadOnly":         false,
		"ReplicationLagSeconds": nil,
		"ReplicationState":      "healthy",
	}
	assertRelationFields(t, relation, want)
	facts := relationFacts(t, relation)
	assertRelationFields(t, facts, models.MetricGroupValue{
		"GlobalClusterIdentifier":     metadata.GlobalClusterIdentifier,
		"GlobalClusterARN":            metadata.GlobalClusterARN,
		"GlobalClusterResourceID":     metadata.GlobalClusterResourceID,
		"RegionalDBClusterIdentifier": metadata.DBClusterIdentifier,
		"RegionalDBClusterARN":        metadata.DBClusterARN,
		"Region":                      "us-west-2",
		"PrimaryDBClusterARN":         metadata.GlobalClusterPrimaryDBClusterARN,
		"PrimaryRegion":               "us-east-1",
		"SynchronizationStatus":       "connected",
		"GlobalWriteForwardingStatus": "disabled",
	})
}

func TestBuildTopologyRelationsRDSReadReplica(t *testing.T) {
	metadata := Metadata{
		Partition:            "aws",
		Region:               "us-east-1",
		DBInstanceIdentifier: "orders-replica-1",
		DBInstanceARN:        "arn:aws:rds:us-east-1:123456789012:db:orders-replica-1",
		DBInstanceResourceID: "db-replica-1-resource",
		Endpoint:             "orders-replica-1.internal",
		EndpointPort:         3306,
		Engine:               "mysql",
		InstanceStatus:       "available",
		HasReadReplicaSource: true,
		ReadReplicaSource: RelatedDBInstance{
			DBInstanceIdentifier: "orders-primary",
			DBInstanceARN:        "arn:aws:rds:us-east-1:123456789012:db:orders-primary",
			DBInstanceResourceID: "db-primary-resource",
			Endpoint:             "orders-primary.internal",
			EndpointPort:         3306,
			Engine:               "mysql",
			InstanceStatus:       "available",
			Partition:            "aws",
			Region:               "us-east-1",
		},
		ReadReplicaDBInstanceIdentifiers: []string{"orders-replica-2"},
		ReadReplicas: []RelatedDBInstance{{
			DBInstanceIdentifier: "orders-replica-2",
			DBInstanceARN:        "arn:aws:rds:us-east-1:123456789012:db:orders-replica-2",
			DBInstanceResourceID: "db-replica-2-resource",
			Endpoint:             "orders-replica-2.internal",
			EndpointPort:         3306,
			Engine:               "mysql",
			InstanceStatus:       "available",
			Partition:            "aws",
			Region:               "us-east-1",
		}},
	}

	relations := BuildTopologyRelations(metadata)
	if len(relations) != 2 {
		t.Fatalf("BuildTopologyRelations(cascading RDS replica) length = %d, want upstream and child-source relations: %#v", len(relations), relations)
	}
	upstream := relationByGroup(t, relations, "rds-replica:db-primary-resource")
	assertRelationFields(t, upstream, models.MetricGroupValue{
		"Type":                  "rds_read_replica",
		"Role":                  "replica",
		"MemberKey":             metadata.DBInstanceARN,
		"MemberHost":            metadata.Endpoint,
		"MemberPort":            int64(3306),
		"PrimaryMemberKey":      metadata.ReadReplicaSource.DBInstanceARN,
		"PrimaryHost":           metadata.ReadReplicaSource.Endpoint,
		"PrimaryPort":           int64(3306),
		"ParentGroupKey":        nil,
		"IsWriter":              false,
		"IsReader":              true,
		"ReadOnly":              true,
		"SuperReadOnly":         false,
		"ReplicationLagSeconds": nil,
		"ReplicationState":      "unknown",
	})

	downstream := relationByGroup(t, relations, "rds-replica:db-replica-1-resource")
	assertRelationFields(t, downstream, models.MetricGroupValue{
		"Type":             "rds_read_replica",
		"Role":             "primary",
		"MemberKey":        metadata.DBInstanceARN,
		"PrimaryMemberKey": metadata.DBInstanceARN,
		"PrimaryHost":      metadata.Endpoint,
		"PrimaryPort":      int64(3306),
		"IsWriter":         false,
		"IsReader":         true,
		"ReadOnly":         true,
		"ReplicationState": "unknown",
	})
	facts := relationFacts(t, downstream)
	if got := facts["ReadReplicaDBInstanceIdentifiers"]; !reflect.DeepEqual(got, []string{"orders-replica-2"}) {
		t.Errorf("BuildTopologyRelations(cascading RDS replica) child identifiers = %#v, want [orders-replica-2]", got)
	}
}

func TestBuildTopologyRelationsRDSMultiAZ(t *testing.T) {
	metadata := Metadata{
		Partition:            "aws",
		Region:               "us-east-1",
		DBInstanceIdentifier: "orders-primary",
		DBInstanceARN:        "arn:aws:rds:us-east-1:123456789012:db:orders-primary",
		DBInstanceResourceID: "db-orders-primary-resource",
		Endpoint:             "orders-primary.internal",
		EndpointPort:         3306,
		Engine:               "mysql",
		InstanceStatus:       "available",
		MultiAZ:              true,
	}

	relations := BuildTopologyRelations(metadata)
	if len(relations) != 1 {
		t.Fatalf("BuildTopologyRelations(classic Multi-AZ) length = %d, want one addressable relation: %#v", len(relations), relations)
	}
	relation := relations[0]
	assertRelationFields(t, relation, models.MetricGroupValue{
		"Type":                  "rds_multi_az",
		"Role":                  "primary",
		"GroupKey":              "rds-multi-az:db-orders-primary-resource",
		"MemberKey":             metadata.DBInstanceARN,
		"MemberHost":            metadata.Endpoint,
		"MemberPort":            int64(3306),
		"PrimaryMemberKey":      metadata.DBInstanceARN,
		"PrimaryHost":           metadata.Endpoint,
		"PrimaryPort":           int64(3306),
		"ParentGroupKey":        nil,
		"IsWriter":              true,
		"IsReader":              true,
		"ReadOnly":              false,
		"SuperReadOnly":         false,
		"ReplicationLagSeconds": nil,
		"ReplicationState":      "unknown",
	})
	facts := relationFacts(t, relation)
	if facts["ManagedStandby"] != true {
		t.Errorf("BuildTopologyRelations(classic Multi-AZ) Facts.ManagedStandby = %#v, want true", facts["ManagedStandby"])
	}
	for _, forbidden := range []string{"StandbyMemberKey", "StandbyHost", "StandbySID"} {
		if _, ok := relation[forbidden]; ok {
			t.Errorf("BuildTopologyRelations(classic Multi-AZ) contains invented %s = %#v", forbidden, relation[forbidden])
		}
		if _, ok := facts[forbidden]; ok {
			t.Errorf("BuildTopologyRelations(classic Multi-AZ) Facts contains invented %s = %#v", forbidden, facts[forbidden])
		}
	}
}

func TestTopologyRelationsGathererOmitsCanonicalSnapshotWhenProviderIsIncomplete(t *testing.T) {
	metadata := testAuroraTopologyMetadata()
	topology := models.MetricGroupValue{
		"Type": "standalone",
		"Facts": models.MetricGroupValue{
			"Relations": []models.MetricGroupValue{{
				"Type":      "standalone",
				"GroupKey":  "mysql-group",
				"MemberKey": "mysql-member",
			}},
		},
		"Relations": []models.MetricGroupValue{{
			"Type":      "standalone",
			"GroupKey":  "mysql-group",
			"MemberKey": "mysql-member",
		}},
	}
	metrics := &models.Metrics{}
	metrics.DB.Topology = topology
	AttachReportMetadata(metrics, metadata, false)
	metadata.DBClusterResourceID = "mutated-cluster-resource"
	metadata.ClusterMembers[0].IsClusterWriter = false
	logger := *logging.Init("aws-topology-incomplete-test", false, false, io.Discard)
	gatherer := NewTopologyRelationsGatherer(logger)

	if err := gatherer.GetMetrics(metrics); err != nil {
		t.Fatalf("TopologyRelationsGatherer.GetMetrics(incomplete provider snapshot) error = %v", err)
	}
	if _, ok := metrics.DB.Topology["Relations"]; ok {
		t.Errorf("TopologyRelationsGatherer.GetMetrics(incomplete provider snapshot) canonical Relations = %#v, want omitted", metrics.DB.Topology["Relations"])
	}
	if metrics.DB.Topology["Type"] != "standalone" {
		t.Errorf("TopologyRelationsGatherer.GetMetrics(incomplete provider snapshot) compatibility Type = %#v, want standalone", metrics.DB.Topology["Type"])
	}
	facts := metrics.DB.Topology["Facts"].(models.MetricGroupValue)
	relations := facts["Relations"].([]models.MetricGroupValue)
	if len(relations) != 2 {
		t.Fatalf("TopologyRelationsGatherer.GetMetrics(incomplete provider snapshot) Facts.Relations length = %d, want native and cached AWS relations: %#v", len(relations), relations)
	}
	if relationByType(t, relations, "aurora_cluster")["GroupKey"] != "aurora:cluster-orders-resource" {
		t.Errorf("TopologyRelationsGatherer.GetMetrics(incomplete provider snapshot) cached AWS relation = %#v, want preserved", relations)
	}
}

func testAuroraTopologyMetadata() Metadata {
	return Metadata{
		Partition:                     "aws",
		Region:                        "us-east-1",
		DBInstanceIdentifier:          "orders-reader",
		DBInstanceARN:                 "arn:aws:rds:us-east-1:123456789012:db:orders-reader",
		DBInstanceResourceID:          "db-orders-reader-resource",
		DBInstanceClass:               "db.r7g.large",
		Endpoint:                      "orders-reader.internal",
		EndpointPort:                  3306,
		Engine:                        "aurora-mysql",
		EngineMode:                    "provisioned",
		DBClusterIdentifier:           "orders",
		DBClusterARN:                  "arn:aws:rds:us-east-1:123456789012:cluster:orders",
		DBClusterResourceID:           "cluster-orders-resource",
		DBClusterParameterGroup:       "orders-cluster-pg",
		DBClusterParameterGroupStatus: "in-sync",
		ClusterEndpoint:               "orders.cluster.internal",
		ClusterReaderEndpoint:         "orders-ro.cluster.internal",
		ClusterMembers: []ClusterMember{
			{
				DBInstanceIdentifier:          "orders-writer",
				DBInstanceARN:                 "arn:aws:rds:us-east-1:123456789012:db:orders-writer",
				DBInstanceResourceID:          "db-orders-writer-resource",
				DBInstanceClass:               "db.r7g.large",
				Endpoint:                      "orders-writer.internal",
				EndpointPort:                  3306,
				InstanceStatus:                "available",
				IsClusterWriter:               true,
				PromotionTier:                 0,
				DBClusterParameterGroupStatus: "in-sync",
			},
			{
				DBInstanceIdentifier:          "orders-reader",
				DBInstanceARN:                 "arn:aws:rds:us-east-1:123456789012:db:orders-reader",
				DBInstanceResourceID:          "db-orders-reader-resource",
				DBInstanceClass:               "db.r7g.large",
				Endpoint:                      "orders-reader.internal",
				EndpointPort:                  3306,
				InstanceStatus:                "available",
				PromotionTier:                 2,
				DBClusterParameterGroupStatus: "in-sync",
			},
		},
		PromotionTier:  2,
		InstanceStatus: "available",
	}
}

func relationByType(t *testing.T, relations []models.MetricGroupValue, typeName string) models.MetricGroupValue {
	t.Helper()
	for _, relation := range relations {
		if relation["Type"] == typeName {
			return relation
		}
	}
	t.Fatalf("relation type %q not found in %#v", typeName, relations)
	return nil
}

func relationByGroup(t *testing.T, relations []models.MetricGroupValue, groupKey string) models.MetricGroupValue {
	t.Helper()
	for _, relation := range relations {
		if relation["GroupKey"] == groupKey {
			return relation
		}
	}
	t.Fatalf("relation group %q not found in %#v", groupKey, relations)
	return nil
}

func relationFacts(t *testing.T, relation models.MetricGroupValue) models.MetricGroupValue {
	t.Helper()
	facts, ok := relation["Facts"].(models.MetricGroupValue)
	if !ok {
		t.Fatalf("relation Facts = %#v, want models.MetricGroupValue", relation["Facts"])
	}
	return facts
}

func assertRelationFields(t *testing.T, got, want models.MetricGroupValue) {
	t.Helper()
	for key, wantValue := range want {
		if gotValue := got[key]; !reflect.DeepEqual(gotValue, wantValue) {
			t.Errorf("relation field %s = %#v, want %#v", key, gotValue, wantValue)
		}
	}
}
