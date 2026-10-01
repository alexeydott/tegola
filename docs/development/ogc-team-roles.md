[Documentation index](../README.md)

# Feature-service team responsibilities

These roles define responsibility and approval authority. Assign real contributor identities before execution; role names alone do not create reviewers or prove independent sign-off. One independent reviewer may cover multiple domains if qualified; record the identity and each verdict separately. An author cannot approve their own work.

## Role Charters

### S1 — Senior Architect / Coordinator / Final Reviewer

**Accountable for:** architecture, public contracts, dependency graph, gates, requirement arbitration, release/Jivan retirement.

**MUST**
- review provider public interfaces, config schema, FeatureService boundary, CRS semantics, filter AST, conformance claims;
- block duplicate query/decoder pipelines without ADR;
- require M5 evidence for gates;
- preserve MVT behavior as explicit invariant;
- document waivers with expiry/follow-up.

**MUST NOT**
- silently reinterpret OGC requirements;
- self-certify a gate without M5 evidence;
- accept provider-specific semantics for a declared common contract.

**Immediate veto**
- OGC path uses fake `TileFeatures`;
- user filter reaches SQL without typed validation/parameters;
- public contract differs by provider without capability declaration;
- conformance advertised without evidence;
- CRS behavior contradicts Part 2.

### M1 — Feature Query Core
Owns FeatureQuery, streaming/cancellation, shared scanners, neutral query/filter models.
Mandatory S1 review for public interfaces; M3 reviews query/compiler architecture.

### M2 — GPKG/MySQL/MariaDB/MOS
Owns GPKG, MySQL/MariaDB, MapplGIS/MOS/raw geometry feature-query implementations.
M1 reviews provider-contract compliance.

### M3 — PostGIS/HANA/SQL Compiler
Owns PostGIS/HANA and provider SQL/filter compilation. SQL compiler changes require M5 security evidence.

### M4 — OGC API/HTTP
Owns FeatureService, catalog/config, protocol resources, GeoJSON, OpenAPI, HTML, migration docs.
MUST NOT implement provider SQL in HTTP/service layer.

### M5 — Integration/QA/Conformance
Owns contract suites, parity fixtures, OGC test harness, fuzz/security/load evidence and gate reports.
M5 is an engineer and may author test infrastructure/product fixes, not merely manual QA.

## RACI

| Area | S1 | M1 | M2 | M3 | M4 | M5 |
|---|---|---|---|---|---|---|
| FeatureQuery public contract | A | R | C | C | C | V |
| GPKG/MySQL/MariaDB | C | C | R | C | I | V |
| PostGIS/HANA | C | C | C | R | I | V |
| CRS semantic contract | A | R | C | C | C | V |
| OGC HTTP resources | A | C | I | I | R | V |
| Filter AST | A | R | C | C | C | V |
| SQL compilers | C | A shared | R | R | I | V/security |
| OGC conformance | A claim | C | C | C | C | R |
| Performance | A | R | C | C | C | R evidence |
| Release/retirement | A/R | C | C | C | R docs | V |

A=accountable, R=responsible, C=consulted, V=verification owner, I=informed.


## High-risk path reviewer map

| Paths/change | Owner | Independent reviewer | Escalation/approval |
|---|---|---|---|
| provider/query.go and shared public query/filter contracts | M1 | M3 or M4 | S1 before public contract merge |
| provider/gpkg, provider/mysql, MOS/geometry decode changes | M2 | M1 | S1 for shared geometry/CRS semantics |
| provider/postgis, provider/hana | M3 | M1 | S1 for common contract divergence |
| SQL/filter compilers | M2/M3 | peer provider owner plus M5 security evidence | S1 for security exception |
| config/features.go, catalog, server feature routes, ogc/features | M4 | M1 for service contract; M5 for protocol | S1 for public config/protocol changes |
| CRS publication/transformation | M1/M4 | provider peer plus M5 fixture verification | S1 |
| conformance, OpenAPI, release/retirement docs | M4 | M5 | S1 for claim/release |
| QA suites authored by M5 | M5 | affected domain peer | S1 for gate acceptance |

## Gate authority

S1 is accountable for G0–G11. M5 owns verification/evidence for every gate; affected domain owners supply fixtures and explanations. G0: M1/M4 architecture and M5 traceability/governance review. G1: M1; G2: M2/M4; G3–G4: M4; G5: M2/M3; G6–G7: M1/M2/M3; G8: M1/M4; G9: M4; G10: all affected domains; G11: M4/deployment owner. Each gate requires actual independent domain verdicts, M5 evidence and recorded S1 disposition. No missing identity can be replaced by self-certification.

## Substitution and escalation

Substitutes: M1↔M3, M2↔M1, M4↔M5, subject to domain competence and independence. S1 records substitutions and the actual actor identities before accepting evidence. If a substitute authored the change, select another independent reviewer or leave the gate pending. Missing reviewers block the affected gate. Conflicting requirements go to S1 and the product owner; stop dependent work and record the resulting ADR/plan amendment. Public-contract changes always require S1 approval.

## Task ownership

The execution index assigns an owner and reviewer to all 37 tasks. Verify those assignments during preflight and again when gate evidence is recorded. Role assignments describe responsibilities; they do not assert six people or agents are currently available.

## See Also

- [Feature-service architecture](../architecture/feature-service.md)
- [Migration and traceability](../migration/jivan-feature-matrix.md)
- [Fork verification policy](../../UPSTREAM.md)
