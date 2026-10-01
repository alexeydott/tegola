# Tegola

![On push](https://github.com/alexeydott/tegola/actions/workflows/on_pr_push.yml/badge.svg?branch=master)
[![license](http://img.shields.io/badge/license-MIT-red.svg?style=flat)](https://github.com/go-spatial/tegola/blob/master/LICENSE.md)

> A Go vector tile server for serving Mapbox Vector Tiles from spatial data.

Tegola is a fork of [go-spatial/tegola](https://github.com/go-spatial/tegola), based on upstream master after v0.21.0. Fork changes and remaining limitations are recorded in [CHANGELOG.md](CHANGELOG.md) and [UPSTREAM.md](UPSTREAM.md).

## Quick Start

1. Download the binary for your platform from the [release page](https://github.com/alexeydott/tegola/releases).
2. Create a TOML configuration with a provider and map. See [Configuration](docs/configuration.md) and the [provider contract](docs/provider-contract.md).
3. From the directory containing the downloaded binary, start the server with the configuration file:

   **Linux / macOS**

   ```sh
   ./tegola serve --config=config.toml
   ```

   **Windows PowerShell**

   ```powershell
   .\tegola.exe serve --config=config.toml
   ```

4. Open `http://localhost:8080/` for the embedded viewer, or request `http://localhost:8080/capabilities` to inspect configured maps.

For a source build or Windows release package, see [Development and Builds](docs/development.md) and [Windows release builds](docs/windows-release.md).

## Key Features

- Mapbox Vector Tile v2 output and an embedded viewer with an automatically generated style.
- PostGIS, GeoPackage, MySQL/MariaDB, and SAP HANA spatial data providers.
- Memory, file, multilevel, GCS, S3, Redis, and Azure Blob cache backends.
- Geometry processing, CRS reprojection, cache seeding, and cache invalidation.
- HTTP tile operations, AWS Lambda support, HTTPS, and Prometheus observability.

## Example

A configured map named `roads` can be requested as a vector tile:

```text
http://localhost:8080/maps/roads/12/1234/567
```

See the [API reference](docs/api.md) for endpoints and tile-cache operations.

## Documentation

| Guide | Description |
|---|---|
| [Documentation index](docs/README.md) | All project guides and package references |
| [API reference](docs/api.md) | HTTP endpoints and tile operations |
| [Configuration](docs/configuration.md) | TOML, providers, caches, and environment variables |
| [Development and builds](docs/development.md) | Debugging, build flags, and source builds |
| [Provider contract](docs/provider-contract.md) | Shared provider configuration and behavior |
| [CRS contract](docs/crs.md) | Coordinate reference system configuration |
| [Geometry formats](docs/geometry-formats.md) | WKB, WKT, MOS, and geometry behavior |
| [Maintenance status](docs/maintenance-status.md) | Current work disposition and evidence |
| [Windows release](docs/windows-release.md) | Windows release build process |

## Contributing and Support

- [Contributing](CONTRIBUTING.md) — build, tests, and pull request process.
- [Security policy](SECURITY.md) — report a vulnerability.
- [Go module consumers](third_party/README.md) — import paths and dependency migration.
- Looking for a vector tile style editor? Try [fresco](https://github.com/go-spatial/fresco).

## License

MIT. See [LICENSE.md](LICENSE.md).
