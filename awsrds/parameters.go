package awsrds

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

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

// ClusterParameterGroupLookup selects the group that may be described for
// live cluster membership. ClassificationOnly means Group must never be used
// as an AWS modification target.
type ClusterParameterGroupLookup struct {
	Group              string
	ClassificationOnly bool
}

// SelectClusterParameterGroupLookup uses the configured group whenever one is
// present. If configuration is empty, an attached Aurora cluster group may be
// read solely to distinguish cluster membership from complete absence.
func SelectClusterParameterGroupLookup(metadata Metadata, configuredGroup string) ClusterParameterGroupLookup {
	if configuredGroup != "" {
		return ClusterParameterGroupLookup{Group: configuredGroup}
	}
	if metadata.IsAurora() && metadata.DBClusterParameterGroup != "" {
		return ClusterParameterGroupLookup{
			Group:              metadata.DBClusterParameterGroup,
			ClassificationOnly: true,
		}
	}
	return ClusterParameterGroupLookup{}
}

// SkipReason is a stable, machine-readable explanation for a recommendation
// that was not included in an apply plan.
type SkipReason string

const (
	SkipAbsent                SkipReason = "absent"
	SkipUnmodifiable          SkipReason = "unmodifiable"
	SkipGroupNotConfigured    SkipReason = "group-not-configured"
	SkipDefaultGroup          SkipReason = "default-group"
	SkipGroupMismatch         SkipReason = "group-mismatch"
	SkipNotClusterWriter      SkipReason = "not-cluster-writer"
	SkipClusterNotSupported   SkipReason = "cluster-not-supported"
	SkipUnsupportedEngineMode SkipReason = "unsupported-engine-mode"
	SkipServerlessManaged     SkipReason = "serverless-managed"
	SkipUnsupportedApplyType  SkipReason = "unsupported-apply-type"
	SkipPendingRebootOnly     SkipReason = "pending-only"
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
	Audit    ApplyAudit  `json:"audit"`
}

// Sort normalizes empty slices and orders all result records for stable task
// output. The apply service calls this again after recording AWS outcomes.
func (result *ApplyResult) Sort() {
	if result == nil {
		return
	}
	sortScopeResult(&result.Instance)
	sortScopeResult(&result.Cluster)
	sortApplyAudit(&result.Audit)
}

// BuildApplyPlanInput contains only discovered/live state and current and
// recommended values, which keeps BuildApplyPlan pure and unit-testable.
type BuildApplyPlanInput struct {
	Metadata                  Metadata
	ConfiguredInstanceGroup   string
	ConfiguredClusterGroup    string
	InstanceMembershipUnknown bool
	InstanceParameters        map[string]ParameterInfo
	ClusterParameters         map[string]ParameterInfo
	Recommendations           map[string]interface{}
	CurrentValues             map[string]interface{}
	PendingRebootOnly         bool
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
func ListParameters(ctx context.Context, client ParameterReader, group string, scope Scope) (map[string]ParameterInfo, error) {
	if client == nil {
		return nil, fmt.Errorf("list %s parameters: RDS client is nil", scope)
	}

	if scope != ScopeInstance && scope != ScopeCluster {
		return nil, fmt.Errorf("list parameters: unsupported scope %q", scope)
	}

	parameters := make(map[string]ParameterInfo)
	seenMarkers := make(map[string]struct{})
	var marker *string
	for {
		var pageParameters []types.Parameter
		var pageMarker *string
		switch scope {
		case ScopeInstance:
			output, err := client.DescribeDBParameters(ctx, &rds.DescribeDBParametersInput{
				DBParameterGroupName: aws.String(group),
				Marker:               marker,
			})
			if err != nil {
				return nil, fmt.Errorf("list %s parameters for group %q: %w", scope, group, err)
			}
			if output == nil {
				return nil, fmt.Errorf("list %s parameters for group %q: DescribeDBParameters returned a nil output", scope, group)
			}
			pageParameters, pageMarker = output.Parameters, output.Marker
		case ScopeCluster:
			output, err := client.DescribeDBClusterParameters(ctx, &rds.DescribeDBClusterParametersInput{
				DBClusterParameterGroupName: aws.String(group),
				Marker:                      marker,
			})
			if err != nil {
				return nil, fmt.Errorf("list %s parameters for group %q: %w", scope, group, err)
			}
			if output == nil {
				return nil, fmt.Errorf("list %s parameters for group %q: DescribeDBClusterParameters returned a nil output", scope, group)
			}
			pageParameters, pageMarker = output.Parameters, output.Marker
		}

		for _, parameter := range pageParameters {
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

		nextMarker := aws.ToString(pageMarker)
		if nextMarker == "" {
			return parameters, nil
		}
		if _, exists := seenMarkers[nextMarker]; exists {
			return nil, fmt.Errorf("list %s parameters for group %q: repeated marker %q", scope, group, nextMarker)
		}
		seenMarkers[nextMarker] = struct{}{}
		marker = aws.String(nextMarker)
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
		Instance: emptyScopeResult(input.ConfiguredInstanceGroup),
		Cluster:  emptyScopeResult(input.ConfiguredClusterGroup),
		Audit:    NewApplyAudit(input.Metadata),
	}

	names := make([]string, 0, len(input.Recommendations))
	for name := range input.Recommendations {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		parameter, inInstance := input.InstanceParameters[name]
		if inInstance {
			record := newParameterAudit(input, name, parameter, ScopeInstance, plan.Instance.Group)
			buildScopeParameter(input, name, parameter, ScopeInstance, &plan.Instance, &result.Instance, &record)
			result.Audit.Parameters = append(result.Audit.Parameters, record)
			continue
		}
		if input.InstanceMembershipUnknown {
			skip, rejected := groupSkip(input, name, ScopeInstance)
			if !rejected {
				skip = SkippedVariable{Name: name, Reason: SkipAbsent}
			}
			result.Instance.Skipped = append(result.Instance.Skipped, skip)
			record := newParameterAudit(input, name, ParameterInfo{}, ScopeInstance, plan.Instance.Group)
			completeSkippedParameterAudit(&record, skip.Reason)
			result.Audit.Parameters = append(result.Audit.Parameters, record)
			continue
		}

		parameter, inCluster := input.ClusterParameters[name]
		if inCluster {
			record := newParameterAudit(input, name, parameter, ScopeCluster, plan.Cluster.Group)
			buildScopeParameter(input, name, parameter, ScopeCluster, &plan.Cluster, &result.Cluster, &record)
			result.Audit.Parameters = append(result.Audit.Parameters, record)
			continue
		}

		skip := SkippedVariable{
			Name:   name,
			Reason: SkipAbsent,
		}
		result.Instance.Skipped = append(result.Instance.Skipped, skip)
		record := newParameterAudit(input, name, ParameterInfo{}, ScopeInstance, plan.Instance.Group)
		completeSkippedParameterAudit(&record, skip.Reason)
		result.Audit.Parameters = append(result.Audit.Parameters, record)
	}

	result.Sort()
	return plan, result
}

// skipParameter records a rejected recommendation in both the plan's skip
// list and its audit trail, keeping the two in sync under one reason value.
func skipParameter(result *ScopeResult, record *ParameterAudit, name string, reason SkipReason) {
	result.Skipped = append(result.Skipped, SkippedVariable{Name: name, Reason: reason})
	completeSkippedParameterAudit(record, reason)
}

func buildScopeParameter(input BuildApplyPlanInput, name string, parameter ParameterInfo, scope Scope, plan *ScopePlan, result *ScopeResult, record *ParameterAudit) {
	if skip, rejected := groupSkip(input, name, scope); rejected {
		skipParameter(result, record, name, skip.Reason)
		return
	}
	if !parameter.IsModifiable {
		skipParameter(result, record, name, SkipUnmodifiable)
		return
	}
	if !supportsEngineMode(parameter.SupportedEngineModes, input.Metadata.EngineMode) {
		skipParameter(result, record, name, SkipUnsupportedEngineMode)
		return
	}
	if isServerlessManaged(input.Metadata, name) {
		skipParameter(result, record, name, SkipServerlessManaged)
		return
	}

	value, err := normalizeAWSRecommendationValue(input.Metadata, name, input.Recommendations[name])
	if err != nil {
		skipParameter(result, record, name, SkipInvalidValue)
		return
	}
	current, currentExists := input.CurrentValues[name]
	if parameter.HasParameterValue {
		if currentValue, currentErr := normalizeParameterValue(name, parameter.ParameterValue); currentErr == nil && currentValue == value {
			skipParameter(result, record, name, SkipUnchanged)
			return
		}
	} else if currentExists {
		currentValue, currentErr := normalizeParameterValue(name, current)
		if currentErr != nil {
			if usesPostgreSQLAWSNativeUnit(input.Metadata, name) {
				skipParameter(result, record, name, SkipInvalidValue)
				return
			}
		} else if currentValue == value {
			skipParameter(result, record, name, SkipUnchanged)
			return
		}
	}

	var applyMethod types.ApplyMethod
	switch strings.ToLower(parameter.ApplyType) {
	case "dynamic":
		if input.PendingRebootOnly {
			skipParameter(result, record, name, SkipPendingRebootOnly)
			return
		}
		applyMethod = types.ApplyMethodImmediate
	case "static":
		applyMethod = types.ApplyMethodPendingReboot
	default:
		skipParameter(result, record, name, SkipUnsupportedApplyType)
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
	record.SubmittedValue = aws.String(value)
	record.ApplyMethod = string(applyMethod)
	record.Outcome = OutcomeNotAttempted
	record.Reason = ReasonNotSubmitted
	record.VerificationStatus = VerificationNotApplicable
}

// IsDefaultParameterGroup reports whether group is an AWS-managed default
// parameter group, which can never be a mutation target.
func IsDefaultParameterGroup(group string) bool {
	return strings.HasPrefix(strings.ToLower(group), "default.")
}

func groupSkip(input BuildApplyPlanInput, name string, scope Scope) (SkippedVariable, bool) {
	configured := input.ConfiguredInstanceGroup
	attached := input.Metadata.DBParameterGroup
	if scope == ScopeCluster {
		configured = input.ConfiguredClusterGroup
		attached = input.Metadata.DBClusterParameterGroup
	}

	if configured == "" {
		return SkippedVariable{Name: name, Reason: SkipGroupNotConfigured}, true
	}
	if IsDefaultParameterGroup(configured) {
		return SkippedVariable{Name: name, Reason: SkipDefaultGroup}, true
	}
	if configured != attached {
		return SkippedVariable{
			Name:   name,
			Reason: SkipGroupMismatch,
		}, true
	}

	if scope == ScopeCluster {
		if !input.Metadata.IsAurora() {
			return SkippedVariable{Name: name, Reason: SkipClusterNotSupported}, true
		}
		if !input.Metadata.IsClusterWriter {
			return SkippedVariable{Name: name, Reason: SkipNotClusterWriter}, true
		}
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

func isServerlessManaged(metadata Metadata, name string) bool {
	if !metadata.IsServerlessV2 {
		return false
	}

	switch strings.ToLower(metadata.Engine) {
	case "aurora", "aurora-mysql":
		switch strings.ToLower(name) {
		case "innodb_buffer_pool_size", "innodb_purge_threads", "table_definition_cache", "table_open_cache":
			return true
		}
	case "aurora-postgresql":
		return strings.EqualFold(name, "shared_buffers")
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
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return "", fmt.Errorf("non-finite number")
		}
		normalized = strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		floating := float64(typed)
		if math.IsNaN(floating) || math.IsInf(floating, 0) {
			return "", fmt.Errorf("non-finite number")
		}
		normalized = strconv.FormatFloat(floating, 'f', -1, 32)
	case int:
		normalized = strconv.FormatInt(int64(typed), 10)
	case int8:
		normalized = strconv.FormatInt(int64(typed), 10)
	case int16:
		normalized = strconv.FormatInt(int64(typed), 10)
	case int32:
		normalized = strconv.FormatInt(int64(typed), 10)
	case int64:
		normalized = strconv.FormatInt(typed, 10)
	case uint:
		normalized = strconv.FormatUint(uint64(typed), 10)
	case uint8:
		normalized = strconv.FormatUint(uint64(typed), 10)
	case uint16:
		normalized = strconv.FormatUint(uint64(typed), 10)
	case uint32:
		normalized = strconv.FormatUint(uint64(typed), 10)
	case uint64:
		normalized = strconv.FormatUint(typed, 10)
	default:
		return "", fmt.Errorf("unsupported parameter value type %T", value)
	}

	if name != "innodb_max_dirty_pages_pct" {
		return normalized, nil
	}

	percentage, err := strconv.ParseFloat(normalized, 64)
	if err != nil || math.IsNaN(percentage) || math.IsInf(percentage, 0) {
		return "", fmt.Errorf("invalid innodb_max_dirty_pages_pct value")
	}
	return strconv.FormatFloat(math.Trunc(percentage), 'f', 0, 64), nil
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

func emptyScopeResult(group string) ScopeResult {
	return ScopeResult{
		Group:   group,
		Applied: []string{},
		Skipped: []SkippedVariable{},
		Failed:  []FailedBatch{},
	}
}

func sortScopeResult(result *ScopeResult) {
	if result.Applied == nil {
		result.Applied = []string{}
	}
	if result.Skipped == nil {
		result.Skipped = []SkippedVariable{}
	}
	if result.Failed == nil {
		result.Failed = []FailedBatch{}
	}

	sort.Strings(result.Applied)
	sort.Slice(result.Skipped, func(i, j int) bool {
		left, right := result.Skipped[i], result.Skipped[j]
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		return left.Reason < right.Reason
	})
	for index := range result.Failed {
		if result.Failed[index].Parameters == nil {
			result.Failed[index].Parameters = []string{}
		}
		sort.Strings(result.Failed[index].Parameters)
	}
	sort.Slice(result.Failed, func(i, j int) bool {
		left := strings.Join(result.Failed[i].Parameters, "\x00")
		right := strings.Join(result.Failed[j].Parameters, "\x00")
		if left != right {
			return left < right
		}
		return result.Failed[i].Error < result.Failed[j].Error
	})
	sort.Slice(result.Diagnostics, func(i, j int) bool {
		left, right := result.Diagnostics[i], result.Diagnostics[j]
		if left.Reason != right.Reason {
			return left.Reason < right.Reason
		}
		if left.ExpectedGroup != right.ExpectedGroup {
			return left.ExpectedGroup < right.ExpectedGroup
		}
		return left.ActualGroup < right.ActualGroup
	})
}
