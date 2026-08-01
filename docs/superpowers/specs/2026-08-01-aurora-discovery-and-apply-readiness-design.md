# Aurora Discovery Resilience and Apply Readiness Design

## Context

The Aurora parameter-group work introduced live RDS topology discovery for every enhanced-metrics report and routed recommendations between instance and cluster parameter groups. Two regressions must be corrected:

1. A transient `DescribeDBInstances` or `DescribeDBClusters` failure currently makes the enhanced-metrics gatherer return an error. `utils.CollectMetrics` treats any gatherer error as fatal, so the complete metrics report is discarded for that cycle.
2. The apply path checks only that the DB instance is `available`. It no longer requires the attached instance parameter group to be `in-sync`, so another apply can start while AWS is still applying an earlier change.

This change is limited to Releem_Agent. It does not change Platform payloads, recommendation generation, parameter ownership, or the general error policy of `utils.CollectMetrics`.

## Goals

- Continue collecting and sending metrics during a transient live RDS discovery failure when startup or previously refreshed metadata is available.
- Keep refreshing metadata once per report so Aurora writer/reader role changes and resource identifiers are observed after recovery.
- Prevent all AWS parameter-group mutations until the attached instance parameter group is strictly `in-sync`.
- Restore public task exit code `2` for the parameter-group-not-ready condition.
- Preserve the startup database endpoint and the existing per-report metadata consistency guarantee.

## Non-goals

- Making every metrics gatherer tolerant of errors.
- Persisting discovered metadata across Agent process restarts.
- Adding retry loops or changing report scheduling.
- Adding a separate readiness check for the cluster parameter group.
- Changing the post-modification waiter, which may continue to accept `pending-reboot` as a completed AWS response.
- Changing Aurora parameter classification, writer-only cluster ownership, group validation, batching, or unit conversion.

## Selected Approach

Use a gatherer-local, in-memory last-known-good metadata cache and restore the strict pre-apply instance parameter-group guard.

This keeps fallback policy next to the operation that can safely use stale metadata. Moving the policy into `utils.CollectMetrics` would affect every gatherer and could send arbitrary partial reports. Wrapping discovery in `main.go` would hide fallback behavior outside the gatherer and make report-level behavior harder to test.

## Enhanced-Metrics Metadata Lifecycle

`NewAWSRDSEnhancedMetricsGatherer` will receive the metadata discovered successfully during Agent startup in addition to the live discovery function. The gatherer will store it as its initial last-known-good value.

For every `GetMetrics` call:

1. Call live discovery exactly once.
2. If discovery succeeds, atomically replace the cached metadata and use that new value for the report.
3. If discovery fails, log the failure and use the cached metadata. The discovery error is not returned because the enhanced-metrics collection can still proceed.
4. Copy the selected metadata into a local value before the CloudWatch request and payload construction. The CloudWatch log stream and every `Host` field in that report therefore come from one consistent snapshot.
5. Return the existing CloudWatch retrieval, empty-event, or JSON parsing errors normally. Only discovery failure gains fallback behavior.

The cache will be protected by a small mutex because the same gatherer can be called concurrently. Successful discovery is the only operation that updates the cache. A failed discovery never replaces valid cached metadata with zero values.

The main startup flow remains fail-closed: if initial RDS discovery fails, Agent startup fails as it does now. Startup discovery continues to set the configured database endpoint once. Later metadata refreshes and fallbacks do not mutate `configuration.MysqlHost` or the PostgreSQL endpoint.

## Pre-Apply Readiness Guard

After `awsrds.DiscoverInstance` succeeds, `ApplyConfAwsRds` will evaluate readiness before group validation, parameter listing, planning, or modification:

1. `metadata.InstanceStatus` must equal `available`; otherwise preserve exit code `1`.
2. `metadata.DBParameterGroupStatus` must equal `in-sync`; every other value, including `applying`, `pending-reboot`, and an empty status, blocks the operation.

When the parameter group is not ready, the task will:

- record a failure in the instance scope with the instance identifier and observed status;
- return task failure status `4` and restored exit code `2`;
- make no `DescribeDBParameters`, `DescribeDBClusterParameters`, `ModifyDBParameterGroup`, or `ModifyDBClusterParameterGroup` calls.

The instance readiness guard applies to the whole apply operation. This intentionally blocks both instance and cluster mutations: an Agent must not start a new routed apply while its attached instance group is still converging.

This pre-apply rule is separate from the post-modification waiter. The waiter can still treat `pending-reboot` as an accepted final state after Releem has submitted static parameter changes.

## Error and Compatibility Semantics

- A discovery fallback is observable in logs but does not change the metrics payload schema.
- Reports generated from cached metadata may temporarily carry the previous writer/reader role; the next successful discovery refreshes it.
- Existing startup discovery requirements and existing non-discovery gatherer failures remain unchanged.
- Exit codes `0`, `1`, `6`, `8`, and `9` retain their current meanings; exit code `2` again identifies an attached parameter group that is not ready.
- The guard applies equally to ordinary RDS MySQL, Aurora MySQL, RDS PostgreSQL, and Aurora PostgreSQL because all use the same AWS apply entry point.

## Test Strategy

Implementation will follow test-driven development.

### Enhanced metrics

- Update the metadata lifecycle test to pass startup metadata explicitly.
- Verify successive successful discoveries update writer/reader fields while leaving the configured endpoint unchanged.
- Verify a later discovery failure returns no error, requests CloudWatch with the last successful resource ID, and produces `Host` metadata from that cached snapshot.
- Verify another successful discovery after the failure replaces the cache.
- Add concurrency coverage for cache access suitable for `go test -race` when a C toolchain is available.
- Verify `utils.CollectMetrics` no longer drops a complete collection solely because this gatherer experiences a discovery failure; subsequent gatherers still run.

### Parameter application

- Add table-driven coverage for `in-sync`, `applying`, `pending-reboot`, and empty parameter-group statuses.
- Verify only `in-sync` reaches parameter listing and modification.
- Verify every blocked status returns exit code `2`, task status `4`, and a structured instance-scope failure.
- Verify blocked Aurora applies make neither instance nor cluster list/modify calls.
- Retain waiter tests proving that post-modification `pending-reboot` remains acceptable.

## Verification

Run focused tests for `metrics/system`, `utils`, `tasks`, and `awsrds`, followed by `go test ./...` and `go vet ./...`. Run `go test -race` for the affected packages if a C compiler is available; otherwise report that environment limitation explicitly.
