package daemon

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLocalTransportBoundary(t *testing.T) {
	for _, dir := range []string{".", "../daemonclient"} {
		files, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range files {
			if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range file.Imports {
				name, _ := strconv.Unquote(imp.Path.Value)
				for _, forbidden := range []string{"net/http", "crypto/tls", "tailscale.com", "slackhq/nebula"} {
					if strings.Contains(name, forbidden) {
						t.Errorf("%s imports %s", path, name)
					}
				}
			}
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "net" {
					return true
				}
				if sel.Sel.Name != "Listen" && sel.Sel.Name != "Dial" && sel.Sel.Name != "DialTimeout" {
					return true
				}
				if len(call.Args) == 0 {
					t.Errorf("%s: missing network argument", path)
					return true
				}
				literal, ok := call.Args[0].(*ast.BasicLit)
				if !ok || literal.Value != `"unix"` {
					t.Errorf("%s: non-unix network", path)
				}
				return true
			})
		}
	}
}
