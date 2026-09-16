package tfgen

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/ivaninkv/tofu-resgen/internal/schema"
)

// TFVarsOptions control generation of the example terraform.tfvars.
type TFVarsOptions struct {
	// VarName is the module variable name, e.g. "vpc_subnet". It becomes the
	// only attribute of the example file.
	VarName string
	// ResourceName is the provider resource type, e.g. "yandex_vpc_subnet".
	// It appears in the file comment only.
	ResourceName string
	// ExampleName is the map key of the single example instance. An empty
	// value means DefaultExampleName.
	ExampleName string
	// Defaults supplies the value an optional attribute falls back to; used
	// only when Optional is set. May be nil.
	Defaults *Defaults
	// Optional emits optional attributes as comments, showing the value from
	// Defaults where one is declared and a placeholder otherwise. When false
	// the example carries the required attributes only, which is exactly what
	// the module variable demands.
	Optional bool
}

// DefaultExampleName is the map key of the example instance.
const DefaultExampleName = "example"

// GenerateTFVars renders the example terraform.tfvars for one resource.
//
// The file is a starting point, not a value set: placeholders are obvious
// (`"<v4_cidr_blocks>"`) and must be replaced. It is deliberately named
// *.example, so that OpenTofu never loads it as a real variable file.
//
// Only the required attributes are emitted: an optional attribute is either
// absent (the type's optional() wrapper, or the default the module declares,
// applies) or shown commented out with TFVarsOptions.Optional. Collections are
// rendered with one element, because an empty literal would not show the shape
// a user has to fill in.
func GenerateTFVars(res schema.Schema, opt TFVarsOptions) ([]byte, error) {
	if opt.VarName == "" {
		return nil, errors.New("variable name is required")
	}
	if !isIdent(opt.VarName) {
		return nil, fmt.Errorf("variable name %q is not a valid HCL identifier", opt.VarName)
	}
	assignment, err := exampleAssignment(res, opt)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	b.WriteString(header)
	b.WriteString("# Copy this file to terraform.tfvars and replace the <placeholder> values.\n")
	fmt.Fprintf(&b, "# One %s instance per map entry; the example below is named %q.\n\n",
		opt.ResourceName, exampleName(opt))
	b.WriteString(assignment)
	return hclwrite.Format([]byte(b.String())), nil
}

// exampleAssignment renders the `var = { example = { ... } }` assignment: the
// body of terraform.tfvars.example and of the README usage example. A module
// call and a tfvars file take the same values, so both carry this one
// rendering and cannot drift apart.
func exampleAssignment(res schema.Schema, opt TFVarsOptions) (string, error) {
	const instanceIndent = 4
	var body strings.Builder
	if err := writeExampleFields(&body, instanceIndent, nil, res.ConfigAttributes(), opt); err != nil {
		return "", err
	}
	instance := "{}"
	if body.Len() > 0 {
		instance = "{\n" + body.String() + "  }"
	}
	return fmt.Sprintf("%s = {\n  %s = %s\n}\n",
		opt.VarName, objectKeyExpr(exampleName(opt)), instance), nil
}

// exampleName is the map key of the example instance.
func exampleName(opt TFVarsOptions) string {
	if opt.ExampleName != "" {
		return opt.ExampleName
	}
	return DefaultExampleName
}

// objectKeyExpr renders an object key: a bare identifier where HCL allows one,
// a quoted string otherwise.
func objectKeyExpr(key string) string {
	if isIdent(key) {
		return key
	}
	return strconv.Quote(key)
}

// writeExampleFields renders the fields of one object literal. indent is the
// column the fields start at.
func writeExampleFields(b *strings.Builder, indent int, path []string, attrs map[string]schema.Attribute, opt TFVarsOptions) error {
	for _, name := range slices.Sorted(maps.Keys(attrs)) {
		a := attrs[name]
		switch {
		case a.ComputedOnly():
			continue
		case a.Required:
			if err := writeExampleField(b, indent, append(path, name), name, a, opt, false); err != nil {
				return err
			}
		case opt.Optional:
			if err := writeExampleField(b, indent, append(path, name), name, a, opt, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeExampleField renders one `name = value` line, commented out when the
// attribute is optional. A commented optional attribute shows the value it
// falls back to, so that the example documents the module default.
func writeExampleField(b *strings.Builder, indent int, path []string, name string, a schema.Attribute, opt TFVarsOptions, comment bool) error {
	if !isIdent(name) {
		return fmt.Errorf("attribute %q is not a valid HCL identifier", name)
	}
	expr, err := exampleValue(indent, path, a)
	if err != nil {
		return err
	}
	if comment {
		if v, ok := opt.Defaults.Lookup(path); ok {
			lit, err := hclLiteral(v)
			if err != nil {
				return fmt.Errorf("default for %s: %w", joinPath(path...), err)
			}
			expr = lit
		}
	}
	line := fmt.Sprintf("%s%s = %s\n", pad(indent), name, expr)
	if !comment {
		b.WriteString(line)
		return nil
	}
	// Every line of a commented field carries the comment marker, so that a
	// multi-line object stays a comment as a whole.
	for _, l := range strings.Split(strings.TrimSuffix(line, "\n"), "\n") {
		fmt.Fprintf(b, "%s# %s\n", pad(indent), strings.TrimPrefix(l, pad(indent)))
	}
	return nil
}

// exampleValue renders a placeholder value for one attribute. indent is the
// column of the attribute's own line; the continuation lines of a multi-line
// value are indented relative to it.
func exampleValue(indent int, path []string, a schema.Attribute) (string, error) {
	if a.NestedType != nil {
		switch a.NestedType.NestingMode {
		case "single":
			return exampleObject(indent, path, a.NestedType.Attributes)
		case "list", "set":
			elem, err := exampleObject(indent, append(path, "item"), a.NestedType.Attributes)
			if err != nil {
				return "", err
			}
			return "[" + elem + "]", nil
		case "map":
			elem, err := exampleObject(indent+2, append(path, "key"), a.NestedType.Attributes)
			if err != nil {
				return "", err
			}
			return keyedLiteral(indent, path, elem), nil
		default:
			return "", fmt.Errorf("attribute %s: unsupported nesting mode %q", joinPath(path...), a.NestedType.NestingMode)
		}
	}
	var v any
	if err := json.Unmarshal(a.Type, &v); err != nil {
		return "", fmt.Errorf("attribute %s: decode type: %w", joinPath(path...), err)
	}
	return exampleTypeValue(indent, path, v)
}

// exampleObject renders an object literal holding every required field.
func exampleObject(indent int, path []string, attrs map[string]schema.Attribute) (string, error) {
	fields, err := requiredFields(attrs)
	if err != nil {
		return "", err
	}
	if len(fields) == 0 {
		return "{}", nil
	}
	var b strings.Builder
	b.WriteString("{\n")
	for _, f := range fields {
		expr, err := exampleValue(indent+2, append(path, f.name), f.attr)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s%s = %s\n", pad(indent+2), f.name, expr)
	}
	b.WriteString(pad(indent) + "}")
	return b.String(), nil
}

// exampleJSONObject renders an object literal for a legacy `type` field, where
// every field is required.
func exampleJSONObject(indent int, path []string, m map[string]any) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	var b strings.Builder
	b.WriteString("{\n")
	for _, k := range slices.Sorted(maps.Keys(m)) {
		expr, err := exampleTypeValue(indent+2, append(path, k), m[k])
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s%s = %s\n", pad(indent+2), k, expr)
	}
	b.WriteString(pad(indent) + "}")
	return b.String(), nil
}

// exampleTypeValue renders a placeholder for a decoded `type` field.
func exampleTypeValue(indent int, path []string, v any) (string, error) {
	switch t := v.(type) {
	case string:
		switch t {
		case "string", "any", "dynamic":
			// A string is a valid value for any type; the placeholder names
			// the attribute it stands for.
			return strconv.Quote(placeholder(path)), nil
		case "number":
			return "0", nil
		case "bool":
			return "false", nil
		}
		return "", fmt.Errorf("attribute %s: unknown primitive type %q", joinPath(path...), t)
	case []any:
		if len(t) != 2 {
			return "", fmt.Errorf("attribute %s: malformed complex type %v", joinPath(path...), t)
		}
		kind, ok := t[0].(string)
		if !ok {
			return "", fmt.Errorf("attribute %s: malformed complex type kind %v", joinPath(path...), t[0])
		}
		switch kind {
		case "list", "set":
			elem, err := exampleTypeValue(indent, append(path, "item"), t[1])
			if err != nil {
				return "", err
			}
			return "[" + elem + "]", nil
		case "map":
			elem, err := exampleTypeValue(indent+2, append(path, "key"), t[1])
			if err != nil {
				return "", err
			}
			return keyedLiteral(indent, path, elem), nil
		case "object":
			m, ok := t[1].(map[string]any)
			if !ok {
				return "", fmt.Errorf("attribute %s: object type argument must be a mapping", joinPath(path...))
			}
			return exampleJSONObject(indent, path, m)
		case "tuple":
			items, ok := t[1].([]any)
			if !ok {
				return "", fmt.Errorf("attribute %s: tuple type argument must be a list", joinPath(path...))
			}
			parts := make([]string, len(items))
			for i, item := range items {
				s, err := exampleTypeValue(indent, append(path, "item"), item)
				if err != nil {
					return "", err
				}
				parts[i] = s
			}
			return "[" + strings.Join(parts, ", ") + "]", nil
		}
		return "", fmt.Errorf("attribute %s: unsupported complex type %q", joinPath(path...), kind)
	default:
		return "", fmt.Errorf("attribute %s: unsupported type value of kind %T", joinPath(path...), v)
	}
}

// keyedLiteral renders a one-entry map literal.
func keyedLiteral(indent int, path []string, elem string) string {
	key := strconv.Quote(placeholder(append(path, "key")))
	return fmt.Sprintf("{\n%s%s = %s\n%s}", pad(indent+2), key, elem, pad(indent))
}

// requiredFields lists the required attributes of an object, in lexical order.
// Optional fields are omitted: the object type allows their absence, and a
// commented-out example of every optional field would drown the structure.
func requiredFields(attrs map[string]schema.Attribute) ([]field, error) {
	var out []field
	for _, name := range slices.Sorted(maps.Keys(attrs)) {
		a := attrs[name]
		if a.ComputedOnly() || !a.Required {
			continue
		}
		if !isIdent(name) {
			return nil, fmt.Errorf("attribute %q is not a valid HCL identifier", name)
		}
		out = append(out, field{name: name, attr: a})
	}
	return out, nil
}

type field struct {
	name string
	attr schema.Attribute
}

// placeholder renders the marker a user has to replace: the attribute path
// inside the example instance, e.g. "<boot_disk.item.disk_id>".
func placeholder(path []string) string {
	return "<" + joinPath(path...) + ">"
}
