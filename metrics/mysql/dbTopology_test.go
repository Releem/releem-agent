package mysql

import "testing"

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
