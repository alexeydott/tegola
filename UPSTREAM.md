# Upstream provenance, fork fixes, and deferred debt

This fork is built on the upstream [go-spatial/tegola](https://github.com/go-spatial/tegola)
project. It was forked from upstream `master` after the v0.21.0 release (2024-12-19):
the fork base is upstream `master` post-v0.21.0, and releases use the version scheme
`v0.21.0-fork.N`. (Upstream does not maintain a CHANGELOG past 0.17.0 - see
[Sync methodology](#sync-methodology).)
This file records upstream provenance, the bugs fixed in the fork that are candidates for
upstream PRs, the versioning scheme, the sync methodology, and the technical debt deferred
to a later wave.

## Version scheme

Fork releases use the scheme `v0.21.0-fork.N`, where the base is upstream `master`
post-v0.21.0 (2024-12-19) and `N` is a monotonically increasing fork revision. The
compiled-in fallbacks (`internal/build.Version` and `server.Version`) carry the string
`v0.21.0-fork.1`; production builds continue to override `build.Version` via
`-ldflags "-X .../internal/build.Version=..."` at release time (see
`.github/workflows/on_release_publish.yml`). Only the fallback value and comments
changed - the ldflags injection is preserved. Nothing here claims "0.17.0 is the upstream
base"; that older label was inaccurate and has been removed.

## Upstream bugs fixed in this fork

Both items below are live bugs on upstream `master` today - identical on the `v0.21.0`
tag and on current `master`. The fork inherited them from `master`; each is fixed here and
is a candidate for an upstream pull request. A minimal reproducing regression test ships
in-tree for each.

| Bug (present on upstream master + v0.21.0 tag) | Minimal repro (in-tree test) | Fork status | Upstream status |
| --- | --- | --- | --- |
| `cache/gcs.Get()` reports every read failure as a cache miss (`return nil, false, nil`), swallowing backend errors. PR [#938](https://github.com/go-spatial/tegola/pull/938) fixed this (merged 2023-08-03) but the fix **regressed on `master`** - the bug is live on `master` today. | `cache/gcs/gcs_test.go`: transient backend error -> `(nil, false, err)`; missing object -> clean miss. | Fixed in fork. | Candidate for upstream PR (repro test included). Do **not** record this as "released in upstream v0.18/v0.19": the fix was merged, then regressed on `master`. |
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

## Sync methodology

Compare the fork against upstream `master` using the git graph:

- `git merge-base HEAD upstream/master` to find the fork point,
- `git log upstream/master..HEAD` (and `git log HEAD..upstream/master`) to see what the
  fork carries and what upstream has added since.

Upstream does **not** maintain a `CHANGELOG` since 0.17.0; the authoritative sources for
release chronology are **GitHub Releases** and **tags**.

## Linting (CI)

`govet`, `errcheck`, `staticcheck`, `sqlclosecheck`, and `rowserrcheck` are enabled
in `.golangci.yml` (test files are linted as well, `tests: true`); CI runs
golangci-lint v2.13.2 via the `lint` job in `.github/workflows/on_pr_push.yml`.
Two targeted `errcheck` exclusions are recorded here: `(*database/sql.Rows).Close`
and `(io.Closer).Close` (cleanup in tests and deferred probe closes are
uninteresting error paths). The exclusions are safe because the dedicated
`sqlclosecheck` and `rowserrcheck` linters cover the dangerous half of that
space: a `*sql.Rows` that is never closed, closed twice, or iterated past a
silent query error is still reported. The row-`Close`/`Err` hygiene findings
the audit recorded in `provider/hana`, `provider/mysql`, `provider/gpkg`,
`provider/postgis`, and `server/` were all fixed in the part12 wave; the
exclusions remain only for uninteresting test/probe cleanup error paths.

Honest limitation: `errcheck` only catches *ignored* (unchecked) errors. It does **not**
catch the "error is checked, then deliberately swallowed as a cache miss" bug class (the
`cache/gcs` pattern). That class is caught by regression tests such as
`cache/gcs/gcs_test.go`, not by lint.

## Fork-specific deferred debt (wave 2)

The following items were identified in the fork-vs-upstream audit but are intentionally
**not** addressed in this wave. They are tracked here so they are not forgotten.

| Audit ID | Description |
| --- | --- |
| 2.1 | Async metatile regeneration - metatile regeneration currently blocks the HTTP tile request; it should be moved off the request path. |
| 2.2 | Redis cache: URI-only configuration. The `go-redis` v9 migration is **done** (module `github.com/redis/go-redis/v9`, `cache/redis/redis.go`); the remaining decision is whether to drop the legacy `address` key in favour of `uri` (the legacy key is still accepted as a fallback). |
| 2.3 | Migrate cloud SDK usage to AWS SDK v2 and the current Azure SDK. |
| 2.4 | Common provider test harness - consolidate duplicated provider test setup (`provider/test/provider.go` TODO). |
| 2.5 | Logging consolidation on `log/slog`. |
| 2.6 | SQL-context-aware token substitution: `!BBOX!`-style token replacement is plain string interpolation and does not protect SQL strings or comments (`provider/gpkg/util.go`, `provider/geometrycodec/probe.go`); the proper fix is an SQL lexer + parametrization effort. |
| part12 0.6 | HANA and PostGIS probes continue with a zero `Layer` when the configured layer is not found: `log.Warnf` and fall through with an empty value instead of failing the request (upstream behaviour; deferred). |
| part12 0.6b | `provider/gpkg` raw-table SQL relies on the GeoPackage RTree without checking that the RTree entry exists (same as upstream). An existence probe (`SELECT` from `gpkg_contents`/`pg_class`) would change query plans and performance relative to upstream behaviour, so this is tracked as upstream debt instead of a fork fix. |
| 3.6 | `basic/line.go` simplification correctness: line simplification does not check point intersection ("malformed geoprocessing with providers of type not mvt_postgis", an open upstream bug noted in v0.21.0). Geometry behavior is left unchanged until a test corpus exists. |
| part10 A15 | External dependency portability - `third_party` `replace` directives complicate out-of-tree consumption. |
| part10 A16 | CI green-status runs are unavailable for this fork (no GitHub Actions quota), so the standing policy is local re-verification on the exact pushed SHA: `go test -mod vendor -count=1 ./...` with `CGO_ENABLED=0` and `CGO_ENABLED=1`, plus `golangci-lint run ./...`. |
