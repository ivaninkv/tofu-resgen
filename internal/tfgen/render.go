package tfgen

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/ivaninkv/tofu-resgen/internal/schema"
)

// Renderer turns schema attributes into HCL object fields.
type Renderer struct {
	// Defaults supplies values for optional attributes. May be nil.
	Defaults *Defaults
	// Doc emits schema descriptions as comments.
	Doc bool
}

// objectType renders `object({ ... })` for a set of attributes.
func (r *Renderer) objectType(path []string, attrs map[string]schema.Attribute) (string, error) {
	keys := slices.Sorted(maps.Keys(attrs))
	var b strings.Builder
	b.WriteString("object({")
	emitted := 0
	for _, name := range keys {
		a := attrs[name]
		if a.ComputedOnly() {
			continue
		}
		fieldPath := append(append([]string{}, path...), name)
		value, err := r.field(fieldPath, a)
		if err != nil {
			return "", err
		}
		b.WriteString("\n")
		if r.Doc && a.Description != "" {
			for _, line := range commentLines(a.Description) {
				b.WriteString(line + "\n")
			}
		}
		fmt.Fprintf(&b, "%s = %s", name, value)
		emitted++
	}
	if emitted > 0 {
		b.WriteString("\n")
	}
	b.WriteString("})")
	return b.String(), nil
}

// field renders the right-hand side of an object field, applying optional()
// wrapping and defaults where the schema allows them.
func (r *Renderer) field(path []string, a schema.Attribute) (string, error) {
	t, err := r.attributeType(path, a)
	if err != nil {
		return "", err
	}
	switch {
	case a.Required:
		return t, nil
	case a.Optional:
		if v, ok := r.Defaults.Lookup(path); ok {
			lit, err := hclLiteral(v)
			if err != nil {
				return "", fmt.Errorf("default for %s: %w", strings.Join(path, "."), err)
			}
			return fmt.Sprintf("optional(%s, %s)", t, lit), nil
		}
		return "optional(" + t + ")", nil
	default:
		return "", fmt.Errorf("attribute %s is neither required nor optional", strings.Join(path, "."))
	}
}

func (r *Renderer) attributeType(path []string, a schema.Attribute) (string, error) {
	if a.NestedType != nil {
		return r.nestedType(path, a.NestedType)
	}
	if len(a.Type) > 0 {
		return renderTypeJSON(a.Type)
	}
	return "", fmt.Errorf("attribute %s has no type information", strings.Join(path, "."))
}

func (r *Renderer) nestedType(path []string, nt *schema.NestedType) (string, error) {
	obj, err := r.objectType(path, nt.Attributes)
	if err != nil {
		return "", err
	}
	if nt.NestingMode == "single" {
		return obj, nil
	}
	return collectionOf(nt.NestingMode, obj)
}

// commentLines normalises a schema description into wrapped `#` comment lines.
func commentLines(desc string) []string {
	words := strings.Fields(desc)
	if len(words) == 0 {
		return nil
	}
	const width = 96
	var lines []string
	cur := "#"
	for _, w := range words {
		if cur != "#" && len(cur)+1+len(w) > width {
			lines = append(lines, cur)
			cur = "#"
		}
		cur += " " + w
	}
	return append(lines, cur)
}

// hclLiteral renders a YAML-decoded value as an HCL literal expression.
func hclLiteral(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "null", nil
	case bool:
		return strconv.FormatBool(t), nil
	case string:
		return strconv.Quote(t), nil
	case int:
		return strconv.Itoa(t), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64), nil
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			s, err := hclLiteral(e)
			if err != nil {
				return "", err
			}
			parts[i] = s
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case map[string]any:
		keys := slices.Sorted(maps.Keys(t))
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			s, err := hclLiteral(t[k])
			if err != nil {
				return "", err
			}
			parts = append(parts, fmt.Sprintf("%s = %s", k, s))
		}
		return "{ " + strings.Join(parts, ", ") + " }", nil
	default:
		return "", fmt.Errorf("unsupported literal of kind %T", v)
	}
}
