# MySQL / MariaDB Provider

The `mysql` provider serves MVT tiles from spatial tables in MySQL (5.7+/8.0+) and MariaDB (10.2+). It supports both the `tablename` and custom `sql` layer configuration modes, plus the full set of SQL tokens available in the postgis provider.

## Config

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
geometry_format = "auto"    # optional: auto (default) | mysql | mariadb | wkb
max_connections = 100       # optional, default 100

[[providers.layers]]
name = "buildings"
tablename = "buildings"
# id_fieldname = "fid"      # optional, default "fid"
# geometry_fieldname = "geom" # optional, default "geom"
# fields = ["height", "name"]

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

## SRID handling

SRID resolution order (highest priority first):

1. Layer-level `srid` config value.
2. Provider-level `srid` config value (if explicitly configured).
3. SRID decoded from the sampled geometry header at registration (native formats only).
4. Default `3857` (Web Mercator).

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

## SQL tokens

The following tokens are supported in custom `sql` (case-insensitive) and behave identically to the postgis provider:

- `!BBOX!` — replaced with `ST_Intersects(<geom_field>, ST_GeomFromText('POLYGON(...)'))` using the tile's buffered extent in the layer's SRID.
- `!ZOOM!`, `!Z!` — the tile's zoom (Z) value.
- `!X!`, `!Y!` — the tile's X/Y values.
- `!SCALE_DENOMINATOR!` — scale denominator assuming 90.7 DPI.
- `!PIXEL_WIDTH!`, `!PIXEL_HEIGHT!` — pixel size in meters for 256x256 tiles.
- `!ID_FIELD!` — the layer's id field name.
- `!GEOM_FIELD!` — the layer's geometry field name.
- `!GEOM_TYPE!` — the layer's geometry type name (POINT, LINESTRING, ...).

## Empty layers

Layers (table or custom SQL) that currently return 0 rows produce a warning and are skipped rather than preventing the server from starting. If *every* configured layer is empty, the provider fails to start, since that almost always indicates a misconfiguration.

## Known limitations

- Only 2D geometries are supported by the decoder for MVT encoding (matching MariaDB's capabilities).
- Custom SQL used in derived-table inspection must be aliasable — the inspection query wraps it as `SELECT geom FROM (<your sql>) AS __tegola_inspection LIMIT 1`.
