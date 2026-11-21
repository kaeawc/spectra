package rules

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/storagestate"
)

const oneGiB = int64(1) << 30

// Reclaimable-area thresholds. Each area is regenerable, so the rule fires
// only when one is big enough to be worth clearing. CoreSimulator gets a
// higher bar because a few installed runtimes and devices legitimately use
// 10+ GiB.
const (
	derivedDataThreshold     = 10 * oneGiB
	coreSimulatorThreshold   = 20 * oneGiB
	deviceSupportThreshold   = 10 * oneGiB
	libraryCachesThreshold   = 10 * oneGiB
	storageFootprintRuleID   = "library-storage-footprint"
	libraryCachesDisplayPath = "~/Library/Caches"
)

// reclaimableArea is one ~/Library area the storage rule checks.
type reclaimableArea struct {
	area      string // storagestate.Area* name, "" for Caches
	path      string
	threshold int64
	fix       string
}

var storageFootprintAreas = []reclaimableArea{
	{
		area: storagestate.AreaDerivedData, path: "~/Library/" + storagestate.AreaDerivedData,
		threshold: derivedDataThreshold,
		fix:       "delete ~/Library/Developer/Xcode/DerivedData (Xcode rebuilds it on the next build)",
	},
	{
		area: storagestate.AreaCoreSimulator, path: "~/Library/" + storagestate.AreaCoreSimulator,
		threshold: coreSimulatorThreshold,
		fix:       "run `xcrun simctl delete unavailable` and remove unused runtimes in Xcode > Settings > Platforms",
	},
	{
		area: storagestate.AreaIOSDeviceSupport, path: "~/Library/" + storagestate.AreaIOSDeviceSupport,
		threshold: deviceSupportThreshold,
		fix:       "delete old OS versions under ~/Library/Developer/Xcode/iOS DeviceSupport (re-copied when that device reconnects)",
	},
	{
		path:      libraryCachesDisplayPath,
		threshold: libraryCachesThreshold,
		fix:       "clear the ~/Library/Caches entries cache-triage classifies as safe",
	},
}

type storageOffender struct {
	path  string
	bytes int64
	fix   string
}

// ruleStorageFootprint fires when regenerable ~/Library areas (Xcode
// DerivedData, simulators, device support, app caches) are over their
// thresholds, naming each offender. The footprint may be cached, so the
// message states when it was measured.
func ruleStorageFootprint() Rule {
	return Rule{
		ID:       storageFootprintRuleID,
		Severity: SeverityLow,
		Message:  "Regenerable ~/Library data (DerivedData, CoreSimulator, iOS DeviceSupport, Caches) is over its threshold.",
		Fix:      "Run `spectra storage cache-triage`, then clear the named areas.",
		MatchFn: func(s snapshot.Snapshot) []Finding {
			offenders := storageOffenders(s.Storage)
			if len(offenders) == 0 {
				return nil
			}
			var total int64
			parts := make([]string, 0, len(offenders))
			fixes := make([]string, 0, len(offenders))
			for _, o := range offenders {
				total += o.bytes
				parts = append(parts, fmt.Sprintf("%s %s", o.path, formatGiB(o.bytes)))
				fixes = append(fixes, o.fix)
			}
			msg := fmt.Sprintf("%s of regenerable ~/Library data is over threshold: %s.",
				formatGiB(total), strings.Join(parts, ", "))
			if age := footprintAge(s); age != "" {
				msg += " " + age + "."
			}
			return []Finding{{
				RuleID:   storageFootprintRuleID,
				Severity: SeverityLow,
				Subject:  "~/Library",
				Message:  msg,
				Fix:      "Run `spectra storage cache-triage` to see what is safe to reclaim, then: " + strings.Join(fixes, "; ") + ".",
			}}
		},
	}
}

func storageOffenders(st storagestate.State) []storageOffender {
	sizes := map[string]int64{"": st.AppCachesBytes}
	for _, r := range st.UserLibraryReclaimable {
		sizes[r.Name] = r.Bytes
	}
	var out []storageOffender
	for _, a := range storageFootprintAreas {
		// Linux footprints have no ~/Library (UserLibraryBytes stays 0) and
		// AppCachesBytes there measures the XDG cache dir, which the
		// ~/Library/Caches advice would misname.
		if a.area == "" && st.UserLibraryBytes == 0 {
			continue
		}
		if b := sizes[a.area]; b >= a.threshold {
			out = append(out, storageOffender{path: a.path, bytes: b, fix: a.fix})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].bytes > out[j].bytes })
	return out
}

func formatGiB(b int64) string {
	return fmt.Sprintf("%.1f GiB", float64(b)/float64(oneGiB))
}

// footprintAge describes how old the footprint was when the snapshot was
// taken, e.g. "Measured 3h ago". Empty when either time is unknown.
func footprintAge(s snapshot.Snapshot) string {
	asOf := s.Storage.UserFootprintAsOf
	if asOf.IsZero() || s.TakenAt.IsZero() {
		return ""
	}
	age := s.TakenAt.Sub(asOf)
	switch {
	case age < time.Minute:
		return "Measured just now"
	case age < time.Hour:
		return fmt.Sprintf("Measured %dm ago", int(age/time.Minute))
	case age < 48*time.Hour:
		return fmt.Sprintf("Measured %dh ago", int(age/time.Hour))
	default:
		return fmt.Sprintf("Measured %dd ago", int(age/(24*time.Hour)))
	}
}
