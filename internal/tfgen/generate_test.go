package tfgen

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
)

func TestGenerateIsParseableAndDeterministic(t *testing.T) {
	res := loadResource(t, "yandex_compute_instance")
	opt := Options{VarName: "compute", ResourceName: "yandex_compute_instance", Doc: false}

	first, err := Generate(res, opt)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	second, err := Generate(res, opt)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("generation is not deterministic")
	}
	if _, diags := hclparse.NewParser().ParseHCL(first, "variables.tf"); diags.HasErrors() {
		t.Fatalf("generated HCL does not parse: %s", diags)
	}

	src := string(first)
	for _, want := range []string{
		`variable "compute"`,
		"type = map(object({",
		"default = {}",
		"boot_disk = list(object({",          // required list block (min_items=1)
		"filesystem = optional(set(object({", // optional set block
		"timeouts = optional(object({",       // optional single block
		"zone = optional(string)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated output missing %q", want)
		}
	}
	if strings.Contains(src, "created_at") {
		t.Error("computed-only attribute created_at must not appear")
	}
}

func TestGenerateWithDefaults(t *testing.T) {
	res := loadResource(t, "yandex_vpc_network")
	out, err := Generate(res, Options{
		VarName:      "vpc_network",
		ResourceName: "yandex_vpc_network",
		Defaults:     NewDefaults(map[string]any{"description": "managed by tofu-resgen"}),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(string(out), `description = optional(string, "managed by tofu-resgen")`) {
		t.Errorf("default was not applied:\n%s", out)
	}
}

func TestGenerateAllResourcesParse(t *testing.T) {
	p := providerSchema(t)
	for _, name := range p.ResourceNames() {
		res, err := p.Resource(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out, err := Generate(res, Options{VarName: "cfg", ResourceName: name, Doc: true})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, diags := hclparse.NewParser().ParseHCL(out, name+".tf"); diags.HasErrors() {
			t.Fatalf("%s: generated HCL does not parse: %s", name, diags)
		}
	}
}

func TestGenerateRequiresVarName(t *testing.T) {
	if _, err := Generate(loadResource(t, "yandex_vpc_network"), Options{}); err == nil {
		t.Fatal("expected error without a variable name")
	}
}
