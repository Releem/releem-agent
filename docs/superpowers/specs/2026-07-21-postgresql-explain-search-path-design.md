# PostgreSQL EXPLAIN search_path Design

## Goal

Align PostgreSQL EXPLAIN schema resolution with Datadog DBM: use the monitoring role’s configured `search_path` instead of forcing `pg_catalog` and retrying candidate user schemas at runtime.

## Background

`pg_stat_statements` does not record the schema / `search_path` of the original session. Unqualified relation names therefore depend on session `search_path` during EXPLAIN.

Current Releem behavior:

1. Force `search_path = pg_catalog` (direct and prepared EXPLAIN).
2. On `undefined_table` (`42P01`), enumerate user schemas and retry EXPLAIN in each.
3. Fail with ambiguity when more than one schema succeeds.

Datadog behavior:

1. Connect to the target database.
2. Run EXPLAIN with the agent role’s default `search_path`.
3. On undefined table / invalid schema, fail and cache; no schema retry.
4. Document / configure `ALTER ROLE … SET search_path` and `GRANT USAGE ON SCHEMA`.

## Behavior

### Runtime EXPLAIN

- Direct and prepared EXPLAIN must not set `search_path` (no `pg_catalog` baseline, no per-schema override).
- Remove candidate-schema retry helpers used only for EXPLAIN:
  - `retryPgExplainForCandidateSchemas`
  - `resolvePgExplainCandidateSchemas`
  - `fetchPgUserSchemas` (if unused elsewhere)
  - `executeDirectExplainInSchema` / `executePreparedExplainInSchema` schema-forcing paths
- On undefined relation or other EXPLAIN errors, return the original error (permission errors still map to `need_grant_permission`).
- Relation resolution uses whatever `search_path` the monitoring connection inherits from role / database settings.

### Installer

Extend `grant_postgresql_query_optimization_access` (Linux `install.sh`; mirror on Windows if PostgreSQL QO grants exist there) so that after existing grants, for each connectable non-template database the installer runs:

```sql
ALTER ROLE "<monitoring_role>" IN DATABASE "<database>"
  SET search_path TO "$user", public, "<schema1>", "<schema2>", ...;
```

Rules:

- Always include `"$user"` and `public` first (PostgreSQL identifier form for `$user` as the special search_path entry).
- Append distinct user schemas for that database (same exclusion set as today: not `information_schema`, `pg_catalog`, `pg_toast%`, `pg_temp_%`), ordered by `nspname` for deterministic installer output.
- Skip empty schema names; quote identifiers safely with the existing `quote_postgresql_identifier` helper.
- If a database has no user schemas, still set `search_path TO "$user", public`.
- PostgreSQL 14+: keep `GRANT CONNECT` + `GRANT pg_read_all_data`; still enumerate schemas **only** to build `search_path`.
- PostgreSQL 12/13: keep per-schema `USAGE` / `SELECT` grants; after those grants (or in the same per-database loop), set `search_path` for that database.
- Do not use `DO`, procedural blocks, `EXECUTE format`, or `set_config` for this change—explicit `ALTER ROLE … IN DATABASE … SET` statements only.
- Failures use `ON_ERROR_STOP=1` and propagate non-zero status.
- Keep / extend the existing warning that operators should rerun the installer after adding schemas (search_path must be refreshed).

### Out of scope

- Switching EXPLAIN source from `pg_stat_statements` to `pg_stat_activity` (Datadog samples).
- Adding a `datadog.explain_statement`-style wrapper function.
- Changing MySQL / MariaDB paths.
- Platform payload changes.

## Tests

### Go

- Direct EXPLAIN must not emit `SET LOCAL search_path` / `SET search_path`.
- Prepared EXPLAIN must not emit search_path overrides.
- Undefined relation returns the original error; no `FROM pg_namespace` candidate lookup.
- Remove or rewrite tests that assert candidate-schema retry / ambiguous-schema behavior.

### Installer (Bats)

- Generated args include `ALTER ROLE … IN DATABASE … SET search_path TO` with `"$user"`, `public`, and discovered schemas.
- PostgreSQL 14+ still grants `pg_read_all_data` and also emits search_path ALTER.
- PostgreSQL 12/13 still emits per-schema grants and search_path ALTER.
- Quoted role / database / schema identifiers remain safe.
- Failure of ALTER ROLE propagates.

## Success criteria

- EXPLAIN no longer depends on runtime schema guessing.
- Monitoring role receives a per-database `search_path` covering known user schemas at install time.
- Existing grant safety constraints (no procedural SQL) remain intact.
