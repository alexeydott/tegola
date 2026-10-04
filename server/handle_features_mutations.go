package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/dimfeld/httptreemux"
	"github.com/alexeydott/tegola/config"
	"github.com/alexeydott/tegola/feature"
	"github.com/alexeydott/tegola/ogc/features"
	"github.com/alexeydott/tegola/ogc/wfs"
	"github.com/alexeydott/tegola/provider"
)

const (
	mediaGeoJSON      = "application/geo+json"
	mediaMergePatch   = "application/merge-patch+json"
	maxMutationBody   = 4 << 20 // 4 MiB per mutation document
)

// writePolicy is the config-backed Policy: mutations are allowed only
// for collections and operations listed in [features.write].
type writePolicy struct {
	cfg config.FeaturesWriteConfig
}

func (p writePolicy) CheckCollection(_ context.Context, principal feature.Principal, action feature.PolicyAction, collection string) feature.PolicyDecision {
	// A04: deny-by-default in production auth mode. Anonymous writes are
	// only allowed in explicit "dev" mode.
	if p.cfg.IsProductionAuth() && principal.Anonymous {
		return feature.PolicyDecision{Reason: "anonymous writes denied in production auth mode (set auth_mode=\"dev\" for trusted networks only)"}
	}
	op := map[feature.PolicyAction]string{
		feature.ActionInsert:  "create",
		feature.ActionReplace: "replace",
		feature.ActionUpdate:  "update",
		feature.ActionDelete:  "delete",
	}[action]
	if op == "" {
		return feature.PolicyDecision{Allow: true}
	}
	if p.cfg.AllowsOperation(collection, op) {
		return feature.PolicyDecision{Allow: true}
	}
	return feature.PolicyDecision{Reason: "operation not allowed for collection"}
}

func (p writePolicy) CheckRow(_ context.Context, _ feature.Principal, _ feature.PolicyAction, _ feature.PhysicalFeatureKey) feature.PolicyDecision {
	return feature.PolicyDecision{Allow: true}
}

func (p writePolicy) CheckPostImage(_ context.Context, _ feature.Principal, _ feature.PhysicalFeatureKey, _ map[string]feature.TypedValue) feature.PolicyDecision {
	return feature.PolicyDecision{Allow: true}
}

// mutationCoordinator builds the neutral coordinator bound to this API's
// service and write config.
func (api *FeatureAPI) mutationCoordinator() *feature.MutationCoordinator {
	return &feature.MutationCoordinator{
		PolicyFor: func(collection string) feature.Policy {
			return writePolicy{cfg: api.cfg.Write}
		},
		SchemaFor: func(collection string) (*feature.SchemaDescriptor, error) {
			return api.service.SchemaDescriptorFor(context.Background(), collection)
		},
		ProviderFor: func(collection string) (provider.MutationProvider, string, error) {
			return api.service.MutationProviderFor(collection)
		},
	}
}

// writeEnabledFor reports whether any mutation is configured for the
// collection. Routes are registered once; per-operation checks happen
// per request.
func (api *FeatureAPI) writeEnabledFor(collection string) bool {
	if !api.cfg.Write.Enabled {
		return false
	}
	for _, c := range api.cfg.Write.Collections {
		if string(c.ID) == collection {
			return len(c.Operations) > 0
		}
	}
	return false
}

// allowedMethods returns the HTTP methods actually available on the
// resource for this collection, per the write config.
func (api *FeatureAPI) allowedMethods(collection string, item bool) []string {
	methods := []string{http.MethodGet, http.MethodHead, http.MethodOptions}
	if !api.writeEnabledFor(collection) {
		return methods
	}
	if !item {
		if api.cfg.Write.AllowsOperation(collection, "create") {
			methods = append(methods, http.MethodPost)
		}
		return methods
	}
	if api.cfg.Write.AllowsOperation(collection, "replace") {
		methods = append(methods, http.MethodPut)
	}
	if api.cfg.Write.AllowsOperation(collection, "update") {
		methods = append(methods, http.MethodPatch)
	}
	if api.cfg.Write.AllowsOperation(collection, "delete") {
		methods = append(methods, http.MethodDelete)
	}
	return methods
}

func (api *FeatureAPI) registerMutations(group *httptreemux.Group) {
	if !api.cfg.Write.Enabled {
		return
	}
	mk := func(h http.HandlerFunc) http.Handler {
		return HeadersHandler(featureNoStoreHandler(h))
	}
	base := api.cfg.BasePath
	group.UsingContext().Handler(http.MethodPost, base+"/collections/:collection/items", mk(api.serveCreateItem))
	group.UsingContext().Handler(http.MethodPut, base+"/collections/:collection/items/:feature", mk(api.serveReplaceItem))
	group.UsingContext().Handler(http.MethodPatch, base+"/collections/:collection/items/:feature", mk(api.servePatchItem))
	group.UsingContext().Handler(http.MethodDelete, base+"/collections/:collection/items/:feature", mk(api.serveDeleteItem))
	group.UsingContext().Handler(http.MethodGet, base+"/collections/:collection/schema", mk(api.serveCollectionSchema))
	// Per-resource OPTIONS advertises the real Allow set.
	group.UsingContext().Handler(http.MethodOptions, base+"/collections/:collection/items", mk(api.serveItemsOptions))
	group.UsingContext().Handler(http.MethodOptions, base+"/collections/:collection/items/:feature", mk(api.serveItemOptions))
}

func (api *FeatureAPI) serveItemsOptions(w http.ResponseWriter, r *http.Request) {
	collection := httptreemux.ContextParams(r.Context())["collection"]
	api.writeOptions(w, api.allowedMethods(collection, false), false)
}


// checkWFSLock enforces WFS LockFeature leases on REST mutations (BUG-4 fix).
// Returns true if the request may proceed.
func (api *FeatureAPI) checkWFSLock(w http.ResponseWriter, r *http.Request, collection string, featureID uint64) bool {
	if wfs.IsLocked(collection, featureID) {
		// Check if client provided a valid lockId
		lockID := r.URL.Query().Get("lockId")
		if lockID == "" {
			lockID = r.Header.Get("Lock-Id")
		}
		if lockID == "" {
			api.writeError(w, r, http.StatusForbidden, "Locked", fmt.Sprintf("feature %d is locked; provide lockId", featureID))
			return false
		}
		if exc := wfs.CheckLock(lockID, collection, featureID); exc != nil {
			api.writeError(w, r, http.StatusForbidden, "Locked", exc.Text)
			return false
		}
	}
	return true
}

func (api *FeatureAPI) serveItemOptions(w http.ResponseWriter, r *http.Request) {
	collection := httptreemux.ContextParams(r.Context())["collection"]
	acceptPatch := api.cfg.Write.AllowsOperation(collection, "update")
	api.writeOptions(w, api.allowedMethods(collection, true), acceptPatch)
}

func (api *FeatureAPI) writeOptions(w http.ResponseWriter, methods []string, acceptPatch bool) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	// CORS preflight needs the methods here, not only in Allow.
	w.Header().Set("Access-Control-Allow-Methods", strings.Join(methods, ", "))
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, If-Match, Content-Crs, Authorization")
	if acceptPatch {
		w.Header().Set("Accept-Patch", mediaMergePatch)
	}
	w.WriteHeader(http.StatusNoContent)
}

// readMutationBody reads and size-limits the request body.
func readMutationBody(r *http.Request) ([]byte, error) {
	if r.ContentLength > maxMutationBody {
		return nil, fmt.Errorf("request body too large")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxMutationBody+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if int64(len(body)) > maxMutationBody {
		return nil, fmt.Errorf("request body too large")
	}
	return body, nil
}

func parseFeatureIDParam(raw string) (uint64, error) {
	if !decimalDigits(raw) {
		return 0, fmt.Errorf("invalid feature ID")
	}
	return strconv.ParseUint(raw, 10, 64)
}

// checkPrecondition enforces If-Match against the current representation
// ETag. If-Match: * checks existence. Absent If-Match is allowed unless
// RequireIfMatch is set, in which case 428 Precondition Required is
// returned.
func (api *FeatureAPI) checkPrecondition(r *http.Request, collection string, featureID uint64) (string, error) {
	match := r.Header.Get("If-Match")
	if match == "" {
		if api.cfg.Write.RequireIfMatch {
			return "", &preconditionRequiredError{}
		}
		return "", nil
	}
	if match == "*" {
		// Existence check.
		_, err := api.service.QueryFeature(r.Context(), collection, featureID)
		if err != nil {
			return "", err
		}
		return "", nil
	}
	current, err := api.currentETag(r, collection, featureID)
	if err != nil {
		return "", err
	}
	if !etagMatches(match, current) {
		return "", &preconditionFailedError{current: current}
	}
	return current, nil
}

type preconditionFailedError struct{ current string }

func (e *preconditionFailedError) Error() string { return "precondition failed" }

// preconditionRequiredError is returned when RequireIfMatch is set and
// the request carries no If-Match header (428).
type preconditionRequiredError struct{}

func (e *preconditionRequiredError) Error() string { return "precondition required" }

func etagMatches(header, current string) bool {
	// Only strong comparison is accepted for mutations; weak tags never
	// grant write permission.
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "W/") {
			continue
		}
		if part == current {
			return true
		}
	}
	return false
}

// currentETag returns the revision-based validator for a feature (A03).
// The revision is bumped atomically with each mutation inside the native
// transaction, so it is a strong validator: equal revisions guarantee
// equal state. Falls back to the representation hash when the provider
// does not track revisions.
func (api *FeatureAPI) currentETag(r *http.Request, collection string, featureID uint64) (string, error) {
	if rev := api.currentRevision(r.Context(), collection, featureID); rev != "" {
		return `"` + rev + `"`, nil
	}
	f, err := api.service.QueryFeature(r.Context(), collection, featureID)
	if err != nil {
		return "", err
	}
	response := api.itemResponse(r, collection, featureID, f)
	raw, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return strongETag(raw), nil
}

// currentRevision returns the provider-tracked revision for a feature,
// or "" when unavailable.
func (api *FeatureAPI) currentRevision(ctx context.Context, collection string, featureID uint64) string {
	// R01: use the provider layer name (not the public collection) for
	// the revision lookup, matching what Apply stores.
	p, layer, err := api.service.MutationProviderFor(collection)
	if err != nil {
		return ""
	}
	rr, ok := p.(provider.RevisionReader)
	if !ok {
		return ""
	}
	rev, err := rr.CurrentRevision(ctx, layer, featureID)
	if err != nil || rev == "" {
		return ""
	}
	return rev
}

// revisionFromETag extracts the revision from a revision-based ETag
// (`"123"`). Returns "" for hash-based or malformed ETags.
func revisionFromETag(etag string) string {
	etag = strings.Trim(etag, `"`)
	if etag == "" {
		return ""
	}
	for _, c := range etag {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return etag
}

// serveCreateItem implements POST /collections/{id}/items (Part 4 Create).
func (api *FeatureAPI) serveCreateItem(w http.ResponseWriter, r *http.Request) {
	collection := httptreemux.ContextParams(r.Context())["collection"]
	if !api.cfg.Write.AllowsOperation(collection, "create") {
		api.writeError(w, r, http.StatusMethodNotAllowed, "MethodNotAllowed", "Create not allowed for this collection")
		return
	}
	if ct := r.Header.Get("Content-Type"); !isGeoJSONContentType(ct) {
		api.writeError(w, r, http.StatusUnsupportedMediaType, "InvalidParameter", "Content-Type must be application/geo+json")
		return
	}
	body, err := readMutationBody(r)
	if err != nil {
		api.writeError(w, r, http.StatusBadRequest, "InvalidParameter", "Invalid request body")
		return
	}
	gf, err := parseGeoJSONFeature(body)
	if err != nil {
		api.writeError(w, r, http.StatusBadRequest, "InvalidParameter", "Invalid GeoJSON Feature")
		return
	}
	schema, err := api.service.SchemaDescriptorFor(r.Context(), collection)
	if err != nil {
		api.writeQueryError(w, r, err)
		return
	}
	m, err := buildInsertMutation(schema, collection, gf)
	if err != nil {
		api.writeError(w, r, http.StatusBadRequest, "InvalidParameter", "Invalid feature")
		return
	}
	outcome, receipt, err := api.mutationCoordinator().Execute(r.Context(), api.principal(r), m)
	if err != nil {
		api.writeMutationError(w, r, err)
		return
	}
	if receipt.Status != provider.CommitCommitted {
		api.writeError(w, r, http.StatusInternalServerError, "CommitUnknown", "Commit outcome unknown")
		return
	}
	// Return the persisted representation (server defaults/triggers may
	// have changed the data): re-read the canonical row.
	created, err := api.service.QueryFeature(r.Context(), collection, outcome.FeatureID)
	if err != nil {
		api.writeQueryError(w, r, err)
		return
	}
	loc := api.itemLocation(r, collection, outcome.FeatureID)
	w.Header().Set("Location", loc)
	api.writeMutationRepresentation(w, r, http.StatusCreated, api.itemResponse(r, collection, outcome.FeatureID, created), outcome.Revision)
}

// serveReplaceItem implements PUT /collections/{id}/items/{fid}.
func (api *FeatureAPI) serveReplaceItem(w http.ResponseWriter, r *http.Request) {
	collection := httptreemux.ContextParams(r.Context())["collection"]
	if !api.cfg.Write.AllowsOperation(collection, "replace") {
		api.writeError(w, r, http.StatusMethodNotAllowed, "MethodNotAllowed", "Replace not allowed for this collection")
		return
	}
	featureID, err := parseFeatureIDParam(httptreemux.ContextParams(r.Context())["feature"])
	if err != nil {
		api.writeError(w, r, http.StatusBadRequest, "InvalidParameter", "Invalid feature ID")
		return
	}
	// A09: enforce WFS locks on PUT (was missing)
	if !api.checkWFSLock(w, r, collection, featureID) {
		return
	}
	if ct := r.Header.Get("Content-Type"); !isGeoJSONContentType(ct) {
		api.writeError(w, r, http.StatusUnsupportedMediaType, "InvalidParameter", "Content-Type must be application/geo+json")
		return
	}
	// A03: capture the matched ETag; its revision becomes the in-tx precondition.
	matchedETag, err := api.checkPrecondition(r, collection, featureID)
	if err != nil {
		api.writePreconditionError(w, r, err)
		return
	}
	// A03: revision for the in-transaction CAS check ("" = no precondition).
	ifRevision := revisionFromETag(matchedETag)
	body, err := readMutationBody(r)
	if err != nil {
		api.writeError(w, r, http.StatusBadRequest, "InvalidParameter", "Invalid request body")
		return
	}
	gf, err := parseGeoJSONFeature(body)
	if err != nil {
		api.writeError(w, r, http.StatusBadRequest, "InvalidParameter", "Invalid GeoJSON Feature")
		return
	}
	schema, err := api.service.SchemaDescriptorFor(r.Context(), collection)
	if err != nil {
		api.writeQueryError(w, r, err)
		return
	}
	m, err := buildReplaceMutation(schema, collection, featureID, gf)
	m.IfRevision = ifRevision
	if err != nil {
		api.writeError(w, r, http.StatusBadRequest, "InvalidParameter", "Invalid feature")
		return
	}
	outcome, receipt, err := api.mutationCoordinator().Execute(r.Context(), api.principal(r), m)
	if err != nil {
		api.writeMutationError(w, r, err)
		return
	}
	if receipt.Status != provider.CommitCommitted {
		api.writeError(w, r, http.StatusInternalServerError, "CommitUnknown", "Commit outcome unknown")
		return
	}
	updated, err := api.service.QueryFeature(r.Context(), collection, outcome.FeatureID)
	if err != nil {
		api.writeQueryError(w, r, err)
		return
	}
	api.writeMutationRepresentation(w, r, http.StatusOK, api.itemResponse(r, collection, updated.ID, updated), outcome.Revision)
}

// servePatchItem implements PATCH with application/merge-patch+json.
func (api *FeatureAPI) servePatchItem(w http.ResponseWriter, r *http.Request) {
	collection := httptreemux.ContextParams(r.Context())["collection"]
	if !api.cfg.Write.AllowsOperation(collection, "update") {
		api.writeError(w, r, http.StatusMethodNotAllowed, "MethodNotAllowed", "Update not allowed for this collection")
		return
	}
	featureID, err := parseFeatureIDParam(httptreemux.ContextParams(r.Context())["feature"])
	if err != nil {
		api.writeError(w, r, http.StatusBadRequest, "InvalidParameter", "Invalid feature ID")
		return
	}
	// BUG-4: enforce WFS locks on REST
	if !api.checkWFSLock(w, r, collection, featureID) {
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != mediaMergePatch && !strings.HasPrefix(ct, mediaMergePatch+";") {
		api.writeError(w, r, http.StatusUnsupportedMediaType, "InvalidParameter", "Content-Type must be application/merge-patch+json")
		return
	}
	// A03: capture the matched ETag; its revision becomes the in-tx precondition.
	matchedETag, err := api.checkPrecondition(r, collection, featureID)
	if err != nil {
		api.writePreconditionError(w, r, err)
		return
	}
	// A03: revision for the in-transaction CAS check ("" = no precondition).
	ifRevision := revisionFromETag(matchedETag)
	body, err := readMutationBody(r)
	if err != nil {
		api.writeError(w, r, http.StatusBadRequest, "InvalidParameter", "Invalid request body")
		return
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var patch map[string]interface{}
	if err := dec.Decode(&patch); err != nil {
		api.writeError(w, r, http.StatusBadRequest, "InvalidParameter", "Invalid merge patch")
		return
	}
	current, err := api.service.QueryFeature(r.Context(), collection, featureID)
	if err != nil {
		api.writeQueryError(w, r, err)
		return
	}
	schema, err := api.service.SchemaDescriptorFor(r.Context(), collection)
	if err != nil {
		api.writeQueryError(w, r, err)
		return
	}
	m, err := buildPatchMutation(schema, collection, featureID, current, patch)
	m.IfRevision = ifRevision
	if err != nil {
		api.writeError(w, r, http.StatusBadRequest, "InvalidParameter", "Invalid patch")
		return
	}
	outcome, receipt, err := api.mutationCoordinator().Execute(r.Context(), api.principal(r), m)
	if err != nil {
		api.writeMutationError(w, r, err)
		return
	}
	if receipt.Status != provider.CommitCommitted {
		api.writeError(w, r, http.StatusInternalServerError, "CommitUnknown", "Commit outcome unknown")
		return
	}
	updated, err := api.service.QueryFeature(r.Context(), collection, outcome.FeatureID)
	if err != nil {
		api.writeQueryError(w, r, err)
		return
	}
	api.writeMutationRepresentation(w, r, http.StatusOK, api.itemResponse(r, collection, updated.ID, updated), outcome.Revision)
}

// serveDeleteItem implements DELETE /collections/{id}/items/{fid}.
func (api *FeatureAPI) serveDeleteItem(w http.ResponseWriter, r *http.Request) {
	collection := httptreemux.ContextParams(r.Context())["collection"]
	if !api.cfg.Write.AllowsOperation(collection, "delete") {
		api.writeError(w, r, http.StatusMethodNotAllowed, "MethodNotAllowed", "Delete not allowed for this collection")
		return
	}
	featureID, err := parseFeatureIDParam(httptreemux.ContextParams(r.Context())["feature"])
	if err != nil {
		api.writeError(w, r, http.StatusBadRequest, "InvalidParameter", "Invalid feature ID")
		return
	}
	// BUG-4: enforce WFS locks on REST
	if !api.checkWFSLock(w, r, collection, featureID) {
		return
	}
	// A03: capture the matched ETag; its revision becomes the in-tx precondition.
	matchedETag, err := api.checkPrecondition(r, collection, featureID)
	if err != nil {
		api.writePreconditionError(w, r, err)
		return
	}
	// A03: revision for the in-transaction CAS check ("" = no precondition).
	ifRevision := revisionFromETag(matchedETag)
	_, receipt, err := api.mutationCoordinator().Execute(r.Context(), api.principal(r), provider.Mutation{
		Op:         provider.MutationDelete,
		Collection: collection,
		FeatureID:  featureID,
		IfRevision: ifRevision,
	})
	if err != nil {
		api.writeMutationError(w, r, err)
		return
	}
	if receipt.Status != provider.CommitCommitted {
		api.writeError(w, r, http.StatusInternalServerError, "CommitUnknown", "Commit outcome unknown")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// itemResponse builds the single-item response shape (Feature + links)
// used by GET item and by mutation responses, so ETags are comparable
// across all of them.
func (api *FeatureAPI) itemResponse(r *http.Request, collection string, featureID uint64, f features.Feature) any {
	queryParameters, _ := url.ParseQuery(r.URL.RawQuery)
	links := api.representationLinks(r, "/collections/"+collection+"/items/"+strconv.FormatUint(featureID, 10), "application/geo+json", queryParameters)
	links = append(links, api.formatLink(r, "/collections/"+collection, "collection", "application/json", nil, featureSelectedFormat(r)))
	return struct {
		features.Feature
		Links []featureLink `json:"links"`
	}{Feature: f, Links: links}
}

// writeMutationRepresentation writes a mutation response. The ETag is
// always the strong validator of the exact response bytes.
// writeMutationRepresentation writes a mutation response. When revision
// is non-empty (R01), the ETag is revision-based for CAS consistency;
// otherwise it falls back to the representation hash.
func (api *FeatureAPI) writeMutationRepresentation(w http.ResponseWriter, r *http.Request, status int, value any, revision string) {
	raw, err := json.Marshal(value)
	if err != nil {
		api.writeError(w, r, http.StatusInternalServerError, "InternalError", "Response encoding failed")
		return
	}
	if int64(len(raw)) > api.cfg.MaxResponseBytes {
		api.writeError(w, r, http.StatusBadRequest, "ResponseTooLarge", "Response exceeds publication limit")
		return
	}
	featureProtocolHeaders(w.Header())
	etag := strongETag(raw)
	if revision != "" {
		etag = `"` + revision + `"`
	}
	w.Header().Set("ETag", etag)
	mergeFeatureHeader(w.Header(), "Access-Control-Expose-Headers", "ETag")
	mergeFeatureHeader(w.Header(), "Access-Control-Expose-Headers", "Location")
	w.Header().Set("Content-Type", mediaGeoJSON)
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(raw); err != nil {
		// Headers already sent; log only.
		fmt.Printf("mutation response write failed: %v\n", err)
	}
}

func (api *FeatureAPI) itemLocation(r *http.Request, collection string, featureID uint64) string {
	return api.link(r, "/collections/"+collection+"/items/"+strconv.FormatUint(featureID, 10), "self", mediaGeoJSON).Href
}

func isGeoJSONContentType(ct string) bool {
	ct = strings.TrimSpace(strings.Split(ct, ";")[0])
	return ct == mediaGeoJSON || ct == "application/vnd.geo+json"
}

// writeMutationError maps the mutation error taxonomy to HTTP.
func (api *FeatureAPI) writeMutationError(w http.ResponseWriter, r *http.Request, err error) {
	if me, ok := provider.AsMutationError(err); ok {
		switch me.Kind {
		case provider.MutationErrMalformedInput, provider.MutationErrSchemaViolation:
			api.writeError(w, r, http.StatusBadRequest, "InvalidParameter", "Invalid input")
			return
		case provider.MutationErrDenied:
			api.writeError(w, r, http.StatusForbidden, "Forbidden", "Operation denied")
			return
		case provider.MutationErrNotFound:
			api.writeError(w, r, http.StatusNotFound, "NotFound", "Feature not found")
			return
		case provider.MutationErrPreconditionFailed:
			api.writeError(w, r, http.StatusPreconditionFailed, "PreconditionFailed", "Precondition failed")
			return
		case provider.MutationErrLockConflict:
			api.writeError(w, r, http.StatusConflict, "LockConflict", "Lock conflict")
			return
		case provider.MutationErrUnsupportedCapability:
			api.writeError(w, r, http.StatusBadRequest, "InvalidParameter", "Unsupported capability")
			return
		case provider.MutationErrQuotaExceeded:
			api.writeError(w, r, http.StatusRequestEntityTooLarge, "QuotaExceeded", "Quota exceeded")
			return
		case provider.MutationErrDomainMismatch:
			api.writeError(w, r, http.StatusBadRequest, "InvalidParameter", "Transaction domain mismatch")
			return
		case provider.MutationErrCommitUnknown:
			api.writeError(w, r, http.StatusInternalServerError, "CommitUnknown", "Commit outcome unknown")
			return
		}
	}
	var cnf features.CollectionNotFoundError
	if errors.As(err, &cnf) {
		api.writeError(w, r, http.StatusNotFound, "NotFound", "Collection not found")
		return
	}
	api.writeQueryError(w, r, err)
}

func (api *FeatureAPI) writePreconditionError(w http.ResponseWriter, r *http.Request, err error) {
	if _, ok := err.(*preconditionFailedError); ok {
		api.writeError(w, r, http.StatusPreconditionFailed, "PreconditionFailed", "If-Match precondition failed")
		return
	}
	if _, ok := err.(*preconditionRequiredError); ok {
		api.writeError(w, r, http.StatusPreconditionRequired, "PreconditionRequired", "If-Match header is required")
		return
	}
	api.writeQueryError(w, r, err)
}

// serveCollectionSchema implements GET /collections/{id}/schema (Part 4):
// JSON Schema for feature creation/replacement on this collection.
func (api *FeatureAPI) serveCollectionSchema(w http.ResponseWriter, r *http.Request) {
	collection := httptreemux.ContextParams(r.Context())["collection"]
	if !api.cfg.Write.Enabled || !api.cfg.Write.AllowsOperation(collection, "create") {
		api.writeError(w, r, http.StatusNotFound, "NotFound", "Schema not available for this collection")
		return
	}
	sd, err := api.service.SchemaDescriptorFor(r.Context(), collection)
	if err != nil {
		api.writeQueryError(w, r, err)
		return
	}
	schema := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"title":   collection + " feature",
		"type":    "object",
	}
	props := map[string]any{}
	required := []string{}
	for _, p := range sd.Properties {
		if p.ReadOnly {
			continue
		}
		prop := map[string]any{}
		switch p.Type {
		case feature.TypeInteger:
			prop["type"] = "integer"
		case feature.TypeDecimal:
			prop["type"] = "number"
		case feature.TypeString:
			prop["type"] = "string"
			if p.MaxLength > 0 {
				prop["maxLength"] = p.MaxLength
			}
		case feature.TypeBoolean:
			prop["type"] = "boolean"
		case feature.TypeDateTime:
			prop["type"] = "string"
			prop["format"] = "date-time"
		}
		if len(p.AllowedValues) > 0 {
			prop["enum"] = p.AllowedValues
		}
		props[p.Name] = prop
		if p.Required {
			required = append(required, p.Name)
		}
	}
	// Geometry: GeoJSON geometry object or null.
	props["geometry"] = map[string]any{
		"oneOf": []any{
			map[string]any{"type": "object"},
			map[string]any{"type": "null"},
		},
	}
	schema["properties"] = map[string]any{
		"type":       map[string]any{"const": "Feature"},
		"geometry":   props["geometry"],
		"properties": map[string]any{"type": "object", "properties": props},
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	api.writeJSON(w, r, http.StatusOK, "application/schema+json", schema)
}
