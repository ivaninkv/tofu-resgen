package tfgen

import (
	"encoding/json"
	"testing"

	"github.com/ivaninkv/tofu-resgen/internal/schema"
)

func TestRenderTypeJSON(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`"string"`, `string`},
		{`"number"`, `number`},
		{`"bool"`, `bool`},
		{`["list","string"]`, `list(string)`},
		{`["set","number"]`, `set(number)`},
		{`["map",["set","string"]]`, `map(set(string))`},
		{`["object",{"b":"number","a":"bool"}]`, "object({\n  a = bool,\n  b = number\n})"},
	}
	for _, tc := range cases {
		got, err := renderTypeJSON(json.RawMessage(tc.raw))
		if err != nil {
			t.Errorf("renderTypeJSON(%s): %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("renderTypeJSON(%s) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestRenderTypeJSONRejectsUnknown(t *testing.T) {
	if _, err := renderTypeJSON(json.RawMessage(`"float128"`)); err == nil {
		t.Error("expected error for unknown primitive")
	}
	if _, err := renderTypeJSON(json.RawMessage(`["widget","string"]`)); err == nil {
		t.Error("expected error for unknown complex type")
	}
}

func TestFieldOptionalityAndDefaults(t *testing.T) {
	required := schema.Attribute{Type: json.RawMessage(`"string"`), Required: true}
	optional := schema.Attribute{Type: json.RawMessage(`"string"`), Optional: true}
	optionalComputed := schema.Attribute{Type: json.RawMessage(`"number"`), Optional: true, Computed: true}

	r := &Renderer{}
	if got, err := r.field([]string{"a"}, required); err != nil || got != "string" {
		t.Errorf("required field = %q, %v", got, err)
	}
	if got, err := r.field([]string{"a"}, optional); err != nil || got != "optional(string)" {
		t.Errorf("optional field = %q, %v", got, err)
	}
	if got, err := r.field([]string{"a"}, optionalComputed); err != nil || got != "optional(number)" {
		t.Errorf("optional+computed field = %q, %v", got, err)
	}

	if _, err := (&Renderer{}).field([]string{"a"}, schema.Attribute{Type: json.RawMessage(`"string"`)}); err == nil {
		t.Error("expected error for attribute that is neither required nor optional")
	}
}

func TestDefaultsApplied(t *testing.T) {
	defaults := NewDefaults(map[string]any{
		"cluster_state": "on",
		"image":         map[string]any{"distribution": "astra"},
	})
	r := &Renderer{Defaults: defaults}
	state := schema.Attribute{Type: json.RawMessage(`"string"`), Optional: true, Computed: true}
	got, err := r.field([]string{"cluster_state"}, state)
	if err != nil {
		t.Fatalf("field: %v", err)
	}
	if got != `optional(string, "on")` {
		t.Errorf("defaulted field = %q", got)
	}
	// No default recorded for this path -> plain optional.
	if got, _ := r.field([]string{"lifetime"}, schema.Attribute{Type: json.RawMessage(`"number"`), Optional: true}); got != "optional(number)" {
		t.Errorf("unexpected field %q", got)
	}
}

func TestComputedOnlyExcluded(t *testing.T) {
	attrs := map[string]schema.Attribute{
		"item_id": {Type: json.RawMessage(`"string"`), Computed: true},
		"name":    {Type: json.RawMessage(`"string"`), Required: true},
	}
	got, err := (&Renderer{}).objectType(nil, attrs)
	if err != nil {
		t.Fatalf("objectType: %v", err)
	}
	if contains(got, "item_id") {
		t.Errorf("computed-only attribute leaked into object: %s", got)
	}
	if !contains(got, "name = string") {
		t.Errorf("required attribute missing: %s", got)
	}
}

func TestNestingModesAndDepth(t *testing.T) {
	deep := schema.Attribute{
		NestedType: &schema.NestedType{
			NestingMode: "list",
			Attributes: map[string]schema.Attribute{
				"inner": {Required: true, NestedType: &schema.NestedType{
					NestingMode: "single",
					Attributes: map[string]schema.Attribute{
						"leaf": {Type: json.RawMessage(`"bool"`), Required: true},
					},
				}},
			},
		},
		Required: true,
	}
	got, err := (&Renderer{}).attributeType([]string{"deep"}, deep)
	if err != nil {
		t.Fatalf("attributeType: %v", err)
	}
	want := "list(object({\n\n  inner = object({\n\n    leaf = bool\n\n  })\n\n}))"
	if normalise(got) != normalise(want) {
		t.Errorf("nested type =\n%s\nwant\n%s", got, want)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

func normalise(s string) string {
	out := make([]byte, 0, len(s))
	prevSpace := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\n' || c == '\t' {
			if !prevSpace {
				out = append(out, ' ')
			}
			prevSpace = true
			continue
		}
		prevSpace = false
		out = append(out, c)
	}
	return string(out)
}
