package verify

import (
	"bytes"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ivaninkv/tofu-resgen/internal/schema"
	"github.com/ivaninkv/tofu-resgen/internal/tfgen"
)

// fixtureAddr is the provider address of the yandex fixture.
const fixtureAddr = "registry.opentofu.org/yandex-cloud/yandex"

// module generates the variables.tf / main.tf pair for one resource.
func module(t *testing.T, res schema.Schema, resource, varName string) (vars, mainTF []byte) {
	t.Helper()
	vars, err := tfgen.Generate(res, tfgen.Options{VarName: varName, ResourceName: resource, Doc: false})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	mainTF, err = tfgen.GenerateMain(res, tfgen.MainOptions{
		VarName: varName, ResourceName: resource, ProviderAddr: fixtureAddr,
	})
	if err != nil {
		t.Fatalf("GenerateMain: %v", err)
	}
	return vars, mainTF
}

func mutate(t *testing.T, src []byte, old, new string) []byte {
	t.Helper()
	if !strings.Contains(string(src), old) {
		t.Fatalf("%q not found in generated main.tf", old)
	}
	return []byte(strings.Replace(string(src), old, new, 1))
}

// dropAttr removes an attribute assignment line from a generated main.tf.
func dropAttr(t *testing.T, src []byte, name string) []byte {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(name) + `\s*=.*\n`)
	out := re.ReplaceAll(src, nil)
	if bytes.Equal(out, src) {
		t.Fatalf("attribute %q not found in generated main.tf", name)
	}
	return out
}

// unlinkRequired removes a required field: an assignment line for an argument,
// or the dynamic block for a block type (renaming its label so that the
// schema no longer sees it).
func unlinkRequired(t *testing.T, src []byte, name string, a schema.Attribute) []byte {
	t.Helper()
	if !a.Block {
		return dropAttr(t, src, name)
	}
	return mutate(t, src, fmt.Sprintf("dynamic %q {", name), fmt.Sprintf("dynamic %q {", name+"_gone"))
}

// pick returns the first attribute matching pred, in lexical order.
func pick(t *testing.T, res schema.Schema, pred func(schema.Attribute) bool) (string, schema.Attribute) {
	t.Helper()
	attrs := res.ConfigAttributes()
	for _, name := range slices.Sorted(maps.Keys(attrs)) {
		if pred(attrs[name]) {
			return name, attrs[name]
		}
	}
	t.Fatal("no attribute matches the predicate")
	return "", schema.Attribute{}
}

// TestEveryGeneratedModuleConforms cross-checks the generated variables.tf and
// main.tf pair of all 253 provider resources. It covers optional+computed
// attributes, legacy block types and all four nesting modes.
func TestEveryGeneratedModuleConforms(t *testing.T) {
	p := providerSchema(t)
	for _, name := range p.ResourceNames() {
		res, err := p.Resource(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		vars, mainTF := module(t, res, name, "cfg")
		problems, err := MainConforms(mainTF, vars, res, fixtureAddr, name, "cfg")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(problems) > 0 {
			t.Errorf("%s: %v", name, problems)
		}
	}
}

// TestMainAcceptsDataWiring pins the contract that a hand-written data source
// reference is not a deviation: a provider schema says nothing about where an
// attribute value must come from.
func TestMainAcceptsDataWiring(t *testing.T) {
	const resourceName, varName = "yandex_compute_instance", "compute"
	res := resource(t, resourceName)
	vars, mainTF := module(t, res, resourceName, varName)

	mainTF = mutate(t, mainTF, "each.value.zone", "data.yandex_compute_image.img[each.key].family")
	problems, err := MainConforms(mainTF, vars, res, fixtureAddr, resourceName, varName)
	if err != nil {
		t.Fatalf("MainConforms: %v", err)
	}
	if len(problems) > 0 {
		t.Errorf("hand wiring reported as a deviation: %v", problems)
	}
}

func TestMainDetectsDeviations(t *testing.T) {
	const resourceName, varName = "yandex_compute_instance", "compute"
	res := resource(t, resourceName)
	requiredName, requiredAttr := pick(t, res, func(a schema.Attribute) bool { return a.Required })
	computed, _ := pick(t, res, func(a schema.Attribute) bool { return a.ComputedOnly() })
	vars, mainTF := module(t, res, resourceName, varName)

	afterForEach := "for_each = var." + varName
	addAfterForEach := func(line string) []byte {
		return mutate(t, mainTF, afterForEach, afterForEach+"\n  "+line)
	}
	cases := []struct {
		name string
		want string
		src  []byte
	}{
		{"unknown attribute", "not in the schema", addAfterForEach("zzz_unknown = each.value.zone")},
		{"computed-only attribute", "computed-only", addAfterForEach(computed + " = each.value.zone")},
		{"protocol identifier", "cannot be set", addAfterForEach("id = each.value.zone")},
		{"missing required field", "required", unlinkRequired(t, mainTF, requiredName, requiredAttr)},
		{"unresolvable each.value path", "does not resolve", mutate(t, mainTF, "each.value.zone", "each.value.zzone")},
		{"for_each not the module variable", "for_each must reference", mutate(t, mainTF, afterForEach, "for_each = var.other")},
		{"missing resource block", "not found in main.tf", []byte("terraform {}\n")},
		{"wrong provider source", "required_providers", mutate(t, mainTF, fixtureAddr, "example/other")},
		{"broken HCL", "", []byte(`resource "x" {`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems, err := MainConforms(tc.src, vars, res, fixtureAddr, resourceName, varName)
			if tc.want == "" {
				if err == nil {
					t.Fatal("expected a parse error")
				}
				return
			}
			if err != nil {
				t.Fatalf("MainConforms: %v", err)
			}
			if joined := strings.Join(problems, "\n"); !strings.Contains(joined, tc.want) {
				t.Errorf("want a problem containing %q, got %v", tc.want, problems)
			}
		})
	}
}

func TestMainDetectsVariablesWithoutType(t *testing.T) {
	res := resource(t, "yandex_compute_instance")
	_, mainTF := module(t, res, "yandex_compute_instance", "compute")
	vars := []byte("variable \"compute\" {}\n")
	if _, err := MainConforms(mainTF, vars, res, fixtureAddr, "yandex_compute_instance", "compute"); err == nil {
		t.Fatal("expected an error for a variable without a type")
	}
}

// TestMainDetectsMissingRequiredArgument covers the other kind of required
// field: an argument rather than a block.
func TestMainDetectsMissingRequiredArgument(t *testing.T) {
	const resourceName, varName = "yandex_airflow_cluster", "cfg"
	res := resource(t, resourceName)
	name, _ := pick(t, res, func(a schema.Attribute) bool { return a.Required && !a.Block })
	vars, mainTF := module(t, res, resourceName, varName)

	problems, err := MainConforms(dropAttr(t, mainTF, name), vars, res, fixtureAddr, resourceName, varName)
	if err != nil {
		t.Fatalf("MainConforms: %v", err)
	}
	if joined := strings.Join(problems, "\n"); !strings.Contains(joined, "required attribute") {
		t.Errorf("want a missing-required problem for %q, got %v", name, problems)
	}
}

// TestMainDetectsMiswiredBlock checks that a dynamic block iterating the wrong
// variable field is reported.
func TestMainDetectsMiswiredBlock(t *testing.T) {
	const resourceName, varName = "yandex_vpc_network", "vpc_network"
	res := resource(t, resourceName)
	vars, mainTF := module(t, res, resourceName, varName)

	mainTF = mutate(t, mainTF, "for_each = each.value.timeouts == null ? [] : [each.value.timeouts]",
		"for_each = each.value.labels == null ? [] : [each.value.labels]")
	problems, err := MainConforms(mainTF, vars, res, fixtureAddr, resourceName, varName)
	if err != nil {
		t.Fatalf("MainConforms: %v", err)
	}
	if joined := strings.Join(problems, "\n"); !strings.Contains(joined, "for_each iterates labels") {
		t.Errorf("want a miswired-block problem, got %v", problems)
	}
}
