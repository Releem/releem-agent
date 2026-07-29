# EXPLAIN Collection SELECT/WITH Alignment Design

## Goal

Make MySQL and PostgreSQL EXPLAIN collection use the same eligibility rule: only statements whose leading command is `SELECT` or `WITH`. For PostgreSQL, also align permission and connection failure markers with MySQL-style contracts.

## Background

MySQL `CollectExplain` today:

- Explains statements whose text *contains* `SELECT ` / `select ` (substring), which can miss leading `WITH` CTEs and is not a leading-command check.
- Skips mysqldump-style `SQL_NO_CACHE` selects without `WHERE`.
- Maps SELECT/access denial to `need_grant_permission`.
- Writes other EXPLAIN failures to `explain_error` as the error text.

PostgreSQL currently treats a broader set as explainable (`SELECT`, `TABLE`, `INSERT`, `UPDATE`, `DELETE`, `MERGE`, `WITH`) and on connection failure sets `explain_error = "connection_failed"` without the underlying error.

Dump traffic for PostgreSQL is mostly `COPY`, which is already outside SELECT/WITH eligibility. No separate dump heuristic is required (option B). MySQL keeps its existing `SQL_NO_CACHE` dump skip.

## Behavior

### Eligible statements (MySQL and PostgreSQL)

Both engines must explain a statement only when the leading command (after whitespace/comments) is:

- `select`
- `with`

All other leading commands (`insert`, `update`, `delete`, `merge`, `table`, `copy`, `replace`, …) are skipped without setting `explain` / `explain_error`.

Shared or mirrored helpers are fine; PostgreSQL already has `pgLeadingCommand` / `isPgExplainableStatement`. MySQL should stop using the substring `Contains("SELECT ")` gate and use the same leading-command rule so `WITH … SELECT` is eligible and non-SELECT statements that merely mention the word SELECT are not.

MySQL dump skip (`SQL_NO_CACHE` without `WHERE`) remains after eligibility.

### Permission errors (PostgreSQL; MySQL already)

When EXPLAIN fails due to insufficient privilege, set:

```text
explain_error = "need_grant_permission"
```

Otherwise set `explain_error` to the error’s text (`err.Error()`).

Successful EXPLAIN continues to set `explain` and count toward the success quota (PostgreSQL) / success counter (MySQL).

### Connection failures (PostgreSQL)

When connecting to the target database fails:

1. Set on that query:
   ```text
   explain_error = "connection_failed: " + <underlying error text>
   ```
2. Do not leave the query without an error marker.
3. Keep marking the database as failed for the remainder of the current collection so later queries for the same database are not retried blindly; those later queries should also receive `explain_error = "connection_failed: " + <same or cached error>` (or the recorded failure reason).

`ConnectionDatabase` / `ConnectionPostgreSQL` currently return only `*sql.DB` (nil on failure) and log the error. The EXPLAIN collector must obtain the underlying error for the `explain_error` string—either by:

- changing the injectable connect helper used by `pgExplainCollectionState` to return `(*sql.DB, error)`, or
- an equivalent local wrapper that surfaces the ping/open error without changing unrelated callers more than necessary.

Prefer scoping the `(db, err)` return to the PostgreSQL EXPLAIN collection path (injectable `connect`) rather than a broad utils API break, unless a shared helper is clearly better.

### Out of scope

- Separate pg_dump SQL text heuristics beyond SELECT/WITH eligibility.
- Switching EXPLAIN source from `pg_stat_statements` / digest to `pg_stat_activity`.
- Installer / `search_path` changes (covered by the earlier search_path design).
- Changing MySQL dump (`SQL_NO_CACHE`) or schema-exclude / age / truncation rules except the eligibility gate above.

## Tests

### Shared eligibility contract

For both MySQL and PostgreSQL helpers:

- Leading `SELECT` and `WITH …` → eligible true.
- Leading `INSERT` / `UPDATE` / `DELETE` / `MERGE` / `TABLE` / `COPY` / `REPLACE` → eligible false.
- Comments/whitespace before `WITH` or `SELECT` still eligible.

### PostgreSQL collect path

- Non-SELECT skipped without `explain_error`.
- Permission failure → `explain_error == "need_grant_permission"`.
- Other EXPLAIN failure → `explain_error` equals `err.Error()`.
- Connection failure → `explain_error` has prefix `connection_failed:` and includes the underlying error text.
- `WITH` queries are attempted for EXPLAIN.

### MySQL collect path

- Leading `WITH` is no longer skipped by the old substring gate.
- Leading non-SELECT is skipped even if the text contains the word `SELECT` elsewhere.

## Success criteria

- MySQL and PostgreSQL EXPLAIN eligibility is SELECT + WITH only (leading command).
- PostgreSQL permission and connection failure markers match the contracts above.
- Existing PostgreSQL success quotas and search_path behavior remain unchanged.
- MySQL dump / schema / age filters remain as today except eligibility.
