package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/rpc"
	"github.com/kaeawc/spectra/internal/serve"
)

const defaultRemotePort = "7878"

type connectTarget struct {
	Network string
	Address string
}

func runConnect(args []string) int {
	fs := flag.NewFlagSet("spectra connect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	timeout := fs.Duration("timeout", 3*time.Second, "Dial/read timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		printConnectUsage(os.Stderr)
		return 2
	}

	target, err := parseConnectTarget(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	method, params, err := parseConnectCall(fs.Args()[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		printConnectUsage(os.Stderr)
		return 2
	}

	conn, err := dialConnectTarget(target, *timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect %s: %v\n", fs.Arg(0), err)
		return 1
	}
	defer conn.Close()

	if d, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		_ = d.SetDeadline(time.Now().Add(*timeout))
	}

	result, err := callRPC(conn, method, params)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "encode response: %v\n", err)
		return 1
	}
	return 0
}

func printConnectUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: spectra connect [--timeout 3s] <target> [status|host|jvm|processes|network|storage|power|rules]")
	fmt.Fprintln(w, "   or: spectra connect [--timeout 3s] <target> inspect <App.app>")
	fmt.Fprintln(w, "   or: spectra connect [--timeout 3s] <target> jvm <pid> | jvm-gc <pid> | jvm-threads <pid> | jvm-heap <pid> | jvm-vm-memory <pid>")
	fmt.Fprintln(w, "   or: spectra connect [--timeout 3s] <target> jvm telemetry [pid] [limit] | jvm-telemetry [pid] [limit]")
	fmt.Fprintln(w, "   or: spectra connect [--timeout 3s] <target> jvm-explain <pid>")
	fmt.Fprintln(w, "   or: spectra connect [--timeout 3s] <target> jvm-jmx-status <pid> | jvm-jmx-start-local <pid>")
	fmt.Fprintln(w, "   or: spectra connect [--timeout 3s] <target> jvm-attach <pid> | jvm-mbeans <pid> | jvm-probe <pid>")
	fmt.Fprintln(w, "   or: spectra connect [--timeout 3s] <target> jvm-mbean-read <pid> <name> <attribute> | jvm-mbean-invoke <pid> <name> <operation>")
	fmt.Fprintln(w, "   or: spectra connect [--timeout 3s] <target> jvm-flamegraph <pid> [dest]")
	fmt.Fprintln(w, "   or: spectra connect [--timeout 3s] <target> snapshot diff <id-a> <id-b>")
	fmt.Fprintln(w, "   or: spectra connect [--timeout 3s] <target> diff <id-a> <id-b>")
	fmt.Fprintln(w, "   or: spectra connect [--timeout 3s] <target> jvm-heap-dump <pid> [dest] | jvm-jfr-start <pid> [name] | jvm-jfr-dump <pid> <dest> [name] | jvm-jfr-stop <pid> [dest] | jvm-jfr-summary <path>")
	fmt.Fprintln(w, "   or: spectra connect [--timeout 3s] <target> metrics [pid] [limit] | telemetry [pid] [limit]")
	fmt.Fprintln(w, "   or: spectra connect [--timeout 3s] <target> network-capture-start <iface> [duration_ms=N] [snap_len=N] [proto=tcp|udp] [host=HOST] [port=N]")
	fmt.Fprintln(w, "   or: spectra connect [--timeout 3s] <target> network-capture-stop <handle>")
	fmt.Fprintln(w, "   or: spectra connect [--timeout 3s] <target> cache [stats|clear [kind]]")
	fmt.Fprintln(w, "   or: spectra connect [--timeout 3s] <target> sample <pid> [duration] [interval]")
	fmt.Fprintln(w, "   or: spectra connect [--timeout 3s] <target> snapshot [list|create|get|diff|processes|login-items|granted-perms|prune] ...")
	fmt.Fprintln(w, "   or: spectra connect [--timeout 3s] <target> issues check [snapshot-id]")
	fmt.Fprintln(w, "   or: spectra connect [--timeout 3s] <target> storage <App.app> [more.apps] | network [state|connections|firewall|by-app [App.app ...]] | network-by-app [App.app ...]")
	fmt.Fprintln(w, "   or: spectra connect [--timeout 3s] <target> call <method> [json-params]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "targets: local, unix:/path/to/sock, /path/to/sock, host:port, host")
}

func parseConnectTarget(raw string) (connectTarget, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return connectTarget{}, fmt.Errorf("empty connect target")
	}
	switch {
	case raw == "local":
		sock, err := serve.DefaultSockPath()
		if err != nil {
			return connectTarget{}, fmt.Errorf("resolve local socket: %w", err)
		}
		return connectTarget{Network: "unix", Address: sock}, nil
	case strings.HasPrefix(raw, "unix:"):
		path := strings.TrimPrefix(raw, "unix:")
		if path == "" {
			return connectTarget{}, fmt.Errorf("empty unix socket target")
		}
		return connectTarget{Network: "unix", Address: path}, nil
	case strings.HasPrefix(raw, "/"):
		return connectTarget{Network: "unix", Address: raw}, nil
	}
	if _, _, err := net.SplitHostPort(raw); err == nil {
		return connectTarget{Network: "tcp", Address: raw}, nil
	}
	return connectTarget{Network: "tcp", Address: net.JoinHostPort(raw, defaultRemotePort)}, nil
}

func parseConnectCall(args []string) (string, json.RawMessage, error) {
	if len(args) == 0 {
		return "health", nil, nil
	}
	if args[0] == "status" || args[0] == "health" {
		if len(args) != 1 {
			return "", nil, fmt.Errorf("connect %s takes no extra arguments", args[0])
		}
		return "health", nil, nil
	}
	if method, params, ok, err := parseConnectShortcut(args); ok || err != nil {
		return method, params, err
	}
	return parseConnectGenericCall(args)
}

func parseConnectShortcut(args []string) (string, json.RawMessage, bool, error) {
	if method, params, ok, err := parseConnectCommonShortcuts(args); ok || err != nil {
		return method, params, ok, err
	}
	if parser, ok := connectShortcutParsers()[args[0]]; ok {
		return parser(args)
	}
	return "", nil, false, nil
}

func parseConnectCommonShortcuts(args []string) (string, json.RawMessage, bool, error) {
	if args[0] == "cache" {
		return parseConnectCache(args)
	}
	if method, ok := connectPIDShortcuts()[args[0]]; ok {
		return parseConnectPIDCall(args, method)
	}
	if shortcut, ok := connectStringSliceShortcuts()[args[0]]; ok {
		return parseConnectStringSliceCall(args, shortcut.method, shortcut.paramKey)
	}
	if method, ok := parseConnectNoArgShortcut(args); ok {
		return method, nil, true, nil
	}
	return "", nil, false, nil
}

func parseConnectNoArgShortcut(args []string) (string, bool) {
	method, ok := connectNoArgShortcuts()[args[0]]
	if !ok || len(args) != 1 {
		return "", false
	}
	return method, true
}

func parseConnectSnapshotDiff(args []string) (string, json.RawMessage, bool, error) {
	if len(args) != 3 {
		return "", nil, true, fmt.Errorf("connect diff requires <id-a> <id-b>")
	}
	return "snapshot.diff", connectParams(map[string]string{"id_a": args[1], "id_b": args[2]}), true, nil
}

func parseConnectStorage(args []string) (string, json.RawMessage, bool, error) {
	if len(args) == 1 {
		return "storage.system", nil, true, nil
	}
	return parseConnectStringSliceCall(args, "storage.byApp", "paths")
}

func parseConnectRules(args []string) (string, json.RawMessage, bool, error) {
	if len(args) == 1 {
		return "rules.check", nil, true, nil
	}
	if len(args) == 2 {
		return "rules.check", connectParams(map[string]string{"snapshot_id": args[1]}), true, nil
	}
	return "", nil, true, fmt.Errorf("connect rules accepts at most one snapshot id")
}

func connectShortcutParsers() map[string]func([]string) (string, json.RawMessage, bool, error) {
	return map[string]func([]string) (string, json.RawMessage, bool, error){
		"diff":                  parseConnectSnapshotDiff,
		"inspect":               parseConnectInspect,
		"network":               parseConnectNetwork,
		"jvm":                   parseConnectJVM,
		"jvm-jfr-start":         parseConnectJFRStart,
		"jvm-jfr-stop":          parseConnectJFRStop,
		"jvm-jfr-dump":          parseConnectJFRDump,
		"jvm-jfr-summary":       parseConnectJFRSummary,
		"jvm-flamegraph":        parseConnectJVMFlamegraph,
		"jvm-telemetry":         parseConnectJVMTelemetry,
		"jvm-heap-dump":         parseConnectJVMHeapDump,
		"jvm-mbean-read":        parseConnectJVMMBeanRead,
		"jvm-mbean-invoke":      parseConnectJVMMBeanInvoke,
		"telemetry":             parseConnectTelemetry,
		"metrics":               parseConnectMetrics,
		"network-capture-start": parseConnectNetworkCaptureStart,
		"netcap-start":          parseConnectNetworkCaptureStart,
		"network-capture-stop":  parseConnectNetworkCaptureStop,
		"netcap-stop":           parseConnectNetworkCaptureStop,
		"sample":                parseConnectSample,
		"storage":               parseConnectStorage,
		"rules":                 parseConnectRules,
		"issues":                parseConnectIssues,
		"snapshot":              parseConnectSnapshot,
	}
}

func parseConnectJVM(args []string) (string, json.RawMessage, bool, error) {
	if len(args) == 1 {
		return "jvm.list", nil, true, nil
	}
	if method, params, ok := parseConnectJVMPIDInspect(args); ok {
		return method, params, true, nil
	}
	if parser, ok := connectJVMSubcommands()[args[1]]; ok {
		return parser(args)
	}
	return "", nil, true, fmt.Errorf("unknown jvm subcommand %q", args[1])
}

func parseConnectJVMPIDInspect(args []string) (string, json.RawMessage, bool) {
	if len(args) != 2 {
		return "", nil, false
	}
	pid, err := parseConnectPositiveInt(args[1], "pid")
	if err != nil {
		return "", nil, false
	}
	return "jvm.inspect", connectParams(map[string]int{"pid": pid}), true
}

func connectJVMSubcommands() map[string]func([]string) (string, json.RawMessage, bool, error) {
	nestedPID := map[string]string{
		"inspect":        "jvm.inspect",
		"gc-stats":       "jvm.gc_stats",
		"thread-dump":    "jvm.thread_dump",
		"threads":        "jvm.thread_dump",
		"heap-histogram": "jvm.heap_histogram",
		"heap":           "jvm.heap_histogram",
		"vm-memory":      "jvm.vm_memory",
		"attach":         "jvm.attach",
		"mbeans":         "jvm.mbeans",
		"probe":          "jvm.probe",
		"explain":        "jvm.explain",
	}
	out := make(map[string]func([]string) (string, json.RawMessage, bool, error), len(nestedPID)+7)
	out["list"] = parseConnectJVMList
	out["telemetry"] = func(args []string) (string, json.RawMessage, bool, error) {
		return parseConnectJVMTelemetry(append([]string{"jvm-telemetry"}, args[2:]...))
	}
	out["heap-dump"] = func(args []string) (string, json.RawMessage, bool, error) {
		return parseConnectJVMHeapDump(append([]string{"jvm-heap-dump"}, args[2:]...))
	}
	out["jmx"] = func(args []string) (string, json.RawMessage, bool, error) {
		return parseConnectJVMJMX(args)
	}
	out["mbean-read"] = func(args []string) (string, json.RawMessage, bool, error) {
		return parseConnectJVMMBeanRead(append([]string{"jvm-mbean-read"}, args[2:]...))
	}
	out["mbean-invoke"] = func(args []string) (string, json.RawMessage, bool, error) {
		return parseConnectJVMMBeanInvoke(append([]string{"jvm-mbean-invoke"}, args[2:]...))
	}
	out["flamegraph"] = func(args []string) (string, json.RawMessage, bool, error) {
		return parseConnectJVMFlamegraph(append([]string{"jvm-flamegraph"}, args[2:]...))
	}
	out["jfr"] = func(args []string) (string, json.RawMessage, bool, error) {
		return parseConnectJVMJFR(args)
	}
	for sub, method := range nestedPID {
		method := method
		out[sub] = func(args []string) (string, json.RawMessage, bool, error) {
			return parseConnectNestedPIDCall(args, method)
		}
	}
	return out
}

func parseConnectJVMList(args []string) (string, json.RawMessage, bool, error) {
	if len(args) != 2 {
		return "", nil, true, fmt.Errorf("connect jvm list takes no extra arguments")
	}
	return "jvm.list", nil, true, nil
}

func parseConnectJVMTelemetry(args []string) (string, json.RawMessage, bool, error) {
	if len(args) > 3 {
		return "", nil, true, fmt.Errorf("connect jvm-telemetry accepts optional [pid] [limit]")
	}
	params := map[string]int{}
	if len(args) >= 2 {
		pid, err := parseConnectPositiveInt(args[1], "pid")
		if err != nil {
			return "", nil, true, err
		}
		params["pid"] = pid
	}
	if len(args) == 3 {
		limit, err := parseConnectPositiveInt(args[2], "limit")
		if err != nil {
			return "", nil, true, err
		}
		params["limit"] = limit
	}
	if len(params) == 0 {
		return "jvm.telemetry.live", nil, true, nil
	}
	return "jvm.telemetry.live", connectParams(params), true, nil
}

func parseConnectTelemetry(args []string) (string, json.RawMessage, bool, error) {
	if len(args) > 3 {
		return "", nil, true, fmt.Errorf("connect telemetry accepts optional [pid] [limit]")
	}
	params := map[string]int{}
	if len(args) >= 2 {
		pid, err := parseConnectPositiveInt(args[1], "pid")
		if err != nil {
			return "", nil, true, err
		}
		params["pid"] = pid
	}
	if len(args) == 3 {
		limit, err := parseConnectPositiveInt(args[2], "limit")
		if err != nil {
			return "", nil, true, err
		}
		params["limit"] = limit
	}
	if len(params) == 0 {
		return "telemetry.live", nil, true, nil
	}
	return "telemetry.live", connectParams(params), true, nil
}

func parseConnectNestedPIDCall(args []string, method string) (string, json.RawMessage, bool, error) {
	if len(args) != 3 {
		return "", nil, true, fmt.Errorf("connect jvm %s requires <pid>", args[1])
	}
	pid, err := parseConnectPositiveInt(args[2], "pid")
	if err != nil {
		return "", nil, true, err
	}
	return method, connectParams(map[string]int{"pid": pid}), true, nil
}

func parseConnectJVMJMX(args []string) (string, json.RawMessage, bool, error) {
	if len(args) < 3 {
		return "", nil, true, fmt.Errorf("connect jvm jmx requires status or start-local")
	}
	switch args[2] {
	case "status":
		return parseConnectNestedSubPIDCall(args, "jvm.jmx.status")
	case "start-local":
		return parseConnectNestedSubPIDCall(args, "jvm.jmx.start_local")
	default:
		return "", nil, true, fmt.Errorf("unknown jvm jmx subcommand %q", args[2])
	}
}

func parseConnectNestedSubPIDCall(args []string, method string) (string, json.RawMessage, bool, error) {
	if len(args) != 4 {
		return "", nil, true, fmt.Errorf("connect jvm %s %s requires <pid>", args[1], args[2])
	}
	pid, err := parseConnectPositiveInt(args[3], "pid")
	if err != nil {
		return "", nil, true, err
	}
	return method, connectParams(map[string]int{"pid": pid}), true, nil
}

func parseConnectJVMJFR(args []string) (string, json.RawMessage, bool, error) {
	if len(args) < 3 {
		return "", nil, true, fmt.Errorf("connect jvm jfr requires start, dump, stop, or summary")
	}
	switch args[2] {
	case "start":
		return parseConnectJFRStart(append([]string{"jvm-jfr-start"}, args[3:]...))
	case "dump":
		return parseConnectJFRDump(append([]string{"jvm-jfr-dump"}, args[3:]...))
	case "stop":
		return parseConnectJFRStop(append([]string{"jvm-jfr-stop"}, args[3:]...))
	case "summary":
		return parseConnectJFRSummary(append([]string{"jvm-jfr-summary"}, args[3:]...))
	default:
		return "", nil, true, fmt.Errorf("unknown jvm jfr subcommand %q", args[2])
	}
}

func parseConnectJVMMBeanRead(args []string) (string, json.RawMessage, bool, error) {
	if len(args) != 4 {
		return "", nil, true, fmt.Errorf("connect jvm-mbean-read requires <pid> <name> <attribute>")
	}
	pid, err := parseConnectPositiveInt(args[1], "pid")
	if err != nil {
		return "", nil, true, err
	}
	return "jvm.mbean.read", connectParams(map[string]any{
		"pid":       pid,
		"name":      args[2],
		"attribute": args[3],
	}), true, nil
}

func parseConnectJVMMBeanInvoke(args []string) (string, json.RawMessage, bool, error) {
	if len(args) != 4 {
		return "", nil, true, fmt.Errorf("connect jvm-mbean-invoke requires <pid> <name> <operation>")
	}
	pid, err := parseConnectPositiveInt(args[1], "pid")
	if err != nil {
		return "", nil, true, err
	}
	return "jvm.mbean.invoke", connectParams(map[string]any{
		"pid":       pid,
		"name":      args[2],
		"operation": args[3],
	}), true, nil
}

func parseConnectJFRStart(args []string) (string, json.RawMessage, bool, error) {
	if len(args) < 2 || len(args) > 3 {
		return "", nil, true, fmt.Errorf("connect jvm-jfr-start requires <pid> [name]")
	}
	pid, err := parseConnectPositiveInt(args[1], "pid")
	if err != nil {
		return "", nil, true, err
	}
	params := map[string]any{"pid": pid}
	if len(args) == 3 {
		params["name"] = args[2]
	}
	return "jvm.jfr.start", connectParams(params), true, nil
}

func parseConnectJFRStop(args []string) (string, json.RawMessage, bool, error) {
	if len(args) < 2 || len(args) > 3 {
		return "", nil, true, fmt.Errorf("connect jvm-jfr-stop requires <pid> [dest]")
	}
	pid, err := parseConnectPositiveInt(args[1], "pid")
	if err != nil {
		return "", nil, true, err
	}
	params := map[string]any{"pid": pid}
	if len(args) == 3 {
		params["dest"] = args[2]
	}
	return "jvm.jfr.stop", connectParams(params), true, nil
}

func parseConnectJFRDump(args []string) (string, json.RawMessage, bool, error) {
	if len(args) < 3 || len(args) > 4 {
		return "", nil, true, fmt.Errorf("connect jvm-jfr-dump requires <pid> <dest> [name]")
	}
	pid, err := parseConnectPositiveInt(args[1], "pid")
	if err != nil {
		return "", nil, true, err
	}
	params := map[string]any{
		"pid":               pid,
		"dest":              args[2],
		"confirm_sensitive": true,
	}
	if len(args) == 4 {
		params["name"] = args[3]
	}
	return "jvm.jfr.dump", connectParams(params), true, nil
}

func parseConnectJFRSummary(args []string) (string, json.RawMessage, bool, error) {
	if len(args) != 2 {
		return "", nil, true, fmt.Errorf("connect jvm-jfr-summary requires <path>")
	}
	return "jvm.jfr.summary", connectParams(map[string]string{"path": args[1]}), true, nil
}

func parseConnectJVMHeapDump(args []string) (string, json.RawMessage, bool, error) {
	if len(args) < 2 || len(args) > 3 {
		return "", nil, true, fmt.Errorf("connect jvm-heap-dump requires <pid> [dest]")
	}
	pid, err := parseConnectPositiveInt(args[1], "pid")
	if err != nil {
		return "", nil, true, err
	}
	params := map[string]any{"pid": pid, "confirm_sensitive": true}
	if len(args) == 3 {
		params["dest"] = args[2]
	}
	return "jvm.heap_dump", connectParams(params), true, nil
}

func parseConnectJVMFlamegraph(args []string) (string, json.RawMessage, bool, error) {
	if len(args) < 2 || len(args) > 3 {
		return "", nil, true, fmt.Errorf("connect jvm-flamegraph requires <pid> [dest]")
	}
	pid, err := parseConnectPositiveInt(args[1], "pid")
	if err != nil {
		return "", nil, true, err
	}
	params := map[string]any{"pid": pid, "confirm_sensitive": true}
	if len(args) == 3 {
		params["dest"] = args[2]
	}
	return "jvm.flamegraph", connectParams(params), true, nil
}

func parseConnectIssues(args []string) (string, json.RawMessage, bool, error) {
	if len(args) < 2 {
		return "", nil, true, fmt.Errorf("connect issues requires a machine id or command")
	}
	switch args[1] {
	case "check":
		return parseConnectIssuesCheck(args)
	case "list":
		return parseConnectIssuesList(args)
	case "update":
		return parseConnectMachineIDOp(args, "issues.update")
	case "acknowledge":
		return parseConnectMachineIDOp(args, "issues.acknowledge")
	case "dismiss":
		return parseConnectMachineIDOp(args, "issues.dismiss")
	}
	return parseConnectIssuesListFallback(args)
}

func parseConnectIssuesCheck(args []string) (string, json.RawMessage, bool, error) {
	switch len(args) {
	case 2:
		return "issues.check", nil, true, nil
	case 3:
		return "issues.check", connectParams(map[string]string{"snapshot_id": args[2]}), true, nil
	default:
		return "", nil, true, fmt.Errorf("connect issues check accepts an optional snapshot id")
	}
}

func parseConnectIssuesList(args []string) (string, json.RawMessage, bool, error) {
	if len(args) < 3 {
		return "", nil, true, fmt.Errorf("connect issues list requires <machine-id> [status]")
	}
	if len(args) > 4 {
		return "", nil, true, fmt.Errorf("connect issues list supports at most one optional status")
	}
	params := map[string]string{"machine_uuid": args[2]}
	if len(args) == 4 {
		params["status"] = args[3]
	}
	return "issues.list", connectParams(params), true, nil
}

func parseConnectMachineIDOp(args []string, method string) (string, json.RawMessage, bool, error) {
	paramKey := "id"
	if method == "issues.update" {
		if len(args) != 4 {
			return "", nil, true, fmt.Errorf("connect issues update requires <issue-id> <status>")
		}
		return "issues.update", connectParams(map[string]string{"id": args[2], "status": args[3]}), true, nil
	}
	if len(args) != 3 {
		return "", nil, true, fmt.Errorf("connect %s %s requires <issue-id>", args[0], args[1])
	}
	return method, connectParams(map[string]string{paramKey: args[2]}), true, nil
}

func parseConnectIssuesListFallback(args []string) (string, json.RawMessage, bool, error) {
	switch len(args) {
	case 2:
		return "issues.list", connectParams(map[string]string{"machine_uuid": args[1]}), true, nil
	case 3:
		return "issues.list", connectParams(map[string]string{"machine_uuid": args[1], "status": args[2]}), true, nil
	default:
		return "", nil, true, fmt.Errorf("connect issues supports check [snapshot-id], list [machine-id [status]], update/acknowledge/dismiss <issue-id>")
	}
}

func parseConnectNetwork(args []string) (string, json.RawMessage, bool, error) {
	switch len(args) {
	case 1:
		return "network.state", nil, true, nil
	case 2:
		switch args[1] {
		case "state", "status":
			return "network.state", nil, true, nil
		case "connections", "conns":
			return "network.connections", nil, true, nil
		case "firewall":
			return "network.firewall", nil, true, nil
		case "by-app", "apps":
			return "network.byApp", nil, true, nil
		}
		return "", nil, true, fmt.Errorf("unknown network subcommand %q", args[1])
	case 3:
		switch args[1] {
		case "by-app", "apps":
			return parseConnectStringSliceCall(args[1:], "network.byApp", "bundles")
		}
	default:
		switch args[1] {
		case "by-app", "apps":
			return parseConnectStringSliceCall(args[1:], "network.byApp", "bundles")
		}
	}
	return "", nil, true, fmt.Errorf("network accepts state, connections, firewall, by-app, or no extra args")
}

func parseConnectCache(args []string) (string, json.RawMessage, bool, error) {
	if len(args) == 1 {
		return "cache.stats", nil, true, nil
	}
	if len(args) == 2 {
		switch args[1] {
		case "stats":
			return "cache.stats", nil, true, nil
		case "clear":
			return "cache.clear", nil, true, nil
		default:
			return "", nil, true, fmt.Errorf("connect cache supports `stats` and `clear`")
		}
	}
	if len(args) == 3 {
		return "cache.clear", connectParams(map[string]string{"kind": args[2]}), true, nil
	}
	return "", nil, true, fmt.Errorf("connect cache clear accepts at most one kind")
}

func parseConnectMetrics(args []string) (string, json.RawMessage, bool, error) {
	switch len(args) {
	case 1:
		return "process.live", nil, true, nil
	case 2:
		pid, err := parseConnectPositiveInt(args[1], "pid")
		if err != nil {
			return "", nil, true, err
		}
		return "process.history", connectParams(map[string]int{"pid": pid}), true, nil
	case 3:
		pid, err := parseConnectPositiveInt(args[1], "pid")
		if err != nil {
			return "", nil, true, err
		}
		limit, err := parseConnectPositiveInt(args[2], "limit")
		if err != nil {
			return "", nil, true, err
		}
		return "process.history", connectParams(map[string]int{"pid": pid, "limit": limit}), true, nil
	default:
		return "", nil, true, fmt.Errorf("connect metrics accepts optional pid and limit only")
	}
}

func parseConnectNetworkCaptureStart(args []string) (string, json.RawMessage, bool, error) {
	if len(args) < 2 {
		return "", nil, true, fmt.Errorf("connect %s requires <iface>", args[0])
	}
	params := map[string]any{"interface": args[1]}
	for _, raw := range args[2:] {
		key, value, ok := strings.Cut(raw, "=")
		if !ok || value == "" {
			return "", nil, true, fmt.Errorf("connect %s optional args must be key=value", args[0])
		}
		switch key {
		case "duration_ms", "snap_len", "port":
			n, err := parseConnectPositiveInt(value, key)
			if err != nil {
				return "", nil, true, err
			}
			params[key] = n
		case "proto":
			if value != "tcp" && value != "udp" {
				return "", nil, true, fmt.Errorf("connect %s proto must be tcp or udp", args[0])
			}
			params[key] = value
		case "host":
			params[key] = value
		default:
			return "", nil, true, fmt.Errorf("connect %s unknown option %q", args[0], key)
		}
	}
	return "helper.net_capture.start", connectParams(params), true, nil
}

func parseConnectNetworkCaptureStop(args []string) (string, json.RawMessage, bool, error) {
	if len(args) != 2 {
		return "", nil, true, fmt.Errorf("connect %s requires <handle>", args[0])
	}
	return "helper.net_capture.stop", connectParams(map[string]string{"handle": args[1]}), true, nil
}

func connectPIDShortcuts() map[string]string {
	return map[string]string{
		"jvm-gc":              "jvm.gc_stats",
		"jvm-attach":          "jvm.attach",
		"jvm-explain":         "jvm.explain",
		"jvm-heap":            "jvm.heap_histogram",
		"jvm-heap-histogram":  "jvm.heap_histogram",
		"jvm-jmx-start-local": "jvm.jmx.start_local",
		"jvm-jmx-status":      "jvm.jmx.status",
		"jvm-mbeans":          "jvm.mbeans",
		"jvm-probe":           "jvm.probe",
		"jvm-thread-dump":     "jvm.thread_dump",
		"jvm-threads":         "jvm.thread_dump",
		"jvm-vm-memory":       "jvm.vm_memory",
	}
}

type connectStringSliceShortcut struct {
	method   string
	paramKey string
}

func connectStringSliceShortcuts() map[string]connectStringSliceShortcut {
	return map[string]connectStringSliceShortcut{
		"network-apps":   {method: "network.byApp", paramKey: "bundles"},
		"network-by-app": {method: "network.byApp", paramKey: "bundles"},
		"process":        {method: "process.list", paramKey: "bundles"},
		"process-tree":   {method: "process.tree", paramKey: "bundles"},
		"processes":      {method: "process.list", paramKey: "bundles"},
		"tree":           {method: "process.tree", paramKey: "bundles"},
	}
}

func connectNoArgShortcuts() map[string]string {
	return map[string]string{
		"build-tools":         "toolchain.build_tools",
		"brew":                "toolchain.brew",
		"cache-stats":         "cache.stats",
		"connections":         "network.connections",
		"firewall":            "network.firewall",
		"health":              "health",
		"host":                "inspect.host",
		"inspect-host":        "inspect.host",
		"jdk":                 "jdk.list",
		"jdk-scan":            "jdk.scan",
		"jdks":                "jdk.list",
		"network-connections": "network.connections",
		"power":               "power.state",
		"runtimes":            "toolchain.runtimes",
		"snapshot-create":     "snapshot.create",
		"snapshot-list":       "snapshot.list",
		"snapshots":           "snapshot.list",
		"toolchain":           "toolchain.scan",
		"toolchains":          "toolchain.scan",
	}
}

func parseConnectInspect(args []string) (string, json.RawMessage, bool, error) {
	if len(args) != 2 {
		return "", nil, true, fmt.Errorf("connect inspect requires <App.app>")
	}
	return "inspect.app", connectParams(map[string]string{"path": args[1]}), true, nil
}

func parseConnectPIDCall(args []string, method string) (string, json.RawMessage, bool, error) {
	if len(args) != 2 {
		return "", nil, true, fmt.Errorf("connect %s requires <pid>", args[0])
	}
	pid, err := parseConnectPositiveInt(args[1], "pid")
	if err != nil {
		return "", nil, true, err
	}
	return method, connectParams(map[string]int{"pid": pid}), true, nil
}

func parseConnectSample(args []string) (string, json.RawMessage, bool, error) {
	if len(args) < 2 || len(args) > 4 {
		return "", nil, true, fmt.Errorf("connect sample requires <pid> [duration] [interval]")
	}
	pid, err := parseConnectPositiveInt(args[1], "pid")
	if err != nil {
		return "", nil, true, err
	}
	params := map[string]int{"pid": pid}
	if len(args) >= 3 {
		duration, err := parseConnectPositiveInt(args[2], "duration")
		if err != nil {
			return "", nil, true, err
		}
		params["duration"] = duration
	}
	if len(args) == 4 {
		interval, err := parseConnectPositiveInt(args[3], "interval")
		if err != nil {
			return "", nil, true, err
		}
		params["interval"] = interval
	}
	return "process.sample", connectParams(params), true, nil
}

func parseConnectStringSliceCall(args []string, method string, key string) (string, json.RawMessage, bool, error) {
	if len(args) == 1 {
		return method, nil, true, nil
	}
	return method, connectParams(map[string][]string{key: args[1:]}), true, nil
}

func parseConnectSnapshot(args []string) (string, json.RawMessage, bool, error) {
	if len(args) == 1 {
		return "snapshot.create", nil, true, nil
	}
	switch args[1] {
	case "create":
		return connectNoArgSubcommand(args, "snapshot.create")
	case "list":
		return connectNoArgSubcommand(args, "snapshot.list")
	case "get", "show":
		if len(args) != 3 {
			return "", nil, true, fmt.Errorf("connect snapshot %s requires <id>", args[1])
		}
		return "snapshot.get", connectParams(map[string]string{"ID": args[2]}), true, nil
	case "diff":
		if len(args) != 4 {
			return "", nil, true, fmt.Errorf("connect snapshot diff requires <id-a> <id-b>")
		}
		return "snapshot.diff", connectParams(map[string]string{"id_a": args[2], "id_b": args[3]}), true, nil
	case "processes":
		return connectSnapshotIDCall(args, "snapshot.processes")
	case "login-items", "login_items":
		return connectSnapshotIDCall(args, "snapshot.login_items")
	case "granted-perms", "granted_perms":
		return connectSnapshotIDCall(args, "snapshot.granted_perms")
	case "prune":
		return parseConnectSnapshotPrune(args)
	default:
		return "", nil, true, fmt.Errorf("unknown snapshot subcommand %q", args[1])
	}
}

func connectNoArgSubcommand(args []string, method string) (string, json.RawMessage, bool, error) {
	if len(args) != 2 {
		return "", nil, true, fmt.Errorf("connect snapshot %s takes no extra arguments", args[1])
	}
	return method, nil, true, nil
}

func connectSnapshotIDCall(args []string, method string) (string, json.RawMessage, bool, error) {
	if len(args) != 3 {
		return "", nil, true, fmt.Errorf("connect snapshot %s requires <id>", args[1])
	}
	return method, connectParams(map[string]string{"id": args[2]}), true, nil
}

func parseConnectSnapshotPrune(args []string) (string, json.RawMessage, bool, error) {
	if len(args) == 2 {
		return "snapshot.prune", nil, true, nil
	}
	if len(args) != 3 {
		return "", nil, true, fmt.Errorf("connect snapshot prune accepts at most one keep count")
	}
	keep, err := parseConnectPositiveInt(args[2], "keep")
	if err != nil {
		return "", nil, true, err
	}
	return "snapshot.prune", connectParams(map[string]int{"keep": keep}), true, nil
}

func parseConnectPositiveInt(raw string, name string) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid %s %q", name, raw)
	}
	return value, nil
}

func connectParams(value any) json.RawMessage {
	params, _ := json.Marshal(value)
	return json.RawMessage(params)
}

func parseConnectGenericCall(args []string) (string, json.RawMessage, error) {
	if args[0] != "call" || len(args) < 2 || len(args) > 3 {
		return "", nil, fmt.Errorf("invalid connect command")
	}
	if len(args) == 2 {
		return args[1], nil, nil
	}
	var params json.RawMessage
	if err := json.Unmarshal([]byte(args[2]), &params); err != nil {
		return "", nil, fmt.Errorf("invalid json params: %w", err)
	}
	return args[1], params, nil
}

func dialConnectTarget(target connectTarget, timeout time.Duration) (io.ReadWriteCloser, error) {
	var d net.Dialer
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return d.DialContext(ctx, target.Network, target.Address)
}

func callRPC(rw io.ReadWriter, method string, params json.RawMessage) (json.RawMessage, error) {
	req := rpc.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  method,
		Params:  params,
	}
	if len(req.Params) == 0 {
		req.Params = nil
	}
	if err := json.NewEncoder(rw).Encode(req); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	var resp struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *rpc.RPCError   `json:"error,omitempty"`
	}
	if err := json.NewDecoder(rw).Decode(&resp); err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("daemon error: %s", resp.Error.Message)
	}
	if len(resp.Result) == 0 {
		return json.RawMessage(`null`), nil
	}
	return resp.Result, nil
}
