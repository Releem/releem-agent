# PostgreSQL Query Optimization Grants Design

## Goal

Replace procedural PostgreSQL privilege setup in `grant_postgresql_query_optimization_access` with explicit `GRANT` statements issued by the installer.

## Behavior

The installer queries the list of connectable non-template databases and executes a separate `GRANT CONNECT ON DATABASE` for each database.

For PostgreSQL 14 and newer, it then executes `GRANT pg_read_all_data` for the monitoring role.

For PostgreSQL 12 and 13, the installer connects to each database, queries its user schemas, and issues these statements for every schema:

- `GRANT USAGE ON SCHEMA`
- `GRANT SELECT ON ALL TABLES IN SCHEMA`
- `GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA`

The PostgreSQL 12/13 path grants access only to objects that exist when the installer runs. It does not configure default privileges for future objects. After grants complete on all supported versions, the installer warns to rerun after adding schemas or objects so grants and search_path stay current.

## Safety

Database, schema, and role identifiers are quoted in shell code by doubling embedded double-quote characters and wrapping the result in PostgreSQL identifier quotes. Empty names are rejected or skipped as appropriate.

The implementation must not use `DO`, procedural blocks, dynamic SQL through `EXECUTE format`, `set_config`, hex encoding, or `ALTER DEFAULT PRIVILEGES`.

Every grant runs with `ON_ERROR_STOP=1`. A failed database or schema grant causes the function to return a non-zero status.

## Tests

Bats tests verify that:

- generated commands contain only explicit grants;
- procedural constructs are absent;
- PostgreSQL 14+ receives `CONNECT` and `pg_read_all_data` grants;
- PostgreSQL 12/13 receives per-database and per-schema grants;
- quoted identifiers are safe;
- failures propagate from the function.

Live tests use PostgreSQL 12 and 14 containers to verify effective privileges for the monitoring role.

## search_path

After grants, the installer sets per-database role defaults:

`ALTER ROLE "<role>" IN DATABASE "<db>" SET search_path TO "$user", public, <user schemas…>;`

This lets EXPLAIN resolve unqualified names without runtime schema retries. Rerun the installer after adding schemas.
