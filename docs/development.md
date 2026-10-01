[← Configuration](configuration.md) · [Back to README](../README.md) · [Provider Contract →](provider-contract.md)

# Development, Debugging, and Builds

This guide covers the CLI, local debugging, and source builds.

## CLI overview

The root command supports `cache`, `help`, `serve`, and `version`. Use `tegola --help` or `tegola <command> --help` for the installed binary's current flags.

```text
tegola is a vector tile server
Usage:
  tegola [command]

Available Commands:
  cache       Manipulate the tile cache
  help        Help about any command
  serve       Use tegola as a tile server
  version     Print the version number of tegola
```

## SQL Debugging

The following environment variables can be used for debugging:

`TEGOLA_SQL_DEBUG` selects the SQL debug information to output. It accepts two values:

- `LAYER_SQL` will print layer SQL as they are parsed from the config file.
- `EXECUTE_SQL` will print SQL that is executed for each tile request, and the number of items it returns or an error.

### Run with SQL debugging

```bash
$ TEGOLA_SQL_DEBUG=LAYER_SQL tegola serve --config=/path/to/conf.toml
```

## Client-side debugging

A debug layer can show tile outlines and Z/X/Y values. Add `debug=true` to the tile URL template to include it:

```
http://localhost:8080/maps/mymap/{z}/{x}/{y}.vector.pbf?debug=true
```

The requested tile will include a `debug` layer with two features:

- `debug_outline` is a line feature that traces the border of the tile
- `debug_text` is a point feature in the middle of the tile. Its `zxy` tag contains the `Z`, `X`, and `Y` values, formatted as `Z:0, X:0, Y:0`.

## Building from source

Tegola is written in [Go](https://golang.org/) and requires [Go 1.26.7](https://go.dev/dl/) or higher to compile from source.
(CI builds with the Go version pinned in `go.mod`.)
To build tegola from the source, make sure you have Go installed and have cloned the repository.
Navigate to the repository then run the following command:

```bash
go generate ./... && cd cmd/tegola/ && go build -mod vendor
```

You will now have a binary named `tegola` in the current directory which is ready to run; follow the [Quick Start](../README.md#quick-start) after preparing a configuration file.

### Build flags

The following build flags can be used to turn off certain features of tegola:

- `noAzblobCache` - turn off the Azure Blob cache backend.
- `noS3Cache` - turn off the AWS S3 cache backend.
- `noRedisCache` - turn off the Redis cache backend.
- `noPostgisProvider` - turn off the PostGIS data provider.
- `noGpkgProvider` — disable the GeoPackage provider. GeoPackage uses CGO and is also disabled when `CGO_ENABLED=0`.
- `noHanaProvider` - turn off the SAP HANA data provider.
- `noMysqlProvider` - turn off the MySQL data provider.
- `noViewer` - turn off the built-in viewer.
- `pprof` - enable [Go profiler](https://golang.org/pkg/net/http/pprof/). Set `TEGOLA_HTTP_PPROF_BIND` to start the profile server (for example, `TEGOLA_HTTP_PPROF_BIND=localhost:6060`).
- `noPrometheusObserver` - turn off support for the Prometheus metrics endpoint.
- `nopgxregisterdefaulttypes` - skip pgx default type registration in the PostGIS provider (rarely needed; used to work around pgx driver conflicts).

Example of using the build flags to turn off the Redis cache backend, the GeoPackage provider and the built-in viewer.

```bash
go build -tags 'noRedisCache noGpkgProvider noViewer'
```

### Setting version information

Use linker flags to set the version information embedded in the binary:

```bash
# first set some env to make it easier to read:
BUILD_PKG=github.com/alexeydott/tegola/internal/build
VERSION="v0.21.0-fork.1+git.$(git rev-parse --short=8 HEAD)"
GIT_BRANCH=$(git branch --no-color --show-current)
GIT_REVISION=$(git log HEAD --oneline | head -n 1 | cut -d ' ' -f 1)

# build the go binary
go build -ldflags "-w -X ${BUILD_PKG}.Version=${VERSION} -X ${BUILD_PKG}.GitRevision=${GIT_REVISION} -X ${BUILD_PKG}.GitBranch=${GIT_BRANCH}"
```

## See Also

- [Windows release builds](windows-release.md) — Windows packaging workflow
- [Contributing](../CONTRIBUTING.md) — build, test, and contribution guidance
- [Configuration](configuration.md) — provider and cache setup
