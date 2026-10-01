[← Documentation Index](README.md) · [Back to README](../README.md) · [Configuration →](configuration.md)

# API Reference

This page documents the HTTP endpoints exposed by Tegola.

## Server Endpoints

```
/
```

The server root will display the built-in viewer with an automatically generated style. For example:

![tegola built in viewer](https://raw.githubusercontent.com/go-spatial/tegola/v0.4.0/docs/screenshots/built-in-viewer.png "tegola built in viewer")

```
/maps/:map_name/:z/:x/:y
```

Return vector tiles for a map. The URI supports the following variables:

- `:map_name` is the name of the map as defined in the `config.toml` file.
- `:z` is the zoom level of the map.
- `:x` is the column of the tile at the zoom level.
- `:y` is the row of the tile at the zoom level.

```
/maps/:map_name/:layer_name/:z/:x/:y
```

Return vector tiles for a map layer. The URI supports the same variables as the map URI with the additional variable:

- `:layer_name` is the name of the map layer as defined in the `config.toml` file.

Tile cache operations can be requested on the map-layer endpoint with the `tile`
query parameter. The query parameter is never included in the tile cache key:

```
/maps/:map_name/:layer_name/:z/:x/:y?tile=status
/maps/:map_name/:layer_name/:z/:x/:y?tile=update
/maps/:map_name/:layer_name/:z/:x/:y?tile=getupdated
```

- `?tile=status` returns JSON describing whether the requested tile is cached
  and whether its aligned 8x8 metatile is currently being regenerated:

  ```json
  {
    "map": "roads",
    "layer": "primary",
    "z": 12,
    "x": 1234,
    "y": 567,
    "cached": true,
    "updating": false,
    "metatile": [1224, 560, 8, 8]
  }
  ```

  The `metatile` array contains the metatile origin (`x`, `y`) and its
  effective width and height. At low zoom levels the dimensions can be
  smaller than 8x8 because they are clipped to the valid tile range.
- `?tile=update` schedules regeneration of every tile in the aligned 8x8
  metatile in the background and returns `202 Accepted` immediately. The
  response body is empty. Regeneration is single-flight per metatile: requests
  that arrive while a regeneration is running join it instead of starting
  another one. `?tile=status` reports `"updating": true` while a regeneration
  is running. If the background regeneration fails it is logged at WARN and
  the next request retries it.
- `?tile=getupdated` renders and returns the requested tile immediately as an
  MVT response, and schedules the same background metatile regeneration as
  `?tile=update`.

The `tile` operation cannot be combined with other query parameters.
Operation responses are marked `Cache-Control: no-store`.

A cache backend must be configured for `?tile=update` and `?tile=getupdated`
to work; without a cache these requests fail. `?tile=status` works without a
cache and reports `"cached": false` in that case.

Tile regeneration can also be forced with the `dirty` query parameter:

```
/maps/:map_name/:layer_name/:z/:x/:y?dirty
/maps/:map_name/:layer_name/:z/:x/:y?dirty=1
/maps/:map_name/:layer_name/:z/:x/:y?dirty=true
```

- The parameter is detected by its presence; empty values and the values
  `1` / `true` (case-insensitive) force regeneration, other values are
  ignored.
- The regenerated tile is written back to the cache only when `dirty` is the
  sole query parameter. Combined with any other query parameter the request
  bypasses the cache entirely (read and write).

```
/capabilities
```

Return a JSON encoded list of the server's configured maps and layers with various attributes.

```
/capabilities/:map_name
```

Return [TileJSON](https://github.com/mapbox/tilejson-spec) details about the map.

```
/maps/:map_name/style.json
```

Return an automatically generated [Mapbox GL Style](https://www.mapbox.com/mapbox-gl-js/style-spec/) for the configured map.

## See Also

- [Configuration](configuration.md) — server and cache settings
- [Provider contract](provider-contract.md) — provider runtime behavior
- [CRS contract](crs.md) — coordinate reference system behavior
