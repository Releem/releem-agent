#!/usr/bin/env bats

setup() {
    TEST_TMPDIR="$(mktemp -d)"
    SCRIPT="${BATS_TEST_DIRNAME}/aws_rds_topology.sh"
    export AWS_TOPOLOGY_RUN_ID=task13-20260908
    export AWS_TOPOLOGY_STATE_DIR="$TEST_TMPDIR/state"
    export AWS_TOPOLOGY_EVIDENCE_DIR="$TEST_TMPDIR/evidence"
    if [[ "$BATS_TEST_DESCRIPTION" == "S3 secret deletion stops retries and sleeps at the shared cleanup deadline" ]]; then
        export AWS_TOPOLOGY_POLL_SECONDS=20
    else
        export AWS_TOPOLOGY_POLL_SECONDS=0
    fi
    source "$SCRIPT" help >/dev/null
}

@test "cleanup disables errexit internally and remains retryable until success" {
    marker="$TEST_TMPDIR/cleanup-retry"
    attempts="$TEST_TMPDIR/cleanup-attempts"
    printf '0\n' >"$attempts"
    cleanup_resources_impl() {
        count="$(cat "$attempts")"
        count=$((count + 1))
        printf '%s\n' "$count" >"$attempts"
        false
        printf 'continued-%s\n' "$count" >>"$marker"
        [ "$count" -gt 1 ]
    }
    CLEANUP_ARMED=true
    CLEANUP_DONE=false

    set -e
    cleanup_once || first_status=$?
    set +e
    [ "${first_status:-0}" -ne 0 ]
    [ "$CLEANUP_DONE" = false ]
    cleanup_once

    [ "$CLEANUP_DONE" = true ]
    [ "$(<"$marker")" = $'continued-1\ncontinued-2' ]
}

@test "successful cleanup returns zero and workflow continues under errexit" {
    marker="$TEST_TMPDIR/cleanup-success"
    cleanup_resources_impl() { printf 'cleaned\n' >"$marker"; }
    CLEANUP_ARMED=true

    set -e
    cleanup_once
    printf 'continued\n' >>"$marker"
    set +e

    [ "$(<"$marker")" = $'cleaned\ncontinued' ]
}

@test "cleanup stops watchdog and defers TERM until dependency cleanup finishes" {
    marker="$TEST_TMPDIR/cleanup-signal"
    sleep 60 &
    WATCHDOG_PID=$!
    cleanup_resources_impl() {
        ! kill -0 "$WATCHDOG_PID" 2>/dev/null
        kill -TERM "$BASHPID"
        printf 'dependency-finished\n' >"$marker"
    }
    CLEANUP_ARMED=true

    run cleanup_once

    [ "$status" -eq 143 ]
    [ "$(<"$marker")" = dependency-finished ]
}

@test "IAM ownership inventory rejects ambiguous role and dependent API failures" {
    aws() {
        case "$*" in
            iam\ get-role\ *) printf '%s\n' 'AccessDenied' >&2; return 254 ;;
            *) return 1 ;;
        esac
    }
    run capture_iam_role_ownership runner-role runner "$TEST_TMPDIR/role.json"
    [ "$status" -ne 0 ]

    aws() {
        case "$*" in
            iam\ get-role\ *) printf '%s\n' '{"Role":{"Arn":"arn:aws:iam::111111111111:role/runner-role"}}' ;;
            iam\ list-role-tags\ *) printf '%s\n' 'Throttling' >&2; return 254 ;;
            *) return 1 ;;
        esac
    }
    run capture_iam_role_ownership runner-role runner "$TEST_TMPDIR/role.json"
    [ "$status" -ne 0 ]
}

@test "IAM absence accepts only NoSuchEntity and ownership rejects empty relationships" {
    aws() { printf '%s\n' 'An error occurred (NoSuchEntity) when calling the GetRole operation: Role not found' >&2; return 254; }
    run iam_entity_presence role missing-role
    [ "$status" -eq 0 ]
    [ "$output" = absent ]

    aws() { printf '%s\n' 'AccessDenied' >&2; return 254; }
    run iam_entity_presence profile profile-name
    [ "$status" -ne 0 ]

    fixture="$TEST_TMPDIR/empty-relationships.json"
    printf '%s\n' '{"bucket":{"exists":false},"runner_role":{"exists":true,"tags":{"releem-topology-run":"task13-20260908","releem-topology-managed":"true"},"attached_policies":[],"inline_policy_names":[],"inline_policy_name":null,"inline_policy":{}},"monitoring_role":{"exists":false},"instance_profile":{"exists":true,"tags":{"releem-topology-run":"task13-20260908","releem-topology-managed":"true"},"roles":[]}}' >"$fixture"
    run assert_destroy_support_ownership "$fixture" 'arn:aws:s3:::bucket/task13-20260908/*' releem-task13-20260908-runner-role releem-task13-20260908-monitoring-role
    [ "$status" -ne 0 ]
}

@test "S3 secret deletion retries ambiguous head-object failures until explicit absence" {
    calls="$TEST_TMPDIR/head-object-calls"
    printf '0\n' >"$calls"
    aws_region() {
        case "$*" in
            *s3api\ delete-object*) return 0 ;;
            *s3api\ head-object*)
                count="$(cat "$calls")"; count=$((count + 1)); printf '%s\n' "$count" >"$calls"
                if [ "$count" -eq 1 ]; then printf '%s\n' 'AccessDenied' >&2; return 254; fi
                printf '%s\n' 'An error occurred (404) when calling the HeadObject operation: Not Found' >&2
                return 254
                ;;
            *) return 1 ;;
        esac
    }

    run delete_s3_object_until_absent bucket secret us-east-1

    [ "$status" -eq 0 ]
    [ "$(cat "$calls")" -eq 2 ]
}

@test "S3 secret deletion fails when head-object remains ambiguous" {
    aws_region() {
        case "$*" in
            *s3api\ delete-object*) return 0 ;;
            *s3api\ head-object*) printf '%s\n' 'RequestTimeout' >&2; return 254 ;;
            *) return 1 ;;
        esac
    }
    run delete_s3_object_until_absent bucket secret us-east-1
    [ "$status" -ne 0 ]
}

@test "S3 secret deletion stops retries and sleeps at the shared cleanup deadline" {
    calls="$TEST_TMPDIR/s3-deadline-calls"
    expired="$TEST_TMPDIR/s3-deadline-expired"
    CLEANUP_DEADLINE_EPOCH=103
    cleanup_remaining_seconds() {
        [[ ! -e "$expired" ]] || return 1
        printf '3\n'
    }
    sleep() {
        printf 'sleep:%s\n' "$1" >>"$calls"
        : >"$expired"
    }
    aws_region() {
        printf 'aws:%s\n' "$*" >>"$calls"
        return 1
    }

    run delete_s3_object_until_absent bucket secret us-east-1

    [ "$status" -ne 0 ]
    [ "$(grep -c '^sleep:' "$calls")" -eq 1 ]
    [ "$(grep '^sleep:' "$calls")" = 'sleep:3' ]
    [ "$(grep -c '^aws:' "$calls")" -eq 1 ]
}

@test "exact SID contract rejects wrong role flags state and extra provider rows" {
    current="$TEST_TMPDIR/current-strict.jsonl"
    upstreams="$TEST_TMPDIR/upstreams-strict.jsonl"
    expected="$TEST_TMPDIR/expected-strict.json"
    : >"$upstreams"
    printf '%s\n' '{"sid":101,"relation_type":"aurora_cluster","group_key":"aurora:c1","parent_group_key":null,"member_key":"db:i1","primary_member_key":"db:i1","role":"primary","is_writer":1,"is_reader":1,"replication_state":"healthy","last_seen_epoch_ms":1710000004000}' >"$current"
    printf '%s\n' '{"marker_epoch_ms":1710000003500,"relations":[{"sid":101,"relation_type":"aurora_cluster","group_key":"aurora:c1","parent_group_key":null,"member_key":"db:i1","primary_member_key":"db:i1","role":"primary","is_writer":1,"is_reader":1,"replication_state":"healthy"}],"upstreams":[]}' >"$expected"

    run assert_exact_sid_contract "$current" "$upstreams" "$expected"
    [ "$status" -eq 0 ]

    jq '.is_writer=0 | .is_reader=0 | .replication_state="degraded"' "$current" >"$current.bad"
    run assert_exact_sid_contract "$current.bad" "$upstreams" "$expected"
    [ "$status" -ne 0 ]

    printf '%s\n' '{"sid":101,"relation_type":"aurora_global_database","group_key":"aurora-global:g1","parent_group_key":null,"member_key":"db:i1","primary_member_key":"db:i1","role":"primary_cluster_member","is_writer":1,"is_reader":1,"replication_state":"healthy","last_seen_epoch_ms":1710000004000}' >>"$current"
    run assert_exact_sid_contract "$current" "$upstreams" "$expected"
    [ "$status" -ne 0 ]
}

@test "native contract rejects an unlisted extra relation for a scoped SID" {
    current="$TEST_TMPDIR/native-current.jsonl"
    upstreams="$TEST_TMPDIR/native-upstreams.jsonl"
    sidmap="$TEST_TMPDIR/native-sidmap.jsonl"
    source="releem-task13-20260908-rds-source"
    replica="releem-task13-20260908-rds-replica"
    printf '%s\n' "{\"sid\":101,\"provider_id\":\"$source\"}" "{\"sid\":202,\"provider_id\":\"$replica\"}" >"$sidmap"
    printf '%s\n' \
      '{"sid":101,"relation_type":"standalone","member_key":"mysql:source","last_seen_epoch_ms":1710000004000}' \
      '{"sid":202,"relation_type":"async_replication","member_key":"mysql:replica","primary_member_key":"mysql:source","last_seen_epoch_ms":1710000004000}' \
      '{"sid":202,"relation_type":"galera_cluster","member_key":"mysql:replica","last_seen_epoch_ms":1710000004000}' >"$current"
    printf '%s\n' '{"sid":202,"upstream_member_key":"mysql:source","last_seen_epoch_ms":1710000004000}' >"$upstreams"

    run capture_native_identity_map "$current" "$sidmap" "$TEST_TMPDIR/native-identities.json"

    [ "$status" -ne 0 ]
}

@test "ClickHouse evidence derives source edges only from persisted relations JSON" {
    observations="$TEST_TMPDIR/ch-relations.jsonl"
    evidence="$TEST_TMPDIR/ch-evidence.jsonl"
    printf '%s\n' '{"sid":202,"rid":"rid-b","observed_epoch_ms":1710000004000,"relations":"[{\"relation_type\":\"rds_read_replica\",\"group_key\":\"rds:g\",\"parent_group_key\":\"rds:g\",\"member_key\":\"db:replica\",\"primary_member_key\":\"db:source\",\"role\":\"replica\",\"is_writer\":0,\"is_reader\":1,\"replication_state\":\"healthy\"}]"}' >"$observations"

    run retain_clickhouse_observations "$observations" "$evidence"
    [ "$status" -eq 0 ]
    run jq -e '.source=="clickhouse" and .source_edges==[{"relation_type":"rds_read_replica","group_key":"rds:g","member_key":"db:replica","source_member_key":"db:source","replication_state":"healthy"}]' "$evidence"
    [ "$status" -eq 0 ]

    jq -c '.relations=(.relations|fromjson|map(.primary_member_key=null)|tojson)' "$observations" >"$observations.bad"
    run retain_clickhouse_observations "$observations.bad" "$evidence"
    [ "$status" -ne 0 ]
}

@test "global switchover waits for one ready target writer and old primary demotion" {
    calls="$TEST_TMPDIR/global-calls"
    printf '0\n' >"$calls"
    aws_region() {
        count="$(cat "$calls")"; count=$((count + 1)); printf '%s\n' "$count" >"$calls"
        if [ "$count" -eq 1 ]; then
            printf '%s\n' '{"Status":"modifying","GlobalClusterMembers":[{"DBClusterArn":"arn:old","IsWriter":true,"SynchronizationStatus":"connected"},{"DBClusterArn":"arn:new","IsWriter":false,"SynchronizationStatus":"connected"}]}'
        else
            printf '%s\n' '{"Status":"available","GlobalClusterMembers":[{"DBClusterArn":"arn:old","IsWriter":false,"SynchronizationStatus":"connected"},{"DBClusterArn":"arn:new","IsWriter":true,"SynchronizationStatus":"connected"}]}'
        fi
    }
    RUN_DEADLINE_EPOCH=$(( $(date +%s) + 30 ))

    run wait_global_switchover us-east-1 global-one arn:new arn:old

    [ "$status" -eq 0 ]
    [ "$(cat "$calls")" -eq 2 ]
}

teardown() {
    rm -rf "$TEST_TMPDIR"
}

@test "run ID rejects values that could escape deterministic names" {
    AWS_TOPOLOGY_RUN_ID='Task13_bad/value'

    run validate_run_id

    [ "$status" -ne 0 ]
}

@test "resource names are deterministic and bounded" {
    run resource_name aurora-provisioned

    [ "$status" -eq 0 ]
    [ "$output" = 'releem-task13-20260908-aurora-provisioned' ]
    [ "${#output}" -le 63 ]
}

@test "latest Aurora MySQL 3 selection uses semantic version ordering" {
    fixture="$TEST_TMPDIR/versions.json"
    cat >"$fixture" <<'EOF'
{"DBEngineVersions":[{"EngineVersion":"8.0.mysql_aurora.3.08.2","Status":"available"},{"EngineVersion":"8.0.mysql_aurora.3.10.0","Status":"available"},{"EngineVersion":"5.7.mysql_aurora.2.12.4","Status":"available"},{"EngineVersion":"8.0.mysql_aurora.3.09.1","Status":"deprecated"}]}
EOF

    run select_latest_aurora_version "$fixture"

    [ "$status" -eq 0 ]
    [ "$output" = '8.0.mysql_aurora.3.10.0' ]
}

@test "smallest orderable class prefers memory before burstable families" {
    fixture="$TEST_TMPDIR/classes.json"
    cat >"$fixture" <<'EOF'
{"OrderableDBInstanceOptions":[{"DBInstanceClass":"db.r6g.large"},{"DBInstanceClass":"db.t4g.medium"},{"DBInstanceClass":"db.r7g.large"},{"DBInstanceClass":"db.t3.small"}]}
EOF

    run select_smallest_orderable_class "$fixture"

    [ "$status" -eq 0 ]
    [ "$output" = 'db.t3.small' ]
}

@test "minimum Serverless v2 ACU is selected from engine features" {
    fixture="$TEST_TMPDIR/features.json"
    cat >"$fixture" <<'EOF'
{"DBEngineVersions":[{"EngineVersion":"8.0.mysql_aurora.3.10.0","SupportedEngineModes":["provisioned"],"SupportedFeatureNames":["Global databases","ServerlessV2"],"ServerlessV2FeaturesSupport":{"MinCapacity":0.5,"MaxCapacity":256}}]}
EOF

    run select_min_serverless_acu "$fixture" '8.0.mysql_aurora.3.10.0'

    [ "$status" -eq 0 ]
    [ "$output" = '0.5' ]
}

@test "cross-region selection only accepts versions and classes available in both regions" {
    east_versions="$TEST_TMPDIR/east-versions.json"
    west_versions="$TEST_TMPDIR/west-versions.json"
    east_classes="$TEST_TMPDIR/east-classes.json"
    west_classes="$TEST_TMPDIR/west-classes.json"
    printf '%s\n' '{"DBEngineVersions":[{"EngineVersion":"8.0.mysql_aurora.3.10.0","Status":"available"},{"EngineVersion":"8.0.mysql_aurora.3.09.1","Status":"available"}]}' >"$east_versions"
    printf '%s\n' '{"DBEngineVersions":[{"EngineVersion":"8.0.mysql_aurora.3.09.1","Status":"available"}]}' >"$west_versions"
    printf '%s\n' '{"OrderableDBInstanceOptions":[{"DBInstanceClass":"db.t3.small"},{"DBInstanceClass":"db.t4g.medium"}]}' >"$east_classes"
    printf '%s\n' '{"OrderableDBInstanceOptions":[{"DBInstanceClass":"db.t4g.medium"}]}' >"$west_classes"

    run select_latest_common_aurora_version "$east_versions" "$west_versions"
    [ "$status" -eq 0 ]
    [ "$output" = '8.0.mysql_aurora.3.09.1' ]

    run select_smallest_common_orderable_class "$east_classes" "$west_classes"
    [ "$status" -eq 0 ]
    [ "$output" = 'db.t4g.medium' ]
}

@test "ownership assertion rejects a matching name with foreign tags" {
    fixture="$TEST_TMPDIR/resources.json"
    cat >"$fixture" <<'EOF'
[{"arn":"arn:aws:rds:us-east-1:111111111111:cluster:releem-task13-20260908-aurora-provisioned","tags":{"releem-topology-run":"someone-else","releem-topology-managed":"true"}}]
EOF

    run assert_inventory_owned "$fixture"

    [ "$status" -ne 0 ]
}

@test "TERM cleans exactly once exits 143 and never resumes" {
    marker="$TEST_TMPDIR/signal-cleanup"
    CLEANUP_ARMED=true
    cleanup_resources() { printf 'cleanup\n' >>"$marker"; }

    run on_signal TERM 143

    [ "$status" -eq 143 ]
    [ "$(wc -l <"$marker")" -eq 1 ]
    [[ "$output" != *resumed* ]]
}

@test "EXIT before first resource performs local cleanup only" {
    marker="$TEST_TMPDIR/unarmed-cleanup"
    cleanup_resources() { printf 'cloud\n' >>"$marker"; }
    secret_file() { printf '%s\n' "$TEST_TMPDIR/local-secret"; }
    printf 'secret\n' >"$(secret_file)"
    CLEANUP_ARMED=false

    run on_exit 0

    [ "$status" -eq 0 ]
    [ ! -e "$marker" ]
    [ ! -e "$(secret_file)" ]
}

@test "cleanup steps continue after an SSM stop failure" {
    marker="$TEST_TMPDIR/cleanup-steps"
    failed_stop() { printf 'stop\n' >>"$marker"; return 1; }
    delete_databases() { printf 'databases\n' >>"$marker"; }
    delete_support() { printf 'support\n' >>"$marker"; }

    run run_cleanup_steps failed_stop delete_databases delete_support

    [ "$status" -ne 0 ]
    [ "$(<"$marker")" = $'stop\ndatabases\nsupport' ]
}

@test "destroy ownership rejects foreign bucket and mismatched IAM relationships" {
    fixture="$TEST_TMPDIR/support-ownership.json"
    cat >"$fixture" <<'EOF'
{"bucket":{"exists":true,"tags":{"releem-topology-run":"task13-20260908","releem-topology-managed":"true"}},"runner_role":{"exists":true,"tags":{"releem-topology-run":"task13-20260908","releem-topology-managed":"true"},"path":"/releem-topology/","trust_policy":{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]},"inline_policy_names":["ReleemTopologyRunnerRead"],"inline_policy_name":"ReleemTopologyRunnerRead","inline_policy":{"Version":"2012-10-17","Statement":[{"Sid":"ReadRunObjects","Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::bucket/task13-20260908/*"]},{"Sid":"ReadEnhancedMonitoring","Effect":"Allow","Action":["logs:GetLogEvents"],"Resource":["arn:aws:logs:*:*:log-group:RDSOSMetrics:log-stream:*"]},{"Sid":"DescribeRDS","Effect":"Allow","Action":["rds:Describe*"],"Resource":["*"]}]},"attached_policies":["arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"]},"monitoring_role":{"exists":true,"tags":{"releem-topology-run":"task13-20260908","releem-topology-managed":"true"},"path":"/releem-topology/","trust_policy":{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"monitoring.rds.amazonaws.com"},"Action":"sts:AssumeRole"}]},"inline_policy_names":[],"attached_policies":["arn:aws:iam::aws:policy/service-role/AmazonRDSEnhancedMonitoringRole"]},"instance_profile":{"exists":true,"tags":{"releem-topology-run":"task13-20260908","releem-topology-managed":"true"},"roles":["releem-task13-20260908-runner-role"]}}
EOF
    run assert_destroy_support_ownership "$fixture" 'arn:aws:s3:::bucket/task13-20260908/*' releem-task13-20260908-runner-role releem-task13-20260908-monitoring-role
    [ "$status" -eq 0 ]

    cat >"$fixture" <<'EOF'
{"bucket":{"exists":true,"tags":{"releem-topology-run":"foreign","releem-topology-managed":"true"}},"runner_role":{"exists":true,"tags":{"releem-topology-run":"task13-20260908","releem-topology-managed":"true"},"inline_policy_name":"WrongPolicy","inline_policy":{}},"monitoring_role":{"exists":true,"tags":{"releem-topology-run":"task13-20260908","releem-topology-managed":"true"},"attached_policy":"arn:aws:iam::aws:policy/ReadOnlyAccess"},"instance_profile":{"exists":true,"tags":{"releem-topology-run":"task13-20260908","releem-topology-managed":"true"},"roles":["foreign-role"]}}
EOF

    run assert_destroy_support_ownership "$fixture" bucket/task13-20260908/* releem-task13-20260908-runner-role releem-task13-20260908-monitoring-role

    [ "$status" -ne 0 ]
}

@test "S3 ownership probing treats only an explicit 404 as absent" {
    error_file="$TEST_TMPDIR/s3-head-error"
    printf '%s\n' 'An error occurred (404) when calling the HeadBucket operation: Not Found' >"$error_file"
    run classify_s3_head_result 254 "$error_file"
    [ "$status" -eq 0 ]
    [ "$output" = absent ]

    printf '%s\n' 'An error occurred (403) when calling the HeadBucket operation: Forbidden' >"$error_file"
    run classify_s3_head_result 254 "$error_file"
    [ "$status" -ne 0 ]
}

@test "AWS absence classifiers accept only standard operation-specific CLI envelopes" {
    error_file="$TEST_TMPDIR/aws-error"
    printf '%s\n' 'An error occurred (NoSuchEntity) when calling the GetRole operation: Role not found' >"$error_file"
    run classify_iam_get_result 254 "$error_file" GetRole
    [ "$status" -eq 0 ]
    [ "$output" = absent ]

    printf '%s\n' 'proxy wrapper: cached NoSuchEntity from GetRole' >"$error_file"
    run classify_iam_get_result 254 "$error_file" GetRole
    [ "$status" -ne 0 ]

    printf '%s\n' 'An error occurred (404) when calling the HeadObject operation: Not Found' >"$error_file"
    run classify_s3_head_result 254 "$error_file" HeadObject
    [ "$status" -eq 0 ]

    printf '%s\n' 'wrapper saw 404 Not Found while proxying HeadObject' >"$error_file"
    run classify_s3_head_result 254 "$error_file" HeadObject
    [ "$status" -ne 0 ]

    printf '%s\n' 'An error occurred (NoSuchKey) when calling the HeadBucket operation: Missing' >"$error_file"
    run classify_s3_head_result 254 "$error_file" HeadBucket
    [ "$status" -ne 0 ]

    printf '%s\n' 'An error occurred (404) when calling the HeadBucket operation: Not Found' >"$error_file"
    run classify_s3_head_result 255 "$error_file" HeadBucket
    [ "$status" -ne 0 ]
}

@test "native expectation builds exact closed rows and synthesized Global upstreams for all 13 SIDs" {
    sidmap="$TEST_TMPDIR/native-13-sids.jsonl"
    current="$TEST_TMPDIR/native-13-current.jsonl"
    providers="$TEST_TMPDIR/native-13-provider.json"
    identities="$TEST_TMPDIR/native-13-identities.json"
    expected="$TEST_TMPDIR/native-13-expected.json"
    : >"$sidmap"; : >"$current"
    sid=100
    while IFS='|' read -r _ identifier; do
        sid=$((sid + 1))
        printf '{"sid":%d,"provider_id":"%s"}\n' "$sid" "$identifier" >>"$sidmap"
        if [[ "$identifier" == *-rds-replica ]]; then
            printf '{"sid":%d,"relation_type":"async_replication","group_key":"uuid-source","parent_group_key":null,"member_key":"uuid-%d","primary_member_key":"uuid-source","role":"replica","is_writer":0,"is_reader":1,"replication_state":"healthy","last_seen_epoch_ms":1710000004000}\n' "$sid" "$sid" >>"$current"
        else
            member="uuid-$sid"; [[ "$identifier" == *-rds-source ]] && member=uuid-source
            printf '{"sid":%d,"relation_type":"standalone","group_key":"%s","parent_group_key":null,"member_key":"%s","primary_member_key":null,"role":"primary","is_writer":1,"is_reader":1,"replication_state":"healthy","last_seen_epoch_ms":1710000004000}\n' "$sid" "$member" "$member" >>"$current"
        fi
    done < <(addressable_instances)
    jq -s '
      {marker_epoch_ms:1710000003500,relations:(map(
        if (.provider_id|contains("-global-west-")) then
          [{sid,relation_type:"aurora_cluster",group_key:"aurora:global-west",parent_group_key:"aurora-global:global-resource",
            member_key:("db:"+(.sid|tostring)),primary_member_key:"db:global-west-writer",
            role:(if (.provider_id|endswith("-1")) then "primary" else "replica" end),
            is_writer:(if (.provider_id|endswith("-1")) then 1 else 0 end),is_reader:1,replication_state:"healthy"},
           {sid,relation_type:"aurora_global_database",group_key:"aurora-global:global-resource",
           parent_group_key:"arn:aws:rds:us-east-1:111111111111:cluster:global-east",
           member_key:("db:"+(.sid|tostring)),primary_member_key:null,role:"replica_cluster_member",
           is_writer:0,is_reader:1,replication_state:"healthy"}]
        elif (.provider_id|contains("-global-east-")) then
          [{sid,relation_type:"aurora_cluster",group_key:"aurora:global-east",parent_group_key:"aurora-global:global-resource",
            member_key:("db:"+(.sid|tostring)),primary_member_key:"db:global-east-writer",
            role:(if (.provider_id|endswith("-1")) then "primary" else "replica" end),
            is_writer:(if (.provider_id|endswith("-1")) then 1 else 0 end),is_reader:1,replication_state:"healthy"},
           {sid,relation_type:"aurora_global_database",group_key:"aurora-global:global-resource",
           parent_group_key:null,member_key:("db:"+(.sid|tostring)),primary_member_key:null,
           role:"primary_cluster_member",is_writer:(if (.provider_id|endswith("-1")) then 1 else 0 end),
           is_reader:1,replication_state:"healthy"}]
        else
          [{sid,relation_type:"rds_multi_az",group_key:("provider:"+(.sid|tostring)),parent_group_key:null,
           member_key:("db:"+(.sid|tostring)),primary_member_key:("db:"+(.sid|tostring)),role:"primary",
           is_writer:1,is_reader:1,replication_state:"unknown"}]
        end)|flatten),upstreams:[]}' "$sidmap" >"$providers"

    run capture_native_identity_map "$current" "$sidmap" "$identities"
    [ "$status" -eq 0 ]
    cp "$providers" "$expected"
    run add_exact_native_expectations "$expected" "$sidmap" "$identities" aws-baseline
    [ "$status" -eq 0 ]
    global_channel="__releem_relation_edge__:$(printf 'aurora_global_database\0aurora-global:global-resource' | sha256sum | awk '{print $1}')"
    run jq -e --arg channel "$global_channel" '
      ([.relations[]|select(.relation_type=="standalone" or .relation_type=="async_replication")]|length)==13 and
      (.upstreams|length)==3 and
      ([.upstreams[]|select(.channel_key=="default" and .upstream_member_key=="uuid-source")]|length)==1 and
      ([.upstreams[]|select(.channel_key==$channel and
        .upstream_member_key=="arn:aws:rds:us-east-1:111111111111:cluster:global-east" and
        .replication_state=="healthy")]|map(.sid)|unique|length)==2' "$expected"
    [ "$status" -eq 0 ]

    jq '
      .relations |= map(
        if .relation_type!="aurora_global_database" then .
        elif (.member_key|startswith("db:107") or startswith("db:108")) then
          .role="replica_cluster_member" |
          .parent_group_key="arn:aws:rds:us-west-2:111111111111:cluster:global-west" |
          .is_writer=0
        else
          .role="primary_cluster_member" | .parent_group_key=null |
          .is_writer=(if (.member_key|endswith("109")) then 1 else 0 end)
        end)' "$providers" >"$expected"
    run add_exact_native_expectations "$expected" "$sidmap" "$identities" aurora-global-transition
    [ "$status" -eq 0 ]
    run jq -e --arg channel "$global_channel" '
      ([.upstreams[]|select(.channel_key==$channel and
        .upstream_member_key=="arn:aws:rds:us-west-2:111111111111:cluster:global-west")]|map(.sid)|sort)==[107,108]' "$expected"
    [ "$status" -eq 0 ]
}

@test "closed topology rejects Aurora async rows wrong native fields and extra upstreams" {
    expected="$TEST_TMPDIR/closed-expected.json"
    current="$TEST_TMPDIR/closed-current.jsonl"
    upstreams="$TEST_TMPDIR/closed-upstreams.jsonl"
    cat >"$expected" <<'EOF'
{"marker_epoch_ms":1710000003500,"relations":[{"sid":101,"relation_type":"aurora_cluster","group_key":"aurora:c","parent_group_key":null,"member_key":"db:a","primary_member_key":"db:a","role":"primary","is_writer":1,"is_reader":1,"replication_state":"healthy"},{"sid":101,"relation_type":"standalone","group_key":"uuid-a","parent_group_key":null,"member_key":"uuid-a","primary_member_key":null,"role":"primary","is_writer":1,"is_reader":1,"replication_state":"healthy"}],"upstreams":[]}
EOF
    jq -c '.relations[] + {last_seen_epoch_ms:1710000004000}' "$expected" >"$current"
    : >"$upstreams"
    run assert_exact_sid_contract "$current" "$upstreams" "$expected"
    [ "$status" -eq 0 ]

    jq -c 'select(.relation_type!="standalone"),select(.relation_type=="standalone")|if .relation_type=="standalone" then .relation_type="async_replication" else . end' "$current" >"$current.bad"
    run assert_exact_sid_contract "$current.bad" "$upstreams" "$expected"
    [ "$status" -ne 0 ]

    jq -c 'if .relation_type=="standalone" then .group_key="wrong"|.is_writer=0|.is_reader=0|.replication_state="stopped" else . end' "$current" >"$current.bad"
    run assert_exact_sid_contract "$current.bad" "$upstreams" "$expected"
    [ "$status" -ne 0 ]

    printf '%s\n' '{"sid":101,"channel_key":"unexpected","upstream_member_key":"wrong-global","replication_state":"healthy","last_seen_epoch_ms":1710000004000}' >"$upstreams"
    run assert_exact_sid_contract "$current" "$upstreams" "$expected"
    [ "$status" -ne 0 ]
}

@test "ClickHouse closed snapshot rejects an extra native relation" {
    current="$TEST_TMPDIR/ch-closed-current.jsonl"
    observations="$TEST_TMPDIR/ch-closed-observations.jsonl"
    printf '%s\n' '{"sid":101,"last_rid":"rid-a","relation_type":"standalone","group_key":"uuid-a","parent_group_key":null,"member_key":"uuid-a","primary_member_key":null,"role":"primary","is_writer":1,"is_reader":1,"replication_state":"healthy"}' >"$current"
    printf '%s\n' '{"sid":101,"rid":"rid-a","observed_epoch_ms":1710000004000,"relations":"[{\"relation_type\":\"standalone\",\"group_key\":\"uuid-a\",\"parent_group_key\":null,\"member_key\":\"uuid-a\",\"primary_member_key\":null,\"role\":\"primary\",\"is_writer\":1,\"is_reader\":1,\"replication_state\":\"healthy\"},{\"relation_type\":\"async_replication\",\"group_key\":\"wrong\",\"parent_group_key\":null,\"member_key\":\"uuid-a\",\"primary_member_key\":\"wrong\",\"role\":\"replica\",\"is_writer\":0,\"is_reader\":1,\"replication_state\":\"healthy\"}]"}' >"$observations"
    run assert_selected_observations "$observations" "$current" 101:rid-a 1710000003500 1
    [ "$status" -ne 0 ]
}

@test "instance tracking precedes readiness failure and incomplete tracking blocks terminal success" {
    initialize_monitoring_tracking
    MONITORING_ROLE_ARN=arn:aws:iam::111111111111:role/releem-monitoring
    instance_exists() { return 1; }
    aws_region() {
        case "$*" in
            *create-db-instance*) return 0 ;;
            *describe-db-instances*) printf '%s\n' db-TRACKED1 ;;
            *) return 1 ;;
        esac
    }
    wait_instance_available() { return 1; }

    run ensure_cluster_instance us-east-1 releem-task13-20260908-aurora-provisioned-1 cluster db.t3.small pg
    [ "$status" -ne 0 ]
    run awk -F '\t' '$2=="releem-task13-20260908-aurora-provisioned-1" && $3=="db-TRACKED1"{found=1} END{exit !found}' "$(monitoring_tracking_file)"
    [ "$status" -eq 0 ]

    aws_region() { return 1; }
    run recover_monitoring_resource_ids
    [ "$status" -ne 0 ]
    run monitoring_streams_absent
    [ "$status" -ne 0 ]
}

@test "cleanup preserves an untracked DB until a later destroy can recover its monitoring ID" {
    initialize_monitoring_tracking
    calls="$TEST_TMPDIR/monitoring-recovery-calls"
    describe_ok=false
    aws_region() {
        case "$*" in
            *describe-db-instances*)
                "$describe_ok" || return 1
                printf '%s\n' db-RECOVERED1
                ;;
            *describe-log-streams*) printf '0\n' ;;
            *) return 1 ;;
        esac
    }
    cleanup_aws_region() {
        case "$*" in
            *delete-db-instance*) printf 'delete:%s\n' "$*" >>"$calls" ;;
            *) return 1 ;;
        esac
    }

    run delete_db_instance_if_monitoring_tracked us-east-1 releem-task13-20260908-aurora-provisioned-1
    [ "$status" -ne 0 ]
    [ ! -e "$calls" ]

    describe_ok=true
    run delete_db_instance_if_monitoring_tracked us-east-1 releem-task13-20260908-aurora-provisioned-1
    [ "$status" -eq 0 ]
    run awk -F '\t' '$2=="releem-task13-20260908-aurora-provisioned-1" && $3=="db-RECOVERED1"{found=1} END{exit !found}' "$(monitoring_tracking_file)"
    [ "$status" -eq 0 ]
    run grep -c '^delete:' "$calls"
    [ "$status" -eq 0 ]
    [ "$output" -eq 1 ]

    awk -F '\t' -v OFS='\t' '{$3=($3=="" ? "db-FINAL" NR : $3); print}' \
        "$(monitoring_tracking_file)" >"$(monitoring_tracking_file).new"
    mv "$(monitoring_tracking_file).new" "$(monitoring_tracking_file)"
    run delete_monitoring_stream_until_absent us-east-1 db-RECOVERED1
    [ "$status" -eq 0 ]
    run monitoring_streams_absent
    [ "$status" -eq 0 ]
}

@test "create response records monitoring resource ID atomically" {
    initialize_monitoring_tracking
    response="$TEST_TMPDIR/create-instance.json"
    printf '%s\n' '{"DBInstance":{"DBInstanceIdentifier":"releem-task13-20260908-rds-source","DbiResourceId":"db-CREATE1"}}' >"$response"

    run capture_monitoring_resource_id_from_response us-east-1 releem-task13-20260908-rds-source "$response"

    [ "$status" -eq 0 ]
    run awk -F '\t' '$2=="releem-task13-20260908-rds-source" && $3=="db-CREATE1"{found=1} END{exit !found}' "$(monitoring_tracking_file)"
    [ "$status" -eq 0 ]
}

@test "terminal monitoring tracking rejects duplicate resource IDs" {
    initialize_monitoring_tracking
    awk -F '\t' -v OFS='\t' '{$3="db-DUPLICATE"; print}' "$(monitoring_tracking_file)" >"$(monitoring_tracking_file).new"
    mv "$(monitoring_tracking_file).new" "$(monitoring_tracking_file)"

    run monitoring_tracking_complete

    [ "$status" -ne 0 ]
}

@test "monitoring stream cleanup is idempotent and fails closed on ambiguous reads" {
    calls="$TEST_TMPDIR/log-calls"
    aws_region() {
        printf '%s\n' "$*" >>"$calls"
        case "$*" in
            *describe-log-streams*) printf '0\n' ;;
            *) return 1 ;;
        esac
    }

    run delete_monitoring_stream_until_absent us-east-1 db-TRACKED1
    [ "$status" -eq 0 ]
    run grep -c delete-log-stream "$calls"
    [ "$status" -ne 0 ]

    aws_region() { return 255; }
    run delete_monitoring_stream_until_absent us-east-1 db-TRACKED1
    [ "$status" -ne 0 ]
}

@test "cleanup budget is shared across retries and exhaustion still runs later phases" {
    marker="$TEST_TMPDIR/cleanup-budget"
    CLEANUP_ARMED=true
    CLEANUP_DEADLINE_EPOCH=$(( $(date +%s) - 1 ))
    expired_epoch="$CLEANUP_DEADLINE_EPOCH"
    declare -F cleanup_wait_until >/dev/null
    cleanup_resources_impl() {
        failed=0
        cleanup_wait_until "expired waiter" false || failed=1
        printf 'delete-issued\n' >>"$marker"
        return "$failed"
    }

    cleanup_once || first_status=$?
    [ "${first_status:-0}" -ne 0 ]
    [ "$(<"$marker")" = delete-issued ]
    [ "$CLEANUP_DONE" = false ]
    [ "$CLEANUP_DEADLINE_EPOCH" -eq "$expired_epoch" ]

    cleanup_once || second_status=$?
    [ "${second_status:-0}" -ne 0 ]
    [ "$(wc -l <"$marker")" -eq 2 ]
    [ "$CLEANUP_DEADLINE_EPOCH" -eq "$expired_epoch" ]
}

@test "cleanup deadline must leave bounded deletion reserve" {
    run validate_cleanup_deadline 10
    [ "$status" -ne 0 ]
    run validate_cleanup_deadline 3600
    [ "$status" -eq 0 ]
}

@test "terminal cleanup polling never sleeps beyond its remaining deadline" {
    run cleanup_poll_seconds 20 7
    [ "$status" -eq 0 ]
    [ "$output" = 7 ]

    run cleanup_poll_seconds 20 25
    [ "$status" -eq 0 ]
    [ "$output" = 20 ]
}

@test "Global convergence allows omitted writer sync but rejects disconnected secondary" {
    good="$TEST_TMPDIR/global-optional-good.json"
    bad="$TEST_TMPDIR/global-optional-bad.json"
    printf '%s\n' '{"Status":"available","GlobalClusterMembers":[{"DBClusterArn":"arn:old","IsWriter":false,"SynchronizationStatus":"connected"},{"DBClusterArn":"arn:new","IsWriter":true}]}' >"$good"
    printf '%s\n' '{"Status":"available","GlobalClusterMembers":[{"DBClusterArn":"arn:old","IsWriter":false,"SynchronizationStatus":"pending-resync"},{"DBClusterArn":"arn:new","IsWriter":true}]}' >"$bad"
    run assert_global_switchover_converged "$good" arn:new arn:old
    [ "$status" -eq 0 ]
    run assert_global_switchover_converged "$bad" arn:new arn:old
    [ "$status" -ne 0 ]
}

@test "destroy rejects an untagged deterministic database name" {
    collisions="$TEST_TMPDIR/destroy-collisions.json"
    owned="$TEST_TMPDIR/destroy-owned.json"
    printf '%s\n' '{"expected":[],"existing":["releem-task13-20260908-rds-source"]}' >"$collisions"
    printf '%s\n' '{"instances":["arn:aws:rds:us-east-1:111111111111:db:releem-task13-20260908-rds-source"],"clusters":[],"global_clusters":[],"parameter_groups":[],"security_groups":[],"subnet_groups":[],"snapshots":[],"runners":[],"enis":[],"ssm_artifacts":[],"monitoring_streams":[],"s3_objects":[],"s3_buckets":[],"instance_profiles":[],"iam_policies":[],"iam_roles":[]}' >"$owned"
    run assert_deterministic_resources_owned "$collisions" "$owned"
    [ "$status" -eq 0 ]

    printf '%s\n' '{"instances":[],"clusters":[],"global_clusters":[],"parameter_groups":[],"security_groups":[],"subnet_groups":[],"snapshots":[],"runners":[],"enis":[],"ssm_artifacts":[],"monitoring_streams":[],"s3_objects":[],"s3_buckets":[],"instance_profiles":[],"iam_policies":[],"iam_roles":[]}' >"$owned"

    run assert_deterministic_resources_owned "$collisions" "$owned"

    [ "$status" -ne 0 ]
}

@test "cleanup plan is dependency ordered and includes secret last" {
    inventory="$TEST_TMPDIR/inventory.json"
    cat >"$inventory" <<'EOF'
{"instances":["i1"],"clusters":["c1"],"global_clusters":["g1"],"parameter_groups":["p1"],"runners":["runner1"],"enis":["eni1"],"security_groups":["sg1"],"subnet_groups":["sub1"],"ssm_artifacts":["cmd1"],"monitoring_streams":["stream1"],"s3_objects":["obj1"],"s3_buckets":["bucket1"],"instance_profiles":["profile1"],"iam_policies":["policy1"],"iam_roles":["role1"],"snapshots":["snap1"]}
EOF

    run cleanup_plan "$inventory"

    [ "$status" -eq 0 ]
    [ "$output" = $'ssm-artifact|cmd1\nrunner|runner1\neni|eni1\ninstance|i1\nmonitoring-stream|stream1\nsnapshot|snap1\ncluster|c1\nglobal-cluster|g1\nparameter-group|p1\ns3-object|obj1\ns3-bucket|bucket1\ninstance-profile|profile1\niam-policy|policy1\niam-role|role1\nsecurity-group|sg1\nsubnet-group|sub1\nsecret-file|runtime' ]
}

@test "post-cleanup assertion rejects any run-owned resource" {
    inventory="$TEST_TMPDIR/post.json"
    cat >"$inventory" <<'EOF'
{"instances":[],"clusters":[],"global_clusters":[],"parameter_groups":[],"runners":[],"enis":[],"security_groups":["sg-left"],"subnet_groups":[],"ssm_artifacts":[],"monitoring_streams":[],"s3_objects":[],"s3_buckets":[],"instance_profiles":[],"iam_policies":[],"iam_roles":[],"snapshots":[]}
EOF

    run assert_inventory_empty "$inventory"

    [ "$status" -ne 0 ]
}

@test "post-cleanup inventory must equal the preflight baseline" {
    before="$TEST_TMPDIR/before.json"
    after="$TEST_TMPDIR/after.json"
    printf '%s\n' '{"instances":[],"clusters":[],"security_groups":[]}' >"$before"
    printf '%s\n' '{"security_groups":[],"clusters":[],"instances":[]}' >"$after"

    run assert_inventory_matches "$before" "$after"
    [ "$status" -eq 0 ]

    printf '%s\n' '{"instances":["left"],"clusters":[],"security_groups":[]}' >"$after"
    run assert_inventory_matches "$before" "$after"
    [ "$status" -ne 0 ]
}

@test "terminal absence fails closed when AWS inventory reads fail" {
    mkdir -p "$(state_dir)"
    printf '%s\t%s\n' us-east-1 db-RESOURCE >"$(state_dir)/monitoring-streams.tsv"
    aws_region() { return 1; }

    run monitoring_streams_absent
    [ "$status" -ne 0 ]

    run runner_enis_absent us-east-1
    [ "$status" -ne 0 ]
}

@test "runner launch specification is private SSM managed and root encrypted" {
    fixture="$TEST_TMPDIR/runner.json"
    cat >"$fixture" <<'EOF'
{"ImageId":"ami-1","InstanceType":"t3.micro","IamInstanceProfile":{"Name":"profile"},"NetworkInterfaces":[{"DeviceIndex":0,"AssociatePublicIpAddress":false,"Groups":["sg-runner"],"SubnetId":"subnet-private"}],"BlockDeviceMappings":[{"DeviceName":"/dev/xvda","Ebs":{"Encrypted":true,"VolumeType":"gp3","DeleteOnTermination":true}}],"MetadataOptions":{"HttpTokens":"required","HttpEndpoint":"enabled"}}
EOF

    run assert_runner_launch_spec "$fixture"

    [ "$status" -eq 0 ]
}

@test "runner security group rejects inbound permissions" {
    fixture="$TEST_TMPDIR/runner-sg.json"
    cat >"$fixture" <<'EOF'
{"SecurityGroups":[{"GroupId":"sg-runner","IpPermissions":[{"IpProtocol":"tcp","FromPort":22,"ToPort":22,"IpRanges":[{"CidrIp":"0.0.0.0/0"}]}],"IpPermissionsEgress":[{"IpProtocol":"-1","IpRanges":[{"CidrIp":"0.0.0.0/0"}]}]}]}
EOF

    run assert_runner_security_group "$fixture"

    [ "$status" -ne 0 ]
}

@test "database security group allows MySQL only from the runner group" {
    good="$TEST_TMPDIR/db-sg-good.json"
    bad="$TEST_TMPDIR/db-sg-bad.json"
    cat >"$good" <<'EOF'
{"SecurityGroups":[{"IpPermissions":[{"IpProtocol":"tcp","FromPort":3306,"ToPort":3306,"UserIdGroupPairs":[{"GroupId":"sg-runner"}],"IpRanges":[],"Ipv6Ranges":[],"PrefixListIds":[]}],"IpPermissionsEgress":[{"IpProtocol":"-1"}]}]}
EOF
    jq '.SecurityGroups[0].IpPermissions[0].IpRanges=[{"CidrIp":"0.0.0.0/0"}]' "$good" >"$bad"

    run assert_database_security_group "$good" sg-runner
    [ "$status" -eq 0 ]

    run assert_database_security_group "$bad" sg-runner
    [ "$status" -ne 0 ]
}

@test "runner policy has only object read logs events and RDS describe access" {
    fixture="$TEST_TMPDIR/runner-policy.json"
    runner_policy_document 'arn:aws:s3:::bucket/run/*' >"$fixture"

    run assert_runner_policy "$fixture"

    [ "$status" -eq 0 ]
    [[ "$(<"$fixture")" != *'"s3:*"'* ]]
    [[ "$(<"$fixture")" != *'"logs:*"'* ]]
}

@test "SSM command document references private S3 paths and no credentials" {
    run runner_ssm_commands us-east-1 bucket prefix/east

    [ "$status" -eq 0 ]
    [[ "$output" == *'s3://bucket/prefix/east'* ]]
    [[ "$output" != *'RELEEM_API_KEY'* ]]
    [[ "$output" != *'mysql_password'* ]]
    [[ "$output" != *'DB_PASSWORD'* ]]
}

@test "all DB instance create arguments enable Enhanced Monitoring" {
    run monitoring_arguments 'arn:aws:iam::111111111111:role/releem-monitoring'

    [ "$status" -eq 0 ]
    [ "$output" = $'--monitoring-interval\n1\n--monitoring-role-arn\narn:aws:iam::111111111111:role/releem-monitoring' ]
}

@test "same-region read replica omits unsupported parameter group option and validates inheritance" {
    fixture="$TEST_TMPDIR/read-replica.json"
    export MYSQL_INSTANCE_CLASS=db.t3.micro
    export MONITORING_ROLE_ARN=arn:aws:iam::111111111111:role/releem-monitoring
    printf '%s\n' '{"DBInstances":[{"DBInstanceIdentifier":"replica","DBParameterGroups":[{"DBParameterGroupName":"expected","ParameterApplyStatus":"in-sync"}]}]}' >"$fixture"

    run read_replica_create_arguments replica source subnet sg
    [ "$status" -eq 0 ]
    [[ "$output" != *'--db-parameter-group-name'* ]]

    run assert_read_replica_parameter_group "$fixture" expected
    [ "$status" -eq 0 ]
}

@test "exact SID contract rejects a replica source mismatch that broad counts accept" {
    current="$TEST_TMPDIR/current-exact.jsonl"
    upstreams="$TEST_TMPDIR/upstreams-exact.jsonl"
    expected="$TEST_TMPDIR/expected-exact.json"
    cat >"$current" <<'EOF'
{"sid":101,"relation_type":"rds_read_replica","group_key":"rds:source-a","parent_group_key":null,"member_key":"db:source-a","primary_member_key":"db:source-a","role":"primary","is_writer":1,"is_reader":1,"replication_state":"healthy","last_seen_epoch_ms":1710000004000}
{"sid":202,"relation_type":"rds_read_replica","group_key":"rds:source-a","parent_group_key":"rds:source-a","member_key":"db:replica-a","primary_member_key":"db:source-a","role":"replica","is_writer":0,"is_reader":1,"replication_state":"healthy","last_seen_epoch_ms":1710000004000}
{"sid":202,"relation_type":"async_replication","group_key":"mysql:source-a","parent_group_key":null,"member_key":"mysql:replica-a","primary_member_key":"mysql:source-a","role":"replica","is_writer":0,"is_reader":1,"replication_state":"healthy","last_seen_epoch_ms":1710000004000}
EOF
    printf '%s\n' '{"sid":202,"channel_key":"default","upstream_member_key":"mysql:source-a","replication_state":"healthy","last_seen_epoch_ms":1710000004000}' >"$upstreams"
    cat >"$expected" <<'EOF'
{"marker_epoch_ms":1710000003500,"relations":[{"sid":101,"relation_type":"rds_read_replica","group_key":"rds:source-a","parent_group_key":null,"member_key":"db:source-a","primary_member_key":"db:source-a","role":"primary","is_writer":1,"is_reader":1,"replication_state":"healthy"},{"sid":202,"relation_type":"rds_read_replica","group_key":"rds:source-a","parent_group_key":"rds:source-a","member_key":"db:replica-a","primary_member_key":"db:source-a","role":"replica","is_writer":0,"is_reader":1,"replication_state":"healthy"},{"sid":202,"relation_type":"async_replication","group_key":"mysql:source-a","parent_group_key":null,"member_key":"mysql:replica-a","primary_member_key":"mysql:source-a","role":"replica","is_writer":0,"is_reader":1,"replication_state":"healthy"}],"upstreams":[{"sid":202,"channel_key":"default","upstream_member_key":"mysql:source-a","replication_state":"healthy"}]}
EOF

    run assert_exact_sid_contract "$current" "$upstreams" "$expected"
    [ "$status" -eq 0 ]

    printf '%s\n' '{"sid":202,"channel_key":"default","upstream_member_key":"mysql:wrong-source","replication_state":"healthy","last_seen_epoch_ms":1710000004000}' >"$upstreams"
    run assert_exact_sid_contract "$current" "$upstreams" "$expected"
    [ "$status" -ne 0 ]
}

@test "exact SID contract rejects a wrong global parent that broad role counts accept" {
    current="$TEST_TMPDIR/global-current.jsonl"
    upstreams="$TEST_TMPDIR/global-upstreams.jsonl"
    expected="$TEST_TMPDIR/global-expected.json"
    : >"$upstreams"
    cat >"$current" <<'EOF'
{"sid":301,"relation_type":"aurora_global_database","group_key":"aurora-global:g1","parent_group_key":null,"member_key":"arn:east:writer","primary_member_key":null,"role":"primary_cluster_member","last_seen_epoch_ms":1710000004000}
{"sid":302,"relation_type":"aurora_global_database","group_key":"aurora-global:g1","parent_group_key":"arn:west:wrong","member_key":"arn:west:reader","primary_member_key":null,"role":"replica_cluster_member","last_seen_epoch_ms":1710000004000}
EOF
    cat >"$expected" <<'EOF'
{"marker_epoch_ms":1710000003500,"relations":[{"sid":301,"relation_type":"aurora_global_database","group_key":"aurora-global:g1","parent_group_key":null,"member_key":"arn:east:writer","role":"primary_cluster_member"},{"sid":302,"relation_type":"aurora_global_database","group_key":"aurora-global:g1","parent_group_key":"arn:east:cluster","member_key":"arn:west:reader","role":"replica_cluster_member"}],"upstreams":[]}
EOF

    run assert_exact_sid_contract "$current" "$upstreams" "$expected"

    [ "$status" -ne 0 ]
}

@test "AWS identity mapping derives exact Aurora and global keys from resource IDs" {
    addressable_instances() { printf '%s\n' 'us-east-1|global-east-1'; }
    aws_region() {
        case "$*" in
            *'describe-db-instances'*) printf '%s\n' '{"DBInstanceIdentifier":"global-east-1","DBInstanceArn":"arn:aws:rds:us-east-1:111111111111:db:global-east-1","DbiResourceId":"db-INSTANCE1","DBClusterIdentifier":"global-east","Endpoint":{"Address":"global-east-1.example","Port":3306},"ReadReplicaDBInstanceIdentifiers":[],"MultiAZ":false}' ;;
            *'describe-db-clusters'*) printf '%s\n' '{"DBClusterIdentifier":"global-east","DBClusterArn":"arn:aws:rds:us-east-1:111111111111:cluster:global-east","DbClusterResourceId":"cluster-REGIONAL1","GlobalClusterIdentifier":"global-one","DBClusterMembers":[{"DBInstanceIdentifier":"global-east-1","IsClusterWriter":true}]}' ;;
            *'describe-global-clusters'*) printf '%s\n' '{"GlobalClusterIdentifier":"global-one","GlobalClusterResourceId":"cluster-GLOBAL1","GlobalClusterMembers":[{"DBClusterArn":"arn:aws:rds:us-east-1:111111111111:cluster:global-east","IsWriter":true}]}' ;;
            *) return 1 ;;
        esac
    }
    fixture="$TEST_TMPDIR/aws-identity.jsonl"

    run capture_aws_identity_map "$fixture"

    [ "$status" -eq 0 ]
    run jq -e '.endpoint=="global-east-1.example" and (.relations|length)==2 and .relations[0].group_key=="aurora:cluster-REGIONAL1" and .relations[0].primary_member_key=="arn:aws:rds:us-east-1:111111111111:db:global-east-1" and .relations[1].group_key=="aurora-global:cluster-GLOBAL1" and .relations[1].parent_group_key==null' "$fixture"
    [ "$status" -eq 0 ]
}

@test "ClickHouse snapshot comparison includes primary parent and source edge identity" {
    current="$TEST_TMPDIR/current-edge.jsonl"
    observations="$TEST_TMPDIR/observations-edge.jsonl"
    printf '%s\n' '{"sid":101,"last_rid":"rid-a","relation_type":"rds_read_replica","group_key":"rds:g","parent_group_key":"rds:g","member_key":"db:replica","primary_member_key":"db:source","upstream_member_key":"mysql:source","role":"replica","is_writer":0,"replication_state":"healthy"}' >"$current"
    printf '%s\n' '{"sid":101,"rid":"rid-a","observed_epoch_ms":1710000003123,"relations":"[{\"relation_type\":\"rds_read_replica\",\"group_key\":\"rds:g\",\"parent_group_key\":\"rds:wrong\",\"member_key\":\"db:replica\",\"primary_member_key\":\"db:wrong\",\"upstream_member_key\":\"mysql:wrong\",\"role\":\"replica\",\"is_writer\":0,\"replication_state\":\"healthy\"}]"}' >"$observations"

    run assert_selected_observations "$observations" "$current" '101:rid-a' 1710000003123 1

    [ "$status" -ne 0 ]
}

@test "credential transport has one-day expiry and immediate bootstrap deletion" {
    lifecycle="$TEST_TMPDIR/lifecycle.json"
    bucket_lifecycle_configuration >"$lifecycle"

    run jq -e '(.Rules|length)==1 and .Rules[0].Status=="Enabled" and .Rules[0].Filter.Prefix=="task13-20260908/" and .Rules[0].Expiration.Days==1' "$lifecycle"
    [ "$status" -eq 0 ]

    run bootstrap_secret_object_keys
    [ "$status" -eq 0 ]
    [ "$output" = $'task13-20260908/east/configs.tar\ntask13-20260908/west/configs.tar' ]
}

@test "Serverless scale evidence requires capacity movement under bounded load" {
    good="$TEST_TMPDIR/serverless-good.json"
    bad="$TEST_TMPDIR/serverless-bad.json"
    printf '%s\n' '{"before":{"capacity":0.5,"acu_utilization":12},"during":{"capacity":1.5,"acu_utilization":76},"after":{"capacity":0.5,"acu_utilization":18},"attempts":2}' >"$good"
    printf '%s\n' '{"before":{"capacity":0.5,"acu_utilization":12},"during":{"capacity":0.5,"acu_utilization":76},"after":{"capacity":0.5,"acu_utilization":18},"attempts":3}' >"$bad"

    run assert_serverless_scale_evidence "$good" 3
    [ "$status" -eq 0 ]
    run assert_serverless_scale_evidence "$bad" 3
    [ "$status" -ne 0 ]
}

@test "preflight rejects deterministic collisions regardless of ownership tags" {
    fixture="$TEST_TMPDIR/collisions.json"
    printf '%s\n' '{"expected":["db/releem-task13-20260908-rds-source","role/releem-task13-20260908-runner-role"],"existing":[]}' >"$fixture"
    run assert_no_deterministic_collisions "$fixture"
    [ "$status" -eq 0 ]

    printf '%s\n' '{"expected":["db/releem-task13-20260908-rds-source","role/releem-task13-20260908-runner-role"],"existing":["db/releem-task13-20260908-rds-source"]}' >"$fixture"

    run assert_no_deterministic_collisions "$fixture"

    [ "$status" -ne 0 ]
}

@test "preflight requires private subnet NAT egress" {
    good="$TEST_TMPDIR/egress-good.json"
    bad="$TEST_TMPDIR/egress-bad.json"
    printf '%s\n' '{"subnets":[{"subnet_id":"subnet-a","routes":[{"destination":"0.0.0.0/0","nat_gateway_id":"nat-1","state":"active"}]},{"subnet_id":"subnet-b","routes":[{"destination":"0.0.0.0/0","nat_gateway_id":"nat-1","state":"active"}]}]}' >"$good"
    printf '%s\n' '{"subnets":[{"subnet_id":"subnet-a","routes":[]},{"subnet_id":"subnet-b","routes":[{"destination":"0.0.0.0/0","gateway_id":"igw-1","state":"active"}]}]}' >"$bad"

    run assert_private_subnet_egress "$good" 2
    [ "$status" -eq 0 ]
    run assert_private_subnet_egress "$bad" 2
    [ "$status" -ne 0 ]
}

@test "preflight quota gate accounts for exact regional matrix headroom" {
    fixture="$TEST_TMPDIR/quotas.json"
    printf '%s\n' '{"us-east-1":{"db_instances":{"quota":40,"used":28,"required":11},"db_clusters":{"quota":40,"used":37,"required":3},"ec2_instances":{"quota":100,"used":99,"required":1}},"us-west-2":{"db_instances":{"quota":40,"used":38,"required":2},"db_clusters":{"quota":40,"used":39,"required":1},"ec2_instances":{"quota":100,"used":99,"required":1}}}' >"$fixture"
    run assert_matrix_quota_headroom "$fixture"
    [ "$status" -eq 0 ]

    printf '%s\n' '{"us-east-1":{"db_instances":{"quota":40,"used":30,"required":11},"db_clusters":{"quota":40,"used":37,"required":3},"ec2_instances":{"quota":100,"used":99,"required":1}},"us-west-2":{"db_instances":{"quota":40,"used":38,"required":2},"db_clusters":{"quota":40,"used":39,"required":1},"ec2_instances":{"quota":100,"used":99,"required":1}}}' >"$fixture"

    run assert_matrix_quota_headroom "$fixture"

    [ "$status" -ne 0 ]
}

@test "global runtime deadline is bounded and validated" {
    run validate_runtime_deadline 14400
    [ "$status" -eq 0 ]
    run validate_runtime_deadline 0
    [ "$status" -ne 0 ]
    run validate_runtime_deadline 999999
    [ "$status" -ne 0 ]
}

@test "preflight cost shape records selected compute storage monitoring and runtime ceilings" {
    summary="$TEST_TMPDIR/summary.json"
    write_preflight_summary "$summary" 8.0.mysql_aurora.3.10.0 db.t4g.medium 0.5 8.0.43 db.t3.micro

    run jq -e '
      .serverless_v2=={instances:3,min_acu:0.5,configured_max_acu:1.5,transition_max_acu:2.5,load_attempts_max:3} and
      .ordinary_mysql.instances==3 and .ordinary_mysql.engine_version=="8.0.43" and
      .ordinary_mysql.instance_class=="db.t3.micro" and .ordinary_mysql.allocated_storage_gib_each==20 and
      .ordinary_mysql.allocated_storage_gib_total==60 and .runners.encrypted_root_gib_total==16 and
      .enhanced_monitoring_interval_seconds==1 and .runtime_deadline_seconds==14400 and
      .cleanup_deadline_seconds==3600 and .cleanup_delete_reserve_seconds==600 and .total_advertised_max_seconds==18000
    ' "$summary"

    [ "$status" -eq 0 ]
}

@test "Enhanced Monitoring readiness requires stream events for exact resource ID" {
    fixture="$TEST_TMPDIR/events.json"
    cat >"$fixture" <<'EOF'
{"events":[{"timestamp":1710000003000,"message":"{\"instanceID\":\"db-1\"}"}]}
EOF

    run assert_monitoring_events "$fixture" db-ABCDEFGHIJKLMNOP

    [ "$status" -eq 0 ]
}

@test "DB safety assertion requires private encrypted monitored instances" {
    good="$TEST_TMPDIR/db-instances-good.json"
    bad="$TEST_TMPDIR/db-instances-bad.json"
    cat >"$good" <<'EOF'
{"DBInstances":[{"DBInstanceIdentifier":"db-1","PubliclyAccessible":false,"StorageEncrypted":true,"MonitoringInterval":1,"MonitoringRoleArn":"arn:aws:iam::111111111111:role/monitor"},{"DBInstanceIdentifier":"db-2","PubliclyAccessible":false,"StorageEncrypted":true,"MonitoringInterval":1,"MonitoringRoleArn":"arn:aws:iam::111111111111:role/monitor"}]}
EOF
    jq '.DBInstances[1].PubliclyAccessible=true' "$good" >"$bad"

    run assert_db_instance_safety "$good" 2 'arn:aws:iam::111111111111:role/monitor'
    [ "$status" -eq 0 ]

    run assert_db_instance_safety "$bad" 2 'arn:aws:iam::111111111111:role/monitor'
    [ "$status" -ne 0 ]
}

@test "current-state query is tenant SID and exact marker scoped without hostname inference" {
    export TOPOLOGY_UID=42

    run current_state_query '101,202' 1710000003123

    [ "$status" -eq 0 ]
    [[ "$output" == *'s.uid = 42'* ]]
    [[ "$output" == *'r.sid IN (101,202)'* ]]
    [[ "$output" == *'r.last_seen_epoch_ms >= 1710000003123'* ]]
    [[ "$output" != *'hostname'* ]]
}

@test "ClickHouse query correlates exact SID RID pairs at millisecond marker precision" {
    export TOPOLOGY_UID=42

    run build_clickhouse_observation_query 1710000003123 '101:rid-a,202:rid-b'

    [ "$status" -eq 0 ]
    [[ "$output" == *'uid = 42'* ]]
    [[ "$output" == *"(sid = 101 AND rid = 'rid-a')"* ]]
    [[ "$output" == *'toDateTime64(1710000003123 / 1000.0, 3)'* ]]
    [[ "$output" != *'LIMIT 1 BY'* ]]
}

@test "ClickHouse assertion matches exact current SID RID relation snapshots" {
    current="$TEST_TMPDIR/current.jsonl"
    observations="$TEST_TMPDIR/observations.jsonl"
    cat >"$current" <<'EOF'
{"sid":101,"last_rid":"rid-a","relation_type":"aurora_cluster","group_key":"aurora:c1","parent_group_key":null,"member_key":"db:i1","role":"primary","is_writer":1,"replication_state":"healthy"}
{"sid":202,"last_rid":"rid-b","relation_type":"rds_multi_az","group_key":"rds:m1","parent_group_key":null,"member_key":"db:i2","role":"primary","is_writer":1,"replication_state":"unknown"}
EOF
    cat >"$observations" <<'EOF'
{"sid":101,"rid":"rid-a","observed_epoch_ms":1710000003123,"relations":"[{\"relation_type\":\"aurora_cluster\",\"group_key\":\"aurora:c1\",\"parent_group_key\":null,\"member_key\":\"db:i1\",\"role\":\"primary\",\"is_writer\":1,\"replication_state\":\"healthy\"}]"}
{"sid":202,"rid":"rid-b","observed_epoch_ms":1710000003124,"relations":"[{\"relation_type\":\"rds_multi_az\",\"group_key\":\"rds:m1\",\"parent_group_key\":null,\"member_key\":\"db:i2\",\"role\":\"primary\",\"is_writer\":1,\"replication_state\":\"unknown\"}]"}
EOF

    run assert_selected_observations "$observations" "$current" '101:rid-a,202:rid-b' 1710000003123 2

    [ "$status" -eq 0 ]
}

@test "Aurora failover assertion requires changed writer and native relation" {
    before="$TEST_TMPDIR/before.jsonl"
    after="$TEST_TMPDIR/after.jsonl"
    cat >"$before" <<'EOF'
{"sid":101,"relation_type":"aurora_cluster","group_key":"aurora:c1","member_key":"db:i1","role":"primary","is_writer":1,"replication_state":"healthy","last_seen_epoch_ms":1710000003000}
EOF
    cat >"$after" <<'EOF'
{"sid":101,"relation_type":"aurora_cluster","group_key":"aurora:c1","member_key":"db:i1","role":"replica","is_writer":0,"replication_state":"healthy","last_seen_epoch_ms":1710000004000}
{"sid":101,"relation_type":"async_replication","group_key":"native:g1","member_key":"mysql:m1","role":"replica","is_writer":0,"replication_state":"healthy","last_seen_epoch_ms":1710000004000}
{"sid":202,"relation_type":"aurora_cluster","group_key":"aurora:c1","member_key":"db:i2","role":"primary","is_writer":1,"replication_state":"healthy","last_seen_epoch_ms":1710000004000}
{"sid":202,"relation_type":"async_replication","group_key":"native:g1","member_key":"mysql:m2","role":"primary","is_writer":1,"replication_state":"healthy","last_seen_epoch_ms":1710000004000}
EOF

    run assert_aurora_failover "$before" "$after" 1710000003500

    [ "$status" -eq 0 ]
}
