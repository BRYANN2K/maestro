package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	maxSchemaBytes = 128 << 10
	maxSchemaDepth = 24
	maxSchemaNodes = 2048
)

// compiledSchema keeps the untrusted wire schema separate from the compiled
// validator. The validator is never exposed to providers or frontends.
type compiledSchema struct {
	wire      map[string]any
	validator *jsonschema.Schema
}

type denySchemaLoader struct{}

func (denySchemaLoader) Load(string) (any, error) {
	return nil, errors.New("external JSON Schema references are disabled")
}

func compileToolSchema(kind string, wire map[string]any) (compiled *compiledSchema, err error) {
	defer func() {
		if recover() != nil {
			compiled = nil
			err = fmt.Errorf("%s schema compiler rejected unsafe input", kind)
		}
	}()
	if wire == nil {
		return nil, fmt.Errorf("%s schema is required", kind)
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("%s schema is not JSON", kind)
	}
	if len(raw) > maxSchemaBytes {
		return nil, fmt.Errorf("%s schema exceeds %d bytes", kind, maxSchemaBytes)
	}
	if err := inspectSchemaValue(wire, 0, new(int)); err != nil {
		return nil, fmt.Errorf("%s schema: %w", kind, err)
	}
	rootType, ok := wire["type"].(string)
	if !ok || rootType != "object" {
		return nil, fmt.Errorf("%s schema root type must be object", kind)
	}
	if ref, _ := wire["$ref"].(string); ref != "" {
		return nil, fmt.Errorf("%s schema root cannot be a reference", kind)
	}

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%s schema is not valid JSON", kind)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(denySchemaLoader{})
	const location = "urn:maestro:mcp:tool-schema"
	if err := compiler.AddResource(location, doc); err != nil {
		return nil, fmt.Errorf("%s schema cannot be registered", kind)
	}
	validator, err := compiler.Compile(location)
	if err != nil {
		return nil, fmt.Errorf("%s schema cannot be compiled: %s", kind, safeSchemaError(err))
	}
	return &compiledSchema{wire: cloneMap(wire), validator: validator}, nil
}

func inspectSchemaValue(value any, depth int, nodes *int) error {
	if depth > maxSchemaDepth {
		return fmt.Errorf("nesting exceeds %d levels", maxSchemaDepth)
	}
	*nodes++
	if *nodes > maxSchemaNodes {
		return fmt.Errorf("complexity exceeds %d nodes", maxSchemaNodes)
	}
	switch v := value.(type) {
	case map[string]any:
		if ref, ok := v["$ref"].(string); ok && ref != "" && !strings.HasPrefix(ref, "#") {
			return errors.New("external references are disabled")
		}
		for key, child := range v {
			if len(key) > 256 {
				return errors.New("property name is too long")
			}
			if err := inspectSchemaValue(child, depth+1, nodes); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := inspectSchemaValue(child, depth+1, nodes); err != nil {
				return err
			}
		}
	case string:
		if len(v) > 32<<10 {
			return errors.New("string value is too long")
		}
	case nil, bool, float64, json.Number:
		return nil
	default:
		return fmt.Errorf("contains non-JSON value %T", value)
	}
	return nil
}

func (s *compiledSchema) validate(value any) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("schema validator rejected unsafe input")
		}
	}()
	if s == nil || s.validator == nil {
		return errors.New("schema is unavailable")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return errors.New("value is not JSON")
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return errors.New("value is not valid JSON")
	}
	if err := s.validator.Validate(doc); err != nil {
		return errors.New(safeSchemaError(err))
	}
	return nil
}

func safeSchemaError(err error) string {
	if err == nil {
		return ""
	}
	// Validation errors can echo submitted values. Keep the useful instance
	// path while never forwarding arbitrary values or multiline diagnostics.
	text := sanitizeText(err.Error(), 512)
	if idx := strings.Index(text, " does not validate with "); idx >= 0 {
		text = text[:idx] + " does not satisfy the declared schema"
	}
	return text
}

func cloneMap(in map[string]any) map[string]any {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var out map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&out) != nil {
		return nil
	}
	return out
}
