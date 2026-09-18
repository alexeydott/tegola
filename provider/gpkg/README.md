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
- `sql` (string): [*Required] custom SQL to use use. Required if `tablename` is not defined. Supports the following WHERE-clause tokens:
  - !BBOX! - [Required] will be replaced with the bounding box of the tile before the query is sent to the database.  To support this token, your custom SQL must do a couple of things. 
    - You must join your feature table to the spatial index table: i.e. `JOIN feature_table ft rtree_feature_table_geom si ON ft.fid = rt.si`
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