package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"

	"github.com/kaeawc/spectra/internal/anomaly"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/store"
)

type metricsLoader func() ([]store.ProcessMetricRow, error)
type nameResolver func() map[int]string

func runAnomalies(args []string) int {
	return runAnomaliesWithIO(args, os.Stdout, os.Stderr, defaultMetricsLoader, defaultNameResolver)
}

func runAnomaliesWithIO(args []string, stdout, stderr io.Writer, loadMetrics metricsLoader, resolveNames nameResolver) int {
	fs := flag.NewFlagSet("anomalies", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	z := fs.Float64("z", 3.0, "z-score threshold")
	minSamples := fs.Int("min-samples", 5, "baseline points required before flagging")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *z <= 0 || math.IsNaN(*z) {
		fmt.Fprintln(stderr, "-z must be a positive number")
		return 2
	}
	if *minSamples < 2 {
		fmt.Fprintln(stderr, "--min-samples must be >= 2 (a baseline needs at least two points for variance)")
		return 2
	}
	rows, err := loadMetrics()
	if err != nil {
		fmt.Fprintf(stderr, "load metrics: %v\n", err)
		return 1
	}
	if len(rows) == 0 {
		fmt.Fprintln(stderr, "no process metrics stored — run `spectra serve` to sample processes over time")
		return 1
	}
	findings := anomaly.Detect(buildRSSSeries(rows), *minSamples, *z)
	names := resolveNames()
	if *asJSON {
		return encodeJSON(stdout, stderr, findings)
	}
	renderAnomalies(stdout, findings, names)
	return 0
}

func buildRSSSeries(rows []store.ProcessMetricRow) []anomaly.Series {
	byPID := map[int][]anomaly.Point{}
	for _, r := range rows {
		byPID[r.PID] = append(byPID[r.PID], anomaly.Point{Value: float64(r.AvgRSSKiB), At: r.MinuteAt})
	}
	series := make([]anomaly.Series, 0, len(byPID))
	for pid, pts := range byPID {
		series = append(series, anomaly.Series{Key: strconv.Itoa(pid), Points: pts})
	}
	return series
}

func defaultMetricsLoader() ([]store.ProcessMetricRow, error) {
	dbPath, err := store.DefaultPath()
	if err != nil {
		return nil, fmt.Errorf("resolve store path: %w", err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open store %s: %w", dbPath, err)
	}
	defer db.Close()
	rows, err := db.GetAllProcessMetrics(context.Background(), 20000)
	if err != nil {
		return nil, fmt.Errorf("query process metrics: %w", err)
	}
	return rows, nil
}

func defaultNameResolver() map[int]string {
	names := map[int]string{}
	for _, p := range process.CollectAll(context.Background(), process.CollectOptions{}) {
		names[p.PID] = p.Command
	}
	return names
}

func renderAnomalies(w io.Writer, findings []anomaly.Finding, names map[int]string) {
	if len(findings) == 0 {
		fmt.Fprintln(w, "No process RSS anomalies vs their rolling baselines.")
		return
	}
	fmt.Fprintf(w, "Process RSS anomalies vs rolling baseline (%d):\n", len(findings))
	for _, f := range findings {
		fmt.Fprintf(w, "  %s RSS %s is %+.1fσ vs baseline (~%s, %d samples)\n",
			anomalyLabel(f.Key, names), mibFromKiB(f.Latest), f.Z, mibFromKiB(f.Mean), f.Samples)
	}
}

func anomalyLabel(key string, names map[int]string) string {
	pid, err := strconv.Atoi(key)
	if err != nil {
		return key
	}
	if name := names[pid]; name != "" {
		return fmt.Sprintf("pid %d (%s)", pid, name)
	}
	return fmt.Sprintf("pid %d (exited)", pid)
}

func mibFromKiB(kib float64) string {
	mb := kib / 1024
	if mb >= 1024 {
		return fmt.Sprintf("%.1f GB", mb/1024)
	}
	return fmt.Sprintf("%.0f MB", mb)
}
