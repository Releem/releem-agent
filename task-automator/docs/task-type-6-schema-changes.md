# Task Type 6: Schema Change Execution

Task type 6 applies DDL statements that the Releem Platform has already
analyzed. `ProcessTask` dispatches the task to `ApplySchemaChanges`; operational
execution is implemented by `phase2.Executor`.

## Task Payload

`TaskStruct.Details` is a JSON object containing a `statements` array:

```json
{
  "type": "query_optimization",
  "id": 123,
  "statements": [
    {
      "schema_name": "app",
      "ddl_statement": "ALTER TABLE app.users ADD COLUMN last_seen_at DATETIME NULL",
      "pre_change_bkp": true,
      "analysis_results": {
        "schema_name": "app",
        "table_name": "users",
        "syntax_valid": true,
        "syntax_error": null,
        "storage_engine": "InnoDB",
        "ok_online_ddl": true,
        "ok_pt_osc": false,
        "ok_online_physical_backup": true,
        "ok_pitr": true
      }
    }
  ]
}
```

Required statement fields for syntactically valid DDL:

- `schema_name`
- `ddl_statement`
- `analysis_results.schema_name`
- `analysis_results.table_name`

`schema_name` is always required. For syntactically valid DDL,
`schema_name` and `analysis_results.schema_name` must use exactly the same
spelling, and `analysis_results.table_name` is required. The Agent rejects an
inconsistent payload before starting backup or execution. Syntax-invalid DDL
may omit the analysis target so the original syntax error can be reported.

Execution flags:

- `syntax_valid` must be true.
- At least one of `ok_online_ddl` and `ok_pt_osc` must be true.
- `pre_change_bkp` requests a backup.
- A requested backup also requires `ok_pitr`.
- `ok_online_physical_backup` selects xtrabackup; otherwise mysqldump is used.

## Agent Gate

The Agent starts task type 6 only when `enable_exec_ddl=true`. When execution is
disabled, it returns task status 4 and exit code 10 without parsing or executing
the DDL.

## Statement Processing

`ApplySchemaChanges` validates the complete payload structure, then processes
statements in order. Processing stops at the first validation, backup, or
execution error. Statements completed before a later failure are not rolled
back, so the task output records every successful statement before the error.

For each statement, the Agent:

1. Verifies the Platform syntax and execution flags.
2. Selects the requested backup method.
3. Calls `Executor.Execute` with the DDL and the structured schema/table from
   `analysis_results`.
4. Appends success or failure details to the task result.

## Filesystem Checks

Unless `disable_space_checks=true`, the executor checks local filesystem
capacity before changing the table:

- The MySQL datadir filesystem must have more than 10 percent free space.
- Projected datadir usage after adding one table-size estimate must not exceed
  90 percent.
- The backup filesystem must hold the estimated backup plus
  `backup_space_buffer`.

The backup directory is created before its filesystem is inspected. Filesystem
usage is collected through a portable implementation used by Linux, FreeBSD,
and Windows builds.

## Backups

Mysqldump creates:

```text
<backup_dir>/<YYMMDDHHMMSS>_<schema>_<table>.sql
```

It uses `--single-transaction`, `--quick`, and `--lock-tables=false`.

Xtrabackup creates:

```text
<backup_dir>/<YYMMDDHHMMSS>_xtrabackup_<schema>_<table>/
```

It filters the selected table, runs the backup, then prepares it with
`--export`.

When `mysql_host` begins with `/`, all external tools use that Unix socket:
mysqldump uses `--socket`, xtrabackup uses `--socket`, and pt-osc uses its `S`
DSN field.

## Native Online DDL

When `ok_online_ddl=true`, the executor:

1. Pins one SQL connection and sets `lock_wait_timeout=20` for the complete
   scratch and production lifecycle.
2. Creates the configured scratch schema and an empty clone of the source table
   with a bounded hashed name.
3. Adds missing `ALGORITHM=INPLACE` and `LOCK=NONE` clauses.
4. Rejects explicit algorithms other than `INPLACE`, `INSTANT`, or MariaDB
   `NOCOPY`.
5. Rejects explicit locks other than `NONE`.
6. Rewrites the exact target-table span and executes the DDL against the clone.
7. Executes the production DDL on the same pinned connection and restores the
   previous timeout.
8. Drops the clone.

The test table name never exceeds MySQL's 64-character identifier limit.
Comments and quoted values are ignored while finding Online DDL clauses and are
preserved in the executed statement. Multi-statement input, server-executable
comments, and backslash-escaped quotes whose parsing depends on `sql_mode` are
rejected before preflight.

The Agent resolves an unqualified SQL table name against the schema supplied by
the task, then rewrites the statement to that fully qualified target. Qualified
SQL must name the same schema and table. Case-only differences are accepted
when the server reports `lower_case_table_names=1` or `2`. A mismatch is
rejected before filesystem checks, backup, preflight, or pt-osc execution.
Unqualified tables following `REFERENCES` inherit the task schema. Table
`RENAME` operations, with or without `TO`/`AS`, and `EXCHANGE PARTITION` are
rejected because their secondary tables cannot be isolated inside scratch
preflight. Column and index rename operations remain supported.

## pt-online-schema-change

Pt-osc is selected directly when native Online DDL is not permitted. It is also
used as a fallback when `ok_pt_osc=true` and either:

- The Agent rejects an explicit blocking Online DDL algorithm or lock before
  preflight.
- The server reports an unsupported online algorithm or lock, including MySQL
  and MariaDB Online DDL error codes.

The Agent extracts the exact alter body without collapsing whitespace inside
quoted content. Standalone `CREATE INDEX` is converted to `ADD ... INDEX` when
that conversion preserves semantics. `ALTER IGNORE TABLE`, `ALTER TABLE IF
EXISTS`, and standalone `CREATE INDEX` with MariaDB `WAIT` or `NOWAIT` are
rejected because pt-osc cannot preserve those semantics. Pt-osc always
completes `--dry-run` before `--execute`.

## Result Contract

Success returns task status 1 and exit code 0. Validation and execution failures
return status 4 with a statement-scoped error in both `task_output` and
`task_error`. The original `task_details` object is preserved in started and
terminal status payloads. Successful statement output includes the method used
and any fallback or cleanup warnings.

## Configuration

| Parameter | Default |
| --- | --- |
| `enable_exec_ddl` | `false` |
| `backup_dir` | `/tmp/backups` |
| `ptosc_path` | `pt-online-schema-change` |
| `mysqldump_path` | `mysqldump` |
| `xtrabackup_path` | `xtrabackup` |
| `backup_space_buffer` | `20.0` |
| `online_ddl_test_schema` | `releem_online_ddl_test` |
| `disable_space_checks` | `false` |
