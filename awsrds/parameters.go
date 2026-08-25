package awsrds

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/Releem/mysqlconfigurer/utils"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
)

// Scope identifies the RDS parameter-group API that owns a live parameter.
type Scope string

const (
	ScopeInstance Scope = "instance"
	ScopeCluster  Scope = "cluster"
)

type ApplyOutcome string

const (
	OutcomeApplied ApplyOutcome = "applied"
	OutcomeFailed  ApplyOutcome = "failed"
)

// ParameterInfo contains the live AWS fields used to decide whether and how a
// recommendation can be applied.
type ParameterInfo struct {
	Name                 string
	ApplyType            string
	IsModifiable         bool
	SupportedEngineModes []string
	ParameterValue       string
	HasParameterValue    bool
}

// SkipReason is a stable, machine-readable explanation for a recommendation
// that was not included in an apply plan.
type SkipReason string

const (
	SkipAbsent                SkipReason = "absent"
	SkipUnmodifiable          SkipReason = "unmodifiable"
	SkipGroupMismatch         SkipReason = "group-mismatch"
	SkipNotClusterWriter      SkipReason = "not-cluster-writer"
	SkipClusterNotSupported   SkipReason = "cluster-not-supported"
	SkipUnsupportedEngineMode SkipReason = "unsupported-engine-mode"
	SkipUnsupportedApplyType  SkipReason = "unsupported-apply-type"
	SkipUnchanged             SkipReason = "unchanged"
	SkipInvalidValue          SkipReason = "invalid-value"
)

// SkippedVariable records one recommendation rejected by live Agent checks.
type SkippedVariable struct {
	Name   string     `json:"name"`
	Reason SkipReason `json:"reason"`
}

// FailedBatch is populated by the apply service when one AWS modification
// batch fails. Scope and group are represented by the containing ScopeResult.
type FailedBatch struct {
	Parameters           []string `json:"parameters"`
	Error                string   `json:"error"`
	DBInstanceIdentifier *string  `json:"db_instance_identifier,omitempty"`
	DBClusterIdentifier  *string  `json:"db_cluster_identifier,omitempty"`
	ParameterGroup       *string  `json:"parameter_group,omitempty"`
	ParameterGroupStatus *string  `json:"parameter_group_status,omitempty"`
}

// ScopeDiagnostic records one scope-wide safety condition without repeating
// it for every recommendation rejected within that scope.
type ScopeDiagnostic struct {
	Reason        SkipReason `json:"reason"`
	ExpectedGroup string     `json:"expected_group,omitempty"`
	ActualGroup   string     `json:"actual_group,omitempty"`
}

// ScopeResult is the deterministic task-output state for one parameter scope.
type ScopeResult struct {
	Group       string            `json:"group,omitempty"`
	Applied     []string          `json:"applied"`
	Skipped     []SkippedVariable `json:"skipped"`
	Failed      []FailedBatch     `json:"failed"`
	Diagnostics []ScopeDiagnostic `json:"diagnostics,omitempty"`
}

// ApplyResult is the JSON-serializable result shared with the apply service.
type ApplyResult struct {
	Instance ScopeResult `json:"instance"`
	Cluster  ScopeResult `json:"cluster"`
}

// BuildApplyPlanInput contains only discovered/live state and current and
// recommended values, which keeps BuildApplyPlan pure and unit-testable.
type BuildApplyPlanInput struct {
	Metadata                Metadata
	ConfiguredInstanceGroup string
	ConfiguredClusterGroup  string
	InstanceParameters      map[string]ParameterInfo
	ClusterParameters       map[string]ParameterInfo
	Recommendations         map[string]interface{}
	CurrentValues           map[string]interface{}
	PendingRebootOnly       bool
}

// ScopePlan contains the exact group and AWS parameters for one modify API.
type ScopePlan struct {
	Group      string
	Parameters []types.Parameter
}

// ApplyPlan separates instance and cluster calls while ApplyResult separately
// carries operator-visible outcomes.
type ApplyPlan struct {
	Instance ScopePlan
	Cluster  ScopePlan
}

// ListParameters reads every page in one parameter group and indexes the live
// AWS parameter metadata by name.
func ListParameters(ctx context.Context, client Client, group string, scope Scope) (map[string]ParameterInfo, error) {
	if client == nil {
		return nil, fmt.Errorf("list %s parameters: RDS client is nil", scope)
	}

	if scope != ScopeInstance && scope != ScopeCluster {
		return nil, fmt.Errorf("list parameters: unsupported scope %q", scope)
	}

	parameters := make(map[string]ParameterInfo)
	// The SDK paginator advances and terminates on the marker, but by default
	// it would page forever if AWS ever echoed one back. Its StopOnDuplicateToken
	// option truncates silently instead, which would hand BuildApplyPlan an
	// incomplete group and misroute the missing names as absent, so a repeated
	// marker is rejected loudly here.
	seenMarkers := make(map[string]struct{})
	recordPage := func(pageParameters []types.Parameter, pageMarker *string) error {
		indexParameters(parameters, pageParameters)

		marker := aws.ToString(pageMarker)
		if marker == "" {
			return nil
		}
		if _, exists := seenMarkers[marker]; exists {
			return fmt.Errorf("list %s parameters for group %q: repeated marker %q", scope, group, marker)
		}
		seenMarkers[marker] = struct{}{}
		return nil
	}

	switch scope {
	case ScopeInstance:
		paginator := rds.NewDescribeDBParametersPaginator(client, &rds.DescribeDBParametersInput{
			DBParameterGroupName: aws.String(group),
		})
		for paginator.HasMorePages() {
			output, err := paginator.NextPage(ctx)
			if err != nil {
				return nil, fmt.Errorf("list %s parameters for group %q: %w", scope, group, err)
			}
			if err := recordPage(output.Parameters, output.Marker); err != nil {
				return nil, err
			}
		}
	case ScopeCluster:
		paginator := rds.NewDescribeDBClusterParametersPaginator(client, &rds.DescribeDBClusterParametersInput{
			DBClusterParameterGroupName: aws.String(group),
		})
		for paginator.HasMorePages() {
			output, err := paginator.NextPage(ctx)
			if err != nil {
				return nil, fmt.Errorf("list %s parameters for group %q: %w", scope, group, err)
			}
			if err := recordPage(output.Parameters, output.Marker); err != nil {
				return nil, err
			}
		}
	}

	return parameters, nil
}

// indexParameters records one page of live AWS parameter metadata by name.
func indexParameters(parameters map[string]ParameterInfo, page []types.Parameter) {
	for _, parameter := range page {
		name := aws.ToString(parameter.ParameterName)
		if name == "" {
			continue
		}
		info := ParameterInfo{
			Name:                 name,
			ApplyType:            aws.ToString(parameter.ApplyType),
			IsModifiable:         aws.ToBool(parameter.IsModifiable),
			SupportedEngineModes: append([]string(nil), parameter.SupportedEngineModes...),
		}
		if parameter.ParameterValue != nil {
			info.ParameterValue = aws.ToString(parameter.ParameterValue)
			info.HasParameterValue = true
		}
		parameters[name] = info
	}
}

// BuildApplyPlan routes recommendations using live parameter-group membership.
// Instance membership always has priority when a name exists in both indexes.
func BuildApplyPlan(input BuildApplyPlanInput) (ApplyPlan, ApplyResult) {
	plan := ApplyPlan{
		Instance: ScopePlan{Group: input.ConfiguredInstanceGroup, Parameters: []types.Parameter{}},
		Cluster:  ScopePlan{Group: input.ConfiguredClusterGroup, Parameters: []types.Parameter{}},
	}
	result := ApplyResult{
		Instance: NewScopeResult(input.ConfiguredInstanceGroup),
		Cluster:  NewScopeResult(input.ConfiguredClusterGroup),
	}

	names := make([]string, 0, len(input.Recommendations))
	for name := range input.Recommendations {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		parameter, inInstance := input.InstanceParameters[name]
		if inInstance {
			buildScopeParameter(input, name, parameter, ScopeInstance, &plan.Instance, &result.Instance)
			continue
		}

		parameter, inCluster := input.ClusterParameters[name]
		if inCluster {
			buildScopeParameter(input, name, parameter, ScopeCluster, &plan.Cluster, &result.Cluster)
			continue
		}

		skip := SkippedVariable{
			Name:   name,
			Reason: SkipAbsent,
		}
		result.Instance.Skipped = append(result.Instance.Skipped, skip)
	}

	return plan, result
}

// skipParameter records a rejected recommendation in both the plan's skip
// list and its audit trail, keeping the two in sync under one reason value.
func skipParameter(result *ScopeResult, name string, reason SkipReason) {
	result.Skipped = append(result.Skipped, SkippedVariable{Name: name, Reason: reason})
}

func buildScopeParameter(input BuildApplyPlanInput, name string, parameter ParameterInfo, scope Scope, plan *ScopePlan, result *ScopeResult) {
	if scope == ScopeCluster {
		if skip, rejected := clusterTopologySkip(input, name); rejected {
			skipParameter(result, name, skip.Reason)
			return
		}
	}
	if !parameter.IsModifiable {
		skipParameter(result, name, SkipUnmodifiable)
		return
	}
	if !supportsEngineMode(parameter.SupportedEngineModes, input.Metadata.EngineMode) {
		skipParameter(result, name, SkipUnsupportedEngineMode)
		return
	}

	value, err := normalizeAWSRecommendationValue(input.Metadata, name, input.Recommendations[name])
	if err != nil {
		skipParameter(result, name, SkipInvalidValue)
		return
	}
	current, currentExists := input.CurrentValues[name]
	if parameter.HasParameterValue {
		if currentValue, currentErr := normalizeParameterValue(name, parameter.ParameterValue); currentErr == nil && currentValue == value {
			skipParameter(result, name, SkipUnchanged)
			return
		}
	} else if currentExists {
		if currentValue, currentErr := normalizeParameterValue(name, current); currentErr == nil && currentValue == value {
			skipParameter(result, name, SkipUnchanged)
			return
		}
	}

	var applyMethod types.ApplyMethod
	switch strings.ToLower(parameter.ApplyType) {
	case "dynamic":
		applyMethod = types.ApplyMethodImmediate
		if input.PendingRebootOnly {
			applyMethod = types.ApplyMethodPendingReboot
		}
	case "static":
		applyMethod = types.ApplyMethodPendingReboot
	default:
		skipParameter(result, name, SkipUnsupportedApplyType)
		return
	}

	parameterName := parameter.Name
	if parameterName == "" {
		parameterName = name
	}
	plan.Parameters = append(plan.Parameters, types.Parameter{
		ParameterName:  aws.String(parameterName),
		ParameterValue: aws.String(value),
		ApplyMethod:    applyMethod,
	})
}

// IsDefaultParameterGroup reports whether group is an AWS-managed default
// parameter group, which can never be a mutation target.
func IsDefaultParameterGroup(group string) bool {
	return strings.HasPrefix(strings.ToLower(group), "default.")
}

func clusterTopologySkip(input BuildApplyPlanInput, name string) (SkippedVariable, bool) {
	if !input.Metadata.IsAurora() {
		return SkippedVariable{Name: name, Reason: SkipClusterNotSupported}, true
	}
	if !input.Metadata.IsClusterWriter {
		return SkippedVariable{Name: name, Reason: SkipNotClusterWriter}, true
	}
	return SkippedVariable{}, false
}

func supportsEngineMode(supported []string, current string) bool {
	if len(supported) == 0 {
		return true
	}
	for _, mode := range supported {
		if strings.EqualFold(mode, current) {
			return true
		}
	}
	return false
}

func normalizeParameterValue(name string, value interface{}) (string, error) {
	var normalized string
	switch typed := value.(type) {
	case string:
		normalized = typed
	case json.Number:
		normalized = typed.String()
		if normalized == "" || strings.TrimSpace(normalized) != normalized || !json.Valid([]byte(normalized)) || (normalized[0] != '-' && (normalized[0] < '0' || normalized[0] > '9')) {
			return "", fmt.Errorf("invalid JSON number")
		}
	default:
		number := utils.AsNumber(value)
		switch number.Kind {
		case utils.SignedNumber:
			normalized = strconv.FormatInt(number.Int, 10)
		case utils.UnsignedNumber:
			normalized = strconv.FormatUint(number.Uint, 10)
		case utils.FloatNumber:
			if math.IsNaN(number.Float) || math.IsInf(number.Float, 0) {
				return "", fmt.Errorf("non-finite number")
			}
			normalized = strconv.FormatFloat(number.Float, 'f', -1, number.FloatBits)
		default:
			return "", fmt.Errorf("unsupported parameter value type %T", value)
		}
	}

	if name == "innodb_max_dirty_pages_pct" {
		percentage, err := strconv.ParseFloat(normalized, 64)
		if err != nil || math.IsNaN(percentage) || math.IsInf(percentage, 0) {
			return "", fmt.Errorf("invalid innodb_max_dirty_pages_pct value")
		}
		return strconv.FormatFloat(math.Trunc(percentage), 'f', 0, 64), nil
	}
	return normalized, nil

}

func normalizeAWSRecommendationValue(metadata Metadata, name string, value interface{}) (string, error) {
	normalized, err := normalizeParameterValue(name, value)
	if err != nil || !usesPostgreSQLAWSNativeUnit(metadata, name) {
		return normalized, err
	}

	divisor, _ := postgreSQLAWSNativeUnitDivisor(name)
	bytes, err := strconv.ParseUint(normalized, 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid PostgreSQL byte value for %s: %w", name, err)
	}
	if bytes%divisor != 0 {
		return "", fmt.Errorf("PostgreSQL byte value for %s is not divisible by %d", name, divisor)
	}
	return strconv.FormatUint(bytes/divisor, 10), nil
}

func usesPostgreSQLAWSNativeUnit(metadata Metadata, name string) bool {
	if metadata.DatabaseType() != "postgresql" {
		return false
	}
	_, exists := postgreSQLAWSNativeUnitDivisor(name)
	return exists
}

func postgreSQLAWSNativeUnitDivisor(name string) (uint64, bool) {
	switch name {
	case "work_mem", "maintenance_work_mem":
		return 1024, true
	case "shared_buffers", "effective_cache_size", "wal_buffers":
		return 8192, true
	default:
		return 0, false
	}
}

// NewScopeResult builds the empty, JSON-stable state for one parameter scope.
// The slices are non-nil so task output always carries explicit empty arrays.
func NewScopeResult(group string) ScopeResult {
	return ScopeResult{
		Group:   group,
		Applied: []string{},
		Skipped: []SkippedVariable{},
		Failed:  []FailedBatch{},
	}
}
