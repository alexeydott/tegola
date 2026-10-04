# Write operations and recovery

These procedures define required deployment checks, not a claim that every
failure/restore scenario has been executed. See [scope](wfs-scope-limitations.md)
and [provider test evidence](provider-matrix.md).

## Backup and restore

Quiesce application and external writers before a recovery backup or restore.
Back up feature tables, their spatial metadata/indexes, `tegola_revisions`,
`tegola_audit`, `tegola_outbox`, and `tegola_schema_version` together, using the
provider's consistent backup tool. Include sequences, triggers, constraints and
all related application tables. A list of a few feature tables alone is not a
complete database recovery plan.

For transactional MySQL/MariaDB tables use a consistent `mysqldump
--single-transaction` backup without concurrent DDL. For PostgreSQL use `pg_dump`
with the required schemas and dependencies. Verify restore under the actual
roles and extension versions before accepting either backup.

For GeoPackage use the SQLite online backup API while writes are quiesced, for
example:

```shell
sqlite3 data.gpkg ".backup 'backup.gpkg'"
sqlite3 backup.gpkg "PRAGMA integrity_check; PRAGMA foreign_key_check;"
```

Do not copy only a live `.gpkg` file: committed data may still be in its WAL.
SQLite integrity checks are necessary but do not validate all GeoPackage metadata,
RTree contents, or application semantics. Restore to a disposable destination and
verify representative geometry/attributes, indexes, relations and service tables.

Keep writes disabled after restore. Restoring older revision/incarnation state can
make a validator held by a pre-restore client match again. Preservation of stored
incarnation values does not itself establish a new database/source generation.
Invalidate outstanding client sessions/validators using a deployment recovery
procedure; do not re-enable writes until stale pre-restore requests are proven
unable to pass concurrency admission. Clear old tile caches on every reader.

## Schema upgrades

Inspect `SELECT MAX(version) FROM tegola_schema_version` and compare with
`provider/audit/migrate.go`. A missing service schema or a known supported older
version may be migrated with the required DDL privileges. Permission/connectivity
errors, unknown versions and newer schemas must stop admission; do not treat every
version-check error as permission to run DDL or downgrade the marker.

Migration is outside feature data transactions. Rehearse it on a restored copy
before deploying, including app rollback compatibility and the runtime role's
permissions. There is no universally safe automatic downgrade procedure.

Use the database's actual metadata interface when checking columns:

```sql
-- PostgreSQL (substitute the configured schema)
SELECT column_name FROM information_schema.columns
WHERE table_schema = 'public' AND table_name = 'tegola_revisions';
-- MySQL/MariaDB
SHOW COLUMNS FROM tegola_revisions;
-- SQLite/GeoPackage
PRAGMA table_info('tegola_revisions');
```

## Failed or ambiguous writes

Never retry an unknown commit blindly. Reconcile against the authoritative
primary using transaction/request correlation, committed audit rows and source
state; a replica read may lag. An old ETag does not prove that the attempted write
did not commit, and a matching post-image alone does not identify which writer
produced it. If evidence cannot distinguish the outcome, keep the operation
unresolved for manual reconciliation. Create retries can duplicate data.

A successful response with `Tegola-Commit-Status: committed` means mutation commit
succeeded but source readback was unavailable. Fetch its resource later using the
returned location/identity; do not repeat the mutation to retrieve a body.

For 412, preserve the local draft, load the authoritative source, reconcile the
changes and issue a new request with that representation's ETag. Do not silently
replace a stale validator and resubmit the old draft.

LockFeature is unsupported in this publication. Internal lock-store tables are
not an operator API or a distributed lock guarantee; manually deleting rows does
not repair concurrency protection.

## Audit and receipts

The application receipt reports a commit status and correlation information.
Audit/outbox rows written inside a data transaction become durable when that
transaction commits. Their timestamps are application observation times generated
before/around commit, not a database-proven exact commit timestamp. The returned
receipt is not automatically a durable, idempotently queryable request receipt.

Retain and protect audit records according to the deployment's privacy and
retention policy. Outbox storage alone does not prove delivery, cache invalidation
or exactly-once processing. Independently test restore, disk-full, timeout,
connection-loss, shutdown and external-writer scenarios before production use.
