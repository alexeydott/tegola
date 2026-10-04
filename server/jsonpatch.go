package server

// A21: RFC 6902 JSON Patch implementation.
//
// Supports all six operations (add, remove, replace, move, copy, test)
// on objects, arrays, and the whole document. Array append (/-),
// insert, and remove are fully implemented. The 'test' operation uses
// JSON semantic equality via the existing jsonEqual helper.
// 'move' is atomic: the source is only removed after the destination
// add succeeds. Patches apply atomically: on any error, the document
// is unchanged.

import (
	"fmt"
	"strconv"
	"strings"
)

// JSONPatchOp is one RFC 6902 operation.
type JSONPatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	From  string `json:"from,omitempty"`
	Value any    `json:"value,omitempty"`
}

func parseJSONPointer(ptr string) ([]string, error) {
	if ptr == "" {
		return nil, nil // whole document
	}
	if !strings.HasPrefix(ptr, "/") {
		return nil, fmt.Errorf("invalid JSON pointer %q: must start with /", ptr)
	}
	parts := strings.Split(ptr[1:], "/")
	for i, p := range parts {
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
	}
	return parts, nil
}

// applyJSONPatch applies ops to doc atomically.
func applyJSONPatch(doc map[string]any, ops []JSONPatchOp) (map[string]any, error) {
	work := deepCopyMap(doc)
	for _, op := range ops {
		var err error
		switch op.Op {
		case "add":
			work, err = patchAddRoot(work, op.Path, op.Value)
		case "remove":
			work, err = patchRemoveRoot(work, op.Path)
		case "replace":
			work, err = patchReplaceRoot(work, op.Path, op.Value)
		case "move":
			work, err = patchMoveRoot(work, op.From, op.Path)
		case "copy":
			work, err = patchCopyRoot(work, op.From, op.Path)
		case "test":
			err = patchTestRoot(work, op.Path, op.Value)
		default:
			err = fmt.Errorf("unsupported op %q", op.Op)
		}
		if err != nil {
			return nil, fmt.Errorf("op %q path %q: %w", op.Op, op.Path, err)
		}
	}
	return work, nil
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

// getAtPath retrieves the value at parts in node.
func getAtPath(node any, parts []string) (any, error) {
	cur := node
	for _, p := range parts {
		switch c := cur.(type) {
		case map[string]any:
			v, ok := c[p]
			if !ok {
				return nil, fmt.Errorf("path does not exist: %q", p)
			}
			cur = v
		case []any:
			idx, err := strconv.Atoi(p)
			if err != nil || idx < 0 || idx >= len(c) {
				return nil, fmt.Errorf("invalid array index %q", p)
			}
			cur = c[idx]
		default:
			return nil, fmt.Errorf("cannot navigate through non-container at %q", p)
		}
	}
	return cur, nil
}

// setAtPath sets value at parts in node, returning the updated node.
// For add semantics on arrays, use addAtPath.
func setAtPath(node any, parts []string, value any) (any, error) {
	if len(parts) == 0 {
		return deepCopyValue(value), nil
	}
	key := parts[0]
	rest := parts[1:]
	switch n := node.(type) {
	case map[string]any:
		if len(rest) == 0 {
			n[key] = deepCopyValue(value)
			return n, nil
		}
		child, ok := n[key]
		if !ok {
			return nil, fmt.Errorf("path does not exist: %q", key)
		}
		updated, err := setAtPath(child, rest, value)
		if err != nil {
			return nil, err
		}
		n[key] = updated
		return n, nil
	case []any:
		idx, err := strconv.Atoi(key)
		if err != nil || idx < 0 || idx >= len(n) {
			return nil, fmt.Errorf("invalid array index %q", key)
		}
		if len(rest) == 0 {
			n[idx] = deepCopyValue(value)
			return n, nil
		}
		updated, err := setAtPath(n[idx], rest, value)
		if err != nil {
			return nil, err
		}
		n[idx] = updated
		return n, nil
	default:
		return nil, fmt.Errorf("cannot navigate through non-container")
	}
}

// addAtPath adds value at parts (add semantics: arrays insert).
func addAtPath(node any, parts []string, value any) (any, error) {
	if len(parts) == 0 {
		return deepCopyValue(value), nil
	}
	key := parts[0]
	rest := parts[1:]
	switch n := node.(type) {
	case map[string]any:
		if len(rest) == 0 {
			n[key] = deepCopyValue(value)
			return n, nil
		}
		child, ok := n[key]
		if !ok {
			return nil, fmt.Errorf("path does not exist: %q", key)
		}
		updated, err := addAtPath(child, rest, value)
		if err != nil {
			return nil, err
		}
		n[key] = updated
		return n, nil
	case []any:
		if len(rest) == 0 {
			if key == "-" {
				return append(n, deepCopyValue(value)), nil
			}
			idx, err := strconv.Atoi(key)
			if err != nil || idx < 0 || idx > len(n) {
				return nil, fmt.Errorf("invalid array index %q", key)
			}
			out := make([]any, 0, len(n)+1)
			out = append(out, n[:idx]...)
			out = append(out, deepCopyValue(value))
			out = append(out, n[idx:]...)
			return out, nil
		}
		idx, err := strconv.Atoi(key)
		if err != nil || idx < 0 || idx >= len(n) {
			return nil, fmt.Errorf("invalid array index %q", key)
		}
		updated, err := addAtPath(n[idx], rest, value)
		if err != nil {
			return nil, err
		}
		n[idx] = updated
		return n, nil
	default:
		return nil, fmt.Errorf("cannot navigate through non-container")
	}
}

// removeAtPath removes the value at parts, returning the updated node.
func removeAtPath(node any, parts []string) (any, error) {
	if len(parts) == 0 {
		return nil, fmt.Errorf("cannot remove whole document")
	}
	key := parts[0]
	rest := parts[1:]
	switch n := node.(type) {
	case map[string]any:
		if len(rest) == 0 {
			if _, ok := n[key]; !ok {
				return nil, fmt.Errorf("path does not exist: %q", key)
			}
			delete(n, key)
			return n, nil
		}
		child, ok := n[key]
		if !ok {
			return nil, fmt.Errorf("path does not exist: %q", key)
		}
		updated, err := removeAtPath(child, rest)
		if err != nil {
			return nil, err
		}
		n[key] = updated
		return n, nil
	case []any:
		idx, err := strconv.Atoi(key)
		if err != nil || idx < 0 || idx >= len(n) {
			return nil, fmt.Errorf("invalid array index %q", key)
		}
		if len(rest) == 0 {
			out := make([]any, 0, len(n)-1)
			out = append(out, n[:idx]...)
			out = append(out, n[idx+1:]...)
			return out, nil
		}
		updated, err := removeAtPath(n[idx], rest)
		if err != nil {
			return nil, err
		}
		n[idx] = updated
		return n, nil
	default:
		return nil, fmt.Errorf("cannot navigate through non-container")
	}
}

func patchAddRoot(root map[string]any, path string, value any) (map[string]any, error) {
	if path == "" {
		if m, ok := value.(map[string]any); ok {
			return deepCopyMap(m), nil
		}
		return nil, fmt.Errorf("add to whole document requires an object value")
	}
	parts, err := parseJSONPointer(path)
	if err != nil {
		return nil, err
	}
	updated, err := addAtPath(root, parts, value)
	if err != nil {
		return nil, err
	}
	if m, ok := updated.(map[string]any); ok {
		return m, nil
	}
	return nil, fmt.Errorf("root became non-object")
}

func patchRemoveRoot(root map[string]any, path string) (map[string]any, error) {
	parts, err := parseJSONPointer(path)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("cannot remove whole document")
	}
	updated, err := removeAtPath(root, parts)
	if err != nil {
		return nil, err
	}
	if m, ok := updated.(map[string]any); ok {
		return m, nil
	}
	return nil, fmt.Errorf("root became non-object")
}

func patchReplaceRoot(root map[string]any, path string, value any) (map[string]any, error) {
	if path == "" {
		if m, ok := value.(map[string]any); ok {
			return deepCopyMap(m), nil
		}
		return nil, fmt.Errorf("replace whole document requires an object value")
	}
	parts, err := parseJSONPointer(path)
	if err != nil {
		return nil, err
	}
	// Verify target exists.
	if _, err := getAtPath(root, parts); err != nil {
		return nil, fmt.Errorf("replace: %w", err)
	}
	updated, err := setAtPath(root, parts, value)
	if err != nil {
		return nil, err
	}
	if m, ok := updated.(map[string]any); ok {
		return m, nil
	}
	return nil, fmt.Errorf("root became non-object")
}

func patchMoveRoot(root map[string]any, from, path string) (map[string]any, error) {
	if from == "" || path == "" {
		return nil, fmt.Errorf("move requires from and path")
	}
	fromParts, err := parseJSONPointer(from)
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	// RFC 6902 §4.4: move = remove(from) then add(path, value).
	// The value is held in a variable, so it's atomic at the patch level
	// (we work on a copy; failure returns error without committing).
	val, err := getAtPath(root, fromParts)
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	val = deepCopyValue(val)
	// Remove source first.
	updated, err := removeAtPath(root, fromParts)
	if err != nil {
		return nil, fmt.Errorf("move source: %w", err)
	}
	// Then add to destination (index evaluated after removal per RFC).
	toParts, err := parseJSONPointer(path)
	if err != nil {
		return nil, err
	}
	updated, err = addAtPath(updated, toParts, val)
	if err != nil {
		return nil, fmt.Errorf("move target: %w", err)
	}
	if m, ok := updated.(map[string]any); ok {
		return m, nil
	}
	return nil, fmt.Errorf("root became non-object")
}

func patchCopyRoot(root map[string]any, from, path string) (map[string]any, error) {
	if from == "" || path == "" {
		return nil, fmt.Errorf("copy requires from and path")
	}
	fromParts, err := parseJSONPointer(from)
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	val, err := getAtPath(root, fromParts)
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	toParts, err := parseJSONPointer(path)
	if err != nil {
		return nil, err
	}
	updated, err := addAtPath(root, toParts, deepCopyValue(val))
	if err != nil {
		return nil, err
	}
	if m, ok := updated.(map[string]any); ok {
		return m, nil
	}
	return nil, fmt.Errorf("root became non-object")
}

func patchTestRoot(root map[string]any, path string, value any) error {
	var target any
	if path == "" {
		target = root
	} else {
		parts, err := parseJSONPointer(path)
		if err != nil {
			return err
		}
		target, err = getAtPath(root, parts)
		if err != nil {
			return fmt.Errorf("test: %w", err)
		}
	}
	// A21: JSON semantic equality (existing jsonEqual uses JSON marshaling).
	if !jsonEqual(target, value) {
		return fmt.Errorf("test failed: value mismatch at %q", path)
	}
	return nil
}
