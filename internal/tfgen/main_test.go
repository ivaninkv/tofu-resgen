package tfgen

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
)

// providerAddr is the address of the provider in the yandex fixture.
const providerAddr = "registry.opentofu.org/yandex-cloud/yandex"

func TestGenerateMainIsParseableAndDeterministic(t *testing.T) {
	res := loadResource(t, "yandex_compute_instance")
	opt := MainOptions{VarName: "compute", ResourceName: "yandex_compute_instance", ProviderAddr: providerAddr}

	first, err := GenerateMain(res, opt)
	if err != nil {
		t.Fatalf("GenerateMain: %v", err)
	}
	second, err := GenerateMain(res, opt)
	if err != nil {
		t.Fatalf("GenerateMain: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("generation is not deterministic")
	}
	if _, diags := hclparse.NewParser().ParseHCL(first, "main.tf"); diags.HasErrors() {
		t.Fatalf("generated HCL does not parse: %s", diags)
	}

	src := string(first)
	for _, want := range []string{
		`source = "registry.opentofu.org/yandex-cloud/yandex"`,
		`resource "yandex_compute_instance" "compute" {`,
		"for_each = var.compute",
		"each.value.zone",
		"each.value.boot_disk",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated output missing %q", want)
		}
	}
	if strings.Contains(src, "created_at") {
		t.Error("computed-only attribute created_at must not be wired")
	}
}

// TestGenerateMainRendersBlocks pins the block handling: a legacy block type
// becomes a dynamic block with a null guard, because a resource (and OpenTofu
// validation) rejects it as an argument.
func TestGenerateMainRendersBlocks(t *testing.T) {
	res := loadResource(t, "yandex_vpc_network")
	out, err := GenerateMain(res, MainOptions{
		VarName: "vpc_network", ResourceName: "yandex_vpc_network", ProviderAddr: providerAddr,
	})
	if err != nil {
		t.Fatalf("GenerateMain: %v", err)
	}
	src := string(out)
	for _, want := range []string{
		`dynamic "timeouts" {`,
		"for_each = each.value.timeouts == null ? [] : [each.value.timeouts]",
		"create = timeouts.value.create",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated output missing %q:\n%s", want, src)
		}
	}
	if strings.Contains(src, "= each.value.timeouts\n") {
		t.Error("a block type must not be assigned as an argument")
	}
	if strings.Contains(src, "each.value.id") {
		t.Error("the protocol identifier must not be wired")
	}
}

// TestGenerateOmitsProtocolID checks that variables and main agree on the
// identifier OpenTofu rejects as a resource argument.
func TestGenerateOmitsProtocolID(t *testing.T) {
	res := loadResource(t, "yandex_vpc_network")
	vars, err := Generate(res, Options{VarName: "vpc_network", ResourceName: "yandex_vpc_network", Doc: false})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.Contains(string(vars), "id = optional(string)") {
		t.Errorf("variables.tf must not declare the protocol identifier:\n%s", vars)
	}
}

func TestGenerateMainAllResourcesParse(t *testing.T) {
	p := providerSchema(t)
	for _, name := range p.ResourceNames() {
		res, err := p.Resource(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out, err := GenerateMain(res, MainOptions{VarName: "cfg", ResourceName: name, ProviderAddr: providerAddr})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, diags := hclparse.NewParser().ParseHCL(out, name+".tf"); diags.HasErrors() {
			t.Fatalf("%s: generated HCL does not parse: %s", name, diags)
		}
	}
}

func TestGenerateMainRejectsBadInput(t *testing.T) {
	res := loadResource(t, "yandex_vpc_network")
	cases := map[string]MainOptions{
		"no variable name": {ResourceName: "yandex_vpc_network", ProviderAddr: providerAddr},
		"no resource name": {VarName: "vpc", ProviderAddr: providerAddr},
		"no provider":      {VarName: "vpc", ResourceName: "yandex_vpc_network"},
		"invalid variable": {VarName: "my-vpc", ResourceName: "yandex_vpc_network", ProviderAddr: providerAddr},
		"numeric variable": {VarName: "1vpc", ResourceName: "yandex_vpc_network", ProviderAddr: providerAddr},
	}
	for name, opt := range cases {
		if _, err := GenerateMain(res, opt); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
