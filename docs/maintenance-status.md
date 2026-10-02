[← Geometry Formats](geometry-formats.md) · [Back to README](../README.md) · [Windows Release →](windows-release.md)

# Current maintenance status

This page records the disposition of documented Tegola work. The baseline
was checked against `master` after `d417e73b` on 2026-09-28; later follow-ups
carry their own verification dates in the table below. The original numbered register and
closure evidence remain in [UPSTREAM.md](../UPSTREAM.md). The
[2026-09-25 audit](audit/tegola_review_part12.md) is historical evidence, not
an active list of unresolved defects.

## Markdown audit scope

The audit enumerated all 199 Git-tracked `.md`/`.markdown` files, ignoring
extension case: 40 first-party documents (including the historical audit),
149 vendored dependency documents and 10 frozen dependency snapshots.
This status page is the new, additional document produced by the audit.
Scratch work, generated release logs and installed npm packages are not
maintained project documentation.

The first-party review covered root documents, issue templates, all cache,
provider and MVT-provider guides, server/Lambda/viewer instructions, internal
package guides, geometry algorithm notes, contracts, release instructions,
the dependency guide and the complete saved audit. Searches for debt markers
were followed by checks of source, tests and published dependency manifests.
External release notes and historical TODOs were inspected for provenance;
they were not rewritten to manufacture a clean result. Vendored files must
continue to match their published module versions.

## Completed work

| Area | Current disposition and evidence |
| --- | --- |
| Numbered fork debt | 2.1, 2.2, 2.4, 2.5, 2.6, part12 0.6/0.6b, 3.6, A15 and part13 P6.5 are closed; see the UPSTREAM closure log. |
| Published dependencies (A15) | `alexeydott/geom v0.1.1` and `alexeydott/proj v0.3.1`; Tegola imports the fork modules without replacements. Published Tegola `d417e73b` passed an external-consumer check in a fresh module cache and an offline vendor build/test. |
| Geographic datum conversion | `longlat` uses the existing three-/seven-parameter datum transformations; independent PROJ references cover both directions and synthetic `crs_defn` registration. |
| Geographic scale | All four SQL providers share source-ellipsoid local parallel scale at the transformed tile center; SQL pixel dimensions use source units. |
| Complex polygon simplification | Ring and component relationships are validated before accepting candidates; unsafe or inconclusive candidates preserve the original geometry. |
| MOS startup sampling | Explicit MOS checks column metadata without format sampling; an explicit geometry type also skips class decoding. Automatic inference stops at three valid MOS geometries within 16 rows; a completed short result needs one valid geometry. Invalid auto-detected MOS rows are skipped while valid features remain renderable. Provider fixtures and a real SQLite startup regression cover the behavior. |
| Legacy MySQL startup and joined MOS bounds (2026-10-02) | Direct bounded MySQL SELECTs avoid the derived-table materialization reproduced on MySQL 5.5.29. Optional `bbox_table` qualifies joined MOS bounds in all four SQL providers. Codec/provider tests passed with CGO on/off. Live MySQL validation covered 16-layer registration and uncached layer/full-map tile responses; it does not establish live HANA/PostgreSQL coverage. |
| SQL probes and token parsing | Providers use explicit dialects; PostgreSQL hash operators and arrays are executable SQL. Probe, missing-layer and geometry-column regressions have tests. |
| Seed/serve interoperability | Layer endpoints reuse seeded map MVT without provider rendering; file roots are resolved and logged. Cold-memory file-cache, TTL and precedence regressions pass; real RU-CHE seed/map/layer HTTP HIT verified. |
| Cache and tile operations | Unique file-cache temp names, safe purge races, authenticated/rate-limited maintenance, queued regeneration status, bounds filtering and MVT-only cache writes are implemented. |
| Other historical findings | Azure missing-object purge/URL validation, GCS self-test hit/content checks, empty GPKG geometry, WebMercator latitude validation and qualified MySQL identifiers are covered by implementation and regression tests. |
| Synthetic SRID collision follow-up | Closed: conflicting definitions fail registration without rebinding an ID already stored by a layer. Regression tests cover both registration orders, warmed forward/inverse projections, explicit registration and provider-layer reuse; focused race tests pass. |

The Markdown audit corrected obsolete instructions about local replacements,
scale formulas, SQL scanning, MVT provider configuration, tile-operation
responses, viewer builds, Lambda initialization and geometry benchmarks.
Old report statuses remain visible beneath an explicit historical banner.

## Agreed exclusions

Only these two numbered items remain intentionally outside the completed scope:

* **A16:** availability of remote GitHub Actions runs. Local verification is
  recorded separately; workflow definitions are not evidence of a green run.
* **2.3:** migration to AWS SDK v2 and the current Azure SDK.

## Supported contracts and verification boundaries

These are explicit operating boundaries, not claims that unimplemented
features have been completed:

* The Go projection fork still rejects grid-based datum transformations,
  non-Greenwich prime meridians and alternate geographic units/axes. Its 2D
  datum API assumes zero input height on each transformation. See
  [CRS definitions](crs.md#geographic-proj-definitions).
* SQL token scanning uses the documented PostgreSQL/MySQL string modes.
  Native MVT providers use database-side geometry/SRS; raw MOS/WKB/WKT and
  synthetic-CRS contracts belong to standard providers. See
  [provider contracts](provider-contract.md).
* Tile output is MVT. An unsupported suffix warns and returns MVT for
  compatibility; `.json` is not a JSON tile endpoint. See
  [server behavior](../server/README.md#tile-format).
* Live HANA and cloud-service integration has not been qualified here.
  Historical PostGIS/MySQL/Redis validation is recorded against its tested
  revision in UPSTREAM. Native SpatiaLite 5.1.0 recorder and geometry/SRID
  write/readback were exercised successfully using the supplied Windows DLL.
* Offering already-fixed patches to upstream repositories is separate
  publication work. Publishing the alexeydott forks closed dependency
  portability; it does not mean upstream pull requests were submitted.

Do not convert the historical audit into a new task list by searching for
TODO words alone, or mark an unsupported capability as implemented merely
to remove a debt marker. New confirmed findings must have an explicit status
and reproducible evidence here or in UPSTREAM.

## See Also

- [Upstream provenance](../UPSTREAM.md) — detailed fork history and evidence
- [Historical audit](audit/tegola_review_part12.md) — original audit findings
- [Provider contract](provider-contract.md) — currently supported provider behavior
