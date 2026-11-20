package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	protocol "github.com/kaeawc/spectra-protocol/protocol/v1"

	"github.com/kaeawc/spectra/internal/capabilities"
	"github.com/kaeawc/spectra/internal/lineage"
	"github.com/kaeawc/spectra/internal/owner"
	"github.com/kaeawc/spectra/internal/spawntrace"
	"github.com/kaeawc/spectra/internal/startup"
	"github.com/kaeawc/spectra/internal/uninstall"
)

// commandSchemasDoc documents each locally owned result schema. Its field
// blocks are the checked-in schema the drift test compares against.
const commandSchemasDoc = "../../docs/reference/command-schemas.md"

var updateCommandSchemas = flag.Bool("update-command-schemas", false, "rewrite the field blocks in docs/reference/command-schemas.md from the Go result types")

// schemaRecord is one JSON value shape a command emits. Most commands emit a
// single record; spawns emits event lines and then a summary line.
type schemaRecord struct {
	name    string
	samples []func() any
}

// resultSchemaTypes maps each locally owned schema to the Go value its
// command encodes. A schema advertised by `capabilities` without an entry
// here fails the test.
var resultSchemaTypes = map[string][]schemaRecord{
	capabilities.SchemaLoad:          single(func() any { return filled[loadReport]() }),
	capabilities.SchemaOrphans:       single(func() any { return filled[orphansReport]() }),
	capabilities.SchemaOwner:         single(func() any { return filled[owner.Result]() }),
	capabilities.SchemaOwnerUnder:    single(func() any { return filled[owner.UnderResult]() }),
	capabilities.SchemaLineage:       single(func() any { return filled[lineage.Result]() }),
	capabilities.SchemaIdle:          single(func() any { return filled[idleReport]() }),
	capabilities.SchemaStartup:       single(func() any { return filled[startup.Report]() }),
	capabilities.SchemaStartupChange: single(func() any { return filled[startup.ChangeResult]() }),
	capabilities.SchemaUninstallPlan: single(func() any { return filled[uninstall.Plan]() }),
	capabilities.SchemaUninstallApply: single(func() any {
		return filled[applyJSON]()
	}),
	capabilities.SchemaSpawns: {
		{name: "event", samples: []func() any{
			func() any { e := filled[spawntrace.Event](); e.Kind = spawntrace.KindStart; return e },
			func() any { e := filled[spawntrace.Event](); e.Kind = spawntrace.KindExit; return e },
		}},
		{name: "summary", samples: []func() any{func() any { return filled[spawntrace.Summary]() }}},
	},
}

func single(sample func() any) []schemaRecord {
	return []schemaRecord{{samples: []func() any{sample}}}
}

// TestCommandResultSchemasMatchDocs fails when a command's JSON shape drifts
// from docs/reference/command-schemas.md. Adding a field: regenerate the doc
// with -update-command-schemas. Removing or renaming a field, or changing its
// JSON type: also bump the schema version in internal/capabilities.
func TestCommandResultSchemasMatchDocs(t *testing.T) {
	data, err := os.ReadFile(commandSchemasDoc)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	documented := parseFieldBlocks(t, doc)
	local := capabilities.LocalSchemas()
	for name := range resultSchemaTypes {
		if _, ok := local[name]; !ok {
			t.Errorf("resultSchemaTypes has %s, which internal/capabilities does not define", name)
		}
	}
	for name := range documented {
		if _, ok := local[name]; !ok {
			t.Errorf("%s documents %s, which internal/capabilities does not define", commandSchemasDoc, name)
		}
	}
	names := sortedKeys(local)
	for _, name := range names {
		records, ok := resultSchemaTypes[name]
		if !ok {
			t.Errorf("schema %s has no Go result type in resultSchemaTypes", name)
			continue
		}
		got := shapeLines(t, records)
		if *updateCommandSchemas {
			doc = replaceFieldBlock(t, doc, name, local[name], got)
			continue
		}
		block, ok := documented[name]
		if !ok {
			t.Errorf("%s has no `<!-- fields: %s v%d -->` block", commandSchemasDoc, name, local[name])
			continue
		}
		if block.version != local[name] {
			t.Errorf("%s documents %s v%d, capabilities declares v%d", commandSchemasDoc, name, block.version, local[name])
		}
		if diff := lineDiff(block.lines, got); diff != "" {
			t.Errorf("%s v%d JSON drifted from %s (re-run with -update-command-schemas after any needed version bump):\n%s",
				name, local[name], commandSchemasDoc, diff)
		}
	}
	if *updateCommandSchemas {
		if err := os.WriteFile(commandSchemasDoc, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// Every JSON interface the manifest advertises must have a documented,
// version-matched schema (protocol-owned schemas are documented elsewhere).
func TestAdvertisedSchemasAreDocumented(t *testing.T) {
	local := capabilities.LocalSchemas()
	for _, goos := range []string{"darwin", "linux"} {
		for _, iface := range capabilities.Build("v", goos, runtime.GOARCH).Interfaces {
			if iface.ResultSchema == nil {
				continue
			}
			if _, ok := protocol.SupportedResultSchemaVersion(iface.ResultSchema.Name); ok {
				continue
			}
			if v, ok := local[iface.ResultSchema.Name]; !ok || v != iface.ResultSchema.Version {
				t.Errorf("%s/%s advertises %v, not a local schema", goos, iface.Name, *iface.ResultSchema)
			}
		}
	}
}

func TestShapeLinesFollowsJSONEncoding(t *testing.T) {
	type inner struct {
		N int `json:"n"`
	}
	type sample struct {
		A    string            `json:"a"`
		B    *float64          `json:"b,omitempty"`
		C    []inner           `json:"c"`
		M    map[string]inner  `json:"m"`
		T    time.Time         `json:"t"`
		Skip string            `json:"-"`
		Any  any               `json:"any"`
		Tags map[string]string `json:"tags,omitempty"`
		inner
	}
	got := shapeLines(t, single(func() any { return filled[sample]() }))
	want := []string{
		"a string", "any null", "b number", "c array", "c[] object", "c[].n number",
		"m object", "m.* object", "m.*.n number", "n number", "t string", "tags object", "tags.* string",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shape = %q\nwant  %q", got, want)
	}
}

type fieldBlock struct {
	version int
	lines   []string
}

var fieldBlockRE = regexp.MustCompile("(?s)<!-- fields: ([a-z0-9_.-]+) v([0-9]+) -->\n```text\n(.*?)```")

func parseFieldBlocks(t *testing.T, doc string) map[string]fieldBlock {
	t.Helper()
	out := map[string]fieldBlock{}
	for _, m := range fieldBlockRE.FindAllStringSubmatch(doc, -1) {
		if _, dup := out[m[1]]; dup {
			t.Errorf("duplicate field block for %s", m[1])
		}
		var v int
		if _, err := fmt.Sscan(m[2], &v); err != nil {
			t.Fatal(err)
		}
		out[m[1]] = fieldBlock{version: v, lines: strings.Split(strings.TrimRight(m[3], "\n"), "\n")}
	}
	return out
}

func replaceFieldBlock(t *testing.T, doc, name string, version int, lines []string) string {
	t.Helper()
	re := regexp.MustCompile("(?s)<!-- fields: " + regexp.QuoteMeta(name) + " v[0-9]+ -->\n```text\n.*?```")
	block := fmt.Sprintf("<!-- fields: %s v%d -->\n```text\n%s\n```", name, version, strings.Join(lines, "\n"))
	if !re.MatchString(doc) {
		t.Fatalf("no field block for %s to update; add `<!-- fields: %s v%d -->` and an empty text fence", name, name, version)
	}
	return re.ReplaceAllLiteralString(doc, block)
}

func lineDiff(want, got []string) string {
	in := func(s []string) map[string]bool {
		m := make(map[string]bool, len(s))
		for _, l := range s {
			m[l] = true
		}
		return m
	}
	w, g := in(want), in(got)
	var b strings.Builder
	for _, l := range want {
		if !g[l] {
			fmt.Fprintf(&b, "  - %s (documented, no longer emitted)\n", l)
		}
	}
	for _, l := range got {
		if !w[l] {
			fmt.Fprintf(&b, "  + %s (emitted, not documented)\n", l)
		}
	}
	return b.String()
}

// shapeLines encodes each sample through encoding/json, so custom
// MarshalJSON methods and tags are honoured, and lists every JSON path with
// its type. Array elements are "[]", map values ".*".
func shapeLines(t *testing.T, records []schemaRecord) []string {
	t.Helper()
	types := map[string]map[string]bool{}
	for _, rec := range records {
		prefix := ""
		if rec.name != "" {
			prefix = rec.name + ": "
		}
		for _, sample := range rec.samples {
			data, err := json.Marshal(sample())
			if err != nil {
				t.Fatal(err)
			}
			var v any
			if err := json.Unmarshal(data, &v); err != nil {
				t.Fatal(err)
			}
			collectShape(types, prefix, "", v)
		}
	}
	var out []string
	for path, set := range types {
		out = append(out, path+" "+strings.Join(sortedKeys(set), "|"))
	}
	sort.Strings(out)
	return out
}

func collectShape(types map[string]map[string]bool, prefix, path string, v any) {
	add := func(p, typ string) {
		if types[prefix+p] == nil {
			types[prefix+p] = map[string]bool{}
		}
		types[prefix+p][typ] = true
	}
	join := func(key string) string {
		if path == "" {
			return key
		}
		return path + "." + key
	}
	switch x := v.(type) {
	case map[string]any:
		if path != "" {
			add(path, "object")
		}
		for k, child := range x {
			key := k
			if strings.HasPrefix(k, mapKeyMarker) {
				key = "*"
			}
			collectShape(types, prefix, join(key), child)
		}
	case []any:
		add(path, "array")
		for _, child := range x {
			collectShape(types, prefix, path+"[]", child)
		}
	case string:
		add(path, "string")
	case float64:
		add(path, "number")
	case bool:
		add(path, "bool")
	case nil:
		add(path, "null")
	}
}

// mapKeyMarker prefixes every map key filled() generates, so the shape
// records map values as ".*" instead of a made-up key.
const mapKeyMarker = "__map_key__"

func filled[T any]() T {
	var v T
	fillValue(reflect.ValueOf(&v).Elem(), map[reflect.Type]int{})
	return v
}

var timeType = reflect.TypeOf(time.Time{})

// fillValue sets every reachable field to a non-zero value so omitempty
// fields appear. A recursive type is expanded twice, which shows its nested
// shape once without looping.
func fillValue(v reflect.Value, seen map[reflect.Type]int) {
	if !v.CanSet() {
		return
	}
	t := v.Type()
	if seen[t] >= 2 {
		return
	}
	seen[t]++
	defer func() { seen[t]-- }()
	switch t.Kind() {
	case reflect.Pointer:
		p := reflect.New(t.Elem())
		fillValue(p.Elem(), seen)
		v.Set(p)
	case reflect.Struct:
		fillStruct(v, t)
		if t != timeType {
			for i := 0; i < v.NumField(); i++ {
				fillValue(v.Field(i), seen)
			}
		}
	case reflect.Slice:
		if seen[t.Elem()] >= 2 {
			v.Set(reflect.MakeSlice(t, 0, 0))
			return
		}
		s := reflect.MakeSlice(t, 1, 1)
		fillValue(s.Index(0), seen)
		v.Set(s)
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			fillValue(v.Index(i), seen)
		}
	case reflect.Map:
		fillMap(v, t, seen)
	default:
		fillScalar(v)
	}
}

func fillStruct(v reflect.Value, t reflect.Type) {
	if t == timeType {
		v.Set(reflect.ValueOf(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)))
	}
}

func fillMap(v reflect.Value, t reflect.Type, seen map[reflect.Type]int) {
	m := reflect.MakeMap(t)
	k := reflect.New(t.Key()).Elem()
	if t.Key().Kind() == reflect.String {
		k.SetString(mapKeyMarker)
	} else {
		fillValue(k, seen)
	}
	val := reflect.New(t.Elem()).Elem()
	fillValue(val, seen)
	m.SetMapIndex(k, val)
	v.Set(m)
}

func fillScalar(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1.5)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
