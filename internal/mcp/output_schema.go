package mcp

import (
	"encoding"
	"encoding/json"
	"reflect"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
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
	"owner":   nil,
}

var (
	outputSchemasOnce sync.Once
	outputSchemas     map[string]*jsonschema.Schema
)

// outputSchema is the tool's outputSchema: inferred from its Go result type,
// or a permissive object schema where inference would not match the JSON the
// type produces.
func outputSchema(tool string) *jsonschema.Schema {
	outputSchemasOnce.Do(func() {
		outputSchemas = map[string]*jsonschema.Schema{}
		for name, t := range reportOutputTypes {
			outputSchemas[name] = schemaForType(t)
		}
		outputSchemas[""] = schemaForType(reflect.TypeFor[toolEnvelope]())
	})
	if s, ok := outputSchemas[tool]; ok {
		return s.CloneSchemas()
	}
	return outputSchemas[""].CloneSchemas()
}

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
	return s
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
