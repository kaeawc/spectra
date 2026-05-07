// Package corefile describes and inspects crashed-process core files.
//
// The package is runtime-neutral: analyzers declare whether they support a
// core artifact and contribute commands or summaries for that application
// architecture. JVM Serviceability Agent support is one analyzer, not the
// package boundary.
package corefile

import "context"

// Artifact describes a crashed-process image and optional executable.
type Artifact struct {
	Path           string `json:"path"`
	ExecutablePath string `json:"executable_path,omitempty"`
	Format         string `json:"format,omitempty"`
	Architecture   string `json:"architecture,omitempty"`
	SizeBytes      int64  `json:"size_bytes,omitempty"`
}

// Command is a suggested offline inspection command for an artifact.
type Command struct {
	Tool        string   `json:"tool"`
	Args        []string `json:"args,omitempty"`
	Purpose     string   `json:"purpose,omitempty"`
	Sensitivity string   `json:"sensitivity,omitempty"`
}

// Observation is a small finding produced while inspecting an artifact.
type Observation struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Report is the combined output from all analyzers that support an artifact.
type Report struct {
	Artifact     Artifact      `json:"artifact"`
	Runtime      string        `json:"runtime,omitempty"`
	Analyzers    []string      `json:"analyzers,omitempty"`
	Observations []Observation `json:"observations,omitempty"`
	Commands     []Command     `json:"commands,omitempty"`
}

// Analyzer contributes runtime-specific offline analysis for a core artifact.
type Analyzer interface {
	Name() string
	Supports(ctx context.Context, artifact Artifact, probe []byte) bool
	Analyze(ctx context.Context, artifact Artifact, probe []byte) (Report, error)
}
