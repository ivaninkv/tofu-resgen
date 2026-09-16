package verify

import (
	"fmt"
	"maps"
	"slices"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/ivaninkv/tofu-resgen/internal/schema"
)

// TFVarsConforms checks an example terraform.tfvars against the provider schema
// and the module variable it instantiates.
//
// It reports what the schema can judge about a file meant to be edited by hand:
// an attribute the schema does not declare, a required attribute left out, a
// value whose type or shape contradicts the schema, a variable the module does
// not declare. It deliberately does not judge the names or the number of map
// entries, concrete values, or anything that is not a literal expression: a
// reference to a variable, a data source or a local passes, because OpenTofu
// resolves it, not the schema.
func TFVarsConforms(src []byte, res schema.Schema, varName string) ([]string, error) {
	expected, err := Expected(res, nil)
	if err != nil {
		return nil, err
	}
	file, diags := hclparse.NewParser().ParseHCL(src, "terraform.tfvars")
	if diags.HasErrors() {
		return nil, fmt.Errorf("parse tfvars: %s", diags.Error())
	}
	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		return nil, fmt.Errorf("tfvars has unexpected body type %T", file.Body)
	}

	var problems []string
	if len(body.Blocks) > 0 {
		problems = append(problems, "example tfvars must assign variables, found a block")
	}
	for _, name := range slices.Sorted(maps.Keys(body.Attributes)) {
		if name != varName {
			problems = append(problems, fmt.Sprintf(
				"attribute %q: the module declares the variable %q only", name, varName))
			continue
		}
		checkExample(body.Attributes[name].Expr, expected, varName, &problems)
	}
	if _, ok := body.Attributes[varName]; !ok {
		problems = append(problems, fmt.Sprintf("variable %q is not set", varName))
	}
	return problems, nil
}

// checkExample compares one expression against the type the schema declares.
func checkExample(e hclsyntax.Expression, node *Node, where string, out *[]string) {
	switch node.Kind {
	case "object":
		checkExampleObject(e, node, where, out)
	case "list", "set":
		tup, ok := e.(*hclsyntax.TupleConsExpr)
		if !ok {
			if isValueExpr(e) {
				*out = append(*out, fmt.Sprintf("%s: want a [ ... ] literal, got %s", where, node.Kind))
			}
			return
		}
		if node.Elem == nil {
			return
		}
		for i, item := range tup.Exprs {
			checkExample(item, node.Elem, fmt.Sprintf("%s[%d]", where, i), out)
		}
	case "map":
		obj, ok := e.(*hclsyntax.ObjectConsExpr)
		if !ok {
			if isValueExpr(e) {
				*out = append(*out, fmt.Sprintf("%s: want a { ... } literal, got %s", where, node.Kind))
			}
			return
		}
		if node.Elem == nil {
			return
		}
		for _, item := range obj.Items {
			key, err := objectKey(item.KeyExpr)
			if err != nil {
				// A computed key: nothing to compare against.
				continue
			}
			checkExample(item.ValueExpr, node.Elem, where+"."+key, out)
		}
	case "string", "number", "bool":
		// OpenTofu converts a string literal to a number or a bool and back,
		// so only a contradiction that no conversion bridges (a bool where a
		// number is declared, and the reverse) is a deviation.
		got := literalKind(e)
		if got != "" && got != node.Kind && got != "string" && node.Kind != "string" {
			*out = append(*out, fmt.Sprintf("%s: want %s, got %s literal", where, node.Kind, got))
		}
	}
}

// checkExampleObject checks one object literal: no attribute outside the
// schema, every required attribute set.
func checkExampleObject(e hclsyntax.Expression, node *Node, where string, out *[]string) {
	obj, ok := e.(*hclsyntax.ObjectConsExpr)
	if !ok {
		if isValueExpr(e) {
			*out = append(*out, fmt.Sprintf("%s: must be an object, got a literal of another shape", where))
		}
		return
	}
	set := make(map[string]bool, len(obj.Items))
	for _, item := range obj.Items {
		key, err := objectKey(item.KeyExpr)
		if err != nil {
			continue
		}
		f, ok := node.Attrs[key]
		if !ok {
			*out = append(*out, fmt.Sprintf(
				"%s: attribute %q is not in the schema (renamed or removed upstream?)", where, key))
			continue
		}
		set[key] = true
		checkExample(item.ValueExpr, f.Type, where+"."+key, out)
	}
	for _, name := range slices.Sorted(maps.Keys(node.Attrs)) {
		if f := node.Attrs[name]; !f.Optional && !set[name] {
			*out = append(*out, fmt.Sprintf("%s: required attribute %q is not set", where, name))
		}
	}
}

// isValueExpr reports whether e denotes a value by itself, so that its shape
// can be judged. A reference, a function call or an interpolated string is
// resolved by OpenTofu instead, and null means "not set".
func isValueExpr(e hclsyntax.Expression) bool {
	switch t := e.(type) {
	case *hclsyntax.LiteralValueExpr:
		return !t.Val.IsNull()
	case *hclsyntax.TemplateExpr:
		_, ok := stringLiteral(t)
		return ok
	case *hclsyntax.TupleConsExpr, *hclsyntax.ObjectConsExpr:
		return true
	}
	return false
}

// literalKind names the type of a literal expression: "string", "number",
// "bool", or "" when e is not a literal at all.
func literalKind(e hclsyntax.Expression) string {
	switch t := e.(type) {
	case *hclsyntax.LiteralValueExpr:
		switch {
		case t.Val.IsNull():
			return ""
		case t.Val.Type() == cty.String:
			return "string"
		case t.Val.Type() == cty.Number:
			return "number"
		case t.Val.Type() == cty.Bool:
			return "bool"
		}
	case *hclsyntax.TemplateExpr:
		if v, diags := t.Value(nil); !diags.HasErrors() && v.Type() == cty.String {
			return "string"
		}
	}
	return ""
}
