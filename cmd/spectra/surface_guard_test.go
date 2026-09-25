package main

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNoRemoteCommandSurface(t *testing.T) {
	forbidden := map[string]bool{
		"serve": true, "connect": true, "remote": true, "agent": true,
		"daemon": true, "daemon-install": true, "mesh": true, "jobs": true,
		"tsnet": true, "nebula": true, "tunnel": true, "proxy": true,
	}
	for _, command := range subcommandList() {
		if forbidden[command.name] {
			t.Errorf("remote command %q returned to core", command.name)
		}
	}
}

func TestNoRemoteImports(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{"tailscale.com", "slackhq/nebula", "github.com/kaeawc/spectra-proxy", "/internal/serve", "/internal/rpc"}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == "testdata" || entry.Name() == "vendor" || entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		for _, imp := range file.Imports {
			importPath, unquoteErr := strconv.Unquote(imp.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			for _, fragment := range forbidden {
				if strings.Contains(importPath, fragment) {
					t.Errorf("remote import %q in %s", importPath, path)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk Go imports: %v", err)
	}
}
