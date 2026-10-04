# WFS/WFS-T scope and operating limits

The branch provides WFS read/transaction adapters and an experimental OGC API
Features CRUD implementation. Implementation, unit tests, native integration
runs, and standards conformance are different levels of evidence. Neither this
file nor the presence of a handler establishes full OGC conformance or production
acceptance. See the current review evidence for exact runs and provider versions.

## Publication and concurrency

Writes require explicit per-collection configuration. Use production authentication
and require strong If-Match preconditions on existing features. A representation
hash alone is not a substitute for an atomic revision check. External database
writers must participate in revision management; otherwise concurrent-edit
protection cannot be promised.

LockFeature is unavailable until a physical-feature guard can be enforced by
all mutation paths and processes. Do not interpret an in-memory token store as a
distributed database lock.

A write-enabled server bypasses tile caches and emits no-store for tiles. Apply
that policy to every reader replica serving the same editable data, including
replicas whose own writes are disabled. Clear old persistent/CDN/browser caches
before returning to a cached read-only deployment. Distributed cache invalidation
and durable delivery of invalidation events are not supplied by this profile.

## Embedded attribute editor

The viewer includes a limited source-feature attribute editor. Its default API
path is `features`; change the same-origin path when the deployment configures a
different Feature API base path. It loads the mutation schema, then loads a feature
by decimal ID together with its ETag. Saving existing features sends JSON Patch
operations only for changed attributes: geometry is retained. NULL, omitted
properties and empty strings are distinct. Undo, redo and cancel act on the local
attribute draft. Create accepts a manually entered GeoJSON geometry; it is not a
map drawing tool.

A 412 keeps the draft and disables saving until the source is explicitly loaded
again. Copy the draft before reloading; there is no automatic conflict merge.
Network/server failures preserve the draft and stop further writes because the
commit outcome can be unknown. Verify the source before reconnecting. There is
no automatic retry of a potentially committed write.

A confirmed commit with failed source readback returns a minimal successful
response and `Tegola-Commit-Status: committed`. The editor distinguishes this
case from an unknown outcome and never retries the write. Successful saves
refresh the viewer's loaded vector sources.

The editor rejects unsafe JavaScript integers and decimal tokens exceeding its
conservative precision limit. Use an external lossless client for large IDs or
exact decimal values; the editor does not claim a complete uint64 editing profile.
Geometry drawing, geometry undo/redo, and collaborative conflict resolution remain
outside the embedded profile. Existing authentication must be provided by the
same-origin deployment; this UI is not an authentication management console.

## Provider and protocol limits

PostGIS, MySQL/MariaDB and GeoPackage have mutation implementations. Their native
test status must be recorded separately per database version, geometry encoding,
CRS and operation; unit tests do not establish this matrix. HANA writes are not
implemented and must not be admitted. Do not infer MOS, custom CRS or XYZ write
support from the availability of those read formats.

Layers with configured or discovered derived bbox columns are rejected for
writing because those columns are not maintained by this profile. Native
GeoPackage RTree indexing is handled separately; do not assume derived bbox
columns and a native spatial index are interchangeable.

Both Merge Patch and the implemented JSON Patch subset are accepted by the REST
adapter. Unsupported JSON Patch operations, filter expressions, spatial profiles
and lock operations must fail explicitly. The bounded WFS filter, sort and hits
implementation is not a declaration of full FES or WFS conformance.

Part 4 follows a draft-derived implementation contract; no final Part 4 conformance
class is declared merely because writes are enabled. The OpenAPI document describes
installed methods and media types; conformance needs independent protocol evidence.

## Operational acceptance

Before enabling production writes, verify migration privileges and upgrade behavior,
backup and actual restore, revision state after restore, write-disable procedures,
connection pool limits and timeouts, audit retention, external-writer policy and
reconciliation of unknown commit outcomes. A successful build and a local fixture
run do not close these operational requirements.
