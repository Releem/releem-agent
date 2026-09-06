# Live database topology harnesses

This directory contains operator-run acceptance harnesses for database topology.
They create billable cloud resources and are intentionally separate from the
normal unit and installation test suites.

## GCP InnoDB Cluster matrix

`gcp_innodb_cluster.sh` manages twelve fixed Ubuntu 22.04 x86-64 instances in
`static-mediator-400907`:

| Topology | Nodes | Expected mode |
| --- | --- | --- |
| `releem-ic-single-1..3` | 3 | single primary |
| `releem-ic-multi-1..3` | 3 | multi-primary |
| `releem-ic-cs-primary-1..3` | 3 | ClusterSet primary cluster |
| `releem-ic-cs-replica-1..3` | 3 | ClusterSet replica cluster |

The harness is resumable. `preflight` is always read-only and must pass before
`create` performs a mutation. Existing matching instances, disks, or firewall
rules are rejected unless their ownership marker matches the current run.
Instances and disks use GCP labels. Compute firewall rules do not support GCP
labels, so the two run-specific rules use an ownership string in `description`
and are checked with the same fail-closed collision semantics. Instances have
no external address. Operator SSH and scp always use IAP; the only ingress is
IAP TCP/22 and tagged internal TCP/3306, TCP/33060, and TCP/33061.

### Runtime secrets

Set secrets only in the invoking shell or a process-scoped secret provider. Do
not put them in this repository, command arguments, logs, or evidence files.
The harness never writes their values. `MYSQL_CLUSTER_PASSWORD` must contain
16-128 ASCII letters, digits, dots, underscores, or hyphens so it can cross the
non-interactive AdminAPI and SQL boundaries without unsafe quoting.

```bash
export GCP_TOPOLOGY_RUN_LABEL=task12-20260906
read -rsp 'Releem dev API key: ' RELEEM_API_KEY; echo
export RELEEM_API_KEY
read -rsp 'Ephemeral cluster admin password: ' MYSQL_CLUSTER_PASSWORD; echo
export MYSQL_CLUSTER_PASSWORD
```

Fresh installation receives the API key only through the process environment,
along with `RELEEM_ENV=dev`, `RELEEM_DB_MEMORY_LIMIT=0`,
`RELEEM_CRON_ENABLE=1`, `RELEEM_QUERY_OPTIMIZATION=true`, and the required
noninteractive local MySQL values. Harness state and evidence never contain
secret values. The installer still creates its normal protected Agent
configuration on each target VM.

Persistence checks require one exact positive numeric tenant UID. For the dev
Platform environment, load credentials without shell tracing and map the
existing variables directly:

```bash
set +x
source /home/dkochetov/Документы/laptop/releem/Releem_Platform/.env

export TOPOLOGY_MYSQL_HOST="$DB_HOST_MASTER"
export TOPOLOGY_MYSQL_USER="$DB_USER"
export TOPOLOGY_MYSQL_PASSWORD="$DB_PASSWORD"
export TOPOLOGY_MYSQL_DATABASE="${DB_NAME:-releemdb}"
export TOPOLOGY_MYSQL_PORT=3306

export TOPOLOGY_CLICKHOUSE_HOST="$CH_HOST"
export TOPOLOGY_CLICKHOUSE_USER="$CH_USER"
export TOPOLOGY_CLICKHOUSE_PASSWORD="$CH_PASSWORD"
export TOPOLOGY_CLICKHOUSE_DATABASE="${CH_NAME:-releemdb_dev}"
export TOPOLOGY_CLICKHOUSE_SCHEME=http
export TOPOLOGY_CLICKHOUSE_PORT=8123

read -rp 'Exact dev tenant UID: ' TOPOLOGY_UID
export TOPOLOGY_UID

[[ "$TOPOLOGY_MYSQL_DATABASE" == releemdb ]]
[[ "$TOPOLOGY_CLICKHOUSE_DATABASE" == releemdb_dev ]]
```

The defaults are MySQL `releemdb` and ClickHouse `releemdb_dev`. The explicit
checks above fail before cloud work if the loaded environment points elsewhere.

### Lifecycle

Run the commands in order. Review
`/tmp/releem-db-topology-evidence/gcp/$GCP_TOPOLOGY_RUN_LABEL/preflight.json`
before `create`.

```bash
tests/topology/gcp_innodb_cluster.sh preflight
tests/topology/gcp_innodb_cluster.sh create
tests/topology/gcp_innodb_cluster.sh configure
tests/topology/gcp_innodb_cluster.sh exercise
tests/topology/gcp_innodb_cluster.sh inventory
```

`configure` builds only to `/tmp/releem-agent-db-topology-x86_64`; it never
overwrites a repository binary. It installs the Agent only when absent, then
replaces `/opt/releem/releem-agent` while retaining owner, group, and mode. All
twelve services must be active and have the local build checksum. The build uses
`-buildvcs=false` because Go 1.26 can resolve linked-worktree VCS stamping to the
non-repository parent directory; this changes build metadata only.

`exercise` records UTC transition markers, restarts each Agent for immediate
collection, waits for current-state persistence, and verifies a post-marker
ClickHouse observation for every exact tenant-scoped SID and current RID.
Sanitized MySQL/JSON evidence is
stored under `/tmp/releem-db-topology-evidence/gcp/` with mode `0600` defaults.
Addresses and credentials are excluded from the evidence and inventory output.

Independent deadlines can be adjusted with
`TOPOLOGY_SSH_TIMEOUT_SECONDS`, `TOPOLOGY_SSH_READY_TIMEOUT_SECONDS`,
`TOPOLOGY_BOOTSTRAP_TIMEOUT_SECONDS`, `TOPOLOGY_ADMINAPI_TIMEOUT_SECONDS`,
`TOPOLOGY_SWITCHOVER_TIMEOUT_SECONDS`,
`TOPOLOGY_MYSQL_PERSISTENCE_TIMEOUT_SECONDS`, and
`TOPOLOGY_CLICKHOUSE_PERSISTENCE_TIMEOUT_SECONDS`.

The VMs are intentionally left running after validation. Teardown is available
for a later explicit request and is bounded by both ownership checks and a
second confirmation variable:

```bash
export GCP_TOPOLOGY_CONFIRM_DESTROY="$GCP_TOPOLOGY_RUN_LABEL"
tests/topology/gcp_innodb_cluster.sh destroy
```

Never run `destroy` as routine test cleanup.
