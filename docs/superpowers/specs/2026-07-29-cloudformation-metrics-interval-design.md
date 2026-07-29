# CloudFormation Metrics Collection Interval Design

## Goal

Allow users of `releem-agent-cloudformation.yml` to configure
`RELEEM_INTERVAL_COLLECT_ALL_METRICS` while guiding most users to retain the
agent's existing default interval.

## Template Interface

Add a visible CloudFormation parameter named `IntervalCollectAllMetrics`:

- Type: `Number`
- Default: `43200`
- Minimum: `1`
- Description: the interval is measured in seconds and, in most cases, the
  default value should be left unchanged.

Include the parameter in the existing `Required` parameter group so it appears
with the other agent settings in the CloudFormation console.

## ECS Integration

Add `RELEEM_INTERVAL_COLLECT_ALL_METRICS` to the Releem Agent container
environment and set its value from `IntervalCollectAllMetrics`.

The default preserves the current Docker entrypoint behavior, which uses
`43200` seconds when the environment variable is absent.

## Compatibility and Validation

Existing stack deployments keep the same effective interval unless a user
overrides the new parameter. Validation consists of:

- parsing the YAML with a CloudFormation-aware validator when available;
- checking that the parameter is declared, exposed in the interface metadata,
  and referenced by the ECS container environment;
- reviewing the final diff to ensure unrelated working-tree changes remain
  untouched.
