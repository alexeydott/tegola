# ADR-0014: GeoPackage as the proven reference writer in this environment

Status: accepted on 2026-10-04.
Date: 2026-10-04
Decision owner: S1
Affected tasks/gates: W09–W14, W19–W20, W40; WG2, WG4, WG8

## Context

The plan sequences PostGIS first (W09–W14, phase 2) as the reference
writer, MySQL/MariaDB in phases 3–4 (W19–W20), GeoPackage in phase 8
(W40). This execution environment provides no PostgreSQL, MySQL/MariaDB
or HANA servers — only file-backed GeoPackage (SQLite via CGO), which is
fully testable here.

## Decision

1. The neutral mutation contracts (`provider/mutation.go`, `feature/`)
   are implemented backend-independently exactly as planned.
2. GeoPackage is implemented and proven first as the reference native
   writer: admission, Insert/Replace/Update/Delete, one native
   transaction, revision CAS, atomic audit/outbox, contract tests —
   all against a real database file. This covers the plan's W09/W11–W14
   acceptance content for one backend.
3. PostGIS/MySQL/MariaDB/HANA writers follow the same contracts but are
   NOT admitted until their native fixtures exist: per the plan's own
   rule, an unproven profile stays read-only and is never declared from
   a mock. Their code is structured for later admission, not stubbed as
   done.
4. This deviation is recorded here per the plan's change governance; it
   changes sequencing and the first proven backend, not the architecture,
   the contracts, or the Definition of Done.

## Consequences

Part 4 and WFS-T are provable end-to-end in this environment on GeoPackage.
Production Postgres/MySQL pilots require their W10/W11/W19/W20 evidence
before admission.

## Verification and acceptance record

`provider/gpkg/mutation_contract_test.go` plus live CRUD/CAS/rollback
tests on a real GeoPackage file. No PostGIS/MySQL write capability is
advertised.
