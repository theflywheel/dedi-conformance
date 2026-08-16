package openapi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// JSONSchema is the slice of a JSON Schema document these cases need.
//
// The standard's reference registry schemas are the one place it does state
// what is mandatory — unlike the OpenAPI document, which declares no `required`
// anywhere — so they are read rather than transcribed. A hand-copied list is a
// second source of truth that goes stale silently, and did: an earlier version
// of the beckn profile carried an invented `type` enum of BAP/BPP/BG/
// BPP_NETWORK/BAP_NETWORK, where the schema says BAP/BPP/BG/CDS. It would have
// passed records the standard rejects and rejected ones it permits.
type JSONSchema struct {
	Required   []string              `json:"required"`
	Properties map[string]SchemaNode `json:"properties"`
}

// SchemaNode is one property.
type SchemaNode struct {
	Type  string   `json:"type"`
	Enum  []string `json:"enum"`
	Items *struct {
		Type string   `json:"type"`
		Enum []string `json:"enum"`
	} `json:"items"`
}

// EnumOf returns the permitted values for a property, following into array
// items where the constraint lives on the element rather than the array.
func (s *JSONSchema) EnumOf(prop string) []string {
	p, ok := s.Properties[prop]
	if !ok {
		return nil
	}
	if len(p.Enum) > 0 {
		return p.Enum
	}
	if p.Items != nil {
		return p.Items.Enum
	}
	return nil
}

// LoadJSONSchema reads a reference registry schema that sits alongside the
// OpenAPI document, e.g. LoadJSONSchema(specPath, "Beckn_subscriber.json").
// specPath is the path to api/openapi.yaml; the schemas live at ../schemas.
func LoadJSONSchema(specPath, name string) (*JSONSchema, error) {
	p := filepath.Join(filepath.Dir(filepath.Dir(specPath)), "schemas", name)
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("conformance: reading reference schema %s: %w", p, err)
	}
	var s JSONSchema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("conformance: parsing reference schema %s: %w", p, err)
	}
	return &s, nil
}
