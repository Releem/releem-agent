# AWS RDS Parameter Apply Logging and Audit Design

## Context

The Aurora parameter-group work routes recommendations between instance and
cluster parameter groups and returns a structured apply result in the Agent
task output. The current observability is not sufficient for a live acceptance
test: operators can see the task exit code and selected errors, but cannot
reliably reconstruct the value proposed by Platform, the value submitted to
AWS, the post-apply value observed in AWS, or why an individual parameter was
excluded.

This is a cross-repository change for Releem_Agent and Releem_Platform. The
Agent remains the source of truth for AWS discovery, planning, application, and
verification. Platform receives the existing task-status payload and makes the
Agent-produced audit visible in Platform logs.

## Goals

- Make ordinary RDS and Aurora parameter application diagnosable from Agent and
  Platform logs.
- Record the full recommendation lifecycle for every planned parameter:
  current, recommended, normalized/submitted, and observed-after values.
- Record instance-versus-cluster ownership, parameter-group identity, apply
  method, batch, outcome, and stable reason or error.
- Verify successful submissions with a best-effort AWS readback after the
  existing waiter.
- Preserve existing task output fields and accept task statuses from older
  Agents that do not send the new audit object.
- Avoid adding a database migration or a new persistent audit store.

## Non-goals

- Persisting task output or audit data in the Platform database.
- Adding a new API, UI, or audit-history screen.
- Changing recommendation generation, instance/cluster classification,
  Serverless-managed exclusions, batching limits, or parameter conversion.
- Making readback failure change the result of an otherwise successful apply.
- Proving active runtime values for static parameters before a reboot.
- Adding live AWS mutation tests to the regular unit-test suite.

## Selected Approach

Extend the existing JSON returned by the Agent AWS apply task with a
versioned `audit` object. Keep the existing top-level `instance` and `cluster`
result objects unchanged for backward compatibility. The complete JSON remains
in `Task.Output`, is sent through the existing task-status request, and is
logged as a structured audit event by both Agent and Platform.

Audit data is intentionally transient. It is retained only according to the
retention of Agent and Platform logs; Platform does not store it in the
`tasks` table or any new table.

## Task Output Contract

The existing result remains present:

```json
{
  "instance": {
    "group": "db-instance-parameter-group",
    "applied": ["max_connections"],
    "skipped": [],
    "failed": [],
    "diagnostics": []
  },
  "cluster": {
    "group": "db-cluster-parameter-group",
    "applied": ["slow_query_log"],
    "skipped": [],
    "failed": [],
    "diagnostics": []
  },
  "audit": {
    "schema_version": 1,
    "topology": {},
    "parameters": []
  }
}
```

### Topology

`audit.topology` records the context needed to interpret the apply:

- DB instance identifier and class;
- engine and engine mode;
- cluster identifier when present;
- writer/reader role;
- Serverless v2 detection;
- instance and cluster parameter-group names;
- observed instance and parameter-group readiness statuses.

The database endpoint address, API key, database username, and database
password are never included.

### Parameter Records

`audit.parameters` contains one record per recommendation considered by the AWS
planner. Values are represented as strings so JSON numeric decoding and AWS
unit conversion cannot lose precision.

Each record contains:

- `scope`: `instance` or `cluster`;
- `name` and the target `group`;
- `current_value` and `current_source`;
- raw `recommended_value` received from Platform;
- normalized AWS-native `submitted_value`, when submission was attempted;
- `apply_method` and one-based `batch`, when applicable;
- `outcome`;
- stable `reason` for exclusions or `error` for failures;
- `observed_after` and `verification_status`.

`current_source` uses `aws-parameter-group`, `db-metrics`, or `missing`.
`outcome` uses `applied`, `skipped`, `failed`, or `not-attempted`.
`not-attempted` identifies parameters left after a prior batch or scope failure;
it is not reported as an AWS rejection.

`verification_status` uses:

- `matched` when an immediate value is read back as submitted;
- `mismatched` when AWS readback succeeds but differs;
- `pending-reboot` when the parameter-group value matches but activation needs
  a reboot;
- `unavailable` when readback fails;
- `not-applicable` for parameters that were not successfully submitted.

Empty fields are omitted where their meaning is not applicable. Stable reason
codes remain machine-searchable; diagnostic text remains available separately.

## Apply and Verification Flow

1. Discover topology and readiness using the existing AWS path.
2. Build instance and cluster plans, including exclusions and unit conversion.
3. Initialize audit records before modification so every considered
   recommendation receives a terminal outcome.
4. Submit existing AWS batches and update each record with the target group,
   apply method, batch, submitted value, and outcome.
5. Preserve partial progress: earlier successful batches stay `applied`, a
   rejected batch is `failed`, and later unsubmitted records become
   `not-attempted` with a prior-failure reason.
6. Run the existing waiter.
7. Best-effort, list the modified instance and/or cluster parameter groups and
   filter the response to successfully submitted parameter names.
8. Populate `observed_after` and `verification_status`.
9. Serialize the backward-compatible result plus audit into `Task.Output`.

Readback is limited to scopes containing successful submissions. A readback
API error produces `unavailable` verification and a warning, but does not
change a successful task status or exit code. A mismatch is also observable as
a warning and audit result without retroactively claiming that AWS rejected the
accepted modification.

For static parameters, `observed_after` is the value stored in the AWS
parameter group, not an assertion about the running database value. A matching
static submission is therefore `pending-reboot`, not `matched`.

## Agent Logging

Agent emits structured, consistently named events:

- `aws_rds_discovery`: topology at startup; successful per-report refreshes at
  debug level and cached-metadata fallback at warning level;
- `aws_rds_apply_plan`: task, target groups, parameter names, and counts before
  mutation;
- `aws_rds_apply_batch`: scope, group, batch, parameter names, and submission
  outcome;
- `aws_rds_apply_wait`: final waiter status for each modified scope;
- `aws_rds_apply_audit`: one complete task audit at completion.

Plan, batch, and waiter INFO events omit parameter values. The final audit event
contains the approved full value lifecycle and is correlated by task ID and
task type. Existing human-readable errors remain available; the structured
events add stable fields for automated test assertions and log queries.

## Platform Logging

Both active task-status entry paths use one shared audit-log helper for AWS task
types 4 and 5:

1. Log the generic task summary with task ID, type, status, and exit code.
2. Parse `task_output` without making successful parsing a prerequisite for the
   existing status update.
3. If `audit.schema_version == 1`, emit one canonical
   `aws_rds_apply_audit` structured event containing the Agent audit.
4. If audit is absent, accept the legacy Agent payload and log only the task
   summary.
5. If task output or audit is malformed, emit a warning and continue the normal
   status update.

The existing full generic task dump is suppressed for AWS task types 4 and 5
to avoid duplicate full-value events inside Platform. Logging behavior for
other task types is unchanged. Platform neither enriches nor rewrites the
Agent audit and performs no new database write.

## Error and Compatibility Semantics

- The current `instance.applied/skipped/failed/diagnostics` and
  `cluster.applied/skipped/failed/diagnostics` shapes do not change.
- Older Platform deployments ignore the additional JSON field.
- New Platform deployments accept older Agents with no audit field.
- Audit serialization should not hide the existing apply result. If audit
  construction encounters an internal error, the task retains its real apply
  status and reports an actionable diagnostic.
- AWS apply errors keep their existing task statuses and exit codes.
- Verification failure or mismatch never converts an accepted apply into a
  failed task; it is exposed through verification status and warning logs.
- Unsupported, non-modifiable, engine-managed, and Serverless-managed
  parameters retain their planner exclusion reasons and are recorded as
  `skipped`.

## Test Strategy

Implementation follows test-driven development.

### Releem_Agent

- Assert that the legacy instance and cluster result fields remain unchanged
  when audit is added.
- Cover ordinary RDS MySQL, Aurora MySQL, RDS PostgreSQL, and Aurora PostgreSQL.
- Cover writer/reader routing and provisioned/Serverless v2 topology.
- Cover instance and cluster plans, exclusions, current-value sources, raw and
  normalized values, apply methods, batching, partial failures, and
  `not-attempted` records.
- Cover `matched`, `mismatched`, `pending-reboot`, `unavailable`, and
  `not-applicable` verification.
- Prove readback failure leaves the apply task status and exit code unchanged.
- Assert that structured events contain correlation fields and that endpoint
  addresses and credentials are absent.

### Releem_Platform

- Cover both task-status entry paths through the shared helper.
- Verify a valid version-1 audit produces exactly one canonical audit event and
  the existing task status update.
- Verify a legacy payload without audit is accepted.
- Verify malformed task output or audit produces a warning but still updates
  task status.
- Verify AWS task summaries do not duplicate the full audit payload.
- Verify no schema, migration, or database-write contract changes are needed.

## Verification

Run focused Agent package tests first, followed by `go test -count=1 ./...` and
`go vet ./...`. Run `go test -race` for affected packages when a C toolchain is
available.

Run focused Platform task-status tests first, followed by the repository Python
test suite using its project virtual environment. Run syntax checks for touched
Python files and available CloudFormation/Serverless validation.

Live AWS mutation and readback require separately authorized test instances and
are reported separately from local verification.
