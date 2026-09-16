package tfgen

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/ivaninkv/tofu-resgen/internal/schema"
)

// MainOptions control generation of a single main.tf.
type MainOptions struct {
	// VarName is the module variable name, e.g. "compute". It is also the local
	// name of the generated resource block.
	VarName string
	// ResourceName is the provider resource type, e.g. "yandex_compute_instance".
	ResourceName string
	// ProviderAddr is the provider address, e.g. "yandex-cloud/yandex". Its
	// trailing segment becomes the local provider name.
	ProviderAddr string
}

// GenerateMain renders main.tf for one resource.
//
// Only what the schema states unambiguously is emitted: the provider
// requirement and a resource block that wires every attribute the user may set
// from the module variable. The variable type declares exactly the resource
// attributes, so the wiring is type-correct by construction.
//
// Legacy block types are rendered as dynamic blocks, because a resource takes
// them as blocks (`name { ... }`), not as arguments, and the block must be
// skipped when the variable is unset.
//
// Data sources are deliberately not emitted. A provider schema carries no
// relation between a resource and the data sources that feed it, and no
// mapping from a data source output to a resource attribute, so any wiring
// would be invented. The author adds it by hand.
func GenerateMain(res schema.Schema, opt MainOptions) ([]byte, error) {
	if opt.VarName == "" {
		return nil, errors.New("variable name is required")
	}
	if !isIdent(opt.VarName) {
		return nil, fmt.Errorf("variable name %q is not a valid HCL identifier", opt.VarName)
	}
	if opt.ResourceName == "" {
		return nil, errors.New("resource name is required")
	}
	local := schema.LocalName(opt.ProviderAddr)
	if local == "" {
		return nil, errors.New("provider address is required")
	}

	var body strings.Builder
	if err := writeAttributes(&body, 2, "each.value", nil, res.ConfigAttributes()); err != nil {
		return nil, err
	}

	var b strings.Builder
	b.WriteString(header)
	fmt.Fprintf(&b, "terraform {\n  required_providers {\n    %s = {\n      source = %s\n    }\n  }\n}\n\n",
		local, strconv.Quote(opt.ProviderAddr))
	fmt.Fprintf(&b, "resource %s %s {\n", strconv.Quote(opt.ResourceName), strconv.Quote(opt.VarName))
	fmt.Fprintf(&b, "  for_each = var.%s\n", opt.VarName)
	if body.Len() > 0 {
		// Keep for_each visually separated from the wiring.
		b.WriteString("\n")
		b.WriteString(body.String())
	}
	b.WriteString("}\n")
	return hclwrite.Format([]byte(b.String())), nil
}

// writeAttributes renders assignments and dynamic blocks for one object level.
// ref is the HCL expression the enclosing object is reachable through, e.g.
// "each.value" at resource level or "listener.value" inside a dynamic block.
// path is the chain of enclosing block names, used for distinct iterator
// context in error messages.
func writeAttributes(b *strings.Builder, indent int, ref string, path []string, attrs map[string]schema.Attribute) error {
	for _, name := range slices.Sorted(maps.Keys(attrs)) {
		a := attrs[name]
		if a.ComputedOnly() {
			continue
		}
		if !isIdent(name) {
			return fmt.Errorf("attribute %q is not a valid HCL identifier", name)
		}
		if !a.Block {
			fmt.Fprintf(b, "%s%s = %s.%s\n", pad(indent), name, ref, name)
			continue
		}
		if a.NestedType == nil {
			return fmt.Errorf("block %q has no nested type", name)
		}
		if err := writeBlock(b, indent, ref+"."+name, append(path, name), a); err != nil {
			return err
		}
	}
	return nil
}

// writeBlock renders a legacy block type as a dynamic block whose single
// iterator is named after the block, which is also the name HCL gives it by
// default.
func writeBlock(b *strings.Builder, indent int, src string, path []string, a schema.Attribute) error {
	name := path[len(path)-1]
	ind := pad(indent)
	fmt.Fprintf(b, "%sdynamic %q {\n", ind, name)
	fmt.Fprintf(b, "%s  for_each = %s\n", ind, forEachExpr(src, a))
	fmt.Fprintf(b, "%s  content {\n", ind)
	if err := writeAttributes(b, indent+4, name+".value", path, a.NestedType.Attributes); err != nil {
		return err
	}
	fmt.Fprintf(b, "%s  }\n%s}\n", ind, ind)
	return nil
}

// forEachExpr renders a dynamic block's for_each. A single block iterates over
// a one-element list so that it is rendered only when the variable is set; a
// collection block iterates over the variable itself, or over an empty
// collection when the variable is null.
func forEachExpr(src string, a schema.Attribute) string {
	mode := a.NestedType.NestingMode
	switch {
	case mode == "single":
		return fmt.Sprintf("%s == null ? [] : [%s]", src, src)
	case !a.Optional:
		// Required block: the variable type has no optional() wrapper.
		return src
	case mode == "map":
		return fmt.Sprintf("%s == null ? {} : %s", src, src)
	default:
		return fmt.Sprintf("%s == null ? [] : %s", src, src)
	}
}

func pad(indent int) string {
	return strings.Repeat(" ", indent)
}

// isIdent reports whether s can be written as a bare HCL identifier.
func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
