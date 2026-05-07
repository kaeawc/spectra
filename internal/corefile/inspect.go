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
	return artifact, probe, nil
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
