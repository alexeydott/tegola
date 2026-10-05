# Upstream provenance, fork fixes, and deferred debt

This fork is built on the upstream [go-spatial/tegola](https://github.com/go-spatial/tegola)
project. It was forked from upstream `master` after the v0.21.0 release (2024-12-19):
the fork base is upstream `master` post-v0.21.0, and releases use the version scheme
`v0.21.0-fork.N`. (Upstream does not maintain a CHANGELOG past 0.17.0 - see
[Sync methodology](#sync-methodology).)
This file records upstream provenance, the bugs fixed in the fork that are candidates for
upstream PRs, the versioning scheme, the sync methodology, and the technical debt deferred
to a later wave.

## Current fork release and source integration (2026-10-02)

The annotated source tag **`v0.21.0-fork.2`** binds
`db4e8ee73a3ddfe3b3c8e054c59e833b697dce85`. It includes the OGC Features
implementation and the MOS probe/diagnostic integration reviewed in `8bc78cd4`.
The subsequent `e4c8a1b8` commit updates documentation only; it does not move the
release tag or change the executable source captured by that tag.

The release adds opt-in feature publication through the existing router,
`ogc/features`, `ogc/cql2` and optional `provider.FeatureQuerier` contracts.
Its application profile includes JSON/HTML, bbox/datetime, stable IDs,
link-driven paging, Queryables, the documented CQL2 text subset and supported
public CRS transformations. Standard-provider eligibility and source protection
remain explicit. Native MVT providers are not raw feature sources; MyISAM tile
sources are not automatically admitted to MySQL feature publication.

MOS startup probes and per-layer/MySQL query timing were adapted into this
implementation without removing strict feature geometry or source guarantees.
The source MOS branch was recorded as merged while retaining the reviewed
integration tree. See [provider contracts](docs/provider-contract.md) and
[tile latency diagnostics](docs/development.md#tile-latency-diagnostics).

Tegola provides a replacement migration path for Jivan's feature-serving role.
Jivan remains independently managed: no upstream archival or modification is
claimed. The optional Lambda adapter shares the assembled HTTP router; local
invocation parity is separate from deployed AWS acceptance.

The source tag and Windows build do not establish OGC certification, publish
GitHub Release assets, or extend fixture-specific evidence to another live
installation. Earlier validation records below retain their dates, revisions
and scopes. See [Feature API release notes](docs/release/feature-api.md),
[Jivan migration](docs/migration/jivan-to-tegola.md) and
[Windows release builds](docs/windows-release.md).

## Tile HTTP caching (fork extension)

The native MOS extension adds transactional geometry/bounds maintenance for
admitted MySQL and GeoPackage-provider raw SQLite tables, and owned custom
`etmerc` horizontal transformations. MySQL 5.5 compatibility requires an explicit
legacy table-identity opt-in; metadata snapshots do not equal a physical table
incarnation proof. These remain fork extensions with provider-specific admission
and runtime evidence, not general PostGIS/MariaDB or vertical-CRS claims. See
[MOS write contracts](docs/geometry-formats.md#mos-writes-with-separate-bounds-columns).

The unreleased WFS/Feature API write profile is a fork extension. It adds
explicit write admission, native provider mutations, conditional revisions and
a limited embedded attribute editor. It does not extend the prior read-only
conformance evidence to WFS-T or Part 4. LockFeature and distributed cache
invalidation remain unavailable; write-enabled routers bypass tile caches and
force no-store. Every reader replica must use the same cache policy for editable
data. See [write scope and operational limits](docs/wfs-scope-limitations.md).

The PR #2 follow-up tightens that profile's input/admission and protocol contracts:
stock CLI production writes fail startup without an authentication adapter;
create requires a generated key, nullability and date-time syntax are validated,
MySQL service schema is prepared before write publication, and receipts preserve
transaction correlation. WFS qualified names, negotiated versions and public
mounted URLs match discovery. MOS read and write evidence is recorded separately
per provider in the [write matrix](docs/provider-matrix.md); native spatial-column
tests do not establish MOS or bounds-backed MapplGIS acceptance. An additional
audit follow-up binds FES literals through Queryables, preserves representation
selection for conditional writes, and carries safe WFS receipt headers. Its
[seven-finding evidence mapping](docs/provider-matrix.md#attached-audit-follow-up)
distinguishes native deadlock and injected acknowledgement tests from unperformed
wire-level fault/restore acceptance.

`webserver.tile_http_max_age` now configures browser/shared-cache freshness for
successful ordinary GET/HEAD tile responses (default `0`, opt-in). The outer tile middleware
applies the same policy after cache lookup/rendering and gzip handling, so cache
hits and misses agree and decompression failures are not cacheable. Query-bearing
requests, maintenance operations, errors, and credential-bearing requests remain
`no-store` when enabled. Existing service-operation `no-store` is not removed.
See [server configuration](server/README.md#tile-http-cache) for TTL and invalidation
semantics. Regression coverage: `server/middleware_tile_http_cache_test.go` and
`config/tile_http_cache_test.go`.

## Version scheme

Fork releases use the scheme `v0.21.0-fork.N`, where the base is upstream `master`
post-v0.21.0 (2024-12-19) and `N` is a monotonically increasing fork revision. The
compiled-in fallbacks (`internal/build.Version` and `server.Version`) carry the string
`v0.21.0-fork.1`; production builds continue to override `build.Version` via
`-ldflags "-X .../internal/build.Version=..."` at release time (see
`.github/workflows/on_release_publish.yml`). The tagged `v0.21.0-fork.2` build injects that exact tag and full Git revision;
plain builds without injection still report the historical `v0.21.0-fork.1` fallback.
The ldflags injection remains the release version authority. Nothing here claims "0.17.0 is the upstream
base"; that older label was inaccurate and has been removed.

## Upstream bugs fixed in this fork

The historical upstream audit identified both items below on its inspected
`master` snapshot and the `v0.21.0` tag. This record does not revalidate the latest
upstream head; recheck it before submitting a pull request. The fork inherited
these defects from `master`; each is fixed here and
is a candidate for an upstream pull request. A minimal reproducing regression test ships
in-tree for each.

| Bug (present on upstream master + v0.21.0 tag) | Minimal repro (in-tree test) | Fork status | Upstream status |
| --- | --- | --- | --- |
| `cache/gcs.Get()` reports every read failure as a cache miss (`return nil, false, nil`), swallowing backend errors. PR [#938](https://github.com/go-spatial/tegola/pull/938) fixed this (merged 2023-08-03) but the fix **regressed on `master`** - the inspected upstream `master` snapshot still contained the bug. | `cache/gcs/gcs_test.go`: transient backend error -> `(nil, false, err)`; missing object -> clean miss. | Fixed in fork. | Candidate for upstream PR (repro test included). Do **not** record this as "released in upstream v0.18/v0.19": the fix was merged, then regressed on `master`. |
| `webserver.HostName` with a malformed value is silently ignored because `url.Parse` soft-parses scheme-less inputs such as `cdn.example.com:443` (parsed as scheme=`cdn.example.com`, Host empty). Present on both the `v0.21.0` tag and `master`. | `internal/env/parse_test.go`: scheme-less `cdn.example.com:443` and a garbage value are rejected at startup. | Fixed in fork. | Candidate for upstream PR (repro test included). Submit upstream **only after** the empty-hostname regression (audit N5) is fixed, so the patch preserves the upstream behaviour "empty `webserver.HostName` = derive the host from the request"; N5 is fixed in this fork (wave 4), so the patch is now upstream-ready. |

## Candidates for upstream PR

Beyond the two bugs in the table above, the audit (`tegola_review_part12.md`, section 0.8;
full report committed in-tree at [`docs/audit/tegola_review_part12.md`](docs/audit/tegola_review_part12.md))
lists the following items as upstream pull-request candidates. Sending them upstream
lowers the future cost of syncing this fork.

* **HANA provider trio**: unchecked `rows.Close()` in `getLayerFields`, `$1` -> `?`
  placeholder style, and `quoteIdentifier` hardening (audit N10/N11/N12). Fixed in the
  fork (part12 wave, `provider/hana/util.go` + `hana.go`); the patch is ready to offer
  upstream.
* **go-spatial/geom WKB decoder**: guard `make(..., num)` allocations against
  stream-supplied element/point counts before allocating (audit N13). The minimal patch
  and a fuzz target live in `third_party/go-spatial/geom/encoding/wkb` in this fork and
  are ready to be offered to `go-spatial/geom`.
* **gzip response decompression**: `gzipDecompressResponseWriter.Write` must buffer the
  compressed body, decode it once at handler completion, and then set `Content-Length`
  (audit N14, `server/middleware_gzip.go`).
* **File cache**: per-call unique temp file (`os.CreateTemp`) instead of the shared
  `destPath + "-tmp"`, and `Purge` must ignore `os.IsNotExist` races on `Remove`.
* **GCS and hostname**: see the table above. The hostname patch may go upstream only
  after the empty-hostname regression (audit N5) is fixed, otherwise the fork's
  regression would be shipped upstream. N5 is fixed in this fork (wave 4).

## Dependency maintenance in the fork

The fork carries a dependency-maintenance layer on top of the upstream base
(2024-12-19). It is recorded here because it changes the sync surface:

* **Go modules** were upgraded in bulk (`go.mod` / `vendor` refresh; e.g.
  `redis/go-redis` v9.22.0). `SAP/go-hdb` was bumped to v1.18.11 and
  `gdey/tbltest` to its final upstream revision; the unused `arolek/p`
  dependency was dropped. Upstream's `require` pins are therefore no longer a
  reliable diff baseline for `go.mod` / `vendor/`.
* **MVT protobuf codegen** migrated from gogo to `google.golang.org/protobuf`
  (`protoc-gen-go` v1.36.12; see `third_party/README.md`). When syncing
  `go-spatial/geom` with upstream, the generated `vector_tile.pb.go` must be
  re-generated with `protoc-gen-go` rather than taken from upstream's gogo
  output (the wire format is unchanged).
* **Built-in viewer (`ui/`)**: `maplibre-gl` upgraded to v5, the deprecated
  `babel-eslint` replaced with `@babel/eslint-parser`, and npm install scripts
  denied by default (`ui/.npmrc` supply-chain hardening).
* The bulk upgrade deliberately stopped short of the cloud-SDK migrations:
  `aws-sdk-go` is still v1 and `azure-storage-blob-go` is still the legacy
  2018 track, so deferred debt 2.3 is unchanged by the refresh.

## Sync methodology

Compare the fork against upstream `master` using the git graph:

- `git merge-base HEAD upstream/master` to find the fork point,
- `git log upstream/master..HEAD` (and `git log HEAD..upstream/master`) to see what the
  fork carries and what upstream has added since.

Upstream does **not** maintain a `CHANGELOG` since 0.17.0; the authoritative sources for
release chronology are **GitHub Releases** and **tags**.

## Linting (CI)

`govet`, `errcheck`, `staticcheck`, `sqlclosecheck`, and `rowserrcheck` are enabled
in `.golangci.yml` (test files are linted as well, `tests: true`). CI is fully
configured in `.github/workflows/on_pr_push.yml` (triggered on push and pull
request): `build_ui`, `test` (Ubuntu, `go test -mod vendor ./...` with a
`CGO_ENABLED` 0/1 matrix and the dockerised test services), `module-modes`,
`docker-build`, `lint` (`gofmt -s` check plus golangci-lint v2.13.2 running
`./...`), and `govulncheck`. Green CI runs, however, cannot be relied on for
this fork (GitHub Actions quota - see `part10 A16` below), so the standing
enforcement policy is **local re-verification on the exact SHA**:
`go test -mod vendor -count=1 ./...` with `CGO_ENABLED=0` and `CGO_ENABLED=1`,
plus `golangci-lint run ./...`. This section and the A16 row state one truth:
the workflow defines *what* is checked, local runs are the *enforcement*.
Two targeted `errcheck` exclusions are recorded here: `(*database/sql.Rows).Close`
and `(io.Closer).Close` (cleanup in tests and deferred probe closes are
uninteresting error paths). The exclusions are safe because the dedicated
`sqlclosecheck` and `rowserrcheck` linters cover the dangerous half of that
space: a `*sql.Rows` that is never closed, closed twice, or iterated past a
silent query error is still reported. The row-`Close`/`Err` hygiene findings
the audit recorded in `provider/hana`, `provider/mysql`, `provider/gpkg`,
`provider/postgis`, and `server/` were fixed in the part12 wave and later
waves; re-verified in part13: every `*sql.Rows.Close()` site in
`provider/hana` and `provider/mysql` is guarded (`defer func() { _ = rows.Close() }()`
or an explicitly checked call), so nothing in those providers is excluded
from the hygiene rules any more. The errcheck exclusions remain only for
uninteresting test/probe cleanup error paths.

Honest limitation: `errcheck` only catches *ignored* (unchecked) errors. It does **not**
catch the "error is checked, then deliberately swallowed as a cache miss" bug class (the
`cache/gcs` pattern). That class is caught by regression tests such as
`cache/gcs/gcs_test.go`, not by lint.

## Fork-specific deferred debt (wave 2)

The table records the current disposition of the original audit items. All
numbered implementation items are closed except the agreed exclusions 2.3
and A16. The closure log below preserves the sequence of fixes; see
[documentation status](docs/maintenance-status.md) for scope and remaining
capability/verification limits.

| Audit ID | Description |
| --- | --- |
| 2.1 | ~~Async metatile regeneration - metatile regeneration currently blocks the HTTP tile request; it should be moved off the request path.~~ — **closed** (wave 2): regeneration moved off the request path onto a bounded single-flight scheduler; `?tile=update` now returns 202. Closure log below. |
| 2.2 | ~~Redis cache: URI-only configuration. The `go-redis` v9 migration is **done** (module `github.com/redis/go-redis/v9`, `cache/redis/redis.go`); the remaining decision is whether to drop the legacy `address` key in favour of `uri` (the legacy key is still accepted as a fallback).~~ — **closed** (wave 2, decision: drop the legacy key): `uri` is the only connection key; legacy keys fail at startup with a migration example. Closure log below. |
| 2.3 | Migrate cloud SDK usage to AWS SDK v2 and the current Azure SDK. |
| 2.4 | ~~Common provider test harness - consolidate duplicated provider test setup (`provider/test/provider.go` TODO).~~ — **closed** (wave 2): shared `provider/test/fixture` harness; the `MVTForLayers` mock TODO is resolved. Closure log below. |
| 2.5 | ~~Logging consolidation on `log/slog`.~~ — **closed** (wave 2): stdlib `slog` TextHandler behind the `internal/log` facade. Closure log below. |
| 2.6 | ~~SQL-context-aware token substitution: `!BBOX!`-style token replacement is plain string interpolation and does not protect SQL strings or comments (`provider/gpkg/util.go`, `provider/geometrycodec/probe.go`); the proper fix is an SQL lexer + parametrization effort.~~ — **closed** (wave 2): context-aware substitution via `internal/sqltoken`. Closure log below. |
| part12 0.6 | ~~HANA and PostGIS probes continue with a zero `Layer` when the configured layer is not found: `log.Warnf` and fall through with an empty value instead of failing the request (upstream behaviour; deferred).~~ — **closed** (wave 2): missing-layer MVT queries fail with `ErrLayerNotFound` before any SQL. Closure log below. |
| part12 0.6b | ~~`provider/gpkg` raw-table SQL relies on the GeoPackage RTree without checking that the RTree entry exists (same as upstream). An existence probe (`SELECT` from `gpkg_contents`/`pg_class`) would change query plans and performance relative to upstream behaviour, so this is tracked as upstream debt instead of a fork fix.~~ — **closed** (wave 2): RTree presence is probed at registration (`sqlite_master`) with a WARN + query-plan fallback. Closure log below. |
| 3.6 | ~~`basic/line.go` simplification correctness: line simplification does not check point intersection ("malformed geoprocessing with providers of type not mvt_postgis", an open upstream bug noted in v0.21.0). Geometry behavior is left unchanged until a test corpus exists.~~ — **closed** (wave 2): intersection-aware Douglas-Peucker in `maths/simplify` with a 25-case corpus (9 pre-fix failures → 0). Closure log below. |
| part10 A15 | ~~External dependency portability - `third_party` `replace` directives complicate out-of-tree consumption.~~ — **closed** (publication follow-up): versioned `alexeydott/geom` and `alexeydott/proj` modules replace the local dependencies, with Tegola migrated to `github.com/alexeydott/tegola`. Closure log below. |
| part10 A16 | Green CI runs are unavailable for this fork (no GitHub Actions quota), so the standing policy is local re-verification on the exact pushed SHA: `go test -mod vendor -count=1 ./...` with `CGO_ENABLED=0` and `CGO_ENABLED=1`, plus `golangci-lint run ./...`. Not a contradiction with "Linting (CI)" above: the workflow defines what is checked, this row records that the runs themselves cannot be relied upon. |
| part13 P6.5 | ~~HANA scale tokens: `!PIXEL_WIDTH!` and `!SCALE_DENOMINATOR!` in `provider/hana/util.go` (`replaceTokens`, TODOs next to the token definitions and the `// TODO: Always convert to meter if we support different projections` note) compute pixel width and scale denominator assuming WebMercator meters and 256x256 tiles regardless of the configured layer CRS; `// TODO: it's currently assumed the tile will always be in WebMercator` is the same debt. Deferred: the fix needs per-CRS scale math and the provider is owned outside this wave. Same debt class as the postgis scale-token work recorded in the part12 audit.~~ — **closed** (wave 2): CRS-aware scale math in `provider/hana/scale.go`. The follow-up below closes the equivalent PostGIS, MySQL and GeoPackage scale-token debt. |

### Wave-2 closure log

Closure details for the wave-2 items struck through above. A16 and 2.3 remain
open by decision and are unchanged.

* **2.6 — SQL-context-aware token substitution.** The providers' `!TOKEN!`
  replacement was plain string interpolation; it is now a shared, lexically
  context-aware substitution (`internal/sqltoken`) used by gpkg, mysql,
  postgis, hana, query-parameter values and geometrycodec probes. Tokens are
  replaced only in SQL code context and preserved verbatim (and un-uppercased)
  inside the strings, identifiers and comments supported by each backend.
  The completion audit replaced the original combined grammar with explicit
  PostgreSQL, MySQL, SQLite and HANA dialects throughout rendering, parameter
  replacement, validation and probes. PostgreSQL arrays/hash operators remain
  executable; ordinary strings and `E'...'` have distinct escaping rules.
  SQLite backslashes and MySQL comment rules are handled separately.
  Detection APIs use the same dialect as substitution. Legacy public wrappers
  preserve their original behavior. PostgreSQL assumes
  `standard_conforming_strings=on`; MySQL assumes default string escaping
  (not `NO_BACKSLASH_ESCAPES` or `ANSI_QUOTES`). Bind-parameter rewrite remains
  out of scope by design.
* **2.5 — Logging consolidation on `log/slog`.** `internal/log` remains the
  single logging facade but is now implemented on stdlib `log/slog`
  (TextHandler) as the one backend, with a `*slog.Logger` accessor for new code
  and nil-writer-safe constructors. Every ad-hoc `log.Printf`/`fmt.Print*`
  diagnostic in first-party code (cache, basic, maths, makevalid/plyg,
  container, draw, cmd/tegola, server, example) was routed through it with
  message texts preserved; level words stay greppable; no new dependencies.
* **part12 0.6 — Missing-layer MVT queries.** HANA and PostGIS `MVTForLayers`
  now fail with a wrapped `ErrLayerNotFound` naming the layer before any SQL is
  issued, instead of `log.Warnf` + falling through with a zero-valued `Layer`.
  DELIBERATE warn→error change. The bounds-contract gate is deliberately
  unchanged: custom SQL omitting the bounds columns still registers with a WARN;
  the two behaviours are kept explicitly distinct in the error message and in
  `TestMVTForLayersMissingLayerVsMissingBoundsContract` (both providers).
* **part12 0.6b — GPKG RTree dependence.** `provider/gpkg` raw-table layers now
  detect the GeoPackage RTree at layer-registration time (one `sqlite_master`
  probe for `rtree_<table>_<column>`; `gpkg_extensions` deliberately not the
  gate) and cache a per-layer query plan: RTree JOIN when present (SQL
  byte-identical to upstream), plain-bbox fallback on the table's bounds columns
  when absent, full-table-scan + mandatory in-memory exact filter when neither
  exists. Fallbacks register with a WARN + `CreateRTreeIndex` hint (DELIBERATE
  change from the fork's previous registration error). The request path keeps
  exactly one SQL round trip per tile request — no per-request probes — pinned
  by `TestTileQueryPlanSelection`.
* **3.6 — Line simplification correctness.** A test corpus
  (`basic/line_simplify_corpus_test.go`) now pins the failure modes (fold-back
  needles, created self-crossings, ring spike-drops, integer truncation) with
  explicit expected-failure markers (9 known failures pre-fix), and
  `maths/simplify` is now intersection-aware: Douglas-Peucker validates
  candidate chords against the replaced sub-polyline (true segment distance,
  deviation + crossing checks), performs a global self-intersection check with a
  recursion/effort safety valve that falls back to the unsimplified input,
  validates the ring closing edge, and `normalizePoints` only removes truly
  collinear-between points. Invariants enforced: no new self-intersections,
  two-sided Hausdorff within tolerance, endpoints/direction preserved,
  monotonicity preserved. Truncation removed from the simplify path (the encoder
  quantizes anyway). A regression at the MVT encoder boundary
  (`maths/simplify/encode_regression_test.go`) asserts decoded tile lines stay
  self-intersection free. 0/25 corpus failures after the fix. (The debt text
  named `basic/line.go`, but the actual simplifier is `maths/simplify`, called
  from `atlas/map.go` `encodeMVTFeature`; `basic/line.go` carried only a TODO
  comment and there is no `basic/ring.go`.)
* **2.1 — Async metatile regeneration.** Metatile regeneration now runs off the
  HTTP request path: `serveTileOperation` schedules the 8x8 render loop on a
  bounded background scheduler (`server/metatile_regen.go`, max 2 concurrent
  regenerations, 18 admitted goroutines, excess shed with 503) with
  single-flight per metatile key, so N concurrent requests trigger at most one
  regeneration (the file cache stampede property is preserved). `?tile=update`
  answers `202 Accepted` immediately (was 204 after a synchronous 64-tile
  rebuild — the one API deviation, documented in the README);
  `?tile=getupdated` answers the requested tile with one synchronous render
  (`writeStable` drops its cache write in favor of the regeneration's fresher
  output) and joins the shared background regen. Failures log at WARN and are
  never negative-cached; shutdown cancels/drains in-flight work via gdcmd LIFO
  before observability/provider cleanup. Zero new config keys. Deterministic
  tests cover single-flight sharing, failure retry, bounded concurrency,
  shutdown, and panic recovery; full suite green including `-race`. Out of
  scope of the original closure: the reported file-cache shared-tempfile race (`cache/file/file.go`)
  — the follow-up below corrects this stale finding.
* **2.2 — Redis cache URI-only.** (Decision: drop the legacy key.) The
  `address`/`network`/`password`/`db`/`ssl` keys are removed; `uri` is the
  single redis cache connection key, parsed with go-redis v9 `ParseURL`
  (`redis://`, `rediss://`, `unix://`), and legacy-key or invalid-uri configs
  fail at registration with a migration-friendly error containing a concrete
  before→after example. See `cache/redis/README.md`.
* **2.4 — Common provider test harness.** Implemented in
  `provider/test/fixture`; duplicated SQL drivers/row conversion, config
  assembly, GPKG database setup, and GPKG/HANA tile mocks migrated and removed.
  `provider/test/provider.go` `MVTForLayers` TODO resolved with a documented
  backward-compatible canned response plus request-aware callback/error
  injection. Helper/mock tests and `provider/test/README.md` added. Existing
  assertions, fixtures, CGO tags and live-DB gates preserved; production
  API/behavior unchanged. Verified vendored build/vet, full CGO=0 and CGO=1
  suites, clean gofmt, and golangci-lint (0 issues). Live DB tests remain
  gated/unrun without credentials.
* **part10 A15 — External dependency portability (bounded mitigation).**
  Inventoried and CI-guarded the two local geom/proj `replace` directives;
  documented and verified an explicit three-module checkout-based consumer
  setup, public geom type compatibility, retained fork behavior, and offline
  consumer vendoring (`ci/check-dependencies`). Fixed standalone geom checksums
  (missing `go.sum` entries from the protobuf migration; the nested module's
  `go` directive raised to the protobuf-required minimum) and enforced
  read-only nested-module verification. **Closed by the publication follow-up:** tagged fork modules are now adopted
  without replacements. This deliberately changes public geom type identity;
  external Go consumers must migrate imports to the alexeydott namespace.
* **part13 P6.5 — HANA scale tokens.** HANA scale tokens now derive pixel
  dimensions from the unbuffered tile extent in the resolved layer CRS and
  actual optional tile pixel dimensions (framework default otherwise).
  Projected CRS units use the projection engine's linear conversion; EPSG:4326
  / planar equivalent initially used a spherical approximation. The later
  ellipsoidal-scale follow-up supersedes it for all providers; the OGC scale
  denominator still uses 0.00028 m/px.
  Unknown CRS/units fail explicitly for executable scale tokens. WebMercator
  values remain byte-exact; `internal/sqltoken` context safety retained;
  obsolete HANA warning/TODO removed. Purely additive
  `basic.ProjectedMetersPerUnit` helper; DB-free math/context/compatibility
  tests and full vendored build/vet/CGO-off+on tests/lint pass. PostGIS's
  remaining scale-token debt and WGS84 geographic `crs_defn`
  engine support were addressed in the follow-up below.

### Completion of newly tracked findings (2026-09-28)

The six findings left after wave 2 were checked against the actual source,
not assumed to remain open from the earlier session notes.

1. **Closed: simplification tolerance units.** `Tile.ZEpislon()` now returns
   `Tolerance * ZRes()` in WebMercator meters. The default ten-unit tolerance
   refers to the 4096-unit MVT coordinate extent, not ten display pixels.
   Regression tests exercise both the numeric conversion and the production
   encode boundary with meter-space geometries.
2. **Closed: unreachable simplification gate.** The default is enabled again;
   `dontsimplifygeo`, per-layer `dont_simplify`, and the maximum zoom still
   disable it. Enabling it exposed a component-topology gap: simplifying a
   shell independently could strand a hole outside it. The subsequent topology follow-up validates ring containment and component
   relationships before accepting simplification of polygons with holes or
   multiple components; unsafe or inconclusive candidates retain the input. Lines and simple shells
   retain the existing self-intersection validation.
3. **Closed: scale tokens outside HANA.** All four providers share
   `provider.TileScale`: unbuffered source-CRS dimensions, optional tile pixel
   size (256x256 default), projected-unit conversion and source-ellipsoid
   geographic scale at the tile center, with the OGC 0.00028 m pixel. HANA planar aliases remain
   supported. Unsupported CRS/unit data errors only when executable scale
   tokens require it; WebMercator defaults remain compatible.
4. **Geographic datum support completed in the Go fork.** The projection fork
   and its vendored copy route `longlat` aliases through the existing
   `datumToWGS84` / `datumFromWGS84` math. Three-/seven-parameter datums in the
   fork's datum table and ellipsoid definitions with explicit `+towgs84`
   use the same datum transformations as projected CRSs; WGS84 identity remains supported.
   Custom ellipsoids can use `+a` with supported `+b`, `+rf`, `+f`, `+es` or
   `+e` parameters, without a conflicting named datum. Unknown datums,
   conflicting datum/ellipsoid combinations and non-WGS84 ellipsoids without
   a defined transformation fail registration. **Remaining fork limitations:** grids,
   non-Greenwich prime meridians, angular-unit and axis conversions. These
   are not general limitations of `crs_defn` or PROJ. The two-dimensional API
   assumes zero input height in each direction, so shifted round trips can
   differ slightly. Scale tokens now use the source ellipsoid
   and local parallel curvature at the transformed source-CRS center latitude. See
   [the CRS contract](docs/crs.md#geographic-proj-definitions).
   Regression tests use independent PROJ 9.5.1 references for three-/seven-parameter
   shifts, a custom ellipsoid, zero shift between different ellipsoids, both
   poles, GGRS87, and synthetic `crs_defn` conversion through WebMercator.
5. **Already fixed; stale finding corrected.** File-cache writes already used
   unique `os.CreateTemp` names in `671a7ea1`, with Windows retry adjustments
   in `769463ea`. The concurrent same-key write and Set/Purge regression tests
   pass. No redundant cache implementation change was made.
6. **Closed: MySQL WKB fixture.** Remove the extra uint64 between X and Y;
   the point is standard 21-byte WKB and tests assert the decoded coordinates,
   rather than only its type and SRID.

A further scheduler regression was fixed: tile status includes accepted
regeneration jobs waiting in the queue, not just jobs already rendering.
An HTTP-level regression checks `updating: true` while a job is queued.

Live PostGIS validation also exposed pre-existing registration-probe failures:
native `!BBOX!` operands were replaced with a boolean, compact zoom comparisons
lost their separator, wrapping a trailing line comment consumed the closing
SQL, and NULL geometry samples aborted type discovery. These paths now have
regression coverage. The PostGIS MVT test checks decoded content rather than
a byte length tied to one PostGIS/GEOS encoder version.

The earlier exclusions remain: cloud-SDK migrations (2.3), reliance on remote
CI quota (A16) are unchanged. A15 is addressed by the publication follow-up below.

### Local verification of the completion changes

The complete code and documentation at `1175119f` passed the vendored full
suite with CGO disabled (1,816 test/subtest passes) and enabled (1,961),
the full race suite, and golangci-lint (zero issues). PostGIS 16 / PostGIS 3.5,
MySQL 8.4 and Redis 7.4 were exercised as live local services. Both nested
fork modules and the independently vendored offline consumer passed; rebuilding
the vendor tree produced no tracked differences.

The compiled Windows server served a real Athens GeoPackage tile containing
6,965 decoded features. HTTP checks covered capabilities, the embedded viewer,
cache MISS/HIT, dirty regeneration, and asynchronous update/status completion.
The viewer assets were built from the lockfile. Twelve tests were skipped:
eight live AWS/Azure tests, three live HANA tests, and one intentionally retired
MySQL runtime-system-info test. These skips are not live-service validation.

For reproducible Windows release flags, source metadata and checksum commands,
see [Windows release builds](docs/windows-release.md). A local release-mode
binary is distinct from publishing a GitHub Release or creating a version tag.

### Published forks, ellipsoidal scale and polygon topology (2026-09-28)

* **A15 closed:** `github.com/alexeydott/geom v0.1.1` and
  `github.com/alexeydott/proj v0.3.1` are standalone versioned forks.
  Tegola's module and imports use `github.com/alexeydott/tegola`; dependencies
  have no local or remote replacements. Public geom types now belong to the
  fork namespace. See [migration and consumer checks](third_party/README.md).
  Historical `third_party/go-spatial` snapshots are retained only for audit
  references; published repositories own active dependency source.
* **Geographic scale:** horizontal meters per longitude degree now use
  `N(phi) * cos(phi) * pi/180`, where `N = a / sqrt(1 - e2*sin(phi)^2)` comes
  from the source CRS ellipsoid. The denominator is horizontal source pixel
  width times this factor divided by 0.00028 m. This is local scale at the
  transformed tile-center latitude, not a finite geodesic or tile diagonal.
* **Complex polygon simplification:** candidate rings must remain simple,
  nondegenerate, retain winding and preserve all containment relationships.
  Crossings, contacts, moved holes, overlapping components, invalid input or
  inconclusive bounded checks return the original geometry. Islands inside
  holes are supported. Safe polygons with holes and MultiPolygons can now
  reduce vertices instead of being unconditionally excluded.

A16 and 2.3 remain excluded. Grid-based datum transformations, non-Greenwich
prime meridians and alternate geographic units/axes are still separate limits
of the Go projection fork; this follow-up does not claim to implement them.

Verification for this follow-up: full root tests with CGO disabled/enabled,
zero-issue lint, and the external consumer with published geom/proj versions
passed. Both standalone fork suites passed; geom was tested with CGO off/on.
The supplied Windows SpatiaLite 5.1.0 extension also passed the actual recorder
test without skips, plus synchronous Point/MultiLineString/Polygon write and
readback with SRID verification. The published Tegola commit `d417e73b` also
passed the `-revision` consumer mode in a fresh module cache, without any
replacements, followed by offline vendored build/test. Its release executable
passed the GeoPackage HTTP/cache/update smoke check (6,965 decoded features).

## Complete Markdown audit and SRID collision follow-up (2026-09-28)

The audit covered all 199 tracked Markdown documents, including provider,
server, cache, Lambda, internal-package and historical guides. The current
[maintenance status](docs/maintenance-status.md) records closed work, the
agreed exclusions A16/2.3 and actual capability/verification boundaries.
Obsolete build, dependency, SQL, scale and endpoint instructions were corrected;
vendored and frozen dependency history was preserved.

A follow-up to synthetic SRID allocation found that collision resolution could
rebind an ID retained by an existing layer. Collisions now fail registration
without changing the registry or projection cache. Explicit registration also
cannot overwrite a synthetic ID owned by another definition. Regression tests
cover both registration orders, idempotence, cached forward/inverse transforms
and the provider layer retaining its original ID; focused race tests pass.

Validation: the full root suite passed with CGO disabled and enabled; lint
reported zero issues. The focused registration/provider tests also passed
with the race detector. Documentation review included local link checks and
an independent final review of the changed contracts.

## MOS startup sampling follow-up (2026-09-28)

Explicit MOS now uses a zero-row metadata query for the custom-SQL contract
in all four standard providers. Geometry-class inference is independent and
is skipped when geometry_type is configured. Automatic MOS inference stops
after three successful nonempty geometries within a 16-row window. Structural
checks and explicit CRS requirements remain active; explicit MOS no longer
receives an inferred sql-sample tag. Regression coverage includes provider
probes, bounded/early-stop inference and SQLite expressions that would fail
if evaluated during explicit MOS plus geometry_type registration.

Validation: full root tests passed with CGO off/on, race tests passed for
all four providers and the shared codec, and lint reported zero issues.

### Short automatic MOS results

A completed probe result with fewer than three valid samples now detects MOS
when at least one nonempty MOS geometry decoded successfully. Exhausting the
16-row window does not qualify as early EOF. Invalid rows in automatically
detected MOS layers no longer prevent class inference or rendering of valid
features. Tests cover one valid row, valid plus invalid in either order, empty
and all-invalid results, budget exhaustion, all provider probe adapters and
real SQLite registration plus feature output. Explicit MOS remains metadata-only
for format inspection.

Validation: full CGO0/1 suites, shared-codec/provider race checks and lint
passed after the short-result change.

## Seed/serve cache interoperability follow-up (2026-09-28)

Layer URLs now reuse a seeded whole-map tile on an individual-layer cache
miss, preserving encoded feature geometry and attributes without provider
queries. Existing layer cache entries retain precedence; derived responses
are not stored with a fresh TTL. Parameterized requests still bypass this
cache path. File caches resolve their root once and log the absolute path.

The supplied MapplVectorTiles warmup/start scripts both use the project root
and mysql-luna.toml; their directories agree. The mismatch was whole-map seed
keys versus index3.html layer URL keys. A real RU-CHE 12/2719/1326 seed followed
by a separate server with cold memory returned HIT for the map and water-poly
layer. Regression tests cover seed-to-file-to-serve, layer aliases, separate
layer precedence, TTL, parameter bypass and working-directory changes.

Validation: full CGO0/1 suites, race checks for server/cache/atlas/seed, and
zero-issue lint passed. The production listener was not restarted during
the isolated live seed/serve check.
