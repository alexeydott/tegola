[Feature-service architecture](../feature-service.md)

# ADR-0001: Optional feature queries and one OGC service

Status: accepted for Task 2 after independent review on 2026-10-01.
Date: 2026-10-01
Decision owner: S1; independent acceptance evidence is retained locally.
Affected tasks/gates: 2–3, 7–37; G0–G11
Sources: pinned baseline above, Jivan source inventory and normative editions in the linked architecture/migration documents.
Source baseline: c65beeb8519f425ff8365c76e54e93baf8e17b07

## Context

Current Tiler uses a Tile and Params. Pinned Jivan materializes features through EmptyTile and does in-memory property/ID filtering. The selected program requires a reusable non-tile query capability, existing MVT compatibility and normative OGC protocol behavior.

## Decision

Adopt the target architecture in [feature-service.md](../feature-service.md#target-architecture-accepted). Place FeatureService in `ogc/features/`; neutral query/filter contracts belong in `provider/`; handlers stay in `server/`. Default response coordinates are CRS84. Optional count distinguishes nil/unknown from exact zero. Deterministic provider paging is independent of HTTP links. The initial capability set excludes MVT-only providers. Lambda wraps the same handler. Concrete FeatureQuery API is finalized in Task 7 within these accepted boundaries.

## Consequences

Providers can support both Tiler and FeatureQuerier with shared decode seams. Existing tile APIs stay stable. Collection publication becomes explicit rather than derived from map styles. Additional packages follow existing technical ownership; no duplicate framework hierarchy or HTTP server is created. Backends without a capability expose a declared limitation rather than a fake-tile fallback.

## Verification and acceptance record

The source baseline and normative editions were verified by Task 1. Acceptance requires separate read-only provider and HTTP-domain verdicts from an actor who did not author this change. No runtime behavior or G0 approval follows from this ADR alone. Post-G0 changes require a replacement/amending ADR with compatibility, migration, tests and gate impact.

Provider-domain (M1): PASS. HTTP-domain (M4): PASS. Independent reviewer identity and execution evidence are retained locally. G0 acceptance is recorded separately.

## Core datetime scope amendment

Product owner approved basic datetime in Core on 2026-10-01. Task 7 defines the neutral temporal constraint; Tasks 9-12 cover provider/service behavior; Tasks 13, 15-16 implement mapping, parsing and protocol verification before G4. Tasks 17-19 preserve semantics for other standard providers before G5. Phase 7 owns Queryables/CQL2 and composition with existing datetime. Authority: OGC Part 1 section 7.15.4 requirements 25/26. Independent review of this amendment is required with Task 3.

## Constraints / normative requirements

Preserve existing MVT APIs and cache protections, provider-neutral streaming/cancellation and immutable response transforms. OGC Part 1 Core includes datetime before G4; supported extensions are declared only with matching evidence. Do not add HTTP strings or SQL fragments to the provider contract.

## Considered options

Rejected fake TileFeatures adaptation because it couples feature access to slippy tiles and full materialization. Rejected a separate HTTP listener/query hierarchy because it duplicates existing routing and decode ownership. Accepted an optional FeatureQuerier beside Tiler, one FeatureService and the current router. Core datetime before G4 was selected by the product owner over delaying G4 until the filtering phase.

## Compatibility and migration

Existing Tiler callers and MVT-only providers retain their interfaces. Explicitly published raw-feature collections use the new optional capability. Jivan time migrates to datetime; arbitrary property filters use Queryables/CQL2. Legacy validator, temp and Lambda dispositions are tracked in the migration matrix.

## Verification required

Independent architecture review and Task 3 traceability review have PASS records. Future provider contracts, GPKG slice, protocol, parity, CRS, filtering, MVT and official conformance checks are mandatory at their gates; no runtime PASS follows from this ADR.

## Expiry/follow-up (for waivers)

No waiver is granted. Finalize concrete query/temporal types in Task 7 and temporal mapping in Task 13 within these boundaries. Any change after G0 follows the documented ADR process.
