package feature

import (
	"fmt"
	"strconv"
	"strings"
)

// PhysicalFeatureKey identifies one stored object independently of the
// collection name or protocol used to address it. Locks, revisions and
// audit use this key. See ADR-0011.
type PhysicalFeatureKey struct {
	// Domain is the transaction domain (one native writer scope).
	Domain string
	// Relation is the physical relation (table) name.
	Relation string
	// PK is the storage primary key value in canonical form.
	PK string
}

func (k PhysicalFeatureKey) String() string {
	return k.Domain + "." + k.Relation + "." + k.PK
}

// EncodeWFSFID reversibly encodes collection + numeric ID as a WFS
// feature ID. The result satisfies XML NCName constraints: it starts with
// a letter or underscore and contains only name characters.
func EncodeWFSFID(collection string, id uint64) (string, error) {
	if collection == "" {
		return "", fmt.Errorf("empty collection for FID encoding")
	}
	safe := sanitizeNCName(collection)
	if safe == "" {
		return "", fmt.Errorf("collection %q has no NCName-safe characters", collection)
	}
	return safe + "." + strconv.FormatUint(id, 10), nil
}

// DecodeWFSFID splits a WFS feature ID into collection and numeric ID.
// The collection part is the encoded (sanitized) form; callers map it
// back through the catalog.
func DecodeWFSFID(fid string) (collection string, id uint64, err error) {
	idx := strings.LastIndex(fid, ".")
	if idx <= 0 || idx == len(fid)-1 {
		return "", 0, fmt.Errorf("malformed feature ID %q", fid)
	}
	collection = fid[:idx]
	n, err := strconv.ParseUint(fid[idx+1:], 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("malformed feature ID %q: %v", fid, err)
	}
	return collection, n, nil
}

func sanitizeNCName(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
			b.WriteRune(r)
		case r >= '0' && r <= '9', r == '.', r == '-':
			if i == 0 {
				b.WriteRune('_')
			}
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		return ""
	}
	if c := out[0]; c >= '0' && c <= '9' || c == '.' || c == '-' {
		out = "_" + out
	}
	return out
}
