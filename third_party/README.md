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
- `encoding/mvt/vector_tile/vector_tile.pb.go`: regenerated with
  protoc-gen-go v1.36.12 (`google.golang.org/protobuf` APIv2); the fork no
  longer depends on `github.com/golang/protobuf`. `vector_tile.proto` gained
  the full `go_package` path; wire format is unchanged.
- `encoding/mvt/layer.go` `vectorTileValue`: unsupported value types stash
  their raw bytes via `ProtoReflect().SetUnknown` (the v2 replacement for
  assigning the old `XXX_unrecognized` field).
- `encoding/mvt/decode.go`: `github.com/arolek/p` usage dropped in favour of
  a local variable (dependency hygiene, no behaviour change).

The `go` directive of the fork go.mod files (`go 1.21`) must stay at or below
the main module's `go` directive. The `require` versions in the root
`go.mod` still record the upstream baselines (`geom v0.1.0`, `proj v0.3.0`).

## Consuming this fork as a Go module (external consumers)

Go only honours `replace` directives from the **main** module of a build.
A consumer that imports this fork as a dependency does **not** inherit the
filesystem `replace` directives above, so it would build against the
*unpatched* upstream `geom`/`proj` modules and silently lose both the
`+towgs84` datum-shift reprojection fix (proj) and the MVT
degenerate-geometry guards (geom).

Until the fixes are upstreamed, external consumers (e.g. go-wfs/Jivan)
**must** replicate the replaces in their own `go.mod`:

```
require github.com/go-spatial/tegola <fork-version>

replace (
    github.com/go-spatial/geom => github.com/alexeydott/geom <fork-revision>
    github.com/go-spatial/proj => github.com/alexeydott/proj <fork-revision>
)
```

Publish the fork modules under the same import paths at accessible VCS
revisions (or point the replaces at your own local checkouts of
`third_party/go-spatial/geom` / `third_party/go-spatial/proj`).

Smoke-check a consumer module:

1. Create a module outside this repository that imports a tegola provider
   package (e.g. `provider/postgis`).
2. `go list -m all` — confirm `github.com/go-spatial/geom` and
   `github.com/go-spatial/proj` resolve to the fork revisions, not upstream.
3. `go build ./...` — must succeed with **no reference** to this checkout's
   `./third_party` paths.
4. Verify patched behaviour: a `+towgs84` projection converts (proj fix) and
   MVT encoding of a clipped degenerate LineString does not panic (geom fix).
