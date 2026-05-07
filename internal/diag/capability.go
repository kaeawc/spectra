// Package diag defines runtime-neutral diagnostic capability metadata.
package diag

// CapabilityStatus describes whether a diagnostic capability is available for
// a target process or runtime.
type CapabilityStatus string

const (
	CapabilityAvailable   CapabilityStatus = "available"
	CapabilityUnavailable CapabilityStatus = "unavailable"
	CapabilityLimited     CapabilityStatus = "limited"
	CapabilityUnknown     CapabilityStatus = "unknown"
)

// Provider is implemented by runtime-specific diagnostic collectors.
type Provider interface {
	DiagnosticsMatrix() Matrix
}

// Capability is one reusable diagnostic action or signal Spectra can collect.
// Runtime-specific packages attach these to their own process snapshots while
// keeping the common support vocabulary stable across JVM, native, Node,
// Python, and future app architectures.
type Capability struct {
	ID          string           `json:"id"`
	Category    string           `json:"category,omitempty"`
	Status      CapabilityStatus `json:"status"`
	Since       string           `json:"since,omitempty"`
	Command     []string         `json:"command,omitempty"`
	Description string           `json:"description,omitempty"`
	Limitations string           `json:"limitations,omitempty"`
}

// Matrix groups capabilities for one runtime family and version.
type Matrix struct {
	Architecture string       `json:"architecture"`
	Runtime      string       `json:"runtime,omitempty"`
	Version      string       `json:"version,omitempty"`
	Capabilities []Capability `json:"capabilities,omitempty"`
}
