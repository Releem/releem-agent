# Cross-Database Live Validation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run 30 distinct PostgreSQL and 30 distinct MySQL schema/query-optimization scenarios through a locally built Agent and locally running Platform against a dedicated GCP VM.

**Architecture:** A manifest-driven Python harness provisions deterministic database fixtures over SSH tunnels, starts local Platform processes, runs the Agent with temporary configs and `--task=queries_optimization`, triggers the optimization worker, and validates payload, queue, checks, recommendations, and applied-index identity. One GCP VM hosts isolated PostgreSQL and MySQL containers without public database ports.

**Tech Stack:** GCP Compute Engine, Docker, PostgreSQL, MySQL, SSH local forwarding, Go Agent, Python Platform, `unittest`, JSON artifacts.

## Global Constraints

- Do not start this plan until the automated gate in `2026-07-21-cross-db-automated-coverage-review.md` is green.
- GCP project ID is `static-mediator-400907`; display name is `releem-test`.
- Create one dedicated Ubuntu 22.04 VM in `us-west1-a`.
- Do not expose PostgreSQL or MySQL ports publicly.
- Use SSH tunnels from the local workstation.
- Run the Agent locally from the Agent worktree.
- Run `main_api.py`, `main_azure.py`, and `query_optimization.py` locally from the Platform worktree.
- Use temporary Agent configs derived from `/home/dkochetov/Документы/laptop/releem/Releem_Agent/.config`.
- Force collection with `--task=queries_optimization`.
- Run 30 distinct scenarios for PostgreSQL and 30 distinct scenarios for MySQL.
- MariaDB is not a separate live matrix.
- Store no secrets in git or result artifacts.
- Stop the VM after evidence is copied; do not delete it without explicit approval.
- Do not create implementation commits unless the user explicitly requests them.

---

### Task 1: Build the Manifest-Driven Live Harness

**Files:**
- Create in Platform: `tests/live_schema_query/__init__.py`
- Create in Platform: `tests/live_schema_query/models.py`
- Create in Platform: `tests/live_schema_query/runner.py`
- Create in Platform: `tests/live_schema_query/processes.py`
- Create in Platform: `tests/live_schema_query/agent.py`
- Create in Platform: `tests/live_schema_query/results.py`
- Create in Platform: `tests/live_schema_query/README.md`

**Interfaces:**
- `Scenario` describes setup, workload, expected checks, optimization, and applied-index behavior.
- `LiveRunner.run_scenario(scenario) -> ScenarioResult` executes one isolated case.
- JSON Lines result output is append-only and redacted.

- [ ] **Step 1: Add harness model tests**

```python
class ScenarioModelTests(unittest.TestCase):
    def test_rejects_missing_expected_checks(self):
        with self.assertRaises(ValueError):
            Scenario.from_dict({"id": "pg-01", "dbms": "postgresql"})

    def test_result_redacts_secrets(self):
        result = ScenarioResult("pg-01", "passed", {"password": "secret", "queue_id": 7})
        self.assertNotIn("secret", result.to_json())
```

- [ ] **Step 2: Implement immutable scenario models**

```python
@dataclass(frozen=True)
class Scenario:
    id: str
    dbms: str
    setup_sql: tuple[str, ...]
    workload_sql: tuple[str, ...]
    expected_checks: tuple[str, ...]
    forbidden_checks: tuple[str, ...]
    expect_query_optimization: bool
    applied_index_sql: str | None = None
    expect_applied_index: bool | None = None
```

Validate IDs, DBMS values, unique scenario names, non-empty setup/workload, and explicit positive and forbidden check lists.

- [ ] **Step 3: Implement bounded process management**

`ManagedProcess` starts a command in its own process group, redirects output to a per-process log, waits for a TCP or HTTP readiness probe, and terminates the process group on exit. Every wait accepts a timeout and includes the log tail in raised errors.

- [ ] **Step 4: Implement Agent invocation**

```python
command = [
    str(agent_binary),
    "--config=" + str(config_path),
    "--task=queries_optimization",
]
```

Capture exit status, request ID from logs, elapsed time, and redacted stderr/stdout paths.

- [ ] **Step 5: Implement append-only results**

Write one JSON object per scenario with setup, workload, Agent, Platform, queue, checks, recommendations, applied-index, status, and diagnostics. Replace values for keys matching `password`, `token`, `secret`, `api_key`, or `authorization` with `***REDACTED***` recursively.

- [ ] **Step 6: Run harness unit tests**

```bash
/home/dkochetov/Документы/laptop/releem/Releem_Platform/venv/bin/python -B -m unittest discover -s tests/live_schema_query -p 'test*.py'
```

---

### Task 2: Define All 60 Scenario Manifests and SQL Fixtures

**Files:**
- Create: `tests/live_schema_query/scenarios/postgresql.json`
- Create: `tests/live_schema_query/scenarios/mysql.json`
- Create: `tests/live_schema_query/sql/postgresql_setup.sql`
- Create: `tests/live_schema_query/sql/postgresql_workload.sql`
- Create: `tests/live_schema_query/sql/mysql_setup.sql`
- Create: `tests/live_schema_query/sql/mysql_workload.sql`
- Create: `tests/live_schema_query/test_scenario_manifests.py`

**Interfaces:**
- Scenario IDs are `pg-01` through `pg-30` and `mysql-01` through `mysql-30`.
- Every SQL object uses a `releem_e2e_` prefix.

- [ ] **Step 1: Add manifest completeness tests**

```python
def test_postgresql_manifest_has_exact_ids(self):
    scenarios = load_scenarios(POSTGRESQL_MANIFEST)
    self.assertEqual([f"pg-{number:02d}" for number in range(1, 31)], [item.id for item in scenarios])

def test_mysql_manifest_has_exact_ids(self):
    scenarios = load_scenarios(MYSQL_MANIFEST)
    self.assertEqual([f"mysql-{number:02d}" for number in range(1, 31)], [item.id for item in scenarios])
```

Also assert each scenario has setup SQL, workload SQL, explicit expected and forbidden checks, deterministic cleanup, and applied-index expectations where applicable.

- [ ] **Step 2: Define PostgreSQL scenarios**

Encode the exact 30 PostgreSQL cases from the approved design in order. Use separate schemas when the same table name is required. Manipulate sequences with `setval`, statistics with controlled INSERT/UPDATE/ANALYZE operations, and index states with PostgreSQL catalog-supported DDL. Create the invalid-index live case by starting `CREATE INDEX CONCURRENTLY` on a sufficiently populated fixture table, cancelling its backend after catalog creation, and verifying `indisvalid = false`. Keep the transient `indisready = false` branch in automated catalog tests because it cannot be held reliably as a live steady state.

- [ ] **Step 3: Define MySQL scenarios**

Encode the exact 30 MySQL cases from the approved design in order. Use `ALTER TABLE ... AUTO_INCREMENT`, controlled data and `ANALYZE TABLE`, prefix indexes, generated columns, visibility where supported, and foreign keys. Compute AUTO_INCREMENT boundaries from type maxima in the assertion code.

- [ ] **Step 4: Define applied-index transitions**

At least five scenarios per DBMS perform two snapshots: before index creation and after creation. Include exact-name, differently named equivalent, expression/functional, schema-qualified, and one deliberately non-equivalent index.

- [ ] **Step 5: Run manifest tests**

```bash
/home/dkochetov/Документы/laptop/releem/Releem_Platform/venv/bin/python -B -m unittest tests.live_schema_query.test_scenario_manifests
```

---

### Task 3: Provision the Dedicated GCP VM

**Files:**
- Create: `tests/live_schema_query/gcp/startup.sh`
- Create: `tests/live_schema_query/gcp/provision.sh`
- Create: `tests/live_schema_query/gcp/stop.sh`

**Interfaces:**
- VM name: use the first available name from `releem-schema-query-e2e-20260721`, then suffixes `-2` through `-20`; fail rather than overwrite an existing instance if all names are occupied.
- PostgreSQL container binds only VM loopback port `15432`.
- MySQL container binds only VM loopback port `13306`.

- [ ] **Step 1: Write startup-script validation tests**

Use shell assertions to verify the script contains pinned images, loopback-only bindings, restart policies, health checks, named volumes, and no embedded credentials from repository files.

- [ ] **Step 2: Implement VM provisioning**

```bash
gcloud compute instances create "$VM_NAME" \
  --project=static-mediator-400907 \
  --zone=us-west1-a \
  --machine-type=e2-standard-4 \
  --image-family=ubuntu-2204-lts \
  --image-project=ubuntu-os-cloud \
  --boot-disk-size=40GB \
  --boot-disk-type=pd-standard \
  --metadata-from-file=startup-script=tests/live_schema_query/gcp/startup.sh \
  --labels=purpose=releem-schema-query-e2e
```

Generate database passwords into a local mode-0600 temporary environment file. The startup script installs Docker but does not start credentialed containers. Copy the environment file over SSH after the VM is ready, set remote mode `0600`, run the container setup command, and delete the remote file after Docker has stored the container configuration. Do not place database credentials in instance metadata.

- [ ] **Step 3: Start pinned containers**

Use PostgreSQL 16 and MySQL 8.0 images. Enable `pg_stat_statements` through command/config arguments and initialize it in the test database. Enable MySQL Performance Schema statement consumers required by the Agent.

- [ ] **Step 4: Verify VM and database health**

```bash
gcloud compute ssh "$VM_NAME" --project=static-mediator-400907 --zone=us-west1-a --command='docker ps --format "{{.Names}} {{.Status}}"'
```

Require both health checks to be healthy before continuing.

---

### Task 4: Start SSH Tunnels and Local Platform Processes

**Files:**
- Create: `tests/live_schema_query/local_runtime.py`
- Create outside repository: `/tmp/releem-live-schema-query/runtime.env`

**Interfaces:**
- Local PostgreSQL endpoint: `127.0.0.1:<allocated-port>`.
- Local MySQL endpoint: `127.0.0.1:<allocated-port>`.
- Platform ports are allocated dynamically and exported to temporary Agent configs.

- [ ] **Step 1: Open bounded SSH tunnels**

Use `gcloud compute ssh` or the resolved SSH command with:

```text
-N -o ExitOnForwardFailure=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=3
-L <local-pg-port>:127.0.0.1:15432
-L <local-mysql-port>:127.0.0.1:13306
```

Verify each forwarded port with a real SQL connection.

- [ ] **Step 2: Prepare local Platform environment**

Load the Platform worktree environment without printing secrets. Set the local Agent API target, TWEngine URL/token, MySQL/ClickHouse/S3 dependencies, and isolated log/result paths.

- [ ] **Step 3: Start `main_api.py`**

Wait for its health endpoint and send a minimal authenticated request that must reach request validation.

- [ ] **Step 4: Start `main_azure.py`**

Use a separate port when supported; otherwise stop `main_api.py`, run the same smoke payload through `main_azure.py`, capture evidence, then restart `main_api.py` for the full matrix.

- [ ] **Step 5: Start `query_optimization.py`**

Confirm the worker is polling and can observe a deliberately inserted test queue row. Delete or complete only the test row created by this harness.

---

### Task 5: Build and Run the Local Agent

**Files:**
- Create outside repository: `/tmp/releem-live-schema-query/releem-postgresql.conf`
- Create outside repository: `/tmp/releem-live-schema-query/releem-mysql.conf`
- Create outside repository: `/tmp/releem-live-schema-query/releem-agent-e2e`

**Interfaces:**
- Configs derive from existing files under `/home/dkochetov/Документы/laptop/releem/Releem_Agent/.config`.
- Only endpoint, DB host/port, API key, and optimization flags are overridden.

- [ ] **Step 1: Build the exact worktree**

```bash
/usr/local/go/bin/go build -o /tmp/releem-live-schema-query/releem-agent-e2e .
```

Record the Agent HEAD SHA and binary SHA-256.

- [ ] **Step 2: Select the local API override mechanism**

Prefer an existing config or environment URL override. If absent, add a tested runtime override to production code and `.env.example`; do not hardcode the temporary port. The override must preserve the production default.

- [ ] **Step 3: Generate mode-0600 configs**

Copy the PostgreSQL and MySQL templates, replace only the allowed fields, and validate that logging never prints passwords or API keys.

- [ ] **Step 4: Run forced collections**

```bash
/tmp/releem-live-schema-query/releem-agent-e2e \
  --config=/tmp/releem-live-schema-query/releem-postgresql.conf \
  --task=queries_optimization

/tmp/releem-live-schema-query/releem-agent-e2e \
  --config=/tmp/releem-live-schema-query/releem-mysql.conf \
  --task=queries_optimization
```

Require zero exit status and capture request IDs. If the Agent is a long-running process, use a bounded timeout and treat confirmed successful task delivery as completion before terminating it.

---

### Task 6: Execute and Validate 30 PostgreSQL Scenarios

**Files:**
- Create result artifact: `/tmp/releem-live-schema-query/results/postgresql.jsonl`
- Create summary artifact: `/tmp/releem-live-schema-query/results/postgresql-summary.md`

**Interfaces:**
- Consumes PostgreSQL manifest and local runtime.
- Produces exactly 30 terminal scenario results.

- [ ] **Step 1: Load all PostgreSQL fixtures and workload**

Apply idempotent cleanup and setup, execute workloads, enable PGSS collection, and verify all expected query IDs/databases appear before Agent collection.

- [ ] **Step 2: Run Agent and Platform pipeline**

Run forced Agent collection, wait for S3/queue ingestion, and run the local optimization worker until the corresponding queue records reach terminal states.

- [ ] **Step 3: Validate schema checks**

For each `pg-01` through `pg-30`, compare expected and forbidden checks by type plus database/schema/table/index/sequence identity. Fail on extra destructive recommendations even when all expected checks exist.

- [ ] **Step 4: Validate query optimization and index creation**

For eligible queries, verify query text, rows per call, EXPLAIN, TWEngine job, recommendations, and ClickHouse persistence. Apply only manifest-approved test DDL, collect a second snapshot, and verify exact/equivalent applied-index matching.

- [ ] **Step 5: Write PostgreSQL summary**

Require 30 result rows. List pass/fail, queue ID, request ID, detected checks, optimization result, and applied-index result without secrets.

---

### Task 7: Execute and Validate 30 MySQL Scenarios

**Files:**
- Create result artifact: `/tmp/releem-live-schema-query/results/mysql.jsonl`
- Create summary artifact: `/tmp/releem-live-schema-query/results/mysql-summary.md`

**Interfaces:**
- Consumes MySQL manifest and local runtime.
- Produces exactly 30 terminal scenario results.

- [ ] **Step 1: Load all MySQL fixtures and workload**

Apply idempotent cleanup and setup, execute workloads, enable required Performance Schema consumers, and verify expected digests before Agent collection.

- [ ] **Step 2: Run Agent and Platform pipeline**

Run forced Agent collection, wait for ingestion, and process corresponding queue records locally.

- [ ] **Step 3: Validate schema checks**

For each `mysql-01` through `mysql-30`, compare expected and forbidden checks. Assert exact separation among `INDEX_DUPLICATION`, `REDUNDANT_INDEX`, and `UNUSED_INDEX`.

- [ ] **Step 4: Validate query optimization and index creation**

Validate TWEngine jobs and apply only approved test DDL. Collect a second snapshot and verify exact-name and equivalent-definition detection without accepting non-equivalent indexes.

- [ ] **Step 5: Write MySQL summary**

Require 30 result rows with the same evidence fields as PostgreSQL.

---

### Task 8: Final Live Review, Evidence, and VM Stop

**Files:**
- Create artifact: `/tmp/releem-live-schema-query/results/final-summary.md`
- Create artifact: `/tmp/releem-live-schema-query/results/process-inventory.txt`
- Create artifact: `/tmp/releem-live-schema-query/results/vm-state.json`

**Interfaces:**
- Produces final evidence without modifying or deleting the worktrees.

- [ ] **Step 1: Re-run focused automated suites after live fixes**

Any product defect fixed during live testing receives a failing automated regression test first. Re-run both complete automated gates after the final fix.

- [ ] **Step 2: Verify result counts and failures**

```python
assert len(load_results("postgresql.jsonl")) == 30
assert len(load_results("mysql.jsonl")) == 30
```

No scenario may remain `running` or `unknown`. Infrastructure-blocked scenarios are explicit failures unless rerun successfully.

- [ ] **Step 3: Stop local processes and tunnels**

Terminate only PIDs/process groups started by the harness. Verify their ports are closed and record the final process inventory.

- [ ] **Step 4: Copy final VM evidence and stop the VM**

```bash
gcloud compute instances stop "$VM_NAME" --project=static-mediator-400907 --zone=us-west1-a
gcloud compute instances describe "$VM_NAME" --project=static-mediator-400907 --zone=us-west1-a --format=json
```

Require `TERMINATED` state. Do not delete the VM or disks.

- [ ] **Step 5: Dispatch final expert live-result review**

The PostgreSQL and MySQL/MariaDB reviewers inspect their 30-case summaries; the Python and architecture reviewers inspect process, queue, payload, and failure evidence. Resolve every blocking finding or identify the exact external blocker.

- [ ] **Step 6: Report final state**

Report code changes, automated test counts, coverage evidence, 30/30 result tables for each DBMS, outstanding non-blocking risks, VM name/state, and all artifact paths. Do not expose secrets.
