[Documentation index](../README.md)

# Jivan feature inventory: pinned baseline

Status: Task 1 route/source inventory, verified on 2026-10-01. Task 3 independent review PASS on 2026-10-01 (all 27 rows); this document does not certify parity.

Jivan source: `c9fba2bb5188ba43c27539740195085e92f0ad7d`. Tegola execution source: `c65beeb8519f425ff8365c76e54e93baf8e17b07`.

## Registered external routes

All 14 registrations in [server/routes.go](https://github.com/go-spatial/jivan/blob/c9fba2bb5188ba43c27539740195085e92f0ad7d/server/routes.go) are enumerated below. Active means registered in source; live deployed behavior has not been tested.

| Method | Route | Handler | Classification |
|---|---|---|---|
| GET | `/` | `root` | active external |
| HEAD | `/` | `root` | active external |
| GET | `/conformance` | `conformance` | active external |
| HEAD | `/conformance` | `conformance` | active external |
| GET | `/api` | `openapi` | active external |
| HEAD | `/api` | `openapi` | active external |
| GET | `/collections` | `collectionsMetaData` | active external |
| HEAD | `/collections` | `collectionsMetaData` | active external |
| GET | `/collections/:name` | `collectionMetaData` | active external |
| HEAD | `/collections/:name` | `collectionMetaData` | active external |
| GET | `/collections/:name/items` | `collectionData` | active external |
| HEAD | `/collections/:name/items` | `collectionData` | active external |
| GET | `/collections/:name/items/:feature_id` | `collectionData` | active external |
| HEAD | `/collections/:name/items/:feature_id` | `collectionData` | active external |

## Reachable behavior and internal boundaries

| Behavior | Pinned source/symbol | Classification / migration constraint |
|---|---|---|
| JSON/HTML and f parameter | server/handlers.go contentType, root, metadata and collectionData | active external; source ignores Accept parsing, so standards-correct negotiation must be rebuilt |
| limit/page, bbox, time, arbitrary property filters | server/handlers.go collectionData | active external on items; map time to datetime and page to link paging in the traceability task |
| ID lookup and collection filtering | data_provider/provider.go GetFeatures, CollectionFeatures | active external through item/items; currently scans/filter-materializes TileFeatures using EmptyTile, which must not be ported |
| Feature/FeatureCollection output and legacy FNV data validator | wfs3/features.go | active external; IDs/names do not prove data freshness; standards and current cache policy govern replacement |
| filteredFeatures handler | server/handlers.go filteredFeatures | internal/unwired: no registration in routes.go |
| temp collections | data_provider/provider.go MakeCollection, FilterFeatures | internal/unwired creation; no public creation route; provider-level retrieval branch exists but does not establish a reachable creation API |
| AWS Lambda startup | server/server_awslambda.go StartServer | deployment-only; uses the same setUpRoutes router; parity requires a future adapter or signed scope disposition |
| WFS3-era OpenAPI and conformance templates | wfs3/openapi3.go, wfs3/conformance.go | active external artifacts with obsolete standards assumptions; rebuild against selected OGC editions |

Sources: [handlers](https://github.com/go-spatial/jivan/blob/c9fba2bb5188ba43c27539740195085e92f0ad7d/server/handlers.go), [provider adapter](https://github.com/go-spatial/jivan/blob/c9fba2bb5188ba43c27539740195085e92f0ad7d/data_provider/provider.go), [feature output](https://github.com/go-spatial/jivan/blob/c9fba2bb5188ba43c27539740195085e92f0ad7d/wfs3/features.go), [OpenAPI](https://github.com/go-spatial/jivan/blob/c9fba2bb5188ba43c27539740195085e92f0ad7d/wfs3/openapi3.go), [conformance](https://github.com/go-spatial/jivan/blob/c9fba2bb5188ba43c27539740195085e92f0ad7d/wfs3/conformance.go), [Lambda](https://github.com/go-spatial/jivan/blob/c9fba2bb5188ba43c27539740195085e92f0ad7d/server/server_awslambda.go).

## Verification performed

All seven canonical pinned files were retrieved successfully and source headers retained. Route registrations and handler/provider call paths were inspected. OGC edition headers were verified independently. No legacy bug or inefficiency is an intended compatibility requirement merely because it appears in reachable code.

## See Also

- [Feature-service baseline](../architecture/feature-service.md)
- [OGC editions](../architecture/ogc-api-features.md)

## Requirement-to-execution traceability

Status: specification coverage only; all listed verification artifacts are required future evidence, not passing results. Task numbers refer to the prepared convergence implementation sequence. Active GET and HEAD registrations have separate rows. Normative OGC behavior takes precedence over legacy Jivan bugs.

| ID | Endpoint/capability | Pinned source or normative requirement | Disposition | Tasks | Required verification artifact/scenario | Gate |
|---|---|---|---|---|---|---|
| J-01 | GET / | routes.go root; Part 1 /req/core/root-success | migrate required navigation links | 14,16 | server/handle_features_test.go: landing links, prefix/proxy | G3 |
| J-02 | HEAD / | routes.go root | migrate with GET metadata and empty body | 14,16 | server/handle_features_test.go: HEAD landing | G3/G9 |
| J-03 | GET /conformance | routes.go conformance; /req/core/conformance-success | replace obsolete classes with implemented classes | 14,28,31 | server/handle_features_test.go: declarations vs capabilities; ETS | G3/G10 |
| J-04 | HEAD /conformance | routes.go conformance | migrate HEAD metadata | 14,16 | server/handle_features_test.go: HEAD conformance | G3/G9 |
| J-05 | GET /api | routes.go openapi; /req/core/api-definition-success | replace legacy API definition | 14,28 | generated OpenAPI validator and runtime route comparison | G9 |
| J-06 | HEAD /api | routes.go openapi | migrate HEAD definition metadata | 16,28 | server/handle_features_test.go: HEAD API | G9 |
| J-07 | GET /collections | routes.go collectionsMetaData; /req/core/fc-md-success | migrate explicit publication catalog | 13,14,16 | server/handle_features_test.go: catalog links, unpublished exclusion | G3 |
| J-08 | HEAD /collections | routes.go collectionsMetaData | migrate HEAD catalog metadata | 14,16 | server/handle_features_test.go: HEAD collections | G3/G9 |
| J-09 | GET /collections/:name | routes.go collectionMetaData; /req/core/sfc-md-success | migrate public collection ID and metadata | 13,14,16 | server/handle_features_test.go: one/unknown collection | G3 |
| J-10 | HEAD /collections/:name | routes.go collectionMetaData | migrate HEAD single collection metadata | 14,16 | server/handle_features_test.go: HEAD collection | G3/G9 |
| J-11 | GET /collections/:name/items | routes.go collectionData; /req/core/fc-response | migrate through FeatureService | 7-16 | provider contract suite and server/handle_features_test.go: items | G4 |
| J-12 | HEAD /collections/:name/items | routes.go collectionData | migrate status/headers without response body | 15,16 | server/handle_features_test.go: HEAD items incl errors | G4/G9 |
| J-13 | GET /collections/:name/items/:feature_id | routes.go collectionData; /req/core/f-success | replace full-scan ID lookup with pushdown | 7,10,15,16,17-19 | provider contract suite: ID; server test: found/not-found; query plan evidence | G4/G5 |
| J-14 | HEAD /collections/:name/items/:feature_id | routes.go collectionData | migrate item HEAD metadata and errors | 15,16 | server/handle_features_test.go: HEAD item/not-found | G4/G9 |
| J-15 | f and JSON/HTML representations | handlers.go contentType | replace legacy Accept handling with validated negotiation | 16,28,29 | server tests: Accept/f/unsupported media; HTML links | G9 |
| J-16 | limit/page | handlers.go collectionData; /req/core/fc-limit-definition | replace page with bounded provider paging and links | 7,9-11,13,15,16 | provider contract suite: stable paging; server test: bounds and preserved filters | G4 |
| J-17 | bbox | handlers.go collectionData; /req/core/fc-bbox-definition | migrate normative intersection, not tile semantics | 7,9-12,15,16 | provider bbox contract and HTTP invalid/degenerate/combined tests | G4 |
| J-18 | time | handlers.go collectionData; Part 1 /req/core/fc-time-definition and fc-time-response | replace with Core datetime before G4 (approved 2026-10-01) | 7,9-13,15,16,17-19 | provider temporal contract; server instant/interval/open bounds/no temporal geometry/bbox/time tests | G4/G5 |
| J-19 | arbitrary property query filters | handlers.go collectionData; provider.go CollectionFeatures; Part 3 and CQL2 | replace with advertised Queryables/CQL2 subset; unsupported syntax explicit error | 21-24 | queryables/type validation, compiler parity and injection/fuzz tests | G6/G7 |
| J-20 | Feature and FeatureCollection output | wfs3/features.go; RFC 7946; /req/geojson/content | replace legacy output with CRS84 immutable GeoJSON | 11,12,15,16 | ogc/features/service_test.go: geometry/count/CRS; protocol schema fixtures | G2/G4 |
| J-21 | FNV validators | wfs3/features.go | retire name/ID-derived freshness; apply reviewed cache policy | 30,33 | HTTP validator variation/error/query/credential tests | G9/G10 |
| J-22 | filteredFeatures | handlers.go filteredFeatures; no routes.go registration | retire internal/unwired handler; no public parity obligation | 34,35 | migration document and route inventory comparison | G11 |
| J-23 | temporary collection creation | provider.go MakeCollection/FilterFeatures; no creation route | experimental-unexposed; product-owner promotion requires ADR | 3,34-37 | route inventory, config surface review, retirement checklist | G11 |
| J-24 | Lambda | server_awslambda.go StartServer | deployment parity through shared router; adapter or signed scope disposition | 2,34-37 | shared-handler adapter smoke, deployment/rollback record or approved disposition | G11 |
| O-01 | Part 2 CRS | Part 2 18-058r1 | implement only declared CRS URIs and bbox-crs/Content-Crs | 25-27 | CRS fixture matrix and Part 2 ATS/ETS evidence | G8 |
| O-02 | Queryables/filter conformance | Part 3 19-079r2; CQL2 21-065r2 | declare only tested supported subset | 21-24,28,31 | queryables schema, compiler fixtures, parser fuzz, declaration comparison | G7/G10 |
| O-03 | MVT compatibility | current provider/provider.go and server/server.go | preserve tile routes, geometry and cache safety | 7-33 | existing provider/server suites and representative MVT smoke per affected gate | affected gates |

Core datetime is unconditional at the parameter boundary. Optional temporal field mapping identifies temporal geometry; an unmapped collection has no temporal geometry and its features satisfy the temporal predicate. Invalid syntax still fails. Provider values are bound parameters and field names resolve through validated metadata. CQL2 temporal expressions remain optional extension work in Phase 7.
