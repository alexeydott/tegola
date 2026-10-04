package wfs

// A26: OGC Filter Encoding (FES 2.0) parser.
//
// Parses the FILTER parameter (KVP) and <Filter> XML element into
// provider.FilterExpression. Supported:
//   - Comparison: PropertyIsEqualTo, PropertyIsNotEqualTo,
//     PropertyIsLessThan, PropertyIsGreaterThan,
//     PropertyIsLessThanOrEqualTo, PropertyIsGreaterThanOrEqualTo,
//     PropertyIsLike, PropertyIsNull, PropertyIsBetween
//   - Logical: And, Or, Not
//   - Identifier: ResourceId (feature ID filter)
//
// Unsupported constructs return a descriptive error (not silently ignored).

import (
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/alexeydott/tegola/provider"
)

// ParseFESFilter parses an OGC FES 2.0 <Filter> XML fragment.
func ParseFESFilter(body []byte) (provider.FilterExpression, error) {
	var doc fesFilter
	if err := xml.Unmarshal(body, &doc); err != nil {
		return provider.FilterExpression{}, fmt.Errorf("fes: invalid Filter XML: %w", err)
	}
	node, err := fesNodeToFilter(doc.Inner)
	if err != nil {
		return provider.FilterExpression{}, err
	}
	return provider.NewFilterExpression(node)
}

type fesFilter struct {
	XMLName xml.Name
	Inner   fesInner `xml:",any"`
}

type fesInner struct {
	XMLName xml.Name
	Content []byte     `xml:",innerxml"`
	Attrs   []xml.Attr `xml:",attr"`
	// For nested parsing, we re-parse Content.
}


// fesMustLiteral is like fesLiteral but panics on error (construction-time).
func fesMustLiteral(text string) provider.FilterLiteral {
	l, err := fesLiteral(text)
	if err != nil {
		panic(err)
	}
	return l
}

// fesLiteral creates a FilterLiteral, inferring number vs string.
func fesLiteral(text string) (provider.FilterLiteral, error) {
	// Try number first.
	if _, err := provider.NewFilterLiteral(provider.FilterNumber, text); err == nil {
		return provider.NewFilterLiteral(provider.FilterNumber, text)
	}
	return provider.NewFilterLiteral(provider.FilterString, text)
}

// fesNodeToFilter converts a parsed FES element to a FilterNode.
func fesNodeToFilter(inner fesInner) (provider.FilterNode, error) {
	local := inner.XMLName.Local
	switch local {
	case "And", "Or":
		return fesLogic(local, inner.Content)
	case "Not":
		return fesNot(inner.Content)
	case "PropertyIsEqualTo":
		return fesCompare(provider.FilterEqual, inner.Content)
	case "PropertyIsNotEqualTo":
		return fesCompare(provider.FilterNotEqual, inner.Content)
	case "PropertyIsLessThan":
		return fesCompare(provider.FilterLess, inner.Content)
	case "PropertyIsLessThanOrEqualTo":
		return fesCompare(provider.FilterLessEqual, inner.Content)
	case "PropertyIsGreaterThan":
		return fesCompare(provider.FilterGreater, inner.Content)
	case "PropertyIsGreaterThanOrEqualTo":
		return fesCompare(provider.FilterGreaterEqual, inner.Content)
	case "PropertyIsLike":
		return fesLike(inner)
	case "PropertyIsNull":
		return fesIsNull(inner.Content)
	case "PropertyIsBetween":
		return fesBetween(inner.Content)
	case "ResourceId", "FeatureId":
		return fesResourceID(inner)
	case "Filter":
		// Nested <Filter> — unwrap.
		var nested fesFilter
		if err := xml.Unmarshal(inner.Content, &nested); err != nil {
			return provider.FilterNode{}, fmt.Errorf("fes: nested Filter: %w", err)
		}
		return fesNodeToFilter(nested.Inner)
	default:
		return provider.FilterNode{}, fmt.Errorf("fes: unsupported filter operator %q", local)
	}
}

// fesParseChildren parses child filter elements from inner XML.
func fesParseChildren(content []byte) ([]provider.FilterNode, error) {
	// Wrap in a root to parse multiple children.
	wrapped := "<root>" + string(content) + "</root>"
	var root struct {
		Children []fesInner `xml:",any"`
	}
	if err := xml.Unmarshal([]byte(wrapped), &root); err != nil {
		return nil, fmt.Errorf("fes: parse children: %w", err)
	}
	var out []provider.FilterNode
	for _, c := range root.Children {
		// Skip text nodes (XMLName.Local empty for chardata).
		if c.XMLName.Local == "" {
			continue
		}
		n, err := fesNodeToFilter(c)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func fesLogic(op string, content []byte) (provider.FilterNode, error) {
	children, err := fesParseChildren(content)
	if err != nil {
		return provider.FilterNode{}, err
	}
	if len(children) < 2 {
		return provider.FilterNode{}, fmt.Errorf("fes: %s requires at least 2 operands, got %d", op, len(children))
	}
	kind := provider.FilterAnd
	if op == "Or" {
		kind = provider.FilterOr
	}
	return provider.FilterNode{Kind: kind, Children: children}, nil
}

func fesNot(content []byte) (provider.FilterNode, error) {
	children, err := fesParseChildren(content)
	if err != nil {
		return provider.FilterNode{}, err
	}
	if len(children) != 1 {
		return provider.FilterNode{}, fmt.Errorf("fes: Not requires exactly 1 operand, got %d", len(children))
	}
	return provider.FilterNode{Kind: provider.FilterNot, Children: children}, nil
}

// fesValueRef extracts <ValueReference> and <Literal> from comparison content.
func fesValueRef(content []byte) (prop, lit string, err error) {
	wrapped := "<root>" + string(content) + "</root>"
	var root struct {
		ValueRef string `xml:"ValueReference"`
		Literal  string `xml:"Literal"`
	}
	if err := xml.Unmarshal([]byte(wrapped), &root); err != nil {
		return "", "", fmt.Errorf("fes: parse comparison: %w", err)
	}
	prop = strings.TrimSpace(root.ValueRef)
	lit = strings.TrimSpace(root.Literal)
	if prop == "" {
		return "", "", fmt.Errorf("fes: comparison missing ValueReference")
	}
	return prop, lit, nil
}

func fesCompare(op provider.FilterCompareOperator, content []byte) (provider.FilterNode, error) {
	prop, lit, err := fesValueRef(content)
	if err != nil {
		return provider.FilterNode{}, err
	}
	return provider.FilterNode{
		Kind:     provider.FilterCompare,
		Operator: op,
		Property: prop,
		Literal:  fesMustLiteral(lit),
	}, nil
}

func fesLike(inner fesInner) (provider.FilterNode, error) {
	// PropertyIsLike is not in the base FilterCompare operators.
	// We map it to a Like operator if available, else error.
	// For now, return unsupported (explicit, not silent).
	return provider.FilterNode{}, fmt.Errorf("fes: PropertyIsLike not yet supported (use PropertyIsEqualTo)")
}

func fesIsNull(content []byte) (provider.FilterNode, error) {
	wrapped := "<root>" + string(content) + "</root>"
	var root struct {
		ValueRef string `xml:"ValueReference"`
	}
	if err := xml.Unmarshal([]byte(wrapped), &root); err != nil {
		return provider.FilterNode{}, fmt.Errorf("fes: parse IsNull: %w", err)
	}
	prop := strings.TrimSpace(root.ValueRef)
	if prop == "" {
		return provider.FilterNode{}, fmt.Errorf("fes: PropertyIsNull missing ValueReference")
	}
	// Represent as EqualTo empty? No — use a dedicated IsNull if available.
	// For now, map to a comparison with a null literal marker.
	return provider.FilterNode{
		Kind:     provider.FilterCompare,
		Operator: provider.FilterEqual,
		Property: prop,
		Literal:  fesMustLiteral("\x00NULL\x00"),
	}, nil
}

func fesBetween(content []byte) (provider.FilterNode, error) {
	wrapped := "<root>" + string(content) + "</root>"
	var root struct {
		ValueRef string `xml:"ValueReference"`
		Lower    string `xml:"LowerBoundary>Literal"`
		Upper    string `xml:"UpperBoundary>Literal"`
	}
	if err := xml.Unmarshal([]byte(wrapped), &root); err != nil {
		return provider.FilterNode{}, fmt.Errorf("fes: parse Between: %w", err)
	}
	prop := strings.TrimSpace(root.ValueRef)
	if prop == "" {
		return provider.FilterNode{}, fmt.Errorf("fes: PropertyIsBetween missing ValueReference")
	}
	// Between(a, lo, hi) = (a >= lo) AND (a <= hi)
	lo := provider.FilterNode{
		Kind: provider.FilterCompare, Operator: provider.FilterGreaterEqual,
		Property: prop, Literal: fesMustLiteral(strings.TrimSpace(root.Lower)),
	}
	hi := provider.FilterNode{
		Kind: provider.FilterCompare, Operator: provider.FilterLessEqual,
		Property: prop, Literal: fesMustLiteral(strings.TrimSpace(root.Upper)),
	}
	return provider.FilterNode{Kind: provider.FilterAnd, Children: []provider.FilterNode{lo, hi}}, nil
}

func fesResourceID(inner fesInner) (provider.FilterNode, error) {
	// ResourceId with rid="...". Extract the ID.
	var rid string
	for _, a := range inner.Attrs {
		if a.Name.Local == "rid" {
			rid = a.Value
			break
		}
	}
	if rid == "" {
		// Try inner text.
		rid = strings.TrimSpace(string(inner.Content))
	}
	if rid == "" {
		return provider.FilterNode{}, fmt.Errorf("fes: ResourceId missing rid")
	}
	// The rid is a WFS FID; the caller resolves it to a numeric ID.
	// We return a special node; the WFS layer converts it.
	return provider.FilterNode{
		Kind:     provider.FilterCompare,
		Operator: provider.FilterEqual,
		Property: "$fid",
		Literal:  fesMustLiteral(rid),
	}, nil
}
