// Package plistread provides typed, read-only access to macOS property lists.
package plistread

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"howett.net/plist"
)

// ErrNeedsFullDiskAccess marks preference reads that macOS commonly denies
// until the caller's terminal or helper has Full Disk Access.
var ErrNeedsFullDiskAccess = errors.New("needs full disk access")

// FullDiskAccessRemediation is suitable for surfacing in CLI error output.
const FullDiskAccessRemediation = "Grant Full Disk Access to your terminal in System Settings -> Privacy & Security -> Full Disk Access."

// Kind is the concrete plist value kind represented by Value.
type Kind int

const (
	Invalid Kind = iota
	String
	Int
	Float
	Bool
	Date
	Data
	Array
	Dict
)

// Value is a normalized plist value. Exactly one value field is meaningful,
// selected by Kind.
type Value struct {
	Kind   Kind
	String string
	Int    int64
	Float  float64
	Bool   bool
	Date   time.Time
	Data   []byte
	Array  []Value
	Dict   map[string]Value
}

// Domain is a decoded plist file plus basic file metadata.
type Domain struct {
	Path  string
	Mtime time.Time
	Size  int64
	Root  Value
}

type fileReader interface {
	Stat(path string) (fs.FileInfo, error)
	ReadFile(path string) ([]byte, error)
	Glob(pattern string) ([]string, error)
}

type osReader struct{}

func (osReader) Stat(path string) (fs.FileInfo, error) { return os.Stat(path) }
func (osReader) ReadFile(path string) ([]byte, error)  { return os.ReadFile(path) }
func (osReader) Glob(pattern string) ([]string, error) { return filepath.Glob(pattern) }
func preferenceRoot() string                           { return "/Library/Preferences" }
func byHostPreferenceRoot(root string) string          { return filepath.Join(root, "ByHost") }
func plistPathForDomain(root, domain string) string    { return filepath.Join(root, domain+".plist") }
func currentHostPattern(root, domain string) string {
	return filepath.Join(byHostPreferenceRoot(root), domain+".*.plist")
}
func domainLooksLikePath(domain string) bool {
	return filepath.IsAbs(domain) || strings.HasSuffix(domain, ".plist")
}
func cleanDomainPath(domain string) string              { return filepath.Clean(domain) }
func mapPreferenceReadErr(path string, err error) error { return mapReadErr(path, err) }
func needsFullDiskAccessPath(path string) bool {
	return strings.HasPrefix(filepath.Clean(path), preferenceRoot()+string(os.PathSeparator))
}

// ReadPath reads and decodes a plist file.
func ReadPath(path string) (Domain, error) {
	return readPathWithReader(path, osReader{})
}

func readPathWithReader(path string, files fileReader) (Domain, error) {
	info, err := files.Stat(path)
	if err != nil {
		return Domain{}, mapReadErr(path, err)
	}
	data, err := files.ReadFile(path)
	if err != nil {
		return Domain{}, mapReadErr(path, err)
	}
	root, err := decode(data)
	if err != nil {
		return Domain{}, fmt.Errorf("decode plist %s: %w", path, err)
	}
	return Domain{
		Path:  path,
		Mtime: info.ModTime(),
		Size:  info.Size(),
		Root:  root,
	}, nil
}

// ReadKey reads one top-level or dotted dictionary key from a plist file.
func ReadKey(path, key string) (Value, error) {
	d, err := ReadPath(path)
	if err != nil {
		return Value{}, err
	}
	v, ok := d.Root.Lookup(key)
	if !ok {
		return Value{}, fmt.Errorf("plist key %q in %s: %w", key, path, fs.ErrNotExist)
	}
	return v, nil
}

// ResolveDomain returns the standard macOS preference plist path for a domain.
//
// Absolute paths and strings ending in ".plist" are returned unchanged. For
// normal domains, currentHost=false maps to /Library/Preferences/<domain>.plist.
// currentHost=true resolves the first existing ByHost match when present, then
// falls back to the unsuffixed ByHost path.
func ResolveDomain(domain string, currentHost bool) (string, error) {
	return resolveDomainWithReader(domain, currentHost, osReader{})
}

func resolveDomainWithReader(domain string, currentHost bool, files fileReader) (string, error) {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return "", fmt.Errorf("resolve domain: empty domain")
	}
	if domainLooksLikePath(domain) {
		return cleanDomainPath(domain), nil
	}
	root := preferenceRoot()
	if !currentHost {
		return plistPathForDomain(root, domain), nil
	}
	matches, err := files.Glob(currentHostPattern(root, domain))
	if err != nil {
		return "", fmt.Errorf("resolve ByHost domain %q: %w", domain, err)
	}
	if len(matches) > 0 {
		return cleanDomainPath(matches[0]), nil
	}
	return plistPathForDomain(byHostPreferenceRoot(root), domain), nil
}

// Lookup returns a dictionary value by dotted path.
func (v Value) Lookup(path string) (Value, bool) {
	if path == "" {
		return v, true
	}
	if v.Kind == Dict {
		if exact, ok := v.Dict[path]; ok {
			return exact, true
		}
	}
	cur := v
	for _, part := range strings.Split(path, ".") {
		if cur.Kind != Dict || part == "" {
			return Value{}, false
		}
		next, ok := cur.Dict[part]
		if !ok {
			return Value{}, false
		}
		cur = next
	}
	return cur, true
}

// Any converts Value back into ordinary Go plist-shaped data.
func (v Value) Any() any {
	switch v.Kind {
	case String:
		return v.String
	case Int:
		return v.Int
	case Float:
		return v.Float
	case Bool:
		return v.Bool
	case Date:
		return v.Date
	case Data:
		return append([]byte(nil), v.Data...)
	case Array:
		out := make([]any, 0, len(v.Array))
		for _, elem := range v.Array {
			out = append(out, elem.Any())
		}
		return out
	case Dict:
		out := make(map[string]any, len(v.Dict))
		for k, elem := range v.Dict {
			out[k] = elem.Any()
		}
		return out
	default:
		return nil
	}
}

func decode(data []byte) (Value, error) {
	var raw any
	if _, err := plist.Unmarshal(data, &raw); err != nil {
		return Value{}, err
	}
	return valueFromAny(raw)
}

func valueFromAny(raw any) (Value, error) {
	if v, ok, err := scalarValueFromAny(raw); ok || err != nil {
		return v, err
	}
	switch v := raw.(type) {
	case []any:
		return arrayValue(v)
	case map[string]any:
		return stringMapValue(v)
	case map[any]any:
		return anyMapValue(v)
	default:
		return Value{}, fmt.Errorf("unsupported plist value %T", raw)
	}
}

func scalarValueFromAny(raw any) (Value, bool, error) {
	switch v := raw.(type) {
	case string:
		return Value{Kind: String, String: v}, true, nil
	case bool:
		return Value{Kind: Bool, Bool: v}, true, nil
	case time.Time:
		return Value{Kind: Date, Date: v}, true, nil
	case []byte:
		return Value{Kind: Data, Data: append([]byte(nil), v...)}, true, nil
	}
	rv := reflect.ValueOf(raw)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return Value{Kind: Int, Int: rv.Int()}, true, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v, err := uintValue(rv.Uint())
		return v, true, err
	case reflect.Float32, reflect.Float64:
		return Value{Kind: Float, Float: rv.Convert(reflect.TypeOf(float64(0))).Float()}, true, nil
	default:
		return Value{}, false, nil
	}
}

func arrayValue(values []any) (Value, error) {
	out := make([]Value, 0, len(values))
	for i, elem := range values {
		converted, err := valueFromAny(elem)
		if err != nil {
			return Value{}, fmt.Errorf("array[%d]: %w", i, err)
		}
		out = append(out, converted)
	}
	return Value{Kind: Array, Array: out}, nil
}

func stringMapValue(values map[string]any) (Value, error) {
	out := make(map[string]Value, len(values))
	for k, elem := range values {
		converted, err := valueFromAny(elem)
		if err != nil {
			return Value{}, fmt.Errorf("dict[%s]: %w", k, err)
		}
		out[k] = converted
	}
	return Value{Kind: Dict, Dict: out}, nil
}

func anyMapValue(values map[any]any) (Value, error) {
	out := make(map[string]Value, len(values))
	for k, elem := range values {
		key, ok := k.(string)
		if !ok {
			return Value{}, fmt.Errorf("dict key %T: not a string", k)
		}
		converted, err := valueFromAny(elem)
		if err != nil {
			return Value{}, fmt.Errorf("dict[%s]: %w", key, err)
		}
		out[key] = converted
	}
	return Value{Kind: Dict, Dict: out}, nil
}

func uintValue(v uint64) (Value, error) {
	if v > math.MaxInt64 {
		return Value{}, fmt.Errorf("integer %d overflows int64", v)
	}
	return Value{Kind: Int, Int: int64(v)}, nil
}

func mapReadErr(path string, err error) error {
	if err == nil {
		return nil
	}
	if needsFullDiskAccessPath(path) && isPermissionErr(err) {
		return fmt.Errorf("%s: %w: %w", path, ErrNeedsFullDiskAccess, err)
	}
	return err
}

func isPermissionErr(err error) bool {
	return errors.Is(err, fs.ErrPermission) ||
		errors.Is(err, syscall.EACCES) ||
		errors.Is(err, syscall.EPERM)
}
