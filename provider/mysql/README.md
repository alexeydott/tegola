# MySQL / MariaDB Provider

The `mysql` provider serves MVT tiles from spatial tables in MySQL (5.7+/8.0+) and MariaDB (10.2+). It supports both the `tablename` and custom `sql` layer configuration modes, plus the full set of SQL tokens available in the postgis provider.

## Config

The provider keeps at most `max_connections` open connections, reuses the same
number of idle connections, and retires idle connections after five minutes or
any connection after thirty minutes. Tile queries use `QueryContext` and are
retried up to two times when the driver reports a broken connection. Features
are held until the complete result set has been read, so a retry after a
mid-stream connection failure cannot emit duplicates.

The connection DSN is built with the go-sql-driver's `Config.FormatDSN`, so
user/password/database values with special characters are escaped correctly.
`multiStatements` is deliberately **not** enabled: layer SQL must be a single
statement.

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
mos_precision = 2           # optional, MOS format only: decimal digits of quantized int coords; default depends on mos_units (mm→0, cm→1, dm→1, m→2, km→5)
mos_units = "m"             # optional, MOS format only: mm | cm | dm | m | km, default m
max_connections = 100       # optional, default 100
# tls = "preferred"         # optional, go-sql-driver TLS config name: true | false | preferred | skip-verify | <registered tls.Config name>
# timeout = "10s"           # optional, dial timeout; Go duration string ("500ms", "10s") or integer seconds; default: driver default (no timeout)

[[providers.layers]]
name = "buildings"
tablename = "buildings"
# id_fieldname = "fid"      # optional, default "fid"
# geometry_fieldname = "geom" # optional, default "geom"
# fields = ["height", "name"]
# srid = 4326               # optional, layer-level override (see "SRID handling")
# crs_defn = "+proj=longlat +ellps=WGS84 +datum=WGS84 +no_defs" # optional, wins over srid
# geometry_format = "wkb"   # optional, layer-level override: auto | mysql | mariadb | wkb | wkt | mos (overrides the provider-level value for this layer)
# mos_precision = 2         # optional, layer-level override, MOS format only
# mos_units = "mm"          # optional, layer-level override: mm | cm | dm | m | km

[[providers.layers]]
name = "landmarks"
sql = "SELECT fid, geom, name FROM landmarks WHERE geom && !BBOX! AND zoom_level >= !ZOOM!"
```

## Common geometry / CRS options

The MySQL/MariaDB provider implements the common geometry contract documented
in [docs/provider-contract.md](../../docs/provider-contract.md). Alongside the
format keys above, the common `geometry_type` layer key is supported: an
explicit value (`Point`, `LineString`, `Polygon`, `MultiPoint`,
`MultiLineString`, `MultiPolygon`, `GeometryCollection`) fixes the layer
geometry type before any data is read and skips geometry-class inference and
the >=3-sample-row requirement (empty data is allowed); structural validation
of custom SQL still runs.
It is orthogonal to MapplGIS table detection: a `tablename` layer is still
checked for the MapplGIS signature and still receives system-info
configuration when detected. Mixed content is permitted with a one-time
warning.

```toml
[[providers.layers]]
name = "lines"
tablename = "lines"
geometry_type = "LineString"
```

## Geometry formats

MySQL and MariaDB store geometry columns in different *native internal layouts*, which this provider handles explicitly:

| Server | Layout |
|---|---|
| MySQL | `[4B SRID little-endian][1B byte-order][WKB type + body]` |
| MariaDB ≤ 10.6 | `[1B byte-order][4B SRID in that byte order][WKB type + body]` — note the SRID comes **after** the byte-order marker |
| MariaDB 10.7+ | same as above, but the top 3 bits of the SRID carry axis-order flags for reference geometries (masked off automatically) |

The `geometry_format` config option controls decoding. It can be set at the
provider level (a default for all layers) and overridden per layer with the
same value set:

- `auto` (default) — the provider runs `SELECT VERSION()` at startup to detect whether the server is MySQL or MariaDB and uses the matching native layout. If a blob does not fit the native layout, plain WKB decoding is attempted as a fallback (covers values already converted with `ST_AsBinary()` in custom SQL).
- `mysql` — force the MySQL native layout.
- `mariadb` — force the MariaDB native layout (handles 10.7+ axis-order flag bits).
- `wkb` — expect plain WKB with no header (e.g. when the layer selects `ST_AsBinary(geom) AS geom`).
- `wkt` — expect WKT text (e.g. a `LINESTRING(...)` stored in a TEXT column). No SRID is decoded; the configured layer/provider SRID applies.
- `mos` — expect the packed binary geometry format written by MapplGIS, typically a `LONGBLOB LINE` column. Coordinates are quantized int32 pairs; `mos_precision` (optional) is the number of decimal digits they carry and `mos_units` (optional) their packed linear units (`mm`, `cm`, `dm`, `m`, or `km`, default `m`; the default `mos_precision` is paired with the units: `mm`→`0`, `cm`→`1`, `dm`→`1`, `m`→`2`, `km`→`5` via `DefaultMOSPrecisionForUnits`). After dequantization, coordinates are converted to metres using the corresponding factor (`mm` → `0.001`, `cm` → `0.01`, `dm` → `0.1`, `m` → `1`, `km` → `1000`) before SRID reprojection. MOS carries no CRS — the configured layer/provider SRID applies (or the layer's own system info blob, see below). Because the blob is opaque, the provider uses indexed `MINX`/`MAXX`/`MINY`/`MAXY` columns as a coarse bounding-box `!BBOX!` filter in the raw MOS units, then applies the decoded geometry's bounding-box intersection check in Go; individual undecodable rows are logged and skipped.

### Geographic SRIDs and MySQL axis order

MySQL 8 interprets geometry values for geographic SRIDs (e.g. `4326`)
latitude-first according to its SRS metadata, while tegola writes WKT and
`!BBOX!` polygons longitude-first. For `wkt`/`wkb` layers on MySQL with a
geographic SRID the provider therefore builds
`ST_GeomFromText(value, srid, 'axis-order=long-lat')` /
`ST_GeomFromWKB(value, srid, 'axis-order=long-lat')` (both for the geometry
column and the tile bbox polygon) so the filter and the stored data agree.
MariaDB does not support the options argument and projected SRIDs have no
axis order — both keep the plain constructor form. (MySQL 5.7 does not
accept the options argument either; use a projected SRID or MariaDB there.)

Geometry *reads* are decoded in Go from the raw column values and are
unaffected. Custom SQL that returns `ST_AsBinary(geom)` for a geographic
SRID should pass the same option — `ST_AsBinary(geom, 'axis-order=long-lat')`
— so the emitted WKB is longitude-first like everything tegola consumes.

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

### MapplGIS table detection (auto-configuration)

MapplGIS tables are detected once, at registration, by their **structure**
(table-canonical MapplGIS detection). This path never scans sample rows and
applies only to `tablename` layers; custom `sql` layers instead use the
separate SQL-sample storage detection (see below). A `tablename` layer is
recognized as a MapplGIS table only when all of the following hold (matched
case-insensitively):

- the table has all nine required columns: `OKEY`, `MUID`, `MINX`, `MAXX`,
  `MINY`, `MAXY`, `ObjectStyle`, `ObjectType`, `LINE`;
- `OKEY` is the table's primary key;
- all six required indexes exist over `MUID`, `MINX`, `MAXX`, `MINY`, `MAXY`,
  `ObjectType`;
- a single point probe (`OKEY = 1 AND LINE IS NOT NULL`) returns a decodable
  `MapplGIS LayerInfo` system info blob.

When detection succeeds the layer gets `IsMapplGIS=true` and the decoded
system info configures the layer:

- `Precision` — the quantization precision (`kPrecision = 10^Precision`). Applied as the layer's `mos_precision` when neither the provider- nor layer-level `mos_precision` key is set.
- `Projection` — the layer's full PROJ.4 definition. Registered as a synthetic SRID (≥ 340000001, same mechanism as `crs_defn`) and used as the layer SRID when no `srid`/`crs_defn` is configured at provider or layer level. Explicit config values always win.
- `MapUnits` / `flMapUnitsDefined` — when `mos_units` is not configured, the declared unit is converted to a metres factor and applied after dequantization. Supported units are millimetres (`muMm`), centimetres (`muSm`), decimetres (`muDm`), metres (`muM`) and kilometres (`muKm`). Unsupported angular/undefined units are rejected.

In practice this means a MapplGIS table needs no `mos_precision`/`mos_units`/`srid` configuration at all — the layer configures itself from its own system info row, and explicit config keys remain available as overrides. The effective geometry format of a detected table is authoritative `mos`: an explicit non-MOS `geometry_format` on the same layer is a startup conflict error. A detected table also replaces the default `id_fieldname = "fid"` with the contract primary key `OKEY`; an explicitly configured `id_fieldname` is honored.

Table-canonical MapplGIS detection never applies to custom `sql` layers, and
`LayerSystemInfo` is never applied from result rows. Custom `sql` layers are
still subject to SQL-sample storage detection: a `mos` (or `auto`) SQL layer
whose registration probe reports the MapplGIS signature is tagged `MapplGIS`
without system info and without applying any projection from the sample. A
MOS custom SQL layer must configure `srid` or `crs_defn` explicitly (a
missing CRS is a startup error); `mos_precision` and `mos_units` are
optional with the normative paired defaults.

## SRID handling

SRID resolution order (highest priority first):

1. Layer-level `crs_defn` config value (full PROJ.4 definition, see below).
2. Layer-level `srid` config value.
3. Provider-level `crs_defn` config value.
4. Provider-level `srid` config value (if explicitly configured).
5. `MapplGIS LayerInfo projection` decoded from the system info blob of a detected MapplGIS table layer (registration-time detection only — never from custom SQL; applied when no `srid`/`crs_defn` is configured at provider or layer level; registered as a synthetic SRID, see [docs/crs.md](../../docs/crs.md)).
6. SRID decoded from the sampled geometry header at registration (native formats only).
7. Default `3857` (Web Mercator).

When the layer SRID differs from Web Mercator, tile bounding boxes are reprojected into the layer SRID before the `!BBOX!` filter is applied, and geometries are delivered to the MVT encoder with their true SRID. The full CRS contract (config keys, precedence, synthetic SRIDs, `!BBOX!` semantics) is documented in [docs/crs.md](../../docs/crs.md) and is shared by all standard providers.

### Deferred custom SQL and mixed SRIDs

Tile-dependent custom SQL (queries containing `!X!`/`!Y!`/`!Z!` and friends) defers geometry inspection to the first query. Without an explicitly configured CRS, the first non-zero native geometry header SRID seen in the results establishes the canonical layer CRS. Rows carrying a *different* header SRID are skipped with a per-row warning instead of being silently interpreted as if they carried the canonical SRID; rows without a header SRID (0) are always processed. Mixed-SRID results are therefore not supported on deferred custom SQL layers — configure `srid`/`crs_defn` explicitly if the source mixes SRIDs.

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

2. **`crs_defn` config value** — a full PROJ.4 definition at provider or layer level, registered under a synthetic internal SRID. See the next section and [docs/crs.md](../../docs/crs.md).

Note that only a subset of PROJ.4 operations is implemented by the vendored proj library (`merc`, `utm`, `etmerc`, `aea`, `leac`, `eqc`); transverse Mercator-based systems use `+proj=etmerc`, not `+proj=tmerc`.

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

- `!BBOX!` — for native spatial geometries, replaced with `ST_Intersects(<geom_field>, ST_GeomFromText('POLYGON(...)', <layer_srid>))` using the tile's buffered extent in the layer's SRID. WKT columns are wrapped with the same SRID. For MOS, replaced with an indexed bounds-columns overlap predicate in the raw packed coordinate units. For MOS custom SQL the `!BBOX!` token is **required** and expands into the bounds-columns predicate (the bounds column names are configurable via `bbox_minx_fieldname` / `bbox_maxx_fieldname` / `bbox_miny_fieldname` / `bbox_maxy_fieldname`, defaults `MINX`/`MAXX`/`MINY`/`MAXY`); the provider verifies the token at registration.
- `!ZOOM!`, `!Z!` — the tile's zoom (Z) value.
- `!X!`, `!Y!` — the tile's X/Y values.
- `!SCALE_DENOMINATOR!` — scale denominator assuming 90.7 DPI.
- `!PIXEL_WIDTH!`, `!PIXEL_HEIGHT!` — pixel size in meters for 256x256 tiles.
- `!ID_FIELD!` — the layer's id field name.
- `!GEOM_FIELD!` — the layer's geometry field name.
- `!GEOM_TYPE!` — the layer's geometry type name (POINT, LINESTRING, ...).

The registration probe always executes custom SQL without a spatial filter:
`!BBOX!` is neutralized to `1=1`, position / zoom tokens (`!X!`, `!Y!`,
`!Z!`, `!SCALE_DENOMINATOR!`, `!PIXEL_WIDTH!`, `!PIXEL_HEIGHT!`) are
permissive, and the probe is capped at 16 sample rows. Tegola decodes the
returned geometries and applies the tile intersection check in memory,
avoiding MySQL spatial functions on an unknown format (including MOS); the
layer is registered with its configured CRS. Note that the scale tokens are
computed in Web Mercator meters and are meaningful only for metric CRSs (see
[docs/crs.md](../../docs/crs.md)). Table-canonical MapplGIS detection never
applies to custom `sql` layers, and no system-info record is applied from
result rows: a MOS custom SQL layer must configure `srid` or `crs_defn`
explicitly (a missing CRS is a startup error), while `mos_precision` /
`mos_units` are optional with the normative paired defaults.

## Empty layers

Layers (table or custom SQL) that currently return 0 rows produce a warning
and remain registered without an inferred geometry type. They are queried
normally when a later request returns data; an empty layer does not prevent the
provider from starting. Until the first decodable geometry is seen, the layer
uses the same safe in-memory filtering path as custom SQL whose geometry type
is not yet resolved so a later
geometry header can establish the source CRS without an incorrect startup
assumption.

## Raw geometry formats need bounds columns (audit P6-19)

Layers using `geometry_format` `wkb`/`wkt`/`mos` store raw geometry, so a
bounds predicate cannot be pushed down and evaluated cheaply: every tile
request scans the full table and filters geometries in memory (O(rows) per
tile). A registration-time warning is logged per affected layer. The
recommended setup is the raw/MOS bounds-columns one: configure
`bbox_minx_fieldname`/`bbox_maxx_fieldname`/`bbox_miny_fieldname`/
`bbox_maxy_fieldname` (precomputed column bounds) and use a bounds-backed
MOS custom query carrying `!BBOX!`, which expands to a server-side
comparison over those columns. Alternatively use a native geometry column
(`geometry_format` unset) so MySQL spatial predicates apply.

## Known limitations

- Only 2D geometries are supported by the decoder for MVT encoding (matching MariaDB's capabilities).
- Custom SQL used in derived-table inspection must be aliasable — the inspection query wraps it as `SELECT geom FROM (<your sql>) AS __tegola_inspection LIMIT 1`.
