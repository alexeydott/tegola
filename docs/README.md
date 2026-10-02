[Back to README](../README.md) · [API Reference →](api.md)

# Documentation Index

Tegola documentation covers HTTP behavior, configuration, provider contracts, geometry, builds, and the current maintenance record. Historical review details remain in the [audit archive](audit/tegola_review_part12.md); see [maintenance status](maintenance-status.md) for current disposition.

## Guides

* [API reference](api.md) — HTTP endpoints and tile operations.
* [Queryables and filtering](filtering.md) — public scalar catalogs and the initial CQL2 text profile.
* [Configuration](configuration.md) — TOML, providers, caches, and environment variables.
* [Development and builds](development.md) — debugging, build flags, and source builds.
* [Provider contract](provider-contract.md) — settings and runtime behavior shared by standard providers.
* [CRS contract](crs.md) — coordinate reference systems, SRIDs, and reprojection.
* [Geometry formats](geometry-formats.md) — WKB, WKT, MOS, and collection behavior.
* [Maintenance status](maintenance-status.md) — current work disposition and verification boundaries.
* [Windows release builds](windows-release.md) — Windows build and packaging workflow.

## Feature API and Historical Baselines

The implemented application profile is described in the [API reference](api.md)
and [publication configuration](configuration.md). Backend guides describe the
admitted tested profiles and their limits; implemented endpoints do not declare
OGC conformance. The
following architecture and migration pages preserve the historical source baseline.

* [Feature-service source baseline](architecture/feature-service.md) - existing contracts and integration points.
* [OGC API Features editions](architecture/ogc-api-features.md) - selected normative sources and verification boundary.
* [Jivan route inventory](migration/jivan-feature-matrix.md) - registered resources and internal/deployment behavior.

## Feature-service contribution process

* [Team roles](development/ogc-team-roles.md) - responsibilities, independent reviewers and escalation.
* [Review and gates](development/ogc-review-gates.md) - required evidence and dependency transitions.

## Provider Guides

* [GeoPackage](../provider/gpkg/README.md) — native binary, raw tables, RTree, and bounds columns.
* [PostGIS](../provider/postgis/README.md) — PostGIS and the `mvt_postgis` variant.
* [MySQL / MariaDB](../provider/mysql/README.md) — provider configuration and behavior.
* [SAP HANA](../provider/hana/README.md) — HANA and the `mvt_hana` variant.
* [Third-party modules](../third_party/README.md) — published `geom` and `proj` modules and consumer migration.

## Package References

Tegola is organized as a Go package monolith. The main request path crosses
these packages; the CLI initializes configuration and registrations, then the
server handles requests against the configured atlas:

```text
cmd/tegola → config + provider/cache registration → atlas → server
                                              server → provider(s) → MVT response
```

| Package | Role and where to start |
|---|---|
| `cmd/` | Executable entry points and Cobra commands. Start at [`cmd/tegola/main.go`](../cmd/tegola/main.go), then follow command setup under `cmd/tegola/cmd/`; `cmd/tegola_lambda/` is the Lambda entry point. |
| `config/` | Loads and validates TOML configuration consumed by the command setup. |
| `atlas/` | Holds configured maps, provider layers, cache and observer integrations; it connects request handling to registered runtime components. |
| `server/` | HTTP router, endpoint handlers, middleware, tile cache behavior, and embedded viewer routes. Start at [`server/server.go`](../server/server.go). |
| `provider/` | Provider interfaces and standard SQL/spatial backends such as `postgis/`, `gpkg/`, `mysql/`, and `hana/`. Standard providers return features for Tegola to process. |
| `mvtprovider/` | Database-side MVT provider implementations. They return encoded tiles and bypass standard feature geometry processing. |
| `cache/` | Cache interfaces, tile keys, and backend implementations such as `memory/`, `file/`, `redis/`, and `multilevel/`. Backend-specific setup notes are under `cache/<backend>/README.md`. |
| `observability/` | Observer interfaces and integrations, including Prometheus metrics. |
| `basic/`, `maths/`, `mapbox/`, `mos/` | Geometry types and operations, math helpers, MVT encoding, and MOS support used across providers and tile generation. |
| `internal/` | Shared implementation details for this module, including build metadata, environment parsing, logging, and SQL token handling. |
| `ui/` | Source assets for the embedded web viewer. |

For focused package contracts, see [`provider/geometrycodec`](../provider/geometrycodec/doc.go) for shared geometry decoding and MOS configuration, and [`provider/crsconfig`](../provider/crsconfig/doc.go) for CRS resolution and synthetic SRID registration.

## Repository Policies and History

* [Contributing](../CONTRIBUTING.md) — build, test, and contribution process.
* [Security policy](../SECURITY.md) — vulnerability reporting.
* [Changelog](../CHANGELOG.md) — release history.
* [Upstream provenance](../UPSTREAM.md) — fork changes, maintenance, and deferred work.

## See Also

- [API reference](api.md) — start with the HTTP endpoints
- [Configuration](configuration.md) — configure providers and caches
- [Maintenance status](maintenance-status.md) — current project status
