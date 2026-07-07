package mysql

import (
	"errors"
	"io"
	"testing"

	"github.com/Releem/mysqlconfigurer/models"
	logging "github.com/google/logger"
)

func TestBuildTopologyFromFactsDetectsAsyncReplica(t *testing.T) {
	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables: map[string]interface{}{
			"server_uuid":     "replica-uuid",
			"read_only":       "ON",
			"super_read_only": "ON",
		},
		ReplicaStatus: []map[string]interface{}{
			{
				"Source_Host":           "db-primary.example.com",
				"Source_UUID":           "primary-uuid",
				"Replica_IO_Running":    "Yes",
				"Replica_SQL_Running":   "Yes",
				"Seconds_Behind_Source": "3",
			},
		},
	})

	if topology["Type"] != "async_replication" {
		t.Fatalf("expected async replication, got %#v", topology["Type"])
	}
	if topology["Role"] != "replica" {
		t.Fatalf("expected replica role, got %#v", topology["Role"])
	}
	if topology["MemberKey"] != "replica-uuid" {
		t.Fatalf("expected replica member key, got %#v", topology["MemberKey"])
	}
	if topology["PrimaryMemberKey"] != "primary-uuid" {
		t.Fatalf("expected primary member key, got %#v", topology["PrimaryMemberKey"])
	}
	if topology["PrimaryHost"] != "db-primary.example.com" {
		t.Fatalf("expected primary host, got %#v", topology["PrimaryHost"])
	}
	if topology["ReplicationLagSeconds"] != int64(3) {
		t.Fatalf("expected lag 3, got %#v", topology["ReplicationLagSeconds"])
	}
	if topology["ReplicationState"] != "lagging" {
		t.Fatalf("expected lagging state, got %#v", topology["ReplicationState"])
	}
	if topology["IsWriter"] != false {
		t.Fatalf("expected replica not to be writer, got %#v", topology["IsWriter"])
	}
}

func TestBuildTopologyFromFactsKeepsAsyncPrimaryAsStandalone(t *testing.T) {
	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables: map[string]interface{}{
			"server_uuid":     "primary-uuid",
			"read_only":       "OFF",
			"super_read_only": "OFF",
		},
	})

	if topology["Type"] != "standalone" {
		t.Fatalf("expected async primary without replica rows to stay standalone, got %#v", topology["Type"])
	}
	if topology["Role"] != "primary" {
		t.Fatalf("expected primary role, got %#v", topology["Role"])
	}
	if topology["GroupKey"] != "primary-uuid" {
		t.Fatalf("expected primary member key as group key, got %#v", topology["GroupKey"])
	}
	if topology["IsWriter"] != true {
		t.Fatalf("expected async primary to be writer, got %#v", topology["IsWriter"])
	}
}

func TestBuildTopologyFromFactsUsesWorstAsyncReplicationChannel(t *testing.T) {
	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables: map[string]interface{}{
			"server_uuid":     "replica-uuid",
			"read_only":       "ON",
			"super_read_only": "ON",
		},
		ReplicaStatus: []map[string]interface{}{
			{
				"Channel_Name":           "healthy-channel",
				"Source_Host":            "healthy-primary.example.com",
				"Source_UUID":            "healthy-primary-uuid",
				"Replica_IO_Running":     "Yes",
				"Replica_SQL_Running":    "Yes",
				"Seconds_Behind_Source":  "0",
				"Large_Unused_Field":     "this should not be persisted",
				"Another_Unused_Field":   "this should not be persisted either",
				"Yet_Another_Unused_Key": "ignored",
			},
			{
				"Channel_Name":           "stopped-channel",
				"Source_Host":            "stopped-primary.example.com",
				"Source_UUID":            "stopped-primary-uuid",
				"Replica_IO_Running":     "No",
				"Replica_SQL_Running":    "Yes",
				"Seconds_Behind_Source":  "0",
				"Large_Unused_Field":     "this should not be persisted",
				"Another_Unused_Field":   "this should not be persisted either",
				"Yet_Another_Unused_Key": "ignored",
			},
		},
	})

	if topology["PrimaryMemberKey"] != "stopped-primary-uuid" {
		t.Fatalf("expected worst channel primary uuid, got %#v", topology["PrimaryMemberKey"])
	}
	if topology["PrimaryHost"] != "stopped-primary.example.com" {
		t.Fatalf("expected worst channel primary host, got %#v", topology["PrimaryHost"])
	}
	if topology["ReplicationState"] != "stopped" {
		t.Fatalf("expected stopped state from worst channel, got %#v", topology["ReplicationState"])
	}

	facts := topology["Facts"].(models.MetricGroupValue)
	replicaStatus := facts["ReplicaStatus"].(models.MetricGroupValue)
	if replicaStatus["Channel_Name"] != "stopped-channel" {
		t.Fatalf("expected selected trimmed replica status, got %#v", replicaStatus["Channel_Name"])
	}
	if _, ok := replicaStatus["Large_Unused_Field"]; ok {
		t.Fatalf("expected unused replica status fields to be trimmed")
	}
	channels := facts["ReplicaChannels"].([]models.MetricGroupValue)
	if len(channels) != 2 {
		t.Fatalf("expected all replica channels in facts, got %d", len(channels))
	}
	if _, ok := channels[0]["Another_Unused_Field"]; ok {
		t.Fatalf("expected unused replica channel fields to be trimmed")
	}
}

func TestBuildTopologyFromFactsDetectsGroupReplicationPrimary(t *testing.T) {
	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables: map[string]interface{}{
			"server_uuid":                           "member-1",
			"group_replication_group_name":          "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"group_replication_single_primary_mode": "ON",
			"read_only":                             "OFF",
		},
		GroupMembers: []map[string]interface{}{
			{
				"MEMBER_ID":    "member-1",
				"MEMBER_HOST":  "db1",
				"MEMBER_STATE": "ONLINE",
				"MEMBER_ROLE":  "PRIMARY",
			},
			{
				"MEMBER_ID":    "member-2",
				"MEMBER_HOST":  "db2",
				"MEMBER_STATE": "ONLINE",
				"MEMBER_ROLE":  "SECONDARY",
			},
		},
	})

	if topology["Type"] != "group_replication" {
		t.Fatalf("expected group replication, got %#v", topology["Type"])
	}
	if topology["Role"] != "primary" {
		t.Fatalf("expected primary role, got %#v", topology["Role"])
	}
	if topology["GroupKey"] != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Fatalf("expected group key, got %#v", topology["GroupKey"])
	}
	if topology["PrimaryMemberKey"] != "member-1" {
		t.Fatalf("expected primary member key, got %#v", topology["PrimaryMemberKey"])
	}
	if topology["IsWriter"] != true {
		t.Fatalf("expected primary writer, got %#v", topology["IsWriter"])
	}
	if topology["ReplicationState"] != "healthy" {
		t.Fatalf("expected healthy state, got %#v", topology["ReplicationState"])
	}
}

func TestBuildTopologyFromFactsInfersGroupReplicationPrimaryWhenMembersUnavailable(t *testing.T) {
	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables: map[string]interface{}{
			"server_uuid":                           "member-1",
			"group_replication_group_name":          "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"group_replication_single_primary_mode": "ON",
			"read_only":                             "OFF",
			"super_read_only":                       "OFF",
		},
	})

	if topology["Type"] != "group_replication" {
		t.Fatalf("expected group replication, got %#v", topology["Type"])
	}
	if topology["Role"] != "primary" {
		t.Fatalf("expected primary role from writable single-primary node, got %#v", topology["Role"])
	}
	if topology["PrimaryMemberKey"] != "member-1" {
		t.Fatalf("expected current member as primary, got %#v", topology["PrimaryMemberKey"])
	}
	if topology["IsWriter"] != true {
		t.Fatalf("expected writable primary, got %#v", topology["IsWriter"])
	}
}

func TestBuildTopologyFromFactsDetectsGaleraCluster(t *testing.T) {
	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables: map[string]interface{}{
			"server_uuid":              "server-uuid",
			"wsrep_on":                 "ON",
			"wsrep_cluster_state_uuid": "cluster-state",
			"wsrep_node_uuid":          "node-uuid",
			"read_only":                "OFF",
		},
		Status: map[string]interface{}{
			"wsrep_ready":               "ON",
			"wsrep_connected":           "ON",
			"wsrep_cluster_status":      "Primary",
			"wsrep_local_state_comment": "Synced",
		},
	})

	if topology["Type"] != "galera_cluster" {
		t.Fatalf("expected galera cluster, got %#v", topology["Type"])
	}
	if topology["Role"] != "multi_primary" {
		t.Fatalf("expected multi primary role, got %#v", topology["Role"])
	}
	if topology["GroupKey"] != "cluster-state" {
		t.Fatalf("expected cluster state group key, got %#v", topology["GroupKey"])
	}
	if topology["MemberKey"] != "node-uuid" {
		t.Fatalf("expected node uuid member key, got %#v", topology["MemberKey"])
	}
	if topology["ReplicationState"] != "healthy" {
		t.Fatalf("expected healthy galera state, got %#v", topology["ReplicationState"])
	}
}

func TestBuildTopologyFromFactsReturnsStandaloneForNoReplicationFacts(t *testing.T) {
	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables: map[string]interface{}{
			"server_uuid": "standalone-uuid",
			"read_only":   "OFF",
		},
	})

	if topology["Type"] != "standalone" {
		t.Fatalf("expected standalone, got %#v", topology["Type"])
	}
	if topology["Role"] != "primary" {
		t.Fatalf("expected primary role for standalone writer, got %#v", topology["Role"])
	}
	if topology["IsWriter"] != true {
		t.Fatalf("expected standalone writer, got %#v", topology["IsWriter"])
	}
}

type fakeTopologyRows struct {
	columns []string
	rows    [][]interface{}
	err     error
	index   int
}

func (r *fakeTopologyRows) Columns() ([]string, error) {
	return r.columns, nil
}

func (r *fakeTopologyRows) Next() bool {
	return r.index < len(r.rows)
}

func (r *fakeTopologyRows) Scan(dest ...interface{}) error {
	row := r.rows[r.index]
	r.index++
	for i := range dest {
		ptr := dest[i].(*interface{})
		*ptr = row[i]
	}
	return nil
}

func (r *fakeTopologyRows) Err() error {
	return r.err
}

func TestScanTopologyRowsReturnsNilOnRowsErr(t *testing.T) {
	logger := *logging.Init("db-topology-test", false, false, io.Discard)
	defer logger.Close()

	rows := &fakeTopologyRows{
		columns: []string{"Source_Host"},
		rows:    [][]interface{}{{"primary.example.com"}},
		err:     errors.New("cursor failed"),
	}

	if result := scanTopologyRows(rows, logger); result != nil {
		t.Fatalf("expected nil result when rows has iteration error, got %#v", result)
	}
}
