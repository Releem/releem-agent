package awsrds

import (
	"encoding/json"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
)

const ApplyAuditSchemaVersion = 1

type CurrentValueSource string

const (
	CurrentSourceAWSParameterGroup CurrentValueSource = "aws-parameter-group"
	CurrentSourceDBMetrics         CurrentValueSource = "db-metrics"
	CurrentSourceMissing           CurrentValueSource = "missing"
)

type ApplyOutcome string

const (
	OutcomeApplied      ApplyOutcome = "applied"
	OutcomeSkipped      ApplyOutcome = "skipped"
	OutcomeFailed       ApplyOutcome = "failed"
	OutcomeNotAttempted ApplyOutcome = "not-attempted"
)

type VerificationStatus string

const (
	VerificationMatched       VerificationStatus = "matched"
	VerificationMismatched    VerificationStatus = "mismatched"
	VerificationPendingReboot VerificationStatus = "pending-reboot"
	VerificationUnavailable   VerificationStatus = "unavailable"
	VerificationNotApplicable VerificationStatus = "not-applicable"
)

const (
	ReasonNotSubmitted             = "not-submitted"
	ReasonPriorBatchFailure        = "prior-batch-failure"
	ReasonPriorScopeFailure        = "prior-scope-failure"
	ReasonReadbackFailed           = "readback-failed"
	ReasonReadbackParameterMissing = "readback-parameter-missing"
)

type AuditTopology struct {
	DBInstanceIdentifier    string `json:"db_instance_identifier,omitempty"`
	DBInstanceClass         string `json:"db_instance_class,omitempty"`
	Engine                  string `json:"engine,omitempty"`
	EngineMode              string `json:"engine_mode,omitempty"`
	DBClusterIdentifier     string `json:"db_cluster_identifier,omitempty"`
	IsClusterWriter         bool   `json:"is_cluster_writer"`
	IsServerlessV2          bool   `json:"is_serverless_v2"`
	DBParameterGroup        string `json:"db_parameter_group,omitempty"`
	DBClusterParameterGroup string `json:"db_cluster_parameter_group,omitempty"`
	InstanceStatus          string `json:"instance_status,omitempty"`
	DBParameterGroupStatus  string `json:"db_parameter_group_status,omitempty"`
}

type ParameterAudit struct {
	Scope              Scope              `json:"scope"`
	Name               string             `json:"name"`
	Group              string             `json:"group,omitempty"`
	CurrentValue       *string            `json:"current_value,omitempty"`
	CurrentSource      CurrentValueSource `json:"current_source"`
	RecommendedValue   string             `json:"recommended_value"`
	SubmittedValue     *string            `json:"submitted_value,omitempty"`
	ApplyMethod        string             `json:"apply_method,omitempty"`
	Batch              *int               `json:"batch,omitempty"`
	Outcome            ApplyOutcome       `json:"outcome"`
	Reason             string             `json:"reason,omitempty"`
	Error              string             `json:"error,omitempty"`
	ObservedAfter      *string            `json:"observed_after,omitempty"`
	VerificationStatus VerificationStatus `json:"verification_status"`
}

type ApplyAudit struct {
	SchemaVersion int              `json:"schema_version"`
	Topology      AuditTopology    `json:"topology"`
	Parameters    []ParameterAudit `json:"parameters"`
}

// NewApplyAudit creates the versioned, secret-free audit envelope.
func NewApplyAudit(metadata Metadata) ApplyAudit {
	audit := ApplyAudit{
		SchemaVersion: ApplyAuditSchemaVersion,
		Parameters:    []ParameterAudit{},
	}
	PopulateAuditTopology(&audit, metadata)
	return audit
}

// PopulateAuditTopology copies only topology fields which are safe to emit in
// task output. In particular, endpoint and resource identifiers remain local.
func PopulateAuditTopology(audit *ApplyAudit, metadata Metadata) {
	if audit == nil {
		return
	}
	audit.Topology = AuditTopology{
		DBInstanceIdentifier:    metadata.DBInstanceIdentifier,
		DBInstanceClass:         metadata.DBInstanceClass,
		Engine:                  metadata.Engine,
		EngineMode:              metadata.EngineMode,
		DBClusterIdentifier:     metadata.DBClusterIdentifier,
		IsClusterWriter:         metadata.IsClusterWriter,
		IsServerlessV2:          metadata.IsServerlessV2,
		DBParameterGroup:        metadata.DBParameterGroup,
		DBClusterParameterGroup: metadata.DBClusterParameterGroup,
		InstanceStatus:          metadata.InstanceStatus,
		DBParameterGroupStatus:  metadata.DBParameterGroupStatus,
	}
}

// AuditValueString preserves JSON-decoder representations without applying
// planner normalization. This records what the Agent received, not what AWS
// ultimately accepts.
func AuditValueString(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}

func newParameterAudit(input BuildApplyPlanInput, name string, parameter ParameterInfo, scope Scope, group string) ParameterAudit {
	record := ParameterAudit{
		Scope:            scope,
		Name:             name,
		Group:            group,
		CurrentSource:    CurrentSourceMissing,
		RecommendedValue: AuditValueString(input.Recommendations[name]),
	}
	if parameter.HasParameterValue {
		record.CurrentValue = aws.String(parameter.ParameterValue)
		record.CurrentSource = CurrentSourceAWSParameterGroup
		return record
	}
	if current, exists := input.CurrentValues[name]; exists {
		record.CurrentValue = aws.String(AuditValueString(current))
		record.CurrentSource = CurrentSourceDBMetrics
	}
	return record
}

func completeSkippedParameterAudit(record *ParameterAudit, reason SkipReason) {
	record.Outcome = OutcomeSkipped
	record.Reason = string(reason)
	record.VerificationStatus = VerificationNotApplicable
}

func sortApplyAudit(audit *ApplyAudit) {
	if audit.Parameters == nil {
		audit.Parameters = []ParameterAudit{}
	}
	sort.Slice(audit.Parameters, func(i, j int) bool {
		left, right := audit.Parameters[i], audit.Parameters[j]
		if left.Scope != right.Scope {
			return left.Scope < right.Scope
		}
		return left.Name < right.Name
	})
}
