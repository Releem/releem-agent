#!/usr/bin/env bats

setup() {
    TEST_TMPDIR="$(mktemp -d)"
    SCRIPT="${BATS_TEST_DIRNAME}/aws_rds_topology.sh"
    export AWS_TOPOLOGY_RUN_ID=task13-20260908
    export AWS_TOPOLOGY_STATE_DIR="$TEST_TMPDIR/state"
    export AWS_TOPOLOGY_EVIDENCE_DIR="$TEST_TMPDIR/evidence"
    source "$SCRIPT" help >/dev/null
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

@test "create planner is idempotent for existing owned resources" {
    desired="$TEST_TMPDIR/desired.txt"
    existing="$TEST_TMPDIR/existing.txt"
    printf '%s\n' network subnet-group security-group cluster/provisioned instance/provisioned-1 >"$desired"
    printf '%s\n' security-group cluster/provisioned >"$existing"

    run plan_missing_resources "$desired" "$existing"

    [ "$status" -eq 0 ]
    [ "$output" = $'network\nsubnet-group\ninstance/provisioned-1' ]
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
