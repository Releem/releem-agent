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
labels, so the one run-specific rule uses an ownership string in `description`
and is checked with the same fail-closed collision semantics.

### Runtime secrets

Set secrets only in the invoking shell or a process-scoped secret provider. Do
not put them in this repository, command arguments, logs, or evidence files.
The harness never writes their values. `MYSQL_CLUSTER_PASSWORD` must contain
16-128 ASCII letters, digits, dots, underscores, or hyphens so it can cross the
non-interactive AdminAPI and SQL boundaries without unsafe quoting.

```bash
read -rsp 'Releem dev API key: ' RELEEM_API_KEY; echo
export RELEEM_API_KEY
read -rsp 'Ephemeral cluster admin password: ' MYSQL_CLUSTER_PASSWORD; echo
export MYSQL_CLUSTER_PASSWORD
export GCP_TOPOLOGY_RUN_LABEL="task12-$(date -u +%Y%m%d)"
```

Persistence checks also need runtime-only dev database credentials:

```bash
export TOPOLOGY_MYSQL_HOST=...
export TOPOLOGY_MYSQL_PORT=3306
export TOPOLOGY_MYSQL_USER=...
read -rsp 'Dev MySQL password: ' TOPOLOGY_MYSQL_PASSWORD; echo
export TOPOLOGY_MYSQL_PASSWORD
export TOPOLOGY_MYSQL_DATABASE=releemdb_dev

export TOPOLOGY_CLICKHOUSE_URL='https://...:8443/'
export TOPOLOGY_CLICKHOUSE_USER=...
read -rsp 'Dev ClickHouse password: ' TOPOLOGY_CLICKHOUSE_PASSWORD; echo
export TOPOLOGY_CLICKHOUSE_PASSWORD
export TOPOLOGY_CLICKHOUSE_DATABASE=releemdb_dev
```

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
ClickHouse observation for every exact SID. Sanitized MySQL/JSON evidence is
stored under `/tmp/releem-db-topology-evidence/gcp/` with mode `0600` defaults.
Addresses and credentials are excluded from the evidence and inventory output.

The VMs are intentionally left running after validation. Teardown is available
for a later explicit request and is bounded by both ownership checks and a
second confirmation variable:

```bash
export GCP_TOPOLOGY_CONFIRM_DESTROY="$GCP_TOPOLOGY_RUN_LABEL"
tests/topology/gcp_innodb_cluster.sh destroy
```

Never run `destroy` as routine test cleanup.
