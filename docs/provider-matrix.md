# Provider write evidence

## Review runs on 2026-10-04

These rows describe specific native runs of the reviewed source. They are not a
blanket certification of all versions, CRS, encodings or operations. Preserve the
exact commit/build identity and raw output with each release gate.

| Provider/profile | Native environment | Proven review coverage | Remaining boundary |
|---|---|---|---|
| MySQL native geometry | MySQL 8.4.11; strict SQL mode; REPEATABLE-READ | Insert, longitude/latitude axis check at 120/35, revision read, identical replace, hidden property/schema admission, physical alias CAS, delete/tombstone, future-schema refusal | Other versions, all geometry encodings and real commit-fault recovery not established |
| PostGIS native geometry | PostgreSQL 16.15; PostGIS 3.5.7; GEOS 3.14.1; PROJ 9.8.1 | Insert, guarded replace/update, revision read, hidden properties, alias CAS, delete, future-schema refusal | Other server/profile combinations and full production acceptance not established |
| GeoPackage | Native SQLite writer with an independently created GDAL fixture copy | Real HTTP PATCH/PUT/CAS, geometry bytes, exact decimal text, child relations, RTree/extents, trigger rollback, delete cascade, WFS 2 update, untouched polygon/multipart/Z, source immutability and integrity | This does not establish XYZ writing, every custom CRS or restore/fault acceptance |
| MariaDB | Not run in this review | Shared implementation and unit coverage only | Native MariaDB acceptance remains NOT_RUN |
| HANA | No write implementation | Read-only provider | Writes unsupported; no writer acceptance claimed |

The embedded browser exercised source-feature attribute load/save against the
GeoPackage server, undo/redo, empty string vs NULL, preservation of existing
geometry, and two-tab stale-validator conflict with the local draft retained.
The final built viewer also saved successfully after map-source refresh support
was added. Geometry drawing and large-number editing are not part of that result.

## Reproduce the provider gates

Use disposable databases: these tests create/modify fixture and service tables.
Supply credentials through the environment and keep them out of logs:

- `TEGOLA_REVIEW_MYSQL_DSN`: used by `TestReviewMySQLNativeMutation` in
  `provider/mysql/mutation_review_test.go`.
- `TEGOLA_REVIEW_POSTGIS_DSN`: used by `TestReviewPostGISNativeMutation` in
  `provider/postgis/mutation_review_test.go`.
- `TEGOLA_REVIEW_GPKG`: optional independent source fixture override used by
  `TestNativeGeoPackageHTTPPreservation` in `server/native_gpkg_review_test.go`.
  With CGO enabled the test defaults to bundled `testdata/wfs/editing-fixture.gpkg`
  and works on a copy; it does not require this environment variable.

Run `scripts/native-test.sh` with the intended environment, or invoke the named Go
tests directly. Missing required MySQL/PostGIS environments produce SKIP, never native PASS.
The GeoPackage tests require CGO and a working C compiler. Build the locked UI
assets before compiling a server for browser acceptance.

## Explicit exclusions

Configured/discovered derived bbox columns are rejected for writes until their
maintenance is implemented. A native GeoPackage RTree has separate supported
maintenance and is not equivalent to a derived bbox-column profile. External
writers that bypass revision management are outside conditional-write guarantees.

This review did not run real connection-loss-during-COMMIT recovery, backup/restore
acceptance, an official WFS/Part 4 ETS, or distributed/CDN invalidation. Keep these
as NOT_RUN requirements for any deployment that depends on them. See
[write limits](wfs-scope-limitations.md) and [operational procedures](operational.md).
