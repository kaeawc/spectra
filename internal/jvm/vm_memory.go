package jvm

import (
	"fmt"
	"strings"
)

// DiagnosticSection is one JVM diagnostic command result. Command failures are
// captured per section because older JVMs or disabled features such as native
// memory tracking should not hide the sections that did succeed.
type DiagnosticSection struct {
	Command []string `json:"command"`
	Output  string   `json:"output,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// VMMemoryDiagnostics groups VM-internal memory views that are deeper than
// process RSS: Java heap layout, metaspace, native memory tracking,
// classloader metadata, and code cache state.
type VMMemoryDiagnostics struct {
	PID              int               `json:"pid"`
	HeapInfo         DiagnosticSection `json:"heap_info"`
	Metaspace        DiagnosticSection `json:"metaspace"`
	NativeMemory     DiagnosticSection `json:"native_memory"`
	ClassLoaderStats DiagnosticSection `json:"classloader_stats"`
	CodeCache        DiagnosticSection `json:"code_cache"`
	CodeHeap         DiagnosticSection `json:"code_heap,omitempty"`
}

// CollectVMMemoryDiagnostics collects VM-internal memory diagnostics through
// HotSpot diagnostic commands. It returns one result even when some sections
// fail so callers can still explore whatever the target JVM exposes.
func CollectVMMemoryDiagnostics(pid int, run CmdRunner) VMMemoryDiagnostics {
	if run == nil {
		run = DefaultRunner
	}
	return VMMemoryDiagnostics{
		PID:              pid,
		HeapInfo:         runDiagnostic(pid, run, "GC.heap_info"),
		Metaspace:        runDiagnostic(pid, run, "VM.metaspace"),
		NativeMemory:     runDiagnostic(pid, run, "VM.native_memory", "summary"),
		ClassLoaderStats: runDiagnostic(pid, run, "VM.classloader_stats"),
		CodeCache:        runDiagnostic(pid, run, "Compiler.codecache"),
		CodeHeap:         runDiagnostic(pid, run, "Compiler.CodeHeap_Analytics"),
	}
}

// JMXStatus reports the target JVM's management-agent state. When local JMX is
// enabled, HotSpot prints the local connector URL used by tools such as
// jconsole and VisualVM.
func JMXStatus(pid int, run CmdRunner) ([]byte, error) {
	if run == nil {
		run = DefaultRunner
	}
	return run("jcmd", fmt.Sprint(pid), "ManagementAgent.status")
}

// JMXStartLocal starts the target JVM's local management agent so JMX clients
// can browse live MBeans through the same local connector used by jconsole.
func JMXStartLocal(pid int, run CmdRunner) ([]byte, error) {
	if run == nil {
		run = DefaultRunner
	}
	return run("jcmd", fmt.Sprint(pid), "ManagementAgent.start_local")
}

func runDiagnostic(pid int, run CmdRunner, command string, args ...string) DiagnosticSection {
	jcmdArgs := append([]string{fmt.Sprint(pid), command}, args...)
	section := DiagnosticSection{Command: append([]string{"jcmd"}, jcmdArgs...)}
	out, err := run("jcmd", jcmdArgs...)
	if err != nil {
		section.Error = err.Error()
		return section
	}
	section.Output = strings.TrimRight(string(out), "\n")
	return section
}
