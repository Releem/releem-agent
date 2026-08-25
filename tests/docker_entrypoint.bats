#!/usr/bin/env bats

setup() {
    TEST_TMPDIR="$(mktemp -d)"
    TEST_OPT_DIR="${TEST_TMPDIR}/opt/releem"
    TEST_MYSQL_DIR="${TEST_TMPDIR}/etc/mysql/releem.conf.d"
    mkdir -p "${TEST_OPT_DIR}" "${TEST_MYSQL_DIR}"

    cp "${BATS_TEST_DIRNAME}/../docker/entrypoint.sh" "${TEST_TMPDIR}/entrypoint.sh"
    sed -i \
        -e "s#/opt/releem#${TEST_OPT_DIR}#g" \
        -e "s#/etc/mysql/releem.conf.d#${TEST_MYSQL_DIR}#g" \
        "${TEST_TMPDIR}/entrypoint.sh"
    cat > "${TEST_OPT_DIR}/releem-agent" <<'EOF'
#!/bin/sh
exit 0
EOF
    chmod +x "${TEST_OPT_DIR}/releem-agent"
}

teardown() {
    rm -rf "${TEST_TMPDIR}"
}

run_entrypoint() {
    run env -i PATH="${PATH}" "$@" bash "${TEST_TMPDIR}/entrypoint.sh" true
}

run_template_values() {
    run env -i PATH="${PATH}" "$@" bash -c '
        source "$1"
        printf "%s\n%s\n%s\n%s\n" \
            "$table_size_cache_table_threshold" \
            "$table_size_cache_ram_multiplier" \
            "$table_size_cache_ttl_seconds" \
            "$aws_rds_cluster_parameter_group"
    ' bash "${BATS_TEST_DIRNAME}/../docker/releem.conf.tpl"
}

config_value() {
    local name="$1"
    sed -n "s/^${name}=//p" "${TEST_OPT_DIR}/releem.conf"
}

@test "Docker entrypoint keeps an empty cluster group for non-Aurora RDS" {
    run_entrypoint

    [ "$status" -eq 0 ]
    [ "$(config_value table_size_cache_table_threshold)" = "10000" ]
    [ "$(config_value table_size_cache_ram_multiplier)" = "4" ]
    [ "$(config_value table_size_cache_ttl_seconds)" = "604800" ]
    [ "$(config_value aws_rds_cluster_parameter_group)" = '""' ]

    run_template_values
    [ "$status" -eq 0 ]
    [ "$output" = $'10000\n4\n604800' ]
}

@test "Docker entrypoint accepts table cache overrides" {
    run_entrypoint \
        RELEEM_TABLE_SIZE_CACHE_TABLE_THRESHOLD=2500 \
        RELEEM_TABLE_SIZE_CACHE_RAM_MULTIPLIER=6 \
        RELEEM_TABLE_SIZE_CACHE_TTL_SECONDS=172800

    [ "$status" -eq 0 ]
    [ "$(config_value table_size_cache_table_threshold)" = "2500" ]
    [ "$(config_value table_size_cache_ram_multiplier)" = "6" ]
    [ "$(config_value table_size_cache_ttl_seconds)" = "172800" ]

    run_template_values \
        RELEEM_TABLE_SIZE_CACHE_TABLE_THRESHOLD=2500 \
        RELEEM_TABLE_SIZE_CACHE_RAM_MULTIPLIER=6 \
        RELEEM_TABLE_SIZE_CACHE_TTL_SECONDS=172800
    [ "$status" -eq 0 ]
    [ "$output" = $'2500\n6\n172800' ]
}

@test "Docker entrypoint keeps the legacy cluster parameter-group environment variable" {
    run_entrypoint AWS_RDS_CLUSTER_PARAMETER_GROUP=legacy-cluster-group

    [ "$status" -eq 0 ]
    [ "$(config_value aws_rds_cluster_parameter_group)" = '"legacy-cluster-group"' ]

    run_template_values AWS_RDS_CLUSTER_PARAMETER_GROUP=legacy-cluster-group
    [ "$status" -eq 0 ]
    [ "$output" = $'10000\n4\n604800\nlegacy-cluster-group' ]
}

@test "Docker entrypoint gives the Releem cluster variable precedence" {
    run_entrypoint \
        AWS_RDS_CLUSTER_PARAMETER_GROUP=legacy-cluster-group \
        RELEEM_AWS_RDS_CLUSTER_PARAMETER_GROUP=releem-cluster-group

    [ "$status" -eq 0 ]
    [ "$(config_value aws_rds_cluster_parameter_group)" = '"releem-cluster-group"' ]

    run_template_values \
        AWS_RDS_CLUSTER_PARAMETER_GROUP=legacy-cluster-group \
        RELEEM_AWS_RDS_CLUSTER_PARAMETER_GROUP=releem-cluster-group
    [ "$status" -eq 0 ]
    [ "$output" = $'10000\n4\n604800\nreleem-cluster-group' ]
}
