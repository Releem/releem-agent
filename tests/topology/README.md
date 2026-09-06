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
`create` performs a mutation. Each run owns a deterministic custom-mode VPC,
`10.212.0.0/24` regional subnet, Cloud Router, Cloud NAT, and two firewall
rules. Network, subnet, router, and firewall ownership uses an exact run marker
in `description`; the NAT is accepted only as the exact child configuration of
the owned router and primary-only subnet. The subnet must have no secondary IP
ranges, so GCP's `PRIMARY_IP_RANGE` and `ALL_IP_RANGES` NAT representations are
equivalent without broadening address coverage. Existing or partial resources
fail closed unless every available resource has the expected ownership and
semantics. Firewall
inventory covers every rule attached to the dedicated VPC and rejects anything
other than the exact internal and IAP rules. Both rules must have no source
tags, source service accounts, target service accounts, or other additive
source/target selectors. Firewall `allowed` objects are normalized by protocol
and port set because GCP may split one requested port list across multiple
objects or reorder it. Validation still requires only TCP/22 for IAP and only
TCP/3306, TCP/33060, and TCP/33061 internally; duplicate ports, ranges,
all-port entries, extra protocols, and malformed allowed objects fail closed.

Instances and disks use GCP labels. Resumed instances must all use one
supported machine type, the selected zone, and the exact run subnet and
network. They have no external address. Cloud NAT supplies private outbound
access for package and installer downloads. Operator SSH and scp always use
IAP. The only cloud ingress is IAP TCP/22 and subnet-sourced TCP/3306,
TCP/33060, and TCP/33061 to the run tags; guest UFW restricts the database ports
to `10.212.0.0/24` as well.

`create` builds and validates the non-billable network components before adding
Cloud NAT. If network creation or validation fails before the first VM, it
rolls back only resources created by that invocation, in dependency order;
pre-existing owned resources are never included in this rollback.

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

`configure` inspects AdminAPI metadata, topology mode, member status, and
read/write mode. It adds absent members, attempts bounded rejoin for supported
OFFLINE/MISSING states, rejects incompatible or unsafe states, and requires the
exact ONLINE member set and member-role semantics before continuing. An
ordinary single-primary cluster and the ClusterSet primary cluster require one
R/W internal primary; a ClusterSet replica requires all three members,
including its internal primary, to be fenced R/O. Multi-primary requires all
three ONLINE members to be R/W primaries. State evidence is accepted only when
the ClusterSet primary-cluster identity, cluster roles, global primary instance,
member roles, and writer availability agree. The expected primary cluster is
checked before ClusterSet transitions and after controlled switchover. Expected
stopped-member and stopped-ClusterSet-replication states are also validated.

`exercise` records UTC transition markers, restarts each Agent for immediate
collection, waits for current-state persistence, and queries every matching
ClickHouse row for the exact tenant-scoped SID/current-RID pairs in a bounded
post-marker window whose lower bound preserves the marker's exact millisecond
value with `DateTime64`. Validation rejects missing, duplicate, extra, early,
late, or relation-mismatched observations; there is no `LIMIT 1 BY sid`
selection.
Sanitized MySQL/JSON evidence is
stored under `/tmp/releem-db-topology-evidence/gcp/` with mode `0600` defaults.
Addresses and credentials are excluded from the evidence and inventory output.

Independent deadlines can be adjusted with
`TOPOLOGY_SSH_TIMEOUT_SECONDS`, `TOPOLOGY_SSH_READY_TIMEOUT_SECONDS`,
`TOPOLOGY_BOOTSTRAP_TIMEOUT_SECONDS`, `TOPOLOGY_ADMINAPI_TIMEOUT_SECONDS`,
`TOPOLOGY_SWITCHOVER_TIMEOUT_SECONDS`,
`TOPOLOGY_MYSQL_PERSISTENCE_TIMEOUT_SECONDS`, and
`TOPOLOGY_CLICKHOUSE_PERSISTENCE_TIMEOUT_SECONDS`. Individual Platform calls
are also bounded by `TOPOLOGY_MYSQL_CONNECT_TIMEOUT_SECONDS`,
`TOPOLOGY_MYSQL_QUERY_TIMEOUT_SECONDS`,
`TOPOLOGY_CLICKHOUSE_CONNECT_TIMEOUT_SECONDS`, and
`TOPOLOGY_CLICKHOUSE_QUERY_TIMEOUT_SECONDS`. The ClickHouse marker window is
controlled by `TOPOLOGY_CLICKHOUSE_MARKER_WINDOW_SECONDS`.

The VMs are intentionally left running after validation. Teardown is available
for a later explicit request and is bounded by both ownership checks and a
second confirmation variable:

```bash
export GCP_TOPOLOGY_CONFIRM_DESTROY="$GCP_TOPOLOGY_RUN_LABEL"
tests/topology/gcp_innodb_cluster.sh destroy
```

Never run `destroy` as routine test cleanup.

After exact ownership and semantic validation, `destroy` removes instances,
owned detached disks, NAT, router, firewall rules, subnet, and network in
dependency order. It does not run quota, zone, or machine-type selection.
