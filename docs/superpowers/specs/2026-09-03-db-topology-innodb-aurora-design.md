# InnoDB Cluster and Aurora/RDS Topology Design

## Status

Approved in chat on 2026-09-03. This document defines the cross-repository
contract for Releem Agent and Releem Platform.

## Objective

Extend database topology discovery and persistence so Releem can model:

- MySQL InnoDB Cluster in single-primary and multi-primary mode;
- MySQL InnoDB ClusterSet, including cluster-to-cluster replication;
- Aurora MySQL provisioned and Serverless v2 clusters;
- Aurora Global Database across `us-east-1` and `us-west-2`;
- ordinary RDS MySQL read replicas and Multi-AZ deployments.

The result must preserve the existing topology payload and Platform current-state
projection while exposing every simultaneous topology membership in a normalized,
queryable form for later tuning and schema-analysis work.

## Non-goals

- MySQL Router deployment, configuration, or health monitoring.
- Creating synthetic server records for an RDS Multi-AZ standby that AWS does not
  expose as an addressable DB instance.
- Aurora PostgreSQL topology.
- Applying tuning recommendations as part of this change.
- Keeping paid AWS test resources after validation.

## Chosen Architecture

Use one additive relation contract for database-native and provider-native
topologies. Do not create provider-specific Platform tables.

The existing top-level `DB.Topology` fields remain the compatibility projection.
The Agent adds `DB.Topology.Relations`, an array whose entries use the same field
vocabulary as the top-level topology. During one compatibility release, the Agent
also mirrors this array to `DB.Topology.Facts.Relations`; Platform accepts the new
location first and falls back to the legacy location.

One server may therefore report several relations at once. For example, an
InnoDB ClusterSet member can report `group_replication`, `innodb_cluster`,
`innodb_clusterset`, and `async_replication` without discarding any relation.

## Relation Contract

Every relation contains:

| Field | Meaning |
| --- | --- |
| `Type` | Stable relation type. |
| `Role` | Member role within this relation. |
| `GroupKey` | Stable identity shared by members of the same scope. |
| `MemberKey` | Stable identity of the reporting member. |
| `MemberHost`, `MemberPort` | Address of the reporting member when addressable. |
| `PrimaryMemberKey`, `PrimaryHost`, `PrimaryPort` | Unique primary when one exists; null otherwise. |
| `ParentGroupKey` | Parent topology scope, such as a ClusterSet or Global Database. |
| `IsWriter`, `IsReader` | Availability derived from current state, never inferred optimistically. |
| `ReplicationState` | `healthy`, `lagging`, `stopped`, `error`, or `unknown`. |
| `ReplicationLagSeconds` | Known lag or null. |
| `Facts` | Type-specific structured facts needed for diagnosis. |

Supported `Type` values are:

- `group_replication`
- `innodb_cluster`
- `innodb_clusterset`
- `async_replication`
- `aurora_cluster`
- `aurora_global_database`
- `rds_read_replica`
- `rds_multi_az`

Identities are namespaced and bounded to 255 characters. Prefer immutable IDs or
ARNs; if a provider value exceeds the limit, use the existing SHA-256 shortening
rule and retain the original value in `Facts`.

## MySQL InnoDB Cluster Discovery

The existing Group Replication collector remains the source of live member state,
roles, primary identity, read/write availability, and physical endpoints.

An InnoDB metadata adapter reads `mysql_innodb_cluster_metadata` non-fatally. It
supports metadata schema 2.x directly and schema 1.x through an explicit adapter.
The adapter determines the installed metadata generation from schema/table/column
introspection before issuing version-specific structured queries.

The adapter collects:

- cluster ID and cluster name;
- instance ID, label, address, and server UUID;
- single-primary or multi-primary mode;
- ClusterSet ID/name when present;
- ClusterSet primary/replica-cluster role;
- cluster-to-cluster replication channel and state.

`innodb_cluster.GroupKey` uses the metadata cluster ID. `innodb_clusterset.GroupKey`
uses the ClusterSet ID. Group Replication UUID remains the key for the physical
`group_replication` relation. Metadata absence or denied access degrades only the
semantic relations; physical Group Replication discovery continues.

Linux and Windows installers add a non-fatal read grant for
`mysql_innodb_cluster_metadata` while retaining the existing Performance Schema
grant. Installer failure to grant metadata access is logged but does not break
installation against a server where the metadata schema does not yet exist.

## AWS Aurora and RDS Discovery

AWS topology is authoritative for managed-service membership. Extend the existing
RDS discovery client with `DescribeGlobalClusters` and retain paginated results
from `DescribeDBInstances` and `DescribeDBClusters` needed to resolve related
members.

The metadata model includes:

- region, partition, account-safe ARN identities, engine, and engine mode;
- DB cluster ARN/identifier and all DB cluster members;
- writer flag, promotion tier, endpoint, and instance status;
- Serverless v2 scaling configuration and `db.serverless` membership;
- Global Cluster ARN/identifier, member cluster ARNs, writer cluster, and regions;
- RDS source/read-replica identifiers;
- classic RDS `MultiAZ` state.

Secrets, resource IDs classified as private, and endpoints continue to be excluded
from logs. Endpoints may appear in the metrics payload because Platform requires
them to connect known topology members, but credentials never appear.

### Aurora cluster

`aurora_cluster.GroupKey` uses the DB cluster ARN, falling back to a namespaced
region plus DB cluster identifier. Each addressable DB instance is a member.
`Role` is `primary` for the AWS writer and `replica` otherwise. Instance status
other than `available` is fail-closed for reader/writer availability.

Provisioned and Serverless v2 use the same relation type. `Facts` records engine
mode, instance class, Serverless v2 capacity bounds, promotion tier, and cluster
parameter group.

### Aurora Global Database

`aurora_global_database.GroupKey` uses the Global Cluster ARN. `MemberKey` remains
the reporting DB instance ARN so each SID keeps an addressable identity. Members of
the primary regional cluster have role `primary_cluster_member`; members of a
secondary regional cluster have role `replica_cluster_member`. `Facts` contains the
regional cluster ARN and region. A secondary relation has an edge from its regional
cluster ARN to the primary regional cluster ARN. The reporting DB instance retains
its Aurora-cluster membership as a separate relation.

### RDS read replicas

A replica reports `rds_read_replica` with a directed edge to its source DB instance.
A source with known read-replica identifiers reports the same group without
inventing replication health that AWS does not expose. SQL replication status may
add lag/state facts when available.

### RDS Multi-AZ

Classic Multi-AZ reports `rds_multi_az` with the source DB instance as the only
addressable member and `Facts.ManagedStandby=true`. No fake member key, SID, host,
or writer is created for the hidden standby. Multi-AZ DB clusters, when returned as
addressable cluster members by AWS, are represented using their actual instances.

AWS discovery failures do not erase the last known Platform topology. The Agent
reports an `unknown` provider relation only when it still has a stable group/member
identity; otherwise it omits that relation and logs an actionable error.

## Platform Persistence

Keep these compatibility tables and behavior:

- `db_topology_groups`
- `db_topology_members`
- `db_topology_upstreams`

Add `db_topology_relation_members` for simultaneous memberships:

```sql
CREATE TABLE db_topology_relation_members (
    group_id bigint unsigned NOT NULL,
    sid int unsigned NOT NULL,
    relation_type varchar(64) NOT NULL,
    group_key varchar(255) NOT NULL,
    parent_group_key varchar(255) DEFAULT NULL,
    member_key varchar(255) NOT NULL,
    member_host varchar(255) DEFAULT NULL,
    member_port int DEFAULT NULL,
    primary_member_key varchar(255) DEFAULT NULL,
    primary_host varchar(255) DEFAULT NULL,
    primary_port int DEFAULT NULL,
    role varchar(64) NOT NULL,
    is_writer tinyint(1) NOT NULL DEFAULT 0,
    is_reader tinyint(1) NOT NULL DEFAULT 0,
    read_only tinyint(1) NOT NULL DEFAULT 0,
    super_read_only tinyint(1) NOT NULL DEFAULT 0,
    replication_state varchar(64) NOT NULL DEFAULT 'unknown',
    replication_lag_seconds int DEFAULT NULL,
    last_rid varchar(255) DEFAULT NULL,
    last_seen_timestamp int DEFAULT NULL,
    last_seen_epoch_ms bigint DEFAULT NULL,
    facts longtext CHARACTER SET utf8mb4 COLLATE utf8mb4_bin,
    created_at datetime NOT NULL DEFAULT current_timestamp(),
    updated_at datetime NOT NULL DEFAULT current_timestamp()
        ON UPDATE current_timestamp(),
    PRIMARY KEY (sid, relation_type, group_key),
    KEY idx_topology_relation_group (group_id, relation_type, group_key),
    KEY idx_topology_relation_parent (parent_group_key),
    CONSTRAINT fk_topology_relation_group FOREIGN KEY (group_id)
        REFERENCES db_topology_groups(id) ON DELETE CASCADE,
    CONSTRAINT fk_topology_relation_server FOREIGN KEY (sid)
        REFERENCES servers(sid) ON DELETE CASCADE,
    CHECK (json_valid(facts))
);
```

Each relation gets or reuses a row in `db_topology_groups`. Persistence upserts all
relation memberships in the same MySQL transaction as the compatibility projection.
Rows for the reporting SID that are absent from a newer complete snapshot are
deleted only after all replacement rows are written. Existing millisecond stale
update guards apply to every upsert and deletion.

`db_topology_upstreams` continues to store directed replication channels. ClusterSet
and Aurora Global Database parent edges receive stable channel keys and provider
facts. Tenant scoping by `uid` is mandatory on every read path.

The ClickHouse local and distributed observation tables gain a `relations String`
column containing canonical JSON. Existing 30-day TTL remains unchanged.

`vw_servers` retains existing columns and adds canonical provider/semantic fields:

- `topology_semantic_type`
- `topology_semantic_group_key`
- `topology_parent_group_key`
- `topology_semantic_role`

The canonical semantic relation priority is Aurora Global Database, ClusterSet,
Aurora cluster, InnoDB Cluster, then the existing physical topology. Full relation
lists are returned by a dedicated tenant-scoped DB method rather than aggregated
into the view.

## Error Handling and Ordering

- All optional discovery queries are non-fatal and record their exact capability
  failure at debug level.
- Provider API throttling uses AWS SDK retry behavior and does not create false
  standalone topology.
- Unknown/offline members are not readers or writers.
- A relation snapshot is applied only when its `requestTimeEpoch` is not older than
  the stored `last_seen_epoch_ms`.
- ClickHouse failure does not roll back committed MySQL current state; it is logged
  with SID and RID.
- Payload values are bounded before SQL insertion; full originals remain only in
  JSON facts when allowed by the logging/privacy contract.

## Automated Testing

Agent tests cover:

- metadata schema 1.x and 2.x adapters;
- single-primary and multi-primary InnoDB Cluster;
- ClusterSet primary and replica clusters;
- concurrent Group Replication, InnoDB, ClusterSet, and async relations;
- Aurora provisioned and Serverless v2 writer/reader membership;
- Aurora Global Database primary/secondary relations;
- RDS read replica and classic Multi-AZ semantics;
- fail-closed state, bounded keys, redacted logs, and API failures;
- Linux and Windows non-fatal grants.

Platform tests cover:

- new and legacy relation locations;
- multiple memberships for one SID;
- tenant isolation and stale millisecond snapshots;
- relation removal only on a newer complete snapshot;
- ClusterSet and Aurora Global edges;
- ClickHouse canonical JSON and 30-day TTL;
- fresh-install and upgrade migrations;
- compatibility projection and `vw_servers` semantic priority.

## Live Infrastructure Validation

### GCP InnoDB Cluster matrix

Create twelve Ubuntu 22.04 x86-64 VMs in project
`static-mediator-400907`, using an available quota family and names prefixed
`releem-ic-`:

- `single-1..3`: one single-primary cluster with one writer and two secondaries;
- `multi-1..3`: one multi-primary cluster with three writers;
- `cs-primary-1..3`: ClusterSet primary cluster;
- `cs-replica-1..3`: ClusterSet replica cluster.

Install MySQL 8.4 and MySQL Shell from signed vendor repositories, create clusters
through MySQL Shell AdminAPI, install the requested dev Agent on every VM, replace
it with the worktree binary, and restart the service.

Validate member offline/online, primary election, multi-primary write availability,
ClusterSet replication stop/start, stable identities, MySQL current state, normalized
memberships/edges, and ClickHouse observations.

### AWS matrix

Use `us-east-1` as primary and `us-west-2` as secondary. Create uniquely prefixed,
tagged, temporary resources:

- provisioned Aurora MySQL cluster with writer and reader;
- Aurora MySQL Serverless v2 cluster with writer and reader;
- Aurora Global Database with one cluster in each region and at least one instance
  per regional cluster;
- ordinary RDS MySQL Multi-AZ source and one read replica.

Select the smallest engine-supported classes and capacity bounds available at test
time. Run the Agent externally in `aws/rds` mode for each addressable test instance.
Validate writer/reader discovery, failover, Serverless facts, Global Database edges,
read-replica source edges, and Multi-AZ managed-standby semantics in both Platform
stores.

Delete AWS instances, clusters, global clusters, subnet groups, security groups,
parameter groups, and temporary secrets after verification, including on test
failure. Retain only redacted logs and DB observation evidence. Leave GCP InnoDB
VMs running for follow-up testing unless the user requests teardown.

## Acceptance Criteria

1. Existing Agent and Platform topology tests remain green.
2. Every requested topology emits stable normalized relations without losing the
   existing physical topology.
3. Platform stores simultaneous memberships and directed edges tenant-safely.
4. MySQL current state and ClickHouse history both reflect each live state change
   and recovery.
5. InnoDB Cluster live tests pass for single-primary, multi-primary, and ClusterSet.
6. Aurora provisioned, Serverless v2, Global Database, RDS read replica, and Multi-AZ
   tests pass in AWS.
7. All temporary AWS resources are deleted and absence is verified by API inventory.
8. No credentials, private resource IDs, or passwords are added to Git or emitted in
   retained test evidence.
