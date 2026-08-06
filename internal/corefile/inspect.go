package corefile

import (
	"context"
	"debug/macho"
	"fmt"
	"os"
	"strings"
)

const defaultProbeBytes = 1 << 20

const machOTypeCore macho.Type = 4

// Inspector coordinates runtime-neutral artifact probing with pluggable analyzers.
type Inspector struct {
	Analyzers []Analyzer
	ReadProbe func(path string, limit int64) ([]byte, error)
}

// Inspect probes path and runs every analyzer that supports the artifact.
func (i Inspector) Inspect(ctx context.Context, path, executablePath string) (Report, error) {
	artifact, probe, err := i.describe(path, executablePath)
	if err != nil {
		return Report{}, err
	}
	report := Report{Artifact: artifact}
	for _, analyzer := range i.Analyzers {
		if analyzer == nil || !analyzer.Supports(ctx, artifact, probe) {
			continue
		}
		next, err := analyzer.Analyze(ctx, artifact, probe)
		if err != nil {
			return Report{}, fmt.Errorf("%s analyzer: %w", analyzer.Name(), err)
		}
		report.Analyzers = append(report.Analyzers, analyzer.Name())
		if report.Runtime == "" {
			report.Runtime = next.Runtime
		}
		report.Observations = append(report.Observations, next.Observations...)
		report.Commands = append(report.Commands, next.Commands...)
	}
	return report, nil
}

func (i Inspector) describe(path, executablePath string) (Artifact, []byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Artifact{}, nil, fmt.Errorf("stat core file: %w", err)
	}
	if info.IsDir() {
		return Artifact{}, nil, fmt.Errorf("core file %q is a directory", path)
	}
	readProbe := i.ReadProbe
	if readProbe == nil {
		readProbe = readPrefix
	}
	probe, err := readProbe(path, defaultProbeBytes)
	if err != nil {
		return Artifact{}, nil, fmt.Errorf("read core probe: %w", err)
	}
	artifact := Artifact{
		Path:           path,
		ExecutablePath: executablePath,
		SizeBytes:      info.Size(),
	}
	artifact.Format, artifact.Architecture = identifyMachO(path)
	if artifact.Format == "" {
		artifact.Format = identifyProbe(probe)
	}
	if artifact.ExecutablePath == "" {
		if resolved := resolveExecutableFromProbe(probe, statExecutable); resolved != "" {
			artifact.ExecutablePath = resolved
			artifact.ExecutableResolved = true
		}
	}
	return artifact, probe, nil
}

// statExecutable reports whether path is a regular file with an executable bit.
// It is the default on-disk check used to validate an auto-resolved executable.
func statExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// resolveExecutableFromProbe scans a core's readable prefix for an absolute
// executable path embedded in the image (typically argv[0] or a dyld image
// path) and returns the first candidate that exists on disk as an executable.
//
// This is best-effort: a core does not guarantee the main executable's path is
// present in the probed prefix, and shared libraries are deliberately excluded
// so the result is biased toward the crashed program rather than a loaded dylib.
// Callers must treat the result as a suggestion the user can override with --exe.
func resolveExecutableFromProbe(probe []byte, exists func(string) bool) string {
	for _, cand := range absolutePathTokens(probe) {
		if isSharedLibraryPath(cand) {
			continue
		}
		if exists(cand) {
			return cand
		}
	}
	return ""
}

// absolutePathTokens extracts NUL/whitespace-delimited absolute paths ("/...")
// made of printable, non-space bytes from a byte buffer.
func absolutePathTokens(buf []byte) []string {
	var tokens []string
	start := -1
	flush := func(end int) {
		if start >= 0 && buf[start] == '/' && end-start > 1 && end-start < 4096 {
			tokens = append(tokens, string(buf[start:end]))
		}
		start = -1
	}
	for i, b := range buf {
		if b > 0x20 && b < 0x7f {
			if start < 0 {
				start = i
			}
			continue
		}
		flush(i)
	}
	flush(len(buf))
	return tokens
}

func isSharedLibraryPath(path string) bool {
	if strings.HasSuffix(path, ".dylib") || strings.HasSuffix(path, ".so") {
		return true
	}
	return strings.HasPrefix(path, "/usr/lib/") || strings.HasPrefix(path, "/System/")
}

func readPrefix(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, limit)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return nil, err
	}
	return buf[:n], nil
}

func identifyMachO(path string) (format, arch string) {
	if f, err := macho.Open(path); err == nil {
		defer f.Close()
		if f.Type == machOTypeCore {
			return "mach-o-core", f.Cpu.String()
		}
		return "mach-o-" + strings.ToLower(f.Type.String()), f.Cpu.String()
	}
	if fat, err := macho.OpenFat(path); err == nil {
		defer fat.Close()
		for _, arch := range fat.Arches {
			if arch.Type == machOTypeCore {
				return "fat-mach-o-core", arch.Cpu.String()
			}
		}
		return "fat-mach-o", ""
	}
	return "", ""
}

func identifyProbe(probe []byte) string {
	if len(probe) >= 4 && string(probe[:4]) == "\x7fELF" {
		return "elf"
	}
	if len(probe) == 0 {
		return "unknown"
	}
	return "unknown"
}
