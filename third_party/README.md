# third_party — in-repo dependency forks

The upstream modules `github.com/go-spatial/proj` and
`github.com/go-spatial/geom` carry fork-specific fixes. The fixes used to live
only as edits inside `vendor/`, which broke `-mod=mod` builds and any consumer
of the tegola module. They now live here as real modules wired in through
`replace` directives in the root `go.mod`:

```
github.com/go-spatial/geom => ./third_party/go-spatial/geom
github.com/go-spatial/proj => ./third_party/go-spatial/proj
```

`go mod vendor` copies the fork sources into `vendor/`, so `-mod=vendor` and
`-mod=mod` builds compile identical code. When changing a fork, edit the files
under `third_party/`, then run `go mod vendor`.

## go-spatial/proj (base: v0.3.0)

- `CustomProjection` / `RemoveCustomProjection`: registering or removing a
  projection now invalidates the cached conversion (the conversion cache is
  keyed by EPSG code only).
- `datum.go`: apply `towgs84=` 3/7-parameter datum shifts
  (`datumToWGS84`/`datumFromWGS84`) around the forward/inverse conversion, so
  custom projections with datum parameters reproject correctly.

## go-spatial/geom (base: v0.1.0)

- `encoding/mvt/feature.go` `encodeGeometry`: skip degenerate
  LineString/MultiLineString components with fewer than two vertices after
  clip/simplify instead of panicking with an index-out-of-range.
- `encoding/mvt/prepare.go` `preparePolygon`: guard against short/nil rings
  returned by `preparelinestr` when a ring collapses below two unique points.

The `go` directive of the fork go.mod files (`go 1.21`) must stay at or below
the main module's `go` directive. The `require` versions in the root
`go.mod` still record the upstream baselines (`geom v0.1.0`, `proj v0.3.0`).
