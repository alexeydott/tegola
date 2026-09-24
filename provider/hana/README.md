# HANA
The HANA provider manages querying for tile requests against an [SAP HANA](https://www.sap.com/products/hana.html) database. The connection between tegola and HANA is configured in a `tegola.toml` file. An example minimum connection config:


```toml
[[providers]]
name = "test_hana"       # provider name is referenced from map layers (required)
type = "hana"            # the type of data provider must be "hana" for this data provider (required)
uri = "hdb://myuser:mypassword@something.hanacloud.ondemand.com:443?" # HANA connection string (required)
```

### Connection Properties

- `uri` (string): [Required] HANA connection string
- `name` (string): [Required] provider name is referenced from map layers
- `type` (string): [Required] the type of data provider. must be "hana" to use this data provider
- `srid` (int): [Optional] The default SRID for the provider. When omitted the SRID is auto-detected from the geometry column. Any numeric SRID known to the database is supported, as well as a synthetic SRID registered via `crs_defn`.
- `crs_defn` (string): [Optional] A PROJ.4 definition of the coordinate reference system, registered internally under a synthetic SRID. Wins over `srid` on the same level. Requires a raw `geometry_format` (`wkb`/`wkt`/`mos`) and is not supported for `mvt_hana`. See [docs/crs.md](../../docs/crs.md).
- `geometry_format` (string): [Optional] Provider-level default geometry format for layers that do not override it: `wkb`, `wkt` or `mos`. Empty/unset means HANA's native `ST_Geometry` handling (`ST_AsBinary`). See [docs/geometry-formats.md](../../docs/geometry-formats.md).
- `mos_precision` (int): [Optional] Decimal digits carried by MOS coordinates. Only applies when `geometry_format = "mos"`.
- `mos_units` (string): [Optional] Packed linear unit of MOS coordinates: `mm`, `cm`, `dm`, `m` or `km`. Only applies when `geometry_format = "mos"`.

#### Connection string properties

**Example**

```
# {protocol}://{user}:{password}@{host}:{port}/{database}?{options}=
hdb://myuser:mypassword@something.hanacloud.ondemand.com:443?TLSInsecureSkipVerify&timeout=3600&max_connections=30
```

**Options**

- `timeout`: [Optional] Driver side connection timeout in seconds.
- `TLSRootCAFile` [Optional] Path,- filename to root certificate(s).
- `TLSServerName` [Optional] ServerName to verify the hostname. By setting TLSServerName=host, the provider will set TLSServerName same as 'host' value in `uri`.
- `TLSInsecureSkipVerify` [Optional] Controls whether a client verifies the server's certificate chain and host name.
- `max_connections`: [Optional] The max connections to maintain in the connection pool. Defaults to 100. 0 means no max.
- `max_connection_idle_time`: [Optional] The maximum time an idle connection is kept alive. Defaults to "30m".
- `max_connection_life_time` [Optional] The maximum time a connection lives before it is terminated and recreated. Defaults to "1h".

## Provider Layers
In addition to the connection configuration above, Provider Layers need to be configured. A Provider Layer tells tegola how to query HANA for a certain layer. An example minimum config:

```toml
[[providers.layers]]
name = "landuse"
# this table uses "geom" for the geometry_fieldname; set id_fieldname because
# the HANA provider does not infer an id column by default
tablename = "gis.zoning_base_3857"
id_fieldname = "gid"
```

### Provider Layers Properties

- `name` (string): [Required] the name of the layer. This is used to reference this layer from map layers.
- `tablename` (string): [*Required] the name of the database table to query against. Required if `sql` is not defined.
- `geometry_fieldname` (string): [Optional] the name of the filed which contains the geometry for the feature. defaults to `geom`.
- `id_fieldname` (string): [Optional] the name of the feature id field. Defaults to empty (no id attribute is attached to features unless configured). See [docs/provider-contract.md](../../docs/provider-contract.md).
- `fields` ([]string): [Optional] a list of fields to include alongside the feature. Can be used if `sql` is not defined.
- `srid` (int): [Optional] the SRID of the layer. Any numeric SRID known to the database is supported, as well as a synthetic SRID registered via `crs_defn`.
- `crs_defn` (string): [Optional] layer-level PROJ.4 CRS override; wins over `srid` on the same level. Requires a raw `geometry_format` (`wkb`/`wkt`/`mos`) and is not supported for `mvt_hana`. See [docs/crs.md](../../docs/crs.md).
- `geometry_format` (string): [Optional] layer-level geometry format override: `wkb`, `wkt` or `mos`. Overrides the provider-level default.
- `mos_precision` / `mos_units` (int / string): [Optional] layer-level MOS overrides (only with `geometry_format = "mos"`). Explicit values always win over the `MapplGIS LayerInfo` self-description blob. See [docs/geometry-formats.md](../../docs/geometry-formats.md).
- `geometry_type` (string): [Optional] the layer geometry type. If not set, the table will be inspected at startup to try and infer the gemetry type. Valid values are: `Point`, `LineString`, `Polygon`, `MultiPoint`, `MultiLineString`, `MultiPolygon`, `GeometryCollection`.
- `sql` (string): [*Required] custom SQL to use use. Required if `tablename` is not defined. Supports the following tokens:
  - `!BBOX!` - [Required] will be replaced with the bounding box of the tile before the query is sent to the database. `!bbox!` and`!BOX!` are supported as well for compatibilitiy with queries from Mapnik and MapServer styles.
  - `!ZOOM!` - [Optional] will be replaced with the "Z" (zoom) value of the requested tile.
  - `!X!` - [Optional] will be replaced with the "X" value of the requested tile.
  - `!Y!` - [Optional] will be replaced with the "Y" value of the requested tile.
  - `!Z!` - [Optional] will be replaced with the "Z" value of the requested tile.
  - `!SCALE_DENOMINATOR!` - [Optional] scale denominator, assuming 90.7 DPI (i.e. 0.28mm pixel size).
  - `!PIXEL_WIDTH!` - [Optional] the pixel width in meters, assuming 256x256 tiles.
  - `!PIXEL_HEIGHT!` - [Optional] the pixel height in meters, assuming 256x256 tiles.
  - `!ID_FIELD!` - [Optional] the id field name.
  - `!GEOM_FIELD!` - [Optional] the geom field name.
  - `!GEOM_TYPE!` - [Optional] the geom type field name.

`*Required`: either the `tablename` or `sql` must be defined, but not both.

### Common geometry / CRS options

The HANA provider implements the common geometry contract documented in
[docs/provider-contract.md](../../docs/provider-contract.md). The
`geometry_type` layer key, `geometry_format`, `mos_precision`, `mos_units`
and the CRS keys above follow the shared semantics; mixed-content features
under an explicit `geometry_type` are permitted with a one-time warning.

### HANA-specific restrictions

- **Synthetic CRS** (from `crs_defn` at either level, or from a MOS
  `MapplGIS LayerInfo projection`): HANA's native `ST_Geometry` handling
  cannot be used with a synthetic SRID, so such layers must set
  `geometry_format` to `wkb`, `wkt` or `mos`. `mvt_hana` does not support
  synthetic CRS at all.
- **Planar-equivalent SRIDs**: HANA distinguishes round-earth and planar SRS.
  A round-earth SRS is queried through its planar equivalent internally
  (`PLANAR_SRID_OFFSET = 1000000000`); these internal SRIDs must not be
  confused with Tegola's synthetic SRIDs (`>= 340000001`).
- **MOS layers** do not use the database spatial index; filtering is done by
  the in-memory bounding-box check (see
  [docs/geometry-formats.md](../../docs/geometry-formats.md)).

The common provider contract (CRS precedence, `!BBOX!` semantics, startup
inspection, MOS system-info auto-configuration) is documented in
[docs/provider-contract.md](../../docs/provider-contract.md).

**Example minimum custom SQL config**

```toml
[[providers.layers]]
name = "rivers"
# Custom sql to be used for this layer as a sub query. ST_AsBinary and !BBOX! filter are applied automatically.
sql = "(SELECT id, geom FROM gis.rivers) AS sub"
```

## Environment Variable support
Helpful debugging environment variables:

- `TEGOLA_SQL_DEBUG`: specify the type of SQL debug information to output. Supports the following values:
  - `LAYER_SQL`: print layer SQL as they’re parsed from the config file.
  - `EXECUTE_SQL`: print SQL that is executed for each tile request and the number of items it returns or an error.
  - `LAYER_SQL:EXECUTE_SQL`: print `LAYER_SQL` and `EXECUTE_SQL`.

Example:

```
$ TEGOLA_SQL_DEBUG=LAYER_SQL tegola serve --config=/path/to/conf.toml
```

## Testing
Testing is designed to work against a live SAP HANA database. To see how to set up a database check this [github actions script](https://github.com/go-spatial/tegola/blob/master/.github/workflows/on_pr_push.yml). To run the HANA tests, the following environment variables need to be set:

```bash
$ export RUN_HANA_TESTS=yes
$ export HANA_CONNECTION_STRING="hdb://myuser:mypassword@something.hanacloud.ondemand.com:443?TLSInsecureSkipVerify"
```
