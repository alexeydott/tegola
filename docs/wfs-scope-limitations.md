# WFS/WFS-T Scope and Known Limitations

This document honestly states what is implemented, what is planned,
and what is explicitly out of scope (A39, A41, A42, A43).

## Implemented and tested

- WFS 1.1.0 and 2.0.2: GetCapabilities, DescribeFeatureType, GetFeature
  (KVP and POST XML), GetPropertyValue, LockFeature, Transaction
- OGC API Features Part 4 (CRUD): POST/PUT/PATCH/DELETE on collections/items
- If-Match/If-None-Match preconditions via ETags (numeric revision or hash)
- Providers: PostGIS, MySQL/MariaDB, GeoPackage (SQLite)
- Geometry formats: PostGIS native, WKB, WKT, MOS blob
- CRS: 4326 ↔ 3857 transforms; axis order from srsName (A07)

## Planned (not yet implemented)

- JSON Patch (RFC 6902): returns 415; only Merge Patch (RFC 7396) supported (A21)
- Native COUNT for hits: bounded scan at 100k, 400 beyond (A29)
- Provider-level SortBy/OFFSET: in-memory sort, startIndex supported (A28)
- Per-map cache epochs: global epoch currently (A36)
- Full FES filter: only ResourceId/FeatureId filters supported (A26)

## Explicitly out of scope

### A41: Embedded UI editor
No embedded web editor is provided. WFS-T endpoints are for external
clients (QGIS, OpenLayers, custom apps). An `external-client-only`
deployment profile is the intended use.

### A42: HANA provider
SAP HANA write support is not implemented. The provider matrix is:
- PostGIS: implemented, tested with unit tests
- MySQL/MariaDB: implemented, tested with unit tests
- GeoPackage: implemented, tested with unit tests (369s suite)
- HANA: not implemented, not advertised

### A43: Operational acceptance
Before production write traffic:
- Run migrations on a backup; verify rollback
- Test backup + real restore
- Rehearse write-disable (config flag)
- Set pool sizes and statement timeouts per provider
- Define audit retention and PII policy
- Reconcile unknown operations via audit log

## Conformance (A39)

Conformance URIs are declared only for implemented features.
Draft vs published spec revisions are not mixed.
New conformance URIs require proof before declaration.
