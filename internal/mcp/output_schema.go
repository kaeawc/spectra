package mcp

import (
	"encoding"
	"encoding/json"
	"reflect"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/kaeawc/spectra/internal/devices"
	"github.com/kaeawc/spectra/internal/hostload"
	"github.com/kaeawc/spectra/internal/idle"
	"github.com/kaeawc/spectra/internal/lineage"
	"github.com/kaeawc/spectra/internal/orphans"
	"github.com/kaeawc/spectra/internal/spawntrace"
)

// reportOutputTypes are the Go result types of the CLI-shaped report tools.
// Every other tool answers with a toolEnvelope. owner has two result shapes
// (target and under), so it has no single type and gets the permissive schema.
var reportOutputTypes = map[string]reflect.Type{
	"load":    reflect.TypeFor[hostload.Report](),
	"orphans": reflect.TypeFor[orphans.Report](),
	"lineage": reflect.TypeFor[lineage.Result](),
	"spawns":  reflect.TypeFor[spawntrace.Report](),
	"idle":    reflect.TypeFor[idle.Report](),
	"devices": reflect.TypeFor[devices.Report](),
	"owner":   nil,
}

var (
	outputSchemasOnce sync.Once
	outputSchemas     map[string]*jsonschema.Schema
)

// outputSchema is the tool's outputSchema: inferred from its Go result type
// for the report tools, and a plain object schema otherwise. The envelope
// tools all share one shape (envelopeSchema), documented once in
// docs/operations/mcp.md rather than repeated in every tools/list entry.
func outputSchema(tool string) *jsonschema.Schema {
	outputSchemasOnce.Do(func() {
		outputSchemas = map[string]*jsonschema.Schema{}
		for name, t := range reportOutputTypes {
			outputSchemas[name] = schemaForType(t)
		}
	})
	if s, ok := outputSchemas[tool]; ok {
		return s.CloneSchemas()
	}
	return permissiveObjectSchema()
}

// envelopeSchema is the shape of every non-report tool's structuredContent.
func envelopeSchema() *jsonschema.Schema { return schemaForType(reflect.TypeFor[toolEnvelope]()) }

func permissiveObjectSchema() *jsonschema.Schema { return &jsonschema.Schema{Type: "object"} }

// anyJSONSchema accepts any JSON value. It lists the types (rather than being
// empty) so the inference's nullable-pointer handling keeps them all.
func anyJSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Types: []string{"null", "boolean", "number", "string", "array", "object"}}
}

var (
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
	timeType          = reflect.TypeFor[time.Time]()
)

// schemaForType infers t's schema. A value of a type with its own JSON or
// text marshaler (other than time.Time, which the inference understands)
// has a shape reflection cannot see, so it is left unconstrained rather than
// described wrongly. A type the inference rejects (a recursive one) gets
// the permissive object schema.
func schemaForType(t reflect.Type) *jsonschema.Schema {
	if t == nil {
		return permissiveObjectSchema()
	}
	custom := map[reflect.Type]*jsonschema.Schema{}
	collectCustomMarshalers(t, map[reflect.Type]bool{}, custom)
	s, err := jsonschema.ForType(t, &jsonschema.ForOptions{TypeSchemas: custom})
	if err != nil || s.Type != "object" {
		return permissiveObjectSchema()
	}
	allowNullMaps(s)
	trimOutputSchema(s, 0)
	return s
}

// outputSchemaDepth is how many levels of nested properties an outputSchema
// spells out. Every client loads every tool's schema on each session, and
// the deep report shapes (load, orphans, idle, devices) cost more tokens
// than they help: the result itself shows the nested fields. Below the
// root, required and additionalProperties are dropped too; arrays do not
// count as a level, so a table's row fields stay listed.
const outputSchemaDepth = 2

// trimOutputSchema loosens s below the root so it still validates every
// result: nested objects keep their type (and enum), lose required and the
// closed-struct additionalProperties, and past outputSchemaDepth lose their
// properties. Map value schemas are kept.
func trimOutputSchema(s *jsonschema.Schema, depth int) {
	if s == nil {
		return
	}
	if depth > 0 {
		s.Required = nil
		if s.AdditionalProperties != nil && s.AdditionalProperties.Not != nil {
			s.AdditionalProperties = nil
		}
	}
	if depth >= outputSchemaDepth {
		s.Properties, s.PropertyOrder = nil, nil
	}
	for _, p := range s.Properties {
		trimOutputSchema(p, depth+1)
	}
	trimOutputSchema(s.Items, depth)
	if s.AdditionalProperties != nil {
		trimOutputSchema(s.AdditionalProperties, depth+1)
	}
}

func implementsMarshaler(t reflect.Type) bool {
	return t.Implements(jsonMarshalerType) || reflect.PointerTo(t).Implements(jsonMarshalerType) ||
		t.Implements(textMarshalerType) || reflect.PointerTo(t).Implements(textMarshalerType)
}

// collectCustomMarshalers maps every custom-marshaling type reachable from t
// to the any-value schema.
func collectCustomMarshalers(t reflect.Type, seen map[reflect.Type]bool, out map[reflect.Type]*jsonschema.Schema) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if seen[t] || t == timeType {
		return
	}
	seen[t] = true
	if implementsMarshaler(t) {
		out[t] = anyJSONSchema()
		return
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		collectCustomMarshalers(t.Elem(), seen, out)
	case reflect.Struct:
		for _, f := range reflect.VisibleFields(t) {
			if f.IsExported() {
				collectCustomMarshalers(f.Type, seen, out)
			}
		}
	}
}

// allowNullMaps lets map-typed values be null, as a nil Go map marshals;
// the inference already does this for slices and pointers.
func allowNullMaps(s *jsonschema.Schema) {
	if s == nil {
		return
	}
	isStruct := s.AdditionalProperties != nil && s.AdditionalProperties.Not != nil
	if s.Type == "object" && s.AdditionalProperties != nil && !isStruct && s.Properties == nil {
		s.Type, s.Types = "", []string{"null", "object"}
	}
	for _, p := range s.Properties {
		allowNullMaps(p)
	}
	allowNullMaps(s.Items)
	if s.AdditionalProperties != nil && !isStruct {
		allowNullMaps(s.AdditionalProperties)
	}
}
