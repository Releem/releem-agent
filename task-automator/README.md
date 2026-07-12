# Task Automator

Task Automator is the Releem Agent package that executes previously analyzed
MySQL and MariaDB schema changes for task type 6. It is integrated into the
main Agent process; this repository does not provide a separate task-automator
CLI.

## Execution Flow

The Platform validates each DDL statement and sends its analysis in the task
payload. The Agent then:

1. Rejects execution unless `enable_exec_ddl` is enabled.
2. Validates the task payload and the Platform analysis flags.
3. Checks local datadir capacity unless space checks are disabled.
4. Performs the requested pre-change backup.
5. Prefers native Online DDL with `ALGORITHM=INPLACE`, `INSTANT`, or MariaDB
   `NOCOPY` and `LOCK=NONE`.
6. Falls back to `pt-online-schema-change` when Online DDL reports an
   unsupported online algorithm or lock, or when an explicit blocking clause
   is rejected, and the analysis permits pt-osc.
7. Stops at the first failed statement and reports the detailed error.

Native Online DDL is first executed against an empty clone in the configured
scratch schema. Scratch creation, preflight, production DDL, and cleanup run on
one pinned database session with a temporary `lock_wait_timeout`.

## Configuration

Task Automator uses the main `releem.conf` file:

| Parameter | Default | Purpose |
| --- | --- | --- |
| `enable_exec_ddl` | `false` | Enables task type 6 execution. |
| `backup_dir` | `/tmp/backups` | Stores logical and physical backups. |
| `ptosc_path` | `pt-online-schema-change` | pt-osc executable path. |
| `mysqldump_path` | `mysqldump` | mysqldump executable path. |
| `xtrabackup_path` | `xtrabackup` | xtrabackup executable path. |
| `backup_space_buffer` | `20.0` | Extra free-space percentage required for backups. |
| `online_ddl_test_schema` | `releem_online_ddl_test` | Scratch schema for Online DDL preflight. |
| `disable_space_checks` | `false` | Disables local datadir and backup filesystem checks. |

External tools use `mysql_host`, `mysql_port`, `mysql_user`, and
`mysql_password`. A `mysql_host` beginning with `/` is passed as a Unix socket
to mysqldump, xtrabackup, and pt-osc.

## Go API

The task dispatcher constructs an executor from the Agent's existing database
connection:

```go
executor := phase2.NewExecutor(models.DB, &logger)
target := phase2.TableInfo{Database: "app", Table: "users"}
result, err := executor.Execute(phase2.ExecuteOptions{
    SQL:          "ALTER TABLE app.users ADD COLUMN last_seen_at DATETIME",
    Target:       &target,
    BackupMethod: phase2.BackupMysqldump,
    OkPTOSC:      true,
    OkOnlineDDL:  true,
    Config:       configuration,
    Debug:        configuration.Debug,
})
```

`Execute` returns the backup path, selected execution method, warnings, and
execution status. Callers must treat any returned error as a failed statement.
`Target` is the structured schema/table produced by Platform analysis. The
legacy `TableName` string remains available only for callers that do not yet
provide `Target`; it is resolved once at the execution boundary.

## Supported DDL

The native Online DDL path supports `ALTER TABLE` and common `CREATE INDEX`
variants. Explicit unsafe clauses such as `ALGORITHM=COPY`, `LOCK=SHARED`, or
`LOCK=EXCLUSIVE` are rejected rather than executed against the production
table. The table named by the SQL must match the task's configured table before
backup or execution begins. An unqualified SQL target inherits the configured
schema and is rewritten to a qualified target before execution. Case-only
differences are accepted when the server's `lower_case_table_names` setting is
`1` or `2`. Multi-statement input, server-executable comments, and
backslash-escaped quotes with SQL-mode-dependent parsing are also rejected.
Unqualified foreign-key `REFERENCES` targets inherit the configured schema.
Multi-object operations such as table `RENAME` (with or without `TO`/`AS`) and
`EXCHANGE PARTITION` are rejected because scratch preflight cannot isolate
their secondary tables.

The pt-osc path extracts the exact alter body without normalizing whitespace
inside quoted identifiers, literals, or comments. It always runs `--dry-run`
before `--execute`. Standalone `CREATE INDEX` statements are converted to the
equivalent `ADD ... INDEX` alter body; variants that cannot be converted without
changing semantics are rejected. This includes `ALTER IGNORE TABLE`, `ALTER
TABLE IF EXISTS`, and
standalone `CREATE INDEX` statements with MariaDB `WAIT` or `NOWAIT` clauses.

## Backups

- `mysqldump` creates one table-level SQL file under `backup_dir`.
- `xtrabackup` creates and prepares a filtered physical backup directory.
- The backup directory is created before its filesystem capacity is checked.

## Verification

Run the package and integration tests from the repository root:

```bash
/usr/local/go/bin/go test -count=1 ./task-automator/pkg/phase2
/usr/local/go/bin/go test -count=1 ./tasks
/usr/local/go/bin/go test -count=1 ./...
```

Release builds include Linux, FreeBSD, and Windows targets through `build.sh`.
