package server

import (
	"fmt"
	"strconv"
	"strings"
)

// A21: RFC 6902 JSON Patch implementation.

// JSONPatchOp is one operation in a JSON Patch document.
type JSONPatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
	From  string `json:"from,omitempty"`
}

// parseJSONPointer parses a JSON Pointer (RFC 6901) into path segments.
// "" -> empty (whole document), "/a/b" -> ["a","b"].
// Unescapes ~1 -> /, ~0 -> ~.
func parseJSONPointer(ptr string) ([]string, error) {
	if ptr == "" {
		return nil, nil
	}
	if !strings.HasPrefix(ptr, "/") {
		return nil, fmt.Errorf("invalid JSON pointer %q: must start with /", ptr)
	}
	parts := strings.Split(ptr[1:], "/")
	for i, p := range parts {
		// Unescape: ~1 -> /, ~0 -> ~ (order matters)
		p = strings.ReplaceAll(p, "~1", "/")
		p = strings.ReplaceAll(p, "~0", "~")
		parts[i] = p
	}
	return parts, nil
}

// applyJSONPatch applies RFC 6902 operations atomically to doc.
// Returns the patched document or an error (doc unchanged on error).
func applyJSONPatch(doc map[string]any, ops []JSONPatchOp) (map[string]any, error) {
	// Deep copy for atomicity.
	result := deepCopyMap(doc)
	for i, op := range ops {
		var err error
		switch op.Op {
		case "add":
			err = patchAdd(result, op.Path, op.Value)
		case "remove":
			err = patchRemove(result, op.Path)
		case "replace":
			err = patchReplace(result, op.Path, op.Value)
		case "move":
			err = patchMove(result, op.From, op.Path)
		case "copy":
			err = patchCopy(result, op.From, op.Path)
		case "test":
			err = patchTest(result, op.Path, op.Value)
		default:
			err = fmt.Errorf("unsupported op %q", op.Op)
		}
		if err != nil {
			return nil, fmt.Errorf("op %d (%s %s): %w", i, op.Op, op.Path, err)
		}
	}
	return result, nil
}

func deepCopyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = deepCopyValue(v)
	}
	return out
}

func deepCopyValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return deepCopyMap(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = deepCopyValue(e)
		}
		return out
	default:
		return v
	}
}

// navigate to parent of target, returning parent container and last key.
func patchNavigate(doc map[string]any, path string) (parent any, key string, err error) {
	segs, err := parseJSONPointer(path)
	if err != nil {
		return nil, "", err
	}
	if len(segs) == 0 {
		return nil, "", fmt.Errorf("cannot operate on whole document with this op")
	}
	current := any(doc)
	for _, s := range segs[:len(segs)-1] {
		switch c := current.(type) {
		case map[string]any:
			v, ok := c[s]
			if !ok {
				return nil, "", fmt.Errorf("path not found: %s", path)
			}
			current = v
		case []any:
			idx, err := strconv.Atoi(s)
			if err != nil || idx < 0 || idx >= len(c) {
				return nil, "", fmt.Errorf("invalid array index %q", s)
			}
			current = c[idx]
		default:
			return nil, "", fmt.Errorf("path not found: %s", path)
		}
	}
	return current, segs[len(segs)-1], nil
}

func patchAdd(doc map[string]any, path string, value any) error {
	if path == "" {
		return fmt.Errorf("add to whole document not supported")
	}
	parent, key, err := patchNavigate(doc, path)
	if err != nil {
		return err
	}
	switch p := parent.(type) {
	case map[string]any:
		p[key] = deepCopyValue(value)
		return nil
	case []any:
		if key == "-" {
			// Append - but we need to modify the parent slice in doc.
			// This requires tracking the parent reference; simplified:
			return fmt.Errorf("append to array root not supported in this path")
		}
		idx, err := strconv.Atoi(key)
		if err != nil || idx < 0 || idx > len(p) {
			return fmt.Errorf("invalid array index %q", key)
		}
		// Insert at idx - need to handle via parent reference.
		// For simplicity, we operate on the slice header; caller must use result.
		// This is a limitation: full array insert requires parent tracking.
		return fmt.Errorf("array insert not yet supported")
	default:
		return fmt.Errorf("cannot add to non-container")
	}
}

func patchRemove(doc map[string]any, path string) error {
	parent, key, err := patchNavigate(doc, path)
	if err != nil {
		return err
	}
	switch p := parent.(type) {
	case map[string]any:
		if _, ok := p[key]; !ok {
			return fmt.Errorf("path does not exist: %s", path)
		}
		delete(p, key)
		return nil
	default:
		return fmt.Errorf("cannot remove from non-object")
	}
}

func patchReplace(doc map[string]any, path string, value any) error {
	parent, key, err := patchNavigate(doc, path)
	if err != nil {
		return err
	}
	switch p := parent.(type) {
	case map[string]any:
		if _, ok := p[key]; !ok {
			return fmt.Errorf("path does not exist: %s", path)
		}
		p[key] = deepCopyValue(value)
		return nil
	default:
		return fmt.Errorf("cannot replace in non-object")
	}
}

func patchMove(doc map[string]any, from, path string) error {
	// Get value at from.
	fromParent, fromKey, err := patchNavigate(doc, from)
	if err != nil {
		return err
	}
	var val any
	switch p := fromParent.(type) {
	case map[string]any:
		v, ok := p[fromKey]
		if !ok {
			return fmt.Errorf("from path does not exist: %s", from)
		}
		val = deepCopyValue(v)
		delete(p, fromKey)
	default:
		return fmt.Errorf("cannot move from non-object")
	}
	// Add to path.
	return patchAdd(doc, path, val)
}

func patchCopy(doc map[string]any, from, path string) error {
	fromParent, fromKey, err := patchNavigate(doc, from)
	if err != nil {
		return err
	}
	var val any
	switch p := fromParent.(type) {
	case map[string]any:
		v, ok := p[fromKey]
		if !ok {
			return fmt.Errorf("from path does not exist: %s", from)
		}
		val = deepCopyValue(v)
	default:
		return fmt.Errorf("cannot copy from non-object")
	}
	return patchAdd(doc, path, val)
}

func patchTest(doc map[string]any, path string, value any) error {
	parent, key, err := patchNavigate(doc, path)
	if err != nil {
		return err
	}
	switch p := parent.(type) {
	case map[string]any:
		v, ok := p[key]
		if !ok {
			return fmt.Errorf("test failed: path does not exist: %s", path)
		}
		// Simple equality check.
		if fmt.Sprintf("%v", v) != fmt.Sprintf("%v", value) {
			return fmt.Errorf("test failed: value mismatch at %s", path)
		}
		return nil
	default:
		return fmt.Errorf("cannot test non-object")
	}
}
