[Documentation index](../README.md)

# Feature service: source baseline

Status: baseline prepared on 2026-10-01. Task 2 architecture accepted after independent provider and HTTP-domain PASS; no Feature API is implemented or advertised by this document.

Execution source is Tegola `c65beeb8519f425ff8365c76e54e93baf8e17b07`. This local documentation revision has the same executable source as published `70c53413b06bab23318bac1722852df1fec335e7`. The historical planning revision `18126bb6ca0737b3644f06359ad7703b57bde125` is separated by 134 commits and is comparison evidence only.

## Existing integration points

| Source | Symbols / responsibility |
|---|---|
| [provider/provider.go](../../provider/provider.go) | Tile, Tiler.TileFeatures, LayerFielder, TilerUnion, Register, MVTRegister |
| [provider/feature.go](../../provider/feature.go) | Feature and feature value contract |
| [server/server.go](../../server/server.go) | NewRouter, Start, URIPrefix, hostName |
| [config/config.go](../../config/config.go) | Config and Webserver |
| [cmd/internal/register/providers.go](../../cmd/internal/register/providers.go) | Providers composition |
| [provider/crsconfig/crsconfig.go](../../provider/crsconfig/crsconfig.go) | ResolveProvider, ResolveLayer, ApplySystemInfoCRS |
| [provider/geometrycodec/geometrycodec.go](../../provider/geometrycodec/geometrycodec.go) | shared geometry decoding |
| [basic/epsg.go](../../basic/epsg.go) | SRID/projection registry |
| [server/middleware_tile_http_cache.go](../../server/middleware_tile_http_cache.go) | TileHTTPCacheHandler |
| [UPSTREAM.md](../../UPSTREAM.md) | local exact-SHA verification policy |
| [go.mod](../../go.mod) | published module namespaces and versions |

Tiler.TileFeatures currently accepts context, layer, Tile, provider.Params and a callback. Registration composes TilerUnion values. No current FeatureQuerier API is assumed. Standard providers are GPKG, MySQL/MariaDB, PostGIS and HANA; mvt_postgis/mvt_hana bypass raw-feature construction and remain outside initial scope.

## Baseline changes to preserve

Shared SQL token scanning, identifier/literal quoting, valid unsigned feature IDs, timed probes and existing fixture harness are current contracts. Source CRS resolution, collision rejection, datum transforms and bounded MOS inference must remain shared. Published geom/proj modules replace historical filesystem replacements. MVT topology/scale behavior, asynchronous metatile updates, cancellation handling and seeded-map/layer cache interoperability must remain compatible.

New transport work integrates into server.NewRouter. Preserve existing credential/query no-store protections and GET/HEAD tile-cache parity. Feature HTTP cache policy will require its own decision and evidence.

## Verification boundary

Baseline/source retrieval has been checked. No Feature runtime, database integration, OGC conformance or new suite pass is claimed. Later gates require the exact candidate revision, environment, fixtures, commands and explicit skips. The current fork's fallback is full vendored tests with CGO disabled/enabled and golangci-lint; see [UPSTREAM.md](../../UPSTREAM.md).

## See Also

- [OGC edition baseline](ogc-api-features.md)
- [Jivan route inventory](../migration/jivan-feature-matrix.md)
- [Provider contract](../provider-contract.md)
- [CRS contract](../crs.md)

## Target architecture (accepted)

Independent review returned provider-domain (M1) PASS and HTTP-domain (M4) PASS on 2026-10-01; [ADR-0001](decisions/ADR-0001-feature-service-boundaries.md) records acceptance state.

1. Introduce optional `provider.FeatureQuerier` alongside `Tiler`. Existing tile interfaces and registrations remain compatible. Feature queries never fabricate a Tile or invoke TileFeatures as a fallback.
2. Own FeatureQuery, typed provider errors, query results and filter AST in `provider/`. Keep route names, query strings, status codes, HTTP headers and SQL strings out of this public contract. Task 7 finalizes its concrete signatures against the current Params interface.
3. Own FeatureService in `ogc/features/`; Task 11 creates `service.go` and `geojson.go` there. It resolves explicitly published collections to configured provider/layer identities, performs CRS transforms and creates output models. No handler runs SQL or scans provider rows.
4. `server/` parses and validates transport input, negotiates representation and maps typed errors. Reuse NewRouter, URI prefix/proxy handling, middleware and observability. `cmd/` remains the composition root; configuration is validated before serving.
5. Reuse stable row/ID/property/geometry decode seams for MVT and feature paths. SQL generation and dialect-specific predicates stay in individual providers. A seam is shared only when semantics are identical; do not force one SQL dialect abstraction.
6. Default Part 1 output uses CRS84 longitude/latitude semantics, independently of source SRID. Transform a copy and retain provider Feature/geometry values. Preserve GeometryCollection in GeoJSON; MVT flattening/clipping/simplification never applies to FeatureService output.
7. Model Part 2 output/query CRS explicitly at the service boundary. Supported public CRS identifiers are URI references; process-local synthetic SRIDs never become fabricated EPSG codes. Task 25 defines publication details using this invariant.
8. Result count distinguishes unknown from exact zero. Emit numberMatched only when exact and known; numberReturned is the encoded result count. Cancellation and callback/encoding errors stop work and retain their error chain.
9. Provider paging is transport-independent, initially deterministic ID ordering and a bounded offset cursor. The service constructs public links and enforces publication limits. Keyset paging is a future explicit extension, not an implicit requirement. Paging across changing datasets does not promise snapshot consistency without backend support.
10. Public collections explicitly map IDs to provider.layer; map-layer styling is not feature publication. Exclude mvt_postgis/mvt_hana from the initial feature capability set; no silent raw-feature fallback.
11. Parser/validation produces a neutral typed AST resolved through published Queryables. Provider compilers bind values and map allowlisted identifiers/operators. Filters are not raw SQL and are never implemented by ordinary full-collection materialization.
12. Lambda adapts the same constructed HTTP handler/service. A deployment adapter cannot fork FeatureService, provider semantics or protocol handlers.

## Dependency direction

`cmd -> config + registration + service construction`; `server -> ogc/features -> provider contracts + geometry/CRS foundations`; `ogc/cql2 -> provider filter model`; individual providers consume shared codec/CRS helpers. The provider contract imports neither server nor ogc packages. New packages must not introduce cycles.

## Architecture review acceptance

Provider review checks optional capability detection, current Params compatibility, cancellation/immutability, shared decoder ownership, paging/count semantics and no fake tile. HTTP review checks publication/service boundary, URI prefix integration, representations, CRS/count/error mapping and shared Lambda handler. Changes to this accepted boundary require an ADR before dependent code.

## Shared decoder ownership

The optional contract is declared in [provider/query.go](../../provider/query.go); it is not an implemented HTTP Feature API. [GPKG row decoding](../../provider/gpkg/row_decode.go) now constructs features for both tile and future raw-query callers. Scanning, SQL, exact tile filtering and callback delivery stay outside that helper. Its strict default returns malformed-geometry errors; the tile caller explicitly selects its existing auto-MOS tolerance and sticky null/empty exclusion.

PostGIS reuses decipherFields and decodeGeometryValue; HANA reuses readRowValues and decodeGeometryValue; shared geometrycodec owns format decoding and geometric predicates. MySQL's deferred CRS/retry flow remains unchanged: its local construction seam is finalized with the FeatureQuerier implementation. No new query path may duplicate those decoders.

Raw null/normalized-empty geometry follows [ADR-0002](decisions/ADR-0002-absent-feature-geometry.md), including bbox matching. The [contract harness](../../provider/internal/querytest/querytest.go) supplies explicit fixture expectations; its reference adapter demonstrates harness behavior, without proving real provider or database parity.
