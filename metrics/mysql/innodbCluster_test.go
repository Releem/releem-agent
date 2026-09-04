package mysql

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Releem/mysqlconfigurer/models"
	drivermysql "github.com/go-sql-driver/mysql"
)

type innodbMetadataFixture struct {
	SchemaVersion string                         `json:"schema_version"`
	Columns       map[string][]string            `json:"columns"`
	Rows          map[string][]map[string]string `json:"rows"`
}

func TestDiscoverInnoDBMetadataSchemaAbsentIsCompleteEmpty(t *testing.T) {
	queries := 0
	metadata, err := DiscoverInnoDBMetadata(func(query string, args ...any) ([]map[string]string, error) {
		queries++
		switch queries {
		case 1:
			if !strings.Contains(strings.ToLower(query), "information_schema.schemata") {
				t.Fatalf("DiscoverInnoDBMetadata() query 1 = %q, want schema visibility check", query)
			}
			return []map[string]string{}, nil
		case 2:
			if !strings.Contains(strings.ToLower(query), "show tables from") {
				t.Fatalf("DiscoverInnoDBMetadata() query 2 = %q, want direct metadata schema probe", query)
			}
			return nil, &drivermysql.MySQLError{Number: 1049, Message: "Unknown database"}
		default:
			t.Fatalf("DiscoverInnoDBMetadata() unexpected query %d = %q", queries, query)
			return nil, nil
		}
	})

	if err != nil {
		t.Fatalf("DiscoverInnoDBMetadata() error = %v", err)
	}
	if !metadata.Complete {
		t.Fatal("DiscoverInnoDBMetadata() Complete = false, want authoritative absence")
	}
	if len(metadata.Clusters) != 0 || len(metadata.Instances) != 0 || len(metadata.ClusterSets) != 0 {
		t.Fatalf("DiscoverInnoDBMetadata() = %#v, want complete empty metadata", metadata)
	}
	if queries != 2 {
		t.Fatalf("DiscoverInnoDBMetadata() queries = %d, want visibility check and direct probe", queries)
	}
}

func TestDiscoverInnoDBMetadataPermissionHiddenSchemaIsIncomplete(t *testing.T) {
	queries := 0
	denied := &drivermysql.MySQLError{Number: 1044, Message: "Access denied for database"}
	metadata, err := DiscoverInnoDBMetadata(func(query string, _ ...any) ([]map[string]string, error) {
		queries++
		switch queries {
		case 1:
			if !strings.Contains(strings.ToLower(query), "information_schema.schemata") {
				t.Fatalf("DiscoverInnoDBMetadata() query 1 = %q, want schema visibility check", query)
			}
			return []map[string]string{}, nil
		case 2:
			if !strings.Contains(strings.ToLower(query), "show tables from") {
				t.Fatalf("DiscoverInnoDBMetadata() query 2 = %q, want direct metadata schema probe", query)
			}
			return nil, denied
		default:
			t.Fatalf("DiscoverInnoDBMetadata() unexpected query %d = %q", queries, query)
			return nil, nil
		}
	})

	if !errors.Is(err, denied) {
		t.Fatalf("DiscoverInnoDBMetadata() error = %v, want wrapped typed access denial", err)
	}
	if metadata.Complete {
		t.Fatal("DiscoverInnoDBMetadata() Complete = true for permission-hidden metadata")
	}
	if queries != 2 {
		t.Fatalf("DiscoverInnoDBMetadata() queries = %d, want visibility check and direct probe", queries)
	}
}

func TestDiscoverInnoDBMetadataAccessDeniedIsIncomplete(t *testing.T) {
	denied := errors.New("select command denied")
	metadata, err := DiscoverInnoDBMetadata(func(string, ...any) ([]map[string]string, error) {
		return nil, denied
	})

	if !errors.Is(err, denied) {
		t.Fatalf("DiscoverInnoDBMetadata() error = %v, want wrapped access error", err)
	}
	if metadata.Complete {
		t.Fatal("DiscoverInnoDBMetadata() Complete = true after access denial")
	}
}

func TestDiscoverInnoDBMetadataUnknownShapeIsIncomplete(t *testing.T) {
	metadata, err := DiscoverInnoDBMetadata(func(query string, _ ...any) ([]map[string]string, error) {
		switch {
		case strings.Contains(strings.ToLower(query), "information_schema.schemata"):
			return []map[string]string{{"schema_name": "mysql_innodb_cluster_metadata"}}, nil
		case strings.Contains(strings.ToLower(query), "information_schema.columns"):
			return []map[string]string{{"table_name": "schema_version", "column_name": "major"}}, nil
		default:
			t.Fatalf("unexpected query before shape validation: %q", query)
			return nil, nil
		}
	})

	if err == nil || !strings.Contains(err.Error(), "metadata schema shape") {
		t.Fatalf("DiscoverInnoDBMetadata() error = %v, want actionable schema-shape error", err)
	}
	if metadata.Complete {
		t.Fatal("DiscoverInnoDBMetadata() Complete = true for unknown shape")
	}
}

func TestDiscoverInnoDBMetadataSchemaV1(t *testing.T) {
	fixture := loadInnoDBMetadataFixture(t, "schema_v1.json")
	metadata, err := DiscoverInnoDBMetadata(fixture.query(t))
	if err != nil {
		t.Fatalf("DiscoverInnoDBMetadata() error = %v", err)
	}

	if !metadata.Complete || metadata.SchemaVersion != "1.0.1" {
		t.Fatalf("DiscoverInnoDBMetadata() version/complete = %q/%v, want 1.0.1/true", metadata.SchemaVersion, metadata.Complete)
	}
	if len(metadata.Clusters) != 1 || metadata.Clusters[0].ID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("DiscoverInnoDBMetadata() clusters = %#v, want adapted 1.x Group Replication identity", metadata.Clusters)
	}
	if len(metadata.Instances) != 2 || metadata.Instances[0].Address != "legacy-1.example.com:3306" {
		t.Fatalf("DiscoverInnoDBMetadata() instances = %#v, want adapted 1.x instances", metadata.Instances)
	}
}

func TestDiscoverInnoDBMetadataSchemaV1BoundsGroupReplicationIdentity(t *testing.T) {
	fixture := loadInnoDBMetadataFixture(t, "schema_v1.json")
	rawGroupName := strings.Repeat("legacy-group-", maxTopologyKeyLength)
	attributes, err := json.Marshal(map[string]string{
		"group_replication_group_name": rawGroupName,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	fixture.Rows[metadataV1ReplicaSets][0]["attributes"] = string(attributes)

	metadata, err := DiscoverInnoDBMetadata(fixture.query(t))
	if err != nil {
		t.Fatalf("DiscoverInnoDBMetadata() error = %v", err)
	}

	wantGroupKey := CompositeTopologyKey("innodb-cluster", []string{rawGroupName})
	if metadata.Clusters[0].ID != wantGroupKey {
		t.Fatalf("DiscoverInnoDBMetadata() cluster ID = %q, want bounded identity %q", metadata.Clusters[0].ID, wantGroupKey)
	}
	if len(metadata.Clusters[0].ID) > maxTopologyKeyLength {
		t.Fatalf("DiscoverInnoDBMetadata() cluster ID length = %d, want at most %d", len(metadata.Clusters[0].ID), maxTopologyKeyLength)
	}
	if metadata.Clusters[0].GroupName != rawGroupName {
		t.Fatalf("DiscoverInnoDBMetadata() GroupName = %q, want raw metadata value", metadata.Clusters[0].GroupName)
	}
}

func TestDiscoverInnoDBMetadataSchemaV2(t *testing.T) {
	fixture := loadInnoDBMetadataFixture(t, "schema_v2.json")
	metadata, err := DiscoverInnoDBMetadata(fixture.query(t))
	if err != nil {
		t.Fatalf("DiscoverInnoDBMetadata() error = %v", err)
	}

	if !metadata.Complete || metadata.SchemaVersion != "2.1.0" {
		t.Fatalf("DiscoverInnoDBMetadata() version/complete = %q/%v, want 2.1.0/true", metadata.SchemaVersion, metadata.Complete)
	}
	if len(metadata.Clusters) != 2 || len(metadata.Instances) != 3 || len(metadata.ClusterSets) != 3 {
		t.Fatalf("DiscoverInnoDBMetadata() row counts = %d/%d/%d, want 2/3/3", len(metadata.Clusters), len(metadata.Instances), len(metadata.ClusterSets))
	}
	if metadata.Clusters[0].GroupName != "aaaaaaaa-1111-1111-1111-aaaaaaaaaaaa" {
		t.Fatalf("DiscoverInnoDBMetadata() GroupName = %q, want preserved physical group identifier", metadata.Clusters[0].GroupName)
	}
	if metadata.ClusterSets[1].PrimaryClusterID != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("DiscoverInnoDBMetadata() replica ClusterSet = %#v, want primary cluster identity", metadata.ClusterSets[1])
	}
}

func TestDiscoverInnoDBMetadataSchemaV2MissingClusterSetShapeIsIncomplete(t *testing.T) {
	fixture := loadInnoDBMetadataFixture(t, "schema_v2.json")
	delete(fixture.Columns, "v2_cs_clustersets")
	delete(fixture.Columns, "v2_cs_members")
	delete(fixture.Rows, "v2_cs_clustersets")
	delete(fixture.Rows, "v2_cs_members")

	metadata, err := DiscoverInnoDBMetadata(fixture.query(t))
	if err == nil || !strings.Contains(err.Error(), "metadata schema shape") {
		t.Fatalf("DiscoverInnoDBMetadata() error = %v, want missing 2.1 ClusterSet shape", err)
	}
	if metadata.Complete {
		t.Fatal("DiscoverInnoDBMetadata() Complete = true with missing 2.1 ClusterSet views")
	}
}

func TestBuildInnoDBClusterRelationsSchemaV1(t *testing.T) {
	metadata := discoverInnoDBFixture(t, "schema_v1.json")
	base := groupReplicationBase("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "primary", "healthy")
	base["PrimaryHost"] = "legacy-1.example.com"
	base["PrimaryPort"] = int64(3306)

	relations := BuildInnoDBRelations(base, metadata)
	if len(relations) != 1 {
		t.Fatalf("BuildInnoDBRelations() = %#v, want one InnoDB Cluster relation", relations)
	}
	relation := relations[0]
	assertTopologyFields(t, relation, map[string]any{
		"Type":             "innodb_cluster",
		"Role":             "primary",
		"GroupKey":         "11111111-1111-1111-1111-111111111111",
		"MemberKey":        "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		"MemberHost":       "legacy-1.example.com",
		"MemberPort":       int64(3306),
		"PrimaryHost":      "legacy-1.example.com",
		"PrimaryPort":      int64(3306),
		"IsWriter":         true,
		"IsReader":         true,
		"ReplicationState": "healthy",
	})
	facts := relation["Facts"].(models.MetricGroupValue)
	assertTopologyFields(t, facts, map[string]any{
		"MetadataVersion":   "1.0.1",
		"MetadataClusterID": "1",
		"ClusterName":       "legacyCluster",
		"InstanceID":        "101",
		"InstanceLabel":     "legacy-1.example.com:3306",
	})
}

func TestBuildInnoDBClusterRelationsSchemaV2SinglePrimary(t *testing.T) {
	metadata := discoverInnoDBFixture(t, "schema_v2.json")
	base := groupReplicationBase("dddddddd-dddd-dddd-dddd-dddddddddddd", "replica", "healthy")
	base["PrimaryMemberKey"] = "ffffffff-ffff-ffff-ffff-ffffffffffff"
	base["PrimaryHost"] = "replica-2.example.com"
	base["PrimaryPort"] = int64(3306)

	relations := BuildInnoDBRelations(base, metadata)
	cluster := relationByType(t, relations, "innodb_cluster")
	assertTopologyFields(t, cluster, map[string]any{
		"Role":             "secondary",
		"GroupKey":         "33333333-3333-3333-3333-333333333333",
		"MemberKey":        "dddddddd-dddd-dddd-dddd-dddddddddddd",
		"PrimaryMemberKey": "ffffffff-ffff-ffff-ffff-ffffffffffff",
		"ParentGroupKey":   nil,
		"IsWriter":         false,
		"IsReader":         true,
	})
}

func TestBuildInnoDBClusterRelationsLiveMultiPrimaryOverridesMetadataSinglePrimary(t *testing.T) {
	metadata := discoverInnoDBFixture(t, "schema_v2.json")
	metadata.Clusters[0].PrimaryMode = "pm"
	base := groupReplicationBase("cccccccc-cccc-cccc-cccc-cccccccccccc", "multi_primary", "healthy")
	base["PrimaryMemberKey"] = "must-be-cleared"
	base["PrimaryHost"] = "must-be-cleared.example.com"
	base["PrimaryPort"] = int64(3306)

	relation := relationByType(t, BuildInnoDBRelations(base, metadata), "innodb_cluster")
	assertTopologyFields(t, relation, map[string]any{
		"Role":             "multi_primary",
		"PrimaryMemberKey": nil,
		"PrimaryHost":      nil,
		"PrimaryPort":      nil,
		"IsWriter":         true,
		"IsReader":         true,
	})

	base["ReplicationState"] = "error"
	base["IsWriter"] = true
	base["IsReader"] = true
	relation = relationByType(t, BuildInnoDBRelations(base, metadata), "innodb_cluster")
	if relation["IsWriter"] != false || relation["IsReader"] != false {
		t.Fatalf("BuildInnoDBRelations(error member) availability = writer:%#v reader:%#v, want fail-closed", relation["IsWriter"], relation["IsReader"])
	}
}

func TestBuildInnoDBClusterRelationsLiveSinglePrimaryOverridesMetadataMultiPrimary(t *testing.T) {
	metadata := discoverInnoDBFixture(t, "schema_v2.json")
	metadata.Clusters[0].PrimaryMode = "mm"
	memberID := "cccccccc-cccc-cccc-cccc-cccccccccccc"
	base := groupReplicationBase(memberID, "primary", "healthy")
	base["PrimaryMemberKey"] = memberID
	base["PrimaryHost"] = "live-primary.example.com"
	base["PrimaryPort"] = int64(4406)

	relation := relationByType(t, BuildInnoDBRelations(base, metadata), "innodb_cluster")
	assertTopologyFields(t, relation, map[string]any{
		"Role":             "primary",
		"PrimaryMemberKey": memberID,
		"PrimaryHost":      "live-primary.example.com",
		"PrimaryPort":      int64(4406),
		"IsWriter":         true,
		"IsReader":         true,
	})
	facts := relation["Facts"].(models.MetricGroupValue)
	if facts["PrimaryMode"] != "mm" {
		t.Fatalf("BuildInnoDBRelations() Facts.PrimaryMode = %#v, want diagnostic metadata value", facts["PrimaryMode"])
	}
}

func TestBuildInnoDBClusterRelationsGatesWriterOnLiveRole(t *testing.T) {
	metadata := discoverInnoDBFixture(t, "schema_v2.json")
	base := groupReplicationBase("cccccccc-cccc-cccc-cccc-cccccccccccc", "replica", "healthy")
	base["IsWriter"] = true

	relation := relationByType(t, BuildInnoDBRelations(base, metadata), "innodb_cluster")
	if relation["Role"] != "secondary" || relation["IsWriter"] != false {
		t.Fatalf("BuildInnoDBRelations() role/writer = %#v/%#v, want secondary/false", relation["Role"], relation["IsWriter"])
	}
}

func TestBuildInnoDBClusterRelationsBoundsAddressFallback(t *testing.T) {
	host := strings.Repeat("long-host-segment", 20) + ".example.com"
	metadata := InnoDBMetadata{
		SchemaVersion: "2.1.0",
		Complete:      true,
		Clusters: []InnoDBCluster{
			{ID: "cluster-id", MetadataID: "cluster-id", GroupName: "physical-group", PrimaryMode: "pm"},
		},
		Instances: []InnoDBInstance{
			{ClusterID: "cluster-id", Label: "address-only", Address: host + ":3306"},
		},
	}
	base := groupReplicationBase("", "primary", "healthy")
	base["GroupKey"] = "physical-group"
	base["MemberHost"] = host
	base["MemberPort"] = int64(3306)

	relation := relationByType(t, BuildInnoDBRelations(base, metadata), "innodb_cluster")
	memberKey := relation["MemberKey"].(string)
	if len(memberKey) > maxTopologyKeyLength || !strings.HasPrefix(memberKey, "innodb-member:sha256:") {
		t.Fatalf("BuildInnoDBRelations() MemberKey = %q (length %d), want bounded address identity", memberKey, len(memberKey))
	}
}

func TestBuildClusterSetRelations(t *testing.T) {
	metadata := discoverInnoDBFixture(t, "schema_v2.json")

	tests := []struct {
		name        string
		memberID    string
		physical    string
		wantRole    string
		wantParent  string
		wantWriter  bool
		wantPrimary any
		wantState   string
	}{
		{
			name:        "primary cluster",
			memberID:    "cccccccc-cccc-cccc-cccc-cccccccccccc",
			physical:    "primary",
			wantRole:    "primary_cluster",
			wantWriter:  true,
			wantPrimary: "cccccccc-cccc-cccc-cccc-cccccccccccc",
			wantState:   "healthy",
		},
		{
			name:        "replica cluster",
			memberID:    "dddddddd-dddd-dddd-dddd-dddddddddddd",
			physical:    "primary",
			wantRole:    "replica_cluster",
			wantParent:  "22222222-2222-2222-2222-222222222222",
			wantWriter:  false,
			wantPrimary: nil,
			wantState:   "healthy",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := groupReplicationBase(test.memberID, test.physical, "healthy")
			relation := relationByType(t, BuildInnoDBRelations(base, metadata), "innodb_clusterset")
			assertTopologyFields(t, relation, map[string]any{
				"GroupKey":         "44444444-4444-4444-4444-444444444444",
				"ParentGroupKey":   nullableString(test.wantParent),
				"Role":             test.wantRole,
				"IsWriter":         test.wantWriter,
				"PrimaryMemberKey": test.wantPrimary,
				"ReplicationState": test.wantState,
			})
			facts := relation["Facts"].(models.MetricGroupValue)
			assertTopologyFields(t, facts, map[string]any{
				"MetadataVersion": "2.1.0",
				"ClusterSetName":  "productionSet",
				"ChannelName":     "clusterset_replication",
				"ChannelState":    "unknown",
			})
		})
	}
}

func TestBuildClusterSetRelationsRequiresAuthoritativePrimary(t *testing.T) {
	metadata := discoverInnoDBFixture(t, "schema_v2.json")
	for index := range metadata.ClusterSets {
		metadata.ClusterSets[index].PrimaryClusterID = ""
	}
	base := groupReplicationBase("cccccccc-cccc-cccc-cccc-cccccccccccc", "primary", "healthy")

	relation := relationByType(t, BuildInnoDBRelations(base, metadata), "innodb_clusterset")
	assertTopologyFields(t, relation, map[string]any{
		"PrimaryMemberKey": nil,
		"PrimaryHost":      nil,
		"PrimaryPort":      nil,
		"IsWriter":         false,
	})
}

func TestBuildInnoDBClusterRelationsIgnoresRemovedInstanceRows(t *testing.T) {
	metadata := discoverInnoDBFixture(t, "schema_v2.json")
	metadata.Instances = append([]InnoDBInstance{
		{
			ID:         "stale-duplicate",
			ClusterID:  "22222222-2222-2222-2222-222222222222",
			ServerUUID: "dddddddd-dddd-dddd-dddd-dddddddddddd",
			Label:      "stale-copy",
			Address:    "stale.example.com:3306",
		},
	}, metadata.Instances...)
	base := groupReplicationBase("dddddddd-dddd-dddd-dddd-dddddddddddd", "primary", "healthy")
	base["GroupKey"] = "bbbbbbbb-2222-2222-2222-bbbbbbbbbbbb"

	relations := BuildInnoDBRelations(base, metadata)
	if len(relations) != 2 {
		t.Fatalf("BuildInnoDBRelations() = %#v, want current cluster and ClusterSet only", relations)
	}
	for _, relation := range relations {
		if relation["GroupKey"] == "99999999-9999-9999-9999-999999999999" || relation["MemberKey"] == "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee" {
			t.Fatalf("BuildInnoDBRelations() retained removed metadata row: %#v", relation)
		}
	}
	cluster := relationByType(t, relations, "innodb_cluster")
	if cluster["GroupKey"] != "33333333-3333-3333-3333-333333333333" {
		t.Fatalf("BuildInnoDBRelations() selected stale duplicate cluster %#v", cluster)
	}
}

func TestBuildTopologyFromFactsPreservesInnoDBClusterSetAndAsyncRelations(t *testing.T) {
	metadata := discoverInnoDBFixture(t, "schema_v2.json")
	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables: map[string]interface{}{
			"server_uuid":                           "dddddddd-dddd-dddd-dddd-dddddddddddd",
			"group_replication_group_name":          "bbbbbbbb-2222-2222-2222-bbbbbbbbbbbb",
			"group_replication_single_primary_mode": "ON",
			"read_only":                             "ON",
		},
		GroupMembers: []map[string]interface{}{
			{
				"MEMBER_ID":    "dddddddd-dddd-dddd-dddd-dddddddddddd",
				"MEMBER_HOST":  "replica-1.example.com",
				"MEMBER_PORT":  "3306",
				"MEMBER_STATE": "ONLINE",
				"MEMBER_ROLE":  "PRIMARY",
			},
		},
		ReplicaStatus: []map[string]interface{}{
			{
				"Channel_Name":          "clusterset_replication",
				"Source_Host":           "primary-1.example.com",
				"Source_UUID":           "cccccccc-cccc-cccc-cccc-cccccccccccc",
				"Replica_IO_Running":    "Yes",
				"Replica_SQL_Running":   "Yes",
				"Seconds_Behind_Source": "0",
			},
		},
		InnoDBMetadata: &metadata,
	})

	facts := topology["Facts"].(models.MetricGroupValue)
	relations := facts["Relations"].([]models.MetricGroupValue)
	gotTypes := make([]string, 0, len(relations))
	for _, relation := range relations {
		gotTypes = append(gotTypes, relation["Type"].(string))
	}
	wantTypes := []string{"async_replication", "group_replication", "innodb_cluster", "innodb_clusterset"}
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Fatalf("BuildTopologyFromFacts() relation types = %#v, want %#v", gotTypes, wantTypes)
	}
	if topology["Type"] != "group_replication" {
		t.Fatalf("BuildTopologyFromFacts() compatibility Type = %#v, want unchanged physical projection", topology["Type"])
	}
	clusterSet := relationByType(t, relations, "innodb_clusterset")
	if clusterSet["ReplicationState"] != "healthy" {
		t.Fatalf("BuildTopologyFromFacts() ClusterSet state = %#v, want async channel health", clusterSet["ReplicationState"])
	}
	clusterSetFacts := clusterSet["Facts"].(models.MetricGroupValue)
	if clusterSetFacts["ChannelState"] != "healthy" {
		t.Fatalf("BuildTopologyFromFacts() ClusterSet ChannelState = %#v, want healthy", clusterSetFacts["ChannelState"])
	}
}

func TestBuildTopologyFromFactsKeepsGRStateWhenClusterSetChannelStopped(t *testing.T) {
	metadata := discoverInnoDBFixture(t, "schema_v2.json")
	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables: map[string]interface{}{
			"server_uuid":                           "dddddddd-dddd-dddd-dddd-dddddddddddd",
			"group_replication_group_name":          "bbbbbbbb-2222-2222-2222-bbbbbbbbbbbb",
			"group_replication_single_primary_mode": "ON",
			"read_only":                             "ON",
		},
		GroupMembers: []map[string]interface{}{
			{
				"MEMBER_ID":    "dddddddd-dddd-dddd-dddd-dddddddddddd",
				"MEMBER_HOST":  "replica-1.example.com",
				"MEMBER_PORT":  "3306",
				"MEMBER_STATE": "ONLINE",
				"MEMBER_ROLE":  "PRIMARY",
			},
		},
		ReplicaStatus: []map[string]interface{}{
			{
				"Channel_Name":        "clusterset_replication",
				"Source_Host":         "primary-1.example.com",
				"Source_UUID":         "cccccccc-cccc-cccc-cccc-cccccccccccc",
				"Replica_IO_Running":  "No",
				"Replica_SQL_Running": "Yes",
			},
		},
		InnoDBMetadata: &metadata,
	})

	facts := topology["Facts"].(models.MetricGroupValue)
	relations := facts["Relations"].([]models.MetricGroupValue)
	clusterSet := relationByType(t, relations, "innodb_clusterset")
	assertTopologyFields(t, clusterSet, map[string]any{
		"ReplicationState": "healthy",
		"IsReader":         true,
		"IsWriter":         false,
	})
	clusterSetFacts := clusterSet["Facts"].(models.MetricGroupValue)
	if clusterSetFacts["ChannelState"] != "stopped" {
		t.Fatalf("BuildTopologyFromFacts() ClusterSet Facts.ChannelState = %#v, want stopped", clusterSetFacts["ChannelState"])
	}
}

func TestBuildTopologyFromFactsRetainsInnoDBFactsWhenMetadataIsIncomplete(t *testing.T) {
	metadata := discoverInnoDBFixture(t, "schema_v1.json")
	metadata.Complete = false
	complete := true
	topology := BuildTopologyFromFacts(TopologyFacts{
		Variables: map[string]interface{}{
			"server_uuid":                           "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
			"group_replication_group_name":          "11111111-1111-1111-1111-111111111111",
			"group_replication_single_primary_mode": "ON",
			"read_only":                             "OFF",
		},
		GroupMembers: []map[string]interface{}{
			{
				"MEMBER_ID":    "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
				"MEMBER_HOST":  "legacy-1.example.com",
				"MEMBER_PORT":  "3306",
				"MEMBER_STATE": "ONLINE",
				"MEMBER_ROLE":  "PRIMARY",
			},
		},
		InnoDBMetadata:            &metadata,
		RelationDiscoveryComplete: &complete,
	})

	if _, ok := topology["Relations"]; ok {
		t.Fatalf("BuildTopologyFromFacts() canonical Relations = %#v, want omitted for incomplete metadata", topology["Relations"])
	}
	facts := topology["Facts"].(models.MetricGroupValue)
	relations := facts["Relations"].([]models.MetricGroupValue)
	if len(relations) != 2 || relations[1]["Type"] != "innodb_cluster" {
		t.Fatalf("BuildTopologyFromFacts() Facts.Relations = %#v, want physical and usable partial semantic facts", relations)
	}
}

func loadInnoDBMetadataFixture(t *testing.T, name string) innodbMetadataFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "innodb_cluster", name))
	if err != nil {
		t.Fatalf("read metadata fixture: %v", err)
	}

	var fixture innodbMetadataFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode metadata fixture: %v", err)
	}
	return fixture
}

func discoverInnoDBFixture(t *testing.T, name string) InnoDBMetadata {
	t.Helper()
	fixture := loadInnoDBMetadataFixture(t, name)
	metadata, err := DiscoverInnoDBMetadata(fixture.query(t))
	if err != nil {
		t.Fatalf("DiscoverInnoDBMetadata(%s) error = %v", name, err)
	}
	return metadata
}

func (f innodbMetadataFixture) query(t *testing.T) topologyRowsQuery {
	t.Helper()
	inspected := false
	return func(query string, args ...any) ([]map[string]string, error) {
		lowerQuery := strings.ToLower(query)
		switch {
		case strings.Contains(lowerQuery, "information_schema.schemata"):
			return []map[string]string{{"schema_name": "mysql_innodb_cluster_metadata"}}, nil
		case strings.Contains(lowerQuery, "information_schema.columns"):
			inspected = true
			rows := make([]map[string]string, 0)
			tables := make([]string, 0, len(f.Columns))
			for table := range f.Columns {
				tables = append(tables, table)
			}
			sort.Strings(tables)
			for _, table := range tables {
				for _, column := range f.Columns[table] {
					rows = append(rows, map[string]string{"table_name": table, "column_name": column})
				}
			}
			return rows, nil
		default:
			if !inspected {
				t.Fatalf("issued metadata SELECT before column introspection: %q", query)
			}
			for table, rows := range f.Rows {
				if strings.Contains(lowerQuery, ".`"+table+"`") || strings.Contains(lowerQuery, "."+table) {
					return rows, nil
				}
			}
			t.Fatalf("unexpected metadata query %q with args %#v", query, args)
			return nil, nil
		}
	}
}

func groupReplicationBase(memberID string, role string, state string) models.MetricGroupValue {
	isHealthy := state == "healthy"
	return models.MetricGroupValue{
		"Type":                  "group_replication",
		"Role":                  role,
		"GroupKey":              "group-replication-id",
		"MemberKey":             memberID,
		"MemberHost":            nil,
		"MemberPort":            nil,
		"PrimaryMemberKey":      memberID,
		"PrimaryHost":           nil,
		"PrimaryPort":           nil,
		"IsWriter":              isHealthy && (role == "primary" || role == "multi_primary"),
		"IsReader":              isHealthy,
		"ReadOnly":              role != "primary" && role != "multi_primary",
		"SuperReadOnly":         false,
		"ReplicationLagSeconds": nil,
		"ReplicationState":      state,
		"Facts":                 models.MetricGroupValue{},
	}
}

func relationByType(t *testing.T, relations []models.MetricGroupValue, relationType string) models.MetricGroupValue {
	t.Helper()
	for _, relation := range relations {
		if relation["Type"] == relationType {
			return relation
		}
	}
	t.Fatalf("relation type %q not found in %#v", relationType, relations)
	return nil
}

func assertTopologyFields(t *testing.T, got models.MetricGroupValue, want map[string]any) {
	t.Helper()
	for key, wantValue := range want {
		if !reflect.DeepEqual(got[key], wantValue) {
			t.Errorf("%s = %#v, want %#v", key, got[key], wantValue)
		}
	}
}
