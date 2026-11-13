package jvm

import (
	"fmt"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/heap"
)

// collectSysProps runs `jcmd <pid> VM.system_properties` and returns the
// filtered set of properties from the selectedProps allowlist.
func collectSysProps(pid int, run CmdRunner) map[string]string {
	out, err := run("jcmd", fmt.Sprint(pid), "VM.system_properties")
	if err != nil {
		return nil
	}
	return parseSysProps(string(out))
}

// parseSysProps parses `jcmd VM.system_properties` output.
//
// Each property is printed as "key=value" with multi-line values escaped as
// literal \n. Only keys in selectedProps are returned.
//
// Example:
//
//	java.home=/Library/Java/JavaVirtualMachines/temurin-21.jdk/...
//	java.vendor=Eclipse Adoptium
func parseSysProps(out string) map[string]string {
	m := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if !selectedProps[k] {
			continue
		}
		// Unescape literal \n sequences jcmd emits for multi-line values.
		v = strings.ReplaceAll(v, `\n`, "\n")
		m[k] = strings.TrimSpace(v)
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// collectCommandLine runs `jcmd <pid> VM.command_line` and returns the
// VM flags and arguments strings.
func collectCommandLine(pid int, run CmdRunner) (vmFlags, vmArgs string) {
	out, err := run("jcmd", fmt.Sprint(pid), "VM.command_line")
	if err != nil {
		return "", ""
	}
	return parseCommandLine(string(out))
}

// parseCommandLine parses `jcmd VM.command_line` output.
//
// Example output:
//
//	VM Arguments:
//	jvm_args: -Xmx4g -Xms256m -XX:+UseG1GC
//	java_command: com.example.Main
//	java_class_path (initial): /path/to/app.jar
//	Launcher Type: SUN_STANDARD
func parseCommandLine(out string) (vmFlags, vmArgs string) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, ":"); ok {
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			switch k {
			case "jvm_args":
				vmArgs = v
			case "VM Flags":
				vmFlags = v
			}
		}
	}
	return
}

// collectThreadCount runs `jcmd <pid> Thread.print` and returns the number
// of threads by counting thread header lines (lines starting with a quote or
// daemon/thread marker).
func collectThreadCount(pid int, run CmdRunner) int {
	out, err := run("jcmd", fmt.Sprint(pid), "Thread.print")
	if err != nil {
		return 0
	}
	return countThreads(string(out))
}

// ThreadDump runs `jcmd <pid> Thread.print` and returns the raw output.
// Pass nil for run to use the default system runner.
func ThreadDump(pid int, run CmdRunner) ([]byte, error) {
	if run == nil {
		run = DefaultRunner
	}
	return run("jcmd", fmt.Sprint(pid), "Thread.print")
}

// HeapHistogram runs `jcmd <pid> GC.class_histogram` and returns the raw
// output. The result can be large for apps with many loaded classes.
// Pass nil for run to use the default system runner.
func HeapHistogram(pid int, run CmdRunner) ([]byte, error) {
	if run == nil {
		run = DefaultRunner
	}
	return run("jcmd", fmt.Sprint(pid), "GC.class_histogram")
}

// HeapHistogramSnapshot runs GC.class_histogram and parses it into structured
// rows for reusable heap-analysis workflows.
func HeapHistogramSnapshot(pid int, run CmdRunner) (heap.Histogram, error) {
	out, err := HeapHistogram(pid, run)
	if err != nil {
		return heap.Histogram{}, err
	}
	return heap.ParseHistogram(string(out))
}

// HeapDump runs `jcmd <pid> GC.heap_dump <destPath>` to write a .hprof file.
// Pass nil for run to use the default system runner.
func HeapDump(pid int, destPath string, run CmdRunner) error {
	if run == nil {
		run = DefaultRunner
	}
	return runJcmdExpect(run, "Heap dump file created", fmt.Sprint(pid), "GC.heap_dump", destPath)
}

// JFRStart starts a Java Flight Recorder recording named recordingName on pid.
// Pass nil for run to use the default system runner.
func JFRStart(pid int, recordingName string, run CmdRunner) error {
	if run == nil {
		run = DefaultRunner
	}
	return runJcmdExpect(run, "Started recording", fmt.Sprint(pid), "JFR.start", "name="+recordingName)
}

// JFRDump dumps the named JFR recording for pid to destPath.
// Pass nil for run to use the default system runner.
func JFRDump(pid int, recordingName, destPath string, run CmdRunner) error {
	if run == nil {
		run = DefaultRunner
	}
	return runJcmdExpect(run, "Dumped recording", fmt.Sprint(pid), "JFR.dump",
		"name="+recordingName, "filename="+destPath)
}

// JFRStop stops and optionally dumps the named JFR recording.
// If destPath is non-empty the recording is dumped before stopping.
// Pass nil for run to use the default system runner.
func JFRStop(pid int, recordingName, destPath string, run CmdRunner) error {
	if run == nil {
		run = DefaultRunner
	}
	args := []string{fmt.Sprint(pid), "JFR.stop", "name=" + recordingName}
	if destPath != "" {
		args = append(args, "filename="+destPath)
	}
	return runJcmdExpect(run, "Stopped recording", args...)
}

// runJcmdExpect runs jcmd and requires its confirmation line. jcmd exits 0
// even when the diagnostic command itself fails ("Unable to create x.hprof:
// File exists", "Could not stop recording", "Dump failed"), so the exit
// status alone would report a heap dump or recording that was never written.
func runJcmdExpect(run CmdRunner, confirmation string, args ...string) error {
	out, err := run("jcmd", args...)
	if err != nil {
		return err
	}
	if !strings.Contains(string(out), confirmation) {
		return fmt.Errorf("jcmd %s: %s", strings.Join(args[1:2], ""), jcmdMessage(out))
	}
	return nil
}

// jcmdMessage condenses jcmd output for an error: it drops the leading
// "<pid>:" banner and joins the remaining non-empty lines.
func jcmdMessage(out []byte) string {
	var parts []string
	for i, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || (i == 0 && strings.HasSuffix(line, ":")) {
			continue
		}
		parts = append(parts, line)
	}
	if len(parts) == 0 {
		return "no confirmation in output"
	}
	return strings.Join(parts, "; ")
}

// countThreads counts parsed thread entries in `jcmd Thread.print` output.
func countThreads(out string) int {
	return len(ParseThreadDump(out, time.Time{}).Threads)
}
