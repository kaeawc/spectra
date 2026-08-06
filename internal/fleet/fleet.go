// Package fleet aggregates per-host snapshots into cross-host answers: which
// hosts trip a given rule (symptom rollup), and how a dimension (JDK or an app)
// varies across the tailnet (drift matrix). The reducers are pure over loaded
// snapshots and deliberately tolerant: a host with no usable snapshot is marked
// "unknown" for a dimension rather than mislabeled as "missing X".
package fleet

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kaeawc/spectra/internal/rules"
	"github.com/kaeawc/spectra/internal/snapshot"
)

// HostSnapshot pairs a host identity with its latest snapshot. Empty is true
// when no snapshot could be loaded for the host.
type HostSnapshot struct {
	Hostname    string
	MachineUUID string
	Snap        snapshot.Snapshot
	Empty       bool
}

func hostLabel(h HostSnapshot) string {
	if h.Hostname != "" {
		return h.Hostname
	}
	if h.MachineUUID != "" {
		return h.MachineUUID
	}
	return "unknown-host"
}

// SymptomRollup groups hosts by whether a rule fires against their snapshot.
type SymptomRollup struct {
	RuleID  string   `json:"rule_id"`
	Firing  []string `json:"firing"`
	Clear   []string `json:"clear"`
	Unknown []string `json:"unknown,omitempty"`
}

// RollupSymptom evaluates catalog against each host's snapshot and groups hosts
// by whether ruleID fired. Hosts without a usable snapshot are Unknown.
func RollupSymptom(hosts []HostSnapshot, ruleID string, catalog []rules.Rule) SymptomRollup {
	r := SymptomRollup{RuleID: ruleID}
	for _, h := range hosts {
		name := hostLabel(h)
		switch {
		case h.Empty:
			r.Unknown = append(r.Unknown, name)
		case ruleFires(h.Snap, ruleID, catalog):
			r.Firing = append(r.Firing, name)
		default:
			r.Clear = append(r.Clear, name)
		}
	}
	sort.Strings(r.Firing)
	sort.Strings(r.Clear)
	sort.Strings(r.Unknown)
	return r
}

func ruleFires(snap snapshot.Snapshot, ruleID string, catalog []rules.Rule) bool {
	for _, f := range rules.Evaluate(snap, catalog) {
		if f.RuleID == ruleID {
			return true
		}
	}
	return false
}

// DriftCell is one host's value for a drift dimension.
type DriftCell struct {
	Host  string `json:"host"`
	Value string `json:"value"`
}

// DriftJDK builds a per-host JDK-version matrix.
func DriftJDK(hosts []HostSnapshot) []DriftCell {
	return driftMatrix(hosts, jdkValue)
}

// DriftApp builds a per-host version matrix for one app bundle ID.
func DriftApp(hosts []HostSnapshot, bundleID string) []DriftCell {
	return driftMatrix(hosts, func(h HostSnapshot) string { return appValue(h, bundleID) })
}

func driftMatrix(hosts []HostSnapshot, value func(HostSnapshot) string) []DriftCell {
	cells := make([]DriftCell, 0, len(hosts))
	for _, h := range hosts {
		cells = append(cells, DriftCell{Host: hostLabel(h), Value: value(h)})
	}
	sort.Slice(cells, func(i, j int) bool { return cells[i].Host < cells[j].Host })
	return cells
}

func jdkValue(h HostSnapshot) string {
	if h.Empty {
		return "unknown"
	}
	jdks := h.Snap.Toolchains.JDKs
	if len(jdks) == 0 {
		return "none"
	}
	seen := map[string]bool{}
	var vs []string
	for _, j := range jdks {
		v := fmt.Sprintf("%d", j.VersionMajor)
		if j.ReleaseString != "" {
			v = j.ReleaseString
		}
		if j.Vendor != "" {
			v += " (" + j.Vendor + ")"
		}
		if !seen[v] {
			seen[v] = true
			vs = append(vs, v)
		}
	}
	sort.Strings(vs)
	return strings.Join(vs, ", ")
}

func appValue(h HostSnapshot, bundleID string) string {
	if h.Empty {
		return "unknown"
	}
	for _, a := range h.Snap.Apps {
		if a.BundleID == bundleID {
			if a.AppVersion != "" {
				return a.AppVersion
			}
			return "installed"
		}
	}
	return "absent"
}
