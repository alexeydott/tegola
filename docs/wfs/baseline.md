# W01 baseline receipt — WFS / Part 4 program

Date: 2026-10-04
Base commit: `54f93b3100df2b4ab94ca45f9401c891f79505dc` (origin/master)
Branch: `wfs/wfst-part4`
Toolchain: go1.26.7 linux/amd64, CGO_ENABLED=1 (mattn/go-sqlite3)

## Build

`go build ./...` passes at the base commit (with GOCACHE relocated off the
512M /tmp tmpfs; see environment note below).

## Existing behavior preserved (pre-change)

- OGC API Features Parts 1–3 read-only endpoints unchanged.
- MVT tile serving unchanged.
- No WFS / Transaction / Part 4 endpoints exist.
- Write path does not exist anywhere in the tree.

## Environment notes

- `/tmp` is a 512M tmpfs that fills during Go builds. GOCACHE is pinned to
  `/home/hatch/go-build-cache` via `go env -w`; set `TMPDIR=/home/hatch/go-tmp`
  for build commands.
- No PostgreSQL / MySQL / HANA servers in this environment. The only
  live-testable native backend is GeoPackage (SQLite via CGO).
  See ADR-0014 for the resulting sequencing decision.

## Regression fixtures referenced

- `provider/gpkg` package suite (read path, filters, CRS).
- `ogc/features` package suite (GeoJSON, CQL2, CRS, conformance).
- `server` package suite (items protocol incl. B1/B2/B3 fixes).
- Natural Earth GeoPackage: `~/workspace/projects/tegola-test/data/packages/natural_earth_vector.gpkg`.

These suites must keep passing after every WFS commit (W47 regression).
