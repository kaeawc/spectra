// Command plutil is a minimal stand-in for the macOS plutil utility, used
// only by the Darling CI job (scripts/darling-ci.sh). Darling does not ship
// plutil, so the job cross-compiles this shim for darwin and prepends it to
// PATH inside the Darling prefix. It is never part of macOS builds: the
// macOS CI job and scripts/dist.sh use the real system plutil and do not
// build or package this directory.
//
// Supported invocations (the subset Spectra shells out to):
//
//	plutil -convert json -o - <path>
//	plutil -convert xml1 -o - <path>
//	plutil -extract <key> raw [-o -] <path>
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"

	"howett.net/plist"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "plutil shim: no arguments")
		return 2
	}
	var out []byte
	var err error
	switch args[0] {
	case "-convert":
		out, err = convert(args[1:])
	case "-extract":
		out, err = extract(args[1:])
	default:
		err = fmt.Errorf("unsupported mode %q", args[0])
	}
	if err != nil {
		fmt.Fprintf(stderr, "plutil shim: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s\n", out)
	return 0
}

// convert handles `-convert <json|xml1> -o - <path>`.
func convert(args []string) ([]byte, error) {
	if len(args) != 4 || args[1] != "-o" || args[2] != "-" {
		return nil, fmt.Errorf("usage: -convert <json|xml1> -o - <path>")
	}
	format, path := args[0], args[3]
	root, err := readPlist(path)
	if err != nil {
		return nil, err
	}
	switch format {
	case "json":
		// Compact JSON like the real plutil, which also requires a
		// collection at the root. howett decodes dates and data blobs to
		// time.Time and []byte, which encoding/json renders as RFC 3339
		// strings and base64 — a lenient superset of plutil, which refuses
		// to convert those types.
		switch root.(type) {
		case map[string]any, []any:
			return json.Marshal(root)
		default:
			return nil, fmt.Errorf("%s: invalid object in plist for destination format", path)
		}
	case "xml1":
		return plist.Marshal(root, plist.XMLFormat)
	default:
		return nil, fmt.Errorf("unsupported conversion format %q", format)
	}
}

// extract handles `-extract <key> raw [-o -] <path>`. Only top-level keys
// and the raw output format are supported.
func extract(args []string) ([]byte, error) {
	if len(args) == 5 && args[2] == "-o" && args[3] == "-" {
		args = []string{args[0], args[1], args[4]}
	}
	if len(args) != 3 || args[1] != "raw" {
		return nil, fmt.Errorf("usage: -extract <key> raw [-o -] <path>")
	}
	key, path := args[0], args[2]
	root, err := readPlist(path)
	if err != nil {
		return nil, err
	}
	dict, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: root is not a dictionary", path)
	}
	v, ok := dict[key]
	if !ok {
		return nil, fmt.Errorf("%s: no value at key path %q", path, key)
	}
	return rawScalar(v)
}

func readPlist(path string) (any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var root any
	if _, err := plist.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return root, nil
}

// rawScalar formats a value the way `plutil -extract ... raw` prints
// scalars: strings bare, booleans as true/false, numbers undecorated.
func rawScalar(v any) ([]byte, error) {
	switch t := v.(type) {
	case string:
		return []byte(t), nil
	case bool:
		return []byte(strconv.FormatBool(t)), nil
	case uint64:
		return []byte(strconv.FormatUint(t, 10)), nil
	case int64:
		return []byte(strconv.FormatInt(t, 10)), nil
	case float64:
		return []byte(strconv.FormatFloat(t, 'g', -1, 64)), nil
	default:
		return nil, fmt.Errorf("unsupported raw value type %T", v)
	}
}
