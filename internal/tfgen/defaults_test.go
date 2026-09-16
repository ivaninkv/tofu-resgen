package tfgen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ivaninkv/tofu-resgen/internal/schema"
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

func loadResource(t *testing.T, name string) schema.Schema {
	t.Helper()
	res, err := providerSchema(t).Resource(name)
	if err != nil {
		t.Fatalf("Resource %s: %v", name, err)
	}
	return res
}

func TestValidateAcceptsOptionalDefaults(t *testing.T) {
	defaults := NewDefaults(map[string]any{
		"description": "managed by tofu-resgen",
		"labels":      map[string]any{"managed-by": "tofu-resgen"},
	})
	if err := defaults.Validate(loadResource(t, "yandex_vpc_network")); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateRejectsRequiredAttribute(t *testing.T) {
	// yandex_vpc_subnet requires network_id, which cannot carry a default.
	defaults := NewDefaults(map[string]any{"network_id": "enpmocknetwork"})
	if err := defaults.Validate(loadResource(t, "yandex_vpc_subnet")); err == nil {
		t.Fatal("expected error: network_id is required, so it cannot carry a default")
	}
}

func TestValidateRejectsUnknownAttribute(t *testing.T) {
	defaults := NewDefaults(map[string]any{"nope": "x"})
	if err := defaults.Validate(loadResource(t, "yandex_vpc_network")); err == nil {
		t.Fatal("expected error for unknown attribute")
	}
}

func TestValidateRejectsTypeMismatch(t *testing.T) {
	defaults := NewDefaults(map[string]any{"description": 5})
	if err := defaults.Validate(loadResource(t, "yandex_vpc_network")); err == nil {
		t.Fatal("expected error for type mismatch")
	}
}

func TestValidateNestedObjectRequiresFields(t *testing.T) {
	// yandex_airflow_cluster.datacatalog is an optional block whose only
	// attribute, enabled, is required.
	bad := NewDefaults(map[string]any{"datacatalog": map[string]any{}})
	if err := bad.Validate(loadResource(t, "yandex_airflow_cluster")); err == nil {
		t.Fatal("expected error for missing required field enabled")
	}
	good := NewDefaults(map[string]any{"datacatalog": map[string]any{"enabled": true}})
	if err := good.Validate(loadResource(t, "yandex_airflow_cluster")); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateNestedCollectionType(t *testing.T) {
	// yandex_compute_instance.secondary_disk is a set of objects.
	bad := NewDefaults(map[string]any{
		"secondary_disk": map[string]any{"disk_id": "a"},
	})
	if err := bad.Validate(loadResource(t, "yandex_compute_instance")); err == nil {
		t.Fatal("expected error: secondary_disk is a set, not a map")
	}
	good := NewDefaults(map[string]any{
		"secondary_disk": []any{map[string]any{"disk_id": "a"}},
	})
	if err := good.Validate(loadResource(t, "yandex_compute_instance")); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestLoadDefaultsFromYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "defaults.yaml")
	if err := os.WriteFile(path, []byte("description: \"managed by tofu-resgen\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defaults, err := LoadDefaults(path)
	if err != nil {
		t.Fatalf("LoadDefaults: %v", err)
	}
	if v, ok := defaults.Lookup([]string{"description"}); !ok || v != "managed by tofu-resgen" {
		t.Errorf("Lookup(description) = %v, %v", v, ok)
	}
	if err := defaults.Validate(loadResource(t, "yandex_vpc_network")); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestNilDefaultsLookup(t *testing.T) {
	var d *Defaults
	if _, ok := d.Lookup([]string{"x"}); ok {
		t.Error("nil defaults must not resolve")
	}
	if err := d.Validate(schema.Schema{}); err != nil {
		t.Errorf("nil defaults must validate: %v", err)
	}
}
