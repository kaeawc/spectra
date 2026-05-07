package playbook

func defaultPlaybooks() []Playbook {
	return []Playbook{
		jvmMemory(),
		networkFailure(),
		storageBloat(),
		remoteTriage(),
		toolchainDrift(),
	}
}

func jvmMemory() Playbook {
	return Playbook{
		ID:          "jvm-memory",
		Title:       "JVM memory",
		Symptom:     "Java app is slow, memory-heavy, GC-bound, or suspected of leaking heap, metaspace, or classloaders.",
		Description: "Start with process discovery, move to interpreted JVM findings, then collect heap, VM memory, JFR, or flamegraph evidence only when the target can tolerate it.",
		Steps: []Step{
			{
				ID:      "identify",
				Title:   "Identify the target JVM",
				Purpose: "Find the PID and basic JVM metadata before collecting heavier diagnostics.",
				Commands: []Command{
					{Args: []string{"jvm"}, Description: "List running JVMs"},
					{Args: []string{"jvm", "--json"}, Description: "List running JVMs as structured data"},
				},
				Signals: []Signal{
					{Name: "Missing JVM", Meaning: "The app may not be Java-based, may be running under another user, or may require helper visibility."},
				},
			},
			{
				ID:      "explain",
				Title:   "Interpret memory pressure",
				Purpose: "Join JVM args, GC counters, heap, metaspace, classloader, and code cache sections into findings.",
				Commands: []Command{
					{Args: []string{"jvm", "<pid>"}, Description: "Inspect one JVM"},
					{Args: []string{"jvm", "explain", "<pid>"}, Description: "Generate interpreted findings"},
					{Args: []string{"jvm", "explain", "--samples", "5", "--interval", "10s", "<pid>"}, Description: "Sample repeatedly when growth matters"},
				},
				Signals: []Signal{
					{Name: "Heap used near max", Meaning: "Java objects are pressuring -Xmx; compare with heap histogram."},
					{Name: "Metaspace or classloader growth", Meaning: "Plugin reloads, dynamic proxies, or classloader leaks are possible."},
					{Name: "NMT unavailable", Meaning: "The JVM was not started with Native Memory Tracking; other VM sections still matter."},
				},
			},
			{
				ID:      "capture",
				Title:   "Capture deeper evidence",
				Purpose: "Collect targeted artifacts for throughput, allocation, lock, or object-retention analysis.",
				Commands: []Command{
					{Args: []string{"jvm", "heap-histogram", "<pid>"}, Description: "Summarize live object counts"},
					{Args: []string{"jvm", "vm-memory", "<pid>"}, Description: "Inspect VM-internal memory sections"},
					{Args: []string{"jvm", "jfr", "start", "<pid>", "--name", "incident"}, Description: "Start a JFR recording"},
					{Args: []string{"jvm", "flamegraph", "--event", "cpu", "--duration", "30", "--out", "~/Desktop/cpu.html", "<pid>"}, Description: "Capture an async-profiler flamegraph"},
					{Args: []string{"jvm", "heap-dump", "--out", "~/Desktop/app.hprof", "<pid>"}, Description: "Capture a heap dump", Destructive: true},
				},
			},
		},
		References: []Reference{
			{Title: "JVM inspection", Path: "docs/inspection/jvm.md"},
			{Title: "Toolchains", Path: "docs/inspection/toolchains.md"},
			{Title: "Remote operations", Path: "docs/operations/remote.md"},
		},
	}
}

func networkFailure() Playbook {
	return Playbook{
		ID:          "network-failure",
		Title:       "Network failure",
		Symptom:     "An app cannot reach a service, connects to the wrong host, or is affected by VPN, proxy, DNS, or firewall state.",
		Description: "Capture machine network state, narrow to one running app or endpoint, then use bounded packet evidence only when needed.",
		Steps: []Step{
			{
				ID:      "machine",
				Title:   "Capture machine network state",
				Purpose: "Record routes, DNS, VPN, proxies, listening ports, and per-process throughput.",
				Commands: []Command{
					{Args: []string{"network"}, Description: "Show current network state"},
					{Args: []string{"network", "--json"}, Description: "Emit current network state as JSON"},
				},
				Signals: []Signal{
					{Name: "Unexpected DNS or proxy", Meaning: "Name resolution or interception may explain the symptom before app-level probing."},
				},
			},
			{
				ID:      "app",
				Title:   "Diagnose the app or endpoint",
				Purpose: "Join live sockets, throughput, DNS, route, proxy, TCP/TLS probes, and traceroute output.",
				Commands: []Command{
					{Args: []string{"network", "diagnose", "--app", "/Applications/App.app"}, Description: "Diagnose one app bundle"},
					{Args: []string{"network", "diagnose", "--pid", "<pid>"}, Description: "Diagnose one process"},
					{Args: []string{"network", "diagnose", "--app", "/Applications/App.app", "--ports", "443", "api.example.com"}, Description: "Probe a narrowed endpoint set"},
					{Args: []string{"--network", "-v", "/Applications/App.app"}, Description: "Extract static endpoint references from an app bundle"},
				},
				Signals: []Signal{
					{Name: "TCP connect fails", Meaning: "Check route, VPN, firewall, and endpoint port."},
					{Name: "TLS issuer is unexpected", Meaning: "Inspect proxy or interception policy."},
					{Name: "No live sockets", Meaning: "Confirm the app is exercising the failing workflow."},
				},
			},
			{
				ID:      "capture",
				Title:   "Capture bounded packet evidence",
				Purpose: "Use helper-backed tcpdump captures with narrow filters.",
				Commands: []Command{
					{Args: []string{"network", "capture", "start", "--interface", "en0", "--duration", "30s", "--proto", "tcp", "--host", "api.example.com", "--port", "443"}, Description: "Start a bounded packet capture"},
					{Args: []string{"network", "capture", "stop", "--summarize", "netcap-1"}, Description: "Stop and summarize a capture"},
					{Args: []string{"network", "firewall"}, Description: "Show firewall rules"},
				},
			},
		},
		References: []Reference{
			{Title: "Network endpoints", Path: "docs/inspection/network-endpoints.md"},
			{Title: "Live data sources", Path: "docs/inspection/live-data-sources.md"},
			{Title: "CLI network commands", Path: "docs/operations/cli.md#spectra-network"},
		},
	}
}

func storageBloat() Playbook {
	return Playbook{
		ID:          "storage-bloat",
		Title:       "Storage bloat",
		Symptom:     "Disk is filling up, an app is unexpectedly large, or state is spread across Library locations.",
		Description: "Start with per-app storage, expand to system storage, and compare snapshots when the question is temporal.",
		Steps: []Step{
			{
				ID:      "app",
				Title:   "Inspect one app",
				Purpose: "Attribute app storage across Application Support, Caches, Containers, Group Containers, HTTPStorages, WebKit, Logs, and Preferences.",
				Commands: []Command{
					{Args: []string{"-v", "/Applications/App.app"}, Description: "Inspect one app with storage footprint"},
					{Args: []string{"--json", "/Applications/App.app"}, Description: "Emit app result as JSON"},
				},
				Signals: []Signal{
					{Name: "Containers dominates", Meaning: "The app is likely sandboxed or stores most state inside its sandbox container."},
					{Name: "Caches dominates", Meaning: "Data may be disposable, but app-specific cache safety still matters."},
				},
			},
			{
				ID:      "system",
				Title:   "Check system storage",
				Purpose: "Find volume pressure, user Library totals, and largest app bundles.",
				Commands: []Command{
					{Args: []string{"storage"}, Description: "Show disk volumes and Library footprint"},
					{Args: []string{"storage", "--json"}, Description: "Emit storage state as JSON"},
					{Args: []string{"snapshot", "--baseline", "pre-incident"}, Description: "Save a baseline snapshot"},
					{Args: []string{"diff", "baseline", "pre-incident", "live"}, Description: "Compare baseline with live state"},
				},
			},
			{
				ID:      "runtime",
				Title:   "Correlate with runtime",
				Purpose: "Connect growth to currently running processes.",
				Commands: []Command{
					{Args: []string{"process"}, Description: "List running processes sorted by RSS"},
					{Args: []string{"connect", "work-mac", "storage", "/Applications/App.app"}, Description: "Inspect app storage on a remote Mac", Remote: true},
				},
			},
		},
		References: []Reference{
			{Title: "Storage footprint", Path: "docs/inspection/storage-footprint.md"},
			{Title: "Storage design", Path: "docs/design/storage.md"},
			{Title: "CLI storage commands", Path: "docs/operations/cli.md"},
		},
	}
}

func remoteTriage() Playbook {
	return Playbook{
		ID:          "remote-triage",
		Title:       "Remote triage",
		Symptom:     "A teammate's Mac is slow, failing a workflow, or behaving differently from a known-good machine.",
		Description: "Establish an explicit daemon target, collect a broad first pass, then narrow by symptom or compare snapshots.",
		Steps: []Step{
			{
				ID:      "target",
				Title:   "Establish the target",
				Purpose: "Confirm the daemon is reachable over a trusted local, SSH, TCP, or tsnet path.",
				Commands: []Command{
					{Args: []string{"serve", "--tcp", "127.0.0.1:7878"}, Description: "Run a local loopback daemon"},
					{Args: []string{"serve", "--tsnet"}, Description: "Run a daemon that joins the tailnet"},
					{Args: []string{"connect", "work-mac"}, Description: "Health check a remote target", Remote: true},
					{Args: []string{"connect", "work-mac", "snapshot"}, Description: "Capture a remote snapshot", Remote: true},
				},
			},
			{
				ID:      "first-pass",
				Title:   "Collect the broad view",
				Purpose: "Separate machine-wide symptoms from app-specific symptoms.",
				Commands: []Command{
					{Args: []string{"connect", "work-mac", "processes"}, Description: "List remote processes", Remote: true},
					{Args: []string{"connect", "work-mac", "network"}, Description: "Show remote network state", Remote: true},
					{Args: []string{"connect", "work-mac", "storage"}, Description: "Show remote storage state", Remote: true},
					{Args: []string{"connect", "work-mac", "toolchains"}, Description: "Show remote toolchain inventory", Remote: true},
					{Args: []string{"connect", "work-mac", "jvm"}, Description: "List remote JVMs", Remote: true},
				},
			},
			{
				ID:      "compare",
				Title:   "Compare against a baseline",
				Purpose: "Turn works-on-my-machine claims into snapshot diffs.",
				Commands: []Command{
					{Args: []string{"snapshot", "--baseline", "local-good"}, Description: "Save a local known-good baseline"},
					{Args: []string{"diff", "local-good", "work-mac"}, Description: "Diff two snapshots"},
					{Args: []string{"fan", "--hosts", "alice-laptop,bob-laptop", "snapshot"}, Description: "Capture snapshots across multiple targets", Remote: true},
				},
			},
		},
		References: []Reference{
			{Title: "Remote operations", Path: "docs/operations/remote.md"},
			{Title: "Daemon operations", Path: "docs/operations/daemon.md"},
			{Title: "Threat model", Path: "docs/design/threat-model.md"},
		},
	}
}

func toolchainDrift() Playbook {
	return Playbook{
		ID:          "toolchain-drift",
		Title:       "Toolchain drift",
		Symptom:     "A build, test, JVM, package manager, or language runtime works on one Mac but fails on another.",
		Description: "Collect local inventory, compare remote hosts or snapshots, and interpret version, vendor, manager, and PATH differences.",
		Steps: []Step{
			{
				ID:      "local",
				Title:   "Collect local inventory",
				Purpose: "Record language runtimes, JDKs, build tools, Homebrew, managers, and environment paths.",
				Commands: []Command{
					{Args: []string{"toolchain"}, Description: "Show full toolchain inventory"},
					{Args: []string{"toolchain", "--json"}, Description: "Emit full toolchain inventory as JSON"},
					{Args: []string{"toolchain", "brew", "--json"}, Description: "Emit Homebrew inventory as JSON"},
					{Args: []string{"toolchain", "jdks", "--json"}, Description: "Emit JDK inventory as JSON"},
				},
				Signals: []Signal{
					{Name: "PATH order differs", Meaning: "A different binary may be selected even when both machines have the tool."},
					{Name: "JDK vendor differs", Meaning: "Compiler, TLS, GC, and runtime behavior may diverge."},
				},
			},
			{
				ID:      "compare",
				Title:   "Compare machines",
				Purpose: "Use fan-out or snapshots when drift spans hosts or time.",
				Commands: []Command{
					{Args: []string{"fan", "--hosts", "alice-laptop,bob-laptop", "toolchains"}, Description: "Collect toolchains across hosts", Remote: true},
					{Args: []string{"fan", "--hosts", "alice-laptop,bob-laptop", "jdk"}, Description: "Collect JDKs across hosts", Remote: true},
					{Args: []string{"snapshot", "--baseline", "before-upgrade"}, Description: "Save a pre-change baseline"},
					{Args: []string{"diff", "baseline", "before-upgrade", "live"}, Description: "Compare a baseline with live state"},
				},
			},
		},
		References: []Reference{
			{Title: "Toolchains", Path: "docs/inspection/toolchains.md"},
			{Title: "System inventory", Path: "docs/design/system-inventory.md"},
			{Title: "CLI toolchain commands", Path: "docs/operations/cli.md"},
		},
	}
}
