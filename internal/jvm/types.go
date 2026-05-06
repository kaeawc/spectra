// Package jvm discovers and inspects running JVM processes using the JDK
// shell tools jps and jcmd. No Java code is embedded; all inspection is
// done by forking bundled JDK commands already present on the machine.
//
// See docs/inspection/jvm.md for the full design.
package jvm

// CmdRunner abstracts shell-out for testability.
type CmdRunner func(name string, args ...string) ([]byte, error)

// Info is the snapshot of one running JVM process.
type Info struct {
	PID          int               `json:"pid"`
	MainClass    string            `json:"main_class,omitempty"`  // from jps -l
	JavaHome     string            `json:"java_home,omitempty"`   // java.home system property
	JDKVendor    string            `json:"jdk_vendor,omitempty"`  // java.vendor
	JDKVersion   string            `json:"jdk_version,omitempty"` // java.version
	JDKInstallID string            `json:"jdk_install_id,omitempty"`
	JDKSource    string            `json:"jdk_source,omitempty"`
	JDKPath      string            `json:"jdk_path,omitempty"`
	VMArgs       string            `json:"vm_args,omitempty"`  // from jcmd VM.command_line
	VMFlags      string            `json:"vm_flags,omitempty"` // JVM flags (XX: flags)
	ThreadCount  int               `json:"thread_count,omitempty"`
	GC           *GCStats          `json:"gc,omitempty"`        // one-shot jstat -gc counters
	SysProps     map[string]string `json:"sys_props,omitempty"` // selected system properties
}

// selectedProps is the allowlist of java system properties captured in SysProps.
// Full java.class.path would be huge; we capture only the diagnostically useful subset.
var selectedProps = map[string]bool{
	"java.home":            true,
	"java.version":         true,
	"java.vendor":          true,
	"java.runtime.version": true,
	"os.arch":              true,
	"os.name":              true,
	"os.version":           true,
	"user.home":            true,
	"user.dir":             true,
	"java.io.tmpdir":       true,
}
