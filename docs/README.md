# Documentation index

This fork's documentation lives in the following places:

## Architecture and contracts

* [crs.md](crs.md) — CRS resolution order, `srid` / `crs_defn`, synthetic
  internal SRIDs (>= 340000001), per-provider auto-detection and
  `!BBOX!` reprojection.
* [provider-contract.md](provider-contract.md) — the configuration keys and
  runtime semantics shared by all standard providers (`mysql`, `gpkg`,
  `postgis`, `hana`), plus the per-provider support matrix.
* [geometry-formats.md](geometry-formats.md) — the `geometry_format` /
  `mos_precision` / `mos_units` options and the WKB, WKT and packed MOS
  payloads.

## Providers

* [provider/gpkg/README.md](../provider/gpkg/README.md) — GeoPackage
  provider (native binary, raw tables, RTree and bounds columns).
* [provider/postgis/README.md](../provider/postgis/README.md) — PostGIS
  provider and the `mvt_postgis` MVT variant.
* [provider/mysql/README.md](../provider/mysql/README.md) — MySQL /
  MariaDB provider.
* [provider/hana/README.md](../provider/hana/README.md) — HANA provider
  and the `mvt_hana` MVT variant.

## Packages

* `provider/geometrycodec` (see its `doc.go`) — shared geometry-format
  decoding and MOS configuration.
* `provider/crsconfig` (see its `doc.go`) — CRS resolution and synthetic
  SRID registration.

## Caches

Each cache back end documents itself under `cache/<backend>/README.md`:
[file](../cache/file/README.md), [s3](../cache/s3/README.md),
[azblob](../cache/azblob/README.md), [redis](../cache/redis/README.md),
[gcs](../cache/gcs/README.md), [memory](../cache/memory/README.md) and
[multilevel](../cache/multilevel/README.md).

## Third-party code

* [third_party/README.md](../third_party/README.md) — vendored /
  replaced low-level packages (`geom`, `maths/makevalid`, proj bindings)
  and the constraints on modifying them.

## Contributing

* [CONTRIBUTING.md](../CONTRIBUTING.md) — building, testing and the
  contribution process for this fork.
* [SECURITY.md](../SECURITY.md) — how to report security issues.
* [CHANGELOG.md](../CHANGELOG.md) — release history, including the
  fork-specific `Unreleased` section.
