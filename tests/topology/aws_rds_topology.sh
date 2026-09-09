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
readonly RUNTIME_DEADLINE_SECONDS="${AWS_TOPOLOGY_RUNTIME_DEADLINE_SECONDS:-14400}"
readonly CLEANUP_DEADLINE_SECONDS="${AWS_TOPOLOGY_CLEANUP_DEADLINE_SECONDS:-3600}"
readonly API_TIMEOUT_SECONDS="${AWS_TOPOLOGY_API_TIMEOUT_SECONDS:-180}"
readonly CLEANUP_API_TIMEOUT_SECONDS="${AWS_TOPOLOGY_CLEANUP_API_TIMEOUT_SECONDS:-120}"
readonly CLEANUP_DELETE_RESERVE_SECONDS="${AWS_TOPOLOGY_CLEANUP_DELETE_RESERVE_SECONDS:-600}"

CLEANUP_ARMED=false
CLEANUP_RUNNING=false
CLEANUP_DONE=false
AGENT_PIDS=()
AWS_IDENTITY_MAP=''
WATCHDOG_PID=''
PENDING_SIGNAL_STATUS=0
RUN_DEADLINE_EPOCH=0
CLEANUP_DEADLINE_EPOCH=0
CLEANUP_BUDGET_EXHAUSTED=false
AWS_LOCK_HELD=false

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
and AWS APIs through NAT. No inbound runner access is used.

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
aws_lock_file() { printf '%s/aws-cli.lock\n' "$(state_dir)"; }

with_aws_lock() {
    local lock_file lock_fd status=0
    if "$AWS_LOCK_HELD"; then
        "$@"
        return
    fi
    lock_file="$(aws_lock_file)"
    mkdir -p "$(dirname "$lock_file")"
    chmod 700 "$(dirname "$lock_file")"
    exec {lock_fd}>"$lock_file"
    flock "$lock_fd"
    AWS_LOCK_HELD=true
    "$@" || status=$?
    AWS_LOCK_HELD=false
    flock -u "$lock_fd"
    exec {lock_fd}>&-
    return "$status"
}

run_aws_api() {
    local timeout_seconds="$1"
    shift
    if declare -F aws >/dev/null; then
        aws "$@"
        return
    fi
    timeout --foreground --kill-after=5s "${timeout_seconds}s" aws "$@"
}

aws_region() {
    local region="$1"
    shift
    if "$CLEANUP_RUNNING"; then
        cleanup_aws_region "$region" "$@"
        return
    fi
    with_aws_lock run_aws_api "$API_TIMEOUT_SECONDS" --no-cli-pager --region "$region" "$@"
}

aws_global() {
    if "$CLEANUP_RUNNING"; then
        cleanup_aws "$@"
        return
    fi
    with_aws_lock run_aws_api "$API_TIMEOUT_SECONDS" "$@"
}

classify_s3_head_result() {
    local status="$1" error_file="$2" operation="${3:-HeadBucket}"
    if [[ "$status" -eq 0 ]]; then printf 'present\n'; return 0; fi
    [[ "$status" -eq 254 && "$operation" =~ ^Head(Bucket|Object)$ ]] || return 1
    local errors='(404|NotFound)'
    [[ "$operation" == HeadObject ]] && errors='(404|NotFound|NoSuchKey)'
    if grep -Eq "^(aws: \\[ERROR\\]: )?An error occurred \\(${errors}\\) when calling the ${operation} operation: (Not Found|[^[:space:]].*)$" "$error_file"; then
        printf 'absent\n'; return 0
    fi
    return 1
}

s3_bucket_presence() {
    local bucket="$1" error_file status
    error_file="$(state_dir)/s3-head-error.txt"
    mkdir -p "$(state_dir)"; chmod 700 "$(state_dir)"
    if aws_global s3api head-bucket --bucket "$bucket" >/dev/null 2>"$error_file"; then status=0; else status=$?; fi
    chmod 600 "$error_file"
    classify_s3_head_result "$status" "$error_file" HeadBucket || die "S3 bucket presence is ambiguous; refusing to continue"
}

classify_iam_get_result() {
    local status="$1" error_file="$2" operation="${3:-GetRole}"
    if ((status == 0)); then printf 'present\n'; return 0; fi
    [[ "$status" -eq 254 && "$operation" =~ ^Get(Role|InstanceProfile)$ ]] || return 1
    if grep -Eq "^(aws: \\[ERROR\\]: )?An error occurred \\(NoSuchEntity\\) when calling the ${operation} operation: [^[:space:]].*$" "$error_file"; then
        printf 'absent\n'; return 0
    fi
    return 1
}

iam_entity_presence() {
    local kind="$1" name="$2" error_file operation status=0
    error_file="$(state_dir)/iam-${kind}-error"
    mkdir -p "$(state_dir)"
    case "$kind" in
        role) aws_global iam get-role --role-name "$name" >/dev/null 2>"$error_file" || status=$?; operation=GetRole ;;
        profile) aws_global iam get-instance-profile --instance-profile-name "$name" >/dev/null 2>"$error_file" || status=$?; operation=GetInstanceProfile ;;
        *) return 1 ;;
    esac
    classify_iam_get_result "$status" "$error_file" "$operation"
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

select_smallest_common_global_orderable_class() {
    jq -ser '
      def size_rank:
        if . == "micro" then 0 elif . == "small" then 1 elif . == "medium" then 2
        elif . == "large" then 3 elif . == "xlarge" then 4
        elif test("^[0-9]+xlarge$") then (capture("^(?<n>[0-9]+)xlarge$").n|tonumber)+4
        else 1000 end;
      ([.[0].OrderableDBInstanceOptions[]|select(.SupportsGlobalDatabases == true)|.DBInstanceClass]|unique) as $east |
      ([.[1].OrderableDBInstanceOptions[]|select(.SupportsGlobalDatabases == true)|.DBInstanceClass]|unique) as $west |
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

validate_runtime_deadline() {
    [[ "$1" =~ ^[0-9]+$ ]] && (( $1 >= 600 && $1 <= 21600 )) ||
        die "AWS_TOPOLOGY_RUNTIME_DEADLINE_SECONDS must be between 600 and 21600"
}

validate_cleanup_deadline() {
    [[ "$1" =~ ^[0-9]+$ ]] && ((CLEANUP_DELETE_RESERVE_SECONDS + 10 <= $1 && $1 <= 10800)) ||
        die "cleanup deadline must exceed the deletion reserve and be at most 3 hours"
}

initialize_cleanup_deadline() {
    ((CLEANUP_DEADLINE_EPOCH > 0)) || CLEANUP_DEADLINE_EPOCH=$(( $(date +%s) + CLEANUP_DEADLINE_SECONDS ))
}

cleanup_remaining_seconds() {
    local remaining=$((CLEANUP_DEADLINE_EPOCH - $(date +%s)))
    ((remaining > 0)) || { printf '0\n'; return 1; }
    printf '%s\n' "$remaining"
}

cleanup_wait_remaining_seconds() {
    local remaining
    remaining="$(cleanup_remaining_seconds)" || return 1
    remaining=$((remaining - CLEANUP_DELETE_RESERVE_SECONDS))
    ((remaining > 0)) || { printf '0\n'; return 1; }
    printf '%s\n' "$remaining"
}

cleanup_poll_seconds() {
    local poll="$1" remaining="$2"
    ((poll <= remaining)) || poll="$remaining"
    printf '%s\n' "$poll"
}

cleanup_bounded_sleep() {
    local requested="$1" remaining sleep_seconds
    if ((CLEANUP_DEADLINE_EPOCH <= 0)); then
        sleep "$requested"
        return 0
    fi
    remaining="$(cleanup_remaining_seconds)" || return 1
    sleep_seconds="$(cleanup_poll_seconds "$requested" "$remaining")"
    ((sleep_seconds > 0)) || return 1
    sleep "$sleep_seconds"
}

cleanup_wait_until() {
    local description="$1" status remaining sleep_seconds
    shift
    while remaining="$(cleanup_wait_remaining_seconds)"; do
        "$@" && return 0
        status=$?
        [[ "$status" -ne 2 ]] || { log "$description failed"; return 1; }
        sleep_seconds="$POLL_SECONDS"
        ((sleep_seconds <= remaining)) || sleep_seconds="$remaining"
        ((sleep_seconds > 0)) && sleep "$sleep_seconds"
    done
    CLEANUP_BUDGET_EXHAUSTED=true
    log "cleanup budget exhausted while waiting for $description; run exact-confirmed destroy"
    return 1
}

cleanup_aws_region() {
    local region="$1" remaining timeout_seconds
    shift
    remaining="$(cleanup_remaining_seconds)" || return 124
    timeout_seconds="$CLEANUP_API_TIMEOUT_SECONDS"; ((timeout_seconds <= remaining)) || timeout_seconds="$remaining"
    with_aws_lock timeout --foreground --kill-after=5s "${timeout_seconds}s" aws --no-cli-pager --region "$region" "$@"
}

cleanup_aws() {
    local remaining timeout_seconds
    remaining="$(cleanup_remaining_seconds)" || return 124
    timeout_seconds="$CLEANUP_API_TIMEOUT_SECONDS"; ((timeout_seconds <= remaining)) || timeout_seconds="$remaining"
    with_aws_lock timeout --foreground --kill-after=5s "${timeout_seconds}s" aws --no-cli-pager "$@"
}

cleanup_db_instance_absent() {
    local region="$1" identifier="$2" response status=0
    response="$(cleanup_aws_region "$region" rds describe-db-instances \
        --db-instance-identifier "$identifier" --output json 2>&1)" || status=$?
    if ((status == 0)); then return 1; fi
    if ((status == 254)) && grep -Eq '^(aws: \[ERROR\]: )?An error occurred \(DBInstanceNotFound(Fault)?\) when calling the DescribeDBInstances operation: [^[:space:]].*$' <<<"$response"; then
        return 0
    fi
    return 2
}

cleanup_db_cluster_absent() {
    local region="$1" identifier="$2" response status=0
    response="$(cleanup_aws_region "$region" rds describe-db-clusters \
        --db-cluster-identifier "$identifier" --output json 2>&1)" || status=$?
    if ((status == 0)); then return 1; fi
    if ((status == 254)) && grep -Eq '^(aws: \[ERROR\]: )?An error occurred \(DBClusterNotFound(Fault)?\) when calling the DescribeDBClusters operation: [^[:space:]].*$' <<<"$response"; then
        return 0
    fi
    return 2
}

cleanup_runner_absent() {
    local region="$1" identifier="$2" response status=0 state
    response="$(cleanup_aws_region "$region" ec2 describe-instances --instance-ids "$identifier" --output json 2>&1)" || status=$?
    if ((status == 0)); then
        state="$(jq -r '.Reservations[0].Instances[0].State.Name // "absent"' <<<"$response")" || return 2
        [[ "$state" == terminated || "$state" == absent ]] && return 0
        return 1
    fi
    if ((status == 254)) && grep -Eq '^(aws: \[ERROR\]: )?An error occurred \(InvalidInstanceID[.]NotFound\) when calling the DescribeInstances operation: [^[:space:]].*$' <<<"$response"; then
        return 0
    fi
    return 2
}

run_cleanup_steps() {
    local step failed=0
    for step in "$@"; do
        ("$step") || { log "cleanup step failed: $step"; failed=1; }
    done
    return "$failed"
}

assert_no_deterministic_collisions() {
    jq -e '(.expected|type)=="array" and (.existing|type)=="array" and (.existing|length)==0' "$1" >/dev/null ||
        die "deterministic AWS resource name collision"
}

assert_deterministic_resources_owned() {
    local collisions="$1" owned="$2"
    jq -e -n --slurpfile collisions "$collisions" --slurpfile owned "$owned" '
      def basename: split(":")[-1] | split("/")[-1];
      (($owned[0] | [.[]?[]? | select(type=="string") | basename]) +
        ($collisions[0].direct_owned // []) | unique) as $owned_names |
      all($collisions[0].existing[]; . as $name | ($owned_names|index($name)) != null)
    ' >/dev/null || die "deterministic resource exists without exact run ownership"
}

assert_private_subnet_egress() {
    local file="$1" expected="$2"
    jq -e --argjson expected "$expected" '
      (.subnets|length)==$expected and all(.subnets[];
        any(.routes[]?; .destination=="0.0.0.0/0" and
          (.nat_gateway_id|type)=="string" and (.nat_gateway_id|startswith("nat-")) and .state=="active"))
    ' "$file" >/dev/null || die "each private runner subnet requires an active NAT default route"
}

assert_matrix_quota_headroom() {
    jq -e '
      [.["us-east-1"],.["us-west-2"]] | all(.[];
        [.db_instances,.db_clusters,.ec2_instances] | all(.[];
          (.quota|type)=="number" and (.used|type)=="number" and (.required|type)=="number" and
          (.quota - .used) >= .required))
    ' "$1" >/dev/null || die "service quota headroom is below the exact matrix requirement"
}

current_standard_vcpus() {
    local region="$1" instances types type count vcpus total=0
    instances="$(aws_region "$region" ec2 describe-instances --filters Name=instance-state-name,Values=pending,running --output json)"
    types="$(jq -r '[.Reservations[].Instances[].InstanceType]|unique[]' <<<"$instances")"
    while IFS= read -r type; do
        [[ -n "$type" ]] || continue
        [[ "$type" =~ ^(f|g|inf|p|trn)[0-9] ]] && continue
        count="$(jq --arg type "$type" '[.Reservations[].Instances[]|select(.InstanceType==$type)]|length' <<<"$instances")"
        vcpus="$(aws_region "$region" ec2 describe-instance-types --instance-types "$type" --query 'InstanceTypes[0].VCpuInfo.DefaultVCpus' --output text)"
        total=$((total + count * vcpus))
    done <<<"$types"
    printf '%s\n' "$total"
}

write_matrix_quota_headroom() {
    local out="$1" east_rds="$2" west_rds="$3" east_ec2="$4" west_ec2="$5" collision_dir="$6"
    local east_vcpu west_vcpu
    east_vcpu="$(current_standard_vcpus "$PRIMARY_REGION")"
    west_vcpu="$(current_standard_vcpus "$SECONDARY_REGION")"
    jq -n --argjson east_vcpu "$east_vcpu" --argjson west_vcpu "$west_vcpu" \
      --slurpfile er "$east_rds" --slurpfile wr "$west_rds" --slurpfile ee "$east_ec2" --slurpfile we "$west_ec2" \
      --slurpfile ed "$collision_dir/east-db.json" --slurpfile wd "$collision_dir/west-db.json" \
      --slurpfile ec "$collision_dir/east-clusters.json" --slurpfile wc "$collision_dir/west-clusters.json" '
      def quota($doc;$name): [$doc.Quotas[]|select(.QuotaName==$name)|.Value]|first;
      {"us-east-1":{
          db_instances:{quota:quota($er[0];"DB instances"),used:($ed[0].DBInstances|length),required:11},
          db_clusters:{quota:quota($er[0];"DB clusters"),used:($ec[0].DBClusters|length),required:3},
          ec2_instances:{quota:quota($ee[0];"Running On-Demand Standard (A, C, D, H, I, M, R, T, Z) instances"),used:$east_vcpu,required:2}},
        "us-west-2":{
          db_instances:{quota:quota($wr[0];"DB instances"),used:($wd[0].DBInstances|length),required:2},
          db_clusters:{quota:quota($wr[0];"DB clusters"),used:($wc[0].DBClusters|length),required:1},
          ec2_instances:{quota:quota($we[0];"Running On-Demand Standard (A, C, D, H, I, M, R, T, Z) instances"),used:$west_vcpu,required:2}}}' >"$out"
    chmod 600 "$out"
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

assert_destroy_support_ownership() {
    local file="$1" object_arn="$2" runner_role="$3" monitoring_role="$4"
    jq -e --arg run "$(run_id)" --arg run_key "$TAG_RUN_KEY" --arg managed_key "$TAG_MANAGED_KEY" \
        --arg object_arn "$object_arn" --arg runner_role "$runner_role" --arg monitoring_role "$monitoring_role" '
      def owned: (.tags[$run_key]==$run and .tags[$managed_key]=="true");
      def absent_or_owned: (.exists==false or owned);
      (.bucket|absent_or_owned) and
      (.runner_role|absent_or_owned) and
      (.monitoring_role|absent_or_owned) and
      (.instance_profile|absent_or_owned) and
      (if .runner_role.exists then
        .runner_role.path=="/releem-topology/" and
        .runner_role.trust_policy=={Version:"2012-10-17",Statement:[{Effect:"Allow",Principal:{Service:"ec2.amazonaws.com"},Action:"sts:AssumeRole"}]} and
        .runner_role.attached_policies==["arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"] and
        .runner_role.inline_policy_names==["ReleemTopologyRunnerRead"] and
        (.runner_role.inline_policy_name=="ReleemTopologyRunnerRead" and
          .runner_role.inline_policy=={Version:"2012-10-17",Statement:[
            {Sid:"ReadRunObjects",Effect:"Allow",Action:["s3:GetObject"],Resource:[$object_arn]},
            {Sid:"ReadEnhancedMonitoring",Effect:"Allow",Action:["logs:GetLogEvents"],Resource:["arn:aws:logs:*:*:log-group:RDSOSMetrics:log-stream:*"]},
            {Sid:"DescribeRDS",Effect:"Allow",Action:["rds:Describe*"],Resource:["*"]}
          ]}
        )
       else true end) and
      (if .monitoring_role.exists then
        .monitoring_role.path=="/releem-topology/" and
        .monitoring_role.trust_policy=={Version:"2012-10-17",Statement:[{Effect:"Allow",Principal:{Service:"monitoring.rds.amazonaws.com"},Action:"sts:AssumeRole"}]} and
        .monitoring_role.attached_policies==["arn:aws:iam::aws:policy/service-role/AmazonRDSEnhancedMonitoringRole"] and
        .monitoring_role.inline_policy_names==[]
       else true end) and
      (if .instance_profile.exists then .instance_profile.roles==[$runner_role] else true end)
    ' "$file" >/dev/null || die "destroy refused: support resource ownership or relationship mismatch"
}

bucket_lifecycle_configuration() {
    jq -n --arg prefix "$(run_id)/" '{Rules:[{ID:"ExpireBootstrapObjects",Status:"Enabled",Filter:{Prefix:$prefix},Expiration:{Days:1},NoncurrentVersionExpiration:{NoncurrentDays:1},AbortIncompleteMultipartUpload:{DaysAfterInitiation:1}}]}'
}

bootstrap_secret_object_keys() {
    printf '%s/east/configs.tar\n%s/west/configs.tar\n' "$(run_id)" "$(run_id)"
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
for config in /tmp/releem-topology/configs/*.conf; do
    /tmp/releem-topology/agent -f -config "\$config" >>/tmp/releem-topology/agent.log 2>&1
done
sleep 30
: >/tmp/releem-topology/agent-pids
for config in /tmp/releem-topology/configs/*.conf; do
    nohup /tmp/releem-topology/agent -config "\$config" </dev/null >>/tmp/releem-topology/agent.log 2>&1 &
    printf '%s\n' "\$!" >>/tmp/releem-topology/agent-pids
done
sleep 5
while IFS= read -r pid; do kill -0 "\$pid"; done </tmp/releem-topology/agent-pids
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

assert_read_replica_parameter_group() {
    local file="$1" expected="$2"
    jq -e --arg expected "$expected" '
      (.DBInstances|length)==1 and
      (.DBInstances[0].DBParameterGroups|length)==1 and
      .DBInstances[0].DBParameterGroups[0].DBParameterGroupName==$expected and
      .DBInstances[0].DBParameterGroups[0].ParameterApplyStatus=="in-sync"
    ' "$file" >/dev/null || die "read replica did not inherit the expected parameter group"
}

assert_serverless_scale_evidence() {
    local file="$1" max_attempts="$2"
    jq -e --argjson max_attempts "$max_attempts" '
      .attempts>=1 and .attempts<=$max_attempts and
      ([.before.capacity,.during.capacity,.after.capacity]|all(type=="number")) and
      ([.before.acu_utilization,.during.acu_utilization,.after.acu_utilization]|all(type=="number")) and
      .during.capacity > .before.capacity
    ' "$file" >/dev/null || die "Serverless v2 capacity did not increase under bounded load"
}

assert_global_switchover_converged() {
    local file="$1" target="$2" old="$3"
    jq -e --arg target "$target" --arg old "$old" '
      (.GlobalClusters[0] // .) as $g |
      $g.Status=="available" and
      ([$g.GlobalClusterMembers[]|select(.IsWriter==true)]|length)==1 and
      any($g.GlobalClusterMembers[];.DBClusterArn==$target and .IsWriter==true) and
      any($g.GlobalClusterMembers[];.DBClusterArn==$old and .IsWriter==false) and
      all($g.GlobalClusterMembers[]|select(.IsWriter==false); .SynchronizationStatus=="connected") and
      all($g.GlobalClusterMembers[]|select(.IsWriter==true);
        (.SynchronizationStatus==null or .SynchronizationStatus=="connected"))
    ' "$file" >/dev/null
}

wait_global_switchover() {
    local region="$1" global_id="$2" target="$3" old="$4" response
    while ((RUN_DEADLINE_EPOCH == 0 || $(date +%s) < RUN_DEADLINE_EPOCH)); do
        response="$(aws_region "$region" rds describe-global-clusters --global-cluster-identifier "$global_id" --output json)" || return 1
        if assert_global_switchover_converged <(printf '%s\n' "$response") "$target" "$old"; then return 0; fi
        sleep "$POLL_SECONDS"
    done
    return 1
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
          map({relation_type,group_key,parent_group_key,member_key,primary_member_key,role,is_writer,is_reader,replication_state})|
          sort_by(.relation_type,.group_key)) as $wanted |
        ($actual|map({relation_type,group_key,parent_group_key,member_key,primary_member_key,role,is_writer,is_reader,replication_state})|
          sort_by(.relation_type,.group_key))==$wanted)
    ' "$observations" >/dev/null || die "ClickHouse observations are missing, duplicated, or relation-invalid"
}

assert_exact_sid_contract() {
    local current="$1" upstreams="$2" expected="$3"
    jq -s -e --slurpfile upstreams "$upstreams" --slurpfile expected "$expected" '
      . as $current | ($expected[0]) as $e |
      all($current[]; .last_seen_epoch_ms >= $e.marker_epoch_ms) and
      all($upstreams[]; .last_seen_epoch_ms >= $e.marker_epoch_ms) and
      def relation: {sid,relation_type,group_key,parent_group_key,member_key,primary_member_key,role,is_writer,is_reader,replication_state};
      def upstream: {sid,channel_key,upstream_member_key,replication_state};
      ($e.relations|map(relation)|sort_by(.sid,.relation_type,.group_key,.member_key)) ==
        ($current|map(relation)|sort_by(.sid,.relation_type,.group_key,.member_key)) and
      ($e.upstreams|map(upstream)|sort_by(.sid,.channel_key,.upstream_member_key)) ==
        ($upstreams|map(upstream)|sort_by(.sid,.channel_key,.upstream_member_key))
    ' "$current" >/dev/null || die "exact per-SID provider/native/source contract mismatch"
}

topology_relation_edge_key() {
    local relation_type="$1" group_key="$2" digest
    digest="$(printf '%s\0%s' "$relation_type" "$group_key" | sha256sum | awk '{print $1}')"
    [[ "$digest" =~ ^[0-9a-f]{64}$ ]] || return 1
    printf '__releem_relation_edge__:%s\n' "$digest"
}

capture_native_identity_map() {
    local current="$1" sid_map="$2" out="$3"
    jq -s -e --slurpfile sidmap "$sid_map" '
      def provider: .relation_type|IN("aurora_cluster","aurora_global_database","rds_read_replica","rds_multi_az");
      [.[]|select(provider|not)] as $native |
      ($sidmap|map(select(.provider_id|endswith("-rds-source")))|if length==1 then .[0].sid else error("source SID") end) as $source_sid |
      ($sidmap|map(select(.provider_id|endswith("-rds-replica")))|if length==1 then .[0].sid else error("replica SID") end) as $replica_sid |
      ($native|map(select(.sid==$source_sid))[0].member_key) as $source_member |
      if (($sidmap|length)>0 and ($native|length)==($sidmap|length) and
        all($sidmap[]; . as $mapping |
        [$native[]|select(.sid==$mapping.sid)] as $rows |
        ($rows|length)==1 and
        ($rows[0] as $row |
          if ($mapping.provider_id|endswith("-rds-replica")) then
            $row.relation_type=="async_replication" and $row.parent_group_key==null and
            $row.role=="replica" and $row.is_writer==0 and $row.is_reader==1 and
            $row.replication_state=="healthy" and ($row.member_key|type)=="string" and
            ($row.member_key|length)>0 and ($row.primary_member_key|type)=="string" and
            $row.group_key==$row.primary_member_key
          else
            $row.relation_type=="standalone" and $row.group_key==$row.member_key and
            $row.parent_group_key==null and $row.primary_member_key==null and
            $row.role=="primary" and ($row.is_writer==0 or $row.is_writer==1) and
            $row.is_reader==1 and $row.replication_state=="healthy" and
            ($row.member_key|type)=="string" and ($row.member_key|length)>0
          end)) and
        ($native|map(select(.sid==$replica_sid))[0].primary_member_key)==$source_member)
      then [$sidmap[] as $mapping | ($native|map(select(.sid==$mapping.sid))[0]) as $row |
        {sid:$mapping.sid,provider_id:$mapping.provider_id,member_key:$row.member_key}]
      else error("native identity contract mismatch") end
    ' "$current" >"$out" || return 1
    chmod 600 "$out"
}

add_exact_native_expectations() {
    local expectation="$1" sid_map="$2" identities="$3" label="$4" global_rows global_upstreams channel_key
    mkdir -p "$(state_dir)"
    global_rows="$(state_dir)/global-upstream-rows.tsv"
    global_upstreams="$(state_dir)/global-upstreams.json"
    jq -r '.relations[]|select(.relation_type=="aurora_global_database" and .role=="replica_cluster_member")|
      [.sid,.group_key,.parent_group_key,.replication_state]|@tsv' "$expectation" >"$global_rows" || return 1
    : >"${global_upstreams}.jsonl"
    while IFS=$'\t' read -r sid group_key upstream_member_key replication_state; do
        [[ -n "$sid" ]] || continue
        channel_key="$(topology_relation_edge_key aurora_global_database "$group_key")" || return 1
        jq -cn --argjson sid "$sid" --arg channel_key "$channel_key" --arg upstream "$upstream_member_key" \
          --arg state "$replication_state" \
          '{sid:$sid,channel_key:$channel_key,upstream_member_key:$upstream,replication_state:$state}' \
          >>"${global_upstreams}.jsonl" || return 1
    done <"$global_rows"
    jq -s '.' "${global_upstreams}.jsonl" >"$global_upstreams" || return 1
    jq -s --slurpfile sidmap "$sid_map" --slurpfile identities "$identities" \
      --slurpfile global_upstreams "$global_upstreams" --arg label "$label" '
      .[0] as $expected | $identities[0] as $ids |
      ($ids|map(select(.provider_id|endswith("-rds-source")))|if length==1 then .[0] else error("source identity") end) as $source |
      ($ids|map(select(.provider_id|endswith("-rds-replica")))|if length==1 then .[0] else error("replica identity") end) as $replica |
      (if $label=="rds-replica-stopped" then "stopped" else "healthy" end) as $replica_state |
      [$ids[] as $identity |
        if $identity.sid==$replica.sid then
          {sid:$identity.sid,relation_type:"async_replication",group_key:$source.member_key,parent_group_key:null,
           member_key:$identity.member_key,primary_member_key:$source.member_key,role:"replica",is_writer:0,
           is_reader:(if $replica_state=="stopped" then 0 else 1 end),replication_state:$replica_state}
        else
          ([$expected.relations[]|select(.sid==$identity.sid and (.relation_type|IN("aurora_cluster","rds_read_replica","rds_multi_az")))]|first) as $provider |
          if $provider==null then error("provider expectation missing") else
            {sid:$identity.sid,relation_type:"standalone",group_key:$identity.member_key,parent_group_key:null,
             member_key:$identity.member_key,primary_member_key:null,role:"primary",is_writer:$provider.is_writer,
             is_reader:1,replication_state:"healthy"}
          end
        end] as $native |
      $expected + {relations:($expected.relations+$native),upstreams:([{
        sid:$replica.sid,channel_key:"default",upstream_member_key:$source.member_key,
        replication_state:$replica_state}]+$global_upstreams[0])}
    ' "$expectation" >"${expectation}.new" || return 1
    mv "${expectation}.new" "$expectation"
    chmod 600 "$expectation"
}

retain_clickhouse_observations() {
    local input="$1" output="$2"
    jq -c '
      (.relations|fromjson) as $relations |
      [$relations[]|select(.primary_member_key != null and .primary_member_key != "")|
        {relation_type,group_key,member_key,source_member_key:.primary_member_key,replication_state}] as $edges |
      if ([ $relations[]|select(.relation_type=="rds_read_replica" and .role=="replica") ]|length)>0 and ($edges|length)==0
      then error("ClickHouse replica source edge missing")
      else {source:"clickhouse",sid,rid,observed_epoch_ms,relations:$relations,source_edges:$edges} end
    ' "$input" >"$output" || return 1
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
          ($r|map(select(.is_writer==1))[0].member_key != $e.old_writer_member_key))
      elif $label == "aws-baseline" then
        (map(select(.relation_type=="aurora_cluster")) as $aurora |
          ($aurora|length)==10 and ($aurora|map(.group_key)|unique|length)==4 and
          ($aurora|group_by(.group_key)|map(length)|sort)==[2,2,3,3] and
          ($aurora|group_by(.group_key)|all((map(select(.role=="primary"))|length)==1)) and
          ($aurora|map(select(.is_writer==1))|length)==3) and
        (map(select(.relation_type=="aurora_global_database")) as $global |
          ($global|length)==4 and ($global|map(.group_key)|unique|length)==1 and
          ($global|map(select(.role=="primary_cluster_member"))|length)==2 and
          ($global|map(select(.role=="replica_cluster_member" and .parent_group_key!=null))|length)==2 and
          ($global|map(select(.is_writer==1 and .is_reader==1 and .replication_state=="healthy"))|length)==1 and
          ($global|all(.is_reader==1 and .replication_state=="healthy"))) and
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
          ($r|map(select(.is_writer==1 and .is_reader==1 and .replication_state=="healthy"))|length)==1 and
          ($r|all(.is_reader==1 and .replication_state=="healthy")) and
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
    local subnet_json route_json subnet route_table_id
    [[ "$vpc" =~ ^vpc-[a-f0-9]+$ ]] || die "invalid VPC ID"
    [[ "$subnet_csv" =~ ^subnet-[a-f0-9]+(,subnet-[a-f0-9]+)+$ ]] || die "at least two subnet IDs are required"
    IFS=',' read -ra subnets <<<"$subnet_csv"
    subnet_json="$(aws_region "$region" ec2 describe-subnets --subnet-ids "${subnets[@]}")"
    jq -e --arg vpc "$vpc" '
      (.Subnets|length) >= 2 and
      (.Subnets|map(.AvailabilityZone)|unique|length) >= 2 and
      all(.Subnets[]; .VpcId==$vpc and .MapPublicIpOnLaunch==false)
    ' <<<"$subnet_json" >/dev/null || die "subnets must be private, in one VPC, and span two AZs"
    route_json='[]'
    for subnet in "${subnets[@]}"; do
        route_table_id="$(aws_region "$region" ec2 describe-route-tables --filters "Name=association.subnet-id,Values=${subnet}" --query 'RouteTables[0].RouteTableId' --output text)"
        if [[ "$route_table_id" == None ]]; then
            route_table_id="$(aws_region "$region" ec2 describe-route-tables --filters "Name=vpc-id,Values=${vpc}" "Name=association.main,Values=true" --query 'RouteTables[0].RouteTableId' --output text)"
        fi
        route_json="$(jq -cn --argjson prior "$route_json" --arg subnet "$subnet" \
            --argjson routes "$(aws_region "$region" ec2 describe-route-tables --route-table-ids "$route_table_id" --query 'RouteTables[0].Routes' --output json)" \
            '$prior+[{subnet_id:$subnet,routes:[$routes[]|{destination:(.DestinationCidrBlock//""),nat_gateway_id:(.NatGatewayId//""),state:(.State//"")}]}]')"
    done
    assert_private_subnet_egress <(jq -n --argjson subnets "$route_json" '{subnets:$subnets}') "${#subnets[@]}"
    jq -n --arg region "$region" --arg vpc "$vpc" \
        --argjson subnets "$(jq '.Subnets|map({subnet_id:.SubnetId,availability_zone:.AvailabilityZone,map_public_ip:.MapPublicIpOnLaunch})' <<<"$subnet_json")" \
        --argjson egress "$route_json" '{region:$region,vpc:$vpc,subnets:$subnets,egress:$egress}' >"$out"
}

capture_deterministic_collisions() {
    local out="$1" prefix dir account bucket bucket_state
    prefix="releem-$(run_id)-"
    dir="$(dirname "$out")/collision-work"
    mkdir -p "$dir"
    aws_region "$PRIMARY_REGION" rds describe-db-instances --output json >"$dir/east-db.json"
    aws_region "$SECONDARY_REGION" rds describe-db-instances --output json >"$dir/west-db.json"
    aws_region "$PRIMARY_REGION" rds describe-db-clusters --output json >"$dir/east-clusters.json"
    aws_region "$SECONDARY_REGION" rds describe-db-clusters --output json >"$dir/west-clusters.json"
    aws_region "$PRIMARY_REGION" rds describe-global-clusters --output json >"$dir/globals.json"
    aws_region "$PRIMARY_REGION" rds describe-db-parameter-groups --output json >"$dir/east-pgs.json"
    aws_region "$SECONDARY_REGION" rds describe-db-parameter-groups --output json >"$dir/west-pgs.json"
    aws_region "$PRIMARY_REGION" rds describe-db-cluster-parameter-groups --output json >"$dir/east-cpgs.json"
    aws_region "$SECONDARY_REGION" rds describe-db-cluster-parameter-groups --output json >"$dir/west-cpgs.json"
    aws_region "$PRIMARY_REGION" rds describe-db-subnet-groups --output json >"$dir/east-subnets.json"
    aws_region "$SECONDARY_REGION" rds describe-db-subnet-groups --output json >"$dir/west-subnets.json"
    aws_region "$PRIMARY_REGION" ec2 describe-security-groups --output json >"$dir/east-sgs.json"
    aws_region "$SECONDARY_REGION" ec2 describe-security-groups --output json >"$dir/west-sgs.json"
    aws_region "$PRIMARY_REGION" ec2 describe-instances --filters "Name=tag:Name,Values=${prefix}runner" --output json >"$dir/east-runners.json"
    aws_region "$SECONDARY_REGION" ec2 describe-instances --filters "Name=tag:Name,Values=${prefix}runner" --output json >"$dir/west-runners.json"
    aws_global iam list-roles --path-prefix /releem-topology/ --output json >"$dir/roles.json"
    aws_global iam list-instance-profiles --path-prefix / --output json >"$dir/profiles.json"
    aws_global s3api list-buckets --output json >"$dir/buckets.json"
    account="$(aws_global sts get-caller-identity --query Account --output text)"
    bucket="releem-topology-$(printf '%s' "$account" | sha256sum | cut -c1-12)-$(run_id)"
    bucket_state="$(s3_bucket_presence "$bucket")" || return 1
    jq -n --arg prefix "$prefix" --arg bucket "$bucket" --arg run "$(run_id)" \
        --arg run_key "$TAG_RUN_KEY" --arg managed_key "$TAG_MANAGED_KEY" \
        --slurpfile ed "$dir/east-db.json" --slurpfile wd "$dir/west-db.json" \
        --slurpfile ec "$dir/east-clusters.json" --slurpfile wc "$dir/west-clusters.json" \
        --slurpfile globals "$dir/globals.json" --slurpfile roles "$dir/roles.json" \
        --slurpfile profiles "$dir/profiles.json" --slurpfile buckets "$dir/buckets.json" \
        --slurpfile ep "$dir/east-pgs.json" --slurpfile wp "$dir/west-pgs.json" \
        --slurpfile ecp "$dir/east-cpgs.json" --slurpfile wcp "$dir/west-cpgs.json" \
        --slurpfile es "$dir/east-subnets.json" --slurpfile ws "$dir/west-subnets.json" \
        --slurpfile esg "$dir/east-sgs.json" --slurpfile wsg "$dir/west-sgs.json" \
        --slurpfile eri "$dir/east-runners.json" --slurpfile wri "$dir/west-runners.json" --arg bucket_state "$bucket_state" '
      def directly_owned:
        ((.Tags // []) | map({key:.Key,value:.Value}) | from_entries) as $tags |
        $tags[$run_key]==$run and $tags[$managed_key]=="true";
      {expected:[],existing:((
        [$ed[0].DBInstances[]?.DBInstanceIdentifier,$wd[0].DBInstances[]?.DBInstanceIdentifier,
         $ec[0].DBClusters[]?.DBClusterIdentifier,$wc[0].DBClusters[]?.DBClusterIdentifier,
         $globals[0].GlobalClusters[]?.GlobalClusterIdentifier,$roles[0].Roles[]?.RoleName,
         $profiles[0].InstanceProfiles[]?.InstanceProfileName,
         $ep[0].DBParameterGroups[]?.DBParameterGroupName,$wp[0].DBParameterGroups[]?.DBParameterGroupName,
         $ecp[0].DBClusterParameterGroups[]?.DBClusterParameterGroupName,$wcp[0].DBClusterParameterGroups[]?.DBClusterParameterGroupName,
         $es[0].DBSubnetGroups[]?.DBSubnetGroupName,$ws[0].DBSubnetGroups[]?.DBSubnetGroupName] | map(select(startswith($prefix)))) +
        [($esg[0].SecurityGroups[]?|select(.GroupName|startswith($prefix))|.GroupId),
         ($wsg[0].SecurityGroups[]?|select(.GroupName|startswith($prefix))|.GroupId)] +
        [($eri[0].Reservations[].Instances[]?|select(.State.Name!="terminated")|.InstanceId),
         ($wri[0].Reservations[].Instances[]?|select(.State.Name!="terminated")|.InstanceId)] +
        (if $bucket_state=="present" then [$bucket] else [] end)),
       direct_owned:([($esg[0].SecurityGroups[]?|select((.GroupName|startswith($prefix)) and directly_owned)|.GroupId),
                      ($wsg[0].SecurityGroups[]?|select((.GroupName|startswith($prefix)) and directly_owned)|.GroupId),
                      ($eri[0].Reservations[].Instances[]?|select(.State.Name!="terminated" and directly_owned)|.InstanceId),
                      ($wri[0].Reservations[].Instances[]?|select(.State.Name!="terminated" and directly_owned)|.InstanceId)])}' >"$out"
    chmod 600 "$out"
}

inventory_region() {
    local region="$1" output="$2" prefix direct_security_groups
    prefix="releem-$(run_id)-"
    direct_security_groups="${output}.security-groups"
    mkdir -p "$(dirname "$output")"
    aws_region "$region" resourcegroupstaggingapi get-resources \
        --tag-filters "Key=${TAG_RUN_KEY},Values=$(run_id)" "Key=${TAG_MANAGED_KEY},Values=true" \
        --resource-type-filters rds:db rds:cluster rds:cluster-pg rds:pg rds:subgrp rds:snapshot ec2:security-group \
        --output json >"$output.raw"
    aws_region "$region" ec2 describe-security-groups \
        --filters "Name=group-name,Values=${prefix}*" --output json >"$direct_security_groups"
    jq --arg prefix "$prefix" --arg region "$region" --slurpfile direct "$direct_security_groups" '
      ([.ResourceTagMappingList[]? | .ResourceARN as $arn |
        {arn:$arn,tags:(.Tags|map({key:.Key,value:.Value})|from_entries)} |
        select(.arn|contains($prefix))] +
       [$direct[0].SecurityGroups[]? | select(.GroupName|startswith($prefix)) |
        {arn:("arn:aws:ec2:"+$region+":"+.OwnerId+":security-group/"+.GroupId),
         tags:((.Tags // [])|map({key:.Key,value:.Value})|from_entries)}]) |
      unique_by(.arn) |
      {instances:[.[]|select(.arn|contains(":db:"))|.arn],
       clusters:[.[]|select(.arn|contains(":cluster:"))|.arn],
       global_clusters:[],
       parameter_groups:[.[]|select(.arn|test(":(cluster-pg|pg):"))|.arn],
       security_groups:[.[]|select(.arn|contains(":security-group/"))|.arn],
       subnet_groups:[.[]|select(.arn|contains(":subgrp:"))|.arn],
       snapshots:[.[]|select(.arn|contains(":snapshot:"))|.arn],
       ownership:[.[]]}
    ' "$output.raw" >"$output"
    rm -f "$output.raw" "$direct_security_groups"
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
    local out="$1" account bucket bucket_state runner_role monitor_role profile east_instances west_instances east_enis west_enis
    account="$(aws_global sts get-caller-identity --query Account --output text)"
    bucket="releem-topology-$(printf '%s' "$account" | sha256sum | cut -c1-12)-$(run_id)"
    bucket_state="$(s3_bucket_presence "$bucket")" || return 1
    runner_role="$(resource_name runner-role)"; monitor_role="$(resource_name monitoring-role)"; profile="$(resource_name runner-profile)"
    east_instances="$(aws_region "$PRIMARY_REGION" ec2 describe-instances --filters "Name=tag:${TAG_RUN_KEY},Values=$(run_id)" "Name=tag:${TAG_MANAGED_KEY},Values=true" "Name=instance-state-name,Values=pending,running,stopping,stopped,shutting-down" --output json)"
    west_instances="$(aws_region "$SECONDARY_REGION" ec2 describe-instances --filters "Name=tag:${TAG_RUN_KEY},Values=$(run_id)" "Name=tag:${TAG_MANAGED_KEY},Values=true" "Name=instance-state-name,Values=pending,running,stopping,stopped,shutting-down" --output json)"
    east_enis="$(aws_region "$PRIMARY_REGION" ec2 describe-network-interfaces --filters "Name=tag:${TAG_RUN_KEY},Values=$(run_id)" "Name=tag:${TAG_MANAGED_KEY},Values=true" --output json)"
    west_enis="$(aws_region "$SECONDARY_REGION" ec2 describe-network-interfaces --filters "Name=tag:${TAG_RUN_KEY},Values=$(run_id)" "Name=tag:${TAG_MANAGED_KEY},Values=true" --output json)"
    local runner_state monitor_state profile_state
    runner_state="$(iam_entity_presence role "$runner_role")" || return 1
    monitor_state="$(iam_entity_presence role "$monitor_role")" || return 1
    profile_state="$(iam_entity_presence profile "$profile")" || return 1
    jq -n \
        --argjson east_instances "$(jq '[.Reservations[].Instances[].InstanceId]' <<<"$east_instances")" \
        --argjson west_instances "$(jq '[.Reservations[].Instances[].InstanceId]' <<<"$west_instances")" \
        --argjson east_enis "$(jq '[.NetworkInterfaces[].NetworkInterfaceId]' <<<"$east_enis")" \
        --argjson west_enis "$(jq '[.NetworkInterfaces[].NetworkInterfaceId]' <<<"$west_enis")" \
        --arg bucket "$bucket" --arg runner_role "$runner_role" --arg monitor_role "$monitor_role" --arg profile "$profile" \
        --argjson bucket_exists "$([[ "$bucket_state" == present ]] && printf true || printf false)" \
        --argjson runner_exists "$([[ "$runner_state" == present ]] && printf true || printf false)" \
        --argjson monitor_exists "$([[ "$monitor_state" == present ]] && printf true || printf false)" \
        --argjson profile_exists "$([[ "$profile_state" == present ]] && printf true || printf false)" \
        '{runners:($east_instances+$west_instances),enis:($east_enis+$west_enis),ssm_artifacts:[],monitoring_streams:[],
          s3_buckets:(if $bucket_exists then [$bucket] else [] end),s3_objects:[],
          instance_profiles:(if $profile_exists then [$profile] else [] end),
          iam_policies:(if $runner_exists then ["ReleemTopologyRunnerRead"] else [] end),
          iam_roles:((if $runner_exists then [$runner_role] else [] end)+(if $monitor_exists then [$monitor_role] else [] end))}' >"$out"
    if [[ "$bucket_state" == present ]]; then
        if ! aws_region "$PRIMARY_REGION" s3api list-objects-v2 --bucket "$bucket" --prefix "$(run_id)/" --output json >"${out}.objects"; then
            return 1
        fi
        if ! jq --slurpfile base "$out" '$base[0] + {s3_objects:[.Contents[]?.Key]}' "${out}.objects" >"${out}.new"; then
            return 1
        fi
        mv "${out}.new" "$out"
    fi
}

capture_iam_role_ownership() {
    local role="$1" kind="$2" out="$3" state dir
    dir="$(dirname "$out")"; mkdir -p "$dir"
    state="$(iam_entity_presence role "$role")" || return 1
    if [[ "$state" == absent ]]; then jq -n '{exists:false}' >"$out"; return 0; fi
    aws_global iam get-role --role-name "$role" --output json >"${out}.get" || return 1
    aws_global iam list-role-tags --role-name "$role" --output json >"${out}.tags" || return 1
    aws_global iam list-attached-role-policies --role-name "$role" --output json >"${out}.attached" || return 1
    aws_global iam list-role-policies --role-name "$role" --output json >"${out}.names" || return 1
    if [[ "$kind" == runner ]]; then
        aws_global iam get-role-policy --role-name "$role" --policy-name ReleemTopologyRunnerRead --output json >"${out}.inline" || return 1
    else
        printf '{}\n' >"${out}.inline"
    fi
    jq -n --slurpfile get "${out}.get" --slurpfile tags "${out}.tags" --slurpfile attached "${out}.attached" --slurpfile names "${out}.names" --slurpfile inline "${out}.inline" \
      '{exists:true,tags:($tags[0].Tags|map({key:.Key,value:.Value})|from_entries),inline_policy_names:$names[0].PolicyNames,
        path:$get[0].Role.Path,trust_policy:$get[0].Role.AssumeRolePolicyDocument,
        inline_policy_name:($inline[0].PolicyName//null),inline_policy:($inline[0].PolicyDocument//{}),attached_policies:[$attached[0].AttachedPolicies[].PolicyArn]}' >"$out"
}

capture_destroy_support_ownership() {
    local out="$1" account bucket bucket_state runner_role monitor_role profile dir
    account="$(aws_global sts get-caller-identity --query Account --output text)"
    bucket="releem-topology-$(printf '%s' "$account" | sha256sum | cut -c1-12)-$(run_id)"
    runner_role="$(resource_name runner-role)"; monitor_role="$(resource_name monitoring-role)"; profile="$(resource_name runner-profile)"
    dir="$(dirname "$out")/ownership-work"; mkdir -p "$dir"
    bucket_state="$(s3_bucket_presence "$bucket")" || return 1
    if [[ "$bucket_state" == present ]]; then
        aws_region "$PRIMARY_REGION" s3api get-bucket-tagging --bucket "$bucket" --output json >"$dir/bucket-tags.json" || return 1
        jq '{exists:true,tags:(.TagSet|map({key:.Key,value:.Value})|from_entries)}' "$dir/bucket-tags.json" >"$dir/bucket.json" || return 1
    else jq -n '{exists:false}' >"$dir/bucket.json"; fi
    capture_iam_role_ownership "$runner_role" runner "$dir/runner.json" || return 1
    capture_iam_role_ownership "$monitor_role" monitor "$dir/monitor.json" || return 1
    local profile_state
    profile_state="$(iam_entity_presence profile "$profile")" || return 1
    if [[ "$profile_state" == present ]]; then
        aws_global iam get-instance-profile --instance-profile-name "$profile" --output json >"$dir/profile-get.json" || return 1
        aws_global iam list-instance-profile-tags --instance-profile-name "$profile" --output json >"$dir/profile-tags.json" || return 1
        jq -n --slurpfile profile "$dir/profile-get.json" --slurpfile tags "$dir/profile-tags.json" \
          '{exists:true,tags:($tags[0].Tags|map({key:.Key,value:.Value})|from_entries),roles:[$profile[0].InstanceProfile.Roles[].RoleName]}' >"$dir/profile.json" || return 1
    else jq -n '{exists:false}' >"$dir/profile.json"; fi
    jq -n --slurpfile bucket "$dir/bucket.json" --slurpfile runner "$dir/runner.json" \
      --slurpfile monitor "$dir/monitor.json" --slurpfile profile "$dir/profile.json" \
      '{bucket:$bucket[0],runner_role:$runner[0],monitoring_role:$monitor[0],instance_profile:$profile[0]}' >"$out" || return 1
    chmod 600 "$out"
}

verify_destroy_ownership() {
    local file account bucket collisions owned
    file="$(state_dir)/destroy-support-ownership.json"
    mkdir -p "$(state_dir)"; chmod 700 "$(state_dir)"
    capture_destroy_support_ownership "$file"
    account="$(aws_global sts get-caller-identity --query Account --output text)"
    bucket="releem-topology-$(printf '%s' "$account" | sha256sum | cut -c1-12)-$(run_id)"
    assert_destroy_support_ownership "$file" "arn:aws:s3:::${bucket}/$(run_id)/*" \
        "$(resource_name runner-role)" "$(resource_name monitoring-role)"
    collisions="$(state_dir)/destroy-name-collisions.json"
    owned="$(state_dir)/destroy-owned-inventory.json"
    capture_deterministic_collisions "$collisions"
    capture_inventory "$owned"
    assert_deterministic_resources_owned "$collisions" "$owned"
}

capture_inventory() {
    local target="$1" dir east west globals support base temporary
    dir="$(dirname "$target")/inventory-work"
    mkdir -p "$dir"
    east="$dir/east.json"; west="$dir/west.json"; globals="$dir/global.json"
    inventory_region "$PRIMARY_REGION" "$east" || return 1
    inventory_region "$SECONDARY_REGION" "$west" || return 1
    inventory_global_clusters "$globals" || return 1
    base="$dir/base.json"; support="$dir/support.json"
    combine_inventory "$east" "$west" "$globals" "$base" || return 1
    inventory_support_resources "$support" || return 1
    temporary="${target}.new"
    rm -f "$temporary"
    jq -s '.[0] * .[1]' "$base" "$support" >"$temporary" || { rm -f "$temporary"; return 1; }
    chmod 600 "$temporary"
    mv "$temporary" "$target"
}

write_selection_state() {
    local engine="$1" class="$2" global_class="$3" acu="$4" family="$5" mysql_version="$6" mysql_class="$7" mysql_family="$8" east_ami="$9" west_ami="${10}" file
    file="$(selection_file)"
    mkdir -p "$(dirname "$file")"
    printf 'ENGINE_VERSION=%q\nINSTANCE_CLASS=%q\nGLOBAL_INSTANCE_CLASS=%q\nMIN_ACU=%q\nENGINE_FAMILY=%q\nMYSQL_ENGINE_VERSION=%q\nMYSQL_INSTANCE_CLASS=%q\nMYSQL_ENGINE_FAMILY=%q\nEAST_RUNNER_AMI=%q\nWEST_RUNNER_AMI=%q\n' \
        "$engine" "$class" "$global_class" "$acu" "$family" "$mysql_version" "$mysql_class" "$mysql_family" "$east_ami" "$west_ami" >"$file"
    chmod 600 "$file"
}

write_preflight_summary() {
    local out="$1" engine="$2" class="$3" global_class="$4" acu="$5" mysql_version="$6" mysql_class="$7" initial_max transition_max
    initial_max="$(awk -v a="$acu" 'BEGIN{print a+1}')"
    transition_max="$(awk -v a="$acu" 'BEGIN{print a+2}')"
    jq -n --arg captured_at "$(date -u +%FT%TZ)" --arg engine "$engine" --arg class "$class" --arg global_class "$global_class" \
        --arg mysql_version "$mysql_version" --arg mysql_class "$mysql_class" --argjson min_acu "$acu" \
        --argjson initial_max "$initial_max" --argjson transition_max "$transition_max" \
        --argjson runtime_deadline_seconds "$RUNTIME_DEADLINE_SECONDS" --argjson cleanup_deadline_seconds "$CLEANUP_DEADLINE_SECONDS" \
        --argjson cleanup_delete_reserve_seconds "$CLEANUP_DELETE_RESERVE_SECONDS" '
      {captured_at:$captured_at,regions:["us-east-1","us-west-2"],aurora_engine_version:$engine,
       provisioned_instance_class:$class,global_instance_class:$global_class,
       serverless_v2:{instances:3,min_acu:$min_acu,
         configured_max_acu:$initial_max,transition_max_acu:$transition_max,load_attempts_max:3},
       ordinary_mysql:{instances:3,engine_version:$mysql_version,instance_class:$mysql_class,
         allocated_storage_gib_each:20,allocated_storage_gib_total:60,storage_type:"gp3"},
       runners:{instances:2,instance_class:"t3.micro",encrypted_root_gib_each:8,encrypted_root_gib_total:16},
       enhanced_monitoring_interval_seconds:1,runtime_deadline_seconds:$runtime_deadline_seconds,
       cleanup_deadline_seconds:$cleanup_deadline_seconds,
       cleanup_delete_reserve_seconds:$cleanup_delete_reserve_seconds,
       total_advertised_max_seconds:($runtime_deadline_seconds+$cleanup_deadline_seconds),
       cleanup_retry_contract:"all automatic attempts share one cleanup deadline; after exhaustion use exact-confirmed destroy",
       hourly_configuration:{provisioned_aurora_instances:7,serverless_v2_instances:3,
         ordinary_rds_instances:3,runner_instances:2,
         note:"configuration units only; consult current AWS pricing before approval"}}' >"$out"
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
    [[ "$GLOBAL_INSTANCE_CLASS" =~ ^db[.][a-z0-9]+[.][a-z0-9]+$ ]] || die "invalid stored Global Database instance class"
    [[ "$MIN_ACU" =~ ^[0-9]+([.][0-9]+)?$ ]] || die "invalid stored minimum ACU"
    [[ "$ENGINE_FAMILY" =~ ^aurora-mysql[0-9]+[.][0-9]+$ ]] || die "invalid stored engine family"
    [[ "$MYSQL_ENGINE_VERSION" =~ ^8[.][0-9]+[.][0-9]+$ ]] || die "invalid stored MySQL engine version"
    [[ "$MYSQL_INSTANCE_CLASS" =~ ^db[.][a-z0-9]+[.][a-z0-9]+$ ]] || die "invalid stored MySQL instance class"
    [[ "$MYSQL_ENGINE_FAMILY" =~ ^mysql8[.]0$ ]] || die "invalid stored MySQL engine family"
    [[ "$EAST_RUNNER_AMI" =~ ^ami-[a-f0-9]+$ && "$WEST_RUNNER_AMI" =~ ^ami-[a-f0-9]+$ ]] || die "invalid stored runner AMI"
}

preflight() {
    validate_run_id
    validate_runtime_deadline "$RUNTIME_DEADLINE_SECONDS"
    validate_cleanup_deadline "$CLEANUP_DEADLINE_SECONDS"
    require_command aws
    require_command flock
    require_command jq
    require_command sha256sum
    local out versions versions_west classes classes_west mysql_versions mysql_classes engine class global_class acu family mysql_version mysql_class mysql_family east_ami west_ami pre east_network west_network collisions quota_headroom
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
    global_class="$(select_smallest_common_global_orderable_class "$classes" "$classes_west")"
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
    write_selection_state "$engine" "$class" "$global_class" "$acu" "$family" "$mysql_version" "$mysql_class" "$mysql_family" "$east_ami" "$west_ami"

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
    collisions="$out/deterministic-name-collisions.json"
    capture_deterministic_collisions "$collisions"
    assert_no_deterministic_collisions "$collisions"
    quota_headroom="$out/quota-headroom.json"
    write_matrix_quota_headroom "$quota_headroom" "$out/quotas-east.json" "$out/quotas-west.json" \
        "$out/ec2-quotas-east.json" "$out/ec2-quotas-west.json" "$out/collision-work"
    assert_matrix_quota_headroom "$quota_headroom"
    pre="$out/inventory-before.json"
    capture_inventory "$pre"
    assert_inventory_empty "$pre"
    write_preflight_summary "$out/summary.json" "$engine" "$class" "$global_class" "$acu" "$mysql_version" "$mysql_class"
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
    aws_global iam list-role-tags --role-name "$role" --output json |
        jq -e --arg run "$(run_id)" --arg run_key "$TAG_RUN_KEY" --arg managed_key "$TAG_MANAGED_KEY" '
          (.Tags|map({key:.Key,value:.Value})|from_entries) as $t |
          $t[$run_key]==$run and $t[$managed_key]=="true"' >/dev/null
}

ensure_iam_role() {
    local role="$1" trust_service="$2" trust_file="$3" state
    jq -n --arg service "$trust_service" '{Version:"2012-10-17",Statement:[{Effect:"Allow",Principal:{Service:$service},Action:"sts:AssumeRole"}]}' >"$trust_file"
    chmod 600 "$trust_file"
    state="$(iam_entity_presence role "$role")" || die "IAM role presence is ambiguous for $role"
    if [[ "$state" == present ]]; then
        role_owned "$role" || die "IAM role name collision without exact ownership"
    else
        aws_global iam create-role --role-name "$role" --path /releem-topology/ \
            --assume-role-policy-document "file://${trust_file}" \
            --tags "$(iam_tags | head -n1)" "$(iam_tags | tail -n1)" >/dev/null
        arm_cleanup
    fi
}

ensure_support_resources() {
    local account bucket bucket_state runner_role runner_profile monitor_role runner_policy trust_file lifecycle_file
    account="$(aws sts get-caller-identity --query Account --output text)"
    [[ "$account" =~ ^[0-9]{12}$ ]] || die "unable to resolve AWS account"
    bucket="releem-topology-$(printf '%s' "$account" | sha256sum | cut -c1-12)-$(run_id)"
    runner_role="$(resource_name runner-role)"
    runner_profile="$(resource_name runner-profile)"
    monitor_role="$(resource_name monitoring-role)"
    trust_file="$(state_dir)/trust.json"
    ensure_iam_role "$runner_role" ec2.amazonaws.com "$trust_file"
    ensure_iam_role "$monitor_role" monitoring.rds.amazonaws.com "$trust_file"
    aws_global iam attach-role-policy --role-name "$runner_role" --policy-arn arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore >/dev/null
    aws_global iam attach-role-policy --role-name "$monitor_role" --policy-arn arn:aws:iam::aws:policy/service-role/AmazonRDSEnhancedMonitoringRole >/dev/null
    sleep 10

    bucket_state="$(s3_bucket_presence "$bucket")"
    if [[ "$bucket_state" == absent ]]; then
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
    lifecycle_file="$(state_dir)/bucket-lifecycle.json"
    bucket_lifecycle_configuration >"$lifecycle_file"
    chmod 600 "$lifecycle_file"
    aws_region "$PRIMARY_REGION" s3api put-bucket-lifecycle-configuration --bucket "$bucket" \
        --lifecycle-configuration "file://${lifecycle_file}"

    runner_policy="$(state_dir)/runner-policy.json"
    runner_policy_document "arn:aws:s3:::${bucket}/$(run_id)/*" >"$runner_policy"
    assert_runner_policy "$runner_policy"
    aws_global iam put-role-policy --role-name "$runner_role" --policy-name ReleemTopologyRunnerRead \
        --policy-document "file://${runner_policy}" >/dev/null
    local profile_state
    profile_state="$(iam_entity_presence profile "$runner_profile")" || die "IAM instance profile presence is ambiguous"
    if [[ "$profile_state" == absent ]]; then
        aws_global iam create-instance-profile --instance-profile-name "$runner_profile" \
            --tags "$(iam_tags | head -n1)" "$(iam_tags | tail -n1)" >/dev/null
        arm_cleanup
        aws_global iam add-role-to-instance-profile --instance-profile-name "$runner_profile" --role-name "$runner_role"
        sleep 10
    fi
    MONITORING_ROLE_ARN="$(aws_global iam get-role --role-name "$monitor_role" --query Role.Arn --output text)"
    {
        printf 'ACCOUNT_ID=%q\nS3_BUCKET=%q\nRUNNER_ROLE=%q\nRUNNER_PROFILE=%q\nMONITORING_ROLE=%q\nMONITORING_ROLE_ARN=%q\n' \
            "$account" "$bucket" "$runner_role" "$runner_profile" "$monitor_role" "$MONITORING_ROLE_ARN"
    } >"$(support_state_file)"
    chmod 600 "$(support_state_file)"
}

delete_s3_object_until_absent() {
    local bucket="$1" key="$2" region="${3:-$PRIMARY_REGION}" attempt status error_file state
    error_file="$(state_dir)/head-object-error"
    mkdir -p "$(state_dir)"
    for attempt in 1 2 3 4 5; do
        ((CLEANUP_DEADLINE_EPOCH <= 0)) || cleanup_remaining_seconds >/dev/null || return 1
        aws_region "$region" s3api delete-object --bucket "$bucket" --key "$key" >/dev/null 2>&1 || { cleanup_bounded_sleep "$POLL_SECONDS" || return 1; continue; }
        status=0
        aws_region "$region" s3api head-object --bucket "$bucket" --key "$key" >/dev/null 2>"$error_file" || status=$?
        if ((status == 0)); then state=present
        else state="$(classify_s3_head_result "$status" "$error_file" HeadObject)" || { cleanup_bounded_sleep "$POLL_SECONDS" || return 1; continue; }
        fi
        [[ "$state" == absent ]] && return 0
        cleanup_bounded_sleep "$POLL_SECONDS" || return 1
    done
    return 1
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

load_support_state_nonfatal() {
    local file
    file="$(support_state_file)"
    [[ -f "$file" && "$(stat -c '%a' "$file")" == 600 ]] || return 1
    # shellcheck disable=SC1090
    source "$file"
    [[ "${S3_BUCKET:-}" =~ ^[a-z0-9.-]{3,63}$ && "${MONITORING_ROLE_ARN:-}" =~ ^arn:aws:iam::[0-9]{12}:role/ ]]
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
    wait_until "runner instance running" runner_instance_running "$region" "$instance"
    wait_until "runner instance status ok" runner_instance_status_ok "$region" "$instance"
    wait_until "runner SSM registration" runner_ssm_online "$region" "$instance"
    printf '%s|%s\n' "$instance" "$sg"
}

runner_instance_running() {
    local response state
    response="$(aws_region "$1" ec2 describe-instances --instance-ids "$2" --output json 2>/dev/null)" || return 2
    state="$(jq -er '.Reservations[0].Instances[0].State.Name' <<<"$response")" || return 2
    [[ "$state" == running ]] && return 0
    [[ "$state" == terminated || "$state" == shutting-down ]] && return 2
    return 1
}

runner_instance_status_ok() {
    local response
    response="$(aws_region "$1" ec2 describe-instance-status --instance-ids "$2" \
        --include-all-instances --output json 2>/dev/null)" || return 2
    jq -e '
      .InstanceStatuses | length == 1 and
      .[0].InstanceStatus.Status == "ok" and
      .[0].SystemStatus.Status == "ok"
    ' <<<"$response" >/dev/null
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
        [[ $? -ne 2 ]] || die "$description failed"
        (($(date +%s) < deadline)) || die "timeout waiting for $description"
        sleep "$POLL_SECONDS"
    done
}

wait_until_nonfatal() {
    local description="$1"
    shift
    local deadline status
    deadline=$(( $(date +%s) + WAIT_TIMEOUT_SECONDS ))
    while :; do
        "$@" && return 0
        status=$?
        [[ "$status" -ne 2 ]] || { log "$description failed"; return 1; }
        (($(date +%s) < deadline)) || { log "timeout waiting for $description"; return 1; }
        sleep "$POLL_SECONDS"
    done
}

instance_exists() { aws_region "$1" rds describe-db-instances --db-instance-identifier "$2" >/dev/null 2>&1; }
cluster_exists() { aws_region "$1" rds describe-db-clusters --db-cluster-identifier "$2" >/dev/null 2>&1; }
global_exists() { aws_region "$PRIMARY_REGION" rds describe-global-clusters --global-cluster-identifier "$1" >/dev/null 2>&1; }

db_instance_available() {
    local response state
    response="$(aws_region "$1" rds describe-db-instances --db-instance-identifier "$2" --output json 2>/dev/null)" || return 2
    state="$(jq -er '.DBInstances[0].DBInstanceStatus' <<<"$response")" || return 2
    [[ "$state" == available ]] && return 0
    [[ "$state" =~ ^(failed|incompatible-.+|inaccessible-encryption-credentials|storage-full|deleting)$ ]] && return 2
    return 1
}

db_cluster_available() {
    local response state
    response="$(aws_region "$1" rds describe-db-clusters --db-cluster-identifier "$2" --output json 2>/dev/null)" || return 2
    state="$(jq -er '.DBClusters[0].Status' <<<"$response")" || return 2
    [[ "$state" == available ]] && return 0
    [[ "$state" =~ ^(failed|inaccessible-encryption-credentials|deleting)$ ]] && return 2
    return 1
}

wait_instance_available() {
    wait_until "database instance availability $1/$2" db_instance_available "$1" "$2"
}

wait_cluster_available() {
    wait_until "database cluster availability $1/$2" db_cluster_available "$1" "$2"
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
            jq 'del(.MasterUsername,.MasterUserPassword,.DatabaseName) +
                {GlobalClusterIdentifier:$global,KmsKeyId:"alias/aws/rds"}' \
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
    local response
    local -a monitor_args
    mapfile -t monitor_args < <(monitoring_arguments "$MONITORING_ROLE_ARN")
    if ! instance_exists "$region" "$identifier"; then
        response="$(create_instance_response_file "$region" "$identifier")"
        mark_instance_create_attempted "$region" "$identifier"
        aws_region "$region" rds create-db-instance --db-instance-identifier "$identifier" \
            --db-cluster-identifier "$cluster" --engine aurora-mysql --db-instance-class "$class" \
            --db-parameter-group-name "$instance_pg" --no-publicly-accessible --auto-minor-version-upgrade \
            "${monitor_args[@]}" \
            --tags "$(tags_args | head -n1)" "$(tags_args | tail -n1)" --output json >"$response"
        arm_cleanup
        capture_monitoring_resource_id_from_response "$region" "$identifier" "$response" ||
            record_monitoring_resource_id "$region" "$identifier"
    else
        mark_instance_create_attempted "$region" "$identifier"
        record_monitoring_resource_id "$region" "$identifier"
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
    local identifier="$1" subnet="$2" sg="$3" pg="$4" multi="$5" file response
    if ! instance_exists "$PRIMARY_REGION" "$identifier"; then
        file="$(rds_input_file "$identifier" "$subnet" "$sg" "$pg" "$multi")"
        response="$(create_instance_response_file "$PRIMARY_REGION" "$identifier")"
        mark_instance_create_attempted "$PRIMARY_REGION" "$identifier"
        aws_region "$PRIMARY_REGION" rds create-db-instance --cli-input-json "file://${file}" --output json >"$response"
        arm_cleanup
        capture_monitoring_resource_id_from_response "$PRIMARY_REGION" "$identifier" "$response" ||
            record_monitoring_resource_id "$PRIMARY_REGION" "$identifier"
    else
        mark_instance_create_attempted "$PRIMARY_REGION" "$identifier"
        record_monitoring_resource_id "$PRIMARY_REGION" "$identifier"
    fi
    wait_instance_available "$PRIMARY_REGION" "$identifier"
}

read_replica_create_arguments() {
    local identifier="$1" source="$2" subnet="$3" sg="$4"
    printf '%s\n' --db-instance-identifier "$identifier" \
        --source-db-instance-identifier "$source" \
        --db-instance-class "$MYSQL_INSTANCE_CLASS" \
        --db-subnet-group-name "$subnet" \
        --vpc-security-group-ids "$sg" \
        --no-publicly-accessible --auto-minor-version-upgrade
    monitoring_arguments "$MONITORING_ROLE_ARN"
    printf '%s\n' --tags "$(tags_args | head -n1)" "$(tags_args | tail -n1)"
}

ensure_read_replica() {
    local identifier="$1" source="$2" subnet="$3" sg="$4" pg="$5"
    local file response
    local -a args
    mapfile -t args < <(read_replica_create_arguments "$identifier" "$source" "$subnet" "$sg")
    if ! instance_exists "$PRIMARY_REGION" "$identifier"; then
        response="$(create_instance_response_file "$PRIMARY_REGION" "$identifier")"
        mark_instance_create_attempted "$PRIMARY_REGION" "$identifier"
        aws_region "$PRIMARY_REGION" rds create-db-instance-read-replica "${args[@]}" --output json >"$response"
        arm_cleanup
        capture_monitoring_resource_id_from_response "$PRIMARY_REGION" "$identifier" "$response" ||
            record_monitoring_resource_id "$PRIMARY_REGION" "$identifier"
    else
        mark_instance_create_attempted "$PRIMARY_REGION" "$identifier"
        record_monitoring_resource_id "$PRIMARY_REGION" "$identifier"
    fi
    wait_instance_available "$PRIMARY_REGION" "$identifier"
    file="$(state_dir)/read-replica-parameter-group.json"
    aws_region "$PRIMARY_REGION" rds describe-db-instances --db-instance-identifier "$identifier" --output json >"$file"
    assert_read_replica_parameter_group "$file" "$pg"
}

create_matrix() {
    local east_network west_network east_pg west_pg east_subnet east_sg west_subnet west_sg east_runner west_runner east_runner_id east_runner_sg west_runner_id west_runner_sg
    local east_cluster_pg east_instance_pg west_cluster_pg west_instance_pg rds_instance_pg
    load_selection_state
    generate_secret
    load_secret
    arm_cleanup
    initialize_monitoring_tracking
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
    for i in 1 2; do ensure_cluster_instance "$PRIMARY_REGION" "${global_east}-${i}" "$global_east" "$GLOBAL_INSTANCE_CLASS" "$east_instance_pg"; done
    ensure_global_cluster "$global_id" "arn:aws:rds:${PRIMARY_REGION}:${ACCOUNT_ID}:cluster:${global_east}"
    ensure_cluster "$SECONDARY_REGION" "$global_west" "$west_subnet" "$west_sg" "$west_cluster_pg" false "$global_id"
    for i in 1 2; do ensure_cluster_instance "$SECONDARY_REGION" "${global_west}-${i}" "$global_west" "$GLOBAL_INSTANCE_CLASS" "$west_instance_pg"; done

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
releem_cnf_dir="/tmp/releem-topology"
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

monitoring_tracking_file() { printf '%s/monitoring-streams.tsv\n' "$(state_dir)"; }

initialize_monitoring_tracking() {
    local file temp
    file="$(monitoring_tracking_file)"
    mkdir -p "$(state_dir)"
    if [[ ! -f "$file" ]]; then
        while IFS='|' read -r region identifier; do
            printf '%s\t%s\tplanned\t\n' "$region" "$identifier"
        done < <(addressable_instances) >"$file"
    elif ! awk -F '\t' 'NF!=4{exit 1}' "$file"; then
        temp="${file}.migrated"
        awk -F '\t' -v OFS='\t' '
          NF==3 && $3~/^db-[A-Za-z0-9]+$/ {print $1,$2,"created_with_resource_id",$3; next}
          NF==3 && $3=="" {print $1,$2,"create_attempted",""; next}
          NF==4 {print $1,$2,$3,$4; next}
          {exit 1}
        ' "$file" >"$temp" || { rm -f "$temp"; return 1; }
        mv "$temp" "$file"
    fi
    chmod 600 "$file"
}

set_monitoring_lifecycle() {
    local region="$1" identifier="$2" lifecycle="$3" resource_id="${4:-}" file temp
    case "$lifecycle" in
        planned|create_attempted|confirmed_absent) [[ -z "$resource_id" ]] || return 1 ;;
        created_with_resource_id) [[ "$resource_id" =~ ^db-[A-Za-z0-9]+$ ]] || return 1 ;;
        *) return 1 ;;
    esac
    initialize_monitoring_tracking || return 1
    file="$(monitoring_tracking_file)"
    temp="${file}.new"
    awk -F '\t' -v OFS='\t' -v region="$region" -v identifier="$identifier" \
      -v lifecycle="$lifecycle" -v resource_id="$resource_id" '
      $4==resource_id && resource_id!="" && !($1==region && $2==identifier) {duplicate=1}
      $1==region && $2==identifier {$3=lifecycle; $4=resource_id; found++}
      {print $1,$2,$3,$4}
      END{if(found!=1 || duplicate) exit 1}
    ' "$file" >"$temp" || { rm -f "$temp"; return 1; }
    mv "$temp" "$file"
    chmod 600 "$file"
}

monitoring_lifecycle_for_instance() {
    local region="$1" identifier="$2"
    initialize_monitoring_tracking || return 1
    awk -F '\t' -v region="$region" -v identifier="$identifier" '
      $1==region && $2==identifier {if(found) exit 2; value=$3; found=1}
      END{if(!found) exit 1; print value}
    ' "$(monitoring_tracking_file)"
}

mark_instance_create_attempted() {
    local region="$1" identifier="$2" lifecycle
    lifecycle="$(monitoring_lifecycle_for_instance "$region" "$identifier")" || return 1
    case "$lifecycle" in
        planned|confirmed_absent) set_monitoring_lifecycle "$region" "$identifier" create_attempted ;;
        create_attempted|created_with_resource_id) return 0 ;;
        *) return 1 ;;
    esac
}

probe_db_instance_resource_id() {
    local region="$1" identifier="$2" error_file status=0 resource_id
    error_file="$(state_dir)/describe-db-instance-${region}-${identifier}.error"
    resource_id="$(aws_region "$region" rds describe-db-instances --db-instance-identifier "$identifier" \
        --query 'DBInstances[0].DbiResourceId' --output text 2>"$error_file")" || status=$?
    chmod 600 "$error_file"
    if ((status == 0)); then
        [[ "$resource_id" =~ ^db-[A-Za-z0-9]+$ ]] || return 1
        printf 'present\t%s\n' "$resource_id"
        return 0
    fi
    if ((status == 254)) && grep -Eq '^(aws: \[ERROR\]: )?An error occurred \(DBInstanceNotFound(Fault)?\) when calling the DescribeDBInstances operation: [^[:space:]].*$' "$error_file"; then
        printf 'absent\n'
        return 0
    fi
    return 1
}

capture_monitoring_resource_id() {
    local region="$1" identifier="$2" result resource_id
    result="$(probe_db_instance_resource_id "$region" "$identifier")" || return 1
    [[ "${result%%$'\t'*}" == present ]] || return 1
    resource_id="${result#*$'\t'}"
    persist_monitoring_resource_id "$region" "$identifier" "$resource_id"
}

persist_monitoring_resource_id() {
    local region="$1" identifier="$2" resource_id="$3" lifecycle
    [[ "$resource_id" =~ ^db-[A-Za-z0-9]+$ ]] || return 1
    lifecycle="$(monitoring_lifecycle_for_instance "$region" "$identifier")" || return 1
    [[ "$lifecycle" == create_attempted || "$lifecycle" == created_with_resource_id || "$lifecycle" == confirmed_absent ]] || return 1
    set_monitoring_lifecycle "$region" "$identifier" created_with_resource_id "$resource_id"
}

capture_monitoring_resource_id_from_response() {
    local region="$1" identifier="$2" response="$3" response_identifier resource_id
    [[ -s "$response" ]] || return 1
    response_identifier="$(jq -er '.DBInstance.DBInstanceIdentifier' "$response")" || return 1
    [[ "$response_identifier" == "$identifier" ]] || return 1
    resource_id="$(jq -er '.DBInstance.DbiResourceId' "$response")" || return 1
    persist_monitoring_resource_id "$region" "$identifier" "$resource_id"
}

create_instance_response_file() {
    printf '%s/create-%s-%s-response.json\n' "$(state_dir)" "$1" "$2"
}

recover_monitoring_resource_id_from_response() {
    local region="$1" identifier="$2" response
    response="$(create_instance_response_file "$region" "$identifier")"
    [[ -s "$response" ]] || return 1
    capture_monitoring_resource_id_from_response "$region" "$identifier" "$response"
}

tracked_monitoring_resource_id() {
    local region="$1" identifier="$2" file record lifecycle resource_id
    file="$(monitoring_tracking_file)"
    [[ -f "$file" ]] || return 1
    record="$(awk -F '\t' -v region="$region" -v identifier="$identifier" '
      $1==region && $2==identifier {if(found) exit 2; value=$3 "\t" $4; found=1}
      END{if(!found) exit 1; print value}
    ' "$file")" || return 1
    lifecycle="${record%%$'\t'*}"
    resource_id="${record#*$'\t'}"
    [[ "$lifecycle" == created_with_resource_id ]] || return 1
    [[ "$resource_id" =~ ^db-[A-Za-z0-9]+$ ]] || return 1
    printf '%s\n' "$resource_id"
}

recover_attempted_instance() {
    local region="$1" identifier="$2" result
    recover_monitoring_resource_id_from_response "$region" "$identifier" && return 0
    result="$(probe_db_instance_resource_id "$region" "$identifier")" || return 1
    if [[ "${result%%$'\t'*}" == present ]]; then
        persist_monitoring_resource_id "$region" "$identifier" "${result#*$'\t'}"
        return
    fi
    # NotFound is only a point-in-time observation. A create request whose
    # outcome was interrupted remains unresolved until a response or instance
    # supplies the durable resource ID.
    return 1
}

delete_db_instance_if_monitoring_tracked() {
    local region="$1" identifier="$2" deleted_file="${3:-}" resource_id
    resource_id="$(tracked_monitoring_resource_id "$region" "$identifier")" || {
        capture_monitoring_resource_id "$region" "$identifier" || return 1
        resource_id="$(tracked_monitoring_resource_id "$region" "$identifier")" || return 1
    }
    [[ -n "$resource_id" ]] || return 1
    cleanup_aws_region "$region" rds delete-db-instance --db-instance-identifier "$identifier" \
        --skip-final-snapshot --delete-automated-backups >/dev/null 2>&1 || return 1
    if [[ -n "$deleted_file" ]]; then
        printf '%s\t%s\n' "$region" "$identifier" >>"$deleted_file"
    fi
}

record_monitoring_resource_id() {
    initialize_monitoring_tracking
    wait_until "Enhanced Monitoring resource ID for $2" capture_monitoring_resource_id "$1" "$2"
}

monitoring_tracking_complete() {
    local file expected actual
    file="$(monitoring_tracking_file)"
    [[ -f "$file" ]] || return 1
    expected="$(addressable_instances | tr '|' '\t' | sort)"
    actual="$(awk -F '\t' 'NF==4{print $1 "\t" $2}' "$file" | sort)"
    [[ "$actual" == "$expected" ]] || return 1
    awk -F '\t' '
      NF!=4 {exit 1}
      $3=="planned" && $4=="" {next}
      $3=="created_with_resource_id" && $4~/^db-[A-Za-z0-9]+$/ {if(seen[$4]++) exit 1; next}
      {exit 1}
    ' "$file"
}

monitoring_all_instances_created() {
    monitoring_tracking_complete || return 1
    [[ "$(awk -F '\t' '$3=="created_with_resource_id"{count++} END{print count+0}' "$(monitoring_tracking_file)")" -eq 13 ]]
}

monitoring_manifest_has_unresolved_attempts() {
    awk -F '\t' '$3=="create_attempted" || $3=="confirmed_absent"{found=1} END{exit !found}' "$(monitoring_tracking_file)"
}

db_dependencies_may_be_deleted() {
    ! monitoring_manifest_has_unresolved_attempts
}

recover_monitoring_resource_ids() {
    local region identifier lifecycle resource_id failed=0
    initialize_monitoring_tracking
    while IFS=$'\t' read -r region identifier lifecycle resource_id; do
        case "$lifecycle" in
            planned) ;;
            created_with_resource_id) [[ "$resource_id" =~ ^db-[A-Za-z0-9]+$ ]] || failed=1 ;;
            create_attempted|confirmed_absent) recover_attempted_instance "$region" "$identifier" || failed=1 ;;
            *) failed=1 ;;
        esac
    done <"$(monitoring_tracking_file)"
    ((failed == 0)) && monitoring_tracking_complete
}

wait_monitoring_events() {
    local region="$1" identifier="$2" resource_id file deadline
    resource_id="$(tracked_monitoring_resource_id "$region" "$identifier")"
    [[ "$resource_id" =~ ^db-[A-Za-z0-9]+$ ]] || die "missing resource ID for Enhanced Monitoring"
    file="$(state_dir)/monitoring-${region}-${identifier}.json"
    deadline=$(( $(date +%s) + WAIT_TIMEOUT_SECONDS ))
    while :; do
        aws_region "$region" logs get-log-events --log-group-name RDSOSMetrics \
            --log-stream-name "$resource_id" --limit 1 --no-start-from-head --output json >"$file" 2>/dev/null || true
        if [[ -s "$file" ]] && assert_monitoring_events "$file" "$resource_id" 2>/dev/null; then
            rm -f "$file"
            return 0
        fi
        (($(date +%s) < deadline)) || die "Enhanced Monitoring events absent for $identifier"
        sleep "$POLL_SECONDS"
    done
}

wait_all_monitoring_events() {
    local region identifier
    monitoring_all_instances_created || die "Enhanced Monitoring resource ID tracking is incomplete"
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
    local region="$1" instance="$2" commands_file="$3" label="$4" mode="${5:-fatal}" parameters command_id record
    parameters="$(state_dir)/ssm-${label}-parameters.json"
    jq -Rs '{commands:split("\n")|map(select(length>0))}' "$commands_file" >"$parameters"
    chmod 600 "$parameters"
    if ! command_id="$(aws_region "$region" ssm send-command --instance-ids "$instance" \
        --document-name AWS-RunShellScript --parameters "file://${parameters}" \
        --cloud-watch-output-config CloudWatchOutputEnabled=false \
        --query Command.CommandId --output text)"; then
        [[ "$mode" == cleanup ]] && return 1
        die "failed to submit SSM command"
    fi
    if [[ ! "$command_id" =~ ^[a-f0-9-]{36}$ ]]; then
        [[ "$mode" == cleanup ]] && return 1
        die "invalid SSM command ID"
    fi
    record="$(state_dir)/ssm-command-ids.tsv"
    printf '%s\t%s\t%s\n' "$region" "$instance" "$command_id" >>"$record"
    chmod 600 "$record"
    if [[ "$mode" == cleanup ]]; then
        cleanup_wait_until "SSM command $label" ssm_command_succeeded "$region" "$instance" "$command_id"
    else
        wait_until "SSM command $label" ssm_command_succeeded "$region" "$instance" "$command_id"
    fi
}

ssm_command_succeeded() {
    local status
    status="$(aws_region "$1" ssm get-command-invocation --command-id "$3" --instance-id "$2" --query Status --output text 2>/dev/null || true)"
    [[ "$status" == Success ]] && return 0
    [[ "$status" == Failed || "$status" == Cancelled || "$status" == TimedOut ]] && return 2
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
        delete_s3_object_until_absent "$S3_BUCKET" "${prefix}/configs.tar" ||
            die "credential bootstrap object could not be deleted"
    done
}

runner_accepts_ssm_for_cleanup() {
    local region="$1" runner="$2" state
    state="$(cleanup_aws_region "$region" ec2 describe-instances \
        --filters "Name=instance-id,Values=${runner}" \
        --query 'Reservations[].Instances[].State.Name' --output text 2>/dev/null)" || return 2
    case "$state" in
        running) return 0 ;;
        ''|None|pending|stopping|stopped|shutting-down|terminated) return 1 ;;
        *) return 2 ;;
    esac
}

stop_agents() {
    local commands region runner label state_status failed=0
    [[ -f "$(support_state_file)" ]] || return 0
    load_support_state_nonfatal || return 1
    commands="$(state_dir)/ssm-stop.sh"
    printf '%s\n' 'pkill -TERM -f /tmp/releem-topology/agent || true' \
        'rm -rf /tmp/releem-topology' >"$commands"
    chmod 600 "$commands"
    for label in east west; do
        if [[ "$label" == east ]]; then region="$PRIMARY_REGION"; runner="${EAST_RUNNER_ID:-}"; else region="$SECONDARY_REGION"; runner="${WEST_RUNNER_ID:-}"; fi
        if [[ -n "$runner" ]]; then
            if runner_accepts_ssm_for_cleanup "$region" "$runner"; then
                send_ssm_commands "$region" "$runner" "$commands" "stop-${label}" cleanup || failed=1
            else
                state_status=$?
                [[ "$state_status" -eq 1 ]] || failed=1
            fi
        fi
    done
    return "$failed"
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

capture_aws_identity_map() {
    local out="$1" region identifier db cluster global source writer_id writer
    : >"$out"
    while IFS='|' read -r region identifier; do
        db="$(aws_region "$region" rds describe-db-instances --db-instance-identifier "$identifier" --query 'DBInstances[0]' --output json)"
        cluster='null'; global='null'; source='null'; writer='null'
        if [[ "$(jq -r '.DBClusterIdentifier//empty' <<<"$db")" != "" ]]; then
            cluster="$(aws_region "$region" rds describe-db-clusters --db-cluster-identifier "$(jq -r .DBClusterIdentifier <<<"$db")" --query 'DBClusters[0]' --output json)"
            writer_id="$(jq -r '.DBClusterMembers[]|select(.IsClusterWriter==true)|.DBInstanceIdentifier' <<<"$cluster")"
            writer="$(aws_region "$region" rds describe-db-instances --db-instance-identifier "$writer_id" --query 'DBInstances[0]' --output json)"
            if [[ "$(jq -r '.GlobalClusterIdentifier//empty' <<<"$cluster")" != "" ]]; then
                global="$(aws_region "$PRIMARY_REGION" rds describe-global-clusters --global-cluster-identifier "$(jq -r .GlobalClusterIdentifier <<<"$cluster")" --query 'GlobalClusters[0]' --output json)"
            fi
        fi
        if [[ "$(jq -r '.ReadReplicaSourceDBInstanceIdentifier//empty' <<<"$db")" != "" ]]; then
            source="$(aws_region "$PRIMARY_REGION" rds describe-db-instances --db-instance-identifier "$(jq -r .ReadReplicaSourceDBInstanceIdentifier <<<"$db")" --query 'DBInstances[0]' --output json)"
        elif [[ "$(jq '.ReadReplicaDBInstanceIdentifiers|length' <<<"$db")" -gt 0 ]]; then
            source="$db"
        fi
        jq -cn --arg region "$region" --arg identifier "$identifier" --argjson db "$db" --argjson cluster "$cluster" \
          --argjson global "$global" --argjson source "$source" --argjson writer "$writer" '
          def member($x): $x.DBInstanceArn;
          def available($x): $x.DBInstanceStatus=="available";
          def global_state($member):
            if $member.SynchronizationStatus=="connected" then "healthy"
            elif $member.SynchronizationStatus=="pending-resync" then "lagging"
            elif $member.IsWriter==true and (($member.SynchronizationStatus//"")=="") then "healthy"
            else "unknown" end;
          def provider_relations:
            (if $cluster != null then
              (any($cluster.DBClusterMembers[];.DBInstanceIdentifier==$identifier and .IsClusterWriter)) as $local_writer |
              [{relation_type:"aurora_cluster",group_key:("aurora:"+$cluster.DbClusterResourceId),
                parent_group_key:(if $global==null then null else "aurora-global:"+$global.GlobalClusterResourceId end),
                member_key:member($db),primary_member_key:member($writer),
                role:(if $local_writer then "primary" else "replica" end),
                is_writer:(available($db) and $local_writer and
                  ($global==null or any($global.GlobalClusterMembers[];.DBClusterArn==$cluster.DBClusterArn and .IsWriter==true and global_state(.)=="healthy"))),
                is_reader:available($db),replication_state:(if available($db) then "healthy" else "unknown" end)}] +
              (if $global != null then
                ($global.GlobalClusterMembers|map(select(.IsWriter==true))[0].DBClusterArn) as $primary_cluster |
                ($global.GlobalClusterMembers|map(select(.DBClusterArn==$cluster.DBClusterArn))[0]) as $global_member |
                (global_state($global_member)) as $state |
                [{relation_type:"aurora_global_database",group_key:("aurora-global:"+$global.GlobalClusterResourceId),
                  parent_group_key:(if $cluster.DBClusterArn==$primary_cluster then null else $primary_cluster end),
                  member_key:member($db),primary_member_key:null,
                  role:(if $cluster.DBClusterArn==$primary_cluster then "primary_cluster_member" else "replica_cluster_member" end),
                  is_writer:(available($db) and $state=="healthy" and $cluster.DBClusterArn==$primary_cluster and $local_writer),
                  is_reader:(available($db) and ($state=="healthy" or $state=="lagging")),replication_state:$state}]
               else [] end)
             else [] end) +
            (if $source != null then
              [{relation_type:"rds_read_replica",group_key:("rds-replica:"+$source.DbiResourceId),parent_group_key:null,
                member_key:member($db),primary_member_key:member($source),
                role:(if $db.DBInstanceIdentifier==$source.DBInstanceIdentifier then "primary" else "replica" end),
                is_writer:(available($db) and $db.DBInstanceIdentifier==$source.DBInstanceIdentifier),
                is_reader:available($db),replication_state:"unknown"}]
             else [] end) +
            (if $db.MultiAZ==true then
              [{relation_type:"rds_multi_az",group_key:("rds-multi-az:"+$db.DbiResourceId),parent_group_key:null,
                member_key:member($db),primary_member_key:member($db),role:"primary",is_writer:available($db),
                is_reader:available($db),replication_state:"unknown"}]
             else [] end);
          {provider_id:$identifier,region:$region,endpoint:$db.Endpoint.Address,resource_id:$db.DbiResourceId,relations:provider_relations}
        ' >>"$out"
    done < <(addressable_instances)
    chmod 600 "$out"
}

build_exact_baseline_expectation() {
    local aws_map="$1" sid_map="$2" marker="$3" out="$4"
    jq -s --slurpfile sidmap "$sid_map" --argjson marker "$marker" '
      {transition:"aws-baseline",marker_epoch_ms:$marker,relations:[.[] as $aws |
        ($sidmap|map(select(.provider_id==$aws.provider_id))|if length==1 then .[0].sid else error("provider SID mapping mismatch") end) as $sid |
        $aws.relations[] | .+{sid:$sid}],upstreams:[]}
    ' "$aws_map" >"$out"
    chmod 600 "$out"
}

add_exact_provider_expectations() {
    local expectation="$1" sid_map="$2" marker="$3" aws_map temp
    aws_map="$(state_dir)/aws-identity-${marker}.jsonl"
    temp="$(state_dir)/provider-expectation-${marker}.json"
    capture_aws_identity_map "$aws_map"
    build_exact_baseline_expectation "$aws_map" "$sid_map" "$marker" "$temp"
    jq -s '.[0] * {marker_epoch_ms:.[1].marker_epoch_ms,relations:.[1].relations,upstreams:(.[0].upstreams//[])}' \
        "$expectation" "$temp" >"${expectation}.new"
    mv "${expectation}.new" "$expectation"
    chmod 600 "$expectation"
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
    local sids current upstream pairs observations observations_raw deadline identities native_count
    sids="$(jq -sr 'map(.sid)|sort|map(tostring)|join(",")' "$map_file")"
    current="$(evidence_dir)/transitions/${marker}-${label}-current.jsonl"
    upstream="$(evidence_dir)/transitions/${marker}-${label}-upstreams.jsonl"
    observations="$(evidence_dir)/transitions/${marker}-${label}-observations.jsonl"
    observations_raw="$(state_dir)/${marker}-${label}-observations.raw.jsonl"
    identities="$(state_dir)/native-identities.json"
    mkdir -p "$(dirname "$current")"
    deadline=$(( $(date +%s) + PERSISTENCE_TIMEOUT_SECONDS ))
    while :; do
        platform_mysql "$(current_state_query "$sids" "$marker")" >"$current"
        platform_mysql "$(upstream_state_query "$sids" "$marker")" >"$upstream"
        native_count="$(jq '[.relations[]?|select(.relation_type=="standalone" or .relation_type=="async_replication")]|length' "$expectation")"
        if [[ "$(jq -sr 'map(.sid)|unique|length' "$current")" == 13 && "$native_count" == 0 ]]; then
            if [[ ! -s "$identities" ]]; then capture_native_identity_map "$current" "$map_file" "$identities" 2>/dev/null || true; fi
            if [[ -s "$identities" ]]; then add_exact_native_expectations "$expectation" "$map_file" "$identities" "$label" 2>/dev/null || true; fi
        fi
        if [[ -s "$current" ]] && [[ "$(jq -sr 'map(.sid)|unique|length' "$current")" == 13 ]] && \
            assert_transition_state "$label" "$current" "$upstream" "$expectation" "$marker" 2>/dev/null && \
            { [[ "$(jq '.relations//[]|length' "$expectation")" == 0 ]] || assert_exact_sid_contract "$current" "$upstream" "$expectation" 2>/dev/null; }; then break; fi
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
    retain_clickhouse_observations "$observations_raw" "$observations" || die "ClickHouse relation evidence is corrupt or lacks source identity"
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
    local identifier="$1" commands evidence attempt before_start before_end load_start load_end after_end before_capacity before_util during_capacity during_util after_capacity after_util
    load_support_state
    commands="$(state_dir)/ssm-serverless-scale.sh"
    cat >"$commands" <<EOF
set -eu
endpoint=\$(aws rds describe-db-instances --region ${PRIMARY_REGION} --db-instance-identifier ${identifier}-1 --query 'DBInstances[0].Endpoint.Address' --output text)
for n in 1 2 3 4 5 6 7 8; do mysql --defaults-extra-file=/tmp/releem-topology/mysql.cnf --connect-timeout=10 --host=\"\$endpoint\" --execute 'SELECT BENCHMARK(20000000,SHA2(UUID(),512));' >/dev/null & done
wait
EOF
    chmod 600 "$commands"
    evidence="$(evidence_dir)/transitions/serverless-capacity-metrics.json"
    mkdir -p "$(dirname "$evidence")"
    before_end="$(date -u +%FT%TZ)"; before_start="$(date -u -d '5 minutes ago' +%FT%TZ)"
    before_capacity="$(serverless_metric_value "$identifier" ServerlessDatabaseCapacity "$before_start" "$before_end" Average)"
    before_util="$(serverless_metric_value "$identifier" ACUUtilization "$before_start" "$before_end" Average)"
    for attempt in 1 2 3; do
        load_start="$(date -u +%FT%TZ)"
        send_ssm_commands "$PRIMARY_REGION" "$EAST_RUNNER_ID" "$commands" "serverless-scale-${attempt}"
        load_end="$(date -u +%FT%TZ)"
        sleep 90
        after_end="$(date -u +%FT%TZ)"
        during_capacity="$(serverless_metric_value "$identifier" ServerlessDatabaseCapacity "$load_start" "$after_end" Maximum)"
        during_util="$(serverless_metric_value "$identifier" ACUUtilization "$load_start" "$after_end" Maximum)"
        after_capacity="$(serverless_metric_value "$identifier" ServerlessDatabaseCapacity "$load_end" "$after_end" Average)"
        after_util="$(serverless_metric_value "$identifier" ACUUtilization "$load_end" "$after_end" Average)"
        jq -n --argjson attempts "$attempt" --argjson before_capacity "$before_capacity" --argjson before_util "$before_util" \
          --argjson during_capacity "$during_capacity" --argjson during_util "$during_util" \
          --argjson after_capacity "$after_capacity" --argjson after_util "$after_util" \
          '{before:{capacity:$before_capacity,acu_utilization:$before_util},during:{capacity:$during_capacity,acu_utilization:$during_util},after:{capacity:$after_capacity,acu_utilization:$after_util},attempts:$attempts}' >"$evidence"
        chmod 600 "$evidence"
        assert_serverless_scale_evidence "$evidence" 3 2>/dev/null && return 0
    done
    assert_serverless_scale_evidence "$evidence" 3
}

serverless_metric_value() {
    local identifier="$1" metric="$2" start="$3" end="$4" statistic="$5" file
    file="$(state_dir)/cloudwatch-${identifier}-${metric}-${statistic}.json"
    aws_region "$PRIMARY_REGION" cloudwatch get-metric-statistics --namespace AWS/RDS --metric-name "$metric" \
        --dimensions "Name=DBClusterIdentifier,Value=${identifier}" --start-time "$start" --end-time "$end" \
        --period 60 --statistics "$statistic" --output json >"$file"
    jq -er --arg statistic "$statistic" '[.Datapoints[]?[$statistic]]|if length>0 then max else error("metric has no datapoints") end' "$file"
}

exercise_matrix() {
    local map_file aws_map provisioned serverless global_id global_west replica multi before marker expectation target_writer sids group_key member_key max_acu replica_sid old_writer old_primary_members
    map_file="$(evidence_dir)/sid-map.jsonl"
    marker="$(marker_ms)"
    wait_sid_map "$map_file"
    aws_map="$(evidence_dir)/aws-identity-map.jsonl"
    capture_aws_identity_map "$aws_map"
    provisioned="$(resource_name aurora-provisioned)"; serverless="$(resource_name aurora-serverless)"
    global_id="$(resource_name aurora-global)"; global_west="$(resource_name global-west)"
    replica="$(resource_name rds-replica)"; multi="$(resource_name rds-multi-az)"

    expectation="$(evidence_dir)/transitions/${marker}-aws-baseline-expectation.json"
    mkdir -p "$(dirname "$expectation")"
    build_exact_baseline_expectation "$aws_map" "$map_file" "$marker" "$expectation"
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
    add_exact_provider_expectations "$expectation" "$map_file" "$marker"
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
    add_exact_provider_expectations "$expectation" "$map_file" "$marker"
    collect_transition aurora-serverless-scaled "$marker" "$expectation" "$map_file"

    marker="$(marker_ms)"
    local target_global_arn old_global_arn
    target_global_arn="arn:aws:rds:${SECONDARY_REGION}:$(aws sts get-caller-identity --query Account --output text):cluster:${global_west}"
    old_global_arn="$(aws_region "$PRIMARY_REGION" rds describe-global-clusters --global-cluster-identifier "$global_id" --query 'GlobalClusters[0].GlobalClusterMembers[?IsWriter==`true`]|[0].DBClusterArn' --output text)"
    aws_region "$PRIMARY_REGION" rds switchover-global-cluster --global-cluster-identifier "$global_id" \
        --target-db-cluster-identifier "$target_global_arn" >/dev/null
    wait_cluster_available "$SECONDARY_REGION" "$global_west"
    wait_global_switchover "$PRIMARY_REGION" "$global_id" "$target_global_arn" "$old_global_arn" || die "global switchover did not converge before runtime deadline"
    expectation="$(evidence_dir)/transitions/${marker}-aurora-global-transition-expectation.json"
    group_key="$(relation_value_for_provider "$before" "$map_file" "${global_west}-1" aurora_global_database group_key)"
    old_primary_members="$(jq -sc --arg group "$group_key" '[.[]|select(.relation_type=="aurora_global_database" and .group_key==$group and .role=="primary_cluster_member")|.member_key]' "$before")"
    jq -n --arg group_key "$group_key" --arg transition managed-global-switchover --argjson old_primary "$old_primary_members" \
        '{group_key:$group_key,supported_transition:$transition,old_primary_member_keys:$old_primary}' >"$expectation"
    add_exact_provider_expectations "$expectation" "$map_file" "$marker"
    collect_transition aurora-global-transition "$marker" "$expectation" "$map_file"

    marker="$(marker_ms)"; mysql_rds_call "$replica" rds_stop_replication
    expectation="$(evidence_dir)/transitions/${marker}-rds-replica-stopped-expectation.json"
    replica_sid="$(jq -sr --arg id "$replica" 'map(select(.provider_id==$id))[0].sid' "$map_file")"
    jq -n --argjson sid "$replica_sid" '{sid:$sid}' >"$expectation"
    add_exact_provider_expectations "$expectation" "$map_file" "$marker"
    collect_transition rds-replica-stopped "$marker" "$expectation" "$map_file"
    marker="$(marker_ms)"; mysql_rds_call "$replica" rds_start_replication
    expectation="$(evidence_dir)/transitions/${marker}-rds-replica-resumed-expectation.json"
    jq -n --argjson sid "$replica_sid" '{sid:$sid}' >"$expectation"
    add_exact_provider_expectations "$expectation" "$map_file" "$marker"
    collect_transition rds-replica-resumed "$marker" "$expectation" "$map_file"

    marker="$(marker_ms)"
    aws_region "$PRIMARY_REGION" rds reboot-db-instance --db-instance-identifier "$multi" --force-failover >/dev/null
    wait_instance_available "$PRIMARY_REGION" "$multi"
    expectation="$(evidence_dir)/transitions/${marker}-rds-multi-az-failover-expectation.json"
    member_key="$(relation_value_for_provider "$before" "$map_file" "$multi" rds_multi_az member_key)"
    jq -n --arg member_key "$member_key" '{member_key:$member_key}' >"$expectation"
    add_exact_provider_expectations "$expectation" "$map_file" "$marker"
    collect_transition rds-multi-az-failover "$marker" "$expectation" "$map_file"
}

identifier_from_arn() { printf '%s\n' "${1##*:}"; }

cleanup_verified_support_iam() {
    local ownership_file="$1" runner_role="$2" runner_profile="$3" monitor_role="$4" preserve_db_dependencies="$5" failed=0
    if jq -e '.instance_profile.exists==true' "$ownership_file" >/dev/null; then
        cleanup_aws iam remove-role-from-instance-profile --instance-profile-name "$runner_profile" --role-name "$runner_role" >/dev/null 2>&1 || failed=1
        cleanup_aws iam delete-instance-profile --instance-profile-name "$runner_profile" >/dev/null 2>&1 || failed=1
    fi
    if jq -e '.runner_role.exists==true' "$ownership_file" >/dev/null; then
        cleanup_aws iam delete-role-policy --role-name "$runner_role" --policy-name ReleemTopologyRunnerRead >/dev/null 2>&1 || failed=1
        cleanup_aws iam detach-role-policy --role-name "$runner_role" --policy-arn arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore >/dev/null 2>&1 || failed=1
        cleanup_aws iam delete-role --role-name "$runner_role" >/dev/null 2>&1 || failed=1
    fi
    if ! "$preserve_db_dependencies" && jq -e '.monitoring_role.exists==true' "$ownership_file" >/dev/null; then
        cleanup_aws iam detach-role-policy --role-name "$monitor_role" --policy-arn arn:aws:iam::aws:policy/service-role/AmazonRDSEnhancedMonitoringRole >/dev/null 2>&1 || failed=1
        cleanup_aws iam delete-role --role-name "$monitor_role" >/dev/null 2>&1 || failed=1
    fi
    return "$failed"
}

cleanup_resources_impl() {
    local cleanup_failed=0
    run_cleanup_steps stop_agents || cleanup_failed=1
    local dir region arn id sg global_id cluster_arn cluster_region runner account bucket runner_role runner_profile monitor_role post ownership_file deleted_instances preserve_db_dependencies=false
    dir="$(state_dir)/cleanup"
    mkdir -p "$dir"
    deleted_instances="$dir/deleted-instances.tsv"
    : >"$deleted_instances"
    capture_inventory "$dir/before.json" || cleanup_failed=1
    recover_monitoring_resource_ids || { log "Enhanced Monitoring resource ID recovery incomplete"; cleanup_failed=1; }

    # Addressable databases must disappear before cluster/global/network dependencies.
    for region in "$PRIMARY_REGION" "$SECONDARY_REGION"; do
        inventory_region "$region" "$dir/${region}.json" || { cleanup_failed=1; continue; }
        while IFS= read -r arn; do
            id="$(identifier_from_arn "$arn")"
            delete_db_instance_if_monitoring_tracked "$region" "$id" "$deleted_instances" || {
                log "database deletion skipped until Enhanced Monitoring resource ID is recorded: $region/$id"
                cleanup_failed=1
            }
        done < <(jq -r --arg source ":$(resource_name rds-source)" '.instances[]|select(endswith($source)|not)' "$dir/${region}.json")
        while IFS=$'\t' read -r deleted_region id; do
            [[ "$deleted_region" == "$region" ]] || continue
            if cleanup_remaining_seconds >/dev/null; then cleanup_wait_until "database deletion $region/$id" cleanup_db_instance_absent "$region" "$id" || cleanup_failed=1; fi
        done <"$deleted_instances"
        while IFS= read -r arn; do
            id="$(identifier_from_arn "$arn")"
            if delete_db_instance_if_monitoring_tracked "$region" "$id" "$deleted_instances"; then
                if cleanup_remaining_seconds >/dev/null; then cleanup_wait_until "database deletion $region/$id" cleanup_db_instance_absent "$region" "$id" || cleanup_failed=1; fi
            else
                log "source database deletion skipped until Enhanced Monitoring resource ID is recorded: $region/$id"
                cleanup_failed=1
            fi
        done < <(jq -r --arg source ":$(resource_name rds-source)" '.instances[]|select(endswith($source))' "$dir/${region}.json")
    done
    if [[ -f "$(monitoring_tracking_file)" ]]; then
        while IFS=$'\t' read -r region identifier lifecycle id; do
            [[ "$lifecycle" != created_with_resource_id ]] && continue
            delete_monitoring_stream_until_absent "$region" "$id" || cleanup_failed=1
        done <"$(monitoring_tracking_file)"
    fi

    if ! db_dependencies_may_be_deleted; then
        preserve_db_dependencies=true
        cleanup_failed=1
        log "database dependencies preserved because create attempts remain unresolved"
    fi

    # Aurora Global members must be detached before regional clusters can be removed.
    if ! "$preserve_db_dependencies"; then
    inventory_global_clusters "$dir/globals.json" || { cleanup_failed=1; printf '[]\n' >"$dir/globals.json"; }
    while IFS= read -r arn; do
        global_id="$(identifier_from_arn "$arn")"
        while IFS= read -r cluster_arn; do
            [[ -n "$cluster_arn" ]] || continue
            cluster_region="${cluster_arn#arn:aws:rds:}"; cluster_region="${cluster_region%%:*}"
            cleanup_aws_region "$cluster_region" rds remove-from-global-cluster \
                --global-cluster-identifier "$global_id" --db-cluster-identifier "$cluster_arn" >/dev/null 2>&1 || cleanup_failed=1
            cleanup_wait_until "global member detachment" global_member_absent "$global_id" "$cluster_arn" || cleanup_failed=1
        done < <(cleanup_aws_region "$PRIMARY_REGION" rds describe-global-clusters --global-cluster-identifier "$global_id" \
            --output json 2>/dev/null | jq -r '.GlobalClusters[0].GlobalClusterMembers|sort_by(.IsWriter)|.[].DBClusterArn')
    done < <(jq -r '.[].arn' "$dir/globals.json")
    fi

    if ! "$preserve_db_dependencies"; then
    for region in "$PRIMARY_REGION" "$SECONDARY_REGION"; do
        [[ -f "$dir/${region}.json" ]] || continue
        while IFS= read -r arn; do
            id="$(identifier_from_arn "$arn")"
            cleanup_aws_region "$region" rds delete-db-snapshot --db-snapshot-identifier "$id" >/dev/null 2>&1 || cleanup_failed=1
        done < <(jq -r '.snapshots[]' "$dir/${region}.json")
        while IFS= read -r arn; do
            id="$(identifier_from_arn "$arn")"
            cleanup_aws_region "$region" rds delete-db-cluster --db-cluster-identifier "$id" --skip-final-snapshot >/dev/null 2>&1 || cleanup_failed=1
        done < <(jq -r '.clusters[]' "$dir/${region}.json")
        while IFS= read -r arn; do
            id="$(identifier_from_arn "$arn")"
            if cleanup_remaining_seconds >/dev/null; then cleanup_wait_until "cluster deletion $region/$id" cleanup_db_cluster_absent "$region" "$id" || cleanup_failed=1; fi
        done < <(jq -r '.clusters[]' "$dir/${region}.json")
    done
    while IFS= read -r arn; do
        id="$(identifier_from_arn "$arn")"
        cleanup_aws_region "$PRIMARY_REGION" rds delete-global-cluster --global-cluster-identifier "$id" >/dev/null 2>&1 || cleanup_failed=1
    done < <(jq -r '.[].arn' "$dir/globals.json")
    fi

    # Stop SSM work first, then terminate runners and wait for their tagged ENIs.
    for region in "$PRIMARY_REGION" "$SECONDARY_REGION"; do
        while IFS= read -r runner; do
            [[ -n "$runner" ]] || continue
            cleanup_aws_region "$region" ec2 terminate-instances --instance-ids "$runner" >/dev/null 2>&1 || cleanup_failed=1
            if cleanup_remaining_seconds >/dev/null; then cleanup_wait_until "runner termination $region/$runner" cleanup_runner_absent "$region" "$runner" || cleanup_failed=1; fi
        done < <(cleanup_aws_region "$region" ec2 describe-instances \
            --filters "Name=tag:${TAG_RUN_KEY},Values=$(run_id)" "Name=tag:${TAG_MANAGED_KEY},Values=true" \
            "Name=instance-state-name,Values=pending,running,stopping,stopped,shutting-down" \
            --query 'Reservations[].Instances[].InstanceId' --output text 2>/dev/null | tr '\t' '\n')
        cleanup_wait_until "runner ENI deletion in $region" runner_enis_absent "$region" || cleanup_failed=1
    done

    if ! "$preserve_db_dependencies"; then
    for region in "$PRIMARY_REGION" "$SECONDARY_REGION"; do
        inventory_region "$region" "$dir/${region}-late.json" || { cleanup_failed=1; continue; }
        while IFS= read -r arn; do
            id="$(identifier_from_arn "$arn")"
            if [[ "$arn" == *":cluster-pg:"* ]]; then
                cleanup_aws_region "$region" rds delete-db-cluster-parameter-group --db-cluster-parameter-group-name "$id" >/dev/null 2>&1 || cleanup_failed=1
            else
                cleanup_aws_region "$region" rds delete-db-parameter-group --db-parameter-group-name "$id" >/dev/null 2>&1 || cleanup_failed=1
            fi
        done < <(jq -r '.parameter_groups[]' "$dir/${region}-late.json")
        while IFS= read -r arn; do
            sg="${arn##*/}"
            cleanup_aws_region "$region" ec2 delete-security-group --group-id "$sg" >/dev/null 2>&1 || cleanup_failed=1
        done < <(jq -r '.security_groups[]' "$dir/${region}-late.json")
        while IFS= read -r arn; do
            sg="${arn##*/}"
            cleanup_aws_region "$region" ec2 delete-security-group --group-id "$sg" >/dev/null 2>&1 || cleanup_failed=1
        done < <(jq -r '.security_groups[]' "$dir/${region}-late.json")
        while IFS= read -r arn; do
            id="$(identifier_from_arn "$arn")"
            cleanup_aws_region "$region" rds delete-db-subnet-group --db-subnet-group-name "$id" >/dev/null 2>&1 || cleanup_failed=1
        done < <(jq -r '.subnet_groups[]' "$dir/${region}-late.json")
    done
    fi

    # Private delivery objects precede the bucket; instance profile precedes roles.
    account="$(aws_global sts get-caller-identity --query Account --output text 2>/dev/null || true)"
    bucket="${S3_BUCKET:-releem-topology-$(printf '%s' "$account" | sha256sum | cut -c1-12)-$(run_id)}"
    runner_role="${RUNNER_ROLE:-$(resource_name runner-role)}"
    runner_profile="${RUNNER_PROFILE:-$(resource_name runner-profile)}"
    monitor_role="${MONITORING_ROLE:-$(resource_name monitoring-role)}"
    ownership_file="$dir/support-ownership.json"
    if (capture_destroy_support_ownership "$ownership_file" &&
        assert_destroy_support_ownership "$ownership_file" "arn:aws:s3:::${bucket}/$(run_id)/*" "$runner_role" "$monitor_role"); then
        if [[ "$bucket" =~ ^[a-z0-9.-]{3,63}$ ]] && [[ "$(s3_bucket_presence "$bucket")" == present ]]; then
            while IFS= read -r key; do
                delete_s3_object_until_absent "$bucket" "$key" || cleanup_failed=1
            done < <(bootstrap_secret_object_keys)
            if ! aws_region "$PRIMARY_REGION" s3api list-objects-v2 --bucket "$bucket" --output json >"$dir/list-objects.json"; then
                log "S3 object inventory failed during cleanup"
                cleanup_failed=1
            elif ! jq '{Objects:[.Contents[]?|{Key:.Key}],Quiet:true}' "$dir/list-objects.json" >"$dir/delete-objects.json"; then
                log "S3 object inventory was not valid JSON"
                cleanup_failed=1
            elif jq -e '.Objects|length>0' "$dir/delete-objects.json" >/dev/null; then
                aws_region "$PRIMARY_REGION" s3api delete-objects --bucket "$bucket" --delete "file://$dir/delete-objects.json" >/dev/null 2>&1 || cleanup_failed=1
            fi
            aws_region "$PRIMARY_REGION" s3api delete-bucket --bucket "$bucket" >/dev/null 2>&1 || cleanup_failed=1
        fi
        cleanup_verified_support_iam "$ownership_file" "$runner_role" "$runner_profile" "$monitor_role" "$preserve_db_dependencies" || cleanup_failed=1
    else
        log "support cleanup skipped because exact ownership could not be proven"
        cleanup_failed=1
    fi

    rm -rf "$(state_dir)/agents" "$(state_dir)/packages"
    rm -f "$(secret_file)" "$(state_dir)"/create-*.json "$(state_dir)"/ssm-* \
        "$(state_dir)"/runner-*.json "$(state_dir)"/runner-policy.json "$(state_dir)"/trust.json 2>/dev/null || true
    post="$(evidence_dir)/post-cleanup-inventory.json"
    local remaining sleep_seconds
    while remaining="$(cleanup_remaining_seconds)"; do
        rm -f "$post"
        if (set -e; capture_inventory "$post") && [[ -s "$post" ]] &&
            assert_terminal_cleanup_state "$post" 2>/dev/null; then break; fi
        sleep_seconds="$(cleanup_poll_seconds "$POLL_SECONDS" "$remaining")"
        ((sleep_seconds > 0)) && sleep "$sleep_seconds"
    done
    if ! cleanup_remaining_seconds >/dev/null; then
        CLEANUP_BUDGET_EXHAUSTED=true
        log "cleanup budget exhausted; run destroy with exact confirmation"
        cleanup_failed=1
    fi
    if [[ -s "$post" ]]; then
        (assert_inventory_empty "$post") || cleanup_failed=1
        if [[ -f "$(evidence_dir)/preflight/inventory-before.json" ]]; then
            (assert_inventory_matches "$(evidence_dir)/preflight/inventory-before.json" "$post") || cleanup_failed=1
        fi
    else
        cleanup_failed=1
    fi
    if ((cleanup_failed == 0)); then
        rm -rf "$(state_dir)"
        CLEANUP_ARMED=false
    fi
    return "$cleanup_failed"
}

stop_watchdog() {
    [[ -z "$WATCHDOG_PID" ]] && return 0
    kill "$WATCHDOG_PID" >/dev/null 2>&1 || true
    wait "$WATCHDOG_PID" 2>/dev/null || true
    WATCHDOG_PID=''
}

defer_cleanup_signal() { PENDING_SIGNAL_STATUS="$1"; }

cleanup_resources() {
    "$CLEANUP_DONE" && return 0
    "$CLEANUP_RUNNING" && return 0
    local restore_errexit=false cleanup_status=0 pending_status
    [[ $- == *e* ]] && restore_errexit=true
    CLEANUP_RUNNING=true
    PENDING_SIGNAL_STATUS=0
    initialize_cleanup_deadline
    set +e
    stop_watchdog
    trap 'defer_cleanup_signal 129' HUP
    trap 'defer_cleanup_signal 130' INT
    trap 'defer_cleanup_signal 143' TERM
    cleanup_resources_impl
    cleanup_status=$?
    pending_status="$PENDING_SIGNAL_STATUS"
    trap - HUP INT TERM
    CLEANUP_RUNNING=false
    if ((cleanup_status == 0)); then CLEANUP_DONE=true; fi
    "$restore_errexit" && set -e
    ((pending_status != 0)) && return "$pending_status"
    return "$cleanup_status"
}

global_member_absent() {
    local count
    count="$(aws_region "$PRIMARY_REGION" rds describe-global-clusters --global-cluster-identifier "$1" \
        --query "length(GlobalClusters[0].GlobalClusterMembers[?DBClusterArn=='$2'])" --output text 2>/dev/null)" || return 1
    [[ "$count" == 0 ]]
}

runner_enis_absent() {
    local count
    count="$(aws_region "$1" ec2 describe-network-interfaces \
        --filters "Name=tag:${TAG_RUN_KEY},Values=$(run_id)" "Name=tag:${TAG_MANAGED_KEY},Values=true" \
        --query 'length(NetworkInterfaces)' --output text 2>/dev/null)" || return 1
    [[ "$count" == 0 ]]
}

monitoring_stream_count() {
    local region="$1" id="$2" count
    count="$(aws_region "$region" logs describe-log-streams --log-group-name RDSOSMetrics \
        --log-stream-name-prefix "$id" --query "length(logStreams[?logStreamName=='${id}'])" --output text 2>/dev/null)" || return 1
    [[ "$count" =~ ^[0-9]+$ ]] || return 1
    printf '%s\n' "$count"
}

delete_monitoring_stream_until_absent() {
    local region="$1" id="$2" count
    count="$(monitoring_stream_count "$region" "$id")" || return 1
    [[ "$count" == 0 ]] && return 0
    [[ "$count" == 1 ]] || return 1
    aws_region "$region" logs delete-log-stream --log-group-name RDSOSMetrics --log-stream-name "$id" >/dev/null 2>&1 || return 1
    count="$(monitoring_stream_count "$region" "$id")" || return 1
    [[ "$count" == 0 ]]
}

monitoring_streams_absent() {
    local region identifier lifecycle id count
    monitoring_tracking_complete || return 1
    while IFS=$'\t' read -r region identifier lifecycle id; do
        [[ "$lifecycle" != created_with_resource_id ]] && continue
        count="$(monitoring_stream_count "$region" "$id")" || return 1
        [[ "$count" == 0 ]] || return 1
    done <"$(monitoring_tracking_file)"
}

assert_terminal_cleanup_state() {
    local inventory="$1"
    assert_inventory_empty "$inventory" && monitoring_streams_absent
}

cleanup_once() {
    "$CLEANUP_DONE" && return 0
    cleanup_resources
}

on_signal() {
    local signal="$1" status="$2"
    trap - EXIT INT TERM HUP
    set +e
    stop_watchdog
    if "$CLEANUP_ARMED"; then cleanup_once; else rm -f "$(secret_file)" 2>/dev/null; fi
    log "terminated by ${signal} after cleanup"
    exit "$status"
}

on_exit() {
    local status="${1:-$?}" cleanup_status=0
    trap - EXIT
    set +e
    stop_watchdog
    if "$CLEANUP_ARMED"; then
        cleanup_once || cleanup_status=$?
    else
        rm -f "$(secret_file)" 2>/dev/null || cleanup_status=$?
    fi
    ((status == 0 && cleanup_status != 0)) && status=$cleanup_status
    exit "$status"
}

run_matrix() {
    validate_run_id
    require_runtime
    for command in aws flock jq openssl mysql curl timeout tar sha256sum; do require_command "$command"; done
    validate_runtime_deadline "$RUNTIME_DEADLINE_SECONDS"
    validate_cleanup_deadline "$CLEANUP_DEADLINE_SECONDS"
    RUN_DEADLINE_EPOCH=$(( $(date +%s) + RUNTIME_DEADLINE_SECONDS ))
    trap on_exit EXIT
    trap 'on_signal HUP 129' HUP
    trap 'on_signal INT 130' INT
    trap 'on_signal TERM 143' TERM
    (sleep "$RUNTIME_DEADLINE_SECONDS"; kill -TERM "$$") &
    WATCHDOG_PID=$!
    preflight
    create_matrix
    assert_all_db_instances_safe
    wait_all_monitoring_events
    start_agents
    exercise_matrix
    stop_watchdog
    cleanup_once
    trap - EXIT INT TERM HUP
}

inventory() {
    validate_run_id
    require_command aws
    require_command flock
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
    require_command flock
    require_command jq
    verify_destroy_ownership
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
