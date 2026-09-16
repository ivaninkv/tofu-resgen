package verify

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/ivaninkv/tofu-resgen/internal/schema"
	"github.com/ivaninkv/tofu-resgen/internal/tfgen"
)

// example renders the example terraform.tfvars for one resource.
func example(t *testing.T, res schema.Schema, resource, varName string, optional bool) []byte {
	t.Helper()
	out, err := tfgen.GenerateTFVars(res, tfgen.TFVarsOptions{
		VarName: varName, ResourceName: resource, Optional: optional,
	})
	if err != nil {
		t.Fatalf("GenerateTFVars: %v", err)
	}
	return out
}

// setField replaces the assignment of one field with the given expression.
func setField(t *testing.T, src []byte, name, expr string) []byte {
	t.Helper()
	loc := fieldLine(t, src, name)
	out := append([]byte{}, src[:loc[0]]...)
	out = append(out, (name + " = " + expr)...)
	return append(out, src[loc[1]:]...)
}

// dropField removes the assignment of one required field.
func dropField(t *testing.T, src []byte, name string) []byte {
	t.Helper()
	loc := fieldLine(t, src, name)
	return append(append([]byte{}, src[:loc[0]]...), src[loc[1]+1:]...)
}

func fieldLine(t *testing.T, src []byte, name string) []int {
	t.Helper()
	re := regexp.MustCompile(`(?m)^[ \t]*` + regexp.QuoteMeta(name) + `[ \t]*=.*$`)
	loc := re.FindIndex(src)
	if loc == nil {
		t.Fatalf("no assignment of %q in the example tfvars:\n%s", name, src)
	}
	return loc
}

// TestEveryGeneratedTFVarsConforms cross-checks the example of all 253 provider
// resources, with and without the optional attributes, against the schema.
func TestEveryGeneratedTFVarsConforms(t *testing.T) {
	p := providerSchema(t)
	for _, name := range p.ResourceNames() {
		res, err := p.Resource(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, optional := range []bool{false, true} {
			src := example(t, res, name, "cfg", optional)
			problems, err := TFVarsConforms(src, res, "cfg")
			if err != nil {
				t.Fatalf("%s (optional=%v): %v", name, optional, err)
			}
			if len(problems) > 0 {
				t.Errorf("%s (optional=%v): %v\n%s", name, optional, problems, src)
			}
		}
	}
}

// TestTFVarsAcceptsHandEdits pins the contract for a file meant to be edited:
// renaming the example instance, adding a second one, wiring a value from a
// data source and interpolating a string are not deviations.
func TestTFVarsAcceptsHandEdits(t *testing.T) {
	const resourceName, varName = "yandex_vpc_subnet", "vpc_subnet"
	res := resource(t, resourceName)
	src := example(t, res, resourceName, varName, true)

	src = []byte(strings.Replace(string(src), "example = {", "production = {", 1))
	src = setField(t, src, "network_id", "data.yandex_vpc_network.default.id")
	src = setField(t, src, "v4_cidr_blocks", "[var.cidr]")
	src = []byte(strings.Replace(string(src), varName+" = {\n", varName+" = {\n  staging = {\n"+
		"    network_id     = \"${var.network}\"\n    v4_cidr_blocks = [\"10.1.0.0/24\"]\n  }\n", 1))

	problems, err := TFVarsConforms(src, res, varName)
	if err != nil {
		t.Fatalf("TFVarsConforms: %v", err)
	}
	if len(problems) > 0 {
		t.Errorf("hand edits reported as deviations: %v\n%s", problems, src)
	}
}

func TestTFVarsDetectsDeviations(t *testing.T) {
	const resourceName, varName = "yandex_vpc_subnet", "vpc_subnet"
	res := resource(t, resourceName)
	src := example(t, res, resourceName, varName, false)

	cases := []struct {
		name string
		want string
		src  []byte
	}{
		{
			"missing required attribute",
			`required attribute "network_id" is not set`,
			dropField(t, src, "network_id"),
		},
		{
			"unknown attribute",
			`attribute "zzz" is not in the schema`,
			[]byte("vpc_subnet = {\n  example = {\n    network_id     = \"n\"\n    v4_cidr_blocks = [\"10.0.0.0/24\"]\n    zzz            = 1\n  }\n}\n"),
		},
		{
			"instance is not an object",
			"must be an object",
			[]byte("vpc_subnet = {\n  example = 5\n}\n"),
		},
		{
			"string where a list is required",
			"v4_cidr_blocks: want a [ ... ] literal",
			setField(t, src, "v4_cidr_blocks", `"10.0.0.0/24"`),
		},
		{
			"variable the module does not declare",
			`the module declares the variable "vpc_subnet" only`,
			[]byte(string(src) + "\nother = {}\n"),
		},
		{
			"variable not set",
			`variable "vpc_subnet" is not set`,
			[]byte("terraform {\n}\n"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems, err := TFVarsConforms(tc.src, res, varName)
			if err != nil {
				t.Fatalf("TFVarsConforms: %v", err)
			}
			if joined := strings.Join(problems, "\n"); !strings.Contains(joined, tc.want) {
				t.Errorf("want a problem containing %q, got %v", tc.want, problems)
			}
		})
	}
}

// TestTFVarsChecksNestedRequired covers the fields of a nested object, which a
// hand edit can drop or mistype just as easily as a top-level one.
func TestTFVarsChecksNestedRequired(t *testing.T) {
	const resourceName, varName = "yandex_compute_instance", "compute"
	res := resource(t, resourceName)
	src := example(t, res, resourceName, varName, false)
	if !strings.Contains(string(src), "subnet_id") {
		t.Fatalf("example does not carry the required network_interface field:\n%s", src)
	}

	cases := []struct {
		name string
		want string
		src  []byte
	}{
		{
			"dropped required field of a collection element",
			`network_interface[0]: required attribute "subnet_id" is not set`,
			dropField(t, src, "subnet_id"),
		},
		{
			"contradictory type deep in a collection element",
			"resources[0].cores: want number, got bool literal",
			setField(t, src, "cores", "true"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems, err := TFVarsConforms(tc.src, res, varName)
			if err != nil {
				t.Fatalf("TFVarsConforms: %v", err)
			}
			if joined := strings.Join(problems, "\n"); !strings.Contains(joined, tc.want) {
				t.Errorf("want a problem containing %q, got %v", tc.want, problems)
			}
		})
	}
}

// TestTFVarsChecksCollectionShapes covers the shape of a collection element,
// where the fixture only reaches maps through optional attributes.
func TestTFVarsChecksCollectionShapes(t *testing.T) {
	res := schema.Schema{Block: schema.Block{Attributes: map[string]schema.Attribute{
		"topics": {Required: true, Type: json.RawMessage(`["map","string"]`)},
		"ports":  {Required: true, Type: json.RawMessage(`["list","number"]`)},
	}}}
	cases := []struct {
		name string
		want string
		src  []byte
	}{
		{
			"list where a map is required",
			"topics: want a { ... } literal",
			[]byte("cfg = { example = { topics = [\"a\"], ports = [1] } }\n"),
		},
		{
			"element of the wrong type",
			"ports[0]: want number, got bool literal",
			[]byte("cfg = { example = { topics = { a = \"b\" }, ports = [true] } }\n"),
		},
		{
			// OpenTofu converts between a string and a primitive, so quoting a
			// number must not be reported.
			"string literal where a number is declared",
			"",
			[]byte("cfg = { example = { topics = { a = \"b\" }, ports = [\"2\"] } }\n"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems, err := TFVarsConforms(tc.src, res, "cfg")
			if err != nil {
				t.Fatalf("TFVarsConforms: %v", err)
			}
			if tc.want == "" {
				if len(problems) > 0 {
					t.Fatalf("want no problems, got %v", problems)
				}
				return
			}
			if joined := strings.Join(problems, "\n"); !strings.Contains(joined, tc.want) {
				t.Errorf("want a problem containing %q, got %v", tc.want, problems)
			}
		})
	}
}

func TestTFVarsRejectsBrokenHCL(t *testing.T) {
	res := resource(t, "yandex_vpc_network")
	if _, err := TFVarsConforms([]byte("cfg = {"), res, "cfg"); err == nil {
		t.Fatal("expected a parse error")
	}
}
