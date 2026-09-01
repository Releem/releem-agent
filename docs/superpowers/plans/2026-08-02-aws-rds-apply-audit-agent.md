# AWS RDS Apply Audit Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Releem_Agent produce a complete, versioned AWS RDS parameter-apply audit with correlated structured logs and best-effort post-wait AWS readback.

**Architecture:** Keep recommendation classification pure in `awsrds`, where the planner creates one deterministic audit record per recommendation alongside the unchanged instance/cluster result. Keep AWS mutation, batch outcome updates, waiter integration, readback, and logging in focused `tasks` helpers. Reuse a safe topology/event representation for startup and enhanced-metrics discovery logs without ever including endpoint addresses or credentials.

**Tech Stack:** Go, AWS SDK for Go v2 RDS types and APIs, `encoding/json`, `github.com/google/logger`, existing package fakes and Go tests.

## Global Constraints

- Preserve the existing top-level `instance` and `cluster` result objects and task exit-code meanings.
- Add `audit.schema_version` with exact value `1`.
- Represent current, recommended, submitted, and observed parameter values as strings.
- Use only `instance` and `cluster` scopes.
- Use only `applied`, `skipped`, `failed`, and `not-attempted` outcomes.
- Use only `matched`, `mismatched`, `pending-reboot`, `unavailable`, and `not-applicable` verification statuses.
- A readback error or mismatch must not change an otherwise successful task status or exit code.
- Do not include the database endpoint address, API key, database username, database password, or raw AWS error text in audit or structured logs.
- Keep unsupported, non-modifiable, engine-managed, and Serverless-managed exclusions unchanged.
- Do not add dependencies, configuration fields, environment variables, or live AWS mutations.

---

### Task 1: Add the versioned audit contract to the pure AWS planner

**Files:**
- Create: `awsrds/audit.go`
- Create: `awsrds/audit_test.go`
- Modify: `awsrds/parameters.go:81-160,243-338,492-535`
- Modify: `awsrds/parameters_test.go`

**Interfaces:**
- Consumes: existing `Metadata`, `BuildApplyPlanInput`, `ParameterInfo`, `Scope`, `SkipReason`, and `types.Parameter`.
- Produces: `ApplyAudit`, `AuditTopology`, `ParameterAudit`, `NewApplyAudit(Metadata)`, `PopulateAuditTopology(*ApplyAudit, Metadata)`, `AuditValueString(interface{}) string`, and an `Audit ApplyAudit` field on `ApplyResult`.

- [ ] **Step 1: Write failing audit-contract and planner tests**

Add tests proving exact JSON names, deterministic ordering, value-source selection, raw-versus-normalized values, skip outcomes, and secret-free topology. Use concrete cases for PostgreSQL unit conversion and Serverless-managed exclusion:

```go
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
		ConfiguredClusterGroup: "cluster-pg",
		InstanceParameters: map[string]ParameterInfo{
			"work_mem": {Name: "work_mem", ApplyType: "dynamic", IsModifiable: true},
		},
		ClusterParameters: map[string]ParameterInfo{
			"max_connections": {Name: "max_connections", ApplyType: "static", IsModifiable: true, ParameterValue: "100", HasParameterValue: true},
		},
		Recommendations: map[string]interface{}{
			"work_mem": json.Number("8388608"),
			"max_connections": json.Number("200"),
			"unknown_parameter": "1",
		},
		CurrentValues: map[string]interface{}{"work_mem": "4096"},
	}

	plan, result := BuildApplyPlan(input)

	if result.Audit.SchemaVersion != 1 { t.Fatalf("schema = %d", result.Audit.SchemaVersion) }
	if got := aws.ToString(plan.Instance.Parameters[0].ParameterValue); got != "8192" { t.Fatalf("submitted = %q", got) }
	assertParameterAudit(t, result.Audit.Parameters, ParameterAudit{
		Scope: ScopeInstance, Name: "work_mem", Group: "instance-pg",
		CurrentValue: aws.String("4096"), CurrentSource: CurrentSourceDBMetrics,
		RecommendedValue: "8388608", SubmittedValue: aws.String("8192"),
		ApplyMethod: string(types.ApplyMethodImmediate), Outcome: OutcomeNotAttempted,
		Reason: ReasonNotSubmitted, VerificationStatus: VerificationNotApplicable,
	})
	encoded, _ := json.Marshal(result)
	if bytes.Contains(encoded, []byte("must-not-appear.example")) { t.Fatalf("endpoint leaked: %s", encoded) }
}

func assertParameterAudit(t *testing.T, records []ParameterAudit, want ParameterAudit) {
	t.Helper()
	for _, record := range records {
		if record.Scope == want.Scope && record.Name == want.Name {
			if !reflect.DeepEqual(record, want) { t.Fatalf("audit = %#v, want %#v", record, want) }
			return
		}
	}
	t.Fatalf("audit record %s/%s not found", want.Scope, want.Name)
}
```

Add a table test with these exact source expectations:

```go
tests := []struct {
	name string
	parameter ParameterInfo
	current map[string]interface{}
	wantValue *string
	wantSource CurrentValueSource
}{
	{"AWS group wins", ParameterInfo{ParameterValue: "100", HasParameterValue: true}, map[string]interface{}{"p": "90"}, aws.String("100"), CurrentSourceAWSParameterGroup},
	{"DB metrics fallback", ParameterInfo{}, map[string]interface{}{"p": "90"}, aws.String("90"), CurrentSourceDBMetrics},
	{"missing", ParameterInfo{}, map[string]interface{}{}, nil, CurrentSourceMissing},
}
```

- [ ] **Step 2: Run the focused test and verify the red state**

Run: `/usr/local/go/bin/go test ./awsrds -run 'TestBuildApplyPlanBuildsVersionedParameterAudit|TestParameterAuditCurrentSource|TestApplyAuditJSONContract' -count=1`

Expected: FAIL because `ApplyAudit`, `ParameterAudit`, and `ApplyResult.Audit` do not exist.

- [ ] **Step 3: Define exact audit types and safe conversion helpers**

Create `awsrds/audit.go` with these public contracts and constants:

```go
const ApplyAuditSchemaVersion = 1

type CurrentValueSource string
const (
	CurrentSourceAWSParameterGroup CurrentValueSource = "aws-parameter-group"
	CurrentSourceDBMetrics CurrentValueSource = "db-metrics"
	CurrentSourceMissing CurrentValueSource = "missing"
)

type ApplyOutcome string
const (
	OutcomeApplied ApplyOutcome = "applied"
	OutcomeSkipped ApplyOutcome = "skipped"
	OutcomeFailed ApplyOutcome = "failed"
	OutcomeNotAttempted ApplyOutcome = "not-attempted"
)

type VerificationStatus string
const (
	VerificationMatched VerificationStatus = "matched"
	VerificationMismatched VerificationStatus = "mismatched"
	VerificationPendingReboot VerificationStatus = "pending-reboot"
	VerificationUnavailable VerificationStatus = "unavailable"
	VerificationNotApplicable VerificationStatus = "not-applicable"
)

const (
	ReasonNotSubmitted = "not-submitted"
	ReasonPriorBatchFailure = "prior-batch-failure"
	ReasonPriorScopeFailure = "prior-scope-failure"
	ReasonReadbackFailed = "readback-failed"
	ReasonReadbackParameterMissing = "readback-parameter-missing"
)

type AuditTopology struct {
	DBInstanceIdentifier string `json:"db_instance_identifier,omitempty"`
	DBInstanceClass string `json:"db_instance_class,omitempty"`
	Engine string `json:"engine,omitempty"`
	EngineMode string `json:"engine_mode,omitempty"`
	DBClusterIdentifier string `json:"db_cluster_identifier,omitempty"`
	IsClusterWriter bool `json:"is_cluster_writer"`
	IsServerlessV2 bool `json:"is_serverless_v2"`
	DBParameterGroup string `json:"db_parameter_group,omitempty"`
	DBClusterParameterGroup string `json:"db_cluster_parameter_group,omitempty"`
	InstanceStatus string `json:"instance_status,omitempty"`
	DBParameterGroupStatus string `json:"db_parameter_group_status,omitempty"`
}

type ParameterAudit struct {
	Scope Scope `json:"scope"`
	Name string `json:"name"`
	Group string `json:"group,omitempty"`
	CurrentValue *string `json:"current_value,omitempty"`
	CurrentSource CurrentValueSource `json:"current_source"`
	RecommendedValue string `json:"recommended_value"`
	SubmittedValue *string `json:"submitted_value,omitempty"`
	ApplyMethod string `json:"apply_method,omitempty"`
	Batch *int `json:"batch,omitempty"`
	Outcome ApplyOutcome `json:"outcome"`
	Reason string `json:"reason,omitempty"`
	Error string `json:"error,omitempty"`
	ObservedAfter *string `json:"observed_after,omitempty"`
	VerificationStatus VerificationStatus `json:"verification_status"`
}

type ApplyAudit struct {
	SchemaVersion int `json:"schema_version"`
	Topology AuditTopology `json:"topology"`
	Parameters []ParameterAudit `json:"parameters"`
}
```

`NewApplyAudit` must allocate a non-nil empty parameter slice. `PopulateAuditTopology` must copy only the listed safe metadata fields. `AuditValueString` must return strings unchanged, `json.Number.String()` unchanged, and canonical `json.Marshal` output for booleans, null, arrays, objects, and other decoded JSON values.

- [ ] **Step 4: Populate audit records inside `BuildApplyPlan` without changing its signature**

Add `Audit ApplyAudit `json:"audit"`` to `ApplyResult`. Initialize it in `BuildApplyPlan` and `newAWSApplyResult`. For every sorted recommendation name, append exactly one audit record to the same selected scope as the planner:

```go
record := newParameterAudit(input, name, parameter, scope, plan.Group)
if skipped {
	record.Outcome = OutcomeSkipped
	record.Reason = string(skip.Reason)
	record.VerificationStatus = VerificationNotApplicable
} else {
	record.SubmittedValue = aws.String(value)
	record.ApplyMethod = string(applyMethod)
	record.Outcome = OutcomeNotAttempted
	record.Reason = ReasonNotSubmitted
	record.VerificationStatus = VerificationNotApplicable
}
result.Audit.Parameters = append(result.Audit.Parameters, record)
```

Extend `ApplyResult.Sort()` to order audit records by `scope`, then `name`, and normalize a nil audit parameter slice to `[]`. Do not derive audit records later from `Applied` or `Skipped`; doing so would lose raw recommendation and current-source information.

- [ ] **Step 5: Run planner tests and the complete package**

Run: `/usr/local/go/bin/go test ./awsrds -count=1`

Expected: PASS, including all existing classification, conversion, Serverless v2, and legacy result tests.

- [ ] **Step 6: Commit the planner contract**

```bash
git add awsrds/audit.go awsrds/audit_test.go awsrds/parameters.go awsrds/parameters_test.go
git commit -m "feat: add AWS parameter apply audit contract"
```

---

### Task 2: Track batch outcomes and emit plan/batch structured events

**Files:**
- Create: `tasks/task_apply_aws_audit.go`
- Create: `tasks/task_apply_aws_audit_test.go`
- Modify: `tasks/task_apply_aws.go:360-430`
- Modify: `tasks/task_apply_aws_test.go:224-745`

**Interfaces:**
- Consumes: `awsrds.ApplyAudit`, `awsrds.ApplyPlan`, `awsrds.ScopePlan`, existing safe error categories, and `logging.Logger`.
- Produces: `awsApplyTaskContext`, `logAWSApplyEvent`, `markAWSAuditBatch`, `markAWSAuditRemaining`, and audit-aware `applyAWSPlan`/`applyAWSScopeBatches` signatures.

- [ ] **Step 1: Write failing batch-lifecycle tests**

Add tests with 21 instance parameters and one cluster parameter. Configure the fake client so the second instance batch fails. Assert:

```go
if got := auditRecord(t, audit, awsrds.ScopeInstance, "p01").Outcome; got != awsrds.OutcomeApplied { t.Fatalf("p01 = %q", got) }
if got := auditRecord(t, audit, awsrds.ScopeInstance, "p21").Outcome; got != awsrds.OutcomeFailed { t.Fatalf("p21 = %q", got) }
if got := auditRecord(t, audit, awsrds.ScopeCluster, "cluster_p").Reason; got != awsrds.ReasonPriorScopeFailure { t.Fatalf("cluster reason = %q", got) }
if batch := auditRecord(t, audit, awsrds.ScopeInstance, "p21").Batch; batch == nil || *batch != 2 { t.Fatalf("batch = %#v", batch) }

func auditRecord(t *testing.T, audit *awsrds.ApplyAudit, scope awsrds.Scope, name string) *awsrds.ParameterAudit {
	t.Helper()
	for index := range audit.Parameters {
		if audit.Parameters[index].Scope == scope && audit.Parameters[index].Name == name { return &audit.Parameters[index] }
	}
	t.Fatalf("audit record %s/%s not found", scope, name)
	return nil
}
```

Capture a `bytes.Buffer` logger and assert JSON events named `aws_rds_apply_plan` and `aws_rds_apply_batch` contain task IDs, scope/group/batch, names, and outcomes but do not contain submitted values.

- [ ] **Step 2: Run the focused task test and verify the red state**

Run: `/usr/local/go/bin/go test ./tasks -run 'TestApplyAWSPlanUpdatesAuditAcrossPartialFailure|TestAWSApplyPlanAndBatchLogsOmitValues' -count=1`

Expected: FAIL because execution does not update audit records or emit structured events.

- [ ] **Step 3: Add the task correlation and structured-event helper**

In `tasks/task_apply_aws_audit.go`, define:

```go
type awsApplyTaskContext struct {
	TaskID int `json:"task_id,omitempty"`
	TaskTypeID int `json:"task_type_id,omitempty"`
}

func logAWSApplyEvent(logger logging.Logger, event string, fields map[string]interface{}) {
	payload := map[string]interface{}{"event": event}
	for name, value := range fields { payload[name] = value }
	encoded, err := json.Marshal(payload)
	if err != nil {
		logger.Warningf("AWS apply log event %q could not be serialized: %v", event, err)
		return
	}
	logger.Info(string(encoded))
}
```

Callers must build explicit field maps; never pass configuration, complete AWS SDK responses, or raw errors.

- [ ] **Step 4: Make batch application update every planned audit record**

Change the internal signatures to:

```go
func applyAWSPlan(ctx context.Context, client awsrds.Client, plan awsrds.ApplyPlan, result *awsrds.ApplyResult, logger logging.Logger, task awsApplyTaskContext) (awsApplyWaitRequest, error)
func applyAWSScopeBatches(ctx context.Context, client awsrds.Client, scope awsrds.Scope, plan awsrds.ScopePlan, result *awsrds.ScopeResult, audit *awsrds.ApplyAudit, logger logging.Logger, task awsApplyTaskContext) ([]types.Parameter, error)
```

Before mutation, emit one plan event containing both groups, per-scope names, and counts. For each one-based batch:

- set `Batch` on all records in the submitted batch;
- on success set `OutcomeApplied`, clear `Reason` and `Error`;
- on failure set the current batch to `OutcomeFailed`, keep only `awsApplySafeErrorCode(err)` in `Error`, and mark later records in the same scope `OutcomeNotAttempted` with `prior-batch-failure`;
- when instance scope stops the plan, mark all planned cluster records `not-attempted` with `prior-scope-failure`;
- emit one batch event after each AWS call without values.

- [ ] **Step 5: Run focused and existing partial-failure tests**

Run: `/usr/local/go/bin/go test ./tasks -run 'TestApplyAWSPlan|TestApplyConfAwsRdsAWSFailurePreservesEarlierBatchesAndNamesFailure|TestApplyConfAwsRdsRefreshesDiscoveryRoutesScopesAndBatchesTwentyPlusOne' -count=1`

Expected: PASS; existing `ScopeResult.Applied` and `Failed` assertions remain unchanged.

- [ ] **Step 6: Commit execution outcome tracking**

```bash
git add tasks/task_apply_aws.go tasks/task_apply_aws_test.go tasks/task_apply_aws_audit.go tasks/task_apply_aws_audit_test.go
git commit -m "feat: audit AWS parameter apply batches"
```

---

### Task 3: Add best-effort post-wait AWS readback

**Files:**
- Modify: `tasks/task_apply_aws_audit.go`
- Modify: `tasks/task_apply_aws_audit_test.go`
- Modify: `tasks/task_apply_aws.go:270-295,595-680`
- Modify: `tasks/task_apply_aws_test.go:921-1185`

**Interfaces:**
- Consumes: the successfully submitted `awsApplyWaitRequest`, `awsrds.ParameterReader`, and audit records created in Tasks 1-2.
- Produces: `awsApplyReadbackFunc`, `defaultReadAWSAppliedParameters`, and `verifyAWSAppliedParameters` with no error return so verification cannot change task success.

- [ ] **Step 1: Write failing verification-status tests**

Use an injected readback function to cover exact cases:

```go
tests := []struct {
	name string
	applyMethod types.ApplyMethod
	readback map[string]awsrds.ParameterInfo
	readbackErr error
	wantObserved *string
	wantStatus awsrds.VerificationStatus
	wantReason string
}{
	{"immediate match", types.ApplyMethodImmediate, parameterMap("p", "200"), nil, aws.String("200"), awsrds.VerificationMatched, ""},
	{"mismatch", types.ApplyMethodImmediate, parameterMap("p", "199"), nil, aws.String("199"), awsrds.VerificationMismatched, ""},
	{"static match", types.ApplyMethodPendingReboot, parameterMap("p", "200"), nil, aws.String("200"), awsrds.VerificationPendingReboot, ""},
	{"API failure", types.ApplyMethodImmediate, nil, errors.New("unavailable"), nil, awsrds.VerificationUnavailable, awsrds.ReasonReadbackFailed},
	{"missing parameter", types.ApplyMethodImmediate, map[string]awsrds.ParameterInfo{}, nil, nil, awsrds.VerificationUnavailable, awsrds.ReasonReadbackParameterMissing},
}

func parameterMap(name, value string) map[string]awsrds.ParameterInfo {
	return map[string]awsrds.ParameterInfo{name: {Name: name, ParameterValue: value, HasParameterValue: true}}
}
```

Add an integration-style `ApplyConfAwsRds` test where modify and waiter succeed but readback returns an error; assert exit `0`, status `1`, audit verification `unavailable`, and a warning log.

- [ ] **Step 2: Run verification tests and confirm they fail**

Run: `/usr/local/go/bin/go test ./tasks -run 'TestVerifyAWSAppliedParameters|TestApplyConfAwsRdsReadbackFailureDoesNotFailTask' -count=1`

Expected: FAIL because no readback phase exists.

- [ ] **Step 3: Implement the injectable readback boundary**

Add:

```go
type awsApplyReadbackFunc func(context.Context, awsrds.ParameterReader, awsrds.Scope, awsrds.ScopePlan) (map[string]awsrds.ParameterInfo, error)

var readAWSAppliedParameters awsApplyReadbackFunc = defaultReadAWSAppliedParameters

func defaultReadAWSAppliedParameters(ctx context.Context, client awsrds.ParameterReader, scope awsrds.Scope, plan awsrds.ScopePlan) (map[string]awsrds.ParameterInfo, error) {
	return awsrds.ListParameters(ctx, client, plan.Group, scope)
}
```

Use this explicit-scope signature in the implementation and tests; never infer scope from group text.

- [ ] **Step 4: Verify successful submissions after the waiter returns**

Call `verifyAWSAppliedParameters` after `waitForAWSApply` for every scope with successfully submitted parameters, even when another batch or the waiter failed. The function must:

- list at most once per modified scope;
- filter to submitted parameter names;
- compare the normalized submitted string to AWS `ParameterValue`;
- assign `pending-reboot` only when a matching submitted parameter used `pending-reboot`;
- assign `matched` only for matching immediate parameters;
- warn for mismatches, missing parameters, and readback errors without returning an error;
- leave skipped, failed, and not-attempted records as `not-applicable`.

Restore the injected function with `t.Cleanup` in every test so the global does not leak across cases.

- [ ] **Step 5: Run waiter, readback, and full tasks tests**

Run: `/usr/local/go/bin/go test ./tasks -run 'TestVerifyAWSAppliedParameters|TestApplyConfAwsRdsReadback|TestDefaultWaitForAWSApply|TestApplyConfAwsRdsRepeatedPendingRebootUsesCanonicalAWSValuesForBothScopes' -count=1`

Expected: PASS. Then run: `/usr/local/go/bin/go test ./tasks -count=1`

Expected: PASS.

- [ ] **Step 6: Commit readback verification**

```bash
git add tasks/task_apply_aws.go tasks/task_apply_aws_test.go tasks/task_apply_aws_audit.go tasks/task_apply_aws_audit_test.go
git commit -m "feat: verify AWS parameter apply results"
```

---

### Task 4: Correlate final audit and discovery logs, then verify the Agent

**Files:**
- Modify: `tasks/task_apply_aws.go:98-295`
- Modify: `tasks/tasks.go:25,80-113`
- Modify: `tasks/tasks_test.go:23-70`
- Modify: `tasks/task_apply_aws_audit.go`
- Modify: `tasks/task_apply_aws_audit_test.go`
- Modify: `metrics/system/awsrdsenhancedmetrics.go:196-228`
- Modify: `metrics/system/awsrdsenhancedmetrics_test.go:157-385`
- Modify: `main.go:120-137`

**Interfaces:**
- Consumes: the completed `ApplyResult.Audit`, `models.Task.ID/TypeID`, and safe `AuditTopology`.
- Produces: an internal context-aware AWS apply entry point while retaining the public `ApplyConfAwsRds` signature, plus `aws_rds_discovery`, `aws_rds_apply_wait`, and `aws_rds_apply_audit` events.

- [ ] **Step 1: Write failing task-correlation and secret-exclusion tests**

Keep existing direct `ApplyConfAwsRds` tests source-compatible. Add a `ProcessTask` test whose `runAWSRDSApply` fake receives this exact context:

```go
want := awsApplyTaskContext{TaskID: 42, TaskTypeID: 4}
runAWSRDSApply = func(_ models.MetricsRepeater, _ []models.MetricsGatherer, _ logging.Logger, _ *config.Config, _ AWSApplyMode, got awsApplyTaskContext) (int, int, string) {
	if got != want { t.Fatalf("task context = %#v, want %#v", got, want) }
	return 0, 1, `{"instance":{"applied":[],"skipped":[],"failed":[]},"cluster":{"applied":[],"skipped":[],"failed":[]},"audit":{"schema_version":1,"topology":{},"parameters":[]}}`
}
```

Capture Agent logs and assert one final JSON event includes `event=aws_rds_apply_audit`, task ID/type, status, exit code, and audit. Extend the existing secret test to assert the audit event and task output omit `Endpoint`, `ApiKey`, `MysqlUser`, `MysqlPassword`, and raw AWS error strings.

Add enhanced-metrics tests asserting successful refresh logs `source=live` at V(5) and failure logs `source=cache` with safe topology but no endpoint.

- [ ] **Step 2: Run focused tests and verify the red state**

Run: `/usr/local/go/bin/go test ./tasks ./metrics/system -run 'TestProcessTask.*AWSApply.*Context|TestAWSApplyAuditLog|TestAWSRDSEnhancedMetrics.*DiscoveryLog' -count=1`

Expected: FAIL because task correlation and named discovery/audit events are absent.

- [ ] **Step 3: Preserve the public apply API while adding internal correlation**

Use a wrapper so external callers and existing direct tests retain the current signature:

```go
func ApplyConfAwsRds(repeaters models.MetricsRepeater, gatherers []models.MetricsGatherer, logger logging.Logger, configuration *config.Config, mode AWSApplyMode) (int, int, string) {
	return applyConfAWSRDS(repeaters, gatherers, logger, configuration, mode, awsApplyTaskContext{})
}

var runAWSRDSApply = applyConfAWSRDS
```

`applyConfAWSRDS` receives the additional `awsApplyTaskContext`. Change only the type-4 and type-5 AWS calls in `ProcessTask` to pass `TaskStruct.ID` and `TaskStruct.TypeID`.

- [ ] **Step 4: Emit wait and exactly one terminal audit event from every return path**

Replace the current failure closure with one terminal helper:

```go
finish := func(exitCode, status int) (int, int, string) {
	output := marshalAWSApplyResult(&result)
	logAWSApplyEvent(logger, "aws_rds_apply_audit", map[string]interface{}{
		"task_id": task.TaskID, "task_type_id": task.TaskTypeID,
		"task_status": status, "task_exit_code": exitCode,
		"audit": result.Audit,
	})
	return exitCode, status, output
}
```

Every early failure and the success return must call `finish`. Emit `aws_rds_apply_wait` after the waiter with modified scopes and safe final outcome; do not log raw AWS responses or submitted values outside the terminal audit.

- [ ] **Step 5: Add safe startup/live/cache discovery events**

Build discovery fields only from `AuditTopology`. In `main.go`, emit `aws_rds_discovery` with `source=startup` after successful discovery. In `metadataForReport`, emit at V(5) with `source=live` on refresh. On fallback, replace the current raw-error message with one structured warning containing `source=cache`, `reason=discovery-failed`, `error_type=fmt.Sprintf("%T", err)`, and safe topology. Do not serialize `err.Error()` into the structured event.

- [ ] **Step 6: Run focused tests and the full Agent verification**

Run:

```bash
/usr/local/go/bin/go test ./awsrds ./tasks ./metrics/system -count=1
/usr/local/go/bin/go test -count=1 ./...
/usr/local/go/bin/go vet ./...
git diff --check 6f5c410..HEAD
```

Expected: every command exits `0`. If `cc`, `gcc`, or `clang` exists, also run `/usr/local/go/bin/go test -race ./awsrds ./tasks ./metrics/system`; otherwise record the toolchain limitation in the handoff. Do not claim live AWS verification.

- [ ] **Step 7: Commit final Agent observability integration**

```bash
git add main.go metrics/system/awsrdsenhancedmetrics.go metrics/system/awsrdsenhancedmetrics_test.go tasks/task_apply_aws.go tasks/task_apply_aws_audit.go tasks/task_apply_aws_audit_test.go tasks/task_apply_aws_test.go tasks/tasks.go tasks/tasks_test.go
git commit -m "feat: log AWS parameter apply audit"
```

After committing, rerun `git status --short`; the pre-existing untracked `tests/__pycache__/` must remain untouched.
