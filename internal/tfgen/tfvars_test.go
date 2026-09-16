package tfgen

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"

	"github.com/ivaninkv/tofu-resgen/internal/schema"
)

// TestGenerateTFVarsPinsTheExampleShape pins what a schema determines: only
// the required attributes are active, and each carries a placeholder naming
// itself. An optional attribute is left out, because the module type (or its
// declared default) already covers it.
func TestGenerateTFVarsPinsTheExampleShape(t *testing.T) {
	res := loadResource(t, "yandex_vpc_subnet")
	opt := TFVarsOptions{VarName: "vpc_subnet", ResourceName: "yandex_vpc_subnet"}

	first, err := GenerateTFVars(res, opt)
	if err != nil {
		t.Fatalf("GenerateTFVars: %v", err)
	}
	second, err := GenerateTFVars(res, opt)
	if err != nil {
		t.Fatalf("GenerateTFVars: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("generation is not deterministic")
	}
	if _, diags := hclparse.NewParser().ParseHCL(first, "terraform.tfvars.example"); diags.HasErrors() {
		t.Fatalf("generated HCL does not parse: %s", diags)
	}

	src := string(first)
	for _, want := range []string{
		"vpc_subnet = {",
		"example = {",
		"network_id",
		`"<network_id>"`,
		`["<v4_cidr_blocks.item>"]`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated output missing %q:\n%s", want, src)
		}
	}
	for _, unwanted := range []string{"zone", "labels", "dhcp_options"} {
		if strings.Contains(src, unwanted) {
			t.Errorf("optional attribute %q must not be in the example:\n%s", unwanted, src)
		}
	}
}

// TestGenerateTFVarsOptionalComments covers -tfvars-optional: an optional
// attribute is shown commented out, carrying the value it falls back to — the
// module default where one is declared, a placeholder otherwise.
func TestGenerateTFVarsOptionalComments(t *testing.T) {
	res := loadResource(t, "yandex_vpc_subnet")
	out, err := GenerateTFVars(res, TFVarsOptions{
		VarName:      "vpc_subnet",
		ResourceName: "yandex_vpc_subnet",
		Optional:     true,
		Defaults:     NewDefaults(map[string]any{"description": "managed by tofu-resgen"}),
	})
	if err != nil {
		t.Fatalf("GenerateTFVars: %v", err)
	}
	if _, diags := hclparse.NewParser().ParseHCL(out, "terraform.tfvars.example"); diags.HasErrors() {
		t.Fatalf("generated HCL does not parse: %s", diags)
	}

	src := string(out)
	for _, want := range []string{
		`# description = "managed by tofu-resgen"`,
		`# zone = "<zone>"`,
		"# dhcp_options = [{}]",
		`# timeouts = {`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated output missing %q:\n%s", want, src)
		}
	}
	if strings.Contains(src, "\n    zone ") {
		t.Errorf("optional attribute zone must stay commented out:\n%s", src)
	}
}

// TestGenerateTFVarsNestedPlaceholders covers the shapes the fixture does not
// have: a required map of objects, and a collection whose element is an
// object. Both must render one entry, so that the structure is visible.
func TestGenerateTFVarsNestedPlaceholders(t *testing.T) {
	res := schema.Schema{Block: schema.Block{Attributes: map[string]schema.Attribute{
		"topics": {
			Required: true,
			NestedType: &schema.NestedType{
				NestingMode: "map",
				Attributes: map[string]schema.Attribute{
					"partitions": {Required: true, Type: json.RawMessage(`"number"`)},
					"name":       {Required: true, Type: json.RawMessage(`"string"`)},
				},
			},
		},
		"sizes": {
			Required: true,
			NestedType: &schema.NestedType{
				NestingMode: "set",
				Attributes: map[string]schema.Attribute{
					"bytes": {Required: true, Type: json.RawMessage(`"number"`)},
				},
			},
		},
	}}}
	out, err := GenerateTFVars(res, TFVarsOptions{VarName: "cfg", ResourceName: "example_thing"})
	if err != nil {
		t.Fatalf("GenerateTFVars: %v", err)
	}
	src := string(out)
	for _, want := range []string{
		`"<topics.key>" = {`,
		`partitions = 0`,
		`name       = "<topics.key.name>"`,
		`sizes = [{`,
		`bytes = 0`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated output missing %q:\n%s", want, src)
		}
	}
}

func TestGenerateTFVarsAllResourcesParse(t *testing.T) {
	p := providerSchema(t)
	for _, name := range p.ResourceNames() {
		res, err := p.Resource(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, optional := range []bool{false, true} {
			out, err := GenerateTFVars(res, TFVarsOptions{
				VarName: "cfg", ResourceName: name, Optional: optional,
			})
			if err != nil {
				t.Fatalf("%s (optional=%v): %v", name, optional, err)
			}
			if _, diags := hclparse.NewParser().ParseHCL(out, name+".tfvars"); diags.HasErrors() {
				t.Fatalf("%s (optional=%v): generated HCL does not parse: %s", name, optional, diags)
			}
		}
	}
}

func TestGenerateTFVarsRejectsBadInput(t *testing.T) {
	res := loadResource(t, "yandex_vpc_network")
	if _, err := GenerateTFVars(res, TFVarsOptions{}); err == nil {
		t.Fatal("expected error without a variable name")
	}
	if _, err := GenerateTFVars(res, TFVarsOptions{VarName: "1bad"}); err == nil {
		t.Fatal("expected error for a variable name that is not an identifier")
	}
}
