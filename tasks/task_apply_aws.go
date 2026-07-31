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
// parameter application: 8 is a general AWS apply failure and 9 identifies
// AccessDenied. Group mismatches are safe per-scope skips and use exit code 0.
const (
	awsApplyExitSuccess             = 0
	awsApplyExitInstanceUnavailable = 1
	awsApplyExitFailure             = 8
	awsApplyExitAccessDenied        = 9

	awsApplyTaskStatusSuccess = 1
	awsApplyTaskStatusFailure = 4
	awsApplyBatchSize         = 20
)

const (
	awsApplyWaitTimeout  = 400 * time.Second
	awsApplyPollInterval = 3 * time.Second
)

type awsApplyModifiedScopes struct {
	Instance bool
	Cluster  bool
}

type awsApplyWaitRequest struct {
	Metadata awsrds.Metadata
	Instance awsrds.ScopePlan
	Cluster  awsrds.ScopePlan
}

func (request awsApplyWaitRequest) modifiedScopes() awsApplyModifiedScopes {
	return awsApplyModifiedScopes{
		Instance: len(request.Instance.Parameters) > 0,
		Cluster:  len(request.Cluster.Parameters) > 0,
	}
}

type awsRDSClientFactory func(context.Context, *config.Config) (awsrds.Client, error)

type awsApplyWaitFunc func(context.Context, awsrds.Client, awsApplyWaitRequest) error

var newAWSRDSClient awsRDSClientFactory = func(ctx context.Context, configuration *config.Config) (awsrds.Client, error) {
	cfg, err := configaws.LoadDefaultConfig(ctx, configaws.WithRegion(configuration.AwsRegion))
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	return rds.NewFromConfig(cfg), nil
}

var waitForAWSApply awsApplyWaitFunc = defaultWaitForAWSApply

// ApplyConfAwsRds obtains one live recommendation snapshot, refreshes AWS
// topology, routes every eligible value to its owning parameter-group API,
// and returns the deterministic per-scope result as task output.
func ApplyConfAwsRds(repeaters models.MetricsRepeater, gatherers []models.MetricsGatherer,
	logger logging.Logger, configuration *config.Config, mode AWSApplyMode) (int, int, string) {

	result := newAWSApplyResult(configuration)
	fail := func(exitCode int) (int, int, string) {
		return exitCode, awsApplyTaskStatusFailure, marshalAWSApplyResult(&result)
	}

	if configuration == nil {
		recordAWSApplyFailure(&result, awsrds.ScopeInstance, nil, fmt.Errorf("AWS RDS configuration is nil"))
		return fail(awsApplyExitFailure)
	}

	// Recommendations and current values must come from the same single
	// collection. In particular, AWSApplyAll does not repeat this work for
	// immediate and pending-reboot parameters.
	metrics := utils.CollectMetrics(gatherers, logger, configuration)
	if metrics == nil {
		recordAWSApplyFailure(&result, awsrds.ScopeInstance, nil, fmt.Errorf("collect current metrics: no metrics returned"))
		return fail(awsApplyExitFailure)
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
		return fail(awsApplyExitFailure)
	}

	ctx := context.Background()
	client, err := newAWSRDSClient(ctx, configuration)
	if err != nil {
		recordAWSApplyFailure(&result, awsrds.ScopeInstance, nil, err)
		return fail(awsApplyExitCode(err))
	}

	metadata, err := awsrds.DiscoverInstance(ctx, client, configuration.AwsRDSDB)
	if err != nil {
		recordAWSApplyFailure(&result, awsrds.ScopeInstance, nil, err)
		return fail(awsApplyExitCode(err))
	}
	if metadata.InstanceStatus != "available" {
		err = fmt.Errorf("DB instance %q status %q is not available", metadata.DBInstanceIdentifier, metadata.InstanceStatus)
		recordAWSApplyFailure(&result, awsrds.ScopeInstance, nil, err)
		return fail(awsApplyExitInstanceUnavailable)
	}

	groupValidation := validateAWSGroups(logger, metadata, configuration)

	instanceParameters := map[string]awsrds.ParameterInfo{}
	instanceLookupGroup := configuration.AwsRDSParameterGroup
	if groupValidation.InstanceMismatch {
		// A mismatched configured group is never a mutation target. Read the
		// attached group only to classify recommendations so BuildApplyPlan can
		// report the mismatch without blocking the cluster scope.
		instanceLookupGroup = metadata.DBParameterGroup
	}
	if instanceLookupGroup != "" {
		instanceParameters, err = awsrds.ListParameters(
			ctx,
			client,
			instanceLookupGroup,
			awsrds.ScopeInstance,
		)
		if err != nil {
			recordAWSApplyFailure(&result, awsrds.ScopeInstance, nil, err)
			return fail(awsApplyExitCode(err))
		}
	}

	clusterParameters := map[string]awsrds.ParameterInfo{}
	clusterLookup := awsrds.SelectClusterParameterGroupLookup(
		metadata,
		configuration.AwsRDSClusterParameterGroup,
	)
	if groupValidation.ClusterMismatch {
		// Keep the selector call as the normal lookup contract, but never read
		// or modify a mismatched configured group. Attached membership is safe
		// classification data and the original configuration remains the plan
		// target, so BuildApplyPlan rejects every cluster mutation.
		clusterLookup = awsrds.ClusterParameterGroupLookup{
			Group:              metadata.DBClusterParameterGroup,
			ClassificationOnly: true,
		}
	}
	if clusterLookup.Group != "" {
		clusterParameters, err = awsrds.ListParameters(
			ctx,
			client,
			clusterLookup.Group,
			awsrds.ScopeCluster,
		)
		if err != nil {
			recordAWSApplyFailure(&result, awsrds.ScopeCluster, nil, err)
			return fail(awsApplyExitCode(err))
		}
		if clusterLookup.ClassificationOnly {
			logger.Infof("DB cluster parameter group %q loaded for recommendation classification only", clusterLookup.Group)
		}
	}

	plan, plannedResult := awsrds.BuildApplyPlan(awsrds.BuildApplyPlanInput{
		Metadata:                metadata,
		ConfiguredInstanceGroup: configuration.AwsRDSParameterGroup,
		ConfiguredClusterGroup:  configuration.AwsRDSClusterParameterGroup,
		InstanceParameters:      instanceParameters,
		ClusterParameters:       clusterParameters,
		Recommendations:         recommendations,
		CurrentValues:           metrics.DB.Conf.Variables,
		PendingRebootOnly:       mode == AWSApplyPendingRebootOnly,
	})
	result = plannedResult

	waitRequest, applyErr := applyAWSPlan(ctx, client, plan, &result)
	waitRequest.Metadata = metadata
	modified := waitRequest.modifiedScopes()
	if modified.Instance || modified.Cluster {
		if waitErr := waitForAWSApply(ctx, client, waitRequest); waitErr != nil {
			recordAWSWaitFailure(&result, waitRequest, waitErr)
			if applyErr == nil {
				applyErr = waitErr
			}
		}
	}
	if applyErr != nil {
		logger.Errorf("AWS parameter apply failed: %v", applyErr)
		return fail(awsApplyExitCode(applyErr))
	}

	return awsApplyExitSuccess, awsApplyTaskStatusSuccess, marshalAWSApplyResult(&result)
}

func decodeAWSRecommendations(raw string) (map[string]interface{}, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var recommendations map[string]interface{}
	if err := decoder.Decode(&recommendations); err != nil {
		return nil, fmt.Errorf("decode AWS RDS recommendations: %w", err)
	}
	if recommendations == nil {
		recommendations = map[string]interface{}{}
	}
	return recommendations, nil
}

type awsGroupValidation struct {
	InstanceMismatch bool
	ClusterMismatch  bool
}

func validateAWSGroups(logger logging.Logger, metadata awsrds.Metadata, configuration *config.Config) awsGroupValidation {
	validation := awsGroupValidation{}
	if configured := configuration.AwsRDSParameterGroup; configured != "" && configured != metadata.DBParameterGroup {
		validation.InstanceMismatch = true
		logger.Errorf("Configured DB parameter group %q does not match attached group %q", configured, metadata.DBParameterGroup)
	}
	if configured := configuration.AwsRDSClusterParameterGroup; configured != "" && configured != metadata.DBClusterParameterGroup {
		validation.ClusterMismatch = true
		logger.Errorf("Configured DB cluster parameter group %q does not match attached group %q", configured, metadata.DBClusterParameterGroup)
	}
	return validation
}

func applyAWSPlan(ctx context.Context, client awsrds.Client, plan awsrds.ApplyPlan, result *awsrds.ApplyResult) (awsApplyWaitRequest, error) {
	waitRequest := awsApplyWaitRequest{
		Instance: awsrds.ScopePlan{Group: plan.Instance.Group, Parameters: []types.Parameter{}},
		Cluster:  awsrds.ScopePlan{Group: plan.Cluster.Group, Parameters: []types.Parameter{}},
	}

	instanceApplied, err := applyAWSScopeBatches(ctx, client, awsrds.ScopeInstance, plan.Instance, &result.Instance)
	waitRequest.Instance.Parameters = instanceApplied
	if err != nil {
		return waitRequest, err
	}

	clusterApplied, err := applyAWSScopeBatches(ctx, client, awsrds.ScopeCluster, plan.Cluster, &result.Cluster)
	waitRequest.Cluster.Parameters = clusterApplied
	if err != nil {
		return waitRequest, err
	}

	return waitRequest, nil
}

func applyAWSScopeBatches(ctx context.Context, client awsrds.Client, scope awsrds.Scope, plan awsrds.ScopePlan, result *awsrds.ScopeResult) ([]types.Parameter, error) {
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

		if err != nil {
			result.Failed = append(result.Failed, awsrds.FailedBatch{
				Parameters: names,
				Error:      err.Error(),
			})
			return applied, fmt.Errorf("modify %s parameter group %q for parameters %s: %w", scope, plan.Group, strings.Join(names, ","), err)
		}

		applied = append(applied, batch...)
		result.Applied = append(result.Applied, names...)
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
		Instance: newAWSApplyScopeResult(instanceGroup),
		Cluster:  newAWSApplyScopeResult(clusterGroup),
	}
}

func newAWSApplyScopeResult(group string) awsrds.ScopeResult {
	return awsrds.ScopeResult{
		Group:   group,
		Applied: []string{},
		Skipped: []awsrds.SkippedVariable{},
		Failed:  []awsrds.FailedBatch{},
	}
}

func recordAWSApplyFailure(result *awsrds.ApplyResult, scope awsrds.Scope, parameters []string, err error) {
	failure := awsrds.FailedBatch{
		Parameters: append([]string(nil), parameters...),
		Error:      err.Error(),
	}
	if scope == awsrds.ScopeCluster {
		result.Cluster.Failed = append(result.Cluster.Failed, failure)
		return
	}
	result.Instance.Failed = append(result.Instance.Failed, failure)
}

type awsApplyWaitError struct {
	Scope awsrds.Scope
	Err   error
}

func (err *awsApplyWaitError) Error() string {
	return fmt.Sprintf("wait for %s parameter apply: %v", err.Scope, err.Err)
}

func (err *awsApplyWaitError) Unwrap() error {
	return err.Err
}

func recordAWSWaitFailure(result *awsrds.ApplyResult, request awsApplyWaitRequest, err error) {
	scope := awsrds.ScopeInstance
	var scopedError *awsApplyWaitError
	if errors.As(err, &scopedError) {
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

func marshalAWSApplyResult(result *awsrds.ApplyResult) string {
	result.Sort()
	encoded, err := json.Marshal(result)
	if err != nil {
		return `{"instance":{"applied":[],"skipped":[],"failed":[{"parameters":[],"error":"serialize AWS apply result"}]},"cluster":{"applied":[],"skipped":[],"failed":[]}}`
	}
	return string(encoded)
}

func awsApplyExitCode(err error) int {
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "accessdenied") {
		return awsApplyExitAccessDenied
	}
	return awsApplyExitFailure
}

func defaultWaitForAWSApply(ctx context.Context, client awsrds.Client, request awsApplyWaitRequest) error {
	scopes := request.modifiedScopes()
	if !scopes.Instance && !scopes.Cluster {
		return nil
	}

	waitContext, cancel := context.WithTimeout(ctx, awsApplyWaitTimeout)
	defer cancel()

	instanceReady := !scopes.Instance
	clusterReady := !scopes.Cluster
	for {
		if !instanceReady {
			ready, err := awsApplyScopeReady(waitContext, client, request.Metadata, awsrds.ScopeInstance, request.Instance)
			if err != nil {
				return &awsApplyWaitError{Scope: awsrds.ScopeInstance, Err: err}
			}
			instanceReady = ready
		}
		if !clusterReady {
			ready, err := awsApplyScopeReady(waitContext, client, request.Metadata, awsrds.ScopeCluster, request.Cluster)
			if err != nil {
				return &awsApplyWaitError{Scope: awsrds.ScopeCluster, Err: err}
			}
			clusterReady = ready
		}
		if instanceReady && clusterReady {
			return nil
		}

		timer := time.NewTimer(awsApplyPollInterval)
		select {
		case <-waitContext.Done():
			timer.Stop()
			scope := awsrds.ScopeInstance
			if instanceReady {
				scope = awsrds.ScopeCluster
			}
			return &awsApplyWaitError{Scope: scope, Err: fmt.Errorf("timed out after %s", awsApplyWaitTimeout)}
		case <-timer.C:
		}
	}
}

func awsApplyScopeReady(ctx context.Context, client awsrds.Client, metadata awsrds.Metadata, scope awsrds.Scope, plan awsrds.ScopePlan) (bool, error) {
	observed, err := awsAppliedParametersObserved(ctx, client, scope, plan)
	if err != nil || !observed {
		return false, err
	}

	switch scope {
	case awsrds.ScopeInstance:
		output, err := client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{
			DBInstanceIdentifier: aws.String(metadata.DBInstanceIdentifier),
		})
		if err != nil {
			return false, fmt.Errorf("poll DB instance %q: %w", metadata.DBInstanceIdentifier, err)
		}
		if output == nil || len(output.DBInstances) != 1 {
			return false, fmt.Errorf("poll DB instance %q: expected one result", metadata.DBInstanceIdentifier)
		}
		instance := output.DBInstances[0]
		if aws.ToString(instance.DBInstanceStatus) != "available" {
			return false, nil
		}
		applyStatus := ""
		for _, group := range instance.DBParameterGroups {
			if aws.ToString(group.DBParameterGroupName) == plan.Group {
				applyStatus = aws.ToString(group.ParameterApplyStatus)
				break
			}
		}
		if applyStatus == "" {
			return false, fmt.Errorf("poll DB instance %q: no status for parameter group %q", metadata.DBInstanceIdentifier, plan.Group)
		}
		if applyStatus != "in-sync" && applyStatus != "pending-reboot" {
			return false, nil
		}
		return true, nil

	case awsrds.ScopeCluster:
		output, err := client.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{
			DBClusterIdentifier: aws.String(metadata.DBClusterIdentifier),
		})
		if err != nil {
			return false, fmt.Errorf("poll DB cluster %q: %w", metadata.DBClusterIdentifier, err)
		}
		if output == nil || len(output.DBClusters) != 1 {
			return false, fmt.Errorf("poll DB cluster %q: expected one result", metadata.DBClusterIdentifier)
		}
		if aws.ToString(output.DBClusters[0].Status) != "available" {
			return false, nil
		}
		return true, nil

	default:
		return false, fmt.Errorf("unsupported AWS parameter scope %q", scope)
	}
}

func awsAppliedParametersObserved(ctx context.Context, client awsrds.Client, scope awsrds.Scope, plan awsrds.ScopePlan) (bool, error) {
	expected := make(map[string]string, len(plan.Parameters))
	for _, parameter := range plan.Parameters {
		expected[aws.ToString(parameter.ParameterName)] = aws.ToString(parameter.ParameterValue)
	}
	if len(expected) == 0 {
		return true, nil
	}

	type parameterPage struct {
		Parameters []types.Parameter
		Marker     *string
	}

	seenMarkers := map[string]struct{}{}
	var marker *string
	for {
		var page parameterPage
		switch scope {
		case awsrds.ScopeInstance:
			output, err := client.DescribeDBParameters(ctx, &rds.DescribeDBParametersInput{
				DBParameterGroupName: aws.String(plan.Group),
				Marker:               marker,
			})
			if err != nil {
				return false, fmt.Errorf("poll DB parameter group %q: %w", plan.Group, err)
			}
			if output == nil {
				return false, fmt.Errorf("poll DB parameter group %q: nil output", plan.Group)
			}
			page = parameterPage{Parameters: output.Parameters, Marker: output.Marker}

		case awsrds.ScopeCluster:
			output, err := client.DescribeDBClusterParameters(ctx, &rds.DescribeDBClusterParametersInput{
				DBClusterParameterGroupName: aws.String(plan.Group),
				Marker:                      marker,
			})
			if err != nil {
				return false, fmt.Errorf("poll DB cluster parameter group %q: %w", plan.Group, err)
			}
			if output == nil {
				return false, fmt.Errorf("poll DB cluster parameter group %q: nil output", plan.Group)
			}
			page = parameterPage{Parameters: output.Parameters, Marker: output.Marker}

		default:
			return false, fmt.Errorf("unsupported AWS parameter scope %q", scope)
		}

		for _, parameter := range page.Parameters {
			name := aws.ToString(parameter.ParameterName)
			if value, exists := expected[name]; exists && aws.ToString(parameter.ParameterValue) == value {
				delete(expected, name)
			}
		}
		if len(expected) == 0 {
			return true, nil
		}

		nextMarker := aws.ToString(page.Marker)
		if nextMarker == "" {
			return false, nil
		}
		if _, exists := seenMarkers[nextMarker]; exists {
			return false, fmt.Errorf("poll %s parameter group %q: repeated marker %q", scope, plan.Group, nextMarker)
		}
		seenMarkers[nextMarker] = struct{}{}
		marker = aws.String(nextMarker)
	}
}
