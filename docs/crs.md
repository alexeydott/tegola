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

Source auto-detect depends on the backend:

- **postgis** — `geometry_columns` / spatial metadata for `tablename` layers.
- **gpkg** — `gpkg_contents.srs_id` and the WKB geometry-header srs id.
- **mysql** — `TLayerSystemInfoRec` projection (MOS layers) when no explicit
  provider- or layer-level CRS was configured.
- **hana** — `geom.ST_SRID()` sampled from the geometry column.

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
| gpkg | yes | yes | Explicit config wins over `gpkg_contents.srs_id` and the WKB header srs id. |
| mysql | yes | yes | `TLayerSystemInfoRec.Projection` applies only when no explicit CRS is configured. |
| hana | yes | yes | `crs_defn` requires a raw `geometry_format` (`wkb`/`wkt`/`mos`); native `ST_Geometry` columns use a database-side SRS. Not supported for MVT providers. Round-earth SRSs are used through their planar-equivalent SRIDs (`PLANAR_SRID_OFFSET = 1000000000`). |
