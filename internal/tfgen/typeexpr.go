// Package tfgen turns a provider schema into an OpenTofu variables.tf.
package tfgen

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// collectionKind is a one-element complex type (list/set/map).
type collectionKind = string

// renderTypeJSON renders the legacy `type` field of an attribute, which is
// either a primitive name or a [kind, arg] tuple, into an HCL type expression.
func renderTypeJSON(raw json.RawMessage) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("decode type: %w", err)
	}
	return renderTypeValue(v)
}

func renderTypeValue(v any) (string, error) {
	switch t := v.(type) {
	case string:
		switch t {
		case "string", "number", "bool", "any", "dynamic":
			return t, nil
		default:
			return "", fmt.Errorf("unknown primitive type %q", t)
		}
	case []any:
		if len(t) != 2 {
			return "", fmt.Errorf("malformed complex type %v", t)
		}
		kind, ok := t[0].(string)
		if !ok {
			return "", fmt.Errorf("malformed complex type kind %v", t[0])
		}
		switch kind {
		case "list", "set", "map":
			inner, err := renderTypeValue(t[1])
			if err != nil {
				return "", err
			}
			return kind + "(" + inner + ")", nil
		case "object":
			m, ok := t[1].(map[string]any)
			if !ok {
				return "", fmt.Errorf("object type argument must be a mapping")
			}
			return renderJSONObject(m)
		case "tuple":
			items, ok := t[1].([]any)
			if !ok {
				return "", fmt.Errorf("tuple type argument must be a list")
			}
			parts := make([]string, len(items))
			for i, item := range items {
				s, err := renderTypeValue(item)
				if err != nil {
					return "", err
				}
				parts[i] = s
			}
			return "tuple([" + strings.Join(parts, ", ") + "])", nil
		default:
			return "", fmt.Errorf("unsupported complex type %q", kind)
		}
	default:
		return "", fmt.Errorf("unsupported type value of kind %T", v)
	}
}

func renderJSONObject(m map[string]any) (string, error) {
	keys := slices.Sorted(maps.Keys(m))
	var b strings.Builder
	b.WriteString("object({")
	for i, k := range keys {
		s, err := renderTypeValue(m[k])
		if err != nil {
			return "", fmt.Errorf("object field %q: %w", k, err)
		}
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "\n  %s = %s", k, s)
	}
	if len(keys) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("})")
	return b.String(), nil
}

// collectionOf wraps an element type in a collection constructor.
func collectionOf(mode collectionKind, elem string) (string, error) {
	switch mode {
	case "list", "set", "map":
		return mode + "(" + elem + ")", nil
	default:
		return "", fmt.Errorf("unsupported nesting_mode %q", mode)
	}
}
