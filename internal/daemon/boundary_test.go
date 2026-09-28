package daemon

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const daemonImport = "github.com/kaeawc/spectra/internal/daemon"

var allowedNetNames = map[string]bool{
	"Conn": true, "Listener": true, "UnixConn": true, "UnixAddr": true,
	"UnixListener": true, "Addr": true, "Error": true, "OpError": true,
	"Dialer": true, "ListenConfig": true, "ErrClosed": true,
}

var netCalls = map[string]bool{
	"Listen": true, "Dial": true, "DialTimeout": true,
	"ListenUnix": true, "DialUnix": true,
}

func TestLocalTransportBoundary(t *testing.T) {
	packages := map[string][]*ast.File{}
	importsDaemon := map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir("../..", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "vendor" || path == "../../cmd" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		dir := filepath.Dir(path)
		packages[dir] = append(packages[dir], file)
		for _, imp := range file.Imports {
			name, _ := strconv.Unquote(imp.Path.Value)
			importsDaemon[dir] = importsDaemon[dir] || name == daemonImport
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for dir, files := range packages {
		if dir != "../../internal/daemon" && dir != "../../internal/daemonclient" && dir != "../../internal/peercred" && !importsDaemon[dir] {
			continue
		}
		for _, file := range files {
			checked++
			for _, problem := range checkLocalTransport(file, fset) {
				t.Error(problem)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no local transport files checked")
	}
}

func checkLocalTransport(file *ast.File, fset *token.FileSet) []error {
	var problems []error
	aliases := map[string]bool{}
	for _, imp := range file.Imports {
		name, _ := strconv.Unquote(imp.Path.Value)
		if forbiddenTransportImport(name) {
			problems = append(problems, fmt.Errorf("%s: forbidden import %s", fset.Position(imp.Pos()), name))
		}
		if name != "net" {
			continue
		}
		alias := "net"
		if imp.Name != nil {
			alias = imp.Name.Name
		}
		if alias == "." || alias == "_" {
			problems = append(problems, fmt.Errorf("%s: net import must be named", fset.Position(imp.Pos())))
		} else {
			aliases[alias] = true
		}
	}
	allowedCalls := map[*ast.SelectorExpr]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, packageName := sel.X.(*ast.Ident)
		packageCall := packageName && aliases[pkg.Name]
		if packageCall && netCalls[sel.Sel.Name] {
			if unixLiteral(call.Args, 0) {
				allowedCalls[sel] = true
			}
		}
		// Treat these method names conservatively, including values obtained from helpers.
		if !packageCall && (sel.Sel.Name == "DialContext" || sel.Sel.Name == "Listen") {
			if unixLiteral(call.Args, 1) {
				allowedCalls[sel] = true
			}
		}
		return true
	})
	ast.Inspect(file, func(node ast.Node) bool {
		sel, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && aliases[pkg.Name] {
			if !allowedNetNames[sel.Sel.Name] && !allowedCalls[sel] {
				problems = append(problems, fmt.Errorf("%s: disallowed net.%s", fset.Position(sel.Pos()), sel.Sel.Name))
			}
		}
		if (sel.Sel.Name == "DialContext" || sel.Sel.Name == "Listen") && !allowedCalls[sel] {
			problems = append(problems, fmt.Errorf("%s: non-unix or indirect net method %s", fset.Position(sel.Pos()), sel.Sel.Name))
		}
		return true
	})
	return problems
}

func unixLiteral(args []ast.Expr, index int) bool {
	if len(args) <= index {
		return false
	}
	literal, ok := args[index].(*ast.BasicLit)
	return ok && literal.Kind == token.STRING && literal.Value == `"unix"`
}

func forbiddenTransportImport(name string) bool {
	for _, exact := range []string{"net/http", "net/rpc", "net/smtp", "crypto/tls"} {
		if name == exact || strings.HasPrefix(name, exact+"/") {
			return true
		}
	}
	return strings.HasPrefix(name, "tailscale.com/") || name == "tailscale.com" || strings.HasPrefix(name, "github.com/slackhq/nebula/") || name == "github.com/slackhq/nebula"
}

func TestLocalTransportBypassCases(t *testing.T) {
	cases := []struct {
		name string
		src  string
		fail bool
	}{
		{"tcp", `package p; import "net"; func f(){net.ListenTCP("tcp", nil)}`, true},
		{"udp", `package p; import "net"; func f(){net.ListenUDP("udp", nil)}`, true},
		{"ip", `package p; import "net"; func f(){net.ListenIP("ip", nil)}`, true},
		{"packet", `package p; import "net"; func f(){net.ListenPacket("udp", "")}`, true},
		{"listen second unix", `package p; import "net"; func f(){net.Listen("tcp", "unix")}`, true},
		{"unixgram", `package p; import "net"; func f(){net.DialUnix("unixgram", nil, nil)}`, true},
		{"dialer", `package p; import "net"; func f(){(&net.Dialer{}).DialContext(nil, "tcp", "")}`, true},
		{"listenconfig", `package p; import "net"; func f(){(&net.ListenConfig{}).Listen(nil, "tcp", "")}`, true},
		{"alias", `package p; import stdnet "net"; func f(){stdnet.Dial("tcp", "")}`, true},
		{"value", `package p; import "net"; var f = net.Listen`, true},
		{"method value", `package p; import "net"; var f = (&net.Dialer{}).DialContext`, true},
		{"variable method", `package p; import "net"; func f(){d := net.Dialer{}; d.DialContext(nil, "tcp", "")}`, true},
		{"parameter method", `package p; import "net"; func f(d *net.Dialer){d.DialContext(nil, "tcp", "")}`, true},
		{"dot import", `package p; import . "net"`, true},
		{"forbidden import", `package p; import "net/rpc"`, true},
		{"allowed", `package p; import n "net"; func f(){n.Listen("unix", ""); (&n.Dialer{}).DialContext(nil, "unix", "")}; var _ n.Conn`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, tc.name+".go", tc.src, 0)
			if err != nil {
				t.Fatal(err)
			}
			problems := checkLocalTransport(file, fset)
			if (len(problems) > 0) != tc.fail {
				t.Fatalf("problems: %v", problems)
			}
		})
	}
}
