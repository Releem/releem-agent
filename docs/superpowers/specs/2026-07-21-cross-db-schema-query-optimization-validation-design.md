# Cross-Database Schema and Query Optimization Validation Design

## Goal

Validate every schema-check, query-optimization, and applied-index-detection behavior changed from `origin/master` across Releem_Agent and Releem_Platform. Close automated test gaps, fix defects found by expert review, and run reproducible local end-to-end tests against a dedicated GCP VM containing PostgreSQL and MySQL.

The live matrix contains 30 distinct PostgreSQL scenarios and 30 distinct MySQL scenarios. MariaDB compatibility is covered by automated MySQL/MariaDB tests, not a separate 30-scenario live matrix.

## Repositories and Baselines

- Agent worktree: `/home/dkochetov/Документы/laptop/releem/worktrees/releem-agent-pg-query-optimization-and-schema-checks`
- Agent branch: `feature/pg-query-optimization-and-schema-checks`
- Platform worktree: `/home/dkochetov/Документы/laptop/releem/worktrees/releem-platform-pg-query-optimization-and-schema-checks`
- Platform branch: `feature/pg_query_optimization`
- Review baseline for each repository: its current `origin/master` merge base.
- Review scope: every production-code change in `origin/master...HEAD`, plus any fixes and tests added during this effort.

The worktrees are preserved. No merge, push, force-push, branch deletion, or worktree cleanup is part of this work.

## Expert Subagents

Four independent expert roles review the complete branch diff before implementation:

1. PostgreSQL DBA expert
   - PostgreSQL catalog and version compatibility.
   - `pg_stat_statements`, EXPLAIN, sequences, statistics, partitions, expression and partial indexes.
   - Safety and correctness of PostgreSQL schema recommendations.
2. MySQL/MariaDB DBA expert
   - MySQL and MariaDB metadata compatibility.
   - AUTO_INCREMENT, fragmentation, indexes, foreign keys, generated columns, and query digests.
   - Differences between MySQL, MariaDB, RDS, and Cloud SQL payloads.
3. Python expert
   - Platform payload validation, S3 loading, ClickHouse writes, queue processing, TWEngine adaptation, error handling, and test isolation.
4. Application architecture expert
   - Agent-to-Platform contract, rolling compatibility, failure propagation, ownership boundaries, observability, and destructive-recommendation safety.

The first pass is read-only and produces:

- a changed-code coverage map;
- missing automated test cases;
- correctness, reliability, security, and compatibility findings;
- exact file and line references;
- proposed test ownership with non-overlapping write sets.

Test implementation is delegated only after the first-pass reports are reconciled. Each worker owns explicit files and must not revert concurrent work. The main agent reviews all uploaded changes and performs production-code fixes. A second independent review is run after all fixes and tests.

## Automated Test Strategy

### Coverage Definition

"Complete coverage" means every changed production behavior related to schema checks, query optimization, applied-index detection, payload transport, and database metadata collection has at least one positive test and one relevant negative or boundary test.

Coverage evidence includes:

- Go package coverage for changed Agent packages;
- Python line and branch coverage for changed Platform modules;
- Bats coverage for changed installer/grant behavior;
- a changed-file-to-test mapping reviewed by the architecture expert;
- explicit integration tests for contracts that cannot be represented by isolated unit tests.

The numerical report is evidence, not a substitute for behavioral assertions. Generated files, docs, logging-only lines, unreachable platform bootstrap code, and third-party wrappers may be excluded only with a written reason.

### Agent Tests

Agent tests cover:

- legacy monitoring payload unchanged when query optimization is disabled;
- query-optimization payload fields, types, and microsecond units;
- PostgreSQL 12 legacy `total_time` compatibility and missing rows fallback;
- PostgreSQL 14+ `total_exec_time` and rows collection;
- 100 successful EXPLAIN plans by total time and 100 additional successful plans by mean time;
- failed or empty EXPLAIN attempts not consuming success quota;
- parameterized queries, direct queries, permission failures, connection failures, and unsupported statements;
- schema collection continuing after a database or section failure;
- table, column, structured index, foreign-key, and sequence metadata;
- expression, partial, INCLUDE, unique, invalid, unready, partitioned, attached-partition, clustered, replica-identity, collation, opclass, direction, and NULL-order metadata;
- supported PostgreSQL version branches;
- MySQL/MariaDB schema metadata parity and installer grants.

### Platform Tests

Platform tests cover:

- request validation and PostgreSQL/MySQL dispatch;
- required and optional payload fields and exact units;
- S3 write/read round trips and missing/corrupt object failures;
- preservation and use of schema-collection failure metadata;
- ClickHouse writes for query text, metrics, and structured EXPLAIN;
- TWEngine query, server, table, column, and index payloads;
- schema resolution for qualified and unqualified PostgreSQL relations;
- duplicate, redundant, and unused index checks;
- PostgreSQL safety gates for partial, expression, INCLUDE, partitioned, invalid, exclusion, clustered, replica-identity, custom opclass/collation, sort direction, and NULL ordering;
- MySQL/MariaDB duplicate, redundant, unused, AUTO_INCREMENT, fragmentation, charset/collation, datatype, primary-key, and foreign-key checks;
- PostgreSQL sequence exhaustion and stale-statistics checks;
- applied-index detection by name and by equivalent definition;
- quoted identifiers, custom schemas, expression indexes, partial predicates, INCLUDE columns, uniqueness, access method, and `NULLS NOT DISTINCT` identity;
- queue state transitions, empty recommendation handling, malformed TWEngine responses, and memory-release paths;
- test isolation for environment variables, module globals, logging mocks, and S3 fakes.

## Live Test Architecture

### GCP VM

- GCP display name: `releem-test`
- GCP project ID: `static-mediator-400907`
- Create one dedicated Ubuntu 22.04 VM in `us-west1-a`.
- Name includes the date and purpose, for example `releem-schema-query-e2e-20260721`.
- Use an `e2-standard-4` or larger machine with a 40 GB standard persistent disk.
- Run pinned PostgreSQL and MySQL Docker containers with persistent Docker volumes.
- Do not expose database ports publicly.
- Reach both databases through SSH local port forwarding.
- Tag all generated database objects with a deterministic `releem_e2e_` prefix.
- Stop the VM after testing. Do not delete it without explicit user approval.

### Local Components

Run from the specified worktrees:

1. Start `main_api.py` and confirm its health endpoint and query-metrics endpoint.
2. Start `main_azure.py` on a separate local port or run it sequentially when it cannot coexist with `main_api.py`.
3. Start `query_optimization.py` and confirm it can claim and complete queue records.
4. Build the Agent from the Agent worktree after all automated tests pass.
5. Create temporary Agent configs from `/home/dkochetov/Документы/laptop/releem/Releem_Agent/.config`.
6. Override only DB host/port, API endpoint, API key, and query-optimization flags required by the test.
7. Run the local Agent with `--config=<temporary-config> --task=queries_optimization`.

Prefer a supported runtime URL override. If the Agent has no runtime override, make a narrowly scoped temporary source change, build the test binary, and restore only that temporary change after the run. Do not revert any branch changes.

### Evidence Per Scenario

Each scenario records:

- scenario ID and DBMS;
- setup SQL and workload SQL;
- expected Agent query and schema payload facts;
- expected schema checks and severities;
- expected query-optimization eligibility and TWEngine job facts;
- expected applied-index-detection result;
- Agent request ID;
- Platform server ID, queue ID, and queue terminal state;
- relevant ClickHouse rows or mocked equivalent when live ClickHouse is unavailable;
- pass/fail result and diagnostic details.

Secrets, tokens, password hashes, and full grants are redacted from artifacts.

## PostgreSQL Live Matrix

All PostgreSQL tests use `pg_stat_statements`. Workload statements are executed enough times to appear in the snapshot. Statistics-sensitive cases run `ANALYZE` or controlled churn as specified.

1. Healthy heap table with primary key and no advisory.
2. Heap table without primary key.
3. Integer sequence below 75 percent consumption.
4. Integer sequence exactly at the 75 percent threshold, with no exhaustion advisory.
5. Integer sequence above 75 percent consumption.
6. Descending sequence approaching its minimum.
7. Cycling sequence near its boundary, which must not produce exhaustion advice.
8. Unowned sequence near exhaustion.
9. Owned sequence with bigint target column.
10. Table with fresh manual ANALYZE statistics.
11. Table with fresh autoanalyze statistics.
12. Table with high `n_mod_since_analyze` and stale statistics.
13. Empty table with no analyze timestamps.
14. Fully duplicate ordinary indexes.
15. Covering redundant indexes where one key list is a strict prefix.
16. Used index that must not be reported unused.
17. Unused index with insufficient observation uptime.
18. Unused index with sufficient uptime and stable stats reset.
19. Unique index excluded from unsafe unused-index removal.
20. Primary-key index excluded from removal.
21. Expression index identity and nullable expression key.
22. Two distinct expression indexes that must not be treated as duplicates.
23. Full and partial index on the same keys.
24. Two partial indexes with different predicates.
25. INCLUDE index versus ordinary covering index.
26. Descending or custom NULL-order index identity.
27. Custom opclass or collation index identity.
28. Partitioned table with parent and attached partition indexes.
29. Invalid or unready index excluded from recommendations and applied-index matches.
30. Query-optimization recommendation followed by actual index creation and successful equivalent-index detection, including a differently named equivalent index.

## MySQL Live Matrix

The MySQL matrix uses Performance Schema statement digests and InnoDB tables unless a scenario explicitly requires another form.

1. Healthy InnoDB table with primary key and no advisory.
2. Table without primary key.
3. INT AUTO_INCREMENT below 75 percent consumption.
4. INT AUTO_INCREMENT exactly at the 75 percent threshold, with no exhaustion advisory.
5. INT AUTO_INCREMENT above 75 percent consumption.
6. BIGINT AUTO_INCREMENT below threshold.
7. Unsigned integer AUTO_INCREMENT boundary.
8. Non-AUTO_INCREMENT integer column excluded from exhaustion checks.
9. Low-fragmentation table.
10. High-fragmentation table eligible for OPTIMIZE TABLE.
11. Small fragmented table below minimum-size guard.
12. Fully duplicate non-unique indexes.
13. Fully duplicate unique indexes with equivalent semantics.
14. Covering redundant indexes with strict left-prefix identity.
15. Same columns in different order, not duplicate or redundant.
16. Prefix-length indexes with different `SUB_PART` values.
17. Used index excluded from UNUSED_INDEX.
18. Unused index with insufficient uptime.
19. Unused index with sufficient uptime.
20. Primary and foreign-key supporting indexes excluded from unsafe removal.
21. Functional or generated-column index identity where supported.
22. Invisible index handling.
23. FULLTEXT index excluded from btree duplicate logic.
24. SPATIAL index excluded from btree duplicate logic.
25. Mixed character sets in one schema.
26. Mixed collations in one schema.
27. Suspicious datatype or oversized character column check.
28. Foreign key with and without a supporting child index.
29. Query digest with EXPLAIN failure and continued processing of other queries.
30. Query-optimization recommendation followed by actual index creation and successful detection by name and by equivalent differently named definition.

## MariaDB Automated Compatibility Matrix

Automated tests cover MariaDB-specific differences without a separate live VM matrix:

- version and feature detection;
- `information_schema` column differences;
- generated columns and functional-index representation;
- invisible/ignored indexes where applicable;
- index statistics nullability;
- sequence-like AUTO_INCREMENT behavior;
- installer grants and cloud-managed permission failures;
- legacy payload compatibility.

## Failure Handling

- A failed scenario does not erase evidence from completed scenarios.
- Infrastructure failures are separated from product failures.
- Agent collection failure, Platform ingestion failure, queue failure, TWEngine failure, and assertion failure have distinct result states.
- Database setup is idempotent and can be rerun after partial failure.
- Every long-running process has a PID/session record and bounded wait.
- Every network operation has a timeout.
- Platform and Agent logs are captured per run.
- The GCP VM is stopped only after log and result artifacts are copied locally.

## Acceptance Criteria

1. Both branch diffs receive independent PostgreSQL DBA, MySQL/MariaDB DBA, Python, and architecture reviews.
2. All Critical, High, Important, P0, P1, and P2 correctness findings are fixed or explicitly presented to the user when a product decision is required.
3. Every changed schema-check, query-optimization, and applied-index-detection behavior has automated positive and boundary/negative coverage.
4. Agent Go tests, Platform Python tests, installer tests, format checks, and changed-code coverage checks pass.
5. The Agent builds from the feature worktree.
6. Local `main_api.py`, `main_azure.py`, and `query_optimization.py` process test traffic successfully.
7. All 30 PostgreSQL and all 30 MySQL live scenarios produce recorded results.
8. Every expected schema check is detected and every expected non-finding remains absent.
9. Applied-index detection succeeds for exact-name and equivalent-definition cases without accepting non-equivalent indexes.
10. A final expert review of `origin/master...HEAD` plus new changes returns no unresolved blocking findings.
11. Worktrees remain available and the GCP test VM is stopped, not deleted.

## Non-Goals

- Merging or pushing either feature branch.
- Deploying Platform or Agent to production.
- Creating a permanent public database endpoint.
- Running a separate 30-scenario MariaDB live matrix.
- Automatically applying arbitrary TWEngine DDL outside the controlled test scenarios.
- Deleting the test VM without explicit approval.
