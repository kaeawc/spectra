package crashquery

import (
	"context"
	"sort"
	"time"

	"github.com/kaeawc/spectra/internal/crashreport"
)

// jetsamBugTypes are numeric .ips bug_type codes that mark a jetsam /
// low-memory kill. Kept small and conservative.
var jetsamBugTypes = map[string]bool{"298": true}

// OOMKill is one row of `spectra crash oom --json`.
type OOMKill struct {
	Time     string `json:"time"`
	Process  string `json:"process"`
	Reason   string `json:"reason"`
	Limit    string `json:"limit,omitempty"`
	Observed string `json:"observed,omitempty"`
	File     string `json:"file"`
	// At is Time parsed, zero when it does not parse.
	At time.Time `json:"-"`
}

// ClassifyMemoryKill reports whether a report is a memory/OOM termination
// and, if so, the attributed kill. The reliable path is EXC_RESOURCE(MEMORY),
// which the crash decoder already resolves; jetsam bug_types are a
// best-effort path.
func ClassifyMemoryKill(r *crashreport.Report) (OOMKill, bool) {
	if r.Resource != nil && r.Resource.Flavor == "MEMORY" {
		return OOMKill{
			Process:  r.Process,
			Reason:   r.Resource.Explanation,
			Limit:    r.Resource.Limit,
			Observed: r.Resource.Observed,
		}, true
	}
	if jetsamBugTypes[r.BugType] {
		return OOMKill{Process: r.Process, Reason: "jetsam / low-memory kill"}, true
	}
	return OOMKill{}, false
}

// CollectMemoryKills returns the memory kills among swept, newest first.
func CollectMemoryKills(swept []Swept) []OOMKill {
	var out []OOMKill
	for _, s := range swept {
		kill, ok := ClassifyMemoryKill(s.Report)
		if !ok {
			continue
		}
		kill.Time = s.Report.Time
		kill.File = s.File
		kill.At = ParseTime(s.Report.Time)
		out = append(out, kill)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// OOMResult is a memory-kill listing. Kills is the --json value.
type OOMResult struct {
	Kills []OOMKill
	// Total counts the parsed reports.
	Total int
}

// OOM lists the memory kills in src, newest first, keeping at most limit
// (all when limit <= 0).
func OOM(ctx context.Context, src Source, limit int) (OOMResult, error) {
	swept, _, err := sweep(ctx, src)
	if err != nil {
		return OOMResult{}, err
	}
	kills := CollectMemoryKills(swept)
	if limit > 0 && len(kills) > limit {
		kills = kills[:limit]
	}
	return OOMResult{Kills: kills, Total: len(swept)}, nil
}
