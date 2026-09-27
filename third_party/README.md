# third_party — in-repo dependency forks

The upstream modules `github.com/go-spatial/proj` and
`github.com/go-spatial/geom` carry fork-specific fixes. The fixes used to live
only as edits inside `vendor/`, which broke `-mod=mod` builds and any consumer
of the tegola module. They now live here as real modules wired in through
`replace` directives in the root `go.mod`. This fixes builds from a checkout,
not automatic downstream dependency resolution:

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

The `go` directive of the fork go.mod files must stay at or below
the main module's `go` directive. `geom` requires Go 1.23.4 (the minimum
required by protobuf v1.36.12); `proj` requires Go 1.21. The `require` versions in the root
`go.mod` still record the upstream baselines (`geom v0.1.0`, `proj v0.3.0`).

Each nested module has its own dependency graph and checksums; the root's
`go.sum` and `vendor/` do not satisfy a nested module's dependencies. After
changing a nested module's requirements, run `go mod tidy` in that module
and commit its `go.mod` and `go.sum`, then regenerate the root vendor tree.
CI builds and tests the nested modules with `-mod=readonly` so missing
requirements or checksums cannot be silently repaired.

## Consuming this fork as a Go module (external consumers)

### Inventory and remaining limitation

| Main module | Local replacements |
| --- | --- |
| Root `github.com/go-spatial/tegola` | `geom` and `proj`, at the paths above |
| `third_party/go-spatial/geom` | None; standalone builds use upstream `proj v0.3.0` |
| `third_party/go-spatial/proj` | None |

Go only honours `replace` directives from the **main** module of a build.
A downstream consumer inherits neither these replacements nor this repository's
`vendor/`. Without its own replacements, it selects the *unpatched* upstream
modules. This can fail to compile against protobuf APIv2, or lose the datum-shift
and MVT degenerate-geometry fixes. Also, a downloaded root module zip excludes
the nested modules: do not point replacements into the module cache and expect
`third_party` to be present.

**Unconfigured, remote-only consumption remains unsupported.** Keeping local
forks is intentional: dropping the replacements discards required patches;
re-homing geom packages changes public Go type identity; and separately
published, versioned fork modules are not currently available as a verified
dependency source. A `go.work` file alone would not fix downstream consumers
either, because workspaces are not inherited. The bounded solution is an
explicit checkout-based consumer setup, checked in CI, while preserving
offline root builds. Fully transparent consumption still requires upstreaming
the fixes or publishing and adopting suitable versioned modules.

### Supported checkout-based consumer setup

Keep a complete checkout of this fork at a pinned commit outside the consumer
module. From the consumer directory, use its own `go.mod` to select all three
modules. For example, with sibling `consumer` and `tegola` directories:

```sh
go mod edit -require=github.com/go-spatial/tegola@v0.0.0
go mod edit -replace=github.com/go-spatial/tegola=../tegola
go mod edit -replace=github.com/go-spatial/geom=../tegola/third_party/go-spatial/geom
go mod edit -replace=github.com/go-spatial/proj=../tegola/third_party/go-spatial/proj
go mod tidy
go list -m all
go build -mod=readonly ./...
go test -mod=readonly ./...
```

The `v0.0.0` version is a placeholder for a local replacement, not a published
release. Paths are relative to the **consumer's** `go.mod`, not Tegola's.
Use quoted absolute paths if the checkouts are not siblings. Imports remain
`github.com/go-spatial/tegola`, even though the checkout is from
`alexeydott/tegola`. Confirm the module listing selects the three local
directories, not upstream geom/proj.

The initial tidy/build needs network access or an already populated module
cache. To make the consumer independently buildable offline, vendor its
dependencies while those checkouts and downloaded modules are available:

```sh
go mod vendor
go build -mod=vendor ./...
go test -mod=vendor ./...
```

Commit the consumer's `go.mod`, `go.sum`, and `vendor/` according to its policy.
Explicit vendor mode then uses its own vendored sources; it does not read the
replacement source directories. Switching back to module mode or regenerating
vendor still needs the pinned checkouts. `go mod verify` verifies downloaded
modules, **not** local replacements: the checkout's commit and the consumer's
vendor review provide provenance for those.

### Automated portability check

Run from the Tegola checkout root:

```sh
go run -mod=vendor ./ci/check-dependencies
go -C third_party/go-spatial/geom build -mod=readonly ./...
go -C third_party/go-spatial/geom test -mod=readonly ./...
go -C third_party/go-spatial/proj build -mod=readonly ./...
go -C third_party/go-spatial/proj test -mod=readonly ./...
```

The check rejects changed root replacements, extra/missing nested modules, or
any nested replacement. It creates a temporary consumer **outside** this
checkout with the three explicit replacements, disables workspace inheritance,
and tests a PostGIS import, public geom type compatibility, protobuf APIv2,
degenerate MVT encoding, and a known datum-shift result. It lists and verifies
the consumer module graph, vendors it, then builds and tests with
`GOPROXY=off`, `GOSUMDB=off`, and `GOTOOLCHAIN=local`. Temporary files are removed
afterward. The initial consumer setup needs network access or cached modules;
it is not a remote-publication or empty-cache offline test.

CI also builds/tests both nested modules with `-mod=readonly` and checks that
their manifests/checksums stay unchanged. Nested builds deliberately do not
use the root vendor tree. For a root-vendored MVT package check, use
`go test -mod=vendor github.com/go-spatial/geom/encoding/mvt/...`; there is no
root `mvt/` directory.
