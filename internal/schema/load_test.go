package schema

import (
	"os"
	"path/filepath"
	"testing"
)

// fixture is a frozen snapshot of the public Yandex Cloud provider schema
// (registry.opentofu.org/yandex-cloud/yandex v0.228.0).
const fixture = "../../testdata/yandex.json"

func TestLoadAutoSelectsProvider(t *testing.T) {
	ps, err := Load(fixture)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if ps.FormatVersion != SupportedFormatVersion {
		t.Fatalf("format_version = %q, want %q", ps.FormatVersion, SupportedFormatVersion)
	}
	addr, p, err := ps.Provider("")
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if addr != "registry.opentofu.org/yandex-cloud/yandex" {
		t.Errorf("auto-selected provider = %q", addr)
	}
	// Counts are pinned because the fixture is committed.
	if got := len(p.ResourceSchemas); got != 253 {
		t.Errorf("resource count = %d, want 253", got)
	}
	if got := len(p.DataSourceSchemas); got != 153 {
		t.Errorf("data source count = %d, want 153", got)
	}
	if _, _, err := ps.Provider("no/such/provider"); err == nil {
		t.Error("expected error for unknown provider")
	}
}

func TestResourceShape(t *testing.T) {
	res := resource(t, "yandex_vpc_network")
	attrs := res.Block.AllAttributes()

	if a := attrs["description"]; !a.Optional || a.Required || a.Computed {
		t.Errorf("description = %+v, want a plain optional string", a)
	}
	if !attrs["created_at"].ComputedOnly() {
		t.Error("created_at must be computed-only")
	}
	if a := attrs["labels"]; !a.Optional || !a.Computed {
		t.Errorf("labels = %+v, want optional+computed", a)
	}
	if got := string(attrs["labels"].Type); got != `["map","string"]` {
		t.Errorf("labels type = %s, want a map of string", got)
	}
	if a := attrs["timeouts"]; a.NestedType == nil || a.NestedType.NestingMode != "single" {
		t.Errorf("timeouts = %+v, want a single nested object", a)
	}

	if _, err := provider(t).Resource("yandex_nope"); err == nil {
		t.Error("expected error for unknown resource")
	}
}

// TestLegacyBlockTypesBecomeNestedAttributes covers the older block form, which
// this provider uses far more than nested_type attributes.
func TestLegacyBlockTypesBecomeNestedAttributes(t *testing.T) {
	res := resource(t, "yandex_compute_instance")
	attrs := res.Block.AllAttributes()

	boot, ok := attrs["boot_disk"]
	if !ok || boot.NestedType == nil || boot.NestedType.NestingMode != "list" {
		t.Fatalf("boot_disk = %+v, want a list block", boot)
	}
	if !boot.Required {
		t.Error("boot_disk has min_items=1, so it must be required")
	}
	if fs := attrs["filesystem"]; !fs.Optional || fs.NestedType.NestingMode != "set" {
		t.Errorf("filesystem = %+v, want an optional set block", fs)
	}
	if inner := boot.NestedType.Attributes["initialize_params"]; inner.NestedType == nil || inner.NestedType.NestingMode != "list" {
		t.Errorf("boot_disk.initialize_params = %+v, want a list block", inner)
	}
}

func TestDeepNesting(t *testing.T) {
	// yandex_connectionmanager_connection.params nests seven levels deep.
	res := resource(t, "yandex_connectionmanager_connection")
	attrs := res.Block.AllAttributes()
	depth := 0
	for _, a := range attrs {
		if d := attributeDepth(a); d > depth {
			depth = d
		}
	}
	if depth < 5 {
		t.Fatalf("nested attribute depth = %d, want at least 5", depth)
	}
}

func TestResourceNamesSorted(t *testing.T) {
	p := provider(t)
	names := p.ResourceNames()
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("resource names not sorted: %q > %q", names[i-1], names[i])
		}
	}
}

func TestLoadRejectsUnsupportedFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.json")
	if err := os.WriteFile(path, []byte(`{"format_version":"9.9","provider_schemas":{"x":{"resource_schemas":{}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for unsupported format_version")
	}
}

func TestProviderAcceptsRegistryRelativeAddress(t *testing.T) {
	ps, err := Load(fixture)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	const want = "registry.opentofu.org/yandex-cloud/yandex"
	// The full address, the registry-relative form and the bare provider type
	// all select the same provider while it is unambiguous.
	for _, addr := range []string{want, "yandex-cloud/yandex", "yandex"} {
		got, _, err := ps.Provider(addr)
		if err != nil {
			t.Errorf("Provider(%q): %v", addr, err)
			continue
		}
		if got != want {
			t.Errorf("Provider(%q) = %q, want %q", addr, got, want)
		}
	}
	if _, _, err := ps.Provider("nosuch/thing"); err == nil {
		t.Error("an unknown address must not resolve")
	}
}

func TestProviderRejectsAmbiguousAddress(t *testing.T) {
	ps := &ProviderSchemas{
		FormatVersion: SupportedFormatVersion,
		ProviderSchemas: map[string]ProviderSchema{
			"registry.opentofu.org/yandex-cloud/yandex": {},
			"registry.terraform.io/yandex-cloud/yandex": {},
		},
	}
	if _, _, err := ps.Provider("yandex-cloud/yandex"); err == nil {
		t.Fatal("expected an ambiguity error")
	}
	got, _, err := ps.Provider("registry.terraform.io/yandex-cloud/yandex")
	if err != nil {
		t.Fatalf("exact match must win: %v", err)
	}
	if got != "registry.terraform.io/yandex-cloud/yandex" {
		t.Fatalf("Provider returned %q", got)
	}
}

func TestLocalName(t *testing.T) {
	cases := map[string]string{
		"registry.opentofu.org/yandex-cloud/yandex": "yandex",
		"registry.example.com/ns/type":              "type",
		"yandex":                                    "yandex",
	}
	for in, want := range cases {
		if got := LocalName(in); got != want {
			t.Errorf("LocalName(%q) = %q, want %q", in, got, want)
		}
	}
}

func provider(t *testing.T) ProviderSchema {
	t.Helper()
	ps, err := Load(fixture)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, p, err := ps.Provider("")
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	return p
}

func resource(t *testing.T, name string) Schema {
	t.Helper()
	res, err := provider(t).Resource(name)
	if err != nil {
		t.Fatalf("Resource %s: %v", name, err)
	}
	return res
}

// attributeDepth returns the nesting depth of an attribute, counting the
// attribute itself as one level when it has a nested type.
func attributeDepth(a Attribute) int {
	if a.NestedType == nil {
		return 0
	}
	depth := 0
	for _, sub := range a.NestedType.Attributes {
		if d := attributeDepth(sub); d > depth {
			depth = d
		}
	}
	return depth + 1
}
