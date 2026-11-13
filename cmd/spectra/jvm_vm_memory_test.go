package main

import (
	"testing"

	"github.com/kaeawc/spectra/internal/jvm"
)

func TestRunJVMVMMemoryExitStatus(t *testing.T) {
	failing := jvm.DiagnosticSection{Error: "exit status 1"}
	cases := []struct {
		name string
		diag jvm.VMMemoryDiagnostics
		want int
	}{
		{"jvm unreachable", jvm.VMMemoryDiagnostics{
			HeapInfo: failing, Metaspace: failing, NativeMemory: failing,
			ClassLoaderStats: failing, CodeCache: failing, CodeHeap: failing,
		}, 1},
		{"nmt disabled only", jvm.VMMemoryDiagnostics{
			HeapInfo: jvm.DiagnosticSection{Output: "garbage-first heap\n"}, NativeMemory: failing,
		}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := collectVMMemory
			collectVMMemory = func(pid int, _ jvm.CmdRunner) jvm.VMMemoryDiagnostics {
				d := tc.diag
				d.PID = pid
				return d
			}
			t.Cleanup(func() { collectVMMemory = old })
			for _, args := range [][]string{{"999999"}, {"--json", "999999"}} {
				var got int
				captureStdout(t, func() { got = runJVMVMMemory(args) })
				if got != tc.want {
					t.Errorf("runJVMVMMemory(%q) = %d, want %d", args, got, tc.want)
				}
			}
		})
	}
}
