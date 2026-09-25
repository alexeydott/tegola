# CRS Contract

This document describes the unified coordinate reference system (CRS) contract
shared by the standard storage providers: `postgis`, `gpkg`, `mysql` and
`hana`.

The implementation lives in [`provider/crsconfig`](../provider/crsconfig/) and
uses the registration helpers from [`basic/epsg.go`](../basic/epsg.go).

## Config keys

Both keys are accepted at the **provider level** (default for all layers) and
at the **layer level** (override for a single layer):

| Key | Type | Description |
|---|---|---|
| `srid` | int | Numeric spatial reference id of the layer's source CRS (e.g. `3857`, `4326`, `32637`). |
| `crs_defn` | string | A full PROJ.4 definition of the source CRS, e.g. `+proj=longlat +datum=WGS84 +no_defs`. |

When `crs_defn` is present on a config level it **wins over a numeric `srid`
on the same level**. The definition is registered under a *synthetic SRID*
(see below) that flows through the regular reprojection path — no provider
treats it as a database-side SRS.

## Precedence

```
layer crs_defn > layer srid > provider crs_defn > provider srid >
source auto-detect > defaults
```

Source auto-detect is provider-specific and shared across all standard
providers (not just MySQL):

- **postgis** — `geometry_columns` / spatial metadata for `tablename` layers;
  MOS layers carry no native spatial metadata, so their CRS comes from the
  `MapplGIS LayerInfo` projection (below). Concretely: for a plain table
  layer (no `sql`), native geometry format, when neither the provider nor
  the layer has an explicit `srid`/`crs_defn`, Tegola runs
  `Find_SRID('<schema>', '<table>', '<geom field>')` (schema defaults to
  `public`) and uses the result as the layer source SRID. If the lookup
  fails — unknown table/column, unregistered or mixed SRID — provider
  creation fails with a controlled error advising an explicit `srid` or
  `crs_defn`; it does **not** silently fall back to 3857. Custom-SQL layers
  and raw geometry formats (`wkb`/`wkt`/`mos`) have no single table column
  to introspect, so they keep the documented defaults.
- **gpkg** — `gpkg_contents.srs_id` for `tablename` layers; for custom-SQL
  layers the srs id of the sampled row's GeoPackage binary header is used as
  a fallback (only when no explicit provider `srid` is configured). Raw
  format tables (`wkb`/`wkt`/`mos`) carry no GeoPackage metadata, so the MOS
  system-info projection applies there too.
- **mysql** — `MapplGIS LayerInfo` projection (MOS layers).
- **hana** — `geom.ST_SRID()` sampled from the geometry column; raw-format
  MOS layers use the system-info projection (see
  [geometry-formats.md](geometry-formats.md)).

Bounds-backed MOS custom SQL builds its `!BBOX!` predicate in the layer's
source CRS and then converts the tile extent into the quantized raw MOS
units (floor/ceil scaling by `10^precision / unit factor`). Unlike a
MapplGIS table, SQL sample detection never decodes a
`MapplGIS LayerInfo projection`, so a `mos` custom-SQL layer must configure
its source CRS (`srid` or `crs_defn`) explicitly.

The `MapplGIS LayerInfo projection` blob is the shared source-derived CRS for
layers of **every** standard provider whose table was identified as a
MapplGIS table at registration (structural detection: DDL + primary key +
required indexes + an `OKEY = 1` probe row; custom `sql` layers are never
auto-detected and never apply system info from result rows). When no explicit
`srid`/`crs_defn` is configured, the projection is registered
as a synthetic SRID through the same mechanism as `crs_defn` (see
[`provider/crsconfig.ApplySystemInfoCRS`](../provider/crsconfig/crsconfig.go)).
Provider-native metadata is consulted only in the provider's native geometry
paths; raw-format layers always fall back to the system-info projection.

An explicit `srid` or `crs_defn` (at either level) always suppresses the
source-inferred value.

## Synthetic SRIDs

SRIDs at or above `basic.SyntheticSRIDMin` (340000001) are reserved for
Tegola-internal synthetic codes allocated by `basic.RegisterProj4Defn` for
`crs_defn` values. They exist only inside the Tegola process:

- never send them to a database spatial API (`ST_SetSRID`, `NEW ST_POINT(.., srid)`,
  round-earth checks, and so on);
- never configure them as a plain numeric `srid`;
- reprojection of features out of a synthetic CRS happens on the client side
  via the registered PROJ.4 definition.

## Built-in CRS registry

The fork ships a built-in registry of commonly used projected CRSs
(`basic.RegisterBuiltinProj4SRIDs`, `basic/epsg.go`), so `srid: 32637` works
even when the backend itself has no such database SRS registered. The registry
is backend-independent and shared by all providers listed above.

## `!BBOX!` semantics

The `!BBOX!` token is always evaluated **in the layer's source CRS**:

- providers that can push spatial predicates to the database convert the tile
  extent from Web Mercator into the source CRS first (e.g. PostGIS
  `ST_SetSRID(geom, 0) && !BBOX!`, HANA `ST_IntersectsRectPlanar`);
- providers/geometry formats that cannot (MOS blobs, raw WKB/WKT columns,
  synthetic CRSs on HANA) replace `!BBOX!` with a no-op and apply an exact
  in-memory bbox filter after decoding.

## Provider support matrix

| Provider | `srid` | `crs_defn` | Notes |
|---|---|---|---|
| postgis | yes | yes | Synthetic CRS via `ST_SetSRID(geom, 0) && !BBOX!` (no database SRS needed). |
| gpkg | yes | yes | Explicit config wins over `gpkg_contents.srs_id` (table layers) and the sampled header srs id (custom-SQL layers). |
| mysql | yes | yes | `MapplGIS LayerInfo projection` applies only when no explicit CRS is configured. |
| hana | yes | yes | `crs_defn` requires a raw `geometry_format` (`wkb`/`wkt`/`mos`); native `ST_Geometry` columns use a database-side SRS. Not supported for MVT providers. Round-earth SRSs are used through their planar-equivalent SRIDs (`PLANAR_SRID_OFFSET = 1000000000`). |
