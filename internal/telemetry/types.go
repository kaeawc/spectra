// Package telemetry defines runtime-neutral process telemetry embedded in
// Spectra snapshots.
package telemetry

import (
	"context"
	"time"
)

// Runtime names the application architecture that produced telemetry.
type Runtime string

const (
	RuntimeJVM Runtime = "jvm"
)

// Collector gathers runtime-specific telemetry in a runtime-neutral shape.
// Collectors should return partial process snapshots when individual probes
// fail instead of failing the entire snapshot.
type Collector interface {
	CollectRuntimeTelemetry(context.Context) []Process
}

// Process is one runtime process observed during a Spectra snapshot.
type Process struct {
	Runtime   Runtime           `json:"runtime"`
	PID       int               `json:"pid"`
	Identity  map[string]string `json:"identity,omitempty"`
	Config    map[string]string `json:"config,omitempty"`
	Heap      *Heap             `json:"heap,omitempty"`
	GC        *GC               `json:"gc,omitempty"`
	Threads   *Threads          `json:"threads,omitempty"`
	Profiles  []Profile         `json:"profiles,omitempty"`
	Sections  []Section         `json:"sections,omitempty"`
	Collected time.Time         `json:"collected,omitempty"`
}

// Heap captures memory counters that are meaningful across managed runtimes.
type Heap struct {
	UsedBytes      int64  `json:"used_bytes,omitempty"`
	CommittedBytes int64  `json:"committed_bytes,omitempty"`
	MaxBytes       int64  `json:"max_bytes,omitempty"`
	NativeBytes    int64  `json:"native_bytes,omitempty"`
	Source         string `json:"source,omitempty"`
}

// GC captures garbage-collector counters and pool-level details.
type GC struct {
	Collections      int64    `json:"collections,omitempty"`
	CollectionTimeMS int64    `json:"collection_time_ms,omitempty"`
	Pools            []GCPool `json:"pools,omitempty"`
	Source           string   `json:"source,omitempty"`
}

// GCPool is a memory pool observed by a managed runtime collector.
type GCPool struct {
	Name           string `json:"name"`
	CapacityBytes  int64  `json:"capacity_bytes,omitempty"`
	UsedBytes      int64  `json:"used_bytes,omitempty"`
	Collections    int64  `json:"collections,omitempty"`
	CollectionTime int64  `json:"collection_time_ms,omitempty"`
}

// Threads captures runtime thread counts and optional state breakdowns.
type Threads struct {
	Live   int            `json:"live,omitempty"`
	Peak   int            `json:"peak,omitempty"`
	Daemon int            `json:"daemon,omitempty"`
	States map[string]int `json:"states,omitempty"`
	Source string         `json:"source,omitempty"`
}

// Profile is metadata for an operator-requested or previously captured
// profiling artifact. Snapshots keep references, not large artifact payloads.
type Profile struct {
	Kind       string `json:"kind"`
	Event      string `json:"event,omitempty"`
	ArtifactID string `json:"artifact_id,omitempty"`
	Path       string `json:"path,omitempty"`
}

// Section carries runtime-specific diagnostic output without forcing every
// runtime to share the same schema up front.
type Section struct {
	Name    string   `json:"name"`
	Command []string `json:"command,omitempty"`
	Output  string   `json:"output,omitempty"`
	Error   string   `json:"error,omitempty"`
}
