# MySQL / MariaDB Provider

The `mysql` provider serves MVT tiles from spatial tables in MySQL (5.7+/8.0+) and MariaDB (10.2+). It supports both the `tablename` and custom `sql` layer configuration modes, plus the full set of SQL tokens available in the postgis provider.

## Config

The provider keeps at most `max_connections` open connections, reuses the same
number of idle connections, and retires idle connections after five minutes or
any connection after thirty minutes. Tile queries use `QueryContext` and are
retried up to two times when the driver reports a broken connection. Features
are held until the complete result set has been read, so a retry after a
mid-stream connection failure cannot emit duplicates.

```toml
[[providers]]
name = "mysql_provider"
type = "mysql"
host = "localhost"
port = 3306                 # optional, default 3306
database = "gis"
user = "user"
password = "password"
srid = 3857                 # optional, default 3857
geometry_format = "auto"    # optional: auto (default) | mysql | mariadb | wkb | wkt | mos
mos_precision = 2           # optional, MOS format only: decimal digits of quantized int coords, default 0
mos_units = "m"             # optional, MOS format only: mm | cm | dm | m | km, default m
max_connections = 100       # optional, default 100

[[providers.layers]]
name = "buildings"
tablename = "buildings"
# id_fieldname = "fid"      # optional, default "fid"
# geometry_fieldname = "geom" # optional, default "geom"
# fields = ["height", "name"]
# srid = 4326               # optional, layer-level override (see "SRID handling")
# crs_defn = "+proj=longlat +ellps=WGS84 +datum=WGS84 +no_defs" # optional, wins over srid
# mos_precision = 2         # optional, layer-level override, MOS format only
# mos_units = "mm"          # optional, layer-level override: mm | cm | dm | m | km

[[providers.layers]]
name = "landmarks"
sql = "SELECT fid, geom, name FROM landmarks WHERE geom && !BBOX! AND zoom_level >= !ZOOM!"
```

## Geometry formats

MySQL and MariaDB store geometry columns in different *native internal layouts*, which this provider handles explicitly:

| Server | Layout |
|---|---|
| MySQL | `[4B SRID little-endian][1B byte-order][WKB type + body]` |
| MariaDB ≤ 10.6 | `[1B byte-order][4B SRID in that byte order][WKB type + body]` — note the SRID comes **after** the byte-order marker |
| MariaDB 10.7+ | same as above, but the top 3 bits of the SRID carry axis-order flags for reference geometries (masked off automatically) |

The `geometry_format` config option controls decoding:

- `auto` (default) — the provider runs `SELECT VERSION()` at startup to detect whether the server is MySQL or MariaDB and uses the matching native layout. If a blob does not fit the native layout, plain WKB decoding is attempted as a fallback (covers values already converted with `ST_AsBinary()` in custom SQL).
- `mysql` — force the MySQL native layout.
- `mariadb` — force the MariaDB native layout (handles 10.7+ axis-order flag bits).
- `wkb` — expect plain WKB with no header (e.g. when the layer selects `ST_AsBinary(geom) AS geom`).
- `wkt` — expect WKT text (e.g. a `LINESTRING(...)` stored in a TEXT column). No SRID is decoded; the configured layer/provider SRID applies.
- `mos` — expect the packed binary geometry format written by Mappl GIS, typically a `LONGBLOB LINE` column. Coordinates are quantized int32 pairs; set `mos_precision` to the number of decimal digits they carry and `mos_units` to their packed linear units (`mm`, `cm`, `dm`, `m`, or `km`). After dequantization, coordinates are converted to metres using the corresponding factor (`mm` → `0.001`, `cm` → `0.01`, `dm` → `0.1`, `m` → `1`, `km` → `1000`) before SRID reprojection. MOS carries no CRS — the configured layer/provider SRID applies (or the layer's own system info blob, see below). Because the blob is opaque, the provider uses indexed `MINX`/`MAXX`/`MINY`/`MAXY` columns as a coarse bounding-box `!BBOX!` filter in the raw MOS units, then applies the decoded geometry's bounding-box intersection check in Go; individual undecodable rows are logged and skipped.

## MOS geometry format

The native MOS blob layout (little-endian) starts with a 10-byte geometry
prefix (object type, subobject count and total point count), then one `uint32`
point count per subobject, followed by all points as contiguous `(int32 x,
int32 y)` pairs. Tegola also accepts the older 12-byte fixture form with an
optional `uint16` flags word before the subobject counts. Real coordinates are
`int / 10^mos_precision`. Object types map to MVT geometries as:

- polygon → `Polygon`/`MultiPolygon` (rings are classified into exteriors and holes by containment, so an island inside a lake hole becomes a second polygon)
- polyline → `LineString`/`MultiLineString`
- point → `Point`/`MultiPoint`
- text/image → anchor `Point`

### Layer system info blob (auto-configuration)

MapplBase commonly stores a `TLayerSystemInfoRec` version wrapper blob near the beginning of every MOS geometry table. It carries the layer's self-description, and the provider samples up to 16 rows while looking for both a decodable geometry and this metadata at registration (for `tablename` and `sql` layers):

- `Precision` — the quantization precision (`kPrecision = 10^Precision`). Applied as the layer's `mos_precision` when neither the provider- nor layer-level `mos_precision` key is set.
- `Projection` — the layer's full PROJ.4 definition. Registered as a synthetic SRID (≥ 340000001, same mechanism as `crs_defn`) and used as the layer SRID when no `srid`/`crs_defn` is configured at provider or layer level. Explicit config values always win.
- `MapUnits` / `flMapUnitsDefined` — when `mos_units` is not configured, the declared unit is converted to a metres factor and applied after dequantization. Supported units are millimetres (`muMm`), centimetres (`muSm`), decimetres (`muDm`), metres (`muM`) and kilometres (`muKm`). Unsupported angular/undefined units are rejected.

In practice this means a MOS table exported by MapplBase needs no `mos_precision`/`mos_units`/`srid` configuration at all — the layer configures itself from its own system info row, and explicit config keys remain available as overrides.

## SRID handling

SRID resolution order (highest priority first):

1. Layer-level `crs_defn` config value (full PROJ.4 definition, see below).
2. Layer-level `srid` config value.
3. Provider-level `crs_defn` config value.
4. Provider-level `srid` config value (if explicitly configured).
5. SRID decoded from the sampled geometry header at registration (native formats only).
6. Default `3857` (Web Mercator).

When the layer SRID differs from Web Mercator, tile bounding boxes are reprojected into the layer SRID before the `!BBOX!` filter is applied, and geometries are delivered to the MVT encoder with their true SRID.

### Reprojection

Reprojection between the layer SRID and Web Mercator uses the vendored `go-spatial/proj` library. Any SRID can be made available in two ways:

1. **Built-in table** — registered automatically at startup (`basic.RegisterBuiltinProj4SRIDs`), covering ~220 widely used systems:

   | Codes | System |
   |---|---|
   | 32601–32660 | WGS 84 / UTM northern zones |
   | 32701–32760 | WGS 84 / UTM southern zones |
   | 28401–28432 | Pulkovo 1942 / Gauss-Kruger zones (8-digit eastings) |
   | 2463–2491 | Pulkovo 1995 / Gauss-Kruger zones |
   | 2492–2522 | Pulkovo 1942 / Gauss-Kruger zones (500000 false easting) |
   | 3785, 900913 | Web Mercator aliases |
   | 53004 | Sphere Mercator (ESRI) |

2. **`proj4` config option** — register any additional EPSG code with an explicit PROJ.4 definition at provider level:

   ```toml
   [[providers]]
   name = "mysql_provider"
   type = "mysql"
   # ...
   # either a string of "EPSG_CODE=proj4 def" entries (newline or ; separated):
   proj4 = "2180=+proj=sterea +lat_0=52 +lon_0=19 +k=0.9993 +x_0=500000 +y_0=-5300000 +ellps=bessel +units=m +no_defs"
   # or a TOML table:
   [providers.proj4]
   2180 = "+proj=sterea +lat_0=52 +lon_0=19 +k=0.9993 +x_0=500000 +y_0=-5300000 +ellps=bessel +units=m +no_defs"
   ```

   Keys may be written as plain integers or with an `EPSG:` / `epsg:` prefix (e.g. `"EPSG:2180"`). Definitions are validated at startup with a forward+inverse round trip; unsupported ones are rejected with a clear error.

Geographic systems stored in degrees (e.g. EPSG:4326, EPSG:4258) do not require registration — they are converted natively. Note that only a subset of PROJ.4 operations is implemented by the vendored proj library (`merc`, `utm`, `etmerc`, `aea`, `leac`, `eqc`); transverse Mercator-based systems use `+proj=etmerc`, not `+proj=tmerc`.

### `crs_defn` — textual CRS definition instead of SRID

When a system has no EPSG code (or you don't want to use one), a full PROJ.4
definition can be given as `crs_defn` instead of a numeric `srid`, at either
provider or layer level. The definition is passed to proj directly and
validated with a forward+inverse round trip at startup; it is registered under
a synthetic internal SRID (≥ 340000001) that flows through the regular
reprojection path. A `crs_defn` always wins over a numeric `srid` configured at
the same level; layer-level settings win over provider-level ones.

```toml
[[providers]]
name = "mysql_provider"
type = "mysql"
# ...
# provider-level default for all layers:
crs_defn = "+proj=merc +lon_0=0 +k_0=1 +x_0=0 +y_0=0 +ellps=WGS84 +datum=WGS84 +units=m +no_defs"

  [[providers.layers]]
  name = "tram_sections"
  # layer-level override wins over the provider-level crs_defn:
  crs_defn = "+proj=etmerc +lat_0=0 +lon_0=61 +k_0=1 +x_0=500000 +y_0=0 +ellps=krass +units=m +no_defs"
```

Use `+proj=etmerc` instead of `+proj=tmerc` for transverse Mercator
definitions (vendored proj limitation).

Synthetic SRIDs (the IDs assigned to `crs_defn`) exist only inside Tegola and
are not registered in MySQL or MariaDB. For native geometry columns, Tegola
therefore disables the server-side `!BBOX!` spatial predicate for a synthetic
SRID and performs reprojection and tile filtering in Tegola. This is correct
but can be slower; use a real database SRID when the database has a matching
registered SRS. MOS layers continue to use their indexed raw-bounds filter.

## SQL tokens

The following tokens are supported in custom `sql` (case-insensitive) and behave identically to the postgis provider:

- `!BBOX!` — for native spatial geometries, replaced with `ST_Intersects(<geom_field>, ST_GeomFromText('POLYGON(...)', <layer_srid>))` using the tile's buffered extent in the layer's SRID. WKT columns are wrapped with the same SRID. For MOS, replaced with an indexed `MINX`/`MAXX`/`MINY`/`MAXY` overlap predicate in the raw packed coordinate units.
- `!ZOOM!`, `!Z!` — the tile's zoom (Z) value.
- `!X!`, `!Y!` — the tile's X/Y values.
- `!SCALE_DENOMINATOR!` — scale denominator assuming 90.7 DPI.
- `!PIXEL_WIDTH!`, `!PIXEL_HEIGHT!` — pixel size in meters for 256x256 tiles.
- `!ID_FIELD!` — the layer's id field name.
- `!GEOM_FIELD!` — the layer's geometry field name.
- `!GEOM_TYPE!` — the layer's geometry type name (POINT, LINESTRING, ...).

Custom SQL containing tile-dependent tokens (`!X!`, `!Y!`, `!Z!`,
`!SCALE_DENOMINATOR!`, `!PIXEL_WIDTH!`, or `!PIXEL_HEIGHT!`) is not executed
during provider startup for geometry-type inspection. The layer is registered
with its configured CRS and inspected when a tile is requested. For such a
layer, `!BBOX!` is deliberately replaced with `1=1` when the geometry format
is `auto`; Tegola decodes the returned geometries and applies the tile
intersection check in memory. This avoids applying MySQL spatial functions to
an unknown format (including MOS). If the first returned row is a MOS
system-info record, its precision, units, and projection are applied to the
request unless the corresponding CRS or MOS settings were explicitly
configured.

## Empty layers

Layers (table or custom SQL) that currently return 0 rows produce a warning
and remain registered without an inferred geometry type. They are queried
normally when a later request returns data; an empty layer does not prevent the
provider from starting. Until the first decodable geometry is seen, the layer
uses the same safe in-memory filtering path as deferred custom SQL so a later
geometry header can establish the source CRS without an incorrect startup
assumption.

## Known limitations

- Only 2D geometries are supported by the decoder for MVT encoding (matching MariaDB's capabilities).
- Custom SQL used in derived-table inspection must be aliasable — the inspection query wraps it as `SELECT geom FROM (<your sql>) AS __tegola_inspection LIMIT 1`.
