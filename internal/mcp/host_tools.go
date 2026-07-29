package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/logquery"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/services"
	"github.com/kaeawc/spectra/internal/storagestate"
	"github.com/kaeawc/spectra/internal/store"
	"github.com/kaeawc/spectra/internal/sysinfo"
	"github.com/kaeawc/spectra/internal/syslimits"
	"github.com/kaeawc/spectra/internal/updates"
)

// toolPower reports battery/thermal state or a per-pid energy-impact ranking.
func (s *Server) toolPower(raw json.RawMessage) ToolResult {
	var p struct {
		Operation       string `json:"operation"`
		Pids            []int  `json:"pids"`
		DurationSeconds int    `json:"duration_seconds"`
		Limit           int    `json:"limit"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolError(err.Error())
	}
	switch p.Operation {
	case "state", "":
		state := s.collect.Power.CollectPower()
		return toolText(toolEnvelope{
			Summary:     "collected battery and thermal state",
			NextActions: []string{"Use power operation=impact to rank processes by energy use."},
			Raw:         state,
			Timestamp:   s.now(),
		})
	case "impact":
		return s.toolPowerImpact(p.Pids, p.DurationSeconds, p.Limit)
	default:
		return toolError("unknown power operation: " + p.Operation)
	}
}

func (s *Server) toolPowerImpact(pids []int, durationSeconds, limit int) ToolResult {
	if durationSeconds <= 0 {
		durationSeconds = 1
	}
	ctx := context.Background()
	if len(pids) == 0 {
		for _, proc := range s.collect.Processes.CollectProcesses(ctx, process.CollectOptions{}) {
			if proc.PID > 0 {
				pids = append(pids, proc.PID)
			}
		}
	}
	deltas, err := s.collect.Power.SampleEnergy(ctx, time.Duration(durationSeconds)*time.Second, pids)
	if err != nil {
		return toolError(err.Error())
	}
	inputs := make([]sysinfo.ImpactInput, 0, len(deltas))
	for _, d := range deltas {
		inputs = append(inputs, sysinfo.FromRusage(d))
	}
	rows := sysinfo.ScoreImpacts(inputs, s.collect.Power.CollectAssertions(), sysinfo.DefaultWeights)
	if limit > 0 && limit < len(rows) {
		rows = rows[:limit]
	}
	return toolText(toolEnvelope{
		Summary:   fmt.Sprintf("ranked %d process(es) by energy impact over %ds", len(rows), durationSeconds),
		Raw:       rows,
		Timestamp: s.now(),
	})
}

// toolMemory reports VM compressor, swap, and memory-pressure state.
func (s *Server) toolMemory(raw json.RawMessage) ToolResult {
	if err := decodeArgs(raw, &struct{}{}); err != nil {
		return toolError(err.Error())
	}
	state, err := s.collect.Memory.Collect()
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{
		Summary:   "collected VM compressor, swap, and pressure state",
		Raw:       state,
		Timestamp: s.now(),
	})
}

// toolStorage reports disk volumes and the ~/Library storage footprint.
func (s *Server) toolStorage(raw json.RawMessage) ToolResult {
	var p struct {
		Paths        []string `json:"paths"`
		LargestAppsN int      `json:"limit"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolError(err.Error())
	}
	n := p.LargestAppsN
	if n <= 0 {
		n = 10
	}
	state := s.collect.Storage.Collect(storagestate.CollectOptions{AppPaths: p.Paths, LargestAppsN: n})
	return toolText(toolEnvelope{
		Summary:   "collected disk volume and ~/Library storage footprint",
		Raw:       state,
		Timestamp: s.now(),
	})
}

// toolSystem reports kernel resource limits, saturation, and top holders.
func (s *Server) toolSystem(raw json.RawMessage) ToolResult {
	var p struct {
		Operation string `json:"operation"`
		Limit     int    `json:"limit"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolError(err.Error())
	}
	switch p.Operation {
	case "limits", "":
		limits := s.collect.System.Collect(syslimits.Options{})
		return toolText(toolEnvelope{
			Summary:     "collected system resource limits and saturation",
			NextActions: []string{"Use system operation=top to see which processes hold saturated resources."},
			Raw:         limits,
			Timestamp:   s.now(),
		})
	case "top":
		limit := p.Limit
		if limit <= 0 {
			limit = 10
		}
		holders := s.collect.System.CollectTopHolders(context.Background(), limit, process.CollectOptions{})
		return toolText(toolEnvelope{
			Summary:   fmt.Sprintf("collected top resource holders (limit %d)", limit),
			Raw:       holders,
			Timestamp: s.now(),
		})
	default:
		return toolError("unknown system operation: " + p.Operation)
	}
}

// toolServices reports launchd jobs and plist schedules.
func (s *Server) toolServices(raw json.RawMessage) ToolResult {
	if err := decodeArgs(raw, &struct{}{}); err != nil {
		return toolError(err.Error())
	}
	inv, err := s.collect.Services.List(context.Background(), services.Options{})
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{
		Summary:   "collected launchd jobs and plist schedules",
		Raw:       inv,
		Timestamp: s.now(),
	})
}

// toolLogs runs a bounded unified-log query.
func (s *Server) toolLogs(raw json.RawMessage) ToolResult {
	var p struct {
		Process              string `json:"process"`
		Subsystem            string `json:"subsystem"`
		Predicate            string `json:"predicate"`
		MinLevel             string `json:"min_level"`
		LastSeconds          int    `json:"last_seconds"`
		MaxRows              int    `json:"max_rows"`
		AllowLongWindow      bool   `json:"allow_long_window"`
		AllowUnsafePredicate bool   `json:"allow_unsafe_predicate"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolError(err.Error())
	}
	q := logquery.Query{
		Predicate:            p.Predicate,
		Process:              p.Process,
		Subsystem:            p.Subsystem,
		MinLevel:             p.MinLevel,
		MaxRows:              p.MaxRows,
		AllowLongWindow:      p.AllowLongWindow,
		AllowUnsafePredicate: p.AllowUnsafePredicate,
	}
	if p.LastSeconds > 0 {
		q.Last = time.Duration(p.LastSeconds) * time.Second
	}
	result, err := s.collect.Logs.Run(context.Background(), q)
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{
		Summary:   "ran bounded unified-log query",
		Raw:       result,
		Timestamp: s.now(),
	})
}

// toolUpdates reports macOS install/update history or install-log entries.
func (s *Server) toolUpdates(raw json.RawMessage) ToolResult {
	var p struct {
		Operation string `json:"operation"`
		Process   string `json:"process"`
		Grep      string `json:"grep"`
		MaxLines  int    `json:"max_lines"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolError(err.Error())
	}
	switch p.Operation {
	case "history", "":
		history, err := s.collect.Updates.CollectHistory(context.Background())
		if err != nil {
			return toolError(err.Error())
		}
		return toolText(toolEnvelope{
			Summary:     "collected macOS install/update history",
			NextActions: []string{"Use updates operation=log for install.log line detail."},
			Raw:         history,
			Timestamp:   s.now(),
		})
	case "log":
		result, err := s.collect.Updates.QueryInstallLog(updates.Query{Process: p.Process, Grep: p.Grep, MaxLines: p.MaxLines})
		if err != nil {
			return toolError(err.Error())
		}
		return toolText(toolEnvelope{
			Summary:   "queried macOS install log",
			Raw:       result,
			Timestamp: s.now(),
		})
	default:
		return toolError("unknown updates operation: " + p.Operation)
	}
}

// toolTimeMachine reports Time Machine status, destinations, and snapshots.
func (s *Server) toolTimeMachine(raw json.RawMessage) ToolResult {
	if err := decodeArgs(raw, &struct{}{}); err != nil {
		return toolError(err.Error())
	}
	state, err := s.collect.TimeMachine.Collect(context.Background())
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{
		Summary:   "collected Time Machine status, destinations, and local snapshots",
		Raw:       state,
		Timestamp: s.now(),
	})
}

// toolPlaybook lists the built-in diagnostic playbooks or fetches one by id.
func (s *Server) toolPlaybook(raw json.RawMessage) ToolResult {
	var p struct {
		Operation string `json:"operation"`
		ID        string `json:"id"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolError(err.Error())
	}
	switch p.Operation {
	case "list", "":
		list := s.collect.Playbooks.List()
		return toolText(toolEnvelope{
			Summary:     fmt.Sprintf("found %d diagnostic playbook(s)", len(list)),
			NextActions: []string{"Use playbook operation=get id=<id> for a full command plan."},
			Raw:         list,
			Timestamp:   s.now(),
		})
	case "get":
		if p.ID == "" {
			return toolError("playbook get requires id")
		}
		pb, ok := s.collect.Playbooks.Get(p.ID)
		if !ok {
			return toolError("unknown playbook id: " + p.ID)
		}
		return toolText(toolEnvelope{
			Summary:   "loaded playbook " + p.ID,
			Raw:       pb,
			Timestamp: s.now(),
		})
	default:
		return toolError("unknown playbook operation: " + p.Operation)
	}
}

// toolCore inspects a crashed-process core file and suggests offline analyzers.
func (s *Server) toolCore(raw json.RawMessage) ToolResult {
	var p struct {
		Path       string `json:"path"`
		Executable string `json:"executable"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolError(err.Error())
	}
	if p.Path == "" {
		return toolError("core inspect requires path")
	}
	report, err := s.collect.CoreFiles.Inspect(context.Background(), p.Path, p.Executable)
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{
		Summary:   "inspected core file " + p.Path,
		Raw:       report,
		Timestamp: s.now(),
	})
}

// toolCache reports or clears the local blob cache.
func (s *Server) toolCache(raw json.RawMessage) ToolResult {
	var p struct {
		Operation string `json:"operation"`
		Kind      string `json:"kind"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolError(err.Error())
	}
	root, err := cache.DefaultRoot()
	if err != nil {
		return toolError(err.Error())
	}
	cache.NewStores(root, cache.Default)
	switch p.Operation {
	case "stats", "":
		stats, err := cache.Default.Stats()
		if err != nil {
			return toolError(err.Error())
		}
		return toolText(toolEnvelope{
			Summary:   fmt.Sprintf("collected stats for %d cache kind(s)", len(stats)),
			Raw:       map[string]interface{}{"kinds": cache.Default.Names(), "stats": stats},
			Timestamp: s.now(),
		})
	case "clear":
		if err := cache.Default.Clear(p.Kind); err != nil {
			return toolError(err.Error())
		}
		target := p.Kind
		if target == "" {
			target = "all kinds"
		}
		return toolText(toolEnvelope{
			Summary:   "cleared cache: " + target,
			Timestamp: s.now(),
		})
	default:
		return toolError("unknown cache operation: " + p.Operation)
	}
}

// toolMetrics reads stored process metrics and app-churn aggregates persisted
// by a running spectra daemon.
func (s *Server) toolMetrics(raw json.RawMessage) ToolResult {
	var p struct {
		Operation string `json:"operation"`
		PID       int    `json:"pid"`
		AppPath   string `json:"app_path"`
		Limit     int    `json:"limit"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolError(err.Error())
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 60
	}
	db, err := openStore()
	if err != nil {
		return toolError(err.Error())
	}
	defer db.Close()
	ctx := context.Background()
	switch p.Operation {
	case "process", "":
		var rows []store.ProcessMetricRow
		if p.PID > 0 {
			rows, err = db.GetProcessMetrics(ctx, p.PID, limit)
		} else {
			rows, err = db.GetAllProcessMetrics(ctx, limit)
		}
		if err != nil {
			return toolError(err.Error())
		}
		return toolText(toolEnvelope{
			Summary:   fmt.Sprintf("found %d stored process-metric row(s)", len(rows)),
			Raw:       rows,
			Timestamp: s.now(),
		})
	case "churn":
		var rows []store.AppChurnRow
		if p.AppPath != "" {
			rows, err = db.GetAppChurn(ctx, p.AppPath, limit)
		} else {
			rows, err = db.GetAllAppChurn(ctx, limit)
		}
		if err != nil {
			return toolError(err.Error())
		}
		return toolText(toolEnvelope{
			Summary:   fmt.Sprintf("found %d stored app-churn row(s)", len(rows)),
			Raw:       rows,
			Timestamp: s.now(),
		})
	default:
		return toolError("unknown metrics operation: " + p.Operation)
	}
}

// toolRemoteHosts lists hosts known from stored snapshots.
func (s *Server) toolRemoteHosts() ToolResult {
	db, err := openStore()
	if err != nil {
		return toolError(err.Error())
	}
	defer db.Close()
	rows, err := db.ListHosts(context.Background())
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{
		Summary:   fmt.Sprintf("found %d known host(s)", len(rows)),
		Raw:       rows,
		Timestamp: s.now(),
	})
}
