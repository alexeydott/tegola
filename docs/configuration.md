[← API Reference](api.md) · [Back to README](../README.md) · [Development and Builds →](development.md)
# Configuration

The tile cache is independent of the data provider, so the same cache
configuration works for PostGIS, GeoPackage, MySQL, and other providers. For a
process-local in-memory cache:

```toml
[cache]
type = "memory"
# Optional: do not cache tiles above this zoom.
max_zoom = 18
# Optional: lazy expiration in seconds; 0 keeps entries until restart.
ttl = 3600
```

The memory cache is volatile and shared by all maps and layers in the running
Tegola process. `max_zoom` controls which tiles are inserted, while total
memory usage is limited only by available process memory; use a persistent or
distributed backend when cache contents must survive restarts or be shared
between instances.

For a two-level memory-plus-file cache, configure each backend independently:

```toml
[cache]
type = "multilevel"

[cache.memory]
max_zoom = 18
ttl = 60

[cache.file]
basepath = "./cache/maps"
max_zoom = 22
ttl = 86400
```

The `multilevel` cache requires both nested sections. The `memory` section
supports `max_zoom` (optional, defaults to the maximum Tegola zoom) and `ttl`
(optional seconds, `0` means no expiration until restart). The `file` section
requires `basepath` and also supports `max_zoom` (optional, defaults to the
maximum Tegola zoom) and `ttl` (optional seconds, `0` means no expiration).

Reads check memory first and then file; a file hit is promoted to memory.
Writes and purges are sent to both levels. A failed promotion is logged while
the successful file hit is still returned. A non-cancellation memory read
error is logged and falls back to file; if both reads fail, both errors are
returned. Write and purge errors from either level are returned to the caller.

The Tegola config file uses the [TOML](https://github.com/toml-lang/toml) format. The following example shows how to configure a `mvt_postgis` data provider. The `mvt_postgis` provider uses PostGIS's `ST_AsMVT()` function for the encoding of the vector tile.

Under the `maps` section, map layers are associated with data provider layers and their `min_zoom` and `max_zoom` values are defined.

### Example config using Postgres 12+ / PostGIS 3.0 ST_AsMVT():

```toml
# register a MVT data provider. MVT data providers have the prefix "mvt_" in their type
# note mvt data providers can not be conflated with any other providers of any type in a map
# thus a map may only contain a single mvt provider.
[[providers]]
name = "my_postgis"         # provider name is referenced from map layers (required).
type = "mvt_postgis"        # the type of data provider must be "mvt_postgis" for this data provider (required)
uri = "postgresql://tegola:<password>@localhost:5432/tegola?ssl_mode=prefer" # database connection string

  [[providers.layers]]
  name = "landuse"
  # MVT data provider must use SQL statements
  # this table uses "geom" for the geometry_fieldname and "gid" for the id_fieldname so they don't need to be configured
  # Wrapping the geom with ST_AsMVTGeom is required.
  # If you want to use the configurable parameters defined in maps.params make sure to include the token in the SQL statement
  sql = "SELECT ST_AsMVTGeom(geom,!BBOX!) AS geom, gid FROM gis.landuse WHERE geom && !BBOX! !PARAM!"

# maps are made up of layers
[[maps]]
name = "zoning"                           # used in the URL to reference this map (/maps/zoning)

  [[maps.layers]]
  name = "landuse"                        # name is optional. If it's not defined the name of the ProviderLayer will be used.
  provider_layer = "my_postgis.landuse"   # must match a data provider layer
  min_zoom = 10                           # minimum zoom level to include this layer
  max_zoom = 16                           # maximum zoom level to include this layer

  # configure addition URL parameters: /maps/:map_name/:layer_name/:z/:x/:y?param=value
  # which will be passed to the database queries
  [[maps.params]]
  name          = "param"         # name used in the URL
  token         = "!PARAM!"       # token to replace in providers.layers.sql query
  type          = "string"        # one of: int, float, string, bool
  sql           = "AND param = ?" # SQL to replace the token in the query. ? will be replaced with a parameter value. If omitted, defaults to "?"
  # if neither default_value nor default_sql is specified, the URL parameter is required to be present in all queries
  # either
  default_value = "value"         # if parameter is not specified, this value will be passed to .sql parameter
  # or
  default_sql   = " "             # if parameter is not specified, this value will replace the .sql parameter. Useful for omitting query entirely
```

- More information on PostgreSQL SSL modes can be found [here](https://www.postgresql.org/docs/current/libpq-ssl.html).
- More information on the `mvt_postgis` provider can be found [here](../mvtprovider/postgis)

## HTTP bind address

`webserver.port` and the `serve --port` option take an address, not a bare
port number:

```toml
[webserver]
port = ":8083"
```

```powershell
.\tegola.exe serve --config .\conf\moek.toml --port :8083
# Bind only to localhost when testing:
.\tegola.exe serve --config .\conf\moek.toml --port 127.0.0.1:18083 --no-cache
```

An explicit `--port` overrides `webserver.port`. `--port 8083` fails with
`listen tcp: address 8083: missing port in address`, even if the TOML value
is correct. Omit the flag to use the configured address.

For joined MOS queries and registration timeouts, see
[bounds-backed MOS SQL](geometry-formats.md#bounds-backed-mos-sql-bbox-over-bounds-columns)
and [MySQL troubleshooting](../provider/mysql/README.md#troubleshooting-mos-custom-sql).

## Environment Variables

### Config TOML

Environment variables can be injected into the configuration file. One caveat is that the injection has to be within a string, though the value it represents does not have to be a string.

The above config example could be written as:

```toml
# register data providers
[[providers]]
name = "test_postgis"
type = "mvt_postgis"
uri = "${POSTGIS_CONN_STR}"  # database connection string
srid = 3857
max_connections = "${POSTGIS_MAX_CONN}"
```

### Runtime options

For standard providers other than `mvt_postgis`, `TEGOLA_OPTIONS` accepts comma- or space-delimited options:

- `DontSimplifyGeo` turns off simplification for all layers.
- `SimplifyMaxZoom={{int}}` sets the maximum zoom at which simplification applies (14 by default).

## See Also

- [API Reference](api.md) — HTTP endpoints and query parameters
- [Provider contract](provider-contract.md) — shared provider configuration
- [CRS contract](crs.md) — `srid` and `crs_defn` behavior
