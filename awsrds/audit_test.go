package awsrds

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
)

// This test fails if the planner records normalized values in the audit rather
// than preserving the original recommendation and its selected value source.
func TestBuildApplyPlanBuildsVersionedParameterAudit(t *testing.T) {
	input := BuildApplyPlanInput{
		Metadata: Metadata{
			DBInstanceIdentifier: "orders-1", DBInstanceClass: "db.r7g.large",
			Engine: "aurora-postgresql", EngineMode: "provisioned",
			DBParameterGroup: "instance-pg", DBClusterIdentifier: "orders",
			DBClusterParameterGroup: "cluster-pg", IsClusterWriter: true,
			Endpoint: "must-not-appear.example", InstanceStatus: "available",
			DBParameterGroupStatus: "in-sync",
		},
		ConfiguredInstanceGroup: "instance-pg",
		ConfiguredClusterGroup:  "cluster-pg",
		InstanceParameters: map[string]ParameterInfo{
			"work_mem": {Name: "work_mem", ApplyType: "dynamic", IsModifiable: true},
		},
		ClusterParameters: map[string]ParameterInfo{
			"max_connections": {Name: "max_connections", ApplyType: "static", IsModifiable: true, ParameterValue: "100", HasParameterValue: true},
		},
		Recommendations: map[string]interface{}{
			"work_mem":          json.Number("8388608"),
			"max_connections":   json.Number("200"),
			"unknown_parameter": "1",
		},
		CurrentValues: map[string]interface{}{"work_mem": "4096"},
	}

	plan, result := BuildApplyPlan(input)

	if result.Audit.SchemaVersion != 1 {
		t.Fatalf("schema = %d, want 1", result.Audit.SchemaVersion)
	}
	if got := aws.ToString(plan.Instance.Parameters[0].ParameterValue); got != "8192" {
		t.Fatalf("submitted = %q, want 8192", got)
	}
	assertParameterAudit(t, result.Audit.Parameters, ParameterAudit{
		Scope: ScopeInstance, Name: "work_mem", Group: "instance-pg",
		CurrentValue: aws.String("4096"), CurrentSource: CurrentSourceDBMetrics,
		RecommendedValue: "8388608", SubmittedValue: aws.String("8192"),
		ApplyMethod: string(types.ApplyMethodImmediate), Outcome: OutcomeNotAttempted,
		Reason: ReasonNotSubmitted, VerificationStatus: VerificationNotApplicable,
	})
	assertParameterAudit(t, result.Audit.Parameters, ParameterAudit{
		Scope: ScopeCluster, Name: "max_connections", Group: "cluster-pg",
		CurrentValue: aws.String("100"), CurrentSource: CurrentSourceAWSParameterGroup,
		RecommendedValue: "200", SubmittedValue: aws.String("200"),
		ApplyMethod: string(types.ApplyMethodPendingReboot), Outcome: OutcomeNotAttempted,
		Reason: ReasonNotSubmitted, VerificationStatus: VerificationNotApplicable,
	})
	assertParameterAudit(t, result.Audit.Parameters, ParameterAudit{
		Scope: ScopeInstance, Name: "unknown_parameter", Group: "instance-pg",
		CurrentSource: CurrentSourceMissing, RecommendedValue: "1",
		Outcome: OutcomeSkipped, Reason: string(SkipAbsent),
		VerificationStatus: VerificationNotApplicable,
	})
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal(result) error = %v", err)
	}
	if bytes.Contains(encoded, []byte("must-not-appear.example")) {
		t.Fatalf("endpoint leaked: %s", encoded)
	}
}

// This test fails if a live AWS parameter value loses precedence over the
// DB-metrics fallback, or if a missing value is represented as an empty string.
func TestParameterAuditCurrentSource(t *testing.T) {
	tests := []struct {
		name       string
		parameter  ParameterInfo
		current    map[string]interface{}
		wantValue  *string
		wantSource CurrentValueSource
	}{
		{"AWS group wins", ParameterInfo{ParameterValue: "100", HasParameterValue: true}, map[string]interface{}{"p": "90"}, aws.String("100"), CurrentSourceAWSParameterGroup},
		{"DB metrics fallback", ParameterInfo{}, map[string]interface{}{"p": "90"}, aws.String("90"), CurrentSourceDBMetrics},
		{"missing", ParameterInfo{}, map[string]interface{}{}, nil, CurrentSourceMissing},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			input := BuildApplyPlanInput{
				Metadata:                Metadata{DBParameterGroup: "instance-pg"},
				ConfiguredInstanceGroup: "instance-pg",
				InstanceParameters:      map[string]ParameterInfo{"p": tt.parameter},
				Recommendations:         map[string]interface{}{"p": "1"},
				CurrentValues:           tt.current,
			}
			_, result := BuildApplyPlan(input)
			if len(result.Audit.Parameters) != 1 {
				t.Fatalf("audit records = %#v, want one", result.Audit.Parameters)
			}
			record := result.Audit.Parameters[0]
			if !reflect.DeepEqual(record.CurrentValue, tt.wantValue) || record.CurrentSource != tt.wantSource {
				t.Fatalf("current = (%#v, %q), want (%#v, %q)", record.CurrentValue, record.CurrentSource, tt.wantValue, tt.wantSource)
			}
		})
	}
}

// This test fails if public field names change, nil audit slices serialize as
// null, or topology begins carrying endpoint data.
func TestApplyAuditJSONContract(t *testing.T) {
	result := ApplyResult{
		Audit: NewApplyAudit(Metadata{
			DBInstanceIdentifier: "orders-1", DBInstanceClass: "db.r7g.large",
			Engine: "aurora-postgresql", Endpoint: "must-not-appear.example",
		}),
	}
	result.Audit.Parameters = []ParameterAudit{
		{Name: "z", Scope: ScopeInstance, RecommendedValue: "2", CurrentSource: CurrentSourceMissing, Outcome: OutcomeSkipped, VerificationStatus: VerificationNotApplicable},
		{Name: "a", Scope: ScopeCluster, RecommendedValue: "1", CurrentSource: CurrentSourceMissing, Outcome: OutcomeSkipped, VerificationStatus: VerificationNotApplicable},
	}
	result.Sort()

	encoded, err := json.Marshal(result.Audit)
	if err != nil {
		t.Fatalf("json.Marshal(audit) error = %v", err)
	}
	const want = `{"schema_version":1,"topology":{"db_instance_identifier":"orders-1","db_instance_class":"db.r7g.large","engine":"aurora-postgresql","is_cluster_writer":false,"is_serverless_v2":false},"parameters":[{"scope":"cluster","name":"a","current_source":"missing","recommended_value":"1","outcome":"skipped","verification_status":"not-applicable"},{"scope":"instance","name":"z","current_source":"missing","recommended_value":"2","outcome":"skipped","verification_status":"not-applicable"}]}`
	if string(encoded) != want {
		t.Fatalf("audit JSON = %s, want %s", encoded, want)
	}
	if bytes.Contains(encoded, []byte("must-not-appear.example")) {
		t.Fatalf("endpoint leaked: %s", encoded)
	}

	for value, want := range map[interface{}]string{
		"raw":                "raw",
		json.Number("12.30"): "12.30",
		true:                 "true",
		nil:                  "null",
	} {
		if got := AuditValueString(value); got != want {
			t.Errorf("AuditValueString(%#v) = %q, want %q", value, got, want)
		}
	}
}

// This test fails if a Serverless-managed recommendation is recorded as
// submitted or loses the machine-readable skip reason.
func TestBuildApplyPlanAuditsServerlessManagedExclusion(t *testing.T) {
	input := BuildApplyPlanInput{
		Metadata: Metadata{
			Engine: "aurora-postgresql", IsServerlessV2: true,
			DBParameterGroup: "instance-pg",
		},
		ConfiguredInstanceGroup: "instance-pg",
		InstanceParameters: map[string]ParameterInfo{
			"shared_buffers": {Name: "shared_buffers", ApplyType: "static", IsModifiable: true},
		},
		Recommendations: map[string]interface{}{"shared_buffers": json.Number("134217728")},
	}

	plan, result := BuildApplyPlan(input)
	if len(plan.Instance.Parameters) != 0 {
		t.Fatalf("submitted parameters = %#v, want none", plan.Instance.Parameters)
	}
	assertParameterAudit(t, result.Audit.Parameters, ParameterAudit{
		Scope: ScopeInstance, Name: "shared_buffers", Group: "instance-pg",
		CurrentSource: CurrentSourceMissing, RecommendedValue: "134217728",
		Outcome: OutcomeSkipped, Reason: string(SkipServerlessManaged),
		VerificationStatus: VerificationNotApplicable,
	})
}

func assertParameterAudit(t *testing.T, records []ParameterAudit, want ParameterAudit) {
	t.Helper()
	for _, record := range records {
		if record.Scope == want.Scope && record.Name == want.Name {
			if !reflect.DeepEqual(record, want) {
				t.Fatalf("audit = %#v, want %#v", record, want)
			}
			return
		}
	}
	t.Fatalf("audit record %s/%s not found", want.Scope, want.Name)
}
