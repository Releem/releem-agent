# AWS topology facts, version 1

The AWS feature branches extend DB.TopologyFacts.Version=1 with AWS.Version=1.
Agent collects allowlisted API observations. Platform interprets them in
src/v2/topology_aws_facts.py, called by topology_facts.build_db_topology before
PersistDbTopology, SQL relation/upstream persistence and ClickHouse observations.

## Wire object

AWS contains Version, Target, Cluster, GlobalCluster, Instances and Sources.
The complete typed field allowlist is awsrds/raw_facts.go in Agent. The five
JSON fixtures in awsrds/testdata/facts are byte-identical to Platform
tests/fixtures/aws_topology_facts and are asserted against actual Go serialization.

- Target/Instances: DBInstanceIdentifier, DBInstanceArn, DbiResourceId,
  DBInstanceClass, Engine, DBInstanceStatus, DBClusterIdentifier, Endpoint
  (address string), Port, MultiAZ, ReadReplicaSourceDBInstanceIdentifier,
  ReadReplicaDBInstanceIdentifiers.
- Cluster: DBClusterIdentifier, DBClusterArn, DbClusterResourceId, Engine,
  EngineMode, Endpoint, ReaderEndpoint, DBClusterParameterGroup,
  GlobalClusterIdentifier, GlobalWriteForwardingStatus, DBClusterMembers
  (DBInstanceIdentifier, IsClusterWriter, PromotionTier,
  DBClusterParameterGroupStatus), ServerlessV2ScalingConfiguration
  (MinCapacity, MaxCapacity, SecondsUntilAutoPause).
- GlobalCluster: GlobalClusterIdentifier, GlobalClusterArn,
  GlobalClusterResourceId, GlobalClusterMembers (DBClusterArn, IsWriter,
  Readers, SynchronizationStatus, GlobalWriteForwardingStatus).
- Sources: Target, Cluster, GlobalCluster, Peers, Source, Children.
  Each value is ok, unsupported or error.

Field names follow SDK/API spelling (Arn, DbiResourceId); no whole SDK objects,
tags, usernames, credentials, parameter values or raw SDK errors are serialized.
Optional booleans/numbers use JSON null for unknown, preserving false and zero.
Variables/Status remain at DB.Conf.Variables and DB.Metrics.Status.

ok means the applicable API lookup completed. unsupported means the applicable
source has no declared relationship. error means a failed lookup. Instances keeps
successful peers even when another lookup fails. An inaccessible Cluster also
marks Peers/GlobalCluster error because absence was not established.
Agent keeps pagination, response identity validation and ARN-region routing.
Only configured target lookup/essential metadata failures fail startup; optional
cluster, peer, source, child and global failures preserve target monitoring.

## Freshness and ordering

AttachReportMetadata's fresh flag describes this report's refresh, separately
from per-source completeness. A fresh partial report preserves successful local
sources. Cached/failed-refresh reports clamp all source statuses to error.
MetadataSnapshot's complete flag is false if any optional source failed.
Report DTOs are deep-copied. SQL collection preserves a preexisting AWS block;
provider collection preserves native SQL facts. Both collection orders are tested.

## Platform semantics

Stable resource IDs form provider group keys; full normalized ARNs form member
identities and global-cluster parent edges. Bare instance references resolve only
in the declaring account/partition/region. Platform rejects mismatched identifiers,
duplicate members, conflicting peer clusters, invalid global ARNs/readers, unknown
global roles and nonreciprocal ordinary RDS source/child relationships. Invalid
optional evidence leaves valid local relations available with incomplete status.

Aurora regional primary remains Role=primary on a global secondary, but IsWriter
is false and ReadOnly true. Forwarding is a separate fact and never establishes
autonomous writer availability. Unknown global role or inaccessible global API
cannot establish a writer. The restriction also applies to the native engine
projection. SQL status is not used to override provider write restrictions.
Instance availability only establishes reader availability; ordinary RDS replication
state remains unknown without SQL evidence.

Classic Multi-AZ uses one addressable relation and ManagedStandby=true; it does
not invent a standby host/member. Multi-AZ DB clusters use rds_multi_az rather than
aurora_cluster. Unknown MultiAZ cannot authorize removal of an existing relation.
Serverless v2 preserves scaling configuration, including zero minimum capacity.

Complete native and AWS evidence emits canonical Relations. Source errors,
inconsistent/missing identities or declared peers, unknown fields needed to prove
absence, and invalid/absent native facts emit only Facts.Relations; missing
memberships cannot be deleted. Provider-only PostgreSQL reports are supported
without inventing native PostgreSQL topology and remain conservatively partial.
Legacy DB.Topology payloads remain compatible. Unknown outer/AWS versions never
fall back to stale accompanying topology or authorize deletion.

## Verification

Agent: go test ./...; go vet ./...; bats tests/topology/aws_rds_topology.bats;
bash -n install.sh mysqlconfigurer.sh tests/topology/aws_rds_topology.sh.
Platform: project venv python -m unittest discover -s tests -p 'test_db_topology*.py'.
RELEEM_TOPOLOGY_TEST_SOCKET may point only to a disposable local test database.
No live AWS API calls, deployment, push, or customer database mutations are required.
