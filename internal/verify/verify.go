// Package verify checks that generated variables.tf matches the provider
// schema it was generated from. The expected tree is derived independently
// from the schema, so generator and verifier share no rendering logic.
package verify

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/ivaninkv/tofu-resgen/internal/schema"
	"github.com/ivaninkv/tofu-resgen/internal/tfgen"
)

// Node is a normalised type expression used to compare schema and HCL.
type Node struct {
	Kind  string
	Elem  *Node
	Attrs map[string]Field
}

// Field is one attribute inside an object type.
type Field struct {
	Type       *Node
	Optional   bool
	HasDefault bool
}

// Expected builds the type tree required by the resource schema: the variable
// type must be map(object(resource attributes)).
func Expected(res schema.Schema, defaults *tfgen.Defaults) (*Node, error) {
	obj, err := expectedObject(res.ConfigAttributes(), defaults, nil)
	if err != nil {
		return nil, err
	}
	return &Node{Kind: "map", Elem: obj}, nil
}

func expectedObject(attrs map[string]schema.Attribute, defaults *tfgen.Defaults, path []string) (*Node, error) {
	n := &Node{Kind: "object", Attrs: map[string]Field{}}
	for _, name := range slices.Sorted(maps.Keys(attrs)) {
		a := attrs[name]
		if a.ComputedOnly() {
			continue
		}
		fieldPath := append(append([]string{}, path...), name)
		t, err := expectedType(a, defaults, fieldPath)
		if err != nil {
			return nil, err
		}
		_, hasDefault := defaults.Lookup(fieldPath)
		n.Attrs[name] = Field{Type: t, Optional: a.Optional, HasDefault: hasDefault && a.Optional}
	}
	return n, nil
}

func expectedType(a schema.Attribute, defaults *tfgen.Defaults, path []string) (*Node, error) {
	if a.NestedType != nil {
		inner, err := expectedObject(a.NestedType.Attributes, defaults, path)
		if err != nil {
			return nil, err
		}
		if a.NestedType.NestingMode == "single" {
			return inner, nil
		}
		return &Node{Kind: a.NestedType.NestingMode, Elem: inner}, nil
	}
	var v any
	if err := json.Unmarshal(a.Type, &v); err != nil {
		return nil, fmt.Errorf("attribute %s: decode type: %w", path, err)
	}
	return typeNodeFromValue(v)
}

func typeNodeFromValue(v any) (*Node, error) {
	switch t := v.(type) {
	case string:
		return &Node{Kind: t}, nil
	case []any:
		kind, _ := t[0].(string)
		switch kind {
		case "list", "set", "map":
			elem, err := typeNodeFromValue(t[1])
			if err != nil {
				return nil, err
			}
			return &Node{Kind: kind, Elem: elem}, nil
		case "object":
			fields, _ := t[1].(map[string]any)
			n := &Node{Kind: "object", Attrs: map[string]Field{}}
			for _, k := range slices.Sorted(maps.Keys(fields)) {
				ft, err := typeNodeFromValue(fields[k])
				if err != nil {
					return nil, err
				}
				n.Attrs[k] = Field{Type: ft}
			}
			return n, nil
		case "tuple":
			return &Node{Kind: "tuple"}, nil
		}
		return nil, fmt.Errorf("unsupported complex type %q", kind)
	default:
		return nil, fmt.Errorf("unsupported type value of kind %T", v)
	}
}

// Conforms parses generated HCL and returns every way the declared variable
// type deviates from the schema.
func Conforms(src []byte, res schema.Schema, varName string, defaults *tfgen.Defaults) ([]string, error) {
	expected, err := Expected(res, defaults)
	if err != nil {
		return nil, err
	}
	got, err := actual(src, varName)
	if err != nil {
		return nil, err
	}
	var problems []string
	compare(expected, got, "type", &problems)
	return problems, nil
}

func actual(src []byte, varName string) (*Node, error) {
	file, diags := hclparse.NewParser().ParseHCL(src, "generated.tf")
	if diags.HasErrors() {
		return nil, fmt.Errorf("parse generated HCL: %s", diags.Error())
	}
	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		return nil, fmt.Errorf("generated HCL has unexpected body type %T", file.Body)
	}
	for _, blk := range body.Blocks {
		if blk.Type != "variable" || len(blk.Labels) != 1 || blk.Labels[0] != varName {
			continue
		}
		attr, ok := blk.Body.Attributes["type"]
		if !ok {
			return nil, fmt.Errorf("variable %q has no type attribute", varName)
		}
		return typeNodeFromExpr(attr.Expr)
	}
	return nil, fmt.Errorf("variable %q not found in generated HCL", varName)
}

func typeNodeFromExpr(e hclsyntax.Expression) (*Node, error) {
	switch x := e.(type) {
	case *hclsyntax.ScopeTraversalExpr:
		name := x.Traversal.RootName()
		switch name {
		case "string", "number", "bool", "any", "dynamic":
			return &Node{Kind: name}, nil
		}
		return nil, fmt.Errorf("unknown type identifier %q", name)
	case *hclsyntax.FunctionCallExpr:
		switch x.Name {
		case "list", "set", "map":
			if len(x.Args) != 1 {
				return nil, fmt.Errorf("%s() expects one argument", x.Name)
			}
			elem, err := typeNodeFromExpr(x.Args[0])
			if err != nil {
				return nil, err
			}
			return &Node{Kind: x.Name, Elem: elem}, nil
		case "object":
			if len(x.Args) != 1 {
				return nil, fmt.Errorf("object() expects one argument")
			}
			oc, ok := x.Args[0].(*hclsyntax.ObjectConsExpr)
			if !ok {
				return nil, fmt.Errorf("object() argument must be an object literal")
			}
			n := &Node{Kind: "object", Attrs: map[string]Field{}}
			for _, item := range oc.Items {
				key, err := objectKey(item.KeyExpr)
				if err != nil {
					return nil, err
				}
				ft, optional, hasDefault, err := attrFromExpr(item.ValueExpr)
				if err != nil {
					return nil, err
				}
				n.Attrs[key] = Field{Type: ft, Optional: optional, HasDefault: hasDefault}
			}
			return n, nil
		case "tuple":
			return &Node{Kind: "tuple"}, nil
		}
		return nil, fmt.Errorf("unknown type constructor %q", x.Name)
	}
	return nil, fmt.Errorf("unsupported type expression %T", e)
}

func attrFromExpr(e hclsyntax.Expression) (node *Node, optional, hasDefault bool, err error) {
	if fc, ok := e.(*hclsyntax.FunctionCallExpr); ok && fc.Name == "optional" {
		if len(fc.Args) < 1 {
			return nil, false, false, fmt.Errorf("optional() expects at least one argument")
		}
		t, err := typeNodeFromExpr(fc.Args[0])
		if err != nil {
			return nil, false, false, err
		}
		return t, true, len(fc.Args) > 1, nil
	}
	t, err := typeNodeFromExpr(e)
	return t, false, false, err
}

func objectKey(e hclsyntax.Expression) (string, error) {
	switch t := e.(type) {
	case *hclsyntax.ObjectConsKeyExpr:
		return objectKey(t.Wrapped)
	case *hclsyntax.ScopeTraversalExpr:
		if len(t.Traversal) == 1 {
			if root, ok := t.Traversal[0].(hcl.TraverseRoot); ok {
				return root.Name, nil
			}
		}
	case *hclsyntax.LiteralValueExpr:
		if t.Val.Type() == cty.String {
			return t.Val.AsString(), nil
		}
	}
	return "", fmt.Errorf("unsupported object key expression %T", e)
}

func compare(expected, got *Node, path string, out *[]string) {
	if expected.Kind != got.Kind {
		*out = append(*out, fmt.Sprintf("%s: kind mismatch: schema %q, generated %q", path, expected.Kind, got.Kind))
		return
	}
	switch expected.Kind {
	case "object":
		for _, name := range unionKeys(expected.Attrs, got.Attrs) {
			e, eok := expected.Attrs[name]
			g, gok := got.Attrs[name]
			cp := path + "." + name
			switch {
			case !eok:
				*out = append(*out, cp+": extra field in generated variables")
			case !gok:
				*out = append(*out, cp+": missing field required by schema")
			default:
				if e.Optional != g.Optional {
					*out = append(*out, fmt.Sprintf("%s: optional mismatch: schema %v, generated %v", cp, e.Optional, g.Optional))
				}
				if e.HasDefault != g.HasDefault {
					*out = append(*out, fmt.Sprintf("%s: default mismatch: schema %v, generated %v", cp, e.HasDefault, g.HasDefault))
				}
				compare(e.Type, g.Type, cp, out)
			}
		}
	case "list", "set", "map":
		compare(expected.Elem, got.Elem, path+"[]", out)
	}
}

func unionKeys(a, b map[string]Field) []string {
	set := map[string]struct{}{}
	for k := range a {
		set[k] = struct{}{}
	}
	for k := range b {
		set[k] = struct{}{}
	}
	return slices.Sorted(maps.Keys(set))
}
