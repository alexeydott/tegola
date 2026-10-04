# ADR-0009: WFS 1.1/2.0, WFS-T and OGC API Features Part 4 program baseline

Status: accepted for WFS program start on 2026-10-04.
Date: 2026-10-04
Decision owner: S1 (solo implementer acts as S1+M1..M5; independent review is
done as a separate pass, not self-approval of evidence).
Affected tasks/gates: W01–W50; WG0–WG10
Source baseline: 54f93b3100df2b4ab94ca45f9401c891f79505dc (origin/master)

## Context

The fork publishes OGC API Features Parts 1–3 (read-only) and MVT tiles from
PostGIS, MySQL/MariaDB, GeoPackage, HANA and MOS/MapplGIS sources. There is no
write path: no WFS, no Transaction, no Part 4. The implementation plan
`tegola-wfs-implementation-plan.md` v1.0 (50 work items W01–W50, 10 phases,
gates WG0–WG10) is the normative project breakdown; this ADR records the
program baseline derived from it.

## Decision

1. Implement WFS 1.1.0 / 2.0.2 (read + Transaction), OGC API Features Part 4
   draft.3 (20-002r2, create-replace-update-delete) inside Tegola. No separate
   WFS server, no HTTP proxy to REST.
2. One common feature core (catalog, schema, identity, policy, transaction
   service); independent WFS and OGC API Features transport adapters.
   Adapters convert input documents into neutral commands; SQL is only
   reachable from provider implementations.
3. Write is disabled by default. Old TOML files, MVT endpoints and the
   read-only Features API keep working unchanged when WFS/editing is off.
4. Phases, gates (WG0–WG10) and per-task Definition of Done from the plan
   apply. A gate is not PASS because a later independent branch started.
   SKIP is never converted to PASS; untested profiles stay read-only.
5. Normative pins: WFS 1.1 = OGC 04-094r1, WFS 2.0 = OGC 09-025r2,
   FES = OGC 09-026r2, Part 4 = 20-002r2 draft.3 at
   `1c68987e1a5222e9a488a7b089f7246010f3e83c`. Draft.2 URIs are not carried
   into the new implementation.

## Consequences

New packages: `feature/` (neutral services), `provider/mutation.go`
(contracts), `ogc/wfs/` (adapters, GML, filters), backend writers under
`provider/<backend>/`, REST handlers in `server/`. Public Go symbols are not
removed without a migration ADR.

## Verification and acceptance record

W01 baseline receipt: this branch builds and the existing read/MVT suites
pass at the base commit before any WFS change (verified by running the
affected packages; see commit history). Independent review of the
implementation is a separate pass after the work completes.
