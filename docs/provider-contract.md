# Provider contract

This document describes the configuration keys and runtime semantics that are
common to all standard (raw-feature) data providers: `mysql`, `gpkg`,
`postgis` and `hana`. Provider-specific READMEs only describe backend
differences; when a provider deviates from this contract it is called out
explicitly in the matrix below.

MVT providers (`mvt_*`, e.g. `mvt_postgis`) encode the tile inside the
database (e.g. `ST_AsMVT`) and intentionally do **not** participate in the
raw geometry format / MOS parts of this contract.

## Common provider-level keys

| Key | Type | Meaning |
|---|---|---|
| `name` | string | Provider name referenced from map layers. Required. |
| `srid` | int | Default SRID for all layers of the provider. |
| `crs_defn` | string | Full PROJ.4 definition used instead of a numeric `srid`. Wins over `srid` at the same level. See [crs.md](crs.md). |
| `geometry_format` | string | Default geometry format for layers that do not override it: `wkb`, `wkt` or `mos`. Empty/unset means the provider's native geometry handling. |
| `mos_precision` | int | Decimal digits carried by MOS coordinates (see below). Only applies when `geometry_format = "mos"`. |
| `mos_units` | string | Packed linear unit of MOS coordinates: `mm`, `cm`, `dm`, `m` or `km`. Only applies when `geometry_format = "mos"`. |

## Common layer-level keys

| Key | Type | Meaning |
|---|---|---|
| `name` | string | Layer name used by map layers. Required. |
| `tablename` | string | Table to query. Mutually exclusive with `sql`. |
| `sql` | string | Custom SQL. Mutually exclusive with `tablename`. Supports `!BBOX!`, `!ZOOM!`, `!X!`, `!Y!`, `!Z!`, `!SCALE_DENOMINATOR!`, `!PIXEL_WIDTH!`, `!PIXEL_HEIGHT!`, `!ID_FIELD!`, `!GEOM_FIELD!`, `!GEOM_TYPE!` (token support varies slightly per provider; unknown tokens are rejected). |
| `geometry_fieldname` | string | Geometry column. Defaults to `geom` for generated table SQL. For custom SQL the column must be present in the result set. |
| `id_fieldname` | string | Feature id column. Defaults: `fid` for `mysql`/`gpkg`, empty for `postgis`/`hana`. |
| `geometry_type` | string | Skips startup geometry-type inspection. Valid values: `Point`, `LineString`, `Polygon`, `MultiPoint`, `MultiLineString`, `MultiPolygon`, `GeometryCollection`. |
| `srid` / `crs_defn` | int / string | Layer CRS override; see [crs.md](crs.md). |
| `geometry_format` | string | Layer-level geometry format override. |
| `mos_precision` / `mos_units` | int / string | Layer-level MOS overrides (only with `mos`). |
| `fields` | []string | Additional fields to include for generated table SQL. Empty means all columns. |

## CRS

The CRS resolution order is identical for every standard provider:

1. Layer `crs_defn`
2. Layer `srid`
3. Provider `crs_defn`
4. Provider `srid` (if explicitly configured)
5. Source-derived SRID (database metadata, geometry header or — for MySQL MOS
   layers — the `TLayerSystemInfoRec.Projection` blob)
6. Provider default (usually 3857)

Built-in projected SRIDs and custom PROJ.4 definitions are documented in
[crs.md](crs.md).

## `tablename` vs `sql`

Both keys are mutually exclusive in every standard provider: specifying both
is a startup error, as is specifying neither. This is enforced by key
presence, so an explicit `tablename` that happens to equal the layer name is
still detected.

## Startup inspection and deferred layers

At registration every layer is inspected to resolve its geometry type and
(electronically where possible) its SRID:

- **Table layers** are inspected via database metadata / a sample query.
- **Custom SQL** is sampled with a token-normalized variant of the query
  (`!BBOX!` widened, `!ZOOM!` replaced with all zooms). If the query currently
  returns no rows, the layer is still registered (without an inferred
  geometry type) so the server can start; it begins serving once the query
  returns data.
- **Tile-dependent SQL** (SQL whose shape changes with the tile, e.g. an
  embedded `!BBOX!` that cannot be normalized) defers inspection with a
  warning.

## `!BBOX!` semantics

`!BBOX!` is always expanded into the layer SRID: tile bounds are converted
from Web Mercator into the layer CRS before the query is executed, so the
filter, the data and the MVT encoding agree on one CRS (see
[crs.md](crs.md) for the conversion details and synthetic-SRID rules).

| Format | SQL-side filter |
|---|---|
| native geometry | Provider spatial predicate (`&&`, `ST_Intersects`, MBR, …) |
| `wkb` / `wkt` / `mos` | No native spatial predicate (raw values are not database geometries); the provider either uses indexed bounds columns (MySQL-style `MINX/MAXX/MINY/MAXY` for MOS) or no SQL filter at all, and applies an exact in-memory bounding-box check on the decoded geometry in all cases |

## System info auto-configuration (MOS)

MOS tables written by MapplBase carry a `TLayerSystemInfoRec` metadata blob.
During startup inspection (up to 16 rows sampled) the provider reads:

- `Precision` → `mos_precision` (when not explicitly configured),
- `Projection` → layer PROJ.4 definition registered as a synthetic SRID (same
  mechanism as `crs_defn`, only used when no `srid`/`crs_defn` is configured),
- `MapUnits` / `flMapUnitsDefined` → `mos_units` metres factor.

Explicit configuration always wins over system info. Rows that carry the blob
are skipped as feature rows.

## Provider support matrix

| Capability | mysql | gpkg | postgis | hana |
|---|---|---|---|---|
| `srid` / `crs_defn` | yes | yes | yes | yes (synthetic CRS requires a raw format, see below) |
| `geometry_format` (`wkb`/`wkt`/`mos`) | yes | yes (`mos` skips the GeoPackage binary header) | yes | yes |
| `mos_precision` / `mos_units` | yes | yes | yes | yes |
| system info auto-config | yes | yes | yes | yes |
| native spatial filter | yes (MBR/indexed bounds) | yes (RTree index; skipped for `mos`) | yes (`&&`) | yes (`ST_IntersectsRect*`; skipped for `mos` and synthetic CRS) |
| `id_fieldname` default | `fid` | `fid` | empty | empty |
| MVT variant | — | — | `mvt_postgis` | — |

HANA notes:

- HANA distinguishes round-earth and planar SRS; a round-earth SRS is queried
  through its planar equivalent internally (`PLANAR_SRID_OFFSET = 1000000000`).
- A synthetic CRS (from `crs_defn` or system-info `Projection`) cannot be used
  with HANA's native `ST_Geometry` handling: the provider requires
  `geometry_format` = `wkb`, `wkt` or `mos` for such layers. MVT providers do
  not support synthetic CRS at all.
- Planar-equivalent SRIDs (`1000000000 + n`) are internal and must not be
  confused with synthetic Tegola SRIDs (`>= 340000001`).
