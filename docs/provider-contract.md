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
| `geometry_format` | string | Default geometry format for layers that do not override it: `wkb`, `wkt` or `mos`. Empty/unset means the provider's native geometry handling. Provider-specific values exist: `mysql` accepts `auto`, `mysql` and `mariadb` in addition to the shared formats; `gpkg` accepts `gpkg` (its native GeoPackage binary). |
| `mos_precision` | int | Decimal digits carried by MOS coordinates (see below). Only applies when `geometry_format = "mos"`. |
| `mos_units` | string | Packed linear unit of MOS coordinates: `mm`, `cm`, `dm`, `m` or `km`. Only applies when `geometry_format = "mos"`. |
| `bbox_minx_fieldname` | string | Bounds column holding the feature minimum X, used by bounds-backed MOS SQL. Default `MINX`. |
| `bbox_maxx_fieldname` | string | Bounds column holding the feature maximum X. Default `MAXX`. |
| `bbox_miny_fieldname` | string | Bounds column holding the feature minimum Y. Default `MINY`. |
| `bbox_maxy_fieldname` | string | Bounds column holding the feature maximum Y. Default `MAXY`. |

## Common layer-level keys

| Key | Type | Meaning |
|---|---|---|
| `name` | string | Layer name used by map layers. Required. |
| `tablename` | string | Table to query. Mutually exclusive with `sql`. |
| `sql` | string | Custom SQL. Mutually exclusive with `tablename`. Supports `!BBOX!`, `!ZOOM!`, `!X!`, `!Y!`, `!Z!`, `!SCALE_DENOMINATOR!`, `!PIXEL_WIDTH!`, `!PIXEL_HEIGHT!`, `!ID_FIELD!`, `!GEOM_FIELD!`, `!GEOM_TYPE!` (token support varies slightly per provider; unknown tokens are rejected). |
| `geometry_fieldname` | string | Geometry column. Defaults to `geom` for generated table SQL. For custom SQL the column must be present in the result set; an empty `geometry_fieldname` means the geometry column is the last column of the result set. |
| `id_fieldname` | string | Feature id column. Defaults: `fid` for `mysql`/`gpkg`, empty for `postgis`/`hana`. |
| `geometry_type` | string | Explicit layer geometry type, valid for every standard provider: `Point`, `LineString`, `Polygon`, `MultiPoint`, `MultiLineString`, `MultiPolygon`, `GeometryCollection`. An explicit value fixes the layer geometry type and skips geometry-class inference and the >=3-sample-row requirement (empty data is allowed); structural validation of custom SQL still runs. It is orthogonal to MapplGIS table detection: a `tablename` layer is still checked for the MapplGIS signature and still receives system-info configuration when detected. Mixed content is permitted: features whose decoded type differs from the declared value are rendered, and the mismatch is logged once per layer. |
| `srid` / `crs_defn` | int / string | Layer CRS override; see [crs.md](crs.md). |
| `geometry_format` | string | Layer-level geometry format override. |
| `mos_precision` / `mos_units` | int / string | Layer-level MOS overrides (only with `mos`). |
| `bbox_*_fieldname` | string | Layer-level bounds column overrides (`bbox_minx_fieldname`, `bbox_maxx_fieldname`, `bbox_miny_fieldname`, `bbox_maxy_fieldname`). Values must be simple identifiers: trimmed, qualified names like `t.MINX` rejected, duplicates rejected case-insensitively. Resolution is per field: layer > provider > defaults (`MINX`/`MAXX`/`MINY`/`MAXY`); for joins use a CTE or derived table with unambiguous bounds column names. Resolved columns are excluded from feature tags. Only used by bounds-backed MOS SQL (see [geometry-formats.md](geometry-formats.md)). |
| `fields` | []string | Additional fields to include for generated table SQL. Semantics of an absent or empty `fields` differ per provider and cannot be mixed freely (see matrix below). |

## CRS

The CRS resolution order is identical for every standard provider:

1. Layer `crs_defn`
2. Layer `srid`
3. Provider `crs_defn`
4. Provider `srid` (if explicitly configured)
5. Source-derived SRID (database metadata, geometry header or — for MOS
   layers of any standard provider — the `MapplGIS LayerInfo projection`
   blob)
6. Provider default (usually 3857)

Provider-native metadata is consulted only in the provider's native geometry
paths; raw-format layers always fall back to the system-info projection (see
[crs.md](crs.md) for the per-provider auto-detect details).

Built-in projected SRIDs and custom PROJ.4 definitions are documented in
[crs.md](crs.md).

## `tablename` vs `sql`

Both keys are mutually exclusive in every standard provider: specifying both
is a startup error, as is specifying neither. This is enforced by key
presence, so an explicit `tablename` that happens to equal the layer name is
still detected.

## Startup inspection and deferred layers

At registration every layer is structurally validated and inspected to
resolve its geometry type and, where possible, its SRID. Structural
validation for custom SQL always runs at registration and failing it is a
startup error (see [geometry-formats.md](geometry-formats.md)): `mos`
requires the geometry column, the four configured bounds columns and the
`!BBOX!` token; custom `geometry_format = "gpkg"` carries the same contract
with the bounds-source-CRS predicate; `wkb` / `wkt` forbid `!BBOX!` and
bounds columns. An explicit `geometry_type` never skips structural
validation — it skips only geometry-class inference and the
>=3-sample-row requirement (explicitly typed layers may have empty data).

- **Table layers** are inspected via database metadata / a sample query.
- **Custom SQL** is sampled by the registration probe, which always executes
  the SQL without a spatial filter: `!BBOX!` is neutralized to `1=1`,
  position / zoom tokens are permissive, and the probe is capped at 16
  sample rows. If the query currently returns no rows, the layer is still
  registered (without an inferred geometry type) so the server can start;
  it begins serving once the query returns data.

## `!BBOX!` semantics

`!BBOX!` is always expanded into the layer SRID: tile bounds are converted
from Web Mercator into the layer CRS before the query is executed, so the
filter, the data and the MVT encoding agree on one CRS (see
[crs.md](crs.md) for the conversion details and synthetic-SRID rules).

| Format | SQL-side filter |
|---|---|
| native geometry | Provider spatial predicate (`&&`, `ST_Intersects`, MBR, …) |
| `wkb` / `wkt` | No native spatial predicate (raw values are not database geometries); the exact in-memory bounding-box check on the decoded geometry is applied in all cases |
| `mos` | Bounds-backed SQL: the `!BBOX!` token expands into the indexed bounds-columns predicate (`bbox_*_fieldname`, defaults `MINX/MAXX/MINY/MAXY`) scaled by the layer's MOS quantization; the exact in-memory bounding-box check remains authoritative. Custom MOS SQL must carry `!BBOX!`/`!BOX!` |

MapplGIS detection distinguishes how a layer was identified: `table-canonical`
(structural DDL + PK + indexes + probe row, guarantees system info) vs
`sql-sample` (custom SQL whose sample carries the four bounds columns and
decodable MOS rows — no system info guarantee). MOS custom SQL requires an
explicit `srid` or `crs_defn` (a missing CRS is a startup error);
`mos_precision` and `mos_units` are optional with the normative paired
defaults (see [geometry-formats.md](geometry-formats.md#mos-quantization)).

## System info auto-configuration (MapplGIS tables only)

MOS tables written by MapplGIS carry a `MapplGIS LayerInfo` metadata blob.
Every standard provider identifies such tables with the same one-time,
registration-time detector — the detection is **structural**, based on the
table's DDL, its indexes and a single probe row, never on scanning result
rows or sample windows:

1. The registered `tablename` is inspected (case-insensitive) for all nine
   required columns: `OKEY`, `MUID`, `MINX`, `MAXX`, `MINY`, `MAXY`,
   `ObjectStyle`, `ObjectType`, `LINE`.
2. `OKEY` must be the table's primary key.
3. All six required indexes must exist over `MUID`, `MINX`, `MAXX`, `MINY`,
   `MAXY`, `ObjectType`.
4. A single point probe (`OKEY = 1 AND LINE IS NOT NULL`) must return a
   decodable `LayerSystemInfo` blob.

Only when all four conditions hold does the layer get `IsMapplGIS=true`,
applied exactly once during registration. From the decoded system info the
provider reads:

- `Precision` → `mos_precision` (when not explicitly configured),
- `Projection` → layer PROJ.4 definition registered as a synthetic SRID (same
  mechanism as `crs_defn`, only used when no `srid`/`crs_defn` is configured),
- `MapUnits` / `flMapUnitsDefined` → `mos_units` metres factor.

Explicit configuration always wins over system info.

**Table-canonical MapplGIS detection never applies to custom `sql` layers,
and `LayerSystemInfo` is never applied from result rows.** Custom `sql`
layers are still subject to `sql-sample` storage detection (above), which
tags the layer `MapplGIS` without system info. A SQL layer that uses
`geometry_format = "mos"` must configure `srid` or `crs_defn` explicitly in
its config (a missing CRS is a startup error); `mos_precision` and
`mos_units` are optional with the normative paired defaults. A decodable
blob that happens to appear in the result set is ignored (the row is still
rendered as a feature if its geometry decodes, otherwise it is skipped).

Because detection happens once at registration, tile requests do not repeat
it: after `NewTileProvider` returns, the layer's MapplGIS identity and MOS
settings are immutable and the tile path only reads them. `geometry_type` is
orthogonal to detection: an explicit `geometry_type` does not skip MapplGIS
table detection (it only fixes the layer's geometry type), and a detected
MapplGIS table still applies its system info even with `geometry_type` set.

The detector is implemented per provider against the backend's own catalog:
MySQL/MariaDB via `SHOW COLUMNS` / `SHOW INDEX` (version-safe parsing of the
5.7/8.0/MariaDB row shapes), GPKG via `PRAGMA table_info` /
`sqlite_master`, PostGIS via `information_schema.columns` /
`pg_index` / `pg_attribute`, HANA via `SYS.TABLE_COLUMNS` /
`SYS.INDEXES` / `SYS.INDEX_COLUMNS`. Detection runs for every `tablename`
layer of every standard provider; custom `sql` layers are subject only to
`sql-sample` storage detection (above), which never applies system info.

When a table is detected as MapplGIS, the effective geometry format is
authoritative `mos`: an unset or `auto` format resolves to `mos`, and an
explicit non-MOS `geometry_format` on the same layer is a startup conflict
error. A detected table also replaces an unset (default) `id_fieldname` with
the contract primary key `OKEY`; an explicitly configured id field is
honored.

Startup type inspection (as opposed to MapplGIS detection) still samples the
same uniform window of up to `codec.InspectionSampleLimit` (16) rows to
infer the layer geometry type from the first decodable geometry — but this
sampling is type inference only and never applies system info.

## Provider support matrix

| Capability | mysql | gpkg | postgis | hana |
|---|---|---|---|---|
| `srid` / `crs_defn` | yes | yes | yes | yes (synthetic CRS requires a raw format, see below) |
| `geometry_format` (`wkb`/`wkt`/`mos`) | yes | yes (`mos` skips the GeoPackage binary header) | yes | yes |
| `mos_precision` / `mos_units` | yes | yes | yes | yes |
| system info auto-config (MapplGIS tables) | yes | yes | yes | yes |
| native spatial filter | yes (MBR/indexed bounds) | yes (RTree index; skipped for all raw formats `wkb`/`wkt`/`mos`, and for raw tables without GeoPackage metadata) | yes (`&&`) | yes (`ST_IntersectsRect*`; skipped for `mos` and synthetic CRS) |
| `id_fieldname` default | `fid` | `fid` | empty | empty |
| absent/empty `fields` | id + geometry only | id + geometry only | all columns | all columns |
| MVT variant | — | — | `mvt_postgis` | `mvt_hana` |

HANA notes:

- HANA distinguishes round-earth and planar SRS; a round-earth SRS is queried
  through its planar equivalent internally (`PLANAR_SRID_OFFSET = 1000000000`).
- A synthetic CRS (from `crs_defn` or system-info `Projection`) cannot be used
  with HANA's native `ST_Geometry` handling: the provider requires
  `geometry_format` = `wkb`, `wkt` or `mos` for such layers. MVT providers do
  not support synthetic CRS at all.
- Planar-equivalent SRIDs (`1000000000 + n`) are internal and must not be
  confused with synthetic Tegola SRIDs (`>= 340000001`).
