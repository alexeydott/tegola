package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/alexeydott/tegola/internal/log"
	"github.com/alexeydott/tegola/ogc/features"
	"github.com/alexeydott/tegola/provider"
)

// queryFeatureRevision pairs the body with its revision. A concurrent commit
// between the feature snapshot and revision lookup must never grant a stale
// representation permission to overwrite the newer row.
func (api *FeatureAPI) queryFeatureRevision(
	ctx context.Context,
	collection string,
	id uint64,
	options features.QueryOptions,
) (features.Feature, string, error) {
	for attempt := 0; attempt < 3; attempt++ {
		before, err := api.currentRevision(ctx, collection, id)
		if err != nil {
			return features.Feature{}, "", err
		}
		f, err := api.service.QueryFeatureWithOptions(ctx, collection, id, options)
		if err != nil {
			return features.Feature{}, "", err
		}
		after, err := api.currentRevision(ctx, collection, id)
		if err != nil {
			return features.Feature{}, "", err
		}
		if before == after {
			return f, after, nil
		}
		log.Debug("[FIX] retry feature read after concurrent mutation", "collection", collection)
	}
	return features.Feature{}, "", &provider.MutationError{
		Kind:   provider.MutationErrPreconditionFailed,
		Reason: "feature changed during representation read",
	}
}

// The revision protects CAS; the digest distinguishes representations (CRS,
// links, JSON/HTML) of that revision for HTTP strong validator semantics.
func revisionETag(raw []byte, revision string) string {
	hash := strongETag(raw)
	if revision == "" {
		return hash
	}
	return `"` + revision + "." + strings.Trim(hash, `"`) + `"`
}

// A readback failure cannot turn a confirmed commit into a retryable failure.
func (api *FeatureAPI) writeCommittedWithoutRepresentation(
	w http.ResponseWriter,
	r *http.Request,
	status int,
	receipt provider.CommitReceipt,
	err error,
) {
	log.Error("[FIX] mutation committed but representation unavailable", "transaction", receipt.TransactionID, "error", err)
	featureProtocolHeaders(w.Header())
	w.Header().Set("Tegola-Commit-Status", "committed")
	w.Header().Set("Tegola-Transaction-ID", receipt.TransactionID)
	mergeFeatureHeader(w.Header(), "Access-Control-Expose-Headers", "Tegola-Commit-Status")
	mergeFeatureHeader(w.Header(), "Access-Control-Expose-Headers", "Tegola-Transaction-ID")
	w.WriteHeader(status)
}
