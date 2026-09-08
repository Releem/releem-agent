#!/usr/bin/env bash
set -euo pipefail

readonly PRIMARY_REGION="us-east-1"
readonly SECONDARY_REGION="us-west-2"
readonly TAG_RUN_KEY="releem-topology-run"
readonly TAG_MANAGED_KEY="releem-topology-managed"
readonly STATE_ROOT="${AWS_TOPOLOGY_STATE_DIR:-/tmp/releem-db-topology-state/aws}"
readonly EVIDENCE_ROOT="${AWS_TOPOLOGY_EVIDENCE_DIR:-/tmp/releem-db-topology-evidence/aws}"
readonly AGENT_BINARY="/tmp/releem-agent-db-topology-x86_64"
readonly POLL_SECONDS="${AWS_TOPOLOGY_POLL_SECONDS:-20}"
readonly WAIT_TIMEOUT_SECONDS="${AWS_TOPOLOGY_WAIT_TIMEOUT_SECONDS:-3600}"
readonly PERSISTENCE_TIMEOUT_SECONDS="${TOPOLOGY_PERSISTENCE_TIMEOUT_SECONDS:-600}"
readonly CLICKHOUSE_MARKER_WINDOW_SECONDS="${TOPOLOGY_CLICKHOUSE_MARKER_WINDOW_SECONDS:-900}"

CLEANUP_ARMED=false
CLEANUP_RUNNING=false
AGENT_PIDS=()

die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
log() { printf '[%s] %s\n' "$(date -u +%FT%TZ)" "$*" >&2; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }

usage() {
    cat <<'EOF'
Usage: tests/topology/aws_rds_topology.sh COMMAND

Commands:
  preflight  Read-only selection, quota, network, collision, and cost-shape inventory
  run        Preflight, create, validate transitions, and always clean up
  inventory  Print sanitized run-owned inventory from both regions
  destroy    Recover from an untrappable termination; exact confirmation required
  help       Show this help

Required for all cloud commands:
  AWS_TOPOLOGY_RUN_ID

Required for run:
  AWS_TOPOLOGY_EAST_VPC_ID, AWS_TOPOLOGY_EAST_SUBNET_IDS
  AWS_TOPOLOGY_WEST_VPC_ID, AWS_TOPOLOGY_WEST_SUBNET_IDS
  RELEEM_API_KEY and the TOPOLOGY_MYSQL_*/TOPOLOGY_CLICKHOUSE_*/TOPOLOGY_UID values

Subnet lists are comma-separated private subnet IDs spanning at least two AZs.
The selected private subnets must provide outbound access to SSM, S3, Releem,
and AWS APIs through NAT or VPC endpoints. No inbound runner access is used.

Set AWS_TOPOLOGY_CONFIRM_DESTROY to the exact run ID before destroy.
No AWS mutation is performed by preflight or inventory.
EOF
}

run_id() { printf '%s' "${AWS_TOPOLOGY_RUN_ID:-}"; }

validate_run_id() {
    local value
    value="$(run_id)"
    [[ "$value" =~ ^[a-z][a-z0-9-]{2,31}$ ]] ||
        die "AWS_TOPOLOGY_RUN_ID must match ^[a-z][a-z0-9-]{2,31}$"
}

resource_name() {
    local suffix="$1" value
    [[ "$suffix" =~ ^[a-z0-9][a-z0-9-]{0,31}$ ]] || die "invalid resource suffix"
    value="releem-$(run_id)-${suffix}"
    ((${#value} <= 63)) || die "generated resource name exceeds 63 characters"
    printf '%s\n' "$value"
}

state_dir() { printf '%s/%s\n' "$STATE_ROOT" "$(run_id)"; }
evidence_dir() { printf '%s/%s\n' "$EVIDENCE_ROOT" "$(run_id)"; }
selection_file() { printf '%s/selection.env\n' "$(state_dir)"; }
secret_file() { printf '%s/runtime-secret.env\n' "$(state_dir)"; }

aws_region() {
    local region="$1"
    shift
    aws --no-cli-pager --region "$region" "$@"
}

tags_args() {
    printf 'Key=%s,Value=%s\n' "$TAG_RUN_KEY" "$(run_id)"
    printf 'Key=%s,Value=true\n' "$TAG_MANAGED_KEY"
}

select_latest_aurora_version() {
    jq -er '
      [.DBEngineVersions[] |
       select((.Status // "available") == "available") |
       select(.EngineVersion | test("^8[.]0[.]mysql_aurora[.]3[.][0-9]+[.][0-9]+$")) |
       .EngineVersion as $v |
       ($v | capture("aurora[.]3[.](?<minor>[0-9]+)[.](?<patch>[0-9]+)$")) as $n |
       {version:$v, minor:($n.minor|tonumber), patch:($n.patch|tonumber)}]
      | sort_by(.minor,.patch) | last | .version
    ' "$1"
}

select_latest_common_aurora_version() {
    jq -ser '
      ([.[0].DBEngineVersions[]|select((.Status//"available")=="available")|.EngineVersion]) as $east |
      ([.[1].DBEngineVersions[]|select((.Status//"available")=="available")|.EngineVersion]) as $west |
      [$east[] | select(. as $v | ($west|index($v)) != null) |
       select(test("^8[.]0[.]mysql_aurora[.]3[.][0-9]+[.][0-9]+$")) |
       . as $v | (capture("aurora[.]3[.](?<minor>[0-9]+)[.](?<patch>[0-9]+)$")) as $n |
       {version:$v,minor:($n.minor|tonumber),patch:($n.patch|tonumber)}] |
      sort_by(.minor,.patch)|last|.version
    ' "$1" "$2"
}

select_smallest_orderable_class() {
    jq -er '
      def size_rank:
        if . == "micro" then 0 elif . == "small" then 1 elif . == "medium" then 2
        elif . == "large" then 3 elif test("^xlarge$") then 4
        elif test("^[0-9]+xlarge$") then (capture("^(?<n>[0-9]+)xlarge$").n|tonumber)+4
        else 1000 end;
      [.OrderableDBInstanceOptions[].DBInstanceClass | select(startswith("db.")) |
       . as $class | (split(".")[-1] | size_rank) as $size |
       {class:$class,size:$size}] | sort_by(.size,.class) | first | .class
    ' "$1"
}

select_smallest_common_orderable_class() {
    jq -ser '
      def size_rank:
        if . == "micro" then 0 elif . == "small" then 1 elif . == "medium" then 2
        elif . == "large" then 3 elif . == "xlarge" then 4
        elif test("^[0-9]+xlarge$") then (capture("^(?<n>[0-9]+)xlarge$").n|tonumber)+4
        else 1000 end;
      ([.[0].OrderableDBInstanceOptions[].DBInstanceClass]|unique) as $east |
      ([.[1].OrderableDBInstanceOptions[].DBInstanceClass]|unique) as $west |
      [$east[]|select(. as $c|($west|index($c)) != null)|. as $class|
       {class:$class,size:($class|split(".")[-1]|size_rank)}] | sort_by(.size,.class)|first|.class
    ' "$1" "$2"
}

select_min_serverless_acu() {
    local file="$1" version="$2"
    jq -er --arg version "$version" '
      .DBEngineVersions[] | select(.EngineVersion == $version) |
      .ServerlessV2FeaturesSupport.MinCapacity |
      select(type == "number" and . > 0)
    ' "$file"
}

assert_inventory_owned() {
    local file="$1" run
    run="$(run_id)"
    jq -e --arg run "$run" --arg run_key "$TAG_RUN_KEY" --arg managed_key "$TAG_MANAGED_KEY" '
      all(.[]; .tags[$run_key] == $run and .tags[$managed_key] == "true")
    ' "$file" >/dev/null || die "matching resource has foreign or incomplete ownership tags"
}

plan_missing_resources() {
    local desired="$1" existing="$2"
    awk 'NR==FNR {present[$0]=1; next} !present[$0]' "$existing" "$desired"
}

cleanup_plan() {
    jq -r '
      (.ssm_artifacts[]? | "ssm-artifact|"+.),
      (.runners[]? | "runner|"+.),
      (.enis[]? | "eni|"+.),
      (.instances[]? | "instance|"+.),
      (.monitoring_streams[]? | "monitoring-stream|"+.),
      (.snapshots[]? | "snapshot|"+.),
      (.clusters[]? | "cluster|"+.),
      (.global_clusters[]? | "global-cluster|"+.),
      (.parameter_groups[]? | "parameter-group|"+.),
      (.s3_objects[]? | "s3-object|"+.),
      (.s3_buckets[]? | "s3-bucket|"+.),
      (.instance_profiles[]? | "instance-profile|"+.),
      (.iam_policies[]? | "iam-policy|"+.),
      (.iam_roles[]? | "iam-role|"+.),
      (.security_groups[]? | "security-group|"+.),
      (.subnet_groups[]? | "subnet-group|"+.),
      "secret-file|runtime"
    ' "$1"
}

assert_runner_launch_spec() {
    jq -e '
      .ImageId != null and .InstanceType != null and .IamInstanceProfile.Name != null and
      (.NetworkInterfaces|length)==1 and
      .NetworkInterfaces[0].AssociatePublicIpAddress==false and
      (.NetworkInterfaces[0].Groups|length)==1 and
      (.BlockDeviceMappings|length)>=1 and all(.BlockDeviceMappings[]; .Ebs.Encrypted==true and .Ebs.DeleteOnTermination==true) and
      .MetadataOptions.HttpTokens=="required" and .MetadataOptions.HttpEndpoint=="enabled"
    ' "$1" >/dev/null || die "runner launch specification is not private and hardened"
}

assert_runner_security_group() {
    jq -e '
      (.SecurityGroups|length)==1 and
      (.SecurityGroups[0].IpPermissions|length)==0 and
      (.SecurityGroups[0].IpPermissionsEgress|length)>0
    ' "$1" >/dev/null || die "runner security group must have no inbound rules and outbound connectivity"
}

assert_database_security_group() {
    local file="$1" runner_sg="$2"
    jq -e --arg runner "$runner_sg" '
      (.SecurityGroups|length)==1 and
      (.SecurityGroups[0].IpPermissions|length)==1 and
      (.SecurityGroups[0].IpPermissions[0] |
        .IpProtocol=="tcp" and .FromPort==3306 and .ToPort==3306 and
        (.UserIdGroupPairs|length)==1 and .UserIdGroupPairs[0].GroupId==$runner and
        ((.IpRanges//[])|length)==0 and ((.Ipv6Ranges//[])|length)==0 and ((.PrefixListIds//[])|length)==0) and
      (.SecurityGroups[0].IpPermissionsEgress|length)>0
    ' "$file" >/dev/null || die "database security group is broader than runner-only MySQL"
}

assert_security_group_owned() {
    jq -e --arg run "$(run_id)" --arg run_key "$TAG_RUN_KEY" --arg managed_key "$TAG_MANAGED_KEY" '
      (.SecurityGroups|length)==1 and
      (.SecurityGroups[0].Tags|map({key:.Key,value:.Value})|from_entries) as $t |
      $t[$run_key]==$run and $t[$managed_key]=="true"
    ' "$1" >/dev/null || die "security group ownership mismatch"
}

runner_policy_document() {
    local object_arn="$1"
    jq -n --arg object_arn "$object_arn" '{
      Version:"2012-10-17",
      Statement:[
        {Sid:"ReadRunObjects",Effect:"Allow",Action:["s3:GetObject"],Resource:[$object_arn]},
        {Sid:"ReadEnhancedMonitoring",Effect:"Allow",Action:["logs:GetLogEvents"],Resource:["arn:aws:logs:*:*:log-group:RDSOSMetrics:log-stream:*"]},
        {Sid:"DescribeRDS",Effect:"Allow",Action:["rds:Describe*"],Resource:["*"]}
      ]
    }'
}

assert_runner_policy() {
    jq -e '
      ([.Statement[].Action[]] | sort) == (["logs:GetLogEvents","rds:Describe*","s3:GetObject"] | sort) and
      all(.Statement[]; .Effect=="Allow") and
      ([.Statement[]|select(.Action|index("s3:GetObject"))|.Resource[]]|all(test("^arn:aws:s3:::[^*]+/.+[/*]$")))
    ' "$1" >/dev/null || die "runner policy exceeds the required read-only permissions"
}

runner_ssm_commands() {
    local region="$1" bucket="$2" prefix="$3"
    [[ "$region" == "$PRIMARY_REGION" || "$region" == "$SECONDARY_REGION" ]] || die "invalid runner region"
    [[ "$bucket" =~ ^[a-z0-9.-]{3,63}$ && "$prefix" =~ ^[a-zA-Z0-9._/-]+$ ]] || die "invalid private object path"
    cat <<EOF
set -eu
umask 077
install -d -m 0700 /tmp/releem-topology
dnf install -y mariadb105 >/dev/null
aws s3 cp s3://${bucket}/${prefix}/agent /tmp/releem-topology/agent --region ${PRIMARY_REGION} --only-show-errors
aws s3 cp s3://${bucket}/${prefix}/configs.tar /tmp/releem-topology/configs.tar --region ${PRIMARY_REGION} --only-show-errors
chmod 0700 /tmp/releem-topology/agent
tar -xf /tmp/releem-topology/configs.tar -C /tmp/releem-topology
find /tmp/releem-topology -name '*.conf' -exec chmod 0600 {} \;
chmod 0600 /tmp/releem-topology/mysql.cnf
for config in /tmp/releem-topology/configs/*.conf; do /tmp/releem-topology/agent -config \"\$config\" >>/tmp/releem-topology/agent.log 2>&1 & done
EOF
}

monitoring_arguments() {
    local role_arn="$1"
    [[ "$role_arn" =~ ^arn:aws:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]+$ ]] || die "invalid monitoring role ARN"
    printf '%s\n' --monitoring-interval 1 --monitoring-role-arn "$role_arn"
}

assert_monitoring_events() {
    local file="$1" resource_id="$2"
    [[ "$resource_id" =~ ^db-[A-Za-z0-9]+$ ]] || die "invalid DB instance resource ID"
    jq -e '.events|length>0 and all(.[]; (.timestamp|type)=="number" and (.message|type)=="string" and (.message|length)>0)' \
        "$file" >/dev/null || die "Enhanced Monitoring stream has no events"
}

assert_db_instance_safety() {
    local file="$1" expected="$2" role_arn="$3"
    jq -e --argjson expected "$expected" --arg role "$role_arn" '
      (.DBInstances|length)==$expected and
      all(.DBInstances[];
        .PubliclyAccessible==false and .StorageEncrypted==true and
        .MonitoringInterval==1 and .MonitoringRoleArn==$role)
    ' "$file" >/dev/null || die "DB instances are not private, encrypted, and Enhanced Monitoring enabled"
}

assert_inventory_empty() {
    jq -e 'to_entries | all(.value | length == 0)' "$1" >/dev/null ||
        die "run-owned AWS resources remain after cleanup"
}

assert_inventory_matches() {
    jq -e -n --slurpfile before "$1" --slurpfile after "$2" '
      def canonical: to_entries|sort_by(.key)|map(.value|=sort)|from_entries;
      ($before[0]|canonical)==($after[0]|canonical)
    ' >/dev/null || die "post-cleanup inventory differs from preflight baseline"
}

validate_sid_list() {
    [[ "$1" =~ ^[1-9][0-9]*(,[1-9][0-9]*)*$ ]] || die "invalid SID list"
}

validate_persistence_identity() {
    [[ "${TOPOLOGY_UID:-}" =~ ^[1-9][0-9]{0,9}$ ]] || die "TOPOLOGY_UID must be a positive integer"
    ((${TOPOLOGY_UID} <= 4294967295)) || die "TOPOLOGY_UID exceeds UInt32"
}

current_state_query() {
    local sids="$1" marker_ms="$2"
    validate_persistence_identity
    validate_sid_list "$sids"
    [[ "$marker_ms" =~ ^[1-9][0-9]{12}$ ]] || die "invalid millisecond marker"
    printf '%s\n' "SELECT JSON_OBJECT('sid',r.sid,'last_rid',r.last_rid,'relation_type',r.relation_type,'group_key',r.group_key,'parent_group_key',r.parent_group_key,'member_key',r.member_key,'primary_member_key',r.primary_member_key,'role',r.role,'is_writer',r.is_writer,'is_reader',r.is_reader,'replication_state',r.replication_state,'replication_lag_seconds',r.replication_lag_seconds,'last_seen_epoch_ms',r.last_seen_epoch_ms,'serverless_min_acu',JSON_EXTRACT(r.facts,'$.ServerlessV2MinCapacity'),'serverless_max_acu',JSON_EXTRACT(r.facts,'$.ServerlessV2MaxCapacity'),'managed_standby',JSON_EXTRACT(r.facts,'$.ManagedStandby')) FROM db_topology_relation_members r JOIN servers s ON s.sid=r.sid WHERE s.uid = ${TOPOLOGY_UID} AND r.uid = ${TOPOLOGY_UID} AND r.sid IN (${sids}) AND r.last_seen_epoch_ms >= ${marker_ms} ORDER BY r.sid,r.relation_type,r.group_key;"
}

upstream_state_query() {
    local sids="$1" marker_ms="$2"
    validate_persistence_identity
    validate_sid_list "$sids"
    [[ "$marker_ms" =~ ^[1-9][0-9]{12}$ ]] || die "invalid millisecond marker"
    printf '%s\n' "SELECT JSON_OBJECT('sid',u.sid,'channel_key',u.channel_key,'upstream_member_key',u.upstream_member_key,'replication_state',u.replication_state,'replication_lag_seconds',u.replication_lag_seconds,'last_seen_epoch_ms',u.last_seen_epoch_ms) FROM db_topology_upstreams u JOIN db_topology_groups g ON g.id=u.group_id WHERE g.uid = ${TOPOLOGY_UID} AND u.sid IN (${sids}) AND u.last_seen_epoch_ms >= ${marker_ms} ORDER BY u.sid,u.channel_key;"
}

build_clickhouse_observation_query() {
    local marker_ms="$1" pairs_csv="$2" pair sid rid first=1 conditions=''
    validate_persistence_identity
    [[ "$marker_ms" =~ ^[1-9][0-9]{12}$ ]] || die "invalid millisecond marker"
    IFS=',' read -ra pairs <<<"$pairs_csv"
    for pair in "${pairs[@]}"; do
        sid="${pair%%:*}"
        rid="${pair#*:}"
        [[ "$sid" =~ ^[1-9][0-9]*$ && "$rid" =~ ^[A-Za-z0-9._-]{1,255}$ ]] ||
            die "invalid SID/RID observation correlation"
        ((first)) || conditions+=' OR '
        conditions+="(sid = ${sid} AND rid = '${rid}')"
        first=0
    done
    ((first == 0)) || die "no SID/RID correlations supplied"
    printf '%s\n' "SELECT sid,rid,toUnixTimestamp64Milli(timestamp) AS observed_epoch_ms,relations FROM db_topology_observations WHERE uid = ${TOPOLOGY_UID} AND timestamp >= toDateTime64(${marker_ms} / 1000.0, 3) AND timestamp < addSeconds(toDateTime64(${marker_ms} / 1000.0, 3), ${CLICKHOUSE_MARKER_WINDOW_SECONDS}) AND (${conditions}) ORDER BY sid,timestamp FORMAT JSONEachRow"
}

assert_selected_observations() {
    local observations="$1" current="$2" pairs_csv="$3" marker_ms="$4" expected="$5"
    jq -s -e --slurpfile current "$current" --arg pairs "$pairs_csv" --argjson marker "$marker_ms" \
        --argjson window "$CLICKHOUSE_MARKER_WINDOW_SECONDS" --argjson expected "$expected" '
      ($pairs|split(",")|map(split(":")|{sid:(.[0]|tonumber),rid:.[1]})|sort_by(.sid,.rid)) as $pairs |
      length==$expected and
      (map({sid,rid})|sort_by(.sid,.rid))==$pairs and
      (group_by([.sid,.rid])|all(length==1)) and
      all(.observed_epoch_ms >= $marker and .observed_epoch_ms < ($marker+($window*1000))) and
      all(. as $o |
        ($o.relations|fromjson) as $actual |
        ($current|map(select(.sid==$o.sid))|
          map({relation_type,group_key,parent_group_key,member_key,role,is_writer,replication_state})|
          sort_by(.relation_type,.group_key)) as $wanted |
        ($actual|map({relation_type,group_key,parent_group_key,member_key,role,is_writer,replication_state})|
          sort_by(.relation_type,.group_key))==$wanted)
    ' "$observations" >/dev/null || die "ClickHouse observations are missing, duplicated, or relation-invalid"
}

assert_aurora_failover() {
    local before="$1" after="$2" marker_ms="$3"
    jq -s -e --slurpfile before "$before" --argjson marker "$marker_ms" '
      ($before | map(select(.relation_type == "aurora_cluster" and .is_writer == 1)) | first.member_key) as $old |
      (map(select(.relation_type == "aurora_cluster"))) as $aurora |
      ($aurora | length >= 2) and
      ($aurora | map(.group_key) | unique | length == 1) and
      ($aurora | map(select(.is_writer == 1 and .role == "primary")) | length == 1) and
      ($aurora | map(select(.is_writer == 1)) | first.member_key != $old) and
      ($aurora | all(.last_seen_epoch_ms >= $marker)) and
      (map(select(.relation_type == "async_replication" or .relation_type == "group_replication"))|length)>0
    ' "$after" >/dev/null || die "Aurora failover persistence assertion failed"
}

assert_transition_state() {
    local label="$1" current="$2" upstreams="$3" expectation="$4" marker_ms="$5"
    jq -s -e --slurpfile x "$expectation" --arg label "$label" --argjson marker "$marker_ms" '
      ($x[0]) as $e |
      all(.last_seen_epoch_ms >= $marker) and
      (group_by([.sid,.relation_type,.group_key]) | all(length == 1)) and
      if $label == "aurora-provisioned-failover" then
        (map(select(.relation_type=="aurora_cluster" and .group_key==$e.group_key)) as $r |
          ($r|length)==3 and ($r|map(select(.is_writer==1 and .role=="primary"))|length)==1 and
          ($r|map(select(.is_writer==1))[0].member_key != $e.old_writer_member_key) and
          (map(select(.relation_type=="async_replication" or .relation_type=="group_replication"))|length)>0)
      elif $label == "aws-baseline" then
        (map(select(.relation_type=="aurora_cluster")) as $aurora |
          ($aurora|length)==10 and ($aurora|map(.group_key)|unique|length)==4 and
          ($aurora|group_by(.group_key)|map(length)|sort)==[2,2,3,3] and
          ($aurora|group_by(.group_key)|all((map(select(.is_writer==1))|length)==1))) and
        (map(select(.relation_type=="aurora_global_database")) as $global |
          ($global|length)==4 and ($global|map(.group_key)|unique|length)==1 and
          ($global|map(select(.role=="primary_cluster_member"))|length)==2 and
          ($global|map(select(.role=="replica_cluster_member" and .parent_group_key!=null))|length)==2) and
        (map(select(.relation_type=="rds_read_replica")) as $replica |
          ($replica|length)==2 and ($replica|map(.group_key)|unique|length)==1 and
          ($replica|map(.role)|sort)==["primary","replica"]) and
        (map(select(.relation_type=="rds_multi_az")) as $multi |
          ($multi|length)==1 and $multi[0].managed_standby==true) and
        (map(select(.relation_type=="async_replication"))|length)>0
      elif $label == "aurora-serverless-scaled" then
        (map(select(.relation_type=="aurora_cluster" and .group_key==$e.group_key)) | length == 3) and
        (map(select(.relation_type=="aurora_cluster" and .group_key==$e.group_key and .is_writer==1)) | length == 1) and
        (map(select(.relation_type=="aurora_cluster" and .group_key==$e.group_key)) |
          all(.serverless_min_acu==$e.min_acu and .serverless_max_acu==$e.max_acu))
      elif $label == "aurora-global-transition" then
        (map(select(.relation_type=="aurora_global_database" and .group_key==$e.group_key)) as $r |
          ($r|length)==4 and ($r|map(select(.role=="primary_cluster_member"))|length)==2 and
          ($r|map(select(.role=="replica_cluster_member" and .parent_group_key != null))|length)==2 and
          ($r|map(select(.role=="primary_cluster_member"))|map(.member_key)|
            all(. as $member|($e.old_primary_member_keys|index($member))==null)))
      elif $label == "rds-replica-stopped" then
        (map(select(.sid==$e.sid and .relation_type=="rds_read_replica")) | length == 1) and
        (map(select(.sid==$e.sid and .relation_type=="async_replication" and (.replication_state=="stopped" or .replication_state=="error"))) | length == 1)
      elif $label == "rds-replica-resumed" then
        (map(select(.sid==$e.sid and .relation_type=="rds_read_replica")) | length == 1) and
        (map(select(.sid==$e.sid and .relation_type=="async_replication" and .replication_state=="healthy")) | length == 1)
      elif $label == "rds-multi-az-failover" then
        (map(select(.relation_type=="rds_multi_az" and .member_key==$e.member_key and .role=="primary" and
          .is_writer==1 and .replication_state=="unknown" and .managed_standby==true)) | length == 1) and
        (map(select(.relation_type=="rds_multi_az" and .member_key!=$e.member_key)) | length == 0)
      else false end
    ' "$current" >/dev/null || die "current-state assertion failed for $label"

    jq -s -e --slurpfile x "$expectation" --arg label "$label" '
      ($x[0]) as $e |
      if $label == "aurora-global-transition" then
        map(select(.upstream_member_key != null and .replication_state=="healthy")) | length > 0
      elif $label == "rds-replica-stopped" then
        map(select(.sid==$e.sid and (.replication_state=="stopped" or .replication_state=="error"))) | length > 0
      elif $label == "rds-replica-resumed" then
        map(select(.sid==$e.sid and .replication_state=="healthy")) | length > 0
      else true end
    ' "$upstreams" >/dev/null || die "upstream assertion failed for $label"
}

require_runtime() {
    local name
    for name in AWS_TOPOLOGY_EAST_VPC_ID AWS_TOPOLOGY_EAST_SUBNET_IDS \
        AWS_TOPOLOGY_WEST_VPC_ID AWS_TOPOLOGY_WEST_SUBNET_IDS \
        RELEEM_API_KEY TOPOLOGY_MYSQL_HOST TOPOLOGY_MYSQL_USER \
        TOPOLOGY_MYSQL_PASSWORD TOPOLOGY_CLICKHOUSE_HOST TOPOLOGY_CLICKHOUSE_USER \
        TOPOLOGY_CLICKHOUSE_PASSWORD; do
        [[ -n "${!name:-}" ]] || die "$name must be set"
    done
    validate_persistence_identity
    [[ -x "$AGENT_BINARY" ]] || die "required Agent binary is absent or not executable: $AGENT_BINARY"
}

verify_network_inputs() {
    local region="$1" vpc="$2" subnet_csv="$3" out="$4"
    local subnet_json
    [[ "$vpc" =~ ^vpc-[a-f0-9]+$ ]] || die "invalid VPC ID"
    [[ "$subnet_csv" =~ ^subnet-[a-f0-9]+(,subnet-[a-f0-9]+)+$ ]] || die "at least two subnet IDs are required"
    IFS=',' read -ra subnets <<<"$subnet_csv"
    subnet_json="$(aws_region "$region" ec2 describe-subnets --subnet-ids "${subnets[@]}")"
    jq -e --arg vpc "$vpc" '
      (.Subnets|length) >= 2 and
      (.Subnets|map(.AvailabilityZone)|unique|length) >= 2 and
      all(.Subnets[]; .VpcId==$vpc and .MapPublicIpOnLaunch==false)
    ' <<<"$subnet_json" >/dev/null || die "subnets must be private, in one VPC, and span two AZs"
    jq -n --arg region "$region" --arg vpc "$vpc" \
        --argjson subnets "$(jq '.Subnets|map({subnet_id:.SubnetId,availability_zone:.AvailabilityZone,map_public_ip:.MapPublicIpOnLaunch})' <<<"$subnet_json")" \
        '{region:$region,vpc:$vpc,subnets:$subnets}' >"$out"
}

inventory_region() {
    local region="$1" output="$2" prefix
    prefix="releem-$(run_id)-"
    mkdir -p "$(dirname "$output")"
    aws_region "$region" resourcegroupstaggingapi get-resources \
        --tag-filters "Key=${TAG_RUN_KEY},Values=$(run_id)" "Key=${TAG_MANAGED_KEY},Values=true" \
        --resource-type-filters rds:db rds:cluster rds:cluster-pg rds:pg rds:subgrp rds:snapshot ec2:security-group \
        --output json >"$output.raw"
    jq --arg prefix "$prefix" '
      [.ResourceTagMappingList[]? | .ResourceARN as $arn |
       {arn:$arn,tags:(.Tags|map({key:.Key,value:.Value})|from_entries)} |
       select(.arn|contains($prefix))] |
      {instances:[.[]|select(.arn|contains(":db:"))|.arn],
       clusters:[.[]|select(.arn|contains(":cluster:"))|.arn],
       global_clusters:[],
       parameter_groups:[.[]|select(.arn|test(":(cluster-pg|pg):"))|.arn],
       security_groups:[.[]|select(.arn|contains(":security-group/"))|.arn],
       subnet_groups:[.[]|select(.arn|contains(":subgrp:"))|.arn],
       snapshots:[.[]|select(.arn|contains(":snapshot:"))|.arn],
       ownership:[.[]]}
    ' "$output.raw" >"$output"
    rm -f "$output.raw"
    assert_inventory_owned <(jq '.ownership' "$output")
}

inventory_global_clusters() {
    local output="$1" prefix arn tags
    prefix="releem-$(run_id)-"
    aws_region "$PRIMARY_REGION" rds describe-global-clusters --output json >"$output.raw"
    : >"$output.items"
    while IFS=$'\t' read -r arn identifier; do
        [[ -n "$arn" ]] || continue
        [[ "$identifier" == "$prefix"* ]] || continue
        tags="$(aws_region "$PRIMARY_REGION" rds list-tags-for-resource --resource-name "$arn")"
        jq -cn --arg arn "$arn" --argjson tags "$(jq '.TagList|map({key:.Key,value:.Value})|from_entries' <<<"$tags")" \
            '{arn:$arn,tags:$tags}' >>"$output.items"
    done < <(jq -r '.GlobalClusters[]?|[.GlobalClusterArn,.GlobalClusterIdentifier]|@tsv' "$output.raw")
    jq -s '.' "$output.items" >"$output"
    rm -f "$output.raw" "$output.items"
    assert_inventory_owned "$output"
}

combine_inventory() {
    local east="$1" west="$2" globals="$3" out="$4"
    jq -s --slurpfile globals "$globals" '
      reduce .[] as $r ({instances:[],clusters:[],global_clusters:[],parameter_groups:[],security_groups:[],subnet_groups:[],snapshots:[]};
        .instances += $r.instances | .clusters += $r.clusters |
        .parameter_groups += $r.parameter_groups | .security_groups += $r.security_groups |
        .subnet_groups += $r.subnet_groups | .snapshots += $r.snapshots) |
      .global_clusters = ($globals[0]|map(.arn)) | with_entries(.value |= unique)
    ' "$east" "$west" >"$out"
}

inventory_support_resources() {
    local out="$1" account bucket runner_role monitor_role profile east_instances west_instances east_enis west_enis
    account="$(aws sts get-caller-identity --query Account --output text)"
    bucket="releem-topology-$(printf '%s' "$account" | sha256sum | cut -c1-12)-$(run_id)"
    runner_role="$(resource_name runner-role)"; monitor_role="$(resource_name monitoring-role)"; profile="$(resource_name runner-profile)"
    east_instances="$(aws_region "$PRIMARY_REGION" ec2 describe-instances --filters "Name=tag:${TAG_RUN_KEY},Values=$(run_id)" "Name=tag:${TAG_MANAGED_KEY},Values=true" "Name=instance-state-name,Values=pending,running,stopping,stopped,shutting-down" --output json)"
    west_instances="$(aws_region "$SECONDARY_REGION" ec2 describe-instances --filters "Name=tag:${TAG_RUN_KEY},Values=$(run_id)" "Name=tag:${TAG_MANAGED_KEY},Values=true" "Name=instance-state-name,Values=pending,running,stopping,stopped,shutting-down" --output json)"
    east_enis="$(aws_region "$PRIMARY_REGION" ec2 describe-network-interfaces --filters "Name=tag:${TAG_RUN_KEY},Values=$(run_id)" "Name=tag:${TAG_MANAGED_KEY},Values=true" --output json)"
    west_enis="$(aws_region "$SECONDARY_REGION" ec2 describe-network-interfaces --filters "Name=tag:${TAG_RUN_KEY},Values=$(run_id)" "Name=tag:${TAG_MANAGED_KEY},Values=true" --output json)"
    jq -n \
        --argjson east_instances "$(jq '[.Reservations[].Instances[].InstanceId]' <<<"$east_instances")" \
        --argjson west_instances "$(jq '[.Reservations[].Instances[].InstanceId]' <<<"$west_instances")" \
        --argjson east_enis "$(jq '[.NetworkInterfaces[].NetworkInterfaceId]' <<<"$east_enis")" \
        --argjson west_enis "$(jq '[.NetworkInterfaces[].NetworkInterfaceId]' <<<"$west_enis")" \
        --arg bucket "$bucket" --arg runner_role "$runner_role" --arg monitor_role "$monitor_role" --arg profile "$profile" \
        --argjson bucket_exists "$(aws s3api head-bucket --bucket "$bucket" >/dev/null 2>&1 && printf true || printf false)" \
        --argjson runner_exists "$(aws iam get-role --role-name "$runner_role" >/dev/null 2>&1 && printf true || printf false)" \
        --argjson monitor_exists "$(aws iam get-role --role-name "$monitor_role" >/dev/null 2>&1 && printf true || printf false)" \
        --argjson profile_exists "$(aws iam get-instance-profile --instance-profile-name "$profile" >/dev/null 2>&1 && printf true || printf false)" \
        '{runners:($east_instances+$west_instances),enis:($east_enis+$west_enis),ssm_artifacts:[],monitoring_streams:[],
          s3_buckets:(if $bucket_exists then [$bucket] else [] end),s3_objects:[],
          instance_profiles:(if $profile_exists then [$profile] else [] end),
          iam_policies:(if $runner_exists then ["ReleemTopologyRunnerRead"] else [] end),
          iam_roles:((if $runner_exists then [$runner_role] else [] end)+(if $monitor_exists then [$monitor_role] else [] end))}' >"$out"
    if aws s3api head-bucket --bucket "$bucket" >/dev/null 2>&1; then
        aws_region "$PRIMARY_REGION" s3api list-objects-v2 --bucket "$bucket" --prefix "$(run_id)/" --output json |
            jq --slurpfile base "$out" '$base[0] + {s3_objects:[.Contents[]?.Key]}' >"${out}.new"
        mv "${out}.new" "$out"
    fi
}

capture_inventory() {
    local target="$1" dir east west globals support base
    dir="$(dirname "$target")/inventory-work"
    mkdir -p "$dir"
    east="$dir/east.json"; west="$dir/west.json"; globals="$dir/global.json"
    inventory_region "$PRIMARY_REGION" "$east"
    inventory_region "$SECONDARY_REGION" "$west"
    inventory_global_clusters "$globals"
    base="$dir/base.json"; support="$dir/support.json"
    combine_inventory "$east" "$west" "$globals" "$base"
    inventory_support_resources "$support"
    jq -s '.[0] * .[1]' "$base" "$support" >"$target"
    chmod 600 "$target"
}

write_selection_state() {
    local engine="$1" class="$2" acu="$3" family="$4" mysql_version="$5" mysql_class="$6" mysql_family="$7" east_ami="$8" west_ami="$9" file
    file="$(selection_file)"
    mkdir -p "$(dirname "$file")"
    printf 'ENGINE_VERSION=%q\nINSTANCE_CLASS=%q\nMIN_ACU=%q\nENGINE_FAMILY=%q\nMYSQL_ENGINE_VERSION=%q\nMYSQL_INSTANCE_CLASS=%q\nMYSQL_ENGINE_FAMILY=%q\nEAST_RUNNER_AMI=%q\nWEST_RUNNER_AMI=%q\n' \
        "$engine" "$class" "$acu" "$family" "$mysql_version" "$mysql_class" "$mysql_family" "$east_ami" "$west_ami" >"$file"
    chmod 600 "$file"
}

load_selection_state() {
    local file
    file="$(selection_file)"
    [[ -f "$file" ]] || die "preflight selection state is absent"
    # Contains validated public service metadata only.
    # shellcheck disable=SC1090
    source "$file"
    [[ "$ENGINE_VERSION" =~ ^8[.]0[.]mysql_aurora[.]3[.][0-9]+[.][0-9]+$ ]] || die "invalid stored engine version"
    [[ "$INSTANCE_CLASS" =~ ^db[.][a-z0-9]+[.][a-z0-9]+$ ]] || die "invalid stored instance class"
    [[ "$MIN_ACU" =~ ^[0-9]+([.][0-9]+)?$ ]] || die "invalid stored minimum ACU"
    [[ "$ENGINE_FAMILY" =~ ^aurora-mysql[0-9]+[.][0-9]+$ ]] || die "invalid stored engine family"
    [[ "$MYSQL_ENGINE_VERSION" =~ ^8[.][0-9]+[.][0-9]+$ ]] || die "invalid stored MySQL engine version"
    [[ "$MYSQL_INSTANCE_CLASS" =~ ^db[.][a-z0-9]+[.][a-z0-9]+$ ]] || die "invalid stored MySQL instance class"
    [[ "$MYSQL_ENGINE_FAMILY" =~ ^mysql8[.]0$ ]] || die "invalid stored MySQL engine family"
    [[ "$EAST_RUNNER_AMI" =~ ^ami-[a-f0-9]+$ && "$WEST_RUNNER_AMI" =~ ^ami-[a-f0-9]+$ ]] || die "invalid stored runner AMI"
}

preflight() {
    validate_run_id
    require_command aws
    require_command jq
    require_command sha256sum
    local out versions versions_west classes classes_west mysql_versions mysql_classes engine class acu family mysql_version mysql_class mysql_family east_ami west_ami pre east_network west_network
    out="$(evidence_dir)/preflight"
    mkdir -p "$(state_dir)" "$out"
    chmod 700 "$(state_dir)" "$out"
    versions="$out/engine-versions.json"
    versions_west="$out/engine-versions-west.json"
    classes="$out/orderable-options.json"
    aws_region "$PRIMARY_REGION" rds describe-db-engine-versions --engine aurora-mysql \
        --include-all --output json >"$versions"
    aws_region "$SECONDARY_REGION" rds describe-db-engine-versions --engine aurora-mysql \
        --include-all --output json >"$versions_west"
    engine="$(select_latest_common_aurora_version "$versions" "$versions_west")"
    aws_region "$PRIMARY_REGION" rds describe-orderable-db-instance-options \
        --engine aurora-mysql --engine-version "$engine" --output json >"$classes"
    classes_west="$out/orderable-options-west.json"
    aws_region "$SECONDARY_REGION" rds describe-orderable-db-instance-options \
        --engine aurora-mysql --engine-version "$engine" --output json >"$classes_west"
    class="$(select_smallest_common_orderable_class "$classes" "$classes_west")"
    acu="$(jq -ser --arg version "$engine" '[.[]|.DBEngineVersions[]|select(.EngineVersion==$version)|.ServerlessV2FeaturesSupport.MinCapacity]|max' "$versions" "$versions_west")"
    family="$(jq -er --arg version "$engine" '.DBEngineVersions[]|select(.EngineVersion==$version)|.DBParameterGroupFamily' "$versions")"
    mysql_versions="$out/mysql-engine-versions.json"
    mysql_classes="$out/mysql-orderable-options.json"
    aws_region "$PRIMARY_REGION" rds describe-db-engine-versions --engine mysql --include-all --output json >"$mysql_versions"
    mysql_version="$(jq -er '[.DBEngineVersions[]|select((.Status//"available")=="available")|select(.EngineVersion|test("^8[.]0[.][0-9]+$"))|{v:.EngineVersion,n:(.EngineVersion|split(".")|map(tonumber))}]|sort_by(.n)|last|.v' "$mysql_versions")"
    mysql_family="$(jq -er --arg version "$mysql_version" '.DBEngineVersions[]|select(.EngineVersion==$version)|.DBParameterGroupFamily' "$mysql_versions")"
    aws_region "$PRIMARY_REGION" rds describe-orderable-db-instance-options --engine mysql --engine-version "$mysql_version" --output json >"$mysql_classes"
    mysql_class="$(select_smallest_orderable_class "$mysql_classes")"
    east_ami="$(aws_region "$PRIMARY_REGION" ssm get-parameter --name /aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64 --query Parameter.Value --output text)"
    west_ami="$(aws_region "$SECONDARY_REGION" ssm get-parameter --name /aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64 --query Parameter.Value --output text)"
    write_selection_state "$engine" "$class" "$acu" "$family" "$mysql_version" "$mysql_class" "$mysql_family" "$east_ami" "$west_ami"

    aws_region "$PRIMARY_REGION" service-quotas list-service-quotas --service-code rds --output json >"$out/quotas-east.json"
    aws_region "$SECONDARY_REGION" service-quotas list-service-quotas --service-code rds --output json >"$out/quotas-west.json"
    aws_region "$PRIMARY_REGION" service-quotas list-service-quotas --service-code ec2 --output json >"$out/ec2-quotas-east.json"
    aws_region "$SECONDARY_REGION" service-quotas list-service-quotas --service-code ec2 --output json >"$out/ec2-quotas-west.json"
    east_network="$out/network-east.json"; west_network="$out/network-west.json"
    if [[ -n "${AWS_TOPOLOGY_EAST_VPC_ID:-}" ]]; then
        verify_network_inputs "$PRIMARY_REGION" "$AWS_TOPOLOGY_EAST_VPC_ID" "$AWS_TOPOLOGY_EAST_SUBNET_IDS" "$east_network"
        verify_network_inputs "$SECONDARY_REGION" "$AWS_TOPOLOGY_WEST_VPC_ID" "$AWS_TOPOLOGY_WEST_SUBNET_IDS" "$west_network"
    else
        jq -n '{status:"not-supplied",required_for:"run"}' >"$east_network"
        cp "$east_network" "$west_network"
    fi
    pre="$out/inventory-before.json"
    capture_inventory "$pre"
    assert_inventory_empty "$pre"
    jq -n --arg captured_at "$(date -u +%FT%TZ)" --arg engine "$engine" --arg class "$class" \
        --argjson min_acu "$acu" \
        '{captured_at:$captured_at,regions:["us-east-1","us-west-2"],engine_version:$engine,
        provisioned_instance_class:$class,serverless_v2_min_acu:$min_acu,runner_instance_class:"t3.micro",
          hourly_configuration:{provisioned_aurora_instances:7,serverless_v2_instances:3,
            serverless_v2_min_acu_total:($min_acu*3),ordinary_rds_instances:3,
            note:"configuration units only; consult current AWS pricing before approval"}}' >"$out/summary.json"
    chmod 600 "$out"/*.json
    log "preflight evidence: $out/summary.json"
}

generate_secret() {
    local file password
    file="$(secret_file)"
    if [[ -f "$file" ]]; then
        chmod 600 "$file"
        return
    fi
    require_command openssl
    mkdir -p "$(dirname "$file")"
    umask 077
    password="$(openssl rand -base64 36 | tr -dc 'A-Za-z0-9._-' | head -c 32)"
    ((${#password} >= 24)) || die "failed to generate runtime password"
    printf 'DB_USER=%q\nDB_PASSWORD=%q\n' releem_topology "$password" >"$file"
    chmod 600 "$file"
}

load_secret() {
    local file
    file="$(secret_file)"
    [[ -f "$file" && "$(stat -c '%a' "$file")" == 600 ]] || die "runtime secret file must be mode 0600"
    # Generated values remain local and are never logged.
    # shellcheck disable=SC1090
    source "$file"
}

arm_cleanup() { CLEANUP_ARMED=true; }

support_state_file() { printf '%s/support.env\n' "$(state_dir)"; }

iam_tags() {
    printf 'Key=%s,Value=%s\n' "$TAG_RUN_KEY" "$(run_id)"
    printf 'Key=%s,Value=true\n' "$TAG_MANAGED_KEY"
}

role_owned() {
    local role="$1"
    aws iam list-role-tags --role-name "$role" --output json |
        jq -e --arg run "$(run_id)" --arg run_key "$TAG_RUN_KEY" --arg managed_key "$TAG_MANAGED_KEY" '
          (.Tags|map({key:.Key,value:.Value})|from_entries) as $t |
          $t[$run_key]==$run and $t[$managed_key]=="true"' >/dev/null
}

ensure_iam_role() {
    local role="$1" trust_service="$2" trust_file="$3"
    jq -n --arg service "$trust_service" '{Version:"2012-10-17",Statement:[{Effect:"Allow",Principal:{Service:$service},Action:"sts:AssumeRole"}]}' >"$trust_file"
    chmod 600 "$trust_file"
    if aws iam get-role --role-name "$role" >/dev/null 2>&1; then
        role_owned "$role" || die "IAM role name collision without exact ownership"
    else
        aws iam create-role --role-name "$role" --path /releem-topology/ \
            --assume-role-policy-document "file://${trust_file}" \
            --tags "$(iam_tags | head -n1)" "$(iam_tags | tail -n1)" >/dev/null
        arm_cleanup
    fi
}

ensure_support_resources() {
    local account bucket runner_role runner_profile monitor_role runner_policy trust_file
    account="$(aws sts get-caller-identity --query Account --output text)"
    [[ "$account" =~ ^[0-9]{12}$ ]] || die "unable to resolve AWS account"
    bucket="releem-topology-$(printf '%s' "$account" | sha256sum | cut -c1-12)-$(run_id)"
    runner_role="$(resource_name runner-role)"
    runner_profile="$(resource_name runner-profile)"
    monitor_role="$(resource_name monitoring-role)"
    trust_file="$(state_dir)/trust.json"
    ensure_iam_role "$runner_role" ec2.amazonaws.com "$trust_file"
    ensure_iam_role "$monitor_role" monitoring.rds.amazonaws.com "$trust_file"
    aws iam attach-role-policy --role-name "$runner_role" --policy-arn arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore >/dev/null
    aws iam attach-role-policy --role-name "$monitor_role" --policy-arn arn:aws:iam::aws:policy/service-role/AmazonRDSEnhancedMonitoringRole >/dev/null
    sleep 10

    if ! aws s3api head-bucket --bucket "$bucket" >/dev/null 2>&1; then
        aws_region "$PRIMARY_REGION" s3api create-bucket --bucket "$bucket" >/dev/null
        arm_cleanup
        aws_region "$PRIMARY_REGION" s3api put-public-access-block --bucket "$bucket" \
            --public-access-block-configuration BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true
        aws_region "$PRIMARY_REGION" s3api put-bucket-encryption --bucket "$bucket" \
            --server-side-encryption-configuration '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}'
        aws_region "$PRIMARY_REGION" s3api put-bucket-tagging --bucket "$bucket" \
            --tagging "TagSet=[{Key=${TAG_RUN_KEY},Value=$(run_id)},{Key=${TAG_MANAGED_KEY},Value=true}]"
    else
        aws_region "$PRIMARY_REGION" s3api get-bucket-tagging --bucket "$bucket" |
            jq -e --arg run "$(run_id)" --arg run_key "$TAG_RUN_KEY" --arg managed_key "$TAG_MANAGED_KEY" '
              (.TagSet|map({key:.Key,value:.Value})|from_entries) as $t |
              $t[$run_key]==$run and $t[$managed_key]=="true"' >/dev/null ||
            die "S3 bucket collision without exact ownership"
    fi

    runner_policy="$(state_dir)/runner-policy.json"
    runner_policy_document "arn:aws:s3:::${bucket}/$(run_id)/*" >"$runner_policy"
    assert_runner_policy "$runner_policy"
    aws iam put-role-policy --role-name "$runner_role" --policy-name ReleemTopologyRunnerRead \
        --policy-document "file://${runner_policy}" >/dev/null
    if ! aws iam get-instance-profile --instance-profile-name "$runner_profile" >/dev/null 2>&1; then
        aws iam create-instance-profile --instance-profile-name "$runner_profile" \
            --tags "$(iam_tags | head -n1)" "$(iam_tags | tail -n1)" >/dev/null
        arm_cleanup
        aws iam add-role-to-instance-profile --instance-profile-name "$runner_profile" --role-name "$runner_role"
        sleep 10
    fi
    MONITORING_ROLE_ARN="$(aws iam get-role --role-name "$monitor_role" --query Role.Arn --output text)"
    {
        printf 'ACCOUNT_ID=%q\nS3_BUCKET=%q\nRUNNER_ROLE=%q\nRUNNER_PROFILE=%q\nMONITORING_ROLE=%q\nMONITORING_ROLE_ARN=%q\n' \
            "$account" "$bucket" "$runner_role" "$runner_profile" "$monitor_role" "$MONITORING_ROLE_ARN"
    } >"$(support_state_file)"
    chmod 600 "$(support_state_file)"
}

load_support_state() {
    local file
    file="$(support_state_file)"
    [[ -f "$file" ]] || die "support state is absent"
    # Contains only validated resource identifiers, never credentials.
    # shellcheck disable=SC1090
    source "$file"
    [[ "$S3_BUCKET" =~ ^[a-z0-9.-]{3,63}$ && "$MONITORING_ROLE_ARN" =~ ^arn:aws:iam::[0-9]{12}:role/ ]] ||
        die "invalid support state"
}

ensure_runner_security_group() {
    local region="$1" vpc="$2" suffix="$3" name result sg
    name="$(resource_name "runner-${suffix}")"
    result="$(aws_region "$region" ec2 describe-security-groups --filters "Name=vpc-id,Values=$vpc" "Name=group-name,Values=$name")"
    sg="$(jq -r '.SecurityGroups[0].GroupId // empty' <<<"$result")"
    if [[ -z "$sg" ]]; then
        sg="$(aws_region "$region" ec2 create-security-group --group-name "$name" \
            --description "Disposable no-ingress Releem runner $(run_id)" --vpc-id "$vpc" \
            --tag-specifications "ResourceType=security-group,Tags=[{Key=${TAG_RUN_KEY},Value=$(run_id)},{Key=${TAG_MANAGED_KEY},Value=true}]" \
            --query GroupId --output text)"
        arm_cleanup
        result="$(aws_region "$region" ec2 describe-security-groups --group-ids "$sg")"
    fi
    assert_security_group_owned <(printf '%s\n' "$result")
    assert_runner_security_group <(printf '%s\n' "$result")
    printf '%s\n' "$sg"
}

runner_launch_spec() {
    local ami="$1" subnet="$2" sg="$3" file="$4"
    jq -n --arg ami "$ami" --arg subnet "$subnet" --arg sg "$sg" --arg profile "$RUNNER_PROFILE" \
        --arg name "$(resource_name runner)" --arg run "$(run_id)" --arg run_key "$TAG_RUN_KEY" --arg managed_key "$TAG_MANAGED_KEY" '{
          ImageId:$ami,InstanceType:"t3.micro",MinCount:1,MaxCount:1,IamInstanceProfile:{Name:$profile},
          NetworkInterfaces:[{DeviceIndex:0,AssociatePublicIpAddress:false,DeleteOnTermination:true,SubnetId:$subnet,Groups:[$sg]}],
          BlockDeviceMappings:[{DeviceName:"/dev/xvda",Ebs:{Encrypted:true,VolumeType:"gp3",VolumeSize:8,DeleteOnTermination:true}}],
          MetadataOptions:{HttpTokens:"required",HttpEndpoint:"enabled"},
          TagSpecifications:[
            {ResourceType:"instance",Tags:[{Key:"Name",Value:$name},{Key:$run_key,Value:$run},{Key:$managed_key,Value:"true"}]},
            {ResourceType:"volume",Tags:[{Key:$run_key,Value:$run},{Key:$managed_key,Value:"true"}]},
            {ResourceType:"network-interface",Tags:[{Key:$run_key,Value:$run},{Key:$managed_key,Value:"true"}]}
          ]
        }' >"$file"
    chmod 600 "$file"
    assert_runner_launch_spec "$file"
}

ensure_runner() {
    local region="$1" vpc="$2" subnet_csv="$3" suffix="$4" ami="$5" sg subnet result instance spec
    sg="$(ensure_runner_security_group "$region" "$vpc" "$suffix")"
    subnet="${subnet_csv%%,*}"
    result="$(aws_region "$region" ec2 describe-instances \
        --filters "Name=tag:${TAG_RUN_KEY},Values=$(run_id)" "Name=tag:${TAG_MANAGED_KEY},Values=true" \
        "Name=tag:Name,Values=$(resource_name runner)" "Name=instance-state-name,Values=pending,running,stopping,stopped")"
    instance="$(jq -r '.Reservations[].Instances[].InstanceId' <<<"$result" | head -n1)"
    if [[ -z "$instance" ]]; then
        spec="$(state_dir)/runner-${suffix}.json"
        runner_launch_spec "$ami" "$subnet" "$sg" "$spec"
        instance="$(aws_region "$region" ec2 run-instances --cli-input-json "file://${spec}" --query 'Instances[0].InstanceId' --output text)"
        arm_cleanup
    fi
    aws_region "$region" ec2 wait instance-running --instance-ids "$instance"
    aws_region "$region" ec2 wait instance-status-ok --instance-ids "$instance"
    wait_until "runner SSM registration" runner_ssm_online "$region" "$instance"
    printf '%s|%s\n' "$instance" "$sg"
}

runner_ssm_online() {
    [[ "$(aws_region "$1" ssm describe-instance-information --filters "Key=InstanceIds,Values=$2" \
        --query 'InstanceInformationList[?PingStatus==`Online`]|length(@)' --output text 2>/dev/null || true)" == 1 ]]
}

wait_until() {
    local description="$1"
    shift
    local deadline
    deadline=$(( $(date +%s) + WAIT_TIMEOUT_SECONDS ))
    until "$@"; do
        (($(date +%s) < deadline)) || die "timeout waiting for $description"
        sleep "$POLL_SECONDS"
    done
}

instance_exists() { aws_region "$1" rds describe-db-instances --db-instance-identifier "$2" >/dev/null 2>&1; }
cluster_exists() { aws_region "$1" rds describe-db-clusters --db-cluster-identifier "$2" >/dev/null 2>&1; }
global_exists() { aws_region "$PRIMARY_REGION" rds describe-global-clusters --global-cluster-identifier "$1" >/dev/null 2>&1; }

wait_instance_available() {
    aws_region "$1" rds wait db-instance-available --db-instance-identifier "$2"
}

wait_cluster_available() {
    aws_region "$1" rds wait db-cluster-available --db-cluster-identifier "$2"
}

create_network_resources() {
    local region="$1" vpc="$2" subnet_csv="$3" runner_sg="$4" suffix="$5"
    local subnet_group sg_name sg_id group_exists
    subnet_group="$(resource_name "subnets-${suffix}")"
    sg_name="$(resource_name "mysql-${suffix}")"
    IFS=',' read -ra subnets <<<"$subnet_csv"
    if ! aws_region "$region" rds describe-db-subnet-groups --db-subnet-group-name "$subnet_group" >/dev/null 2>&1; then
        aws_region "$region" rds create-db-subnet-group --db-subnet-group-name "$subnet_group" \
            --db-subnet-group-description "Disposable Releem topology $(run_id)" \
            --subnet-ids "${subnets[@]}" --tags "$(tags_args | head -n1)" "$(tags_args | tail -n1)" >/dev/null
        arm_cleanup
    fi
    local subnet_description subnet_arn
    subnet_description="$(aws_region "$region" rds describe-db-subnet-groups --db-subnet-group-name "$subnet_group")"
    subnet_arn="$(jq -r '.DBSubnetGroups[0].DBSubnetGroupArn' <<<"$subnet_description")"
    aws_region "$region" rds list-tags-for-resource --resource-name "$subnet_arn" |
        jq -e --arg run "$(run_id)" --arg run_key "$TAG_RUN_KEY" --arg managed_key "$TAG_MANAGED_KEY" '
          (.TagList|map({key:.Key,value:.Value})|from_entries) as $t |
          $t[$run_key]==$run and $t[$managed_key]=="true"' >/dev/null || die "DB subnet group ownership mismatch"
    group_exists="$(aws_region "$region" ec2 describe-security-groups \
        --filters "Name=vpc-id,Values=$vpc" "Name=group-name,Values=$sg_name" --output json)"
    sg_id="$(jq -r '.SecurityGroups[0].GroupId // empty' <<<"$group_exists")"
    if [[ -z "$sg_id" ]]; then
        sg_id="$(aws_region "$region" ec2 create-security-group --group-name "$sg_name" \
            --description "Disposable Releem topology $(run_id)" --vpc-id "$vpc" \
            --tag-specifications "ResourceType=security-group,Tags=[{Key=${TAG_RUN_KEY},Value=$(run_id)},{Key=${TAG_MANAGED_KEY},Value=true}]" \
            --query GroupId --output text)"
        arm_cleanup
        aws_region "$region" ec2 authorize-security-group-ingress --group-id "$sg_id" \
            --ip-permissions "IpProtocol=tcp,FromPort=3306,ToPort=3306,UserIdGroupPairs=[{GroupId=${runner_sg},Description=Releem-topology-$(run_id)}]" >/dev/null
    fi
    group_exists="$(aws_region "$region" ec2 describe-security-groups --group-ids "$sg_id")"
    assert_security_group_owned <(printf '%s\n' "$group_exists")
    assert_database_security_group <(printf '%s\n' "$group_exists") "$runner_sg"
    printf '%s|%s\n' "$subnet_group" "$sg_id"
}

ensure_rds_parameter_group() {
    local region="$1" name
    name="$(resource_name rds-instance-pg)"
    if ! aws_region "$region" rds describe-db-parameter-groups --db-parameter-group-name "$name" >/dev/null 2>&1; then
        aws_region "$region" rds create-db-parameter-group --db-parameter-group-name "$name" \
            --db-parameter-group-family "$MYSQL_ENGINE_FAMILY" --description "Disposable Releem topology $(run_id)" \
            --tags "$(tags_args | head -n1)" "$(tags_args | tail -n1)" >/dev/null
        arm_cleanup
    fi
    printf '%s\n' "$name"
}

ensure_parameter_groups() {
    local region="$1" suffix="$2" cluster_pg instance_pg
    cluster_pg="$(resource_name "cluster-pg-${suffix}")"
    instance_pg="$(resource_name "instance-pg-${suffix}")"
    if ! aws_region "$region" rds describe-db-cluster-parameter-groups --db-cluster-parameter-group-name "$cluster_pg" >/dev/null 2>&1; then
        aws_region "$region" rds create-db-cluster-parameter-group --db-cluster-parameter-group-name "$cluster_pg" \
            --db-parameter-group-family "$ENGINE_FAMILY" --description "Disposable Releem topology $(run_id)" \
            --tags "$(tags_args | head -n1)" "$(tags_args | tail -n1)" >/dev/null
        arm_cleanup
    fi
    if ! aws_region "$region" rds describe-db-parameter-groups --db-parameter-group-name "$instance_pg" >/dev/null 2>&1; then
        aws_region "$region" rds create-db-parameter-group --db-parameter-group-name "$instance_pg" \
            --db-parameter-group-family "$ENGINE_FAMILY" --description "Disposable Releem topology $(run_id)" \
            --tags "$(tags_args | head -n1)" "$(tags_args | tail -n1)" >/dev/null
        arm_cleanup
    fi
    printf '%s|%s\n' "$cluster_pg" "$instance_pg"
}

cluster_input_file() {
    local region="$1" identifier="$2" subnet_group="$3" sg="$4" cluster_pg="$5" serverless="$6" file
    file="$(state_dir)/create-${identifier}.json"
    mkdir -p "$(state_dir)"
    jq -n --arg id "$identifier" --arg engine "$ENGINE_VERSION" --arg user "$DB_USER" \
        --arg password "$DB_PASSWORD" --arg subnet "$subnet_group" --arg sg "$sg" --arg pg "$cluster_pg" \
        --arg run "$(run_id)" --arg run_key "$TAG_RUN_KEY" --arg managed_key "$TAG_MANAGED_KEY" \
        --argjson serverless "$serverless" --argjson acu "$MIN_ACU" '
        {DBClusterIdentifier:$id,Engine:"aurora-mysql",EngineVersion:$engine,
         MasterUsername:$user,MasterUserPassword:$password,DatabaseName:"releem_topology",
         DBSubnetGroupName:$subnet,VpcSecurityGroupIds:[$sg],DBClusterParameterGroupName:$pg,
         StorageEncrypted:true,DeletionProtection:false,BackupRetentionPeriod:1,CopyTagsToSnapshot:true,
         Tags:[{Key:$run_key,Value:$run},{Key:$managed_key,Value:"true"}]} +
         (if $serverless then {ServerlessV2ScalingConfiguration:{MinCapacity:$acu,MaxCapacity:($acu+1)}} else {} end)
    ' >"$file"
    chmod 600 "$file"
    printf '%s\n' "$file"
}

ensure_cluster() {
    local region="$1" identifier="$2" subnet_group="$3" sg="$4" cluster_pg="$5" serverless="$6" global_id="${7:-}" file
    if ! cluster_exists "$region" "$identifier"; then
        file="$(cluster_input_file "$region" "$identifier" "$subnet_group" "$sg" "$cluster_pg" "$serverless")"
        if [[ -n "$global_id" ]]; then
            jq 'del(.MasterUsername,.MasterUserPassword,.DatabaseName) + {GlobalClusterIdentifier:$global}' \
                --arg global "$global_id" "$file" >"${file}.secondary"
            mv "${file}.secondary" "$file"
        fi
        aws_region "$region" rds create-db-cluster --cli-input-json "file://${file}" >/dev/null
        arm_cleanup
    fi
    wait_cluster_available "$region" "$identifier"
}

ensure_cluster_instance() {
    local region="$1" identifier="$2" cluster="$3" class="$4" instance_pg="$5"
    local -a monitor_args
    mapfile -t monitor_args < <(monitoring_arguments "$MONITORING_ROLE_ARN")
    if ! instance_exists "$region" "$identifier"; then
        aws_region "$region" rds create-db-instance --db-instance-identifier "$identifier" \
            --db-cluster-identifier "$cluster" --engine aurora-mysql --db-instance-class "$class" \
            --db-parameter-group-name "$instance_pg" --no-publicly-accessible --auto-minor-version-upgrade \
            "${monitor_args[@]}" \
            --tags "$(tags_args | head -n1)" "$(tags_args | tail -n1)" >/dev/null
        arm_cleanup
    fi
    wait_instance_available "$region" "$identifier"
}

ensure_global_cluster() {
    local identifier="$1" source_arn="$2"
    if ! global_exists "$identifier"; then
        aws_region "$PRIMARY_REGION" rds create-global-cluster --global-cluster-identifier "$identifier" \
            --source-db-cluster-identifier "$source_arn" \
            --tags "$(tags_args | head -n1)" "$(tags_args | tail -n1)" >/dev/null
        arm_cleanup
    fi
}

rds_input_file() {
    local identifier="$1" subnet="$2" sg="$3" parameter_group="$4" multi_az="$5" file
    file="$(state_dir)/create-${identifier}.json"
    mkdir -p "$(state_dir)"
    jq -n --arg id "$identifier" --arg user "$DB_USER" --arg password "$DB_PASSWORD" \
        --arg subnet "$subnet" --arg sg "$sg" --arg pg "$parameter_group" --arg class "$MYSQL_INSTANCE_CLASS" \
        --arg run "$(run_id)" --arg run_key "$TAG_RUN_KEY" --arg managed_key "$TAG_MANAGED_KEY" \
        --argjson multi "$multi_az" '
        {DBInstanceIdentifier:$id,Engine:"mysql",EngineVersion:$mysql_version,DBInstanceClass:$class,AllocatedStorage:20,
         StorageType:"gp3",StorageEncrypted:true,MasterUsername:$user,MasterUserPassword:$password,
         DBName:"releem_topology",DBSubnetGroupName:$subnet,VpcSecurityGroupIds:[$sg],
         DBParameterGroupName:$pg,PubliclyAccessible:false,MultiAZ:$multi,
         BackupRetentionPeriod:1,DeletionProtection:false,AutoMinorVersionUpgrade:true,
         MonitoringInterval:1,MonitoringRoleArn:$monitor_role,
         Tags:[{Key:$run_key,Value:$run},{Key:$managed_key,Value:"true"}]}
    ' --arg mysql_version "$MYSQL_ENGINE_VERSION" --arg monitor_role "$MONITORING_ROLE_ARN" >"$file"
    chmod 600 "$file"
    printf '%s\n' "$file"
}

ensure_rds_instance() {
    local identifier="$1" subnet="$2" sg="$3" pg="$4" multi="$5" file
    if ! instance_exists "$PRIMARY_REGION" "$identifier"; then
        file="$(rds_input_file "$identifier" "$subnet" "$sg" "$pg" "$multi")"
        aws_region "$PRIMARY_REGION" rds create-db-instance --cli-input-json "file://${file}" >/dev/null
        arm_cleanup
    fi
    wait_instance_available "$PRIMARY_REGION" "$identifier"
}

ensure_read_replica() {
    local identifier="$1" source="$2" subnet="$3" sg="$4" pg="$5"
    local -a monitor_args
    mapfile -t monitor_args < <(monitoring_arguments "$MONITORING_ROLE_ARN")
    if ! instance_exists "$PRIMARY_REGION" "$identifier"; then
        aws_region "$PRIMARY_REGION" rds create-db-instance-read-replica \
            --db-instance-identifier "$identifier" --source-db-instance-identifier "$source" \
            --db-instance-class "$MYSQL_INSTANCE_CLASS" --db-subnet-group-name "$subnet" \
            --vpc-security-group-ids "$sg" --db-parameter-group-name "$pg" \
            --no-publicly-accessible --auto-minor-version-upgrade \
            "${monitor_args[@]}" \
            --tags "$(tags_args | head -n1)" "$(tags_args | tail -n1)" >/dev/null
        arm_cleanup
    fi
    wait_instance_available "$PRIMARY_REGION" "$identifier"
}

create_matrix() {
    local east_network west_network east_pg west_pg east_subnet east_sg west_subnet west_sg east_runner west_runner east_runner_id east_runner_sg west_runner_id west_runner_sg
    local east_cluster_pg east_instance_pg west_cluster_pg west_instance_pg rds_instance_pg
    load_selection_state
    generate_secret
    load_secret
    arm_cleanup
    ensure_support_resources
    load_support_state
    east_runner="$(ensure_runner "$PRIMARY_REGION" "$AWS_TOPOLOGY_EAST_VPC_ID" "$AWS_TOPOLOGY_EAST_SUBNET_IDS" east "$EAST_RUNNER_AMI")"
    west_runner="$(ensure_runner "$SECONDARY_REGION" "$AWS_TOPOLOGY_WEST_VPC_ID" "$AWS_TOPOLOGY_WEST_SUBNET_IDS" west "$WEST_RUNNER_AMI")"
    IFS='|' read -r east_runner_id east_runner_sg <<<"$east_runner"
    IFS='|' read -r west_runner_id west_runner_sg <<<"$west_runner"
    printf 'EAST_RUNNER_ID=%q\nWEST_RUNNER_ID=%q\n' "$east_runner_id" "$west_runner_id" >>"$(support_state_file)"
    east_network="$(create_network_resources "$PRIMARY_REGION" "$AWS_TOPOLOGY_EAST_VPC_ID" "$AWS_TOPOLOGY_EAST_SUBNET_IDS" "$east_runner_sg" east)"
    west_network="$(create_network_resources "$SECONDARY_REGION" "$AWS_TOPOLOGY_WEST_VPC_ID" "$AWS_TOPOLOGY_WEST_SUBNET_IDS" "$west_runner_sg" west)"
    IFS='|' read -r east_subnet east_sg <<<"$east_network"
    IFS='|' read -r west_subnet west_sg <<<"$west_network"
    east_pg="$(ensure_parameter_groups "$PRIMARY_REGION" east)"
    west_pg="$(ensure_parameter_groups "$SECONDARY_REGION" west)"
    IFS='|' read -r east_cluster_pg east_instance_pg <<<"$east_pg"
    IFS='|' read -r west_cluster_pg west_instance_pg <<<"$west_pg"
    rds_instance_pg="$(ensure_rds_parameter_group "$PRIMARY_REGION")"

    local provisioned serverless global_id global_east global_west source replica multi i
    provisioned="$(resource_name aurora-provisioned)"
    serverless="$(resource_name aurora-serverless)"
    global_id="$(resource_name aurora-global)"
    global_east="$(resource_name global-east)"
    global_west="$(resource_name global-west)"
    source="$(resource_name rds-source)"; replica="$(resource_name rds-replica)"; multi="$(resource_name rds-multi-az)"

    ensure_cluster "$PRIMARY_REGION" "$provisioned" "$east_subnet" "$east_sg" "$east_cluster_pg" false
    for i in 1 2 3; do ensure_cluster_instance "$PRIMARY_REGION" "${provisioned}-${i}" "$provisioned" "$INSTANCE_CLASS" "$east_instance_pg"; done
    ensure_cluster "$PRIMARY_REGION" "$serverless" "$east_subnet" "$east_sg" "$east_cluster_pg" true
    for i in 1 2 3; do ensure_cluster_instance "$PRIMARY_REGION" "${serverless}-${i}" "$serverless" db.serverless "$east_instance_pg"; done

    ensure_cluster "$PRIMARY_REGION" "$global_east" "$east_subnet" "$east_sg" "$east_cluster_pg" false
    for i in 1 2; do ensure_cluster_instance "$PRIMARY_REGION" "${global_east}-${i}" "$global_east" "$INSTANCE_CLASS" "$east_instance_pg"; done
    ensure_global_cluster "$global_id" "arn:aws:rds:${PRIMARY_REGION}:${ACCOUNT_ID}:cluster:${global_east}"
    ensure_cluster "$SECONDARY_REGION" "$global_west" "$west_subnet" "$west_sg" "$west_cluster_pg" false "$global_id"
    for i in 1 2; do ensure_cluster_instance "$SECONDARY_REGION" "${global_west}-${i}" "$global_west" "$INSTANCE_CLASS" "$west_instance_pg"; done

    ensure_rds_instance "$source" "$east_subnet" "$east_sg" "$rds_instance_pg" false
    ensure_read_replica "$replica" "$source" "$east_subnet" "$east_sg" "$rds_instance_pg"
    ensure_rds_instance "$multi" "$east_subnet" "$east_sg" "$rds_instance_pg" true
}

platform_mysql() {
    local query="$1" MYSQL_PWD="$TOPOLOGY_MYSQL_PASSWORD"
    export MYSQL_PWD
    timeout "${TOPOLOGY_MYSQL_QUERY_TIMEOUT_SECONDS:-30}s" mysql --batch --raw --skip-column-names \
        --connect-timeout="${TOPOLOGY_MYSQL_CONNECT_TIMEOUT_SECONDS:-10}" \
        --host="$TOPOLOGY_MYSQL_HOST" --port="${TOPOLOGY_MYSQL_PORT:-3306}" \
        --user="$TOPOLOGY_MYSQL_USER" "${TOPOLOGY_MYSQL_DATABASE:-releemdb}" --execute "$query"
}

platform_clickhouse() {
    local query="$1"
    printf 'user = "%s:%s"\n' "$TOPOLOGY_CLICKHOUSE_USER" "$TOPOLOGY_CLICKHOUSE_PASSWORD" |
        timeout "${TOPOLOGY_CLICKHOUSE_QUERY_TIMEOUT_SECONDS:-30}s" curl --config - --fail --silent --show-error \
            --connect-timeout "${TOPOLOGY_CLICKHOUSE_CONNECT_TIMEOUT_SECONDS:-10}" \
            --max-time "${TOPOLOGY_CLICKHOUSE_QUERY_TIMEOUT_SECONDS:-30}" --data-binary "$query" \
            "${TOPOLOGY_CLICKHOUSE_SCHEME:-http}://${TOPOLOGY_CLICKHOUSE_HOST}:${TOPOLOGY_CLICKHOUSE_PORT:-8123}/?database=${TOPOLOGY_CLICKHOUSE_DATABASE:-releemdb_dev}"
}

db_endpoint() {
    aws_region "$1" rds describe-db-instances --db-instance-identifier "$2" \
        --query 'DBInstances[0].Endpoint.Address' --output text
}

write_agent_config() {
    local region="$1" identifier="$2" file
    file="$(state_dir)/agents/${identifier}.conf"
    mkdir -p "$(dirname "$file")"
    umask 077
    cat >"$file" <<EOF
env="dev"
apikey="${RELEEM_API_KEY}"
hostname="${identifier}"
interval_seconds=30
interval_read_config_seconds=3600
interval_generate_config_seconds=43200
mysql_user="${DB_USER}"
mysql_password="${DB_PASSWORD}"
mysql_ssl_mode=true
instance_type="aws/rds"
aws_region="${region}"
aws_rds_db="${identifier}"
query_optimization=false
memory_limit=0
EOF
    chmod 600 "$file"
    printf '%s\n' "$file"
}

addressable_instances() {
    local provisioned serverless global_east global_west source replica multi i
    provisioned="$(resource_name aurora-provisioned)"; serverless="$(resource_name aurora-serverless)"
    global_east="$(resource_name global-east)"; global_west="$(resource_name global-west)"
    source="$(resource_name rds-source)"; replica="$(resource_name rds-replica)"; multi="$(resource_name rds-multi-az)"
    for i in 1 2 3; do printf '%s|%s\n' "$PRIMARY_REGION" "${provisioned}-${i}"; done
    for i in 1 2 3; do printf '%s|%s\n' "$PRIMARY_REGION" "${serverless}-${i}"; done
    for i in 1 2; do printf '%s|%s\n' "$PRIMARY_REGION" "${global_east}-${i}"; done
    for i in 1 2; do printf '%s|%s\n' "$SECONDARY_REGION" "${global_west}-${i}"; done
    printf '%s|%s\n' "$PRIMARY_REGION" "$source"
    printf '%s|%s\n' "$PRIMARY_REGION" "$replica"
    printf '%s|%s\n' "$PRIMARY_REGION" "$multi"
}

wait_monitoring_events() {
    local region="$1" identifier="$2" resource_id file deadline
    resource_id="$(aws_region "$region" rds describe-db-instances --db-instance-identifier "$identifier" \
        --query 'DBInstances[0].DbiResourceId' --output text)"
    [[ "$resource_id" =~ ^db-[A-Za-z0-9]+$ ]] || die "missing resource ID for Enhanced Monitoring"
    file="$(state_dir)/monitoring-${region}-${identifier}.json"
    deadline=$(( $(date +%s) + WAIT_TIMEOUT_SECONDS ))
    while :; do
        aws_region "$region" logs get-log-events --log-group-name RDSOSMetrics \
            --log-stream-name "$resource_id" --limit 1 --no-start-from-head --output json >"$file" 2>/dev/null || true
        if [[ -s "$file" ]] && assert_monitoring_events "$file" "$resource_id" 2>/dev/null; then
            printf '%s\t%s\n' "$region" "$resource_id" >>"$(state_dir)/monitoring-streams.tsv"
            chmod 600 "$(state_dir)/monitoring-streams.tsv"
            rm -f "$file"
            return 0
        fi
        (($(date +%s) < deadline)) || die "Enhanced Monitoring events absent for $identifier"
        sleep "$POLL_SECONDS"
    done
}

wait_all_monitoring_events() {
    local region identifier
    while IFS='|' read -r region identifier; do
        wait_monitoring_events "$region" "$identifier"
    done < <(addressable_instances)
    jq -n --arg captured_at "$(date -u +%FT%TZ)" --argjson instances 13 \
        '{captured_at:$captured_at,log_group:"RDSOSMetrics",instances_with_events:$instances}' \
        >"$(evidence_dir)/enhanced-monitoring-ready.json"
    chmod 600 "$(evidence_dir)/enhanced-monitoring-ready.json"
}

assert_all_db_instances_safe() {
    local east west combined
    load_support_state
    east="$(state_dir)/db-instances-east.json"
    west="$(state_dir)/db-instances-west.json"
    combined="$(state_dir)/db-instances-all.json"
    aws_region "$PRIMARY_REGION" rds describe-db-instances --output json >"$east"
    aws_region "$SECONDARY_REGION" rds describe-db-instances --output json >"$west"
    jq -s --arg prefix "releem-$(run_id)-" \
        '{DBInstances:[.[].DBInstances[]|select(.DBInstanceIdentifier|startswith($prefix))]}' \
        "$east" "$west" >"$combined"
    assert_db_instance_safety "$combined" 13 "$MONITORING_ROLE_ARN"
    rm -f "$east" "$west" "$combined"
}

send_ssm_commands() {
    local region="$1" instance="$2" commands_file="$3" label="$4" parameters command_id record
    parameters="$(state_dir)/ssm-${label}-parameters.json"
    jq -Rs '{commands:split("\n")|map(select(length>0))}' "$commands_file" >"$parameters"
    chmod 600 "$parameters"
    command_id="$(aws_region "$region" ssm send-command --instance-ids "$instance" \
        --document-name AWS-RunShellScript --parameters "file://${parameters}" \
        --cloud-watch-output-config CloudWatchOutputEnabled=false \
        --query Command.CommandId --output text)"
    [[ "$command_id" =~ ^[a-f0-9-]{36}$ ]] || die "invalid SSM command ID"
    record="$(state_dir)/ssm-command-ids.tsv"
    printf '%s\t%s\t%s\n' "$region" "$instance" "$command_id" >>"$record"
    chmod 600 "$record"
    wait_until "SSM command $label" ssm_command_succeeded "$region" "$instance" "$command_id"
}

ssm_command_succeeded() {
    local status
    status="$(aws_region "$1" ssm get-command-invocation --command-id "$3" --instance-id "$2" --query Status --output text 2>/dev/null || true)"
    [[ "$status" == Success ]] && return 0
    [[ "$status" == Failed || "$status" == Cancelled || "$status" == TimedOut ]] && die "SSM command failed with status $status"
    return 1
}

start_agents() {
    local region identifier config region_dir archive commands runner prefix
    load_secret
    load_support_state
    rm -rf "$(state_dir)/packages"
    mkdir -p "$(state_dir)/packages/east/configs" "$(state_dir)/packages/west/configs"
    for region_dir in east west; do
        cat >"$(state_dir)/packages/${region_dir}/mysql.cnf" <<EOF
[client]
user=${DB_USER}
password=${DB_PASSWORD}
ssl
EOF
        chmod 600 "$(state_dir)/packages/${region_dir}/mysql.cnf"
    done
    while IFS='|' read -r region identifier; do
        config="$(write_agent_config "$region" "$identifier")"
        if [[ "$region" == "$PRIMARY_REGION" ]]; then region_dir=east; else region_dir=west; fi
        cp "$config" "$(state_dir)/packages/${region_dir}/configs/${identifier}.conf"
    done < <(addressable_instances)
    for region_dir in east west; do
        archive="$(state_dir)/packages/${region_dir}/configs.tar"
        tar -cf "$archive" -C "$(state_dir)/packages/${region_dir}" configs mysql.cnf
        chmod 600 "$archive"
        prefix="$(run_id)/${region_dir}"
        aws_region "$PRIMARY_REGION" s3api put-object --bucket "$S3_BUCKET" --key "${prefix}/agent" \
            --body "$AGENT_BINARY" --server-side-encryption AES256 >/dev/null
        aws_region "$PRIMARY_REGION" s3api put-object --bucket "$S3_BUCKET" --key "${prefix}/configs.tar" \
            --body "$archive" --server-side-encryption AES256 >/dev/null
        commands="$(state_dir)/ssm-start-${region_dir}.sh"
        if [[ "$region_dir" == east ]]; then region="$PRIMARY_REGION"; runner="$EAST_RUNNER_ID"; else region="$SECONDARY_REGION"; runner="$WEST_RUNNER_ID"; fi
        runner_ssm_commands "$region" "$S3_BUCKET" "$prefix" >"$commands"
        chmod 600 "$commands"
        send_ssm_commands "$region" "$runner" "$commands" "start-${region_dir}"
    done
}

stop_agents() {
    local commands region runner label
    [[ -f "$(support_state_file)" ]] || return 0
    load_support_state || return 0
    commands="$(state_dir)/ssm-stop.sh"
    printf '%s\n' 'pkill -TERM -f /tmp/releem-topology/agent || true' \
        'rm -rf /tmp/releem-topology' >"$commands"
    chmod 600 "$commands"
    for label in east west; do
        if [[ "$label" == east ]]; then region="$PRIMARY_REGION"; runner="${EAST_RUNNER_ID:-}"; else region="$SECONDARY_REGION"; runner="${WEST_RUNNER_ID:-}"; fi
        [[ -n "$runner" ]] && send_ssm_commands "$region" "$runner" "$commands" "stop-${label}" || true
    done
}

sid_map_query() {
    local first=1 region identifier ids=''
    validate_persistence_identity
    while IFS='|' read -r region identifier; do
        ((first)) || ids+=','
        ids+="'${identifier}'"
        first=0
    done < <(addressable_instances)
    printf '%s\n' "SELECT JSON_OBJECT('sid',sid,'provider_id',hostname) FROM servers WHERE uid=${TOPOLOGY_UID} AND hostname IN (${ids}) ORDER BY sid;"
}

wait_sid_map() {
    local output="$1" deadline count
    deadline=$(( $(date +%s) + PERSISTENCE_TIMEOUT_SECONDS ))
    while :; do
        platform_mysql "$(sid_map_query)" >"$output"
        count="$(wc -l <"$output")"
        ((count == 13)) && return 0
        (($(date +%s) < deadline)) || die "timeout resolving exact test instance SIDs"
        sleep 10
    done
}

marker_ms() { date -u +%s%3N; }

relation_value_for_provider() {
    local current="$1" map_file="$2" provider="$3" relation_type="$4" field="$5" sid
    [[ "$field" == group_key || "$field" == member_key ]] || die "invalid relation identity field"
    sid="$(jq -sr --arg id "$provider" 'map(select(.provider_id==$id))|if length==1 then .[0].sid else error("provider SID missing") end' "$map_file")"
    jq -sr --argjson sid "$sid" --arg type "$relation_type" --arg field "$field" '
      map(select(.sid==$sid and .relation_type==$type)) |
      if length==1 then .[0][$field] else error("relation identity missing") end
    ' "$current"
}

collect_transition() {
    local label="$1" marker="$2" expectation="$3" map_file="$4"
    local sids current upstream pairs observations observations_raw deadline
    sids="$(jq -sr 'map(.sid)|sort|map(tostring)|join(",")' "$map_file")"
    current="$(evidence_dir)/transitions/${marker}-${label}-current.jsonl"
    upstream="$(evidence_dir)/transitions/${marker}-${label}-upstreams.jsonl"
    observations="$(evidence_dir)/transitions/${marker}-${label}-observations.jsonl"
    observations_raw="$(state_dir)/${marker}-${label}-observations.raw.jsonl"
    mkdir -p "$(dirname "$current")"
    deadline=$(( $(date +%s) + PERSISTENCE_TIMEOUT_SECONDS ))
    while :; do
        platform_mysql "$(current_state_query "$sids" "$marker")" >"$current"
        platform_mysql "$(upstream_state_query "$sids" "$marker")" >"$upstream"
        if [[ -s "$current" ]] && [[ "$(jq -sr 'map(.sid)|unique|length' "$current")" == 13 ]] && \
            assert_transition_state "$label" "$current" "$upstream" "$expectation" "$marker" 2>/dev/null; then break; fi
        (($(date +%s) < deadline)) || die "Platform persistence timeout for $label"
        sleep 10
    done
    pairs="$(jq -sr '
      group_by(.sid) |
      if length != 13 then error("unexpected SID count") else . end |
      map(if (map(.last_rid)|unique|length)==1 and .[0].last_rid != null
          then ((.[0].sid|tostring)+":"+.[0].last_rid) else error("ambiguous RID") end) | join(",")
    ' "$current")"
    deadline=$(( $(date +%s) + PERSISTENCE_TIMEOUT_SECONDS ))
    while :; do
        platform_clickhouse "$(build_clickhouse_observation_query "$marker" "$pairs")" >"$observations_raw"
        if [[ -s "$observations_raw" ]] && assert_selected_observations "$observations_raw" "$current" "$pairs" "$marker" 13 2>/dev/null; then break; fi
        (($(date +%s) < deadline)) || die "ClickHouse exact SID/RID history timeout for $label"
        sleep 10
    done
    jq -c '{sid,rid,observed_epoch_ms,relations:(.relations|fromjson|map({relation_type,group_key,parent_group_key,member_key,role,is_writer,replication_state}))}' \
        "$observations_raw" >"$observations"
    rm -f "$observations_raw"
    chmod 600 "$current" "$upstream" "$observations" "$expectation"
}

mysql_rds_call() {
    local identifier="$1" procedure="$2" commands
    [[ "$identifier" =~ ^[a-z0-9-]+$ && "$procedure" =~ ^rds_(stop|start)_replication$ ]] || die "invalid RDS SQL transition"
    load_support_state
    commands="$(state_dir)/ssm-rds-${procedure}.sh"
    cat >"$commands" <<EOF
set -eu
endpoint=\$(aws rds describe-db-instances --region ${PRIMARY_REGION} --db-instance-identifier ${identifier} --query 'DBInstances[0].Endpoint.Address' --output text)
mysql --defaults-extra-file=/tmp/releem-topology/mysql.cnf --connect-timeout=10 --host=\"\$endpoint\" --execute 'CALL mysql.${procedure};'
EOF
    chmod 600 "$commands"
    send_ssm_commands "$PRIMARY_REGION" "$EAST_RUNNER_ID" "$commands" "$procedure"
}

force_serverless_scale() {
    local identifier="$1" commands
    load_support_state
    commands="$(state_dir)/ssm-serverless-scale.sh"
    cat >"$commands" <<EOF
set -eu
endpoint=\$(aws rds describe-db-instances --region ${PRIMARY_REGION} --db-instance-identifier ${identifier}-1 --query 'DBInstances[0].Endpoint.Address' --output text)
for n in 1 2 3 4 5 6 7 8; do mysql --defaults-extra-file=/tmp/releem-topology/mysql.cnf --connect-timeout=10 --host=\"\$endpoint\" --execute 'SELECT BENCHMARK(20000000,SHA2(UUID(),512));' >/dev/null & done
wait
EOF
    chmod 600 "$commands"
    send_ssm_commands "$PRIMARY_REGION" "$EAST_RUNNER_ID" "$commands" serverless-scale
}

exercise_matrix() {
    local map_file provisioned serverless global_id global_west replica multi before marker expectation target_writer sids group_key member_key max_acu replica_sid old_writer old_primary_members
    map_file="$(evidence_dir)/sid-map.jsonl"
    marker="$(marker_ms)"
    wait_sid_map "$map_file"
    provisioned="$(resource_name aurora-provisioned)"; serverless="$(resource_name aurora-serverless)"
    global_id="$(resource_name aurora-global)"; global_west="$(resource_name global-west)"
    replica="$(resource_name rds-replica)"; multi="$(resource_name rds-multi-az)"

    expectation="$(evidence_dir)/transitions/${marker}-aws-baseline-expectation.json"
    mkdir -p "$(dirname "$expectation")"
    jq -n '{transition:"aws-baseline"}' >"$expectation"
    collect_transition aws-baseline "$marker" "$expectation" "$map_file"

    before="$(evidence_dir)/transitions/aurora-failover-before.jsonl"
    sids="$(jq -sr 'map(.sid)|map(tostring)|join(",")' "$map_file")"
    platform_mysql "$(current_state_query "$sids" 1000000000000)" >"$before"
    target_writer="$(aws_region "$PRIMARY_REGION" rds describe-db-clusters --db-cluster-identifier "$provisioned" \
        --query 'DBClusters[0].DBClusterMembers[?IsClusterWriter==`false`]|[0].DBInstanceIdentifier' --output text)"
    marker="$(marker_ms)"
    aws_region "$PRIMARY_REGION" rds failover-db-cluster --db-cluster-identifier "$provisioned" \
        --target-db-instance-identifier "$target_writer" >/dev/null
    wait_cluster_available "$PRIMARY_REGION" "$provisioned"
    expectation="$(evidence_dir)/transitions/${marker}-aurora-provisioned-failover-expectation.json"
    group_key="$(relation_value_for_provider "$before" "$map_file" "${provisioned}-1" aurora_cluster group_key)"
    old_writer="$(jq -sr --arg group "$group_key" 'map(select(.relation_type=="aurora_cluster" and .group_key==$group and .is_writer==1))|if length==1 then .[0].member_key else error("old writer missing") end' "$before")"
    jq -n --arg group_key "$group_key" --arg old_writer "$old_writer" \
        '{group_key:$group_key,old_writer_member_key:$old_writer}' >"$expectation"
    collect_transition aurora-provisioned-failover "$marker" "$expectation" "$map_file"

    marker="$(marker_ms)"
    aws_region "$PRIMARY_REGION" rds modify-db-cluster --db-cluster-identifier "$serverless" \
        --serverless-v2-scaling-configuration "MinCapacity=${MIN_ACU},MaxCapacity=$(awk -v a="$MIN_ACU" 'BEGIN{print a+2}')" --apply-immediately >/dev/null
    wait_cluster_available "$PRIMARY_REGION" "$serverless"
    force_serverless_scale "$serverless"
    expectation="$(evidence_dir)/transitions/${marker}-aurora-serverless-scaled-expectation.json"
    group_key="$(relation_value_for_provider "$before" "$map_file" "${serverless}-1" aurora_cluster group_key)"
    max_acu="$(awk -v a="$MIN_ACU" 'BEGIN{print a+2}')"
    jq -n --arg group_key "$group_key" --argjson min_acu "$MIN_ACU" --argjson max_acu "$max_acu" \
        '{group_key:$group_key,min_acu:$min_acu,max_acu:$max_acu}' >"$expectation"
    collect_transition aurora-serverless-scaled "$marker" "$expectation" "$map_file"

    marker="$(marker_ms)"
    aws_region "$PRIMARY_REGION" rds switchover-global-cluster --global-cluster-identifier "$global_id" \
        --target-db-cluster-identifier "arn:aws:rds:${SECONDARY_REGION}:$(aws sts get-caller-identity --query Account --output text):cluster:${global_west}" >/dev/null
    wait_cluster_available "$SECONDARY_REGION" "$global_west"
    expectation="$(evidence_dir)/transitions/${marker}-aurora-global-transition-expectation.json"
    group_key="$(relation_value_for_provider "$before" "$map_file" "${global_west}-1" aurora_global_database group_key)"
    old_primary_members="$(jq -sc --arg group "$group_key" '[.[]|select(.relation_type=="aurora_global_database" and .group_key==$group and .role=="primary_cluster_member")|.member_key]' "$before")"
    jq -n --arg group_key "$group_key" --arg transition managed-global-switchover --argjson old_primary "$old_primary_members" \
        '{group_key:$group_key,supported_transition:$transition,old_primary_member_keys:$old_primary}' >"$expectation"
    collect_transition aurora-global-transition "$marker" "$expectation" "$map_file"

    marker="$(marker_ms)"; mysql_rds_call "$replica" rds_stop_replication
    expectation="$(evidence_dir)/transitions/${marker}-rds-replica-stopped-expectation.json"
    replica_sid="$(jq -sr --arg id "$replica" 'map(select(.provider_id==$id))[0].sid' "$map_file")"
    jq -n --argjson sid "$replica_sid" '{sid:$sid}' >"$expectation"
    collect_transition rds-replica-stopped "$marker" "$expectation" "$map_file"
    marker="$(marker_ms)"; mysql_rds_call "$replica" rds_start_replication
    expectation="$(evidence_dir)/transitions/${marker}-rds-replica-resumed-expectation.json"
    jq -n --argjson sid "$replica_sid" '{sid:$sid}' >"$expectation"
    collect_transition rds-replica-resumed "$marker" "$expectation" "$map_file"

    marker="$(marker_ms)"
    aws_region "$PRIMARY_REGION" rds reboot-db-instance --db-instance-identifier "$multi" --force-failover >/dev/null
    wait_instance_available "$PRIMARY_REGION" "$multi"
    expectation="$(evidence_dir)/transitions/${marker}-rds-multi-az-failover-expectation.json"
    member_key="$(relation_value_for_provider "$before" "$map_file" "$multi" rds_multi_az member_key)"
    jq -n --arg member_key "$member_key" '{member_key:$member_key}' >"$expectation"
    collect_transition rds-multi-az-failover "$marker" "$expectation" "$map_file"
}

identifier_from_arn() { printf '%s\n' "${1##*:}"; }

cleanup_resources() {
    "$CLEANUP_RUNNING" && return 0
    CLEANUP_RUNNING=true
    stop_agents
    local dir region arn id sg global_id cluster_arn cluster_region runner account bucket runner_role runner_profile monitor_role deadline post
    dir="$(state_dir)/cleanup"
    mkdir -p "$dir"
    capture_inventory "$dir/before.json" || true

    # Addressable databases must disappear before cluster/global/network dependencies.
    for region in "$PRIMARY_REGION" "$SECONDARY_REGION"; do
        inventory_region "$region" "$dir/${region}.json" || continue
        while IFS= read -r arn; do
            id="$(identifier_from_arn "$arn")"
            aws_region "$region" rds describe-db-instances --db-instance-identifier "$id" \
                --query 'DBInstances[0].DbiResourceId' --output text 2>/dev/null |
                awk -v region="$region" '/^db-[A-Za-z0-9]+$/ {print region "\t" $0}' >>"$(state_dir)/monitoring-streams.tsv" || true
            aws_region "$region" rds delete-db-instance --db-instance-identifier "$id" --skip-final-snapshot --delete-automated-backups >/dev/null 2>&1 || true
        done < <(jq -r --arg source ":$(resource_name rds-source)" '.instances[]|select(endswith($source)|not)' "$dir/${region}.json")
        while IFS= read -r arn; do
            id="$(identifier_from_arn "$arn")"
            aws_region "$region" rds wait db-instance-deleted --db-instance-identifier "$id" >/dev/null 2>&1 || true
        done < <(jq -r --arg source ":$(resource_name rds-source)" '.instances[]|select(endswith($source)|not)' "$dir/${region}.json")
        while IFS= read -r arn; do
            id="$(identifier_from_arn "$arn")"
            aws_region "$region" rds describe-db-instances --db-instance-identifier "$id" \
                --query 'DBInstances[0].DbiResourceId' --output text 2>/dev/null |
                awk -v region="$region" '/^db-[A-Za-z0-9]+$/ {print region "\t" $0}' >>"$(state_dir)/monitoring-streams.tsv" || true
            aws_region "$region" rds delete-db-instance --db-instance-identifier "$id" --skip-final-snapshot --delete-automated-backups >/dev/null 2>&1 || true
            aws_region "$region" rds wait db-instance-deleted --db-instance-identifier "$id" >/dev/null 2>&1 || true
        done < <(jq -r --arg source ":$(resource_name rds-source)" '.instances[]|select(endswith($source))' "$dir/${region}.json")
    done
    if [[ -f "$(state_dir)/monitoring-streams.tsv" ]]; then
        while IFS=$'\t' read -r region id; do
            [[ -n "$id" ]] && aws_region "$region" logs delete-log-stream --log-group-name RDSOSMetrics --log-stream-name "$id" >/dev/null 2>&1 || true
        done <"$(state_dir)/monitoring-streams.tsv"
    fi

    # Aurora Global members must be detached before regional clusters can be removed.
    inventory_global_clusters "$dir/globals.json" || printf '[]\n' >"$dir/globals.json"
    while IFS= read -r arn; do
        global_id="$(identifier_from_arn "$arn")"
        while IFS= read -r cluster_arn; do
            [[ -n "$cluster_arn" ]] || continue
            cluster_region="${cluster_arn#arn:aws:rds:}"; cluster_region="${cluster_region%%:*}"
            aws_region "$cluster_region" rds remove-from-global-cluster \
                --global-cluster-identifier "$global_id" --db-cluster-identifier "$cluster_arn" >/dev/null 2>&1 || true
            wait_until "global member detachment" global_member_absent "$global_id" "$cluster_arn"
        done < <(aws_region "$PRIMARY_REGION" rds describe-global-clusters --global-cluster-identifier "$global_id" \
            --output json 2>/dev/null | jq -r '.GlobalClusters[0].GlobalClusterMembers|sort_by(.IsWriter)|.[].DBClusterArn')
    done < <(jq -r '.[].arn' "$dir/globals.json")

    for region in "$PRIMARY_REGION" "$SECONDARY_REGION"; do
        [[ -f "$dir/${region}.json" ]] || continue
        while IFS= read -r arn; do
            id="$(identifier_from_arn "$arn")"
            aws_region "$region" rds delete-db-snapshot --db-snapshot-identifier "$id" >/dev/null 2>&1 || true
        done < <(jq -r '.snapshots[]' "$dir/${region}.json")
        while IFS= read -r arn; do
            id="$(identifier_from_arn "$arn")"
            aws_region "$region" rds delete-db-cluster --db-cluster-identifier "$id" --skip-final-snapshot >/dev/null 2>&1 || true
        done < <(jq -r '.clusters[]' "$dir/${region}.json")
        while IFS= read -r arn; do
            id="$(identifier_from_arn "$arn")"
            aws_region "$region" rds wait db-cluster-deleted --db-cluster-identifier "$id" >/dev/null 2>&1 || true
        done < <(jq -r '.clusters[]' "$dir/${region}.json")
    done
    while IFS= read -r arn; do
        id="$(identifier_from_arn "$arn")"
        aws_region "$PRIMARY_REGION" rds delete-global-cluster --global-cluster-identifier "$id" >/dev/null 2>&1 || true
    done < <(jq -r '.[].arn' "$dir/globals.json")

    # Stop SSM work first, then terminate runners and wait for their tagged ENIs.
    [[ -f "$(support_state_file)" ]] && load_support_state || true
    for region in "$PRIMARY_REGION" "$SECONDARY_REGION"; do
        while IFS= read -r runner; do
            [[ -n "$runner" ]] || continue
            aws_region "$region" ec2 terminate-instances --instance-ids "$runner" >/dev/null 2>&1 || true
            aws_region "$region" ec2 wait instance-terminated --instance-ids "$runner" >/dev/null 2>&1 || true
        done < <(aws_region "$region" ec2 describe-instances \
            --filters "Name=tag:${TAG_RUN_KEY},Values=$(run_id)" "Name=tag:${TAG_MANAGED_KEY},Values=true" \
            "Name=instance-state-name,Values=pending,running,stopping,stopped,shutting-down" \
            --query 'Reservations[].Instances[].InstanceId' --output text 2>/dev/null | tr '\t' '\n')
        wait_until "runner ENI deletion in $region" runner_enis_absent "$region"
    done

    for region in "$PRIMARY_REGION" "$SECONDARY_REGION"; do
        inventory_region "$region" "$dir/${region}-late.json" || continue
        while IFS= read -r arn; do
            id="$(identifier_from_arn "$arn")"
            if [[ "$arn" == *":cluster-pg:"* ]]; then
                aws_region "$region" rds delete-db-cluster-parameter-group --db-cluster-parameter-group-name "$id" >/dev/null 2>&1 || true
            else
                aws_region "$region" rds delete-db-parameter-group --db-parameter-group-name "$id" >/dev/null 2>&1 || true
            fi
        done < <(jq -r '.parameter_groups[]' "$dir/${region}-late.json")
        while IFS= read -r arn; do
            sg="${arn##*/}"
            aws_region "$region" ec2 delete-security-group --group-id "$sg" >/dev/null 2>&1 || true
        done < <(jq -r '.security_groups[]' "$dir/${region}-late.json")
        while IFS= read -r arn; do
            sg="${arn##*/}"
            aws_region "$region" ec2 delete-security-group --group-id "$sg" >/dev/null 2>&1 || true
        done < <(jq -r '.security_groups[]' "$dir/${region}-late.json")
        while IFS= read -r arn; do
            id="$(identifier_from_arn "$arn")"
            aws_region "$region" rds delete-db-subnet-group --db-subnet-group-name "$id" >/dev/null 2>&1 || true
        done < <(jq -r '.subnet_groups[]' "$dir/${region}-late.json")
    done

    # Private delivery objects precede the bucket; instance profile precedes roles.
    account="$(aws sts get-caller-identity --query Account --output text 2>/dev/null || true)"
    bucket="${S3_BUCKET:-releem-topology-$(printf '%s' "$account" | sha256sum | cut -c1-12)-$(run_id)}"
    runner_role="${RUNNER_ROLE:-$(resource_name runner-role)}"
    runner_profile="${RUNNER_PROFILE:-$(resource_name runner-profile)}"
    monitor_role="${MONITORING_ROLE:-$(resource_name monitoring-role)}"
    if [[ "$bucket" =~ ^[a-z0-9.-]{3,63}$ ]] && aws s3api head-bucket --bucket "$bucket" >/dev/null 2>&1; then
        aws_region "$PRIMARY_REGION" s3api list-objects-v2 --bucket "$bucket" --output json |
            jq '{Objects:[.Contents[]?|{Key:.Key}],Quiet:true}' >"$dir/delete-objects.json"
        if jq -e '.Objects|length>0' "$dir/delete-objects.json" >/dev/null; then
            aws_region "$PRIMARY_REGION" s3api delete-objects --bucket "$bucket" --delete "file://$dir/delete-objects.json" >/dev/null 2>&1 || true
        fi
        aws_region "$PRIMARY_REGION" s3api delete-bucket --bucket "$bucket" >/dev/null 2>&1 || true
    fi
    aws iam remove-role-from-instance-profile --instance-profile-name "$runner_profile" --role-name "$runner_role" >/dev/null 2>&1 || true
    aws iam delete-instance-profile --instance-profile-name "$runner_profile" >/dev/null 2>&1 || true
    aws iam delete-role-policy --role-name "$runner_role" --policy-name ReleemTopologyRunnerRead >/dev/null 2>&1 || true
    aws iam detach-role-policy --role-name "$runner_role" --policy-arn arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore >/dev/null 2>&1 || true
    aws iam detach-role-policy --role-name "$monitor_role" --policy-arn arn:aws:iam::aws:policy/service-role/AmazonRDSEnhancedMonitoringRole >/dev/null 2>&1 || true
    aws iam delete-role --role-name "$runner_role" >/dev/null 2>&1 || true
    aws iam delete-role --role-name "$monitor_role" >/dev/null 2>&1 || true

    rm -rf "$(state_dir)/agents" "$(state_dir)/packages"
    rm -f "$(secret_file)" "$(state_dir)"/create-*.json "$(state_dir)"/ssm-* \
        "$(state_dir)"/runner-*.json "$(state_dir)"/runner-policy.json "$(state_dir)"/trust.json 2>/dev/null || true
    post="$(evidence_dir)/post-cleanup-inventory.json"
    deadline=$(( $(date +%s) + WAIT_TIMEOUT_SECONDS ))
    while :; do
        capture_inventory "$post" || true
        if [[ -s "$post" ]] && assert_inventory_empty "$post" 2>/dev/null && monitoring_streams_absent; then break; fi
        (($(date +%s) < deadline)) || { CLEANUP_RUNNING=false; die "cleanup did not reach terminal absence"; }
        sleep "$POLL_SECONDS"
    done
    assert_inventory_matches "$(evidence_dir)/preflight/inventory-before.json" "$post"
    rm -rf "$(state_dir)"
    CLEANUP_ARMED=false
    CLEANUP_RUNNING=false
}

global_member_absent() {
    [[ "$(aws_region "$PRIMARY_REGION" rds describe-global-clusters --global-cluster-identifier "$1" \
        --query "length(GlobalClusters[0].GlobalClusterMembers[?DBClusterArn=='$2'])" --output text 2>/dev/null || printf 0)" == 0 ]]
}

runner_enis_absent() {
    [[ "$(aws_region "$1" ec2 describe-network-interfaces \
        --filters "Name=tag:${TAG_RUN_KEY},Values=$(run_id)" "Name=tag:${TAG_MANAGED_KEY},Values=true" \
        --query 'length(NetworkInterfaces)' --output text 2>/dev/null || printf 0)" == 0 ]]
}

monitoring_streams_absent() {
    local region id count
    [[ -f "$(state_dir)/monitoring-streams.tsv" ]] || return 0
    while IFS=$'\t' read -r region id; do
        count="$(aws_region "$region" logs describe-log-streams --log-group-name RDSOSMetrics \
            --log-stream-name-prefix "$id" --query "length(logStreams[?logStreamName=='${id}'])" --output text 2>/dev/null || printf 0)"
        [[ "$count" == 0 ]] || return 1
    done <"$(state_dir)/monitoring-streams.tsv"
}

on_exit() {
    local status=$?
    if "$CLEANUP_ARMED"; then
        set +e
        cleanup_resources
        local cleanup_status=$?
        set -e
        ((status == 0 && cleanup_status != 0)) && status=$cleanup_status
    else
        stop_agents
        rm -f "$(secret_file)" 2>/dev/null || true
    fi
    return "$status"
}

run_matrix() {
    validate_run_id
    require_runtime
    for command in aws jq openssl mysql curl timeout tar sha256sum; do require_command "$command"; done
    trap on_exit EXIT INT TERM HUP
    preflight
    create_matrix
    assert_all_db_instances_safe
    wait_all_monitoring_events
    start_agents
    exercise_matrix
    cleanup_resources
    trap - EXIT INT TERM HUP
}

inventory() {
    validate_run_id
    require_command aws
    require_command jq
    local file
    file="$(evidence_dir)/inventory.json"
    capture_inventory "$file"
    jq '{instances:(.instances|length),clusters:(.clusters|length),global_clusters:(.global_clusters|length),parameter_groups:(.parameter_groups|length),security_groups:(.security_groups|length),subnet_groups:(.subnet_groups|length),snapshots:(.snapshots|length)}' "$file"
}

destroy() {
    validate_run_id
    [[ "${AWS_TOPOLOGY_CONFIRM_DESTROY:-}" == "$(run_id)" ]] ||
        die "AWS_TOPOLOGY_CONFIRM_DESTROY must equal the exact run ID"
    require_command aws
    require_command jq
    CLEANUP_ARMED=true
    cleanup_resources
}

main() {
    case "${1:-help}" in
        help|-h|--help) usage ;;
        preflight) shift; [[ $# -eq 0 ]] || die "preflight takes no arguments"; preflight ;;
        run) shift; [[ $# -eq 0 ]] || die "run takes no arguments"; run_matrix ;;
        inventory) shift; [[ $# -eq 0 ]] || die "inventory takes no arguments"; inventory ;;
        destroy) shift; [[ $# -eq 0 ]] || die "destroy takes no arguments"; destroy ;;
        *) usage >&2; die "unknown command: $1" ;;
    esac
}

main "$@"
