package runtimeattach

import "testing"

func noProbes() Probes {
	return Probes{GoBinary: func(string) bool { return false }, DotnetSocket: func(int) bool { return false }}
}

func TestClassifyRuntimes(t *testing.T) {
	goProbe := Probes{GoBinary: func(string) bool { return true }, DotnetSocket: func(int) bool { return false }}
	dotnetProbe := Probes{GoBinary: func(string) bool { return false }, DotnetSocket: func(int) bool { return true }}

	cases := []struct {
		name  string
		proc  Process
		probe Probes
		want  Runtime
	}{
		{"java exe + flags", Process{ExecutablePath: "/usr/bin/java", CommandLine: "java -Xmx2g -jar app.jar"}, noProbes(), RuntimeJVM},
		{"jdk path", Process{ExecutablePath: "/Library/Java/JavaVirtualMachines/temurin-21.jdk/Contents/Home/bin/java"}, noProbes(), RuntimeJVM},
		{"electron renderer", Process{ExecutablePath: "/Applications/Foo.app/Contents/MacOS/Foo", CommandLine: "Foo --type=renderer"}, noProbes(), RuntimeElectron},
		{"electron framework", Process{ExecutablePath: "/Applications/Foo.app/Contents/Frameworks/Electron Framework.framework/Electron"}, noProbes(), RuntimeElectron},
		{"electron utility child", Process{ExecutablePath: "/Applications/Foo.app/Contents/MacOS/Foo Helper", CommandLine: "Foo Helper --type=utility --utility-sub-type=network.mojom.NetworkService"}, noProbes(), RuntimeElectron},
		{"node exe", Process{ExecutablePath: "/usr/local/bin/node", CommandLine: "node server.js"}, noProbes(), RuntimeNode},
		{"node inspect", Process{ExecutablePath: "/opt/x", CommandLine: "x --inspect=9229"}, noProbes(), RuntimeNode},
		{"dotnet exe", Process{ExecutablePath: "/usr/local/share/dotnet/dotnet", CommandLine: "dotnet App.dll"}, noProbes(), RuntimeDotNet},
		{"dotnet socket", Process{ExecutablePath: "/opt/app/App", PID: 42}, dotnetProbe, RuntimeDotNet},
		{"python", Process{ExecutablePath: "/usr/bin/python3.11", CommandLine: "python3 run.py"}, noProbes(), RuntimePython},
		{"go binary", Process{ExecutablePath: "/opt/svc/server"}, goProbe, RuntimeGo},
		{"native", Process{ExecutablePath: "/bin/zsh", CommandLine: "-zsh"}, noProbes(), RuntimeNative},
	}
	for _, c := range cases {
		got := Classify(c.proc, c.probe)
		if got.Runtime != c.want {
			t.Errorf("%s: runtime = %s, want %s (evidence %q)", c.name, got.Runtime, c.want, got.Evidence)
		}
		if got.Evidence == "" {
			t.Errorf("%s: expected evidence", c.name)
		}
	}
}

func TestDotnetSocketWinsOverGoBinary(t *testing.T) {
	// A dotnet host is itself a native binary; the socket must decide first.
	probe := Probes{GoBinary: func(string) bool { return true }, DotnetSocket: func(int) bool { return true }}
	if got := Classify(Process{PID: 7, ExecutablePath: "/x"}, probe); got.Runtime != RuntimeDotNet {
		t.Errorf("runtime = %s, want dotnet", got.Runtime)
	}
}

func TestCapabilitiesAlwaysIncludeSample(t *testing.T) {
	for _, r := range []Runtime{RuntimeJVM, RuntimeNode, RuntimeElectron, RuntimeGo, RuntimeDotNet, RuntimePython, RuntimeNative} {
		caps := capabilities(r, 1234)
		if len(caps) == 0 {
			t.Fatalf("%s: no capabilities", r)
		}
		found := false
		for _, c := range caps {
			if c.How == "spectra sample 1234" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: capabilities missing the universal sample fallback", r)
		}
		// The universal sample fallback is always listed last, per the contract.
		if caps[len(caps)-1].How != "spectra sample 1234" {
			t.Errorf("%s: sample fallback should be last, got %q", r, caps[len(caps)-1].How)
		}
	}
}

func TestCapabilitiesRouteJVMAndElectron(t *testing.T) {
	jvm := capabilities(RuntimeJVM, 5)
	if jvm[0].How[:12] != "spectra jvm " {
		t.Errorf("jvm should route to `spectra jvm`, got %q", jvm[0].How)
	}
	el := capabilities(RuntimeElectron, 5)
	if el[0].How != "spectra web processes" {
		t.Errorf("electron should route to web processes, got %q", el[0].How)
	}
}
