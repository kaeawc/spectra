package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/kaeawc/spectra/internal/process"
)

// chromiumRole is a process's decoded Chromium role.
type chromiumRole struct {
	Role     string `json:"role"`
	SubType  string `json:"sub_type,omitempty"`
	ClientID string `json:"client_id,omitempty"`
}

type roleGroup struct {
	Role    string   `json:"role"`
	Count   int      `json:"count"`
	RSSKiB  int64    `json:"rss_kib"`
	PIDs    []int    `json:"pids"`
	Details []string `json:"details,omitempty"`
}

type rendererOutlier struct {
	PID            int     `json:"pid"`
	ClientID       string  `json:"client_id,omitempty"`
	RSSKiB         int64   `json:"rss_kib"`
	MedianMultiple float64 `json:"median_multiple"`
}

type webProcTopology struct {
	App             string           `json:"app"`
	Processes       int              `json:"processes"`
	TotalRSSKiB     int64            `json:"total_rss_kib"`
	Roles           []roleGroup      `json:"roles"`
	OutlierRenderer *rendererOutlier `json:"outlier_renderer,omitempty"`
}

func defaultProcessCollector(app string) []process.Info {
	return process.CollectAll(context.Background(), process.CollectOptions{BundlePaths: []string{app}})
}

func runWebProcesses(args []string, stdout, stderr io.Writer, collect func(string) []process.Info) int {
	fs := flag.NewFlagSet("web processes", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: spectra web processes [--json] <app.app>")
		fmt.Fprintln(stderr, "Attribute a running Electron/Chromium app's memory to each helper role.")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	app := fs.Arg(0)
	procs := appProcesses(collect(app))
	if len(procs) == 0 {
		fmt.Fprintf(stderr, "%s: no running processes found for this app\n", app)
		return 1
	}
	topo := buildTopology(strings.TrimSuffix(baseName(app), ".app"), procs)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(topo); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	renderTopology(stdout, topo)
	return 0
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// appProcesses keeps only processes attributed to the target bundle.
func appProcesses(all []process.Info) []process.Info {
	var out []process.Info
	for _, p := range all {
		if p.AppPath != "" {
			out = append(out, p)
		}
	}
	return out
}

// classifyChromiumRole decodes a Chromium child's role from its argv. A process
// with no --type flag is the main (browser) process.
func classifyChromiumRole(cmdline string) chromiumRole {
	t := flagValue(cmdline, "--type=")
	r := chromiumRole{
		SubType:  flagValue(cmdline, "--utility-sub-type="),
		ClientID: flagValue(cmdline, "--renderer-client-id="),
	}
	switch t {
	case "":
		r.Role = "browser"
	case "renderer":
		r.Role = "renderer"
	case "gpu-process":
		r.Role = "gpu"
	case "utility":
		r.Role = "utility"
	case "zygote":
		r.Role = "zygote"
	default:
		r.Role = t
	}
	return r
}

// flagValue returns the value of a "--flag=" occurrence in a command line.
func flagValue(cmdline, prefix string) string {
	for _, f := range strings.Fields(cmdline) {
		if strings.HasPrefix(f, prefix) {
			return strings.TrimPrefix(f, prefix)
		}
	}
	return ""
}

func buildTopology(app string, procs []process.Info) webProcTopology {
	groups := map[string]*roleGroup{}
	order := []string{}
	var total int64
	var renderers []process.Info
	for _, p := range procs {
		role := classifyChromiumRole(p.FullCommandLine)
		g, ok := groups[role.Role]
		if !ok {
			g = &roleGroup{Role: role.Role}
			groups[role.Role] = g
			order = append(order, role.Role)
		}
		g.Count++
		g.RSSKiB += p.RSSKiB
		g.PIDs = append(g.PIDs, p.PID)
		if d := roleDetail(role); d != "" {
			g.Details = append(g.Details, fmt.Sprintf("pid %d %s", p.PID, d))
		}
		total += p.RSSKiB
		if role.Role == "renderer" {
			renderers = append(renderers, p)
		}
	}
	roles := make([]roleGroup, 0, len(order))
	for _, name := range order {
		roles = append(roles, *groups[name])
	}
	sort.Slice(roles, func(i, j int) bool { return roles[i].RSSKiB > roles[j].RSSKiB })
	return webProcTopology{
		App:             app,
		Processes:       len(procs),
		TotalRSSKiB:     total,
		Roles:           roles,
		OutlierRenderer: findRendererOutlier(renderers),
	}
}

func roleDetail(r chromiumRole) string {
	switch {
	case r.SubType != "":
		return r.SubType
	case r.ClientID != "":
		return "client-id " + r.ClientID
	default:
		return ""
	}
}

// findRendererOutlier flags the renderer whose RSS most exceeds the renderer
// median, when there are enough renderers to have a meaningful median and the
// top one is at least 2.5x it.
func findRendererOutlier(renderers []process.Info) *rendererOutlier {
	if len(renderers) < 3 {
		return nil
	}
	rss := make([]int64, len(renderers))
	for i, p := range renderers {
		rss[i] = p.RSSKiB
	}
	sort.Slice(rss, func(i, j int) bool { return rss[i] < rss[j] })
	median := rss[len(rss)/2]
	if median == 0 {
		return nil
	}
	top := renderers[0]
	for _, p := range renderers {
		if p.RSSKiB > top.RSSKiB {
			top = p
		}
	}
	mult := float64(top.RSSKiB) / float64(median)
	if mult < 2.5 {
		return nil
	}
	return &rendererOutlier{
		PID:            top.PID,
		ClientID:       flagValue(top.FullCommandLine, "--renderer-client-id="),
		RSSKiB:         top.RSSKiB,
		MedianMultiple: mult,
	}
}

func renderTopology(w io.Writer, t webProcTopology) {
	fmt.Fprintf(w, "%s — Chromium process topology (%d processes, %s)\n", t.App, t.Processes, mib(t.TotalRSSKiB))
	for _, g := range t.Roles {
		fmt.Fprintf(w, "  %-9s %2d proc  %10s\n", g.Role, g.Count, mib(g.RSSKiB))
		for _, d := range g.Details {
			fmt.Fprintf(w, "                          %s\n", d)
		}
	}
	if o := t.OutlierRenderer; o != nil {
		id := ""
		if o.ClientID != "" {
			id = " (client-id " + o.ClientID + ")"
		}
		fmt.Fprintf(w, "\noutlier renderer: pid %d%s %s = %.1fx the renderer median\n", o.PID, id, mib(o.RSSKiB), o.MedianMultiple)
	}
}

func mib(kib int64) string {
	return fmt.Sprintf("%d MB", kib/1024)
}
