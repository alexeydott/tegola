# GeoPackage
This provider connects to GeoPackage databases (See http://www.geopackage.org/ http://www.opengeospatial.org/standards/geopackage)

The connection between tegola and a GeoPackage is configured in a `tegola.toml` file. An example minimum connection config:

```toml
[[providers]]
name = "sample_gpkg"
type = "gpkg"
filepath = "/path/to/my/sample_gpkg.gpkg"
```

### Connection Properties

- `name` (string): [Required] provider name is referenced from map layers.
- `type` (string): [Required] the type of data provider. must be "gpkg" to use this data provider.
- `filepath` (string): [Required] The system file path to the GeoPackage file you wish to connect to.
- `srid` (int): [Optional] the SRID of the geometries in the GeoPackage. When set explicitly it wins over the value inferred from `gpkg_contents.srs_id` or the geometry headers. Defaults to Web Mercator (3857) when nothing can be inferred.
- `crs_defn` (string): [Optional] a full PROJ.4 coordinate system definition used instead of a numeric `srid` (see below). Wins over `srid` at the same level.

## Provider Layers
In addition to the connection configuration above, Provider Layers need to be configured. A Provider Layer tells tegola how to query a GeoPackage for a certain layer. An example minimum config:

```toml
[[providers.layers]]
name = "land_polygons"
tablename = "land_polygons"
id_fieldname = "fid"
```

### Provider Layers Properties

- `name` (string): [Required] the name of the layer. This is used to reference this layer from map layers.
- `tablename` (string): [*Required] the name of the database table to query against. Required if `sql` is not defined.
- `id_fieldname` (string): [Optional] the name of the feature id field. defaults to `fid`
- `fields` ([]string): [Optional] a list of fields (column names) to include as feature tags. Can be used if `sql` is not defined.
- `srid` (int): [Optional] layer-level SRID override. Wins over the provider-level value and over the SRID inferred from the GeoPackage.
- `crs_defn` (string): [Optional] layer-level full PROJ.4 definition used instead of a numeric `srid`. Wins over `srid` at the same level.
- `geometry_fieldname` (string): [Optional] the name of the geometry field. defaults to `geom`. Note: for layers backed by a GeoPackage `tablename` (native `gpkg` format), the geometry column is taken from `gpkg_geometry_columns` and this setting is ignored. Use a custom `sql` layer (or a raw `geometry_format`) if you need to point at a different geometry column.
- `sql` (string): [*Required] custom SQL to use. Required if `tablename` is not defined. Supports the following WHERE-clause tokens:
  - !BBOX! - [Required] will be replaced with the bounding box of the tile before the query is sent to the database.  To support this token, your custom SQL must do a couple of things. 
    - You must join your feature table to the spatial index table: i.e. `FROM feature_table ft JOIN rtree_feature_table_geom si ON ft.fid = si.id`
	- Include the following fields in your SELECT clause: si.minx, si.miny, si.maxx, si.maxy
	- Note that the id field for your feature table may be something other than `fid`
  - `!ZOOM!` - [Optional] will be replaced with the "Z" (zoom) value of the requested tile.
  - `!X!` - [Optional] will be replaced with the "X" value of the requested tile.
  - `!Y!` - [Optional] will be replaced with the "Y" value of the requested tile.
  - `!Z!` - [Optional] will be replaced with the "Z" value of the requested tile.
  - `!SCALE_DENOMINATOR!` - [Optional] scale denominator, assuming 90.7 DPI (i.e. 0.28mm pixel size).
  - `!PIXEL_WIDTH!` - [Optional] the pixel width in meters, assuming 256x256 tiles.
  - `!PIXEL_HEIGHT!` - [Optional] the pixel height in meters, assuming 256x256 tiles.
  - `!ID_FIELD!` - [Optional] the ID field name.
  - `!GEOM_FIELD!` - [Optional] the geometry field name.
  - `!GEOM_TYPE!` - [Optional] the geometry type if known, otherwise an empty string.

  Custom SQL containing tile-dependent tokens (`!X!`, `!Y!`, `!Z!`,
  `!SCALE_DENOMINATOR!`, `!PIXEL_WIDTH!`, or `!PIXEL_HEIGHT!`) is not executed
  during provider startup for geometry-type inspection. The layer is registered
  with its configured CRS and geometry type remains unknown until the query is
  served. This avoids inspecting a different tile from the one requested.

  `*Required`: either the `tablename` or `sql` must be defined, but not both.

**Example minimum custom SQL config**

```toml
[[providers.layers]]
name = "a_points"
sql = "SELECT fid, geom, amenity, religion, tourism, shop, si.minx, si.miny, si.maxx, si.maxy FROM land_polygons lp JOIN rtree_land_polygons_geom si ON lp.fid = si.id WHERE !BBOX!"
```

### Custom coordinate systems (`crs_defn`)

When the GeoPackage stores geometries in a system that has no EPSG code (or
you prefer not to rely on one), a full PROJ.4 definition can be provided as
`crs_defn` instead of a numeric `srid`, at provider or layer level. The
definition is passed to proj directly and validated at startup; it is
registered under a synthetic internal SRID (≥ 340000001) and used for both
`!BBOX!` reprojection and geometry conversion to Web Mercator. A `crs_defn`
always wins over a numeric `srid` configured at the same level.

```toml
[[providers]]
name = "sample_gpkg"
type = "gpkg"
filepath = "/path/to/my/sample_gpkg.gpkg"
# provider-level default for all layers:
crs_defn = "+proj=merc +lon_0=0 +k_0=1 +x_0=0 +y_0=0 +ellps=WGS84 +datum=WGS84 +units=m +no_defs"
```

Use `+proj=etmerc` instead of `+proj=tmerc` for transverse Mercator
definitions (vendored proj limitation). Note the RTree spatial index bounds
must be stored in the same CRS as the geometries for `!BBOX!` filtering to
work correctly.

### Common geometry / CRS options

The GPKG provider implements the common geometry contract documented in
[docs/provider-contract.md](../../docs/provider-contract.md):

- `geometry_type` (string): [Optional, **layer level only**] explicit layer
  geometry type
  (`Point`, `LineString`, `Polygon`, `MultiPoint`, `MultiLineString`,
  `MultiPolygon`, `GeometryCollection`). Skips startup type inspection;
  mixed content is permitted with a one-time warning.
- `geometry_format` (string): [Optional] `gpkg` (GeoPackage native binary,
  the default), `wkb`, `wkt` or `mos`. Valid at provider level (defaults for
  all layers) and at layer level (overrides). With `gpkg` the GeoPackage
  binary header is parsed and the embedded WKB body is decoded; with `mos`
  the header is skipped and the raw MOS payload is used directly.
- `mos_precision` (int): [Optional] decimal digits carried by MOS
  coordinates. Only applies when the effective geometry format is `mos`.
  Valid at provider and layer level.
- `mos_units` (string): [Optional] packed linear unit of MOS coordinates
  (`mm`, `cm`, `dm`, `m` or `km`). Only applies when the effective geometry
  format is `mos`. Valid at provider and layer level.

```toml
[[providers]]
name = "sample_gpkg"
type = "gpkg"
filepath = "/path/to/my/sample_gpkg.gpkg"
geometry_format = "mos"
mos_precision = 2
mos_units = "m"

[[providers.layers]]
name = "lines"
tablename = "lines"
geometry_type = "LineString"
```

### Raw tables

A layer with `geometry_format` set to a raw format (`wkb`, `wkt` or `mos`) or
a raw table that has no `gpkg_contents` / `gpkg_geometry_columns` metadata
does not need GeoPackage metadata at all: the table is registered from
`PRAGMA table_info` and the RTree spatial index is **not** used. Every
`tablename` layer — including raw tables missing from the GeoPackage
metadata — is first checked against the shared structural MapplGIS contract
(see [docs/provider-contract.md](../../docs/provider-contract.md)); a
detected MapplGIS table is served via the `mos` format automatically, with
no explicit `geometry_format` needed. A plain raw table (not detected as
MapplGIS) that is missing from the GeoPackage metadata **requires** an
explicit raw `geometry_format` — without one registration fails with a
`table does not exist` error, because the native `gpkg` path looks the table
up in `gpkg_geometry_columns` and will not find it. The `!BBOX!`
token still works, but rows are filtered in memory, so performance depends on
table size.

**Performance warning:** without an RTree join every query scans the table
and decodes all candidate geometries. For large tables prefer the native
`gpkg` format (which joins the RTree index table named `rtree_` + table name
+ `_` + geometry column name, e.g. `rtree_land_polygons_geom` for table
`land_polygons` with geometry column `geom`), or declare per-row bounds
columns so the coarse `!BBOX!` filter can be applied by SQLite.

If the table has numeric bounds columns named (case-insensitively)
`minx`, `maxx`, `miny` and `maxy`, the provider detects them and applies a
coarse SQL-level `!BBOX!` filter before in-memory refinement. The units of
the stored bounds values must match what the provider compares against:

* for `wkb` / `wkt` layers the bounds columns hold plain layer-CRS
  coordinates (the same units as the decoded geometries);
* for `mos` layers the bounds columns hold the quantized raw MOS values as
  stored in the database (`raw value = coordinate * 10^mos_precision /
  unitFactor`, e.g. millimetres with `mos_precision = 0` and
  `mos_units = "mm"`), because the extent is scaled by `rawScale` before the
  comparison.

The bounds column names are configurable with the common
`bbox_minx_fieldname` / `bbox_maxx_fieldname` / `bbox_miny_fieldname` /
`bbox_maxy_fieldname` keys (layer level overrides provider level, per
field; defaults `MINX`/`MAXX`/`MINY`/`MAXY`). Resolved bounds columns are
excluded from feature tags.

For custom SQL with `geometry_format = "mos"` the `!BBOX!` token is
**required** and expands into the bounds-columns predicate over the
configured bounds fields (with MOS raw scaling); the provider verifies at
registration that the token is present. Custom SQL with the native `gpkg`
binary format may use `!BBOX!` as a source-CRS bounds comparison over the
same columns (no MOS scaling). Bounds-backed MOS custom SQL must configure
`srid`/`crs_defn`, `mos_precision` and `mos_units` explicitly.

Example table layout for a `wkb` layer:

```sql
CREATE TABLE land_polygons (
  fid INTEGER PRIMARY KEY,
  geom BLOB,
  minx REAL, maxx REAL, miny REAL, maxy REAL
)
```

```toml
[[providers.layers]]
name = "land_polygons"
tablename = "land_polygons"
id_fieldname = "fid"
geometry_format = "wkb"
```

For raw custom-SQL layers `geometry_fieldname` and `id_fieldname` must point
at existing columns; if the configured `id_fieldname` does not exist the
table primary key is used as the id field with a warning, while a missing
geometry column is a startup error.

### Empty layers

If a configured custom-SQL layer currently returns no rows, Tegola logs a
warning and keeps the layer registered without an inferred geometry type. This
allows data to appear later without requiring a restart. A tile request still
executes the configured SQL normally.