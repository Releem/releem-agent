package mysql

import (
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/Releem/mysqlconfigurer/models"
	logging "github.com/google/logger"
)

func TestReplicaStatusQueriesUsesMariaDBAllChannels(t *testing.T) {
	queries := replicaStatusQueries(map[string]string{
		"version":         "10.11.14-MariaDB-0ubuntu0.24.04.2",
		"version_comment": "Ubuntu 24.04",
	})

	expected := []string{
		"SHOW ALL REPLICAS STATUS",
		"SHOW ALL SLAVES STATUS",
		"SHOW REPLICA STATUS",
		"SHOW SLAVE STATUS",
	}
	if len(queries) != len(expected) {
		t.Fatalf("expected %d MariaDB replica status queries, got %#v", len(expected), queries)
	}
	for idx := range expected {
		if queries[idx] != expected[idx] {
			t.Fatalf("expected query %d to be %q, got %q", idx, expected[idx], queries[idx])
		}
	}
}

func TestReplicaStatusQueriesKeepsMySQLCompatibilityOrder(t *testing.T) {
	queries := replicaStatusQueries(map[string]string{
		"version":         "8.0.42",
		"version_comment": "MySQL Community Server - GPL",
	})

	expected := []string{"SHOW REPLICA STATUS", "SHOW SLAVE STATUS"}
	if len(queries) != len(expected) {
		t.Fatalf("expected %d MySQL replica status queries, got %#v", len(expected), queries)
	}
	for idx := range expected {
		if queries[idx] != expected[idx] {
			t.Fatalf("expected query %d to be %q, got %q", idx, expected[idx], queries[idx])
		}
	}
}

func TestReplicaStatusQueriesDetectsMariaDBFromVersionComment(t *testing.T) {
	queries := replicaStatusQueries(map[string]string{
		"version":         "10.6.22",
		"version_comment": "MariaDB Server",
	})

	if queries[0] != "SHOW ALL REPLICAS STATUS" {
		t.Fatalf("expected MariaDB query order from version comment, got %#v", queries)
	}
}

func TestFirstSupportedTopologyQueryFallsBackAfterQueryError(t *testing.T) {
	queries := []string{"unsupported", "supported", "not-called"}
	called := []string{}
	expectedRows := []map[string]interface{}{{"Connection_name": "channel-a"}}

	rows := firstSupportedTopologyQuery(queries, func(query string) ([]map[string]interface{}, bool) {
		called = append(called, query)
		if query == "supported" {
			return expectedRows, true
		}
		return nil, false
	})

	if len(called) != 2 || called[0] != "unsupported" || called[1] != "supported" {
		t.Fatalf("expected fallback to stop at first supported query, called %#v", called)
	}
	if len(rows) != 1 || rows[0]["Connection_name"] != "channel-a" {
		t.Fatalf("expected rows from supported fallback query, got %#v", rows)
	}
}

func TestFirstSupportedTopologyQueryStopsOnSuccessfulEmptyResult(t *testing.T) {
	called := []string{}

	rows := firstSupportedTopologyQuery([]string{"supported-empty", "not-called"}, func(query string) ([]map[string]interface{}, bool) {
		called = append(called, query)
		return []map[string]interface{}{}, true
	})

	if len(called) != 1 || called[0] != "supported-empty" {
		t.Fatalf("expected successful empty result to stop fallback, called %#v", called)
	}
	if rows == nil || len(rows) != 0 {
		t.Fatalf("expected successful empty result, got %#v", rows)
	}
}

func TestBuildTopologyFromFactsDetectsAsyncReplica(t *testing.T) {
	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables: map[string]interface{}{
			"server_uuid":     "replica-uuid",
			"hostname":        "db-replica.example.com",
			"port":            "3307",
			"read_only":       "ON",
			"super_read_only": "ON",
		},
		ReplicaStatus: []map[string]interface{}{
			{
				"Source_Host":           "db-primary.example.com",
				"Source_Port":           "3308",
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
	if topology["MemberHost"] != "db-replica.example.com" || topology["MemberPort"] != int64(3307) {
		t.Fatalf("BuildTopologyFromFacts(async) member endpoint = %#v:%#v, want db-replica.example.com:3307", topology["MemberHost"], topology["MemberPort"])
	}
	if topology["PrimaryPort"] != int64(3308) {
		t.Fatalf("BuildTopologyFromFacts(async) PrimaryPort = %#v, want 3308", topology["PrimaryPort"])
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
	facts := topology["Facts"].(models.MetricGroupValue)
	channels := facts["ReplicaChannels"].([]models.MetricGroupValue)
	if channels[0]["Source_Port"] != "3308" {
		t.Fatalf("BuildTopologyFromFacts(async) channel Source_Port = %#v, want 3308", channels[0]["Source_Port"])
	}
}

func TestBuildTopologyFromFactsUsesMariaDBMasterServerID(t *testing.T) {
	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables: map[string]interface{}{
			"server_id":       "22",
			"read_only":       "ON",
			"version":         "10.11.14-MariaDB",
			"version_comment": "MariaDB Server",
		},
		ReplicaStatus: []map[string]interface{}{
			{
				"Connection_name":       "source-a",
				"Master_Host":           "mariadb-primary.example.com",
				"Master_Server_Id":      "11",
				"Slave_IO_Running":      "Yes",
				"Slave_SQL_Running":     "Yes",
				"Seconds_Behind_Master": "0",
			},
		},
	})

	if topology["PrimaryMemberKey"] != "11" {
		t.Fatalf("expected MariaDB primary server_id, got %#v", topology["PrimaryMemberKey"])
	}
	if topology["GroupKey"] != "11" {
		t.Fatalf("expected MariaDB primary server_id as group key, got %#v", topology["GroupKey"])
	}
	facts := topology["Facts"].(models.MetricGroupValue)
	channels := facts["ReplicaChannels"].([]models.MetricGroupValue)
	if channels[0]["Master_Server_Id"] != "11" {
		t.Fatalf("expected Master_Server_Id retained in facts, got %#v", channels[0])
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
	if topology["IsReader"] != true {
		t.Fatalf("expected writable async primary to serve reads, got %#v", topology["IsReader"])
	}
}

func TestBuildTopologyFromFactsMarksReadOnlyReplicaAsReader(t *testing.T) {
	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables: map[string]interface{}{
			"server_uuid":     "replica-uuid",
			"read_only":       "ON",
			"super_read_only": "ON",
		},
		ReplicaStatus: []map[string]interface{}{
			{
				"Source_UUID":           "primary-uuid",
				"Replica_IO_Running":    "Yes",
				"Replica_SQL_Running":   "Yes",
				"Seconds_Behind_Source": "0",
			},
		},
	})

	if topology["IsReader"] != true {
		t.Fatalf("expected read-only async replica to be reader, got %#v", topology["IsReader"])
	}
	if topology["IsWriter"] != false {
		t.Fatalf("expected read-only async replica not to be writer, got %#v", topology["IsWriter"])
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
	if topology["IsReader"] != false {
		t.Fatalf("expected stopped async replica not to serve reads, got %#v", topology["IsReader"])
	}
	if topology["GroupKey"] != "multi-source:82acf8935d3df10fc257ed12b4b9245a87310a07d571d8dbca1cdbc56294a445" {
		t.Fatalf("expected stable multi-source digest, got %#v", topology["GroupKey"])
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

func TestBuildTopologyFromFactsBreaksAsyncReplicationTiesByLag(t *testing.T) {
	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables: map[string]interface{}{
			"server_uuid":     "replica-uuid",
			"read_only":       "ON",
			"super_read_only": "ON",
		},
		ReplicaStatus: []map[string]interface{}{
			{
				"Channel_Name":          "low-lag-channel",
				"Source_Host":           "low-lag-primary.example.com",
				"Source_UUID":           "low-lag-primary-uuid",
				"Replica_IO_Running":    "Yes",
				"Replica_SQL_Running":   "Yes",
				"Seconds_Behind_Source": "5",
			},
			{
				"Channel_Name":          "high-lag-channel",
				"Source_Host":           "high-lag-primary.example.com",
				"Source_UUID":           "high-lag-primary-uuid",
				"Replica_IO_Running":    "Yes",
				"Replica_SQL_Running":   "Yes",
				"Seconds_Behind_Source": "30",
			},
		},
	})

	if topology["PrimaryMemberKey"] != "high-lag-primary-uuid" {
		t.Fatalf("expected higher-lag channel primary uuid, got %#v", topology["PrimaryMemberKey"])
	}
	if topology["ReplicationLagSeconds"] != int64(30) {
		t.Fatalf("expected higher lag at top level, got %#v", topology["ReplicationLagSeconds"])
	}
	if topology["GroupKey"] != "multi-source:86b73165acc9c089ed26433fa9afc4feacb02f04121bb1cce08bee9ff24c46fb" {
		t.Fatalf("expected stable sorted multi-source digest, got %#v", topology["GroupKey"])
	}
}

func TestAsyncReplicaGroupKeyUsesAnalyzedChannels(t *testing.T) {
	groupKey := asyncReplicaGroupKey(
		[]asyncReplicaChannel{
			{primaryMemberKey: "primary-b", primaryHost: "db-b.example.com"},
			{primaryMemberKey: "primary-a", primaryHost: "db-a.example.com"},
			{primaryMemberKey: "primary-a", primaryHost: "db-a-duplicate.example.com"},
		},
		"replica-uuid",
		asyncReplicaChannel{primaryMemberKey: "primary-b", primaryHost: "db-b.example.com"},
	)

	if groupKey != "multi-source:3495b08ea97ce2bcd1bbcab31a6538ddc3500c561b3c7ae30e5cf45d9ec9b137" {
		t.Fatalf("expected sorted digest from analyzed channels, got %#v", groupKey)
	}
}

func TestAsyncReplicaGroupKeyUsesFixedLengthDigest(t *testing.T) {
	channels := []asyncReplicaChannel{
		{primaryMemberKey: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"},
		{primaryMemberKey: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"},
		{primaryMemberKey: "cccccccc-cccc-cccc-cccc-cccccccccccc"},
		{primaryMemberKey: "dddddddd-dddd-dddd-dddd-dddddddddddd"},
		{primaryMemberKey: "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"},
		{primaryMemberKey: "ffffffff-ffff-ffff-ffff-ffffffffffff"},
		{primaryMemberKey: "11111111-1111-1111-1111-111111111111"},
	}
	selected := channels[0]

	groupKey := asyncReplicaGroupKey(channels, "replica-uuid", selected)
	reversed := append([]asyncReplicaChannel(nil), channels...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	reversedGroupKey := asyncReplicaGroupKey(reversed, "replica-uuid", selected)

	if !strings.HasPrefix(groupKey, "multi-source:") {
		t.Fatalf("expected multi-source digest prefix, got %q", groupKey)
	}
	if len(groupKey) != len("multi-source:")+64 {
		t.Fatalf("expected fixed-length SHA-256 group key, got %d characters: %q", len(groupKey), groupKey)
	}
	if groupKey != reversedGroupKey {
		t.Fatalf("expected channel order not to affect digest, got %q and %q", groupKey, reversedGroupKey)
	}
}

func TestBuildTopologyFromFactsDistinguishesDuplicateMariaDBServerIDsByEndpoint(t *testing.T) {
	channels := []map[string]interface{}{
		{
			"Master_Server_Id":      "11",
			"Master_Host":           "db-b.example.com",
			"Master_Port":           "3307",
			"Slave_IO_Running":      "Yes",
			"Slave_SQL_Running":     "Yes",
			"Seconds_Behind_Master": "0",
		},
		{
			"Master_Server_Id":      "11",
			"Master_Host":           "db-a.example.com",
			"Master_Port":           "3306",
			"Slave_IO_Running":      "Yes",
			"Slave_SQL_Running":     "Yes",
			"Seconds_Behind_Master": "0",
		},
	}

	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables:     map[string]interface{}{"server_id": "22"},
		ReplicaStatus: channels,
	})
	reversed := BuildTopologyFromFacts(TopologyFacts{
		Variables:     map[string]interface{}{"server_id": "22"},
		ReplicaStatus: []map[string]interface{}{channels[1], channels[0]},
	})

	if topology["GroupKey"] == "11" || topology["GroupKey"] != reversed["GroupKey"] {
		t.Fatalf("BuildTopologyFromFacts(duplicate server IDs) GroupKey = %#v, reversed = %#v; want stable multi-source digest", topology["GroupKey"], reversed["GroupKey"])
	}
	if topology["PrimaryHost"] != "db-a.example.com" || topology["PrimaryPort"] != int64(3306) {
		t.Fatalf("BuildTopologyFromFacts(duplicate server IDs) selected endpoint = %#v:%#v, want db-a.example.com:3306", topology["PrimaryHost"], topology["PrimaryPort"])
	}
}

func TestBuildTopologyFromFactsDetectsGroupReplicationPrimary(t *testing.T) {
	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables: map[string]interface{}{
			"server_uuid":                           "member-1",
			"hostname":                              "db1-variable.example.com",
			"port":                                  "3306",
			"group_replication_group_name":          "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"group_replication_single_primary_mode": "ON",
			"read_only":                             "OFF",
		},
		GroupMembers: []map[string]interface{}{
			{
				"MEMBER_ID":    "member-1",
				"MEMBER_HOST":  "db1.example.com",
				"MEMBER_PORT":  "3307",
				"MEMBER_STATE": "ONLINE",
				"MEMBER_ROLE":  "PRIMARY",
			},
			{
				"MEMBER_ID":    "member-2",
				"MEMBER_HOST":  "db2",
				"MEMBER_PORT":  "3308",
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
	if topology["MemberHost"] != "db1.example.com" || topology["MemberPort"] != int64(3307) {
		t.Fatalf("BuildTopologyFromFacts(group replication) member endpoint = %#v:%#v, want db1.example.com:3307", topology["MemberHost"], topology["MemberPort"])
	}
	if topology["PrimaryHost"] != "db1.example.com" || topology["PrimaryPort"] != int64(3307) {
		t.Fatalf("BuildTopologyFromFacts(group replication) primary endpoint = %#v:%#v, want db1.example.com:3307", topology["PrimaryHost"], topology["PrimaryPort"])
	}
	if topology["IsWriter"] != true {
		t.Fatalf("expected primary writer, got %#v", topology["IsWriter"])
	}
	if topology["IsReader"] != true {
		t.Fatalf("expected healthy group replication member to serve reads, got %#v", topology["IsReader"])
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
	if topology["IsWriter"] != false {
		t.Fatalf("expected unknown replication state not to advertise writer availability, got %#v", topology["IsWriter"])
	}
	if topology["IsReader"] != false {
		t.Fatalf("expected unknown replication state not to advertise reader availability, got %#v", topology["IsReader"])
	}
}

func TestBuildTopologyFromFactsDetectsGroupReplicationSecondary(t *testing.T) {
	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables: map[string]interface{}{
			"server_uuid":                           "member-2",
			"group_replication_group_name":          "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"group_replication_single_primary_mode": "ON",
			"read_only":                             "ON",
			"super_read_only":                       "ON",
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

	if topology["Role"] != "replica" {
		t.Fatalf("expected secondary role, got %#v", topology["Role"])
	}
	if topology["IsWriter"] != false {
		t.Fatalf("expected secondary not to be writer, got %#v", topology["IsWriter"])
	}
	if topology["IsReader"] != true {
		t.Fatalf("expected healthy group replication secondary to serve reads, got %#v", topology["IsReader"])
	}
}

func TestBuildTopologyFromFactsMapsMySQL57GroupReplicationPrimaryFromStatus(t *testing.T) {
	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables: map[string]interface{}{
			"server_uuid":                           "member-2",
			"group_replication_group_name":          "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"group_replication_single_primary_mode": "ON",
			"read_only":                             "ON",
			"super_read_only":                       "ON",
		},
		Status: map[string]interface{}{
			"group_replication_primary_member": "member-1",
		},
		GroupMembers: []map[string]interface{}{
			{
				"MEMBER_ID":    "member-1",
				"MEMBER_HOST":  "db1.example.com",
				"MEMBER_STATE": "ONLINE",
			},
			{
				"MEMBER_ID":    "member-2",
				"MEMBER_HOST":  "db2.example.com",
				"MEMBER_STATE": "ONLINE",
			},
		},
	})

	if topology["Role"] != "replica" {
		t.Fatalf("expected MySQL 5.7 secondary role, got %#v", topology["Role"])
	}
	if topology["PrimaryMemberKey"] != "member-1" {
		t.Fatalf("expected primary from group_replication_primary_member, got %#v", topology["PrimaryMemberKey"])
	}
	if topology["PrimaryHost"] != "db1.example.com" {
		t.Fatalf("expected primary host mapped from members, got %#v", topology["PrimaryHost"])
	}
}

func TestBuildTopologyFromFactsClearsPrimaryForMultiPrimaryGroupReplication(t *testing.T) {
	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables: map[string]interface{}{
			"server_uuid":                           "member-2",
			"group_replication_group_name":          "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"group_replication_single_primary_mode": "OFF",
			"read_only":                             "OFF",
			"super_read_only":                       "OFF",
		},
		Status: map[string]interface{}{
			"group_replication_primary_member": "member-1",
		},
		GroupMembers: []map[string]interface{}{
			{
				"MEMBER_ID":    "member-1",
				"MEMBER_HOST":  "db1.example.com",
				"MEMBER_STATE": "ONLINE",
				"MEMBER_ROLE":  "PRIMARY",
			},
			{
				"MEMBER_ID":    "member-2",
				"MEMBER_HOST":  "db2.example.com",
				"MEMBER_STATE": "ONLINE",
				"MEMBER_ROLE":  "PRIMARY",
			},
		},
	})

	if topology["Role"] != "multi_primary" {
		t.Fatalf("expected multi-primary role, got %#v", topology["Role"])
	}
	if topology["PrimaryMemberKey"] != nil || topology["PrimaryHost"] != nil {
		t.Fatalf("expected no unique primary in multi-primary mode, got key=%#v host=%#v", topology["PrimaryMemberKey"], topology["PrimaryHost"])
	}
	if topology["IsWriter"] != true {
		t.Fatalf("expected healthy writable multi-primary member, got %#v", topology["IsWriter"])
	}
}

func TestBuildTopologyFromFactsMarksOfflineGroupReplicationPrimaryAsNonWriter(t *testing.T) {
	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables: map[string]interface{}{
			"server_uuid":                           "member-1",
			"group_replication_group_name":          "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"group_replication_single_primary_mode": "ON",
			"read_only":                             "OFF",
			"super_read_only":                       "OFF",
		},
		GroupMembers: []map[string]interface{}{
			{
				"MEMBER_ID":    "member-1",
				"MEMBER_HOST":  "db1.example.com",
				"MEMBER_STATE": "OFFLINE",
				"MEMBER_ROLE":  "PRIMARY",
			},
		},
	})

	if topology["ReplicationState"] != "error" {
		t.Fatalf("expected offline member state to be error, got %#v", topology["ReplicationState"])
	}
	if topology["IsWriter"] != false {
		t.Fatalf("expected offline primary not to be a writer, got %#v", topology["IsWriter"])
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
	if topology["IsReader"] != true {
		t.Fatalf("expected healthy galera node to serve reads, got %#v", topology["IsReader"])
	}
}

func TestBuildTopologyFromFactsUsesGaleraNodeNameBeforeClusterStateUUID(t *testing.T) {
	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables: map[string]interface{}{
			"wsrep_on":                 "ON",
			"wsrep_node_name":          "galera-node-2",
			"wsrep_node_address":       "10.0.0.12",
			"wsrep_cluster_state_uuid": "shared-cluster-state",
			"read_only":                "OFF",
		},
		Status: map[string]interface{}{
			"wsrep_local_state_uuid":    "shared-cluster-state",
			"wsrep_ready":               "ON",
			"wsrep_connected":           "ON",
			"wsrep_cluster_status":      "Primary",
			"wsrep_local_state_comment": "Synced",
		},
	})

	if topology["MemberKey"] != "galera-node-2" {
		t.Fatalf("expected Galera node name as member key, got %#v", topology["MemberKey"])
	}
	if topology["MemberKey"] == topology["GroupKey"] {
		t.Fatalf("expected member identity to differ from shared cluster state UUID")
	}
}

func TestAttachTopologyRelationsPublishesCanonicalAndLegacyViews(t *testing.T) {
	topology := models.MetricGroupValue{
		"Type":      "group_replication",
		"GroupKey":  "physical-group",
		"MemberKey": "member-a",
		"Facts":     models.MetricGroupValue{"GroupMembers": []models.MetricGroupValue{}},
	}
	relations := []models.MetricGroupValue{
		{
			"Type":                  " group_replication ",
			"GroupKey":              " group-a ",
			"MemberKey":             " member-a ",
			"IsWriter":              "YES",
			"IsReader":              "false",
			"ReadOnly":              "ON",
			"SuperReadOnly":         "0",
			"MemberPort":            "3306",
			"PrimaryPort":           []byte("3307"),
			"ReplicationLagSeconds": "12",
			"Facts":                 models.MetricGroupValue{"Source": "first"},
		},
		{
			"Type":      "async_replication",
			"GroupKey":  "source-a",
			"MemberKey": "member-a",
			"IsWriter":  false,
			"IsReader":  true,
		},
		{
			"Type":      "group_replication",
			"GroupKey":  "group-a",
			"MemberKey": "member-a",
			"Facts":     models.MetricGroupValue{"Source": "duplicate"},
		},
	}

	AttachTopologyRelations(topology, relations, true)

	canonical, ok := topology["Relations"].([]models.MetricGroupValue)
	if !ok {
		t.Fatalf("AttachTopologyRelations(topology, relations, true) Relations = %#v, want []models.MetricGroupValue", topology["Relations"])
	}
	facts := topology["Facts"].(models.MetricGroupValue)
	legacy, ok := facts["Relations"].([]models.MetricGroupValue)
	if !ok {
		t.Fatalf("AttachTopologyRelations(topology, relations, true) Facts.Relations = %#v, want []models.MetricGroupValue", facts["Relations"])
	}
	if !reflect.DeepEqual(canonical, legacy) {
		t.Fatalf("AttachTopologyRelations(topology, relations, true) Relations = %#v, want identical Facts.Relations %#v", canonical, legacy)
	}

	want := []models.MetricGroupValue{
		{
			"Type":      "async_replication",
			"GroupKey":  "source-a",
			"MemberKey": "member-a",
			"IsWriter":  false,
			"IsReader":  true,
		},
		{
			"Type":                  "group_replication",
			"GroupKey":              "group-a",
			"MemberKey":             "member-a",
			"IsWriter":              true,
			"IsReader":              false,
			"ReadOnly":              true,
			"SuperReadOnly":         false,
			"MemberPort":            int64(3306),
			"PrimaryPort":           int64(3307),
			"ReplicationLagSeconds": int64(12),
			"Facts":                 models.MetricGroupValue{"Source": "first"},
		},
	}
	if !reflect.DeepEqual(canonical, want) {
		t.Fatalf("AttachTopologyRelations(topology, relations, true) Relations = %#v, want %#v", canonical, want)
	}
}

func TestNormalizeTopologyRelationsSortsAndDeduplicates(t *testing.T) {
	tests := []struct {
		name      string
		relations []models.MetricGroupValue
		want      []models.MetricGroupValue
	}{
		{
			name: "normalizes identity fields and keeps the first duplicate",
			relations: []models.MetricGroupValue{
				{"Type": " group_replication ", "GroupKey": " group-b ", "MemberKey": " member-a ", "Facts": models.MetricGroupValue{"Source": "first"}},
				{"Type": "async_replication", "GroupKey": "source-a", "MemberKey": "member-a"},
				{"Type": "group_replication", "GroupKey": "group-b", "MemberKey": "member-a", "Facts": models.MetricGroupValue{"Source": "duplicate"}},
			},
			want: []models.MetricGroupValue{
				{"Type": "async_replication", "GroupKey": "source-a", "MemberKey": "member-a"},
				{"Type": "group_replication", "GroupKey": "group-b", "MemberKey": "member-a", "Facts": models.MetricGroupValue{"Source": "first"}},
			},
		},
		{
			name: "drops relations without a complete identity",
			relations: []models.MetricGroupValue{
				{"Type": "group_replication", "MemberKey": "member-a"},
				{"Type": "group_replication", "GroupKey": "group-a"},
				{"GroupKey": "group-a", "MemberKey": "member-a"},
			},
			want: []models.MetricGroupValue{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := NormalizeTopologyRelations(test.relations)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("NormalizeTopologyRelations(%#v) = %#v, want %#v", test.relations, got, test.want)
			}

			if len(test.relations) > 0 && len(got) > 0 {
				test.relations[0]["Type"] = "mutated-input"
				if got[len(got)-1]["Type"] != "group_replication" {
					t.Fatalf("NormalizeTopologyRelations(%#v) retained the input map, got %#v", test.relations, got[len(got)-1])
				}
				facts := test.relations[0]["Facts"].(models.MetricGroupValue)
				facts["Source"] = "mutated-input"
				canonicalFacts := got[len(got)-1]["Facts"].(models.MetricGroupValue)
				if canonicalFacts["Source"] != "first" {
					t.Fatalf("NormalizeTopologyRelations(%#v) retained nested input maps, got %#v", test.relations, canonicalFacts)
				}
			}
		})
	}
}

func TestCompositeTopologyKeyIsBoundedAndStable(t *testing.T) {
	tests := []struct {
		name       string
		namespace  string
		identities []string
		want       string
	}{
		{
			name:       "uses the canonical single identity",
			namespace:  "multi-source",
			identities: []string{" source-a ", "source-a"},
			want:       "multi-source:source-a",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CompositeTopologyKey(test.namespace, test.identities); got != test.want {
				t.Fatalf("CompositeTopologyKey(%q, %#v) = %q, want %q", test.namespace, test.identities, got, test.want)
			}
		})
	}

	identities := make([]string, 0, 20)
	for index := 0; index < 20; index++ {
		identities = append(identities, "source-"+string(rune('a'+index))+"-"+strings.Repeat("identity-", 32))
	}
	reversed := append([]string(nil), identities...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}

	key := CompositeTopologyKey("multi-source", identities)
	if key != CompositeTopologyKey("multi-source", reversed) {
		t.Fatalf("CompositeTopologyKey(%q, %#v) = %q, want stable result for reordered identities", "multi-source", reversed, key)
	}
	if !strings.HasPrefix(key, "multi-source:sha256:") {
		t.Fatalf("CompositeTopologyKey(%q, 20 identities) = %q, want sha256 fallback", "multi-source", key)
	}
	if len(key) > maxTopologyKeyLength || len(key) >= 255 {
		t.Fatalf("CompositeTopologyKey(%q, 20 identities) length = %d, want at most %d and below 255", "multi-source", len(key), maxTopologyKeyLength)
	}
	for _, character := range key[len("multi-source:sha256:"):] {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			t.Fatalf("CompositeTopologyKey(%q, 20 identities) = %q, want lowercase SHA-256 hex", "multi-source", key)
		}
	}
}

func TestIncompleteRelationDiscoveryOmitsCanonicalSnapshot(t *testing.T) {
	topology := models.MetricGroupValue{
		"Relations": []models.MetricGroupValue{{"Type": "stale", "GroupKey": "stale", "MemberKey": "stale"}},
		"Facts":     models.MetricGroupValue{},
	}
	relations := []models.MetricGroupValue{{"Type": "async_replication", "GroupKey": "source-a", "MemberKey": "member-a"}}

	AttachTopologyRelations(topology, relations, false)

	if _, ok := topology["Relations"]; ok {
		t.Fatalf("AttachTopologyRelations(topology, relations, false) retained canonical Relations %#v, want omitted", topology["Relations"])
	}
	facts := topology["Facts"].(models.MetricGroupValue)
	want := []models.MetricGroupValue{{"Type": "async_replication", "GroupKey": "source-a", "MemberKey": "member-a"}}
	if got := facts["Relations"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("AttachTopologyRelations(topology, relations, false) Facts.Relations = %#v, want %#v", got, want)
	}
}

func TestBuildTopologyFromFactsPreservesConcurrentTopologyRelations(t *testing.T) {
	asyncStatus := []map[string]interface{}{
		{
			"Connection_name":       "clusterset-channel",
			"Master_Host":           "upstream.example.com",
			"Master_UUID":           "upstream-uuid",
			"Slave_IO_Running":      "Yes",
			"Slave_SQL_Running":     "Yes",
			"Seconds_Behind_Master": "0",
		},
	}

	tests := []struct {
		name      string
		facts     TopologyFacts
		primary   string
		relations []string
	}{
		{
			name: "galera with async channel",
			facts: TopologyFacts{
				Variables: map[string]interface{}{
					"wsrep_on":                 "ON",
					"wsrep_cluster_state_uuid": "galera-group",
					"wsrep_node_name":          "galera-1",
					"read_only":                "OFF",
				},
				Status: map[string]interface{}{
					"wsrep_ready":               "ON",
					"wsrep_connected":           "ON",
					"wsrep_cluster_status":      "Primary",
					"wsrep_local_state_comment": "Synced",
				},
				ReplicaStatus: asyncStatus,
			},
			primary:   "galera_cluster",
			relations: []string{"async_replication", "galera_cluster"},
		},
		{
			name: "group replication with ClusterSet channel",
			facts: TopologyFacts{
				Variables: map[string]interface{}{
					"server_uuid":                           "member-1",
					"group_replication_group_name":          "gr-group",
					"group_replication_single_primary_mode": "ON",
					"read_only":                             "OFF",
				},
				GroupMembers: []map[string]interface{}{
					{
						"MEMBER_ID":    "member-1",
						"MEMBER_HOST":  "db1.example.com",
						"MEMBER_STATE": "ONLINE",
						"MEMBER_ROLE":  "PRIMARY",
					},
				},
				ReplicaStatus: asyncStatus,
			},
			primary:   "group_replication",
			relations: []string{"async_replication", "group_replication"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			topology := BuildTopologyFromFacts(test.facts)
			if topology["Type"] != test.primary {
				t.Fatalf("expected primary topology %q, got %#v", test.primary, topology["Type"])
			}

			facts := topology["Facts"].(models.MetricGroupValue)
			relations, ok := facts["Relations"].([]models.MetricGroupValue)
			if !ok {
				t.Fatalf("expected concurrent topology relations in facts, got %#v", facts["Relations"])
			}
			if len(relations) != len(test.relations) {
				t.Fatalf("expected %d relations, got %#v", len(test.relations), relations)
			}
			for idx, relationType := range test.relations {
				if relations[idx]["Type"] != relationType {
					t.Fatalf("expected relation %d type %q, got %#v", idx, relationType, relations[idx]["Type"])
				}
			}
			var asyncFacts models.MetricGroupValue
			for _, relation := range relations {
				if relation["Type"] == "async_replication" {
					asyncFacts = relation["Facts"].(models.MetricGroupValue)
					break
				}
			}
			channels := asyncFacts["ReplicaChannels"].([]models.MetricGroupValue)
			if len(channels) != 1 || channels[0]["Master_UUID"] != "upstream-uuid" {
				t.Fatalf("expected full async channel relation, got %#v", channels)
			}
			if _, err := json.Marshal(topology); err != nil {
				t.Fatalf("expected concurrent relations to be JSON serializable: %v", err)
			}
		})
	}
}

func TestBuildTopologyFromFactsMarksUnhealthyGaleraAsNonReader(t *testing.T) {
	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables: map[string]interface{}{
			"server_uuid":              "server-uuid",
			"wsrep_on":                 "ON",
			"wsrep_cluster_state_uuid": "cluster-state",
			"wsrep_node_uuid":          "node-uuid",
			"read_only":                "OFF",
		},
		Status: map[string]interface{}{
			"wsrep_ready":          "ON",
			"wsrep_connected":      "OFF",
			"wsrep_cluster_status": "Primary",
		},
	})

	if topology["ReplicationState"] != "error" {
		t.Fatalf("expected unhealthy galera state, got %#v", topology["ReplicationState"])
	}
	if topology["IsReader"] != false {
		t.Fatalf("expected unhealthy galera node not to serve reads, got %#v", topology["IsReader"])
	}
}

func TestBuildTopologyFromFactsReturnsStandaloneForNoReplicationFacts(t *testing.T) {
	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables: map[string]interface{}{
			"server_uuid": "standalone-uuid",
			"hostname":    "standalone.example.com",
			"port":        "3310",
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
	if topology["MemberHost"] != "standalone.example.com" || topology["MemberPort"] != int64(3310) {
		t.Fatalf("BuildTopologyFromFacts(standalone) member endpoint = %#v:%#v, want standalone.example.com:3310", topology["MemberHost"], topology["MemberPort"])
	}
}

type fakeTopologyRows struct {
	columns    []string
	columnsErr error
	rows       [][]interface{}
	scanErr    error
	err        error
	index      int
}

func (r *fakeTopologyRows) Columns() ([]string, error) {
	return r.columns, r.columnsErr
}

func (r *fakeTopologyRows) Next() bool {
	return r.index < len(r.rows)
}

func (r *fakeTopologyRows) Scan(dest ...interface{}) error {
	if r.scanErr != nil {
		return r.scanErr
	}
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

func TestScanTopologyRowsReportsRowErrors(t *testing.T) {
	logger := *logging.Init("db-topology-test", false, false, io.Discard)
	defer logger.Close()

	tests := []struct {
		name string
		rows *fakeTopologyRows
	}{
		{
			name: "columns error",
			rows: &fakeTopologyRows{columnsErr: errors.New("columns failed")},
		},
		{
			name: "scan error",
			rows: &fakeTopologyRows{
				columns: []string{"Source_Host"},
				rows:    [][]interface{}{{"primary.example.com"}},
				scanErr: errors.New("scan failed"),
			},
		},
		{
			name: "iteration error",
			rows: &fakeTopologyRows{
				columns: []string{"Source_Host"},
				rows:    [][]interface{}{{"primary.example.com"}},
				err:     errors.New("cursor failed"),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, ok := scanTopologyRows(test.rows, logger)
			if ok || result != nil {
				t.Fatalf("expected failed row scan, got ok=%v result=%#v", ok, result)
			}
		})
	}
}
