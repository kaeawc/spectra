// Package runtimeattach identifies the language runtime of a live process and
// routes to the diagnostics available for it. It only classifies and advises —
// it never signals a process, attaches, or starts a profiler.
package runtimeattach

import (
	"debug/buildinfo"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Runtime is a coarse language-runtime classification of a process.
type Runtime string

const (
	RuntimeJVM      Runtime = "jvm"
	RuntimeNode     Runtime = "node"
	RuntimeElectron Runtime = "electron"
	RuntimeGo       Runtime = "go"
	RuntimeDotNet   Runtime = "dotnet"
	RuntimePython   Runtime = "python"
	RuntimeNative   Runtime = "native"
	RuntimeUnknown  Runtime = "unknown"
)

// Process is the minimal identity runtimeattach classifies.
type Process struct {
	PID            int
	Command        string // short comm / executable base name
	ExecutablePath string
	CommandLine    string // full argv joined
}

// Capability is one diagnostic action available for a runtime.
type Capability struct {
	Name string `json:"name"`
	How  string `json:"how"`
}

// Result is the classification and routing for one process.
type Result struct {
	PID            int          `json:"pid"`
	Command        string       `json:"command,omitempty"`
	ExecutablePath string       `json:"executable_path,omitempty"`
	Runtime        Runtime      `json:"runtime"`
	Evidence       string       `json:"evidence,omitempty"`
	Capabilities   []Capability `json:"capabilities"`
}

// Probes are the injectable checks that require touching the filesystem, so
// classification stays a pure function in tests.
type Probes struct {
	// GoBinary reports whether the executable at path is a Go binary.
	GoBinary func(path string) bool
	// DotnetSocket reports whether a .NET diagnostic IPC socket exists for pid.
	DotnetSocket func(pid int) bool
}

// DefaultProbes fingerprints Go binaries via the embedded build info and looks
// for a .NET diagnostic socket in the temp directory.
func DefaultProbes() Probes {
	return Probes{
		GoBinary: func(path string) bool {
			if path == "" {
				return false
			}
			_, err := buildinfo.ReadFile(path)
			return err == nil
		},
		DotnetSocket: func(pid int) bool {
			matches, _ := filepath.Glob(filepath.Join(os.TempDir(), fmt.Sprintf("dotnet-diagnostic-%d-*-socket", pid)))
			return len(matches) > 0
		},
	}
}

// Classify determines a process's runtime and the diagnostics available for it.
func Classify(p Process, probes Probes) Result {
	runtime, evidence := detect(p, probes)
	return Result{
		PID:            p.PID,
		Command:        p.Command,
		ExecutablePath: p.ExecutablePath,
		Runtime:        runtime,
		Evidence:       evidence,
		Capabilities:   capabilities(runtime, p.PID),
	}
}

func detect(p Process, probes Probes) (Runtime, string) {
	hay := strings.ToLower(p.ExecutablePath + " " + p.CommandLine + " " + p.Command)
	base := strings.ToLower(filepath.Base(p.ExecutablePath))

	switch {
	case probes.DotnetSocket != nil && probes.DotnetSocket(p.PID):
		return RuntimeDotNet, "a .NET diagnostic socket exists for this pid"
	case isDotNet(base, hay):
		return RuntimeDotNet, "executable is the .NET/Mono runtime"
	case isJVM(base, hay):
		return RuntimeJVM, "java executable or JVM launch flags"
	case isElectron(hay):
		return RuntimeElectron, "Electron/Chromium framework or child-process type flag"
	case isNode(base, hay):
		return RuntimeNode, "node executable or --inspect flag"
	case isPython(base, hay):
		return RuntimePython, "python executable"
	case probes.GoBinary != nil && probes.GoBinary(p.ExecutablePath):
		return RuntimeGo, "executable carries Go build info"
	default:
		return RuntimeNative, "no managed-runtime signal; treated as a native binary"
	}
}

func isDotNet(base, hay string) bool {
	return base == "dotnet" || strings.Contains(hay, "/dotnet ") || strings.Contains(hay, "mono-sgen")
}

func isJVM(base, hay string) bool {
	return base == "java" || strings.Contains(hay, "javavirtualmachines") ||
		strings.Contains(hay, "-xx") || strings.Contains(hay, "-jar ")
}

func isElectron(hay string) bool {
	return strings.Contains(hay, "electron") ||
		strings.Contains(hay, "--type=renderer") || strings.Contains(hay, "--type=gpu-process") ||
		strings.Contains(hay, "--type=utility")
}

func isNode(base, hay string) bool {
	return base == "node" || strings.Contains(hay, "/node ") || strings.Contains(hay, "--inspect")
}

func isPython(base, hay string) bool {
	return strings.Contains(base, "python") || strings.Contains(hay, "python3")
}

// capabilities returns the diagnostics available for a runtime, most specific
// first, always ending with the universal CPU sample.
func capabilities(r Runtime, pid int) []Capability {
	sample := Capability{"user-space CPU sample", fmt.Sprintf("spectra sample %d", pid)}
	switch r {
	case RuntimeJVM:
		return []Capability{
			{"thread dump, JFR, native memory", fmt.Sprintf("spectra jvm %d  (jcmd Thread.print, JFR.start, VM.native_memory)", pid)},
			sample,
		}
	case RuntimeElectron:
		return []Capability{
			{"renderer/GPU process topology", "spectra web processes"},
			{"open the V8 inspector", fmt.Sprintf("kill -USR1 %d, then connect a CDP client to 127.0.0.1:9229", pid)},
			sample,
		}
	case RuntimeNode:
		return []Capability{
			{"open the V8 inspector", fmt.Sprintf("kill -USR1 %d, then connect a CDP client to 127.0.0.1:9229", pid)},
			sample,
		}
	case RuntimeGo:
		return []Capability{
			{"full goroutine dump", fmt.Sprintf("kill -QUIT %d  (if SIGQUIT is not trapped)", pid)},
			{"pprof, if net/http/pprof is imported", "curl http://127.0.0.1:<port>/debug/pprof/goroutine?debug=2"},
			sample,
		}
	case RuntimeDotNet:
		return []Capability{
			{"traces, dumps, GC heap", fmt.Sprintf("dotnet-trace / dotnet-dump / dotnet-gcdump against pid %d via its diagnostic socket", pid)},
			sample,
		}
	case RuntimePython:
		return []Capability{
			{"native stack sample", fmt.Sprintf("py-spy dump --pid %d", pid)},
			sample,
		}
	default:
		return []Capability{
			{"attach a native debugger", fmt.Sprintf("lldb -p %d  (or spindump for a whole-system stack)", pid)},
			sample,
		}
	}
}
