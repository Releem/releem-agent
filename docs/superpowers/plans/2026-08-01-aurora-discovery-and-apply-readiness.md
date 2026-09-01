# Aurora Discovery Resilience and Apply Readiness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Preserve complete metrics cycles during transient live RDS discovery failures and block every AWS parameter apply until the attached instance parameter group is strictly in-sync.

**Architecture:** Give the enhanced-metrics gatherer an explicit startup metadata snapshot and a mutex-protected last-known-good cache that is refreshed once per report. Restore a pre-plan readiness guard in the shared AWS apply entry point, returning historical exit code 2 before either parameter-group scope is listed or modified.

**Tech Stack:** Go 1.26.4, AWS SDK for Go v2 (rds and cloudwatchlogs), existing awsrds.Metadata and fake AWS clients, Go testing, sync, and sync/atomic.

## Global Constraints

- Modify Releem_Agent only; do not change Platform payloads or recommendation logic.
- Keep startup RDS discovery fail-closed and keep endpoint mutation startup-only.
- Attempt live discovery exactly once per enhanced-metrics report.
- Update cached metadata only after successful discovery and use one local metadata snapshot for each report.
- Do not change the general failure policy in utils.CollectMetrics.
- Require metadata.DBParameterGroupStatus == "in-sync" before any instance or cluster parameter-group listing or mutation.
- Treat applying, pending-reboot, empty, and every other pre-apply status as not ready with exit code 2 and task status 4.
- Preserve post-modification waiter acceptance of pending-reboot.
- Preserve the unrelated untracked tests/__pycache__/ directory.

---

### Task 1: Last-known-good metadata for enhanced metrics

**Files:**
- Modify: metrics/system/awsrdsenhancedmetrics.go:20-32,190-211
- Modify: metrics/system/awsrdsenhancedmetrics_test.go:1-252
- Modify: awsrds/payload_contract_test.go:158-173
- Modify: main.go:119-135

**Interfaces:**
- Consumes: startup awsrds.Metadata returned by awsrds.DiscoverInstance and the existing RDSMetadataDiscoverer function.
- Produces: NewAWSRDSEnhancedMetricsGatherer(logger logging.Logger, cwlogsclient *cloudwatchlogs.Client, configuration *config.Config, initialMetadata awsrds.Metadata, discoverMetadata RDSMetadataDiscoverer) *AWSRDSEnhancedMetricsGatherer.
- Produces: metadataForReport(ctx context.Context) awsrds.Metadata, returning either a fresh successful snapshot or the mutex-protected last-known-good snapshot.

- [ ] **Step 1: Rewrite the lifecycle test for fallback and recovery**

Update all constructor calls in metrics/system/awsrdsenhancedmetrics_test.go to pass explicit initial metadata. Replace the third-report failure expectation in TestAWSRDSEnhancedMetricsGathererRefreshesMetadataForEveryReport with a four-report sequence:

~~~go
writerMetadata := awsrds.Metadata{
	DBInstanceIdentifier:    "orders-1",
	DBInstanceResourceID:    "db-resource-orders-1",
	DBInstanceClass:         "db.r7g.large",
	Endpoint:                "orders-writer.example",
	Engine:                  "aurora-mysql",
	EngineMode:              "provisioned",
	DBParameterGroup:        "orders-instance-pg",
	DBClusterIdentifier:     "orders-cluster",
	DBClusterParameterGroup: "orders-cluster-pg",
	IsClusterWriter:         true,
}
readerMetadata := writerMetadata
readerMetadata.Endpoint = "orders-reader.example"
readerMetadata.IsClusterWriter = false
recoveredWriterMetadata := writerMetadata
recoveredWriterMetadata.DBInstanceResourceID = "db-resource-orders-2"
recoveredWriterMetadata.Endpoint = "orders-writer-2.example"

startupMetadata := awsrds.Metadata{
	DBInstanceIdentifier: "orders-1",
	DBInstanceResourceID: "db-resource-startup",
	Engine:               "aurora-mysql",
	IsClusterWriter:      true,
}
reports := []struct {
	metadata awsrds.Metadata
	err      error
}{
	{metadata: writerMetadata},
	{metadata: readerMetadata},
	{err: errors.New("discovery unavailable")},
	{metadata: recoveredWriterMetadata},
}
gatherer := NewAWSRDSEnhancedMetricsGatherer(
	logger,
	client,
	configuration,
	startupMetadata,
	func(context.Context) (awsrds.Metadata, error) {
		report := reports[discoveryCalls]
		discoveryCalls++
		return report.metadata, report.err
	},
)
~~~

After the first two existing assertions, assert fallback to readerMetadata, then recovery to recoveredWriterMetadata:

~~~go
fallback := &models.Metrics{}
if err := gatherer.GetMetrics(fallback); err != nil {
	t.Fatalf("fallback GetMetrics() error = %v", err)
}
fallbackHost := fallback.System.Info["Host"].(models.MetricGroupValue)
if fallbackHost["IsClusterWriter"] != false {
	t.Fatalf("fallback IsClusterWriter = %#v, want cached reader role", fallbackHost["IsClusterWriter"])
}
if got := <-requestedStream; got != readerMetadata.DBInstanceResourceID {
	t.Fatalf("fallback CloudWatch stream = %q, want %q", got, readerMetadata.DBInstanceResourceID)
}

recovered := &models.Metrics{}
if err := gatherer.GetMetrics(recovered); err != nil {
	t.Fatalf("recovered GetMetrics() error = %v", err)
}
recoveredHost := recovered.System.Info["Host"].(models.MetricGroupValue)
if recoveredHost["IsClusterWriter"] != true {
	t.Fatalf("recovered IsClusterWriter = %#v, want refreshed writer role", recoveredHost["IsClusterWriter"])
}
if got := <-requestedStream; got != recoveredWriterMetadata.DBInstanceResourceID {
	t.Fatalf("recovered CloudWatch stream = %q, want %q", got, recoveredWriterMetadata.DBInstanceResourceID)
}
if discoveryCalls != 4 {
	t.Fatalf("discovery calls = %d, want one per report", discoveryCalls)
}
if configuration.MysqlHost != "startup.example" {
	t.Fatalf("MySQL host = %q, want startup endpoint unchanged", configuration.MysqlHost)
}
~~~

- [ ] **Step 2: Add collection-continuity and concurrent-cache tests**

Import sync, sync/atomic, and github.com/Releem/mysqlconfigurer/utils. Add a following gatherer and verify utils.CollectMetrics proceeds when live discovery falls back:

~~~go
type countingMetricsGatherer struct {
	calls int
}

func (g *countingMetricsGatherer) GetMetrics(*models.Metrics) error {
	g.calls++
	return nil
}

func TestAWSRDSEnhancedMetricsDiscoveryFallbackKeepsCollectionAlive(t *testing.T) {
	fixture, err := os.ReadFile("../../awsrds/testdata/aurora_mysql_writer.json")
	if err != nil {
		t.Fatalf("read enhanced-monitoring fixture: %v", err)
	}
	client, requestedStream := testCloudWatchLogsClient(t, fixture)
	logger := *logging.Init("aws-rds-fallback-collection-test", false, false, io.Discard)
	configuration := &config.Config{}
	initial := testRDSMetadata("orders-1", "db-resource-cached", "db.r7g.large", "aurora-mysql", "instance-pg", "orders", "cluster-pg", "provisioned", true)
	gatherer := NewAWSRDSEnhancedMetricsGatherer(logger, client, configuration, initial, func(context.Context) (awsrds.Metadata, error) {
		return awsrds.Metadata{}, errors.New("discovery unavailable")
	})
	following := &countingMetricsGatherer{}

	metrics := utils.CollectMetrics([]models.MetricsGatherer{gatherer, following}, logger, configuration)
	if metrics == nil {
		t.Fatal("CollectMetrics() = nil, want report built from cached metadata")
	}
	if following.calls != 1 {
		t.Fatalf("following gatherer calls = %d, want 1", following.calls)
	}
	if got := <-requestedStream; got != initial.DBInstanceResourceID {
		t.Fatalf("CloudWatch stream = %q, want cached %q", got, initial.DBInstanceResourceID)
	}
}
~~~

Add a race-focused test that drives both cache writes and fallback reads:

~~~go
func TestAWSRDSEnhancedMetricsMetadataCacheIsConcurrentSafe(t *testing.T) {
	logger := *logging.Init("aws-rds-metadata-cache-race-test", false, false, io.Discard)
	initial := awsrds.Metadata{DBInstanceResourceID: "initial"}
	refreshed := awsrds.Metadata{DBInstanceResourceID: "refreshed"}
	var calls atomic.Int64
	gatherer := NewAWSRDSEnhancedMetricsGatherer(logger, nil, &config.Config{}, initial, func(context.Context) (awsrds.Metadata, error) {
		if calls.Add(1)%2 == 0 {
			return awsrds.Metadata{}, errors.New("discovery unavailable")
		}
		return refreshed, nil
	})

	const workers = 64
	results := make(chan string, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			results <- gatherer.metadataForReport(context.Background()).DBInstanceResourceID
		}()
	}
	group.Wait()
	close(results)
	for resourceID := range results {
		if resourceID != "initial" && resourceID != "refreshed" {
			t.Fatalf("metadata resource ID = %q, want a complete cached snapshot", resourceID)
		}
	}
}
~~~

- [ ] **Step 3: Run the focused tests and confirm RED**

Run:

~~~bash
go test ./metrics/system -run 'TestAWSRDSEnhancedMetrics'
~~~

Expected: compilation fails because the constructor does not accept initialMetadata and metadataForReport does not exist. This confirms the new behavior is absent.

- [ ] **Step 4: Implement the mutex-protected metadata cache**

Add sync to metrics/system/awsrdsenhancedmetrics.go, extend the gatherer, and implement the helper:

~~~go
type AWSRDSEnhancedMetricsGatherer struct {
	logger           logging.Logger
	debug            bool
	cwlogsclient     *cloudwatchlogs.Client
	configuration    *config.Config
	discoverMetadata RDSMetadataDiscoverer
	metadataMu       sync.RWMutex
	metadata         awsrds.Metadata
}

func NewAWSRDSEnhancedMetricsGatherer(logger logging.Logger, cwlogsclient *cloudwatchlogs.Client,
	configuration *config.Config, initialMetadata awsrds.Metadata,
	discoverMetadata RDSMetadataDiscoverer) *AWSRDSEnhancedMetricsGatherer {
	return &AWSRDSEnhancedMetricsGatherer{
		logger:           logger,
		debug:            configuration.Debug,
		cwlogsclient:     cwlogsclient,
		configuration:    configuration,
		discoverMetadata: discoverMetadata,
		metadata:         initialMetadata,
	}
}

func (g *AWSRDSEnhancedMetricsGatherer) metadataForReport(ctx context.Context) awsrds.Metadata {
	metadata, err := g.discoverMetadata(ctx)
	if err != nil {
		g.logger.Errorf("Failed to refresh AWS RDS metadata, using last known metadata: %v", err)
		g.metadataMu.RLock()
		defer g.metadataMu.RUnlock()
		return g.metadata
	}

	g.metadataMu.Lock()
	g.metadata = metadata
	g.metadataMu.Unlock()
	return metadata
}
~~~

Replace the discovery/error-return block at the start of GetMetrics with:

~~~go
ctx := context.Background()
metadata := awsrdsenhancedmetrics.metadataForReport(ctx)
~~~

Do not call ApplyEndpoint from the helper or GetMetrics.

- [ ] **Step 5: Pass startup metadata at every constructor call**

In main.go, pass the existing startup metadata before the discovery closure:

~~~go
gatherers["default"] = append(gatherers["default"], system.NewAWSRDSEnhancedMetricsGatherer(
	logger,
	cwlogsclient,
	configuration,
	metadata,
	func(ctx context.Context) (awsrds.Metadata, error) {
		return awsrds.DiscoverInstance(ctx, rdsclient, configuration.AwsRDSDB)
	},
))
~~~

In awsrds/payload_contract_test.go, pass tc.metadata as both the initial snapshot and successful discovery result:

~~~go
if err := system.NewAWSRDSEnhancedMetricsGatherer(logger, client, &tc.config, tc.metadata, func(context.Context) (awsrds.Metadata, error) {
	return tc.metadata, nil
}).GetMetrics(metrics); err != nil {
	t.Fatalf("emit Host metrics: %v", err)
}
~~~

- [ ] **Step 6: Run focused tests and formatting**

~~~bash
gofmt -w metrics/system/awsrdsenhancedmetrics.go metrics/system/awsrdsenhancedmetrics_test.go awsrds/payload_contract_test.go main.go
go test ./metrics/system ./awsrds ./utils
~~~

Expected: all listed packages pass. The lifecycle test proves fallback and recovery; the collection-continuity test reaches the following gatherer.

If a C compiler is available:

~~~bash
go test -race ./metrics/system -run 'TestAWSRDSEnhancedMetrics'
~~~

Expected: PASS without a race report. If no C compiler exists, record the exact toolchain error and continue with non-race verification.

- [ ] **Step 7: Commit the metadata fallback**

~~~bash
git add metrics/system/awsrdsenhancedmetrics.go metrics/system/awsrdsenhancedmetrics_test.go awsrds/payload_contract_test.go main.go
git commit -m "fix: preserve metrics on RDS discovery failures"
~~~

---

### Task 2: Strict pre-apply parameter-group readiness

**Files:**
- Modify: tasks/task_apply_aws.go:24-42,138-150
- Modify: tasks/task_apply_aws_test.go:132-200

**Interfaces:**
- Consumes: awsrds.Metadata.InstanceStatus, awsrds.Metadata.DBParameterGroup, and awsrds.Metadata.DBParameterGroupStatus populated by awsrds.DiscoverInstance.
- Produces: private constant awsApplyExitParameterGroupNotInSync = 2.
- Preserves: ApplyConfAwsRds(models.MetricsRepeater, []models.MetricsGatherer, logging.Logger, *config.Config, AWSApplyMode) (int, int, string).

- [ ] **Step 1: Add a table-driven readiness regression test**

Add TestApplyConfAwsRdsRequiresInstanceParameterGroupInSync:

~~~go
func TestApplyConfAwsRdsRequiresInstanceParameterGroupInSync(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		wantExit  int
		wantLists bool
	}{
		{name: "in sync", status: "in-sync", wantExit: awsApplyExitSuccess, wantLists: true},
		{name: "applying", status: "applying", wantExit: awsApplyExitParameterGroupNotInSync},
		{name: "pending reboot", status: "pending-reboot", wantExit: awsApplyExitParameterGroupNotInSync},
		{name: "missing status", status: "", wantExit: awsApplyExitParameterGroupNotInSync},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := auroraApplyClient(true, "instance-custom", "cluster-custom")
			client.instanceOutput.DBInstances[0].DBParameterGroups[0].ParameterApplyStatus = aws.String(tt.status)
			client.instancePages = map[string]*rds.DescribeDBParametersOutput{"": {
				Parameters: []types.Parameter{modifiableAWSParameter("instance_value", "dynamic")},
			}}
			client.clusterPages = map[string]*rds.DescribeDBClusterParametersOutput{"": {
				Parameters: []types.Parameter{modifiableAWSParameter("cluster_value", "dynamic")},
			}}
			installAWSApplyTestDependencies(t, client, nil)

			exitCode, taskStatus, output := ApplyConfAwsRds(
				&awsApplyRepeater{recommendations: `{"instance_value":"2","cluster_value":"3"}`},
				[]models.MetricsGatherer{&awsApplyGatherer{current: map[string]interface{}{
					"instance_value": "1",
					"cluster_value":  "1",
				}}},
				testAWSApplyLogger(),
				awsApplyConfig("instance-custom", "cluster-custom"),
				AWSApplyAll,
			)

			if exitCode != tt.wantExit {
				t.Fatalf("ApplyConfAwsRds() exit = %d, want %d; output %s", exitCode, tt.wantExit, output)
			}
			if tt.wantLists {
				if taskStatus != awsApplyTaskStatusSuccess || len(client.instanceDescribeGroups) != 1 || len(client.clusterDescribeGroups) != 1 {
					t.Fatalf("ready apply status/lists = %d/%#v/%#v", taskStatus, client.instanceDescribeGroups, client.clusterDescribeGroups)
				}
				if len(client.instanceModifyCalls) != 1 || len(client.clusterModifyCalls) != 1 {
					t.Fatalf("ready modify calls = instance %d cluster %d, want 1 each", len(client.instanceModifyCalls), len(client.clusterModifyCalls))
				}
				return
			}

			if taskStatus != awsApplyTaskStatusFailure {
				t.Fatalf("blocked task status = %d, want %d", taskStatus, awsApplyTaskStatusFailure)
			}
			if len(client.instanceDescribeGroups) != 0 || len(client.clusterDescribeGroups) != 0 || len(client.instanceModifyCalls) != 0 || len(client.clusterModifyCalls) != 0 {
				t.Fatalf("blocked apply reached parameter APIs: instance lists %#v, cluster lists %#v, instance modifies %d, cluster modifies %d", client.instanceDescribeGroups, client.clusterDescribeGroups, len(client.instanceModifyCalls), len(client.clusterModifyCalls))
			}
			result := decodeAWSApplyResult(t, output)
			if len(result.Instance.Failed) != 1 || len(result.Cluster.Failed) != 0 {
				t.Fatalf("blocked result = %#v, want one instance-scope failure", result)
			}
		})
	}
}
~~~

- [ ] **Step 2: Run the regression test and confirm RED**

~~~bash
go test ./tasks -run TestApplyConfAwsRdsRequiresInstanceParameterGroupInSync
~~~

Expected: compilation first fails because awsApplyExitParameterGroupNotInSync is undefined. After defining only the constant to expose the behavior, applying, pending-reboot, and empty-status cases fail because the old path reaches parameter APIs and returns success.

- [ ] **Step 3: Restore exit code 2 and add the guard**

Add the constant without renumbering existing codes:

~~~go
const (
	awsApplyExitSuccess                 = 0
	awsApplyExitInstanceUnavailable     = 1
	awsApplyExitParameterGroupNotInSync = 2
	awsApplyExitTimeout                 = 6
	awsApplyExitFailure                 = 8
	awsApplyExitAccessDenied            = 9
)
~~~

Immediately after the InstanceStatus check and before validateAWSGroups, add:

~~~go
if metadata.DBParameterGroupStatus != "in-sync" {
	err = fmt.Errorf(
		"DB instance %q parameter group %q status %q is not in-sync",
		metadata.DBInstanceIdentifier,
		metadata.DBParameterGroup,
		metadata.DBParameterGroupStatus,
	)
	logger.Error(err)
	recordAWSApplyFailure(&result, awsrds.ScopeInstance, nil, err)
	return fail(awsApplyExitParameterGroupNotInSync)
}
~~~

Do not allow pending-reboot before apply. Do not change awsApplyScopeReady, where pending-reboot remains valid after a submitted modification.

- [ ] **Step 4: Run focused apply tests and formatting**

~~~bash
gofmt -w tasks/task_apply_aws.go tasks/task_apply_aws_test.go
go test ./tasks ./awsrds
~~~

Expected: PASS. The table proves all non-in-sync states stop before both parameter scopes, while existing waiter tests still accept pending-reboot.

- [ ] **Step 5: Commit the readiness guard**

~~~bash
git add tasks/task_apply_aws.go tasks/task_apply_aws_test.go
git commit -m "fix: wait for RDS parameter group readiness"
~~~

---

### Task 3: Combined verification and review preparation

**Files:**
- Verify only; no production or test files change in this task.

**Interfaces:**
- Consumes: the Task 1 constructor/cache behavior and Task 2 exit-code/readiness behavior.
- Produces: verification evidence for the complete Agent branch.

- [ ] **Step 1: Run focused regression packages together**

~~~bash
go test ./metrics/system ./utils ./tasks ./awsrds
~~~

Expected: PASS for every package.

- [ ] **Step 2: Run repository-wide tests**

~~~bash
go test ./...
~~~

Expected: PASS. If an unrelated baseline failure appears, capture the exact package, test, and error instead of claiming a clean run.

- [ ] **Step 3: Run static analysis**

~~~bash
go vet ./...
~~~

Expected: exit code 0 with no diagnostics.

- [ ] **Step 4: Run race verification when supported**

~~~bash
command -v cc || command -v gcc || command -v clang
~~~

When a compiler is present:

~~~bash
go test -race ./metrics/system ./tasks
~~~

Expected: PASS with no race report. When no compiler is present, record that go test -race is unavailable rather than treating it as passed.

- [ ] **Step 5: Check the final diff and worktree scope**

~~~bash
git diff --check HEAD~2..HEAD
git status --short
git log -3 --oneline --decorate
~~~

Expected: no whitespace errors; only the known tests/__pycache__/ remains untracked; the two implementation commits follow the design and plan commits. Re-review the combined production diff to confirm there is no runtime endpoint mutation, no Platform change, and no relaxation of the strict in-sync guard.
