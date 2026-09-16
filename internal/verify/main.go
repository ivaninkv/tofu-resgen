package verify

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/ivaninkv/tofu-resgen/internal/schema"
)

// MainConforms checks a main.tf against the provider schema and the variable
// type declared in the companion variables.tf.
//
// It reports drift and mistakes, not hand edits: wiring an attribute from a
// data source instead of each.value passes, because a provider schema says
// nothing about where a value must come from. A deviation is
//
//   - a resource block whose type or local name differs from the schema
//     resource and the module variable;
//   - a for_each that does not reference the module variable;
//   - an assigned attribute the schema does not declare, or one that is
//     computed-only and therefore not settable;
//   - a dynamic block the schema does not know, or one that is an argument
//     rather than a block;
//   - a required attribute or block that nothing wires;
//   - an each.value or block-iterator path the type does not declare.
func MainConforms(mainTF, variablesTF []byte, res schema.Schema, providerAddr, resource, varName string) ([]string, error) {
	file, diags := hclparse.NewParser().ParseHCL(mainTF, "main.tf")
	if diags.HasErrors() {
		return nil, fmt.Errorf("parse generated HCL: %s", diags.Error())
	}
	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		return nil, fmt.Errorf("generated HCL has unexpected body type %T", file.Body)
	}
	elem, err := variableElem(variablesTF, varName)
	if err != nil {
		return nil, err
	}

	var problems []string
	if providerAddr != "" {
		problems = append(problems, providerSourceProblems(body, providerAddr)...)
	}
	blk := findResource(body, resource, varName)
	if blk == nil {
		return append(problems, fmt.Sprintf("resource %q %q: not found in main.tf", resource, varName)), nil
	}
	if fe, ok := blk.Body.Attributes["for_each"]; !ok {
		problems = append(problems, fmt.Sprintf("resource %q: no for_each", resource))
	} else if !varRef(fe.Expr, varName) {
		problems = append(problems, fmt.Sprintf("resource %q: for_each must reference var.%s", resource, varName))
	}

	sc := mainScope{elem: elem, iters: map[string]*Node{}}
	checkBody(blk.Body, res.ConfigAttributes(), sc, fmt.Sprintf("resource %q", resource), true, &problems)
	return problems, nil
}

// mainScope resolves the references the generator writes: each.value.<path> at
// resource level, and <iterator>.value.<path> inside a dynamic block.
type mainScope struct {
	elem  *Node
	iters map[string]*Node
}

// resolve splits a traversal rooted at each.value or at a dynamic block
// iterator into the object it starts from and the path selected on it. ok is
// false for traversals this checker does not track (data sources, variables,
// locals, an iterator's key, ...).
func (s mainScope) resolve(t hcl.Traversal) (node *Node, path []string, ok bool) {
	if len(t) < 2 {
		return nil, nil, false
	}
	root, ok := t[0].(hcl.TraverseRoot)
	if !ok {
		return nil, nil, false
	}
	attr, ok := t[1].(hcl.TraverseAttr)
	if !ok || attr.Name != "value" {
		return nil, nil, false
	}
	switch {
	case root.Name == "each":
		node = s.elem
	default:
		n, found := s.iters[root.Name]
		if !found {
			return nil, nil, false
		}
		node = n
	}
	path = make([]string, 0, len(t)-2)
	for _, step := range t[2:] {
		switch st := step.(type) {
		case hcl.TraverseAttr:
			path = append(path, st.Name)
		case hcl.TraverseIndex:
			path = append(path, "")
		default:
			return nil, nil, false
		}
	}
	return node, path, true
}

// checkBody verifies one object level: the resource body, or the content block
// of a dynamic block. top marks the resource level, where the protocol
// maintains the identifier attribute.
func checkBody(body *hclsyntax.Body, attrs map[string]schema.Attribute, sc mainScope, where string, top bool, out *[]string) {
	assigned := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(body.Attributes)) {
		if name == "for_each" {
			// Handled by the caller: at resource level it selects the module
			// variable, inside a dynamic block it is checked with the block.
			continue
		}
		expr := body.Attributes[name].Expr
		a, ok := attrs[name]
		switch {
		case top && schema.ProtocolAttribute(name):
			*out = append(*out, fmt.Sprintf(
				"%s: attribute %q is maintained by the provider protocol and cannot be set", where, name))
			continue
		case !ok:
			*out = append(*out, fmt.Sprintf(
				"%s: attribute %q is not in the schema (renamed or removed upstream?)", where, name))
			continue
		case a.ComputedOnly():
			*out = append(*out, fmt.Sprintf(
				"%s: attribute %q is computed-only and cannot be set", where, name))
			continue
		}
		assigned[name] = true
		checkRefs(expr, sc, fmt.Sprintf("%s: attribute %q", where, name), out)
	}
	for _, blk := range body.Blocks {
		// Non-dynamic nested blocks are meta-blocks (lifecycle, provider,
		// connection, ...) and are not part of the resource schema.
		if blk.Type != "dynamic" || len(blk.Labels) != 1 {
			continue
		}
		name := blk.Labels[0]
		a, ok := attrs[name]
		switch {
		case !ok:
			*out = append(*out, fmt.Sprintf("%s: dynamic block %q is not in the schema", where, name))
			continue
		case !a.Block:
			*out = append(*out, fmt.Sprintf(
				"%s: %q is an argument in the schema, not a block", where, name))
			continue
		}
		assigned[name] = true
		checkDynamic(blk, name, a, sc, where, out)
	}
	for _, name := range slices.Sorted(maps.Keys(attrs)) {
		a := attrs[name]
		if !a.Required || assigned[name] {
			continue
		}
		kind := "attribute"
		if a.Block {
			kind = "block"
		}
		*out = append(*out, fmt.Sprintf("%s: required %s %q is not wired", where, kind, name))
	}
}

func checkDynamic(blk *hclsyntax.Block, name string, a schema.Attribute, sc mainScope, where string, out *[]string) {
	where = fmt.Sprintf("%s: dynamic block %q", where, name)
	fe, ok := blk.Body.Attributes["for_each"]
	if !ok {
		*out = append(*out, where+": no for_each")
		return
	}
	refs := checkRefs(fe.Expr, sc, where+" for_each", out)
	if len(refs) == 0 {
		*out = append(*out, where+": for_each must reference the module variable")
	} else if refs[0] != name {
		*out = append(*out, fmt.Sprintf("%s: for_each iterates %s, want %s", where, refs[0], name))
	}

	value, err := blockValueNode(a)
	if err != nil {
		*out = append(*out, fmt.Sprintf("%s: %v", where, err))
		return
	}
	inner := mainScope{elem: sc.elem, iters: maps.Clone(sc.iters)}
	inner.iters[name] = value

	contents := 0
	for _, c := range blk.Body.Blocks {
		if c.Type == "content" && len(c.Labels) == 0 {
			contents++
			checkBody(c.Body, a.NestedType.Attributes, inner, where, false, out)
		}
	}
	if contents != 1 {
		*out = append(*out, fmt.Sprintf("%s: want exactly one content block, got %d", where, contents))
	}
}

// blockValueNode is the type a dynamic block's iterator holds in .value: the
// element type for a collection block, the object itself for a single block.
func blockValueNode(a schema.Attribute) (*Node, error) {
	if a.NestedType == nil {
		return nil, fmt.Errorf("block has no nested type")
	}
	n, err := expectedType(a, nil, nil)
	if err != nil {
		return nil, err
	}
	if a.NestedType.NestingMode == "single" {
		return n, nil
	}
	if n.Elem == nil {
		return nil, fmt.Errorf("block %s has no element type", n.Kind)
	}
	return n.Elem, nil
}

// checkRefs resolves every tracked reference in e. It returns the top-level
// attribute names they select, in order, so a caller can tell which variable
// field a block iterates.
func checkRefs(e hclsyntax.Expression, sc mainScope, where string, out *[]string) []string {
	var names []string
	for _, t := range e.Variables() {
		node, path, ok := sc.resolve(t)
		if !ok {
			continue
		}
		text := traversalText(t)
		if _, err := lookup(node, path); err != nil {
			*out = append(*out, fmt.Sprintf("%s: %s does not resolve: %v", where, text, err))
			continue
		}
		if len(path) > 0 {
			names = append(names, path[0])
		}
	}
	return names
}

// traversalText renders a traversal the way it is written in HCL.
func traversalText(t hcl.Traversal) string {
	var b strings.Builder
	for i, step := range t {
		switch st := step.(type) {
		case hcl.TraverseRoot:
			b.WriteString(st.Name)
		case hcl.TraverseAttr:
			b.WriteString("." + st.Name)
		case hcl.TraverseIndex:
			fmt.Fprintf(&b, "[%v]", st.Key)
		default:
			_ = i
			return "<?>"
		}
	}
	return b.String()
}

// variableElem returns the element type of the module variable declared in a
// variables.tf, i.e. what each.value is.
func variableElem(variablesTF []byte, varName string) (*Node, error) {
	node, err := actual(variablesTF, varName)
	if err != nil {
		return nil, err
	}
	if node.Kind != "map" || node.Elem == nil {
		return nil, fmt.Errorf("variable %q: type must be map(object(...)), got %s", varName, node.Kind)
	}
	return node.Elem, nil
}

func findResource(body *hclsyntax.Body, resource, varName string) *hclsyntax.Block {
	for _, blk := range body.Blocks {
		if blk.Type != "resource" || len(blk.Labels) != 2 {
			continue
		}
		if blk.Labels[0] == resource && blk.Labels[1] == varName {
			return blk
		}
	}
	return nil
}

func providerSourceProblems(body *hclsyntax.Body, addr string) []string {
	local := schema.LocalName(addr)
	for _, blk := range body.Blocks {
		if blk.Type != "terraform" {
			continue
		}
		for _, rp := range blk.Body.Blocks {
			if rp.Type != "required_providers" {
				continue
			}
			if probe, ok := rp.Body.Attributes[local]; ok {
				if src, ok := objectString(probe.Expr, "source"); ok && src == addr {
					return nil
				}
			}
		}
	}
	return []string{fmt.Sprintf(
		"terraform.required_providers: provider %s is not declared with source %q", local, addr)}
}

// objectString reads a literal string field out of an object constructor.
func objectString(e hclsyntax.Expression, field string) (string, bool) {
	obj, ok := e.(*hclsyntax.ObjectConsExpr)
	if !ok {
		return "", false
	}
	for _, item := range obj.Items {
		key, err := objectKey(item.KeyExpr)
		if err != nil || key != field {
			continue
		}
		return stringLiteral(item.ValueExpr)
	}
	return "", false
}

// stringLiteral evaluates a literal string expression. Native HCL parses a
// quoted string as a template, not as a plain literal value.
func stringLiteral(e hclsyntax.Expression) (string, bool) {
	switch t := e.(type) {
	case *hclsyntax.LiteralValueExpr:
		if t.Val.Type() == cty.String {
			return t.Val.AsString(), true
		}
	case *hclsyntax.TemplateExpr:
		v, diags := t.Value(nil)
		if !diags.HasErrors() && v.Type() == cty.String {
			return v.AsString(), true
		}
	}
	return "", false
}

// varRef reports whether e is the traversal var.<name>.
func varRef(e hclsyntax.Expression, name string) bool {
	t, ok := e.(*hclsyntax.ScopeTraversalExpr)
	if !ok || len(t.Traversal) != 2 {
		return false
	}
	root, ok := t.Traversal[0].(hcl.TraverseRoot)
	if !ok || root.Name != "var" {
		return false
	}
	attr, ok := t.Traversal[1].(hcl.TraverseAttr)
	return ok && attr.Name == name
}

// lookup walks a type tree: an object consumes an attribute name, a collection
// consumes an index or key.
func lookup(n *Node, path []string) (*Node, error) {
	for i, seg := range path {
		switch n.Kind {
		case "object":
			f, ok := n.Attrs[seg]
			if !ok {
				return nil, fmt.Errorf("no attribute %q in object(%s)", seg, slices.Sorted(maps.Keys(n.Attrs)))
			}
			n = f.Type
		case "list", "set", "map":
			if n.Elem == nil {
				return nil, fmt.Errorf("collection %q has no element type", seg)
			}
			n = n.Elem
		default:
			return nil, fmt.Errorf("cannot select %q from %s at %s", seg, n.Kind, joinPath(path[:i]...))
		}
	}
	return n, nil
}

// joinPath renders an attribute path the way the schema report does.
func joinPath(parts ...string) string {
	return strings.Join(parts, ".")
}
