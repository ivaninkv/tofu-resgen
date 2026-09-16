package schema

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
)

// SupportedFormatVersion is the only schema format this tool understands.
const SupportedFormatVersion = "1.0"

// Load reads and validates a `tofu providers schema -json` document.
func Load(path string) (*ProviderSchemas, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read schema: %w", err)
	}
	var ps ProviderSchemas
	if err := json.Unmarshal(raw, &ps); err != nil {
		return nil, fmt.Errorf("parse schema %s: %w", path, err)
	}
	if ps.FormatVersion == "" {
		return nil, fmt.Errorf("schema %s: missing format_version", path)
	}
	if ps.FormatVersion != SupportedFormatVersion {
		return nil, fmt.Errorf("schema %s: unsupported format_version %q (want %s)",
			path, ps.FormatVersion, SupportedFormatVersion)
	}
	if len(ps.ProviderSchemas) == 0 {
		return nil, fmt.Errorf("schema %s: no provider schemas", path)
	}
	return &ps, nil
}

// Provider selects a provider schema by address. An empty address is accepted
// only when the document contains exactly one provider. A registry-relative
// address is accepted as well, so "yandex-cloud/yandex" selects
// "registry.opentofu.org/yandex-cloud/yandex".
func (ps *ProviderSchemas) Provider(addr string) (string, ProviderSchema, error) {
	if addr == "" {
		if len(ps.ProviderSchemas) == 1 {
			for name, p := range ps.ProviderSchemas {
				return name, p, nil
			}
		}
		return "", ProviderSchema{}, fmt.Errorf(
			"schema contains %d providers; select one with -provider", len(ps.ProviderSchemas))
	}
	if p, ok := ps.ProviderSchemas[addr]; ok {
		return addr, p, nil
	}
	var found []string
	for name := range ps.ProviderSchemas {
		if strings.HasSuffix(name, "/"+addr) {
			found = append(found, name)
		}
	}
	switch len(found) {
	case 0:
		return "", ProviderSchema{}, fmt.Errorf("provider %q not found in schema", addr)
	case 1:
		return found[0], ps.ProviderSchemas[found[0]], nil
	default:
		slices.Sort(found)
		return "", ProviderSchema{}, fmt.Errorf(
			"provider %q is ambiguous in schema: %s", addr, strings.Join(found, ", "))
	}
}

// LocalName returns the trailing segment of a provider address.
func LocalName(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == '/' {
			return addr[i+1:]
		}
	}
	return addr
}
