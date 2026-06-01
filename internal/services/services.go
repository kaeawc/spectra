package services

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"howett.net/plist"
)

type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type RunnerFunc func(context.Context, string, ...string) ([]byte, error)

func (f RunnerFunc) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f(ctx, name, args...)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

type Options struct {
	Domain    string
	Runner    Runner
	Now       func() time.Time
	Home      string
	PlistDirs []string
}

func List(ctx context.Context, opts Options) (LaunchInventory, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	run := opts.Runner
	if run == nil {
		run = execRunner{}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	domain := normalizeDomain(opts.Domain)
	plistJobs := readPlistJobs(opts, domain)
	jobsByLabel := map[string]LaunchJob{}
	for _, job := range plistJobs {
		jobsByLabel[job.Label] = job
	}
	if out, err := run.Run(ctx, "/bin/launchctl", "list"); err == nil {
		for _, job := range ParseLaunchctlList(out) {
			if existing, ok := jobsByLabel[job.Label]; ok {
				mergeLaunchctlState(&existing, job)
				jobsByLabel[job.Label] = existing
				continue
			}
			if domain == "all" {
				jobsByLabel[job.Label] = job
			}
		}
	}
	jobs := make([]LaunchJob, 0, len(jobsByLabel))
	for _, job := range jobsByLabel {
		job.OnDemand = computeOnDemand(job)
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].Label < jobs[j].Label
	})
	return LaunchInventory{Jobs: jobs, CollectedAt: now().UTC()}, nil
}

func IsLoaded(ctx context.Context, label string) bool {
	return IsLoadedWithRunner(ctx, execRunner{}, "system", label)
}

func IsLoadedWithRunner(ctx context.Context, run Runner, domain, label string) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	if run == nil || label == "" {
		return false
	}
	service := label
	if !strings.Contains(label, "/") {
		service = normalizeDomain(domain) + "/" + label
	}
	_, err := run.Run(ctx, "/bin/launchctl", "print", service)
	return err == nil
}

func ParseLaunchctlList(data []byte) []LaunchJob {
	var jobs []LaunchJob
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] == "PID" {
			continue
		}
		pid := parseLaunchctlInt(fields[0])
		status := parseLaunchctlInt(fields[1])
		jobs = append(jobs, LaunchJob{
			Label:          fields[2],
			Domain:         "loaded",
			PID:            pid,
			LastExitStatus: status,
		})
	}
	return jobs
}

func parseLaunchctlInt(value string) int32 {
	if value == "-" {
		return 0
	}
	n, _ := strconv.ParseInt(value, 10, 32)
	return int32(n)
}

func readPlistJobs(opts Options, domain string) []LaunchJob {
	dirs := opts.PlistDirs
	if len(dirs) == 0 {
		dirs = plistDirs(domain, opts.Home)
	}
	var jobs []LaunchJob
	for _, dir := range dirs {
		filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error { //nolint:errcheck
			if err != nil {
				return err
			}
			if entry.IsDir() || filepath.Ext(path) != ".plist" {
				return nil
			}
			job, err := readLaunchPlist(path, domainForPlistPath(path))
			if err == nil && job.Label != "" {
				jobs = append(jobs, job)
			}
			return nil
		})
	}
	return jobs
}

func plistDirs(domain, home string) []string {
	var dirs []string
	if domain == "system" || domain == "all" {
		dirs = append(dirs, "/System/Library/LaunchDaemons", "/Library/LaunchDaemons")
	}
	if domain == "user" || domain == "all" {
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		if home != "" {
			dirs = append(dirs, filepath.Join(home, "Library", "LaunchAgents"))
		}
	}
	return dirs
}

func readLaunchPlist(path, domain string) (LaunchJob, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return LaunchJob{}, err
	}
	var root map[string]any
	if _, err := plist.Unmarshal(data, &root); err != nil {
		return LaunchJob{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return LaunchJob{}, err
	}
	return jobFromPlist(root, path, domain, info.ModTime()), nil
}

func jobFromPlist(root map[string]any, path, domain string, mtime time.Time) LaunchJob {
	job := LaunchJob{
		Label:         stringValue(root, "Label"),
		Domain:        domain,
		Disabled:      boolValue(root, "Disabled"),
		KeepAlive:     keepAliveValue(root["KeepAlive"]),
		RunAtLoad:     boolValue(root, "RunAtLoad"),
		Program:       stringValue(root, "Program"),
		PlistPath:     path,
		PlistMTime:    mtime,
		StartInterval: intValue(root, "StartInterval"),
		StartCalendar: jsonString(root["StartCalendarInterval"]),
		WatchPaths:    stringSlice(root["WatchPaths"]),
	}
	job.ProgramArguments = stringSlice(root["ProgramArguments"])
	return job
}

func mergeLaunchctlState(dst *LaunchJob, state LaunchJob) {
	dst.PID = state.PID
	dst.LastExitStatus = state.LastExitStatus
}

func computeOnDemand(job LaunchJob) bool {
	return !job.KeepAlive &&
		!job.RunAtLoad &&
		job.StartInterval == 0 &&
		job.StartCalendar == "" &&
		len(job.WatchPaths) == 0
}

func normalizeDomain(domain string) string {
	switch domain {
	case "", "system":
		return "system"
	case "user", "all":
		return domain
	default:
		return "system"
	}
}

func domainForPlistPath(path string) string {
	if strings.Contains(path, "/LaunchAgents/") {
		return "user"
	}
	return "system"
}

func stringValue(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if raw, ok := m[key]; ok && raw != nil {
			if s, ok := raw.(string); ok {
				return s
			}
			return fmt.Sprint(raw)
		}
	}
	return ""
}

func boolValue(m map[string]any, keys ...string) bool {
	for _, key := range keys {
		if raw, ok := m[key]; ok {
			if b, ok := raw.(bool); ok {
				return b
			}
		}
	}
	return false
}

func keepAliveValue(raw any) bool {
	switch v := raw.(type) {
	case bool:
		return v
	case map[string]any:
		return len(v) > 0
	default:
		return false
	}
}

func intValue(m map[string]any, keys ...string) int {
	for _, key := range keys {
		raw, ok := m[key]
		if !ok {
			continue
		}
		n, err := strconv.Atoi(fmt.Sprint(raw))
		if err == nil && n > 0 {
			return n
		}
	}
	return 0
}

func stringSlice(raw any) []string {
	switch v := raw.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return append([]string(nil), v...)
	case string:
		return []string{v}
	default:
		return nil
	}
}

func jsonString(raw any) string {
	if raw == nil {
		return ""
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return fmt.Sprint(raw)
	}
	return string(data)
}
