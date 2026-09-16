package verify

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ivaninkv/tofu-resgen/internal/schema"
	"github.com/ivaninkv/tofu-resgen/internal/tfgen"
)

// fixture is a frozen snapshot of the public Yandex Cloud provider schema
// (registry.opentofu.org/yandex-cloud/yandex v0.228.0).
const fixture = "../../testdata/yandex.json"

func providerSchema(t *testing.T) schema.ProviderSchema {
	t.Helper()
	ps, err := schema.Load(fixture)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, p, err := ps.Provider("")
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	return p
}

func resource(t *testing.T, name string) schema.Schema {
	t.Helper()
	res, err := providerSchema(t).Resource(name)
	if err != nil {
		t.Fatalf("Resource %s: %v", name, err)
	}
	return res
}

func generate(t *testing.T, res schema.Schema, name, varName string, defaults *tfgen.Defaults) []byte {
	t.Helper()
	out, err := tfgen.Generate(res, tfgen.Options{
		VarName:      varName,
		ResourceName: name,
		Doc:          true,
		Defaults:     defaults,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return out
}

// TestEveryResourceConforms cross-checks the generated variables of all 253
// provider resources against their schema. It covers legacy block types,
// nested_type attributes, all four nesting modes and blocks nested up to
// eight levels (yandex_sws_security_profile).
func TestEveryResourceConforms(t *testing.T) {
	p := providerSchema(t)
	for _, name := range p.ResourceNames() {
		res, err := p.Resource(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out, err := tfgen.Generate(res, tfgen.Options{VarName: "cfg", ResourceName: name, Doc: true})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		problems, err := Conforms(out, res, "cfg", nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, problem := range problems {
			t.Errorf("%s: %s", name, problem)
		}
	}
}

func TestConforms(t *testing.T) {
	res := resource(t, "yandex_compute_instance")
	problems, err := Conforms(generate(t, res, "yandex_compute_instance", "compute", nil), res, "compute", nil)
	if err != nil {
		t.Fatalf("Conforms: %v", err)
	}
	if len(problems) > 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
}

func TestConformsTracksDefaults(t *testing.T) {
	res := resource(t, "yandex_vpc_network")
	defaults := tfgen.NewDefaults(map[string]any{"description": "managed by tofu-resgen"})
	out := generate(t, res, "yandex_vpc_network", "vpc_network", defaults)

	problems, err := Conforms(out, res, "vpc_network", defaults)
	if err != nil {
		t.Fatalf("Conforms: %v", err)
	}
	if len(problems) > 0 {
		t.Fatalf("unexpected problems with defaults: %v", problems)
	}
	// Without the defaults the emitted optional(string, "…") must be flagged.
	problems, err = Conforms(out, res, "vpc_network", nil)
	if err != nil {
		t.Fatalf("Conforms: %v", err)
	}
	if !hasProblem(problems, "description", "default mismatch") {
		t.Fatalf("expected default mismatch, got %v", problems)
	}
}

func TestDetectsMissingField(t *testing.T) {
	res := resource(t, "yandex_compute_instance")
	out := generate(t, res, "yandex_compute_instance", "compute", nil)
	delete(res.Block.Attributes, "zone")

	problems, err := Conforms(out, res, "compute", nil)
	if err != nil {
		t.Fatalf("Conforms: %v", err)
	}
	if !hasProblem(problems, "zone", "extra field") {
		t.Fatalf("expected extra field for zone, got %v", problems)
	}
}

func TestDetectsMissingBlock(t *testing.T) {
	res := resource(t, "yandex_compute_instance")
	out := generate(t, res, "yandex_compute_instance", "compute", nil)
	delete(res.Block.BlockTypes, "filesystem")

	problems, err := Conforms(out, res, "compute", nil)
	if err != nil {
		t.Fatalf("Conforms: %v", err)
	}
	if !hasProblem(problems, "filesystem", "extra field") {
		t.Fatalf("expected extra field for filesystem, got %v", problems)
	}
}

func TestDetectsKindMismatch(t *testing.T) {
	res := resource(t, "yandex_vpc_network")
	out := generate(t, res, "yandex_vpc_network", "vpc_network", nil)
	res.Block.Attributes["description"] = schema.Attribute{Type: json.RawMessage(`"number"`), Optional: true}

	problems, err := Conforms(out, res, "vpc_network", nil)
	if err != nil {
		t.Fatalf("Conforms: %v", err)
	}
	if !hasProblem(problems, "description", "kind mismatch") {
		t.Fatalf("expected kind mismatch, got %v", problems)
	}
}

func TestDetectsOptionalityMismatch(t *testing.T) {
	res := resource(t, "yandex_vpc_network")
	out := generate(t, res, "yandex_vpc_network", "vpc_network", nil)
	res.Block.Attributes["description"] = schema.Attribute{Type: json.RawMessage(`"string"`), Required: true}

	problems, err := Conforms(out, res, "vpc_network", nil)
	if err != nil {
		t.Fatalf("Conforms: %v", err)
	}
	if !hasProblem(problems, "description", "optional mismatch") {
		t.Fatalf("expected optional mismatch, got %v", problems)
	}
}

func TestMissingVariableIsAnError(t *testing.T) {
	res := resource(t, "yandex_vpc_network")
	out := generate(t, res, "yandex_vpc_network", "vpc_network", nil)
	if _, err := Conforms(out, res, "other", nil); err == nil {
		t.Fatal("expected error when the variable is absent")
	}
}

func TestRejectsBrokenHCL(t *testing.T) {
	res := resource(t, "yandex_vpc_network")
	if _, err := Conforms([]byte("variable \"vpc_network\" {"), res, "vpc_network", nil); err == nil {
		t.Fatal("expected parse error")
	}
}

func hasProblem(problems []string, substr, kind string) bool {
	for _, p := range problems {
		if strings.Contains(p, substr) && strings.Contains(p, kind) {
			return true
		}
	}
	return false
}
