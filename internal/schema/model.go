// Package schema models the JSON emitted by `tofu providers schema -json`
// (format_version 1.0) and loads it from disk.
package schema

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// ProviderSchemas is the root document.
type ProviderSchemas struct {
	FormatVersion   string                    `json:"format_version"`
	ProviderSchemas map[string]ProviderSchema `json:"provider_schemas"`
}

// ProviderSchema holds every resource and data source of one provider.
type ProviderSchema struct {
	Provider          ProviderConfig    `json:"provider"`
	ResourceSchemas   map[string]Schema `json:"resource_schemas"`
	DataSourceSchemas map[string]Schema `json:"data_source_schemas"`
}

// ProviderConfig carries provider-level metadata such as the version.
type ProviderConfig struct {
	Version int `json:"version"`
}

// Schema describes a single resource or data source.
type Schema struct {
	Version int   `json:"version"`
	Block   Block `json:"block"`
}

// Block is a set of attributes plus legacy nested block types.
type Block struct {
	Attributes      map[string]Attribute   `json:"attributes"`
	BlockTypes      map[string]NestedBlock `json:"block_types"`
	Description     string                 `json:"description"`
	DescriptionKind string                 `json:"description_kind"`
}

// Attribute is a single field. Exactly one of Type / NestedType is set.
type Attribute struct {
	Type            json.RawMessage `json:"type"`
	NestedType      *NestedType     `json:"nested_type"`
	Description     string          `json:"description"`
	DescriptionKind string          `json:"description_kind"`
	Required        bool            `json:"required"`
	Optional        bool            `json:"optional"`
	Computed        bool            `json:"computed"`
	Deprecated      bool            `json:"deprecated"`
	// Block marks an attribute normalised from a legacy nested block type.
	// Its value is a nested_type, but the resource takes it as a block
	// (`name { ... }`), not as an argument, so main.tf must render block
	// syntax for it.
	Block bool `json:"-"`
}

// NestedType is an inline object of attributes with a collection mode.
type NestedType struct {
	Attributes  map[string]Attribute `json:"attributes"`
	NestingMode string               `json:"nesting_mode"`
}

// NestedBlock is the legacy (pre nested_type) block form.
type NestedBlock struct {
	NestingMode string `json:"nesting_mode"`
	Block       Block  `json:"block"`
	MinItems    int    `json:"min_items"`
	MaxItems    int    `json:"max_items"`
}

// protocolID is the identifier the plugin protocol maintains for every
// resource. OpenTofu rejects it as a configuration argument even when a
// provider schema marks it optional, so it is never settable.
const protocolID = "id"

// ConfigAttributes returns the attributes a resource configuration may set:
// the block's attributes minus the ones the provider computes and minus the
// protocol-maintained identifier. Nested levels keep their own attributes
// as-is; the reservation applies to the resource itself only.
func (s Schema) ConfigAttributes() map[string]Attribute {
	out := s.Block.AllAttributes()
	delete(out, protocolID)
	return out
}

// ProtocolAttribute reports whether a resource-level attribute is maintained
// by the plugin protocol rather than by the provider's configuration.
func ProtocolAttribute(name string) bool {
	return name == protocolID
}

// ComputedOnly reports an attribute that the provider computes and the user
// never sets. Such attributes are not representable in module variables.
func (a Attribute) ComputedOnly() bool {
	return a.Computed && !a.Optional && !a.Required
}

// AllAttributes flattens attributes and legacy block types into one map.
// Block types are normalised into nested_type attributes so every consumer
// has a single shape to walk.
func (b Block) AllAttributes() map[string]Attribute {
	out := make(map[string]Attribute, len(b.Attributes)+len(b.BlockTypes))
	maps.Copy(out, b.Attributes)
	for name, nb := range b.BlockTypes {
		out[name] = Attribute{
			Description:     nb.Block.Description,
			DescriptionKind: nb.Block.DescriptionKind,
			NestedType: &NestedType{
				Attributes:  nb.Block.AllAttributes(),
				NestingMode: nb.NestingMode,
			},
			Required: nb.MinItems > 0,
			Optional: nb.MinItems == 0,
			Block:    true,
		}
	}
	return out
}

// ResourceNames returns the resource type names in lexical order.
func (p ProviderSchema) ResourceNames() []string {
	return slices.Sorted(maps.Keys(p.ResourceSchemas))
}

// Resource looks up one resource schema by its type name.
func (p ProviderSchema) Resource(name string) (Schema, error) {
	s, ok := p.ResourceSchemas[name]
	if !ok {
		return Schema{}, fmt.Errorf("resource %q not found in provider schema", name)
	}
	return s, nil
}
