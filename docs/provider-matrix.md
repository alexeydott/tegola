[← Write scope](wfs-scope-limitations.md) · [Back to README](../README.md) · [Write operations →](operational.md)

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
| MySQL raw MOS follow-up | MySQL 8.4.11; LONGBLOB; EPSG:3857; explicit precision 2, units m | Native six-family mutation matrix, quantization, annotation-byte preservation, attribute filter/bbox, polygon-hole exclusion, replacement/delete and 4326→3857 geometry write | No derived bbox-column writes; no MariaDB or default-precision inference |
| GeoPackage provider / raw SQLite MOS follow-up | SQLite BLOB; EPSG:3857; explicit precision 2, units m | Same native mutation matrix, plus real HTTP GET/PATCH/stale 412/geometry PATCH/direct SQL readback/DELETE→404 | This raw MOS storage is not GeoPackage binary geometry; bounds-backed writes remain rejected |
| Bounds-backed MOS read-only follow-up | MySQL 8.4.11 and GeoPackage provider/SQLite; independent native 10-byte-header MOS point fixture | Ordinary-table and custom `!BBOX!` tile reads; integer bounds; m/precision 2 and cm/precision 0; ordinary FeatureQuery; explicit custom feature-query/write rejection | Read evidence only; separate bounds-column maintenance is not implemented for writes |
| MariaDB | Not run in this review | Shared implementation and unit coverage only | Native MariaDB acceptance remains NOT_RUN |
| HANA | No write implementation | Read-only provider | Writes unsupported; no writer acceptance claimed |

The embedded browser exercised source-feature attribute load/save against the
GeoPackage server, undo/redo, empty string vs NULL, preservation of existing
geometry, and two-tab stale-validator conflict with the local draft retained.
The final built viewer also saved successfully after map-source refresh support
was added. Geometry drawing and large-number editing are not part of that result.

The native PR #2 follow-up also passed on the listed MySQL/PostGIS versions:
generated-key create admission while manual-key update remains available,
transaction ID matching the committed audit row, and deterministic revision-first
lock ordering for update/replace/delete, first revision creation and unguarded
writes. MySQL additionally verifies startup migration and admission with a
restricted DML-only runtime role after administrator preparation. The error-path
receipt test commits an already closed native transaction; it verifies retained
correlation, not a real connection loss during COMMIT or a lost acknowledgement.

## Attached audit follow-up

The seven findings in the follow-up audit are mapped below to scoped checks.
These checks do not imply every scenario proposed by that audit was executed.

| Finding | Implemented contract and verification |
|---|---|
| PR2-01 authentication | Stock CLI refuses production writes without an adapter; embedded authenticated write succeeds in the focused test. |
| PR2-02 QName | Advertised names resolve using validated namespace bindings; foreign/unknown bindings fail; discovery and request tests pass. |
| PR2-03 FES types | Queryables determine literal types. SQLite WKT and native PostGIS queries verify numeric-looking strings, booleans and large integers. PostGIS checks exact `123`/`00123`, true/false and `9007199254740993`, and explicit rejection of native NUMERIC/DATE/TIMESTAMPTZ. Date/time/decimal positive binding is covered only by focused catalog tests. |
| PR2-04 transaction locks | Native MySQL/PostGIS tests verify revision-first ordering. Real deadlock victims cannot subsequently report successful commit; rolled-back data and audit rows remain absent. |
| PR2-05 representation ETag | Native GeoPackage HTTP tests cover CRS84, EPSG:3857 and EPSG:4326 with JSON/HTML selection, matching mutation/readback validators, wrong-representation and stale rejection, and unchanged geometry bytes on attribute PATCH. |
| PR2-06 public URLs | Capabilities tests cover mounted paths, configured HTTPS/public host and URL-root overrides for all supported versions. |
| PR2-07 receipt correlation | Coordinator/WFS tests cover committed, known-not-committed and unknown outcomes, safe correlation headers, valid XML errors and post-commit callback panic. Native PostGIS commits data/audit, then an injected driver acknowledgement error returns unknown with the retained ID. |

The PostGIS acknowledgement test injects a driver error after a real native
COMMIT. It proves durable data/audit plus an unknown receipt under that fault;
it is not a wire-level connection cut. The earlier closed-transaction receipt
test remains a separate, narrower check. No public receipt lookup endpoint or
cross-user receipt access test is claimed.

Further online-review checks cover DescribeFeatureType geometry occurrence and
nullability in all three WFS versions, plus mutation timestamps using the shared
announced-positive-leap-second validator. These are focused schema/validation
checks, not additional native database or conformance-suite runs. A separate
native PostGIS regression rejects constant-default keys for create (including a
serial column whose default was changed to a constant), while preserving update
admission; identity/validated sequence defaults establish generated-key capability.

FES date/time/exact-decimal binding is unit-level evidence subject to provider
catalog admission. The current PostGIS source JSON path rejects native DATE
(OID 1082), TIMESTAMP (1114), TIMESTAMPTZ (1184) and NUMERIC (1700) before querying.
This review does not extend those source types or use casts to imply native
PostGIS FES/HTTP support. Admitted string, boolean and integer query tests must
be reported separately from rejection checks for unsupported native types.
`TestNativePostGISTypedFES` in `ogc/wfs/fes_postgis_native_test.go` uses
`TEGOLA_REVIEW_POSTGIS_DSN`; it executes the admitted filter through the real
catalog/GetFeature/provider query and checks returned IDs. The native runner
includes `./ogc/wfs`; missing credentials produce SKIP, not native PASS.

Native GeoPackage failure-path regressions also pass: a metadata-trigger failure
before COMMIT reports not-committed, and any failed Apply prevents a later partial
commit. Feature, audit, outbox and revision tables remain unchanged in those
cases. This does not add a GeoPackage transaction correlation ID or establish a
lost-acknowledgement recovery profile for that writer.

## Reproduce the provider gates

Use disposable databases: these tests create/modify fixture and service tables.
Supply credentials through the environment and keep them out of logs:

- `TEGOLA_REVIEW_MYSQL_DSN`: used by `TestReviewMySQLNativeMutation` in
  `provider/mysql/mutation_review_test.go`.
- `TEGOLA_REVIEW_MYSQL_ADMIN_DSN`: privileged disposable-server connection for
  `TestFollowupMySQL*` in `provider/mysql/mutation_followup_test.go`. These tests
  create isolated databases/users and inspect `performance_schema`; an ordinary
  database-scoped test user is insufficient. If omitted, the helper falls back
  to `TEGOLA_REVIEW_MYSQL_DSN`, which must then provide those privileges. CI uses
  its disposable MySQL administrator for this fixture, not a production account.
- `TEGOLA_REVIEW_POSTGIS_DSN`: used by `TestReviewPostGISNativeMutation` in
  `provider/postgis/mutation_review_test.go` and `TestFollowupPostGIS*` in
  `provider/postgis/mutation_followup_test.go`; the follow-up creates isolated
  schemas, so its role needs schema-creation privileges.
- `TEGOLA_REVIEW_GPKG`: optional independent source fixture override used by
  `TestNativeGeoPackageHTTPPreservation` in `server/native_gpkg_review_test.go`.
  With CGO enabled the test defaults to bundled `testdata/wfs/editing-fixture.gpkg`
  and works on a copy; it does not require this environment variable.

Run `scripts/native-test.sh` with the intended environment, or invoke the named Go
tests directly. Missing required MySQL/PostGIS environments produce SKIP, never native PASS.
The GeoPackage tests require CGO and a working C compiler. Build the locked UI
assets before compiling a server for browser acceptance.

### MOS-specific gates

The MOS matrix uses explicit `geometry_format = "mos"`, EPSG:3857,
`mos_precision = 2`, and `mos_units = "m"`. MySQL stores raw MOS in a LONGBLOB;
the GeoPackage provider test uses a raw SQLite BLOB table, not GeoPackage binary
geometry. Neither fixture has derived MINX/MAXX/MINY/MAXY columns. These tests
must not be relabeled as a bounds-backed MapplGIS write profile.

The passing mutation cases cover Point, LineString, Polygon with a hole,
MultiPoint, MultiLineString and MultiPolygon with a hole. An attribute-only
update preserves native 10-byte-header MOS and appended opaque annotations.
Geometry replacement is checked after decode and quantization; the explicit
4326 point `(0.0001, 0.0002)` is stored as 3857 `(11.13, 22.26)` under the tested
precision. The matrix exercises insert/read/filter/bbox/replace/delete and does
not claim every CRS or automatically selected precision.

Enable CGO (`CGO_ENABLED=1`) with a working C compiler, then run the
self-contained raw SQLite MOS matrix:

```shell
go test ./provider/gpkg -run '^TestNativeMOS(MutationMatrix|BoundsReadOnly)$' -count=1 -v
```

For MySQL, set `TEGOLA_MOS_MYSQL_DSN` to a disposable database using the Go MySQL
driver DSN syntax, then run:

```shell
go test ./provider/mysql -run '^TestNativeMOS(MutationMatrix|BoundsReadOnly)$' -count=1 -v
```

`TEGOLA_MOS_MYSQL_DSN` overrides the MOS test database. When unset, both direct
tests and `scripts/native-test.sh` reuse `TEGOLA_REVIEW_MYSQL_DSN`. If neither
variable is set, the MySQL MOS tests report SKIP, not PASS. The test role needs
fixture/service-table creation privileges; these commands are not read-only
probes against a production dataset. `TestNativeMOSBoundsReadOnly` separately
exercises bounds-backed reads and rejected write admission.

The self-contained HTTP regression uses the GeoPackage provider's raw MOS table:

```shell
go test ./server -run '^TestNativeMOSHTTPRoundtrip$' -count=1 -v
```

It verifies source HTTP reads, attribute PATCH with byte preservation, stale
If-Match rejection, CRS-aware geometry PATCH checked through direct SQL MOS
readback, and deletion followed by 404. This HTTP result does not imply a
separate MySQL HTTP/browser MOS run.

## Explicit exclusions

Configured/discovered derived bbox columns are rejected for writes until their
maintenance is implemented. A native GeoPackage RTree has separate supported
maintenance and is not equivalent to a derived bbox-column profile. External
writers that bypass revision management are outside conditional-write guarantees.

This review did not run wire-level connection-loss-during-COMMIT recovery, backup/restore
acceptance, an official WFS/Part 4 ETS, or distributed/CDN invalidation. Keep these
as NOT_RUN requirements for any deployment that depends on them. See
[write limits](wfs-scope-limitations.md) and [operational procedures](operational.md).

## See Also

- [Write scope](wfs-scope-limitations.md) — admitted operations and exclusions.
- [Geometry formats](geometry-formats.md) — MOS quantization and read-side bounds.
- [Write operations](operational.md) — recovery requirements beyond fixture tests.
