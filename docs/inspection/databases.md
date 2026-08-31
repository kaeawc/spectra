# Database inspection

When an application under debug talks to a database, the schema, its
relationships, and the table health stats are often the fastest route to an
explanation — a missing index behind a slow screen, dead-tuple bloat behind
growing latency, a foreign key that explains a cascade of deletes. `spectra
db` connects directly with credentials you already have and reads that
picture without disturbing the running workload.

PostgreSQL is supported via [pgx](https://github.com/jackc/pgx),
MySQL/MariaDB via [go-sql-driver](https://github.com/go-sql-driver/mysql),
and SQLite via [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) —
spectra's own storage driver, so SQLite support adds no dependency. All
three sit behind the same engine-neutral report types in
`internal/dbinspect`. The engine is inferred from the DSN: `postgres://` /
`postgresql://` URLs and libpq keyword form select postgres, `mysql://`
URLs and go-sql-driver's `user:pass@tcp(host:port)/db` form select mysql,
and `sqlite://` URLs, `file:` URIs, or a bare path ending in `.db` /
`.sqlite` / `.sqlite3` select sqlite.

## Non-disruptive by construction

Every inspection opens one short-lived connection with a session forced
read-only and bounded, so no query — even a mistaken one — can write or
stall the server.

PostgreSQL sessions set at startup:

| Parameter | Value | Effect |
|---|---|---|
| `default_transaction_read_only` | `on` | every transaction is read-only |
| `statement_timeout` | 5s | no query can run away |
| `lock_timeout` | 2s | never queues behind DDL or long writers |
| `idle_in_transaction_session_timeout` | 5s | can't pin vacuum horizon |
| `application_name` | `spectra-dbinspect` | identifiable in `pg_stat_activity` |

MySQL/MariaDB sessions run `SET SESSION TRANSACTION READ ONLY` immediately
after connecting — if the server refuses, spectra refuses to inspect it —
then bound waits best-effort (`max_execution_time` on MySQL,
`max_statement_time` on MariaDB, `innodb_lock_wait_timeout` on both). The
connection pool is capped at one connection so those session settings
govern every query.

SQLite files open with `mode=ro` (refuses writes at the file layer), the
`query_only` pragma (refuses them at the statement layer), and a 2s busy
timeout so inspection fails fast rather than holding locks against the
application that owns the file.

Structural reads use only the engine's catalog (`pg_catalog` and
`pg_stat_*` on postgres, `information_schema` on mysql, `sqlite_schema`
and pragma functions on sqlite). Row counts are estimates
(`reltuples`/`n_live_tup`, `table_rows`, `sqlite_stat1`) — spectra never
issues `COUNT(*)` or any other full-table scan. Every caller-supplied
filter is a bind parameter; the one place an identifier is interpolated
(row sampling) resolves the name through the catalog first (`to_regclass`
against `pg_class`, `information_schema.TABLES`, or `sqlite_schema`) and
quote-escapes the catalog's own spelling.

## Discovering that an app uses a database

`spectra db discover` combines three signals:

- **Live sockets** — active connections whose remote port is a well-known
  database port (5432/5433/6432 postgres, 3306/3307 mysql), from the same
  `lsof` collector that backs `spectra network connections`, with PID and
  command attribution.
- **Connection env vars** — a fixed allowlist (`DATABASE_URL`,
  `POSTGRES_URL`, `PGHOST`, `PGUSER`, `PGPASSWORD`, ...), never the full
  environment. Passwords and URL credentials are redacted before they reach
  output or logs.
- **Open SQLite files** — regular-file handles whose path ends in a
  database suffix (`.db`, `.sqlite`, `.sqlite3`), from one host-wide
  `lsof` pass, with WAL/SHM/journal sidecars folded into their database
  path and PID+command attribution.

## CLI

```bash
spectra db discover
spectra db overview
spectra db schema --schema public
spectra db relations
spectra db stats
spectra db sample --limit 20 billing.invoices
```

Connection strings resolve from `--dsn`, then `SPECTRA_DB_DSN`, then
`DATABASE_URL`, then the standard libpq `PG*` env vars. Both URL and
keyword DSN forms work. Every subcommand takes `--json`.

- `overview` — server version, database size, connection usage, and table
  counts per schema.
- `schema` — every user relation with columns (type, nullability, default,
  primary key) and indexes.
- `relations` — foreign keys with column lists and delete/update actions,
  for reconstructing the data model.
- `stats` — per-table health, largest tables first. On postgres this is
  `pg_stat_user_tables`: sequential vs index scans, live/dead row
  estimates, total size, last (auto)vacuum and analyze. On mysql it is
  `information_schema.TABLES` row and size estimates — scan counters need
  `performance_schema` and are left zero. On sqlite, row estimates come
  from `sqlite_stat1` (present after `ANALYZE`) and sizes from the
  `dbstat` virtual table when the build ships it.
- `sample` — up to N rows (default 10, capped at 500) from one table.

## Row data is sensitive

Schema and stats are structural. Row samples are not: they can contain
customer PII and secrets. `sample` is therefore treated like a heap dump —
it is recorded in the artifact manifest at `very-high` sensitivity, and the
daemon RPC and MCP operations refuse it without
`{"confirm_sensitive": true}`, subject to the same
[artifact policy](../operations/artifacts.md) as `jvm.heap_dump`.

## Daemon RPC and MCP

The daemon registers `db.discover`, `db.overview`, `db.schema`,
`db.relations`, `db.stats`, and `db.sample`, so a remote engineer can
inspect through an existing `spectra serve` session:

```bash
spectra connect work-mac db.overview '{"dsn": "postgres://app@10.0.0.5/orders"}'
```

The MCP server exposes the same operations on the `db` tool
(`operation: discover | overview | schema | relations | stats | sample`),
so an agent can go from "this process holds a socket to 10.0.0.5:5432" to a
schema map in two calls.

## See also

- [Live data sources](live-data-sources.md) — where database inspection
  sits among the other collectors.
- [Network endpoints](network-endpoints.md) — the socket attribution that
  feeds discovery.
- [Artifacts](../operations/artifacts.md) — sensitivity policy that gates
  row sampling.
