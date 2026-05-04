package jvm

import (
	"fmt"
	"strings"
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

// countThreads counts threads in `jcmd Thread.print` output.
//
// Thread entries begin with a line like:
//
//	"main" #1 prio=5 os_prio=31 cpu=... elapsed=... tid=... nid=... waiting [...]
//
// We count lines that start with `"` followed by a non-whitespace char
// (thread name in quotes) as one thread entry.
func countThreads(out string) int {
	count := 0
	for _, line := range strings.Split(out, "\n") {
		// Thread name lines start with a double-quote.
		if len(line) > 0 && line[0] == '"' {
			count++
		}
	}
	return count
}
