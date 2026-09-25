# Upstream provenance and deferred debt

This fork tracks the upstream [go-spatial/tegola](https://github.com/go-spatial/tegola)
project. This file records what has been back-ported from upstream, the deliberate
change to the versioning scheme, and the technical debt that has been deferred to a
later wave.

## Version scheme

Fork releases use `v0.17.0-fork.N` where `0.17.0` is the upstream base version this
fork is built on and `N` is a monotonically increasing fork revision. The compiled-in
defaults (`internal/build.Version` and `server.Version`) carry the fallback string
`v0.17.0-fork.1`; production builds continue to override `build.Version` via
`-ldflags "-X .../internal/build.Version=..."` at release time (see
`.github/workflows/on_release_publish.yml`). Only the fallback value and comments
were changed — the ldflags injection is preserved.

## Ported from upstream

| Upstream change | PR / source | Ported on | Status |
| --- | --- | --- | --- |
| `cache/gcs`: `Get()` dropped read errors and reported every failure as a cache miss (`nil, false, nil`). Fixed to return backend errors, treating `storage.ErrObjectNotExist` as a clean miss. | [go-spatial/tegola#938](https://github.com/go-spatial/tegola/pull/938) (released upstream in v0.18 / v0.19) | 2026-09-25 | Ported (this wave). Regression covered by `cache/gcs/gcs_test.go`. |
| `webserver.HostName` malformed values were silently ignored because `url.Parse` soft-parses inputs such as `cdn.example.com:443`. Fixed with strict `host[:port]` / URL validation that fails startup on a bad value. | upstream `malformed webserver.HostName handling` fix (v0.21) | 2026-09-25 | Ported (this wave). Regression covered by `internal/env/parse_test.go`. |

## Fork-specific deferred debt (wave 2)

The following items were identified in the fork-vs-upstream audit but are intentionally
**not** addressed in this wave. They are tracked here so they are not forgotten.

| Audit ID | Description |
| --- | --- |
| 2.1 | Async metatile regeneration — metatile regeneration currently blocks the HTTP tile request; it should be moved off the request path. |
| 2.2 | Redis cache: move to URI-only configuration and migrate `go-redis` to v9. |
| 2.3 | Migrate cloud SDK usage to AWS SDK v2 and the current Azure SDK. |
| 2.4 | Common provider test harness — consolidate duplicated provider test setup (`provider/test/provider.go` TODO). |
| 2.5 | Logging consolidation on `log/slog`. |
| 2.6 | SQL token lexer for GPKG custom SQL (replace string-interpolation-based query assembly). |
| 3.6 | `basic/line.go` simplification correctness: line simplification does not check point intersection ("malformed geoprocessing with providers of type not mvt_postgis", an open upstream bug noted in v0.21.0). Geometry behavior is left unchanged until a test corpus exists. |
| part10 A15 | External dependency portability — `third_party` `replace` directives complicate out-of-tree consumption. |
| part10 A16 | CI green-status verification for the fork's full matrix. |
