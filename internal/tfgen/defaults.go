package tfgen

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ivaninkv/tofu-resgen/internal/schema"
)

// Defaults holds per-resource default values for optional attributes, keyed by
// the nested attribute path.
type Defaults struct {
	root map[string]any
}

// LoadDefaults reads a YAML (or JSON) defaults file.
func LoadDefaults(path string) (*Defaults, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read defaults: %w", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse defaults %s: %w", path, err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return &Defaults{root: doc}, nil
}

// NewDefaults wraps an already-decoded mapping (used by tests).
func NewDefaults(root map[string]any) *Defaults {
	if root == nil {
		root = map[string]any{}
	}
	return &Defaults{root: root}
}

// Lookup resolves a default value along an attribute path. A nil receiver, or
// a path that is absent, yields ok=false.
func (d *Defaults) Lookup(path []string) (any, bool) {
	if d == nil || len(path) == 0 {
		return nil, false
	}
	var cur any = d.root
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok := m[key]
		if !ok {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

// Validate checks every default against the resource schema: paths must exist,
// top-level target attributes must be optional, and literals must match the
// schema type.
func (d *Defaults) Validate(res schema.Schema) error {
	if d == nil {
		return nil
	}
	return validateMap(d.root, res.ConfigAttributes(), nil, false)
}

// validateMap checks a mapping of defaults. At the top level (nested=false)
// every entry must target an optional attribute, because a default is only
// meaningful where the generator emits optional(). Inside an object literal
// (nested=true) required fields are expected to be present instead.
func validateMap(m map[string]any, attrs map[string]schema.Attribute, path []string, nested bool) error {
	for _, name := range slices.Sorted(maps.Keys(m)) {
		fieldPath := append(append([]string{}, path...), name)
		a, ok := attrs[name]
		if !ok {
			return fmt.Errorf("default for unknown attribute %s", joinPath(fieldPath...))
		}
		if !nested && !a.Optional {
			return fmt.Errorf("default for attribute %s: attribute is not optional", joinPath(fieldPath...))
		}
		if err := checkValue(m[name], a, fieldPath); err != nil {
			return err
		}
	}
	return nil
}

func checkValue(v any, a schema.Attribute, path []string) error {
	if a.NestedType != nil {
		return checkNested(v, a.NestedType, path)
	}
	var tv any
	if err := json.Unmarshal(a.Type, &tv); err != nil {
		return fmt.Errorf("attribute %s: decode type: %w", joinPath(path...), err)
	}
	return checkTypeValue(v, tv, path)
}

func checkNested(v any, nt *schema.NestedType, path []string) error {
	switch nt.NestingMode {
	case "single":
		m, ok := v.(map[string]any)
		if !ok {
			return typeError(path, "object", v)
		}
		return validateObjectLiteral(m, nt.Attributes, path)
	case "map":
		m, ok := v.(map[string]any)
		if !ok {
			return typeError(path, "map of objects", v)
		}
		for _, k := range slices.Sorted(maps.Keys(m)) {
			sub, ok := m[k].(map[string]any)
			if !ok {
				return typeError(append(path, k), "object", m[k])
			}
			if err := validateObjectLiteral(sub, nt.Attributes, append(path, k)); err != nil {
				return err
			}
		}
		return nil
	case "list", "set":
		items, ok := v.([]any)
		if !ok {
			return typeError(path, nt.NestingMode+" of objects", v)
		}
		for i, item := range items {
			sub, ok := item.(map[string]any)
			if !ok {
				return typeError(append(path, fmt.Sprintf("[%d]", i)), "object", item)
			}
			if err := validateObjectLiteral(sub, nt.Attributes, append(path, fmt.Sprintf("[%d]", i))); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("attribute %s: unsupported nesting_mode %q", joinPath(path...), nt.NestingMode)
	}
}

// validateObjectLiteral checks an object default against nested attributes,
// requiring every required sub-field to be present.
func validateObjectLiteral(m map[string]any, attrs map[string]schema.Attribute, path []string) error {
	for _, name := range slices.Sorted(maps.Keys(attrs)) {
		if attrs[name].Required {
			if _, ok := m[name]; !ok {
				return fmt.Errorf("default for %s: missing required field %q", joinPath(path...), name)
			}
		}
	}
	return validateMap(m, attrs, path, true)
}

func checkTypeValue(v any, tv any, path []string) error {
	switch t := tv.(type) {
	case string:
		return checkPrimitive(v, t, path)
	case []any:
		kind := t[0].(string)
		switch kind {
		case "list", "set":
			items, ok := v.([]any)
			if !ok {
				return typeError(path, kind, v)
			}
			for i, item := range items {
				if err := checkTypeValue(item, t[1], append(path, fmt.Sprintf("[%d]", i))); err != nil {
					return err
				}
			}
			return nil
		case "map":
			m, ok := v.(map[string]any)
			if !ok {
				return typeError(path, "map", v)
			}
			for _, k := range slices.Sorted(maps.Keys(m)) {
				if err := checkTypeValue(m[k], t[1], append(path, k)); err != nil {
					return err
				}
			}
			return nil
		case "object":
			m, ok := v.(map[string]any)
			if !ok {
				return typeError(path, "object", v)
			}
			fields, _ := t[1].(map[string]any)
			for _, k := range slices.Sorted(maps.Keys(m)) {
				ft, ok := fields[k]
				if !ok {
					return fmt.Errorf("default for %s: unknown field %q", joinPath(path...), k)
				}
				if err := checkTypeValue(m[k], ft, append(path, k)); err != nil {
					return err
				}
			}
			return nil
		case "tuple":
			items, ok := v.([]any)
			elems, _ := t[1].([]any)
			if !ok || len(items) != len(elems) {
				return typeError(path, "tuple", v)
			}
			for i := range items {
				if err := checkTypeValue(items[i], elems[i], append(path, fmt.Sprintf("[%d]", i))); err != nil {
					return err
				}
			}
			return nil
		}
	}
	return fmt.Errorf("default for %s: unsupported schema type", joinPath(path...))
}

func checkPrimitive(v any, want string, path []string) error {
	ok := false
	switch want {
	case "string":
		_, ok = v.(string)
	case "bool":
		_, ok = v.(bool)
	case "number":
		switch v.(type) {
		case int, int64, float64:
			ok = true
		}
	case "any", "dynamic":
		ok = true
	}
	if !ok {
		return typeError(path, want, v)
	}
	return nil
}

func typeError(path []string, want string, got any) error {
	return fmt.Errorf("default for %s: want %s, got %T", joinPath(path...), want, got)
}

func joinPath(parts ...string) string {
	return strings.Join(parts, ".")
}
