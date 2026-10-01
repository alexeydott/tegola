[Documentation index](../README.md)

# OGC API Features: source editions

Status: selected source editions verified on 2026-10-01; implementation and conformance claims remain pending. Tegola source baseline is `c65beeb8519f425ff8365c76e54e93baf8e17b07`.

## Normative source set

| Source ID | Document / edition | Planned responsibility |
|---|---|---|
| OGC-P1 | [17-069r4, Part 1 Core 1.0.1](https://docs.ogc.org/is/17-069r4/17-069r4.html) | resource graph, limit/bbox/datetime, errors and representations |
| OGC-P2 | [18-058r1, Part 2 CRS 1.0.1](https://docs.ogc.org/is/18-058r1/18-058r1.html) | public CRS references, crs/bbox-crs, Content-Crs |
| OGC-P3 | [19-079r2, Part 3 Filtering 1.0](https://docs.ogc.org/is/19-079r2/19-079r2.html) | Queryables and selected filtering classes |
| CQL2 | [21-065r2, CQL2 1.0.0](https://docs.ogc.org/is/21-065r2/21-065r2.html) | selected typed filter language classes |
| GEOJSON | [RFC 7946](https://www.rfc-editor.org/rfc/rfc7946) | geometry representation and default coordinate semantics |

Document header versions were read from these authoritative sources. Future standard revisions require an explicit scope decision. Selecting an edition does not advertise conformance; each advertised class requires matching implementation and official verification.

## Reference implementations and tooling

Jivan is pinned at `c9fba2bb5188ba43c27539740195085e92f0ad7d`; its [routes](https://github.com/go-spatial/jivan/blob/c9fba2bb5188ba43c27539740195085e92f0ad7d/server/routes.go), handlers, provider adapter and WFS3 output provide migration evidence. Their legacy behavior never overrides normative requirements.

The [OGC source/components repository](https://github.com/opengeospatial/ogcapi-features), [Features ETS](https://cite.ogc.org/teamengine/about/ogcapi-features-1.0/1.0/site/), and [TEAM Engine](https://github.com/opengeospatial/teamengine) are tooling references. Tool versions/components must be pinned by the tasks which adopt them; mutable master URLs are not immutable conformance evidence.

## Existing Tegola transport

[server.NewRouter](../../server/server.go) owns existing MVT/capabilities routing, URI prefix, CORS and observability. [config.Config](../../config/config.go) and [provider.Tiler](../../provider/provider.go) are current integration points. Architecture acceptance will define FeatureService and optional FeatureQuerier boundaries before product code.

## See Also

- [Feature-service baseline](feature-service.md)
- [Jivan route inventory](../migration/jivan-feature-matrix.md)
- [HTTP API](../api.md)
