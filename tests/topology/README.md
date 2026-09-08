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

## AWS Aurora and RDS matrix

`aws_rds_topology.sh` is a single-run, destructive acceptance harness. It uses
`us-east-1` for the primary region and `us-west-2` for the secondary region. A
mandatory lowercase run ID owns every temporary resource through both a
deterministic `releem-<run-id>-` name and the exact
`releem-topology-run`/`releem-topology-managed` tags.

The read-only `preflight` command intersects both regions when selecting the
latest available Aurora MySQL 3 version and smallest common orderable class.
It also selects the current Serverless v2 minimum ACU, a current MySQL 8.0
version/class, and the regional Amazon Linux 2023 runner images. Its summary
records initial and transition Serverless maximum ACU, the selected ordinary
MySQL version/class, three 20 GiB ordinary database volumes, two 8 GiB runner
roots, the one-second monitoring interval, all resource counts, and the global
runtime ceiling. Exact regional
RDS instance/cluster and standard EC2 vCPU use is checked against the matrix's
required headroom. Quotas, private subnet routes, deterministic-name collisions
independent of tags, and the bounded hourly configuration shape are
written under `/tmp/releem-db-topology-evidence/aws/<run-id>/preflight/`.
Review `summary.json`, both quota files, and `inventory-before.json` before
authorizing a live run. The cost shape is 7 provisioned Aurora instances, 3
Serverless v2 instances, 3 ordinary RDS instances, and 2 `t3.micro` runners,
plus storage, I/O, Enhanced Monitoring logs, S3, and network egress. AWS prices
are not stable, so preflight records selected billable units instead of a
hard-coded currency estimate.

The operator supplies two existing VPCs and at least two private subnets in
different availability zones per region. Subnets must not auto-assign public
addresses. Every selected runner subnet must have an active NAT default route,
because the runner needs outbound access to the public Releem endpoint in
addition to SSM, S3, and AWS APIs. The harness creates one no-ingress SSM runner security
group and one runner-only MySQL security group in each VPC. Both EC2 runners
have no public address, encrypted `gp3` root volumes, IMDSv2 enforcement, and
no inbound rules.

```bash
export AWS_TOPOLOGY_RUN_ID=task13-20260908
export AWS_TOPOLOGY_EAST_VPC_ID=vpc-...
export AWS_TOPOLOGY_EAST_SUBNET_IDS=subnet-...,subnet-...
export AWS_TOPOLOGY_WEST_VPC_ID=vpc-...
export AWS_TOPOLOGY_WEST_SUBNET_IDS=subnet-...,subnet-...

tests/topology/aws_rds_topology.sh preflight
```

Load the Releem and Platform credentials into the invoking shell with tracing
disabled, using the same `TOPOLOGY_MYSQL_*`, `TOPOLOGY_CLICKHOUSE_*`, and exact
positive `TOPOLOGY_UID` contract documented above. Do not put credentials in
arguments, checked-in files, retained logs, or shell history. The harness
generates the database password in a mode-`0600` file under `/tmp`.

`run` creates this exact matrix and always invokes dependency-ordered cleanup
on success, assertion failure, `EXIT`, `INT`, `TERM`, or `HUP` after mutation
begins. `AWS_TOPOLOGY_RUNTIME_DEADLINE_SECONDS` defaults to four hours and must
remain between 10 minutes and six hours; expiry sends `TERM`, cleans once, and
exits with status 143. Cleanup has a separate one-hour shared deadline, with
the final ten minutes reserved for bounded dependency deletion attempts. All
automatic cleanup attempts reuse that same deadline; after exhaustion the
harness returns nonzero and requires exact-confirmed `destroy`. The advertised
default maximum is therefore five hours:

| Topology | Region and members |
| --- | --- |
| Aurora provisioned | `us-east-1`, one writer and two readers |
| Aurora Serverless v2 | `us-east-1`, one writer and two readers |
| Aurora Global Database | two members in each region |
| RDS MySQL replication | `us-east-1`, one source and one read replica |
| RDS MySQL Multi-AZ | `us-east-1`, one addressable instance and managed standby |

Every database is storage-encrypted and has `PubliclyAccessible=false`.
Every addressable DB instance uses a temporary RDS Enhanced Monitoring role at
a one-second interval. Before Agent startup, the harness requires at least one
event in the exact `RDSOSMetrics/<DBInstanceResourceID>` stream. The runner
role permits only SSM management, reads of its exact private S3 run prefix,
`logs:GetLogEvents` for `RDSOSMetrics`, and `rds:Describe*`.
The harness initializes an exact 13-instance monitoring manifest before cloud
mutation. Every deterministic instance starts as `planned`; immediately before
an AWS create request it becomes `create_attempted`, and a successful response
or describe stores `created_with_resource_id` plus the resource ID atomically.
Untouched `planned` entries need no monitoring stream. Cleanup retries IDs for
attempted entries but never deletes an instance whose ID is not durably
recorded. Three settled, exact `DBInstanceNotFound` responses may instead mark
an attempted entry `confirmed_absent`; ambiguous, transient, or single absence
responses leave it unresolved. In that case cleanup removes independent
runners and private delivery resources, returns nonzero, and preserves DB
clusters, database network/parameter dependencies, and the monitoring role so
an exact-confirmed later `destroy` can recover safely. IDs remain in the
manifest after database deletion for exact log-stream deletion and absence
proof. Terminal success requires empty AWS inventory, no unresolved attempts,
unique IDs for every created instance with its stream absent, and only
unattempted or confirmed-absent entries otherwise. Legacy blank manifests are
migrated fail-closed as attempted rather than assumed never created.

The reviewed `/tmp/releem-agent-db-topology-x86_64` binary and mode-`0600`
Agent/MySQL configuration archives are transported as server-side-encrypted
private S3 objects. A one-day lifecycle is installed before upload, and each
secret-bearing `configs.tar` object is independently retried to confirmed
absence immediately after its runner bootstraps; only the non-secret binary
may remain until cleanup. Generated credentials otherwise remain in local and
runner-local mode-`0600` files and are deleted during cleanup. SSM commands
contain only fixed object paths and resource names;
API keys and database passwords are never command parameters or command
output. SSM CloudWatch and S3 command output are disabled. Agent processes run
inside their regional VPC while remaining external to the database hosts.

```bash
tests/topology/aws_rds_topology.sh run
```

The harness verifies provisioned failover, a Serverless v2 capacity change and
bounded load event using before/during/after `ServerlessDatabaseCapacity` and
`ACUUtilization` CloudWatch evidence, a managed Global Database switchover, read-replica SQL stop/start,
and Multi-AZ failover. Persistence queries first resolve the exact test SIDs,
then use only tenant UID, SID, current RID, and millisecond transition markers.
Assertions map each exact SID back to AWS instance/cluster/global resource IDs,
ARNs, endpoints, writers, and sources. Every provider row is matched exactly,
including parent, primary, role, writer/reader flags, and replication state;
unlisted provider or native rows are rejected. A validated baseline identity
map generates one exact native relation for every one of the 13 SIDs and
transition-specific writer/reader/state expectations. The complete upstream
set contains the ordinary replica channel/source/state plus one deterministic
Platform-synthesized relation channel for every Aurora Global Database instance
in the secondary regional cluster. Those channels use the exact relation/group
digest and primary regional cluster ARN; their direction reverses after the
managed switchover. Missing or extra channels fail. ClickHouse snapshots
parse primary/source edges only from the persisted ClickHouse relations JSON,
label that evidence as ClickHouse-sourced, and compare it with current MySQL
state without hostname inference or `LIMIT 1 BY`.

Cleanup first stops and disarms the watchdog, masks/defer signals, and disables
errexit internally. It remains best-effort after individual failures and is
retryable unless terminal absence succeeds. It stops Agent processes, deletes DB instances and Enhanced Monitoring
streams, detaches Global Database members, then removes regional clusters,
global cluster, parameter groups, runners/ENIs, S3 objects/bucket, instance
profile, inline/managed IAM policies, IAM roles, security groups, subnet
groups, snapshots, SSM local artifacts, and secret files. It polls both regions
until the combined run inventory is empty. AWS retains completed SSM command
history as an account audit record; the API has no delete operation for that
record, but no command output or credentials are stored in it.
AWS API calls during cleanup have short per-call timeouts and waiters consume
only the remaining shared wait budget. Explicit absence classification accepts
only standard operation-specific AWS CLI service-error envelopes and rejects
ambiguous wrapper, proxy, timeout, or authorization text. S3 object deletion
retries and their sleeps also stop at the shared cleanup deadline.

Only an untrappable termination such as `SIGKILL` can bypass the EXIT trap.
Recover by confirming the exact run ID. `destroy` is only for the same single
run, inventories deterministic names independently of tags, and verifies exact
resource tags plus S3/IAM policy, role, and instance-profile relationships
before its first deletion. Missing or foreign ownership fails closed; recovery
never adopts a same-named resource:

```bash
export AWS_TOPOLOGY_CONFIRM_DESTROY="$AWS_TOPOLOGY_RUN_ID"
tests/topology/aws_rds_topology.sh destroy
```
