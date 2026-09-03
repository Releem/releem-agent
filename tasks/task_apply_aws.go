package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Releem/mysqlconfigurer/awsrds"
	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/Releem/mysqlconfigurer/utils"
	"github.com/aws/aws-sdk-go-v2/aws"
	configaws "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	logging "github.com/google/logger"
)

type AWSApplyMode int

const (
	AWSApplyAll AWSApplyMode = iota
	AWSApplyPendingRebootOnly
)

// These values preserve the public RDS task meanings used before routed
// parameter application: 3 identifies an attached parameter-group mismatch,
// 6 is a bounded-wait timeout, 8 is a general AWS apply failure, 9 identifies
// AccessDenied, and 10 reports that an applied change requires a reboot.
const (
	awsApplyExitSuccess                        = 0
	awsApplyExitInstanceUnavailable            = 1
	awsApplyExitParameterGroupNotInSync        = 2
	awsApplyExitParameterGroupMismatch         = 3
	awsApplyExitClusterParameterGroupNotInSync = 4
	awsApplyExitClusterParameterGroupMismatch  = 5
	awsApplyExitTimeout                        = 6
	awsApplyExitFailure                        = 8
	awsApplyExitAccessDenied                   = 9
	awsApplyExitPendingReboot                  = 10

	awsApplyTaskStatusSuccess = 1
	awsApplyTaskStatusFailure = 4
	awsApplyBatchSize         = 20
)

const (
	awsApplyErrorTimeout                 = "timeout"
	awsApplyErrorAccessDenied            = "access-denied"
	awsApplyErrorAWSAPI                  = "aws-api-error"
	awsApplyErrorParameterGroupNotInSync = "parameter-group-not-in-sync"
	awsApplyErrorParameterGroupMismatch  = "parameter-group-mismatch"
	awsApplySerializationFailureOutput   = `{"instance":{"applied":[],"skipped":[],"failed":[{"parameters":[],"error":"serialize AWS apply result"}]},"cluster":{"applied":[],"skipped":[],"failed":[]}}`
)

var errAWSApplyWaitTimeout = errors.New("timed out waiting for AWS parameter apply")

type awsAPIError interface {
	error
	ErrorCode() string
}

const (
	// awsApplyWaitTimeout preserves the effective wall-clock budget of the
	// pre-routing waiter, which polled every 3s for up to 400 iterations
	// (~1200s) rather than 400s.
	awsApplyWaitTimeout  = 1200 * time.Second
	awsApplyPollInterval = 5 * time.Second
)

type awsApplyModifiedScopes struct {
	Instance bool `json:"instance"`
	Cluster  bool `json:"cluster"`
}

type awsApplyWaitRequest struct {
	Instance awsrds.ScopePlan
	Cluster  awsrds.ScopePlan
}

type awsApplyGroupStatuses struct {
	Instance string
	Cluster  string
}

func (request awsApplyWaitRequest) modifiedScopes() awsApplyModifiedScopes {
	return awsApplyModifiedScopes{
		Instance: len(request.Instance.Parameters) > 0,
		Cluster:  len(request.Cluster.Parameters) > 0,
	}
}

type awsRDSClientFactory func(context.Context, *config.Config) (awsrds.Client, error)

type awsApplyWaitFunc func(context.Context, awsrds.Client, awsApplyWaitRequest, awsrds.Metadata) (awsApplyGroupStatuses, error)

type awsApplyResultEncoder func(*awsrds.ApplyResult) ([]byte, error)

var newAWSRDSClient awsRDSClientFactory = func(ctx context.Context, configuration *config.Config) (awsrds.Client, error) {
	cfg, err := configaws.LoadDefaultConfig(ctx, configaws.WithRegion(configuration.AwsRegion))
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	return rds.NewFromConfig(cfg), nil
}

var waitForAWSApply awsApplyWaitFunc = defaultWaitForAWSApply

var encodeAWSApplyResult awsApplyResultEncoder = func(result *awsrds.ApplyResult) ([]byte, error) {
	return json.Marshal(result)
}

func ApplyConfAwsRds(repeaters models.MetricsRepeater, gatherers []models.MetricsGatherer,
	logger logging.Logger, configuration *config.Config, mode AWSApplyMode) (int, int, string) {
	result := newAWSApplyResult(configuration)
	finish := func(exitCode, status int) (int, int, string) {
		output, encodeErr := encodeAWSApplyOutput(&result)
		if encodeErr != nil {
			logger.Errorf("AWS apply result serialization failed: %s", awsApplySafeErrorCode(encodeErr))
			if exitCode == awsApplyExitSuccess {
				exitCode = awsApplyExitFailure
				status = awsApplyTaskStatusFailure
			}
		}
		logAWSApplyEvent(logger, "aws_rds_apply_audit", map[string]interface{}{
			"exit_code": exitCode,
			"status":    status,
		})
		return exitCode, status, output
	}

	if configuration == nil {
		recordAWSApplyFailure(&result, awsrds.ScopeInstance, nil, fmt.Errorf("AWS RDS configuration is nil"))
		return finish(awsApplyExitFailure, awsApplyTaskStatusFailure)
	}

	// Recommendations and current values must come from the same single
	// collection. In particular, AWSApplyAll does not repeat this work for
	// immediate and pending-reboot parameters.
	metrics := utils.CollectMetrics(gatherers, logger, configuration)
	if metrics == nil {
		recordAWSApplyFailure(&result, awsrds.ScopeInstance, nil, fmt.Errorf("collect current metrics: no metrics returned"))
		return finish(awsApplyExitFailure, awsApplyTaskStatusFailure)
	}
	recommendationJSON := utils.ProcessRepeaters(
		metrics,
		repeaters,
		configuration,
		logger,
		models.ModeType{Name: "Configurations", Type: "GetJson"},
	)
	recommendations, err := decodeAWSRecommendations(recommendationJSON)
	if err != nil {
		recordAWSApplyFailure(&result, awsrds.ScopeInstance, nil, err)
		return finish(awsApplyExitFailure, awsApplyTaskStatusFailure)
	}

	ctx := context.Background()
	client, err := newAWSRDSClient(ctx, configuration)
	if err != nil {
		recordAWSApplyFailure(&result, awsrds.ScopeInstance, nil, err)
		return finish(awsApplyExitCode(err), awsApplyTaskStatusFailure)
	}

	metadata, err := awsrds.DiscoverInstanceForApply(ctx, client, configuration.AwsRDSDB)
	if err != nil {
		recordAWSApplyFailure(&result, awsrds.ScopeInstance, nil, err)
		return finish(awsApplyExitCode(err), awsApplyTaskStatusFailure)
	}
	if metadata.InstanceStatus != "available" {
		err = fmt.Errorf("DB instance %q status %q is not available", metadata.DBInstanceIdentifier, metadata.InstanceStatus)
		recordAWSApplyFailure(&result, awsrds.ScopeInstance, nil, err)
		return finish(awsApplyExitInstanceUnavailable, awsApplyTaskStatusFailure)
	}
	instanceGroup := awsInstanceGroupScope(metadata, configuration)
	if instanceGroup.mismatched(logger) {
		recordAWSGroupMismatchFailure(&result, instanceGroup)
		return finish(awsApplyExitParameterGroupMismatch, awsApplyTaskStatusFailure)
	}
	if !instanceGroup.inSync() {
		logger.Errorf(
			"DB instance %q parameter group %q status %q is not in-sync",
			metadata.DBInstanceIdentifier,
			instanceGroup.attached,
			instanceGroup.status,
		)
		recordAWSGroupReadinessFailure(&result, metadata, instanceGroup)
		return finish(awsApplyExitParameterGroupNotInSync, awsApplyTaskStatusFailure)
	}
	instanceParameters := map[string]awsrds.ParameterInfo{}
	instanceParameters, err = awsrds.ListParameters(
		ctx,
		client,
		configuration.AwsRDSParameterGroup,
		awsrds.ScopeInstance,
	)
	if err != nil {
		recordAWSApplyFailure(&result, awsrds.ScopeInstance, nil, err)
		return finish(awsApplyExitCode(err), awsApplyTaskStatusFailure)

	}

	clusterGroup := awsClusterGroupScope(metadata, configuration)
	if metadata.IsAurora() && !clusterGroup.inSync() {
		logger.Errorf(
			"DB cluster %q parameter group %q status %q is not in-sync",
			metadata.DBClusterIdentifier,
			clusterGroup.attached,
			clusterGroup.status,
		)
		recordAWSGroupReadinessFailure(&result, metadata, clusterGroup)
		return finish(awsApplyExitClusterParameterGroupNotInSync, awsApplyTaskStatusFailure)
	}
	clusterParameters := map[string]awsrds.ParameterInfo{}
	clusterClassificationNeeded := awsClusterClassificationNeeded(metadata, recommendations, instanceParameters, logger)
	if clusterClassificationNeeded {
		if clusterGroup.mismatched(logger) {
			recordAWSGroupMismatchFailure(&result, clusterGroup)
			return finish(awsApplyExitClusterParameterGroupMismatch, awsApplyTaskStatusFailure)
		}

		clusterParameters, err = awsrds.ListParameters(ctx, client, configuration.AwsRDSClusterParameterGroup, awsrds.ScopeCluster)
		if err != nil {
			recordAWSApplyFailure(&result, awsrds.ScopeCluster, nil, err)
			return finish(awsApplyExitCode(err), awsApplyTaskStatusFailure)
		}
	}

	plan, plannedResult := awsrds.BuildApplyPlan(awsrds.BuildApplyPlanInput{
		Metadata:                metadata,
		ConfiguredInstanceGroup: configuration.AwsRDSParameterGroup,
		ConfiguredClusterGroup:  configuration.AwsRDSClusterParameterGroup,
		InstanceParameters:      instanceParameters,
		ClusterParameters:       clusterParameters,
		Recommendations:         recommendations,
		CurrentValues:           awsCurrentParameterValues(metrics.DB.Conf.Variables),
		PendingRebootOnly:       mode == AWSApplyPendingRebootOnly,
	})
	result = plannedResult

	waitRequest, applyErr := applyAWSPlan(ctx, client, plan, &result, logger)
	modified := waitRequest.modifiedScopes()
	var (
		groupStatuses awsApplyGroupStatuses
		waitErr       error
	)
	if modified.Instance || modified.Cluster {
		groupStatuses, waitErr = waitForAWSApply(ctx, client, waitRequest, metadata)
		if waitErr != nil {
			logAWSApplyEvent(logger, "aws_rds_apply_wait", map[string]interface{}{
				"modified_scopes": modified,
				"scope_outcomes":  awsApplyWaitScopeOutcomes(modified, waitErr),
				"outcome":         "failed",
				"error":           awsApplySafeErrorCode(waitErr),
			})
			recordAWSWaitFailure(&result, waitRequest, waitErr)
			if applyErr == nil {
				applyErr = waitErr
			}
		} else {
			logAWSApplyEvent(logger, "aws_rds_apply_wait", map[string]interface{}{
				"modified_scopes": modified,
				"scope_outcomes":  awsApplyWaitScopeOutcomes(modified, nil),
				"outcome":         "success",
			})
		}
	}
	if applyErr != nil {
		logger.Errorf("AWS parameter apply failed: %s", awsApplySafeErrorCode(applyErr))
		return finish(awsApplyExitCode(applyErr), awsApplyTaskStatusFailure)
	}
	if awsApplyRequiresReboot(modified, groupStatuses) {
		logger.Info("AWS parameter apply completed with pending reboot")
		return finish(awsApplyExitPendingReboot, awsApplyTaskStatusFailure)
	}

	return finish(awsApplyExitSuccess, awsApplyTaskStatusSuccess)
}

func decodeAWSRecommendations(raw string) (map[string]interface{}, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var recommendations map[string]interface{}
	if err := decoder.Decode(&recommendations); err != nil {
		return nil, fmt.Errorf("decode AWS RDS recommendations: %w", err)
	}
	if decoder.More() {
		return nil, fmt.Errorf("decode AWS RDS recommendations: trailing data")
	}
	if recommendations == nil {
		recommendations = map[string]interface{}{}
	}
	return recommendations, nil
}

// awsGroupScope carries the per-scope values that the instance and cluster
// parameter-group checks differ by, so validation and failure recording are
// written once instead of once per scope.
type awsGroupScope struct {
	scope      awsrds.Scope
	label      string
	configured string
	attached   string
	status     string
}

func awsInstanceGroupScope(metadata awsrds.Metadata, configuration *config.Config) awsGroupScope {
	return awsGroupScope{
		scope:      awsrds.ScopeInstance,
		label:      "DB parameter group",
		configured: configuration.AwsRDSParameterGroup,
		attached:   metadata.DBParameterGroup,
		status:     metadata.DBParameterGroupStatus,
	}
}

func awsClusterGroupScope(metadata awsrds.Metadata, configuration *config.Config) awsGroupScope {
	return awsGroupScope{
		scope:      awsrds.ScopeCluster,
		label:      "DB cluster parameter group",
		configured: configuration.AwsRDSClusterParameterGroup,
		attached:   metadata.DBClusterParameterGroup,
		status:     metadata.DBClusterParameterGroupStatus,
	}
}

// mismatched reports whether the configured group is unusable as a mutation
// target, logging which of the three conditions rejected it.
func (group awsGroupScope) mismatched(logger logging.Logger) bool {
	switch {
	case group.configured == "":
		logger.Errorf("Configured %s is empty; attached group is %q", group.label, group.attached)
	case awsrds.IsDefaultParameterGroup(group.configured):
		logger.Errorf("Configured %s %q is AWS-managed and cannot be modified", group.label, group.configured)
	case group.configured != group.attached:
		logger.Errorf("Configured %s %q does not match attached group %q", group.label, group.configured, group.attached)
	default:
		return false
	}
	return true
}

// inSync reports whether AWS has finished applying the group.
func (group awsGroupScope) inSync() bool {
	return group.status == "in-sync"
}

func (group awsGroupScope) resultScope(result *awsrds.ApplyResult) *awsrds.ScopeResult {
	if group.scope == awsrds.ScopeCluster {
		return &result.Cluster
	}
	return &result.Instance
}

func awsClusterClassificationNeeded(metadata awsrds.Metadata, recommendations map[string]interface{}, instanceParameters map[string]awsrds.ParameterInfo, logger logging.Logger) bool {
	if !metadata.IsAurora() {
		return false
	}
	for name := range recommendations {
		if _, exists := instanceParameters[name]; !exists {
			logger.Infof("Recommendation %q is not in the instance parameter group %q", name, metadata.DBParameterGroup)
			return true
		}
	}
	return false
}

func recordAWSGroupMismatchFailure(result *awsrds.ApplyResult, group awsGroupScope) {
	if result == nil {
		return
	}
	scopeResult := group.resultScope(result)
	appendAWSGroupMismatchDiagnostic(scopeResult, group.configured, group.attached)
	scopeResult.Failed = append(scopeResult.Failed, awsrds.FailedBatch{
		Parameters: []string{},
		Error:      awsApplyErrorParameterGroupMismatch,
	})
}

func recordAWSGroupReadinessFailure(result *awsrds.ApplyResult, metadata awsrds.Metadata, group awsGroupScope) {
	if result == nil {
		return
	}
	failure := awsrds.FailedBatch{
		Parameters:           []string{},
		Error:                awsApplyErrorParameterGroupNotInSync,
		ParameterGroup:       &group.attached,
		ParameterGroupStatus: &group.status,
	}
	if group.scope == awsrds.ScopeCluster {
		failure.DBClusterIdentifier = &metadata.DBClusterIdentifier
	} else {
		failure.DBInstanceIdentifier = &metadata.DBInstanceIdentifier
	}
	scopeResult := group.resultScope(result)
	scopeResult.Failed = append(scopeResult.Failed, failure)
}

func appendAWSGroupMismatchDiagnostic(result *awsrds.ScopeResult, expected, actual string) {
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Reason == awsrds.SkipGroupMismatch && diagnostic.ExpectedGroup == expected && diagnostic.ActualGroup == actual {
			return
		}
	}
	result.Diagnostics = append(result.Diagnostics, awsrds.ScopeDiagnostic{
		Reason:        awsrds.SkipGroupMismatch,
		ExpectedGroup: expected,
		ActualGroup:   actual,
	})
}

func awsCurrentParameterValues(values models.MetricGroupValue) map[string]interface{} {
	current := make(map[string]interface{}, len(values))
	for name, value := range values {
		switch nested := value.(type) {
		case models.MetricGroupValue:
			if setting, exists := nested["setting"]; exists {
				value = setting
			}
		case map[string]interface{}:
			if setting, exists := nested["setting"]; exists {
				value = setting
			}
		}
		current[name] = value
	}
	return current
}

func awsApplyRequiresReboot(modified awsApplyModifiedScopes, statuses awsApplyGroupStatuses) bool {
	return modified.Instance && statuses.Instance == "pending-reboot" ||
		modified.Cluster && statuses.Cluster == "pending-reboot"
}

func applyAWSPlan(ctx context.Context, client awsrds.Client, plan awsrds.ApplyPlan, result *awsrds.ApplyResult, logger logging.Logger) (awsApplyWaitRequest, error) {
	waitRequest := awsApplyWaitRequest{
		Instance: awsrds.ScopePlan{Group: plan.Instance.Group, Parameters: []types.Parameter{}},
		Cluster:  awsrds.ScopePlan{Group: plan.Cluster.Group, Parameters: []types.Parameter{}},
	}
	logAWSApplyEvent(logger, "aws_rds_apply_plan", map[string]interface{}{
		"instance": awsApplyScopePlanEventFields(plan.Instance),
		"cluster":  awsApplyScopePlanEventFields(plan.Cluster),
	})

	instanceApplied, err := applyAWSScopeBatches(ctx, client, awsrds.ScopeInstance, plan.Instance, &result.Instance, logger)
	waitRequest.Instance.Parameters = instanceApplied
	if err != nil {
		return waitRequest, err
	}

	clusterApplied, err := applyAWSScopeBatches(ctx, client, awsrds.ScopeCluster, plan.Cluster, &result.Cluster, logger)
	waitRequest.Cluster.Parameters = clusterApplied
	if err != nil {
		return waitRequest, err
	}

	return waitRequest, nil
}

func applyAWSScopeBatches(ctx context.Context, client awsrds.Client, scope awsrds.Scope, plan awsrds.ScopePlan, result *awsrds.ScopeResult, logger logging.Logger) ([]types.Parameter, error) {
	applied := []types.Parameter{}
	for start := 0; start < len(plan.Parameters); start += awsApplyBatchSize {
		end := start + awsApplyBatchSize
		if end > len(plan.Parameters) {
			end = len(plan.Parameters)
		}
		batch := append([]types.Parameter(nil), plan.Parameters[start:end]...)
		names := awsParameterNames(batch)

		var err error
		switch scope {
		case awsrds.ScopeInstance:
			_, err = client.ModifyDBParameterGroup(ctx, &rds.ModifyDBParameterGroupInput{
				DBParameterGroupName: aws.String(plan.Group),
				Parameters:           batch,
			})
		case awsrds.ScopeCluster:
			_, err = client.ModifyDBClusterParameterGroup(ctx, &rds.ModifyDBClusterParameterGroupInput{
				DBClusterParameterGroupName: aws.String(plan.Group),
				Parameters:                  batch,
			})
		default:
			err = fmt.Errorf("unsupported AWS parameter scope %q", scope)
		}

		batchNumber := start/awsApplyBatchSize + 1
		if err != nil {
			errorCode := awsApplySafeErrorCode(err)
			result.Failed = append(result.Failed, awsrds.FailedBatch{
				Parameters: names,
				Error:      errorCode,
			})
			logAWSApplyEvent(logger, "aws_rds_apply_batch", map[string]interface{}{
				"scope":   string(scope),
				"group":   plan.Group,
				"batch":   batchNumber,
				"names":   names,
				"outcome": string(awsrds.OutcomeFailed),
				"error":   errorCode,
			})

			return applied, fmt.Errorf("modify %s parameter group %q for parameters %s: %w", scope, plan.Group, strings.Join(names, ","), err)
		}

		applied = append(applied, batch...)
		result.Applied = append(result.Applied, names...)
		logAWSApplyEvent(logger, "aws_rds_apply_batch", map[string]interface{}{
			"scope":   string(scope),
			"group":   plan.Group,
			"batch":   batchNumber,
			"names":   names,
			"outcome": string(awsrds.OutcomeApplied),
		})
	}
	return applied, nil
}

func awsParameterNames(parameters []types.Parameter) []string {
	names := make([]string, len(parameters))
	for index, parameter := range parameters {
		names[index] = aws.ToString(parameter.ParameterName)
	}
	return names
}

func newAWSApplyResult(configuration *config.Config) awsrds.ApplyResult {
	var instanceGroup, clusterGroup string
	if configuration != nil {
		instanceGroup = configuration.AwsRDSParameterGroup
		clusterGroup = configuration.AwsRDSClusterParameterGroup
	}
	return awsrds.ApplyResult{
		Instance: awsrds.NewScopeResult(instanceGroup),
		Cluster:  awsrds.NewScopeResult(clusterGroup),
	}
}

func recordAWSApplyFailure(result *awsrds.ApplyResult, scope awsrds.Scope, parameters []string, err error) {
	failure := awsrds.FailedBatch{
		Parameters: append([]string(nil), parameters...),
		Error:      awsApplySafeErrorCode(err),
	}
	if scope == awsrds.ScopeCluster {
		result.Cluster.Failed = append(result.Cluster.Failed, failure)
		return
	}
	result.Instance.Failed = append(result.Instance.Failed, failure)
}

type awsApplyWaitError struct {
	Scope      awsrds.Scope
	Unresolved awsApplyModifiedScopes
	Err        error
}

func (err *awsApplyWaitError) Error() string {
	if err.Unresolved.Instance && err.Unresolved.Cluster {
		return fmt.Sprintf("wait for instance and cluster parameter apply: %v", err.Err)
	}
	return fmt.Sprintf("wait for %s parameter apply: %v", err.Scope, err.Err)
}

func (err *awsApplyWaitError) Unwrap() error {
	return err.Err
}

func newAWSApplyTimeoutErrorForScopes(unresolved awsApplyModifiedScopes) error {
	scope := awsrds.ScopeInstance
	if !unresolved.Instance && unresolved.Cluster {
		scope = awsrds.ScopeCluster
	}
	return &awsApplyWaitError{Scope: scope, Unresolved: unresolved, Err: errAWSApplyWaitTimeout}
}

func newAWSApplyPollingError(scope awsrds.Scope, unresolved awsApplyModifiedScopes, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return newAWSApplyTimeoutErrorForScopes(unresolved)
	}
	return &awsApplyWaitError{Scope: scope, Unresolved: unresolved, Err: err}
}

func recordAWSWaitFailure(result *awsrds.ApplyResult, request awsApplyWaitRequest, err error) {
	scope := awsrds.ScopeInstance
	var scopedError *awsApplyWaitError
	if errors.As(err, &scopedError) {
		if scopedError.Unresolved.Instance || scopedError.Unresolved.Cluster {
			if scopedError.Unresolved.Instance {
				recordAWSApplyFailure(result, awsrds.ScopeInstance, awsParameterNames(request.Instance.Parameters), err)
			}
			if scopedError.Unresolved.Cluster {
				recordAWSApplyFailure(result, awsrds.ScopeCluster, awsParameterNames(request.Cluster.Parameters), err)
			}
			return
		}
		scope = scopedError.Scope
	} else if len(request.Instance.Parameters) == 0 {
		scope = awsrds.ScopeCluster
	}

	parameters := request.Instance.Parameters
	if scope == awsrds.ScopeCluster {
		parameters = request.Cluster.Parameters
	}
	recordAWSApplyFailure(result, scope, awsParameterNames(parameters), err)
}

func encodeAWSApplyOutput(result *awsrds.ApplyResult) (string, error) {
	encoded, err := encodeAWSApplyResult(result)
	if err != nil {
		return awsApplySerializationFailureOutput, err
	}
	return string(encoded), nil
}

func awsApplyExitCode(err error) int {
	if errors.Is(err, errAWSApplyWaitTimeout) {
		return awsApplyExitTimeout
	}
	if isAWSAccessDenied(err) {
		return awsApplyExitAccessDenied
	}
	return awsApplyExitFailure
}

func awsApplySafeErrorCode(err error) string {
	if errors.Is(err, errAWSApplyWaitTimeout) {
		return awsApplyErrorTimeout
	}
	if isAWSAccessDenied(err) {
		return awsApplyErrorAccessDenied
	}

	var apiError awsAPIError
	if errors.As(err, &apiError) {
		if code := apiError.ErrorCode(); isSafeAWSAPIErrorCode(code) {
			return "aws-api:" + code
		}
	}
	return awsApplyErrorAWSAPI
}

func isAWSAccessDenied(err error) bool {
	if err == nil {
		return false
	}
	var apiError awsAPIError
	if errors.As(err, &apiError) && strings.Contains(strings.ToLower(apiError.ErrorCode()), "accessdenied") {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "accessdenied")
}

func isSafeAWSAPIErrorCode(code string) bool {
	if code == "" || len(code) > 128 {
		return false
	}
	for _, char := range code {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func defaultWaitForAWSApply(ctx context.Context, client awsrds.Client, request awsApplyWaitRequest, metadata awsrds.Metadata) (awsApplyGroupStatuses, error) {
	scopes := request.modifiedScopes()
	var statuses awsApplyGroupStatuses
	if !scopes.Instance && !scopes.Cluster {
		return statuses, nil
	}

	waitContext, cancel := context.WithTimeout(ctx, awsApplyWaitTimeout)
	defer cancel()

	instanceReady := !scopes.Instance
	clusterReady := !scopes.Cluster
	for {
		if !instanceReady {
			ready, status, err := awsApplyScopeReady(waitContext, client, metadata, awsrds.ScopeInstance, request.Instance)
			if err != nil {
				return statuses, newAWSApplyPollingError(awsrds.ScopeInstance, awsApplyModifiedScopes{
					Instance: scopes.Instance && !instanceReady,
					Cluster:  scopes.Cluster && !clusterReady,
				}, err)
			}
			instanceReady = ready
			if ready {
				statuses.Instance = status
			}
		}
		if !clusterReady {
			ready, status, err := awsApplyScopeReady(waitContext, client, metadata, awsrds.ScopeCluster, request.Cluster)
			if err != nil {
				return statuses, newAWSApplyPollingError(awsrds.ScopeCluster, awsApplyModifiedScopes{
					Instance: scopes.Instance && !instanceReady,
					Cluster:  scopes.Cluster && !clusterReady,
				}, err)
			}
			clusterReady = ready
			if ready {
				statuses.Cluster = status
			}
		}
		if instanceReady && clusterReady {
			return statuses, nil
		}

		timer := time.NewTimer(awsApplyPollInterval)
		select {
		case <-waitContext.Done():
			timer.Stop()
			return statuses, newAWSApplyTimeoutErrorForScopes(awsApplyModifiedScopes{
				Instance: scopes.Instance && !instanceReady,
				Cluster:  scopes.Cluster && !clusterReady,
			})
		case <-timer.C:
		}
	}
}

func awsApplyScopeReady(ctx context.Context, client awsrds.Client, metadata awsrds.Metadata, scope awsrds.Scope, plan awsrds.ScopePlan) (bool, string, error) {
	observed, err := awsAppliedParametersObserved(ctx, client, scope, plan)
	if err != nil || !observed {
		return false, "", err
	}

	switch scope {
	case awsrds.ScopeInstance:
		output, err := client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{
			DBInstanceIdentifier: aws.String(metadata.DBInstanceIdentifier),
		})
		if err != nil {
			return false, "", fmt.Errorf("poll DB instance %q: %w", metadata.DBInstanceIdentifier, err)
		}
		if output == nil || len(output.DBInstances) != 1 {
			return false, "", fmt.Errorf("poll DB instance %q: expected one result", metadata.DBInstanceIdentifier)
		}
		instance := output.DBInstances[0]
		if aws.ToString(instance.DBInstanceStatus) != "available" {
			return false, "", nil
		}
		applyStatus := ""
		for _, group := range instance.DBParameterGroups {
			if aws.ToString(group.DBParameterGroupName) == plan.Group {
				applyStatus = aws.ToString(group.ParameterApplyStatus)
				break
			}
		}
		if applyStatus == "" {
			return false, "", fmt.Errorf("poll DB instance %q: no status for parameter group %q", metadata.DBInstanceIdentifier, plan.Group)
		}
		if applyStatus == "failed" {
			return false, applyStatus, fmt.Errorf("DB instance %q parameter group %q apply status is %q", metadata.DBInstanceIdentifier, plan.Group, applyStatus)
		}
		if applyStatus != "in-sync" && applyStatus != "pending-reboot" {
			return false, applyStatus, nil
		}
		return true, applyStatus, nil

	case awsrds.ScopeCluster:
		output, err := client.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{
			DBClusterIdentifier: aws.String(metadata.DBClusterIdentifier),
		})
		if err != nil {
			return false, "", fmt.Errorf("poll DB cluster %q: %w", metadata.DBClusterIdentifier, err)
		}
		if output == nil || len(output.DBClusters) != 1 {
			return false, "", fmt.Errorf("poll DB cluster %q: expected one result", metadata.DBClusterIdentifier)
		}
		cluster := output.DBClusters[0]
		if aws.ToString(cluster.Status) != "available" {
			return false, "", nil
		}
		if aws.ToString(cluster.DBClusterParameterGroup) != plan.Group {
			return false, "", fmt.Errorf("poll DB cluster %q: attached parameter group does not match %q", metadata.DBClusterIdentifier, plan.Group)
		}
		for _, member := range cluster.DBClusterMembers {
			if aws.ToString(member.DBInstanceIdentifier) != metadata.DBInstanceIdentifier {
				continue
			}
			applyStatus := aws.ToString(member.DBClusterParameterGroupStatus)
			if applyStatus == "failed" {
				return false, applyStatus, fmt.Errorf("DB cluster %q parameter group %q apply status is %q", metadata.DBClusterIdentifier, plan.Group, applyStatus)
			}
			if applyStatus != "in-sync" && applyStatus != "pending-reboot" {
				return false, applyStatus, nil
			}
			return true, applyStatus, nil
		}
		return false, "", fmt.Errorf("poll DB cluster %q: no parameter group status for DB instance %q", metadata.DBClusterIdentifier, metadata.DBInstanceIdentifier)

	default:
		return false, "", fmt.Errorf("unsupported AWS parameter scope %q", scope)
	}
}

func awsAppliedParametersObserved(ctx context.Context, client awsrds.Client, scope awsrds.Scope, plan awsrds.ScopePlan) (bool, error) {
	listed, err := awsrds.ListParameters(ctx, client, plan.Group, scope)
	if err != nil {
		return false, fmt.Errorf("poll %s parameter group %q: %w", scope, plan.Group, err)
	}
	for _, parameter := range plan.Parameters {
		name := aws.ToString(parameter.ParameterName)
		actual, exists := listed[name]
		if !exists || !actual.HasParameterValue ||
			actual.ParameterValue != aws.ToString(parameter.ParameterValue) {
			return false, nil
		}
	}
	return true, nil
}
