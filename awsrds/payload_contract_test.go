package awsrds_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Releem/mysqlconfigurer/awsrds"
	"github.com/Releem/mysqlconfigurer/config"
	metricspkg "github.com/Releem/mysqlconfigurer/metrics"
	"github.com/Releem/mysqlconfigurer/metrics/system"
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logging "github.com/google/logger"
)

type contractPayload struct {
	System struct {
		Info struct {
			Host map[string]any
		}
	}
	DB struct {
		Conf struct {
			Variables map[string]any
		}
	}
	ReleemAgent struct {
		Conf map[string]any
	}
}

type contractCase struct {
	metadata awsrds.Metadata
	config   config.Config
	capacity float64
}

func TestAuroraPayloadContract(t *testing.T) {
	cases := map[string]contractCase{
		"aurora_mysql_provisioned_writer.json":      newContractCase("orders-writer", "db-resource-mysql-writer", "db.r7g.large", "aurora-mysql", "orders-instance-pg", "orders-cluster", "orders-cluster-pg", true, 0),
		"aurora_mysql_provisioned_reader.json":      newContractCase("orders-reader", "db-resource-mysql-reader", "db.r7g.large", "aurora-mysql", "orders-instance-pg", "orders-cluster", "orders-cluster-pg", false, 0),
		"aurora_mysql_serverless_writer.json":       newContractCase("orders-serverless-writer", "db-resource-mysql-serverless-writer", "db.serverless", "aurora-mysql", "orders-serverless-instance-pg", "orders-serverless-cluster", "orders-serverless-cluster-pg", true, 8),
		"aurora_mysql_serverless_reader.json":       newContractCase("orders-serverless-reader", "db-resource-mysql-serverless-reader", "db.serverless", "aurora-mysql", "orders-serverless-instance-pg", "orders-serverless-cluster", "orders-serverless-cluster-pg", false, 4),
		"aurora_postgresql_provisioned_writer.json": newContractCase("analytics-writer", "db-resource-pg-writer", "db.r7g.large", "aurora-postgresql", "analytics-instance-pg", "analytics-cluster", "analytics-cluster-pg", true, 0),
		"aurora_postgresql_provisioned_reader.json": newContractCase("analytics-reader", "db-resource-pg-reader", "db.r7g.large", "aurora-postgresql", "analytics-instance-pg", "analytics-cluster", "analytics-cluster-pg", false, 0),
		"aurora_postgresql_serverless_writer.json":  newContractCase("analytics-serverless-writer", "db-resource-pg-serverless-writer", "db.serverless", "aurora-postgresql", "analytics-serverless-instance-pg", "analytics-serverless-cluster", "analytics-serverless-cluster-pg", true, 8),
		"aurora_postgresql_serverless_reader.json":  newContractCase("analytics-serverless-reader", "db-resource-pg-serverless-reader", "db.serverless", "aurora-postgresql", "analytics-serverless-instance-pg", "analytics-serverless-cluster", "analytics-serverless-cluster-pg", false, 4),
	}

	wantNames := make([]string, 0, len(cases)+2)
	for name := range cases {
		wantNames = append(wantNames, name)
	}
	wantNames = append(wantNames, "legacy_aurora_mysql.json", "legacy_aurora_postgresql.json")
	sort.Strings(wantNames)

	entries, err := os.ReadDir(filepath.Join("testdata", "payloads"))
	if err != nil {
		t.Fatalf("read payload fixture directory: %v", err)
	}
	gotNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			gotNames = append(gotNames, entry.Name())
		}
	}
	sort.Strings(gotNames)
	if fmt.Sprint(gotNames) != fmt.Sprint(wantNames) {
		t.Fatalf("payload fixtures = %v, want exactly %v", gotNames, wantNames)
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			payload := readContractPayload(t, name)
			assertSecretFree(t, payload)
			assertExactKeys(t, "Host", payload.System.Info.Host, []string{
				"DBClusterIdentifier",
				"DBClusterParameterGroup",
				"DBInstanceClass",
				"DBInstanceIdentifier",
				"DBInstanceResourceID",
				"DBParameterGroup",
				"Engine",
				"EngineMode",
				"IsClusterWriter",
				"ServerlessDatabaseCapacity",
			})
			assertExactKeys(t, "ReleemAgent.Conf", payload.ReleemAgent.Conf, []string{
				"AwsRDSClusterParameterGroup",
				"AwsRDSDB",
				"AwsRDSParameterGroup",
			})

			emittedHost, emittedConf := emitContractFields(t, tc)
			assertFieldsEqual(t, "Host", payload.System.Info.Host, emittedHost)
			assertFieldsEqual(t, "ReleemAgent.Conf", payload.ReleemAgent.Conf, emittedConf)
		})
	}

	for _, name := range []string{"legacy_aurora_mysql.json", "legacy_aurora_postgresql.json"} {
		t.Run(name, func(t *testing.T) {
			payload := readContractPayload(t, name)
			assertSecretFree(t, payload)
			if len(payload.System.Info.Host) != 0 {
				t.Fatalf("legacy Host = %v, want no additive Aurora fields", payload.System.Info.Host)
			}
			assertExactKeys(t, "legacy ReleemAgent.Conf", payload.ReleemAgent.Conf, []string{"AwsRDSParameterGroup"})
			if _, ok := payload.DB.Conf.Variables["aurora_version"]; !ok {
				t.Fatal("legacy DB.Conf.Variables must contain aurora_version")
			}
		})
	}
}

func TestGoldenPayloadSafetyValidation(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]interface{}
		wantErr bool
	}{
		{
			name: "valid synthetic topology",
			payload: map[string]interface{}{
				"MemberHost":      "writer.db.example",
				"InstanceAddress": "writer.db.example:3306",
				"ClusterEndpoint": "writer.cluster.example",
				"DBClusterARN":    "arn:aws:rds:us-east-1:fixture:cluster:inventory",
			},
		},
		{name: "live DNS name", payload: map[string]interface{}{"MemberHost": "writer.production.example.com"}, wantErr: true},
		{name: "IPv4 endpoint", payload: map[string]interface{}{"Endpoint": "192.0.2.10"}, wantErr: true},
		{name: "IPv6 endpoint", payload: map[string]interface{}{"Endpoint": "[2001:db8::10]:3306"}, wantErr: true},
		{name: "AWS account ARN", payload: map[string]interface{}{"DBClusterARN": "arn:aws:rds:us-east-1:123456789012:cluster:inventory"}, wantErr: true},
		{name: "non-synthetic ARN", payload: map[string]interface{}{"DBClusterARN": "arn:aws:rds:us-east-1:customer:cluster:inventory"}, wantErr: true},
		{name: "non-synthetic ARN in generic identity field", payload: map[string]interface{}{"ParentGroupKey": "arn:aws:rds:us-east-1:customer:cluster:inventory"}, wantErr: true},
		{name: "AWS access key ID", payload: map[string]interface{}{"Value": "AKIAABCDEFGHIJKLMNOP"}, wantErr: true},
		{name: "AWS secret-shaped value", payload: map[string]interface{}{"Value": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}, wantErr: true},
		{name: "credential field", payload: map[string]interface{}{"Password": "fixture-password"}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := json.Marshal(test.payload)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			err = validateOfflineGoldenPayload(payload)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateOfflineGoldenPayload() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestMetadataLogFieldsOmitSensitiveTopologyData(t *testing.T) {
	t.Parallel()

	metadata := awsrds.Metadata{
		DBInstanceIdentifier:    "orders-writer",
		DBInstanceARN:           "arn:aws:rds:us-east-1:123456789012:db:orders-writer",
		DBInstanceResourceID:    "db-private-resource",
		Endpoint:                "orders-writer.private.internal",
		DBClusterIdentifier:     "orders-cluster",
		DBClusterARN:            "arn:aws:rds:us-east-1:123456789012:cluster:orders-cluster",
		DBClusterResourceID:     "cluster-private-resource",
		ClusterEndpoint:         "orders-cluster.private.internal",
		ClusterReaderEndpoint:   "orders-cluster-ro.private.internal",
		GlobalClusterIdentifier: "orders-global",
		GlobalClusterARN:        "arn:aws:rds::123456789012:global-cluster:orders-global",
		GlobalClusterResourceID: "global-private-resource",
		ClusterMembers: []awsrds.ClusterMember{{
			DBInstanceIdentifier: "orders-reader",
			DBInstanceARN:        "arn:aws:rds:us-east-1:123456789012:db:orders-reader",
			DBInstanceResourceID: "db-reader-private-resource",
			Endpoint:             "orders-reader.private.internal",
		}},
		GlobalClusterMembers: []awsrds.GlobalClusterMember{{
			DBClusterARN: "arn:aws:rds:us-west-2:123456789012:cluster:orders-secondary",
		}},
		HasReadReplicaSource: true,
		ReadReplicaSource: awsrds.RelatedDBInstance{
			DBInstanceARN:        "arn:aws:rds:us-east-1:123456789012:db:orders-source",
			DBInstanceResourceID: "db-source-private-resource",
			Endpoint:             "orders-source.private.internal",
		},
		ReadReplicas: []awsrds.RelatedDBInstance{{
			DBInstanceARN:        "arn:aws:rds:us-east-1:123456789012:db:orders-child",
			DBInstanceResourceID: "db-child-private-resource",
			Endpoint:             "orders-child.private.internal",
		}},
	}

	fields := metadata.LogFields()
	assertExactKeys(t, "Metadata.LogFields", fields, []string{
		"topology_incomplete",
		"db_cluster_identifier",
		"db_cluster_parameter_group",
		"db_cluster_parameter_group_status",
		"db_instance_class",
		"db_instance_identifier",
		"db_parameter_group",
		"db_parameter_group_status",
		"engine",
		"engine_mode",
		"global_cluster_identifier",
		"instance_status",
		"is_cluster_writer",
		"is_serverless_v2",
		"multi_az",
		"partition",
		"region",
		"serverless_v2_max_capacity",
		"serverless_v2_min_capacity",
	})

	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("json.Marshal(Metadata.LogFields()) unexpected error: %v", err)
	}
	normalized := strings.ToLower(string(encoded))
	for _, forbidden := range []string{
		"123456789012",
		"arn:aws",
		"private.internal",
		"private-resource",
		"endpoint",
		"credential",
		"password",
		"raw_payload",
	} {
		if strings.Contains(normalized, forbidden) {
			t.Errorf("Metadata.LogFields() JSON contains forbidden fragment %q: %s", forbidden, encoded)
		}
	}
}

func newContractCase(identifier, resourceID, instanceClass, engine, instanceGroup, clusterID, clusterGroup string, writer bool, capacity float64) contractCase {
	return contractCase{
		metadata: awsrds.Metadata{
			DBInstanceIdentifier:    identifier,
			DBInstanceResourceID:    resourceID,
			DBInstanceClass:         instanceClass,
			Engine:                  engine,
			EngineMode:              "provisioned",
			DBParameterGroup:        instanceGroup,
			DBClusterIdentifier:     clusterID,
			DBClusterParameterGroup: clusterGroup,
			IsClusterWriter:         writer,
		},
		config: config.Config{
			AwsRDSDB:                    identifier,
			AwsRDSParameterGroup:        instanceGroup,
			AwsRDSClusterParameterGroup: clusterGroup,
		},
		capacity: capacity,
	}
}

func readContractPayload(t *testing.T, name string) contractPayload {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "payloads", name))
	if err != nil {
		t.Fatalf("read payload fixture %q: %v", name, err)
	}
	var payload contractPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode payload fixture %q: %v", name, err)
	}
	return payload
}

func emitContractFields(t *testing.T, tc contractCase) (map[string]any, map[string]any) {
	t.Helper()
	logger := *logging.Init("aurora-payload-contract-test", false, false, io.Discard)
	metrics := &models.Metrics{}
	client := contractCloudWatchClient(t, tc.capacity)
	if err := system.NewAWSRDSEnhancedMetricsGatherer(
		logger,
		client,
		&tc.config,
		tc.metadata,
		func(context.Context) (awsrds.Metadata, error) { return tc.metadata, nil },
	).GetMetrics(metrics); err != nil {
		t.Fatalf("emit Host metrics: %v", err)
	}
	if err := metricspkg.NewAgentMetricsGatherer(logger, &tc.config).GetMetrics(metrics); err != nil {
		t.Fatalf("emit ReleemAgent.Conf: %v", err)
	}

	host, ok := metrics.System.Info["Host"].(models.MetricGroupValue)
	if !ok {
		t.Fatalf("emitted Host = %#v, want MetricGroupValue", metrics.System.Info["Host"])
	}
	conf := map[string]any{
		"AwsRDSDB":                    metrics.ReleemAgent.Conf.AwsRDSDB,
		"AwsRDSParameterGroup":        metrics.ReleemAgent.Conf.AwsRDSParameterGroup,
		"AwsRDSClusterParameterGroup": metrics.ReleemAgent.Conf.AwsRDSClusterParameterGroup,
	}
	return host, conf
}

func contractCloudWatchClient(t *testing.T, capacity float64) *cloudwatchlogs.Client {
	t.Helper()
	client := contractHTTPClient{do: func(*http.Request) (*http.Response, error) {
		message, err := json.Marshal(map[string]any{"serverlessDatabaseCapacity": capacity})
		if err != nil {
			return nil, err
		}
		var response bytes.Buffer
		if err := json.NewEncoder(&response).Encode(map[string]any{
			"events": []map[string]any{{"message": string(message)}},
		}); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/x-amz-json-1.1"}},
			Body:       io.NopCloser(&response),
		}, nil
	}}
	return cloudwatchlogs.New(cloudwatchlogs.Options{
		BaseEndpoint: aws.String("https://cloudwatchlogs.test"),
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient:   client,
	})
}

type contractHTTPClient struct {
	do func(*http.Request) (*http.Response, error)
}

func (client contractHTTPClient) Do(request *http.Request) (*http.Response, error) {
	return client.do(request)
}

func assertExactKeys(t *testing.T, label string, values map[string]any, want []string) {
	t.Helper()
	got := make([]string, 0, len(values))
	for key := range values {
		got = append(got, key)
	}
	sort.Strings(got)
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s fields = %v, want exactly %v", label, got, want)
	}
}

func assertFieldsEqual(t *testing.T, label string, fixture, emitted map[string]any) {
	t.Helper()
	for key, want := range fixture {
		if got := emitted[key]; got != want {
			t.Errorf("%s.%s = %#v, want emitted value %#v", label, key, want, got)
		}
	}
}

func assertSecretFree(t *testing.T, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode fixture for secret-field check: %v", err)
	}
	normalized := strings.ToLower(string(data))
	for _, forbidden := range []string{"apikey", "credential", "password", "secret", "token"} {
		if strings.Contains(normalized, forbidden) {
			t.Errorf("fixture contains forbidden secret field fragment %q", forbidden)
		}
	}
}

type goldenRelationIdentity struct {
	relationType string
	groupKey     string
	memberKey    string
}

func assertGoldenRelationContract(t *testing.T, topology models.MetricGroupValue, want []goldenRelationIdentity) {
	t.Helper()
	canonical, ok := topology["Relations"].([]models.MetricGroupValue)
	if !ok {
		t.Fatalf("generated topology Relations = %#v, want []models.MetricGroupValue", topology["Relations"])
	}
	got := make([]goldenRelationIdentity, 0, len(canonical))
	for index, relation := range canonical {
		relationType, typeOK := relation["Type"].(string)
		groupKey, groupOK := relation["GroupKey"].(string)
		memberKey, memberOK := relation["MemberKey"].(string)
		if !typeOK || !groupOK || !memberOK {
			t.Fatalf("generated topology Relations[%d] identity = %#v, want string Type/GroupKey/MemberKey", index, relation)
		}
		got = append(got, goldenRelationIdentity{relationType: relationType, groupKey: groupKey, memberKey: memberKey})
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("generated topology relation identities = %#v, want exact ordered identities %#v", got, want)
	}

	facts, ok := topology["Facts"].(models.MetricGroupValue)
	if !ok {
		t.Fatalf("generated topology Facts = %#v, want models.MetricGroupValue", topology["Facts"])
	}
	compatibility, ok := facts["Relations"].([]models.MetricGroupValue)
	if !ok {
		t.Fatalf("generated topology Facts.Relations = %#v, want []models.MetricGroupValue", facts["Relations"])
	}
	if !reflect.DeepEqual(compatibility, canonical) {
		t.Fatalf("generated topology Facts.Relations = %#v, want exact canonical Relations mirror %#v", compatibility, canonical)
	}
}

func assertGoldenSemanticJSON(t *testing.T, path string, generatedValue interface{}) {
	t.Helper()
	generated, err := json.MarshalIndent(generatedValue, "", "  ")
	if err != nil {
		t.Fatalf("encode generated golden payload: %v", err)
	}
	generated = append(generated, '\n')
	assertOfflineGoldenPayload(t, generated)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("create golden fixture directory: %v", err)
		}
		if err := os.WriteFile(path, generated, 0644); err != nil {
			t.Fatalf("write golden fixture %q: %v", path, err)
		}
	}

	wantBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden fixture %q: %v", path, err)
	}
	var want interface{}
	if err := json.Unmarshal(wantBytes, &want); err != nil {
		t.Fatalf("decode golden fixture %q: %v", path, err)
	}
	var got interface{}
	if err := json.Unmarshal(generated, &got); err != nil {
		t.Fatalf("decode generated golden payload: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("generated payload does not match golden fixture %q\ngot:\n%s\nwant:\n%s", path, generated, wantBytes)
	}
}

func assertOfflineGoldenPayload(t *testing.T, payload []byte) {
	t.Helper()
	if err := validateOfflineGoldenPayload(payload); err != nil {
		t.Fatal(err)
	}
}

var (
	goldenAccountIDPattern         = regexp.MustCompile(`(^|[^0-9])[0-9]{12}([^0-9]|$)`)
	goldenAWSAccessKeyIDPattern    = regexp.MustCompile(`\b(AKIA|ASIA|AIDA|AROA|AIPA|ANPA|ANVA|ASCA)[A-Z0-9]{16}\b`)
	goldenSecretValuePattern       = regexp.MustCompile(`^[A-Za-z0-9/+=]{40}$`)
	goldenSyntheticARNPattern      = regexp.MustCompile(`^arn:aws(-[a-z0-9-]+)?:rds:[a-z0-9-]*:fixture:(db|cluster|global-cluster):[A-Za-z0-9._/-]+$`)
	goldenSyntheticHostnamePattern = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+example$`)
)

func validateOfflineGoldenPayload(payload []byte) error {
	var value interface{}
	if err := json.Unmarshal(payload, &value); err != nil {
		return fmt.Errorf("decode generated golden payload for offline validation: %w", err)
	}
	if goldenAccountIDPattern.Match(payload) {
		return fmt.Errorf("golden payload contains a 12-digit account identifier")
	}
	return validateOfflineGoldenValue(value, "$", "")
}

func validateOfflineGoldenValue(value interface{}, path, field string) error {
	switch typed := value.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if isGoldenCredentialField(key) {
				return fmt.Errorf("golden payload field %s.%s is credential-bearing", path, key)
			}
			if err := validateOfflineGoldenValue(typed[key], path+"."+key, key); err != nil {
				return err
			}
		}
	case []interface{}:
		for index, item := range typed {
			if err := validateOfflineGoldenValue(item, fmt.Sprintf("%s[%d]", path, index), field); err != nil {
				return err
			}
		}
	case string:
		if goldenAWSAccessKeyIDPattern.MatchString(typed) {
			return fmt.Errorf("golden payload field %s contains an AWS access key ID", path)
		}
		if goldenSecretValuePattern.MatchString(typed) || strings.Contains(typed, "-----BEGIN ") {
			return fmt.Errorf("golden payload field %s contains a credential-shaped value", path)
		}
		if isGoldenIPAddress(typed) {
			return fmt.Errorf("golden payload field %s contains an IP address", path)
		}
		if isGoldenHostField(field) {
			if err := validateSyntheticGoldenHost(typed); err != nil {
				return fmt.Errorf("golden payload field %s: %w", path, err)
			}
		}
		isARNValue := strings.HasPrefix(strings.ToLower(strings.TrimSpace(typed)), "arn:")
		if (isGoldenARNField(field) || isARNValue) && !goldenSyntheticARNPattern.MatchString(typed) {
			return fmt.Errorf("golden payload field %s contains non-synthetic ARN %q", path, typed)
		}
	}
	return nil
}

func normalizeGoldenFieldName(field string) string {
	field = strings.ToLower(field)
	field = strings.ReplaceAll(field, "_", "")
	return strings.ReplaceAll(field, "-", "")
}

func isGoldenCredentialField(field string) bool {
	field = normalizeGoldenFieldName(field)
	for _, marker := range []string{"apikey", "credential", "password", "secret", "token", "accesskey", "privatekey"} {
		if strings.Contains(field, marker) {
			return true
		}
	}
	return false
}

func isGoldenHostField(field string) bool {
	field = normalizeGoldenFieldName(field)
	return strings.HasSuffix(field, "host") || strings.HasSuffix(field, "hostname") ||
		strings.HasSuffix(field, "endpoint") || strings.HasSuffix(field, "address")
}

func isGoldenARNField(field string) bool {
	field = normalizeGoldenFieldName(field)
	return strings.HasSuffix(field, "arn") || strings.HasSuffix(field, "arns")
}

func isGoldenIPAddress(value string) bool {
	value = strings.TrimSpace(value)
	if net.ParseIP(strings.Trim(value, "[]")) != nil {
		return true
	}
	host, _, err := net.SplitHostPort(value)
	return err == nil && net.ParseIP(strings.Trim(host, "[]")) != nil
}

func validateSyntheticGoldenHost(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("synthetic host is empty")
	}
	host := value
	if strings.Contains(value, ":") {
		var port string
		var err error
		host, port, err = net.SplitHostPort(value)
		if err != nil {
			return fmt.Errorf("synthetic endpoint %q is malformed", value)
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return fmt.Errorf("synthetic endpoint %q has invalid port", value)
		}
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if net.ParseIP(host) != nil {
		return fmt.Errorf("synthetic host %q must not be an IP address", value)
	}
	if !goldenSyntheticHostnamePattern.MatchString(host) {
		return fmt.Errorf("host %q must use the reserved .example suffix", value)
	}
	return nil
}
