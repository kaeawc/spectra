package jvm

import (
	"context"
	"fmt"
	"testing"
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
}
