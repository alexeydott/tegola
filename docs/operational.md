# Operational Procedures (A43)

## Backup

### What to back up
1. **Feature data tables** — your application tables (use native DB tools)
2. **`tegola_revisions`** — revision + incarnation per feature (critical for CAS)
3. **`tegola_audit`** — audit log (durable receipt)
4. **`tegola_outbox`** — pending events (if using outbox dispatcher)
5. **`tegola_schema_version`** — schema version marker

### MySQL/MariaDB
```bash
mysqldump --single-transaction mydb \
  parcels tegola_revisions tegola_audit tegola_outbox tegola_schema_version \
  > backup-$(date +%Y%m%d).sql
```

### PostGIS
```bash
pg_dump -t parcels -t tegola_revisions -t tegola_audit -t tegola_outbox \
  -t tegola_schema_version mydb > backup-$(date +%Y%m%d).sql
```

### GeoPackage
```bash
cp data.gpkg backup-$(date +%Y%m%d).gpkg
# SQLite backup is atomic for a single file
```

## Restore

1. Restore the database from backup.
2. Verify `tegola_schema_version`:
   ```sql
   SELECT version FROM tegola_schema_version;
   -- Must equal the app's CurrentSchemaVersion (see provider/audit/migrate.go)
   ```
3. If version mismatch, run the migration (see Upgrade below).
4. Verify incarnation continuity:
   ```sql
   SELECT collection, feature_id, incarnation, revision
   FROM tegola_revisions ORDER BY collection, feature_id LIMIT 10;
   ```
   Incarnations must be preserved — a restore that resets incarnation to 0
   will cause stale clients (holding old incarnation in ETag) to get false
   412 conflicts, or worse, lost updates if clients ignore ETag.

## Upgrade

1. Stop write traffic (or put in read-only mode).
2. Run the migration **before** starting the new version:
   - The app runs `Migrate` automatically on `BeginFeatureTx` if
     `CheckSchemaVersion` fails, but for large tables run it manually.
3. Verify:
   ```sql
   SELECT version FROM tegola_schema_version; -- should be CurrentSchemaVersion
   ```
4. Check the `incarnation` column exists:
   ```sql
   -- MySQL
   SHOW COLUMNS FROM tegola_revisions LIKE 'incarnation';
   -- Postgres/SQLite
   SELECT name FROM pragma_table_info('tegola_revisions') WHERE name='incarnation';
   ```
5. Resume write traffic.

## Fault Handling

### "Commit outcome unknown" (A35)
If the client receives `MutationErrCommitUnknown`:
1. **Do NOT retry blindly** — the transaction may have committed.
2. Read the current revision:
   ```
   GET /collections/{id}/items/{fid}  → ETag header
   ```
3. Compare with the If-Match you sent:
   - If ETag matches your intended post-image → it committed, done.
   - If ETag is the old value → it did not commit, safe to retry.
   - If ETag is something else → another writer intervened, rebase.

### Stale ETag (412 Precondition Failed)
The feature was modified (or deleted/recreated with new incarnation).
1. GET the feature to obtain the new ETag.
2. Rebase your changes onto the new state.
3. Retry with the new If-Match.

### Lock conflicts
If a WFS `Transaction` fails with "feature is locked":
1. Check who holds the lock (WFS `GetFeature` does not expose this;
   inspect `tegola_lock_leases` table directly).
2. Wait for expiry or ask the holder to release.
3. Do not delete lock rows manually unless the holder is confirmed dead —
   this can cause lost updates.

## Durable Receipt (A43)

After a successful `MutationCoordinator.ExecuteAll`, the returned
`provider.CommitReceipt` contains:
- `Status`: `CommitCommitted`
- `TransactionID`: provider-native tx ID (for log correlation)
- `Timestamp`: commit time (RFC3339Nano, UTC)
- `Actor`: principal ID from `TxOptions.Actor`
- `Collections`: deduplicated public collection names mutated

The per-mutation `MutationOutcome` contains:
- `FeatureID`, `Revision` ("incarnation.revision"), `RevisionBefore`

These are also written to `tegola_audit` in the same transaction
(see A34), providing a durable, queryable receipt.
