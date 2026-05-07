package detect

import (
	"path/filepath"
	"sort"
	"strings"
)

type rustMarkerScanner interface {
	scanMarkers(path string) binaryMarkers
}

type rustLinker interface {
	linkedLibs(path string) []string
}

type rustSidecarResolver interface {
	sidecars(path string, libs []string) []string
}

type defaultRustInspectorDeps struct{}

func (defaultRustInspectorDeps) scanMarkers(path string) binaryMarkers {
	return scanBinaryMarkers(path)
}

func (defaultRustInspectorDeps) linkedLibs(path string) []string {
	return otoolL(path)
}

func (defaultRustInspectorDeps) sidecars(path string, libs []string) []string {
	var out []string
	for _, lib := range libs {
		if !strings.HasPrefix(lib, "@rpath/") {
			continue
		}
		sib := filepath.Join(filepath.Dir(path), strings.TrimPrefix(lib, "@rpath/"))
		if exists(sib) {
			out = append(out, sib)
		}
	}
	return out
}

type rustInspector struct {
	markers rustMarkerScanner
	links   rustLinker
	sidecar rustSidecarResolver
}

func newRustInspector(markers rustMarkerScanner, links rustLinker, sidecar rustSidecarResolver) rustInspector {
	deps := defaultRustInspectorDeps{}
	if markers == nil {
		markers = deps
	}
	if links == nil {
		links = deps
	}
	if sidecar == nil {
		sidecar = deps
	}
	return rustInspector{markers: markers, links: links, sidecar: sidecar}
}

func inspectRustApp(appPath, exe string, r *Result) *RustInspection {
	ins := newRustInspector(nil, nil, nil).inspect(appPath, exe, r)
	if ins == nil || ins.Kind == "none" {
		return nil
	}
	return ins
}

func (i rustInspector) inspect(appPath, exe string, r *Result) *RustInspection {
	ins := &RustInspection{Kind: "none"}
	if exe != "" && (r.Runtime == "Rust" || r.UI == "Tauri") {
		ins.Kind = rustInspectionKind(r)
		ins.PrimaryBinary = relToBundle(appPath, exe)
		markers := i.markers.scanMarkers(exe)
		ins.PanicStringHits = markers.rustHits
		ins.LinkedFrameworks = notableRustFrameworks(i.links.linkedLibs(exe))
	}

	for _, mod := range r.NativeModules {
		if mod.Language != "Rust" {
			continue
		}
		ins.NativeModules = append(ins.NativeModules, mod.Path)
		if ins.Kind == "none" {
			ins.Kind = "electron-native-module"
		}
	}

	for _, path := range rustModulePaths(appPath, r.NativeModules) {
		libs := i.links.linkedLibs(path)
		for _, sidecar := range i.sidecar.sidecars(path, libs) {
			if i.markers.scanMarkers(sidecar).rustHits >= 50 {
				ins.Sidecars = append(ins.Sidecars, relToBundle(appPath, sidecar))
			}
		}
	}

	dedupeStrings(&ins.LinkedFrameworks)
	dedupeStrings(&ins.NativeModules)
	dedupeStrings(&ins.Sidecars)
	ins.FollowUps = rustFollowUps(ins)
	if ins.Kind == "none" {
		return nil
	}
	return ins
}

func rustInspectionKind(r *Result) string {
	switch r.UI {
	case "Tauri":
		return "tauri"
	case "Electron":
		return "electron-native-module"
	default:
		return "native"
	}
}

func notableRustFrameworks(libs []string) []string {
	var out []string
	for _, lib := range libs {
		for _, fw := range []string{"AppKit", "WebKit", "Metal", "CoreGraphics", "Security", "Network"} {
			if strings.Contains(lib, "/"+fw+".framework/") {
				out = append(out, fw)
			}
		}
	}
	return out
}

func rustModulePaths(appPath string, mods []NativeModule) []string {
	var out []string
	for _, mod := range mods {
		if mod.Language != "Rust" || mod.Path == "" {
			continue
		}
		out = append(out, filepath.Join(appPath, mod.Path))
	}
	return out
}

func rustFollowUps(ins *RustInspection) []string {
	switch ins.Kind {
	case "tauri":
		return []string{"inspect WebKit usage", "inspect custom protocols", "inspect local resources", "inspect network endpoints"}
	case "electron-native-module":
		return []string{"inspect native modules", "inspect sidecar dylibs", "inspect linked Apple frameworks"}
	case "native":
		return []string{"inspect code signing", "inspect entitlements", "inspect helpers", "inspect storage", "inspect network endpoints"}
	default:
		return nil
	}
}

func relToBundle(appPath, path string) string {
	rel, err := filepath.Rel(appPath, path)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return path
	}
	return rel
}

func dedupeStrings(values *[]string) {
	if len(*values) == 0 {
		return
	}
	sort.Strings(*values)
	out := (*values)[:0]
	var last string
	for i, value := range *values {
		if i > 0 && value == last {
			continue
		}
		out = append(out, value)
		last = value
	}
	*values = out
}
