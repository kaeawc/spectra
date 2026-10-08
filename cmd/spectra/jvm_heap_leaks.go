package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/kaeawc/spectra/internal/heap"
	issueflow "github.com/kaeawc/spectra/internal/issues"
	"github.com/kaeawc/spectra/internal/rules"
	"github.com/kaeawc/spectra/internal/store"
)

// leakSuspectRuleID identifies heap leak-suspect findings in the issues
// table. They come from dump analysis rather than the snapshot rule engine.
const leakSuspectRuleID = "jvm-heap-leak-suspect"

// heapGraphFlags are the `jvm heap-hprof` flags that need the object graph.
type heapGraphFlags struct {
	leaks      *bool
	threshold  *float64
	fileIssues *bool
	paths      *string
	maxPaths   *int
}

func registerHeapGraphFlags(fs *flag.FlagSet) heapGraphFlags {
	return heapGraphFlags{
		leaks:      fs.Bool("leak-suspects", false, "Report leak suspects: dominator subtrees by retained size, accumulation points, and paths to GC roots"),
		threshold:  fs.Float64("threshold", heap.DefaultLeakThresholdPercent, "Leak-suspect threshold as a percentage of the reachable heap"),
		fileIssues: fs.Bool("file-issues", false, "With --leak-suspects, file each suspect as a Spectra issue"),
		paths:      fs.String("paths", "", "Show shortest paths to GC roots for an object id (0x… or decimal) or every instance of a class name"),
		maxPaths:   fs.Int("max-paths", 3, "Maximum paths (from distinct GC roots) for --paths"),
	}
}

// mode returns which object-graph analysis was requested ("" for none).
func (f heapGraphFlags) mode(retained bool) (string, error) {
	var modes []string
	if retained {
		modes = append(modes, "retained")
	}
	if *f.leaks {
		modes = append(modes, "leak-suspects")
	}
	if *f.paths != "" {
		modes = append(modes, "paths")
	}
	switch {
	case len(modes) > 1:
		return "", errors.New("--retained, --leak-suspects, and --paths are mutually exclusive")
	case *f.fileIssues && !*f.leaks:
		return "", errors.New("--file-issues requires --leak-suspects")
	case *f.threshold <= 0 || *f.threshold > 100:
		return "", errors.New("--threshold must be in (0, 100]")
	case *f.maxPaths < 1:
		return "", errors.New("--max-paths must be >= 1")
	case len(modes) == 0:
		return "", nil
	}
	return modes[0], nil
}

func (f heapGraphFlags) run(mode, path string, limit int, asJSON bool) int {
	switch mode {
	case "retained":
		return runRetainedAnalysis(path, limit, asJSON)
	case "leak-suspects":
		return runLeakSuspects(path, *f.threshold, limit, *f.fileIssues, asJSON)
	default:
		return runPathsToRoots(path, *f.paths, *f.maxPaths, asJSON)
	}
}

func loadObjectGraph(path string) (*heap.ObjectGraph, bool) {
	graph, err := heap.ParseObjectGraphFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parsing %q: %v\n", path, err)
		return nil, false
	}
	if graph.Unresolved > 0 {
		fmt.Fprintf(os.Stderr, "note: %d instance(s) had no class layout; their references were not walked\n", graph.Unresolved)
	}
	return graph, true
}

// heapLeakReport is the JSON shape emitted by `heap-hprof --leak-suspects --json`.
type heapLeakReport struct {
	Report   heap.LeakReport `json:"report"`
	Findings []rules.Finding `json:"findings"`
	IssueIDs []string        `json:"issue_ids,omitempty"`
}

func runLeakSuspects(path string, threshold float64, limit int, fileIssues, asJSON bool) int {
	graph, ok := loadObjectGraph(path)
	if !ok {
		return 1
	}
	rep := heap.LeakSuspects(heap.ComputeRetained(graph), heap.LeakOptions{ThresholdPercent: threshold, MaxSuspects: limit})
	out := heapLeakReport{Report: rep, Findings: leakSuspectFindings(path, rep)}
	if fileIssues && len(out.Findings) > 0 {
		ids, err := fileLeakIssuesInDefaultDB(out.Findings)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 1
		}
		out.IssueIDs = ids
		fmt.Fprintf(os.Stderr, "filed %d leak-suspect issue(s); see `spectra issues list`\n", len(ids))
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return 0
	}
	printLeakSuspects(os.Stdout, path, rep)
	return 0
}

func printLeakSuspects(w io.Writer, path string, rep heap.LeakReport) {
	fmt.Fprintf(w, "=== leak suspects: %s ===\n", path)
	fmt.Fprintf(w, "reachable heap: %s across %d objects (threshold %.1f%%)\n",
		humanSize(rep.ReachableBytes), rep.ReachableObjects, rep.ThresholdPercent)
	if len(rep.Suspects) == 0 {
		fmt.Fprintln(w, "no leak suspects: no dominator subtree or class reaches the threshold")
		return
	}
	for i, s := range rep.Suspects {
		fmt.Fprintf(w, "\nSuspect %d: %s — %s (%.1f%%)\n", i+1, s.ClassName, humanSize(s.RetainedBytes), s.PercentOfHeap)
		fmt.Fprintf(w, "  %s\n", s.Description)
		if acc := s.Accumulation; acc != nil {
			fmt.Fprintf(w, "  accumulation point: %s (0x%x) retains %s, directly dominating %d objects\n",
				acc.ClassName, acc.ID, humanSize(acc.RetainedBytes), acc.DominatedObjects)
		}
		if p := s.PathToRoot; p != nil {
			printRootPath(w, "  ", *p)
		}
	}
}

func printRootPath(w io.Writer, indent string, p heap.RootPath) {
	kinds := make([]string, 0, len(p.Roots))
	for _, r := range p.Roots {
		k := string(r.Kind)
		if r.ThreadID != 0 {
			k += fmt.Sprintf(" (thread 0x%x)", r.ThreadID)
		}
		kinds = append(kinds, k)
	}
	fmt.Fprintf(w, "%spath from GC root [%s]:\n", indent, strings.Join(kinds, ", "))
	for i, st := range p.Steps {
		arrow := "  "
		if i > 0 {
			arrow = "→ "
		}
		fmt.Fprintf(w, "%s  %s%s (0x%x)\n", indent, arrow, st.ClassName, st.ID)
	}
}

// leakSuspectFindings converts leak suspects into rule findings so they can be
// filed and tracked as issues. The subject is the suspect's class so a later
// dump showing the same suspect refreshes the existing issue.
func leakSuspectFindings(path string, rep heap.LeakReport) []rules.Finding {
	findings := make([]rules.Finding, 0, len(rep.Suspects))
	for _, s := range rep.Suspects {
		sev := rules.SeverityMedium
		if s.PercentOfHeap >= 30 {
			sev = rules.SeverityHigh
		}
		target := s.ID
		if s.Accumulation != nil {
			target = s.Accumulation.ID
		}
		fix := fmt.Sprintf("Release the references that keep the subtree alive or bound the collection; trace them with `spectra jvm heap-hprof --paths %s %s`.", pathsQuery(s.Kind, s.ClassName, target), path)
		findings = append(findings, rules.Finding{
			RuleID:   leakSuspectRuleID,
			Severity: sev,
			Subject:  "heap leak suspect: " + s.ClassName,
			Message:  fmt.Sprintf("%s (heap dump %s)", s.Description, path),
			Fix:      fix,
		})
	}
	return findings
}

func pathsQuery(kind, className string, id uint64) string {
	if kind == heap.LeakSuspectClass {
		return strconv.Quote(className)
	}
	return fmt.Sprintf("0x%x", id)
}

// issueUpserter is the slice of the store used to file leak-suspect issues.
type issueUpserter interface {
	UpsertIssues(ctx context.Context, machineUUID, snapshotID string, findings []store.FindingInput) ([]string, error)
}

func fileLeakIssues(ctx context.Context, st issueUpserter, machineUUID string, findings []rules.Finding) ([]string, error) {
	ids, err := st.UpsertIssues(ctx, machineUUID, "", issueflow.FindingInputs(findings))
	if err != nil {
		return nil, fmt.Errorf("file leak-suspect issues: %w", err)
	}
	return ids, nil
}

func fileLeakIssuesInDefaultDB(findings []rules.Finding) ([]string, error) {
	db, machineUUID, err := openIssuesDB()
	if err != nil {
		return nil, fmt.Errorf("open issues db: %w", err)
	}
	defer db.Close()
	ids, err := fileLeakIssues(context.Background(), db, machineUUID, findings)
	if err != nil {
		return nil, fmt.Errorf("%w (run `spectra issues check` once so this host is registered)", err)
	}
	return ids, nil
}

// heapPathsReport is the JSON shape emitted by `heap-hprof --paths --json`.
type heapPathsReport struct {
	Query   string          `json:"query"`
	Targets int             `json:"targets"`
	Paths   []heap.RootPath `json:"paths"`
}

func runPathsToRoots(path, query string, maxPaths int, asJSON bool) int {
	graph, ok := loadObjectGraph(path)
	if !ok {
		return 1
	}
	targets := resolvePathTargets(graph, query)
	if len(targets) == 0 {
		fmt.Fprintf(os.Stderr, "no object or class matches %q\n", query)
		return 1
	}
	rep := heapPathsReport{Query: query, Targets: len(targets), Paths: graph.PathsToGCRoots(targets, maxPaths)}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
		return 0
	}
	fmt.Fprintf(os.Stdout, "=== paths to GC roots: %s (%d target object(s)) ===\n", query, len(targets))
	if len(rep.Paths) == 0 {
		fmt.Fprintln(os.Stdout, "unreachable: no path from any GC root")
	}
	for _, p := range rep.Paths {
		printRootPath(os.Stdout, "", p)
	}
	return 0
}

// resolvePathTargets interprets query as an object id (0x-prefixed hex or
// decimal) present in the dump, else as a class name whose instances are the
// targets.
func resolvePathTargets(g *heap.ObjectGraph, query string) []int32 {
	if id, err := strconv.ParseUint(query, 0, 64); err == nil {
		if i, ok := g.Index(id); ok {
			return []int32{i}
		}
	}
	return g.InstancesOf(query)
}
