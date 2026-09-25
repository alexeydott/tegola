# Geometry formats

All standard providers (`mysql`, `gpkg`, `postgis`, `hana`) decode geometries
into an internal in-memory representation before projecting them to Web
Mercator and encoding them as MVT features. This document describes the
supported input formats, their bounding-box behaviour, and how
`GeometryCollection` values are processed.

## Supported formats

`geometry_format` is a provider-level default and can be overridden per
layer.

| Format | Value | Description |
|---|---|---|
| Native | *(unset / empty)* | The provider's own binary geometry handling: WKB-backed blobs for `postgis`/`hana`/`mysql` (`ST_AsBinary`), GeoPackage binary blobs for `gpkg`. |
| WKB | `wkb` | Plain OGC WKB, e.g. selected via `ST_AsBinary(...)` in custom SQL. |
| WKT | `wkt` | OGC WKT text, e.g. selected via `ST_AsText(...)`. |
| MOS | `mos` | Opaque MapplGIS MOS binary blob with integer-quantized coordinates. |

`geometry_fieldname` selects the column that carries the geometry (default
`geom`); `id_fieldname` selects the feature id column (default `fid` except
`postgis`/`hana`, see [provider-contract.md](provider-contract.md)).

## MOS quantization

MOS coordinates are integers in a packed linear unit. `mos_precision` (decimal
digits) and `mos_units` (`mm`, `cm`, `dm`, `m`, `km`; default `m`) define the
scale factor applied when converting to metres. The default `mos_precision` is
paired with the effective units: `mm`→`0`, `cm`→`1`, `dm`→`1`, `m`→`2`,
`km`→`5`. An explicitly set `mos_precision` overrides the units-paired default.
Explicitly set
`mos_precision`/`mos_units` always win over values detected from a
`MapplGIS LayerInfo` metadata blob during registration-time MapplGIS table
detection (see
[provider-contract.md](provider-contract.md#system-info-auto-configuration-mapplgis-tables-only)).

## CRS handling

The layer SRID resolution order and the Web Mercator (EPSG:3857) conversion
are documented in [crs.md](crs.md). In short: tile bounds (`!BBOX!`) are
always expressed in the layer's resolved CRS, decoded geometries are
reprojected from that CRS to Web Mercator, and MVT encoding consumes the
reprojected geometry — one consistent CRS contract end to end.

## BBOX filtering per format

| Format | SQL filter | In-memory filter |
|---|---|---|
| Native | Provider spatial predicate against the indexed geometry (e.g. `&&`, MBR, RTree) | Exact bounding-box check |
| `wkb` | None (raw bytes are not database geometries); `mysql` wraps the WKB column in `ST_GeomFromWKB` as a provider optimization | Exact bounding-box check |
| `wkt` | None; `mysql` wraps the WKT column in `ST_GeomFromText` as a provider optimization | Exact bounding-box check |
| `mos` | Bounds-backed SQL: indexed bounds columns (`MINX/MAXX/MINY/MAXY` by default) filtered with `!BBOX!`; mandatory for custom SQL | Exact bounding-box check |

Because raw formats cannot use the database spatial index (except the
bounds-backed MOS case), spatial selectivity comes from the in-memory
bounding-box check applied to every decoded feature. The MySQL
`ST_GeomFromWKB`/`ST_GeomFromText` wrapping is only a server-side coarse
filter (a provider optimization, not an index use); the exact in-memory
bounding-box check remains mandatory for every raw format.

### Bounds-backed MOS SQL (`!BBOX!` over bounds columns)

The `mos` format stores each feature's raw bounds in four numeric columns
(EGKO MapplGIS convention: `MINX`, `MAXX`, `MINY`, `MAXY` by default). These
are the only server-side filter a raw MOS column can support: for `mos`
layers the `!BBOX!` token (and `!BOX!`) expands into the bounds predicate

```
<maxx> >= tile.maxx AND <minx> <= tile.minx AND <maxy> >= tile.maxy AND <miny> <= tile.miny
```

scaled from the tile metres by the layer's MOS quantization
(floor/ceil x 10^precision / unit-factor), so the comparison runs in the
same integer units the bounds columns store.

The four columns are configurable with the common config keys (layer level
overrides provider level, per field):

- `bbox_minx_fieldname` (default `MINX`)
- `bbox_maxx_fieldname` (default `MAXX`)
- `bbox_miny_fieldname` (default `MINY`)
- `bbox_maxy_fieldname` (default `MAXY`)

Columns resolved this way are excluded from the feature tags: they are
implementation details of the bounds contract, not feature attributes.

### Raw formats and custom SQL

Custom SQL (`sql` key) with a raw `geometry_format`:

- `mos` **must** use `!BBOX!` (or `!BOX!`): the bounds predicate over the
  configured bounds fields is the only server-side selectivity a raw MOS
  column supports. Providers reject bounds-backed MOS custom SQL without the
  token at startup with an error naming the layer. As a registration-time
  sanity probe, providers additionally sample the custom SQL result and warn
  when the four bounds columns or decodable MOS rows are missing
  (`MapplGISSource` = `sql-sample`).
- `wkb`/`wkt` must **not** use `!BBOX!` (nor its `!BOX!` alias): its
  expansion assumes a native spatial column (e.g. `geom && ST_MakeEnvelope(...)`
  or a `minx`/`maxx` column predicate), which a raw BLOB/TEXT column does not
  provide. Providers reject such custom SQL at startup with an error naming the
  layer and the offending token — remove `!BBOX!` from the custom SQL; the exact
  in-memory bounding-box filter is always applied for raw formats. Custom SQL
  without `!BBOX!` works for raw formats in every provider, so the same
  configuration behaves identically across `postgis`, `hana`, `gpkg`, and
  `mysql`. The `auto` format of `mysql` is exempt from the startup check because
  runtime inspection may resolve the column to a native spatial type for which
  `!BBOX!` is valid.

## GeometryCollection behaviour

`GeometryCollection` values (including nested collections) are handled in
three cooperating stages:

1. **CRS conversion.** The reprojection helper applies the CRS transform to
   every point of every member geometry, recursively. Collections are not
   flattened at this stage.
2. **MVT encoding.** `GeometryCollection` features are recursively flattened
   into separate MVT features, one per leaf geometry, so MVT never emits
   nested collections. Leaf multiparts are kept whole.
3. **Auto-styling** (`?style` generated styles). A layer whose collection
   mixes polygon and line leaf geometries gets two sibling style layers:
   a line layer rendered before the fill layer, with `"filter": {"$type":
   "Polygon"/"LineString"}` so leaf geometry classes are styled correctly.

## See also

- [provider-contract.md](provider-contract.md) — configuration keys shared by all providers
- [crs.md](crs.md) — SRID resolution, built-in and synthetic CRS definitions
