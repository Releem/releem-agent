# CloudFormation Metrics Collection Interval Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose `RELEEM_INTERVAL_COLLECT_ALL_METRICS` as a visible CloudFormation parameter with the existing 43200-second default.

**Architecture:** Add one numeric parameter to the existing template interface and reference it from the ECS container environment. Keep the change within `releem-agent-cloudformation.yml` and preserve all unrelated working-tree edits.

**Tech Stack:** AWS CloudFormation YAML, Amazon ECS task definitions

## Global Constraints

- Parameter name: `IntervalCollectAllMetrics`.
- Environment variable name: `RELEEM_INTERVAL_COLLECT_ALL_METRICS`.
- Type: `Number`.
- Default: `43200`.
- Minimum: `1`.
- The description must state that the value is in seconds and should remain at its default in most cases.
- Do not modify `releem-agent-cloudformation-private.yml`, built binaries, secrets, or local configuration.
- Do not commit the implementation unless the user explicitly requests a commit.

---

### Task 1: Expose the metrics collection interval

**Files:**
- Modify: `releem-agent-cloudformation.yml`
- Test: structural checks against `releem-agent-cloudformation.yml`

**Interfaces:**
- Consumes: the ECS `ContainerDefinitions[].Environment` list and the `AWS::CloudFormation::Interface` parameter group already defined by the template.
- Produces: CloudFormation parameter `IntervalCollectAllMetrics`, referenced by ECS environment variable `RELEEM_INTERVAL_COLLECT_ALL_METRICS`.

- [ ] **Step 1: Run the structural check before implementation**

```bash
rg -n "IntervalCollectAllMetrics|RELEEM_INTERVAL_COLLECT_ALL_METRICS" releem-agent-cloudformation.yml
```

Expected: no matches, demonstrating that the new parameter contract is absent.

- [ ] **Step 2: Add the CloudFormation parameter**

Insert after `DBQueryOptimization`:

```yaml
  IntervalCollectAllMetrics:
    Description: Interval in seconds for collecting all metrics. In most cases, leave the default value unchanged.
    Type: Number
    MinValue: 1
    Default: 43200
```

- [ ] **Step 3: Pass the parameter to the ECS container**

Insert after `RELEEM_DATABASES_QUERY_OPTIMIZATION`:

```yaml
                - Name: RELEEM_INTERVAL_COLLECT_ALL_METRICS
                  Value: !Ref IntervalCollectAllMetrics
```

- [ ] **Step 4: Expose the parameter in CloudFormation console metadata**

Append the following item to the existing `Required` parameter group:

```yaml
        - IntervalCollectAllMetrics
```

- [ ] **Step 5: Run focused verification**

```bash
rg -n -C 4 "IntervalCollectAllMetrics|RELEEM_INTERVAL_COLLECT_ALL_METRICS" releem-agent-cloudformation.yml
git diff --check -- releem-agent-cloudformation.yml
```

Expected: one parameter declaration with `Default: 43200` and `MinValue: 1`, one ECS environment reference, one metadata entry, and no whitespace errors.

- [ ] **Step 6: Run a CloudFormation-aware validation when available**

```bash
if command -v cfn-lint >/dev/null 2>&1; then cfn-lint releem-agent-cloudformation.yml; else echo "cfn-lint not installed; structural validation completed"; fi
```

Expected: `cfn-lint` exits successfully, or the explicit availability message is printed.

- [ ] **Step 7: Review scope**

```bash
git diff -- releem-agent-cloudformation.yml
git status --short
```

Expected: the target diff contains the user's pre-existing `Image` placeholder edit plus the three planned interval additions; the private template and binary remain unmodified and untracked.
