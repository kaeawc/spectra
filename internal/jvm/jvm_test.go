package jvm

import (
	"context"
	"fmt"
	"testing"

	"github.com/kaeawc/spectra/internal/toolchain"
)

// --- jps parsing ---

func TestParseJPS(t *testing.T) {
	out := `12345 com.example.Main
23456 org.gradle.launcher.daemon.bootstrap.GradleDaemon
34567
56789 sun.tools.jps.Jps
`
	got := parseJPS(out)
	if len(got) != 3 {
		t.Fatalf("expected 3 entries (jps itself excluded), got %d: %v", len(got), got)
	}
	if got[12345] != "com.example.Main" {
		t.Errorf("12345: got %q", got[12345])
	}
	if got[23456] != "org.gradle.launcher.daemon.bootstrap.GradleDaemon" {
		t.Errorf("23456: got %q", got[23456])
	}
	if got[34567] != "" {
		t.Errorf("34567 (no main): got %q", got[34567])
	}
	if _, ok := got[56789]; ok {
		t.Error("sun.tools.jps.Jps should be excluded")
	}
}

func TestParseJPSEmpty(t *testing.T) {
	if got := parseJPS(""); len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

// --- system properties parsing ---

func TestParseSysProps(t *testing.T) {
	out := `java.home=/Library/Java/JavaVirtualMachines/temurin-21.jdk/Contents/Home
java.vendor=Eclipse Adoptium
java.version=21.0.6
java.runtime.version=21.0.6+7-LTS
os.arch=aarch64
user.home=/Users/alice
java.class.path=/some/long/path  <- not in allowlist
ignored.key=value
`
	got := parseSysProps(out)
	if got["java.home"] != "/Library/Java/JavaVirtualMachines/temurin-21.jdk/Contents/Home" {
		t.Errorf("java.home: %q", got["java.home"])
	}
	if got["java.vendor"] != "Eclipse Adoptium" {
		t.Errorf("java.vendor: %q", got["java.vendor"])
	}
	if got["java.version"] != "21.0.6" {
		t.Errorf("java.version: %q", got["java.version"])
	}
	if _, ok := got["java.class.path"]; ok {
		t.Error("java.class.path should be filtered out (not in allowlist)")
	}
	if _, ok := got["ignored.key"]; ok {
		t.Error("ignored.key should be filtered out")
	}
}

func TestParseSysPropsNilOnEmpty(t *testing.T) {
	if got := parseSysProps("no matching lines here"); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

// --- command line parsing ---

func TestParseCommandLine(t *testing.T) {
	out := `VM Arguments:
jvm_args: -Xmx4g -Xms256m -XX:+UseG1GC
java_command: com.example.Main arg1
java_class_path (initial): /path/to/app.jar
Launcher Type: SUN_STANDARD
`
	flags, args := parseCommandLine(out)
	if args != "-Xmx4g -Xms256m -XX:+UseG1GC" {
		t.Errorf("vmArgs: %q", args)
	}
	if flags != "" {
		t.Errorf("vmFlags expected empty, got %q", flags)
	}
}

func TestParseCommandLineWithFlags(t *testing.T) {
	out := `VM Arguments:
jvm_args: -Xmx2g
VM Flags: -XX:InitialHeapSize=268435456 -XX:MaxHeapSize=2147483648
java_command: some.App
`
	flags, args := parseCommandLine(out)
	if args != "-Xmx2g" {
		t.Errorf("vmArgs: %q", args)
	}
	if flags != "-XX:InitialHeapSize=268435456 -XX:MaxHeapSize=2147483648" {
		t.Errorf("vmFlags: %q", flags)
	}
}

// --- thread count ---

func TestCountThreads(t *testing.T) {
	out := `2024-05-04 00:00:00
Full thread dump OpenJDK 64-Bit Server VM (21.0.6+7-LTS mixed mode, sharing):

"main" #1 prio=5 os_prio=31 cpu=320.93ms elapsed=3723.12s tid=0x00007f... nid=0x203 waiting on condition [0x70000...]
   java.lang.Thread.State: TIMED_WAITING (sleeping)

"Reference Handler" #2 daemon prio=10 os_prio=31 cpu=0.09ms elapsed=3723.11s tid=0x... nid=0x... waiting on condition [0x...]
   java.lang.Thread.State: RUNNABLE

"Finalizer" #3 daemon prio=8 os_prio=31 cpu=0.16ms elapsed=3723.11s tid=... nid=... in Object.wait() [...]

JNI global refs: 42, weak refs: 1
`
	n := countThreads(out)
	if n != 3 {
		t.Errorf("expected 3 threads, got %d", n)
	}
}

// --- ThreadDump / HeapHistogram / HeapDump ---

func TestThreadDumpFakeRunner(t *testing.T) {
	want := "\"main\" #1 prio=5\n\"GC\" #2\n"
	run := func(name string, args ...string) ([]byte, error) {
		if name == "jcmd" && len(args) == 2 && args[1] == "Thread.print" {
			return []byte(want), nil
		}
		return nil, fmt.Errorf("unexpected: %s %v", name, args)
	}
	got, err := ThreadDump(42, run)
	if err != nil {
		t.Fatalf("ThreadDump: %v", err)
	}
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHeapHistogramFakeRunner(t *testing.T) {
	want := " num     #instances         #bytes  class name\n   1:          1234       9876543  [B\n"
	run := func(name string, args ...string) ([]byte, error) {
		if name == "jcmd" && len(args) == 2 && args[1] == "GC.class_histogram" {
			return []byte(want), nil
		}
		return nil, fmt.Errorf("unexpected: %s %v", name, args)
	}
	got, err := HeapHistogram(42, run)
	if err != nil {
		t.Fatalf("HeapHistogram: %v", err)
	}
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHeapDumpFakeRunner(t *testing.T) {
	var capturedArgs []string
	run := func(name string, args ...string) ([]byte, error) {
		if name == "jcmd" {
			capturedArgs = args
			return []byte("Heap dump file created"), nil
		}
		return nil, fmt.Errorf("unexpected: %s %v", name, args)
	}
	if err := HeapDump(42, "/tmp/test.hprof", run); err != nil {
		t.Fatalf("HeapDump: %v", err)
	}
	if len(capturedArgs) != 3 || capturedArgs[1] != "GC.heap_dump" || capturedArgs[2] != "/tmp/test.hprof" {
		t.Errorf("unexpected jcmd args: %v", capturedArgs)
	}
}

func TestThreadDumpNilDefaultRunner(t *testing.T) {
	// nil runner should not panic (uses DefaultRunner which may fail if no jcmd).
	// We just verify it doesn't panic — error is expected on machines without a JDK.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("ThreadDump panicked: %v", r)
		}
	}()
	_, _ = ThreadDump(0, nil)
}

// --- JFR ---

func TestJFRStartFakeRunner(t *testing.T) {
	var capturedArgs []string
	run := func(name string, args ...string) ([]byte, error) {
		capturedArgs = args
		return []byte("Started recording 1 with name spectra"), nil
	}
	if err := JFRStart(42, "spectra", run); err != nil {
		t.Fatalf("JFRStart: %v", err)
	}
	if len(capturedArgs) < 3 || capturedArgs[1] != "JFR.start" || capturedArgs[2] != "name=spectra" {
		t.Errorf("unexpected jcmd args: %v", capturedArgs)
	}
}

func TestJFRDumpFakeRunner(t *testing.T) {
	var capturedArgs []string
	run := func(name string, args ...string) ([]byte, error) {
		capturedArgs = args
		return []byte("Dumped recording spectra to /tmp/test.jfr"), nil
	}
	if err := JFRDump(42, "spectra", "/tmp/test.jfr", run); err != nil {
		t.Fatalf("JFRDump: %v", err)
	}
	if len(capturedArgs) < 4 || capturedArgs[1] != "JFR.dump" {
		t.Errorf("unexpected jcmd args: %v", capturedArgs)
	}
	found := false
	for _, a := range capturedArgs {
		if a == "filename=/tmp/test.jfr" {
			found = true
		}
	}
	if !found {
		t.Errorf("filename arg not found in: %v", capturedArgs)
	}
}

func TestJFRStopFakeRunner(t *testing.T) {
	var capturedArgs []string
	run := func(name string, args ...string) ([]byte, error) {
		capturedArgs = args
		return []byte("Stopped recording spectra"), nil
	}
	if err := JFRStop(42, "spectra", "", run); err != nil {
		t.Fatalf("JFRStop: %v", err)
	}
	if len(capturedArgs) < 3 || capturedArgs[1] != "JFR.stop" {
		t.Errorf("unexpected jcmd args: %v", capturedArgs)
	}
}

func TestJFRStopWithDumpFakeRunner(t *testing.T) {
	var capturedArgs []string
	run := func(name string, args ...string) ([]byte, error) {
		capturedArgs = args
		return []byte("Stopped recording spectra, dumped to /tmp/out.jfr"), nil
	}
	if err := JFRStop(42, "spectra", "/tmp/out.jfr", run); err != nil {
		t.Fatalf("JFRStop with dump: %v", err)
	}
	found := false
	for _, a := range capturedArgs {
		if a == "filename=/tmp/out.jfr" {
			found = true
		}
	}
	if !found {
		t.Errorf("filename arg not found in: %v", capturedArgs)
	}
}

// --- CollectAll with fake runner ---

func TestCollectAllNoJPS(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("command not found: %s", name)
	}
	got := CollectAll(context.Background(), CollectOptions{CmdRunner: run})
	if got != nil {
		t.Errorf("expected nil when jps not found, got %v", got)
	}
}

func TestCollectAllSingleProcess(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) {
		switch name {
		case "jps":
			return []byte("4012 com.example.Server\n"), nil
		case "jcmd":
			if len(args) < 2 {
				return nil, fmt.Errorf("jcmd needs args")
			}
			switch args[1] {
			case "VM.system_properties":
				return []byte(`java.home=/usr/lib/jvm/temurin-21
java.vendor=Eclipse Adoptium
java.version=21.0.6
`), nil
			case "VM.command_line":
				return []byte(`VM Arguments:
jvm_args: -Xmx2g
java_command: com.example.Server
`), nil
			case "Thread.print":
				return []byte(`"main" #1 prio=5
"GC Thread" #2
`), nil
			}
		case "jstat":
			return []byte(`S0C S1C S0U S1U EC EU OC OU MC MU CCSC CCSU YGC YGCT FGC FGCT GCT
0.0 0.0 0.0 0.0 40960.0 20480.0 204800.0 4096.0 61440.0 59900.3 8064.0 7678.7 5 0.078 0 0.000 0.078
`), nil
		}
		return nil, fmt.Errorf("unexpected: %s %v", name, args)
	}

	got := CollectAll(context.Background(), CollectOptions{CmdRunner: run})
	if len(got) != 1 {
		t.Fatalf("expected 1 JVM info, got %d", len(got))
	}
	info := got[0]
	if info.PID != 4012 {
		t.Errorf("PID = %d, want 4012", info.PID)
	}
	if info.MainClass != "com.example.Server" {
		t.Errorf("MainClass = %q", info.MainClass)
	}
	if info.JDKVendor != "Eclipse Adoptium" {
		t.Errorf("JDKVendor = %q", info.JDKVendor)
	}
	if info.JDKVersion != "21.0.6" {
		t.Errorf("JDKVersion = %q", info.JDKVersion)
	}
	if info.VMArgs != "-Xmx2g" {
		t.Errorf("VMArgs = %q", info.VMArgs)
	}
	if info.ThreadCount != 2 {
		t.Errorf("ThreadCount = %d, want 2", info.ThreadCount)
	}
	if info.SysProps["java.home"] != "/usr/lib/jvm/temurin-21" {
		t.Errorf("java.home = %q", info.SysProps["java.home"])
	}
	if info.GC == nil || info.GC.YGC != 5 {
		t.Fatalf("GC = %#v, want YGC=5", info.GC)
	}
}

func TestCollectAllAttributesJDKInstall(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) {
		switch name {
		case "jps":
			return []byte("4012 com.example.Server\n"), nil
		case "jcmd":
			if len(args) < 2 {
				return nil, fmt.Errorf("jcmd needs args")
			}
			switch args[1] {
			case "VM.system_properties":
				return []byte(`java.home=/Library/Java/JavaVirtualMachines/temurin-21.jdk/Contents/Home
java.vendor=Eclipse Adoptium
java.version=21.0.6
`), nil
			case "VM.command_line":
				return []byte("jvm_args: -Xmx2g\n"), nil
			case "Thread.print":
				return []byte("\"main\" #1 prio=5\n"), nil
			}
		}
		return nil, fmt.Errorf("unexpected: %s %v", name, args)
	}

	got := CollectAll(context.Background(), CollectOptions{
		CmdRunner: run,
		JDKs: []toolchain.JDKInstall{{
			InstallID: "system-eclipse-adoptium-21.0.6",
			Path:      "/Library/Java/JavaVirtualMachines/temurin-21.jdk/Contents/Home",
			Source:    "system",
		}},
	})
	if len(got) != 1 {
		t.Fatalf("expected 1 JVM info, got %d", len(got))
	}
	info := got[0]
	if info.JDKInstallID != "system-eclipse-adoptium-21.0.6" {
		t.Errorf("JDKInstallID = %q", info.JDKInstallID)
	}
	if info.JDKSource != "system" {
		t.Errorf("JDKSource = %q", info.JDKSource)
	}
	if info.JDKPath != "/Library/Java/JavaVirtualMachines/temurin-21.jdk/Contents/Home" {
		t.Errorf("JDKPath = %q", info.JDKPath)
	}
}

func TestAttributeJDKsUpdatesExistingInfos(t *testing.T) {
	infos := []Info{{
		PID:      4012,
		JavaHome: "/Library/Java/JavaVirtualMachines/temurin-21.jdk/Contents/Home",
	}}
	AttributeJDKs(infos, []toolchain.JDKInstall{{
		InstallID: "system-eclipse-adoptium-21.0.6",
		Path:      "/Library/Java/JavaVirtualMachines/temurin-21.jdk/Contents/Home",
		Source:    "system",
	}})
	if infos[0].JDKInstallID != "system-eclipse-adoptium-21.0.6" {
		t.Errorf("JDKInstallID = %q", infos[0].JDKInstallID)
	}
}
