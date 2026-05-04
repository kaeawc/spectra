package toolchain

import (
	"context"
	"os"
	"path/filepath"
	"sync"
)

// CollectOptions parameterises the collector. All path fields have sane
// defaults (live machine) but can be overridden in tests to point at
// a fake home directory.
type CollectOptions struct {
	// Home is the user's home directory. Defaults to os.UserHomeDir().
	Home string
	// BrewCellars are candidate Homebrew Cellar roots.
	// Defaults to [/opt/homebrew/Cellar, /usr/local/Cellar].
	BrewCellars []string
	// SystemJVMRoot is the system JVM search path (/Library/Java/...).
	// Defaults to /Library/Java/JavaVirtualMachines.
	SystemJVMRoot string
	// UserJVMRoot is ~/Library/Java/JavaVirtualMachines.
	// Defaults to <Home>/Library/Java/JavaVirtualMachines.
	UserJVMRoot string
	// CmdRunner runs external commands. Defaults to execRunner.
	// Inject a fake in tests to avoid shelling out to brew.
	CmdRunner CmdRunner
}

// CmdRunner abstracts exec.Command for testability.
type CmdRunner func(name string, args ...string) ([]byte, error)

// Collect runs all subsystem discoverers in parallel and returns the
// aggregate Toolchains. Any discoverer error is silently absorbed —
// a partial result is still useful.
func Collect(ctx context.Context, opts CollectOptions) Toolchains {
	opts = withDefaults(opts)

	var (
		mu  sync.Mutex
		out Toolchains
	)

	set := func(fn func(t *Toolchains)) {
		fn(&out)
	}

	run := func(fn func() (func(t *Toolchains), error)) func() {
		return func() {
			if apply, err := fn(); err == nil && apply != nil {
				mu.Lock()
				set(apply)
				mu.Unlock()
			}
		}
	}

	var wg sync.WaitGroup
	tasks := []func(){
		run(func() (func(*Toolchains), error) {
			inv, err := discoverBrew(opts.CmdRunner)
			return func(t *Toolchains) { t.Brew = inv }, err
		}),
		run(func() (func(*Toolchains), error) {
			jdks, err := discoverJDKs(opts)
			return func(t *Toolchains) { t.JDKs = jdks }, err
		}),
		run(func() (func(*Toolchains), error) {
			installs, err := discoverNode(opts)
			return func(t *Toolchains) { t.Node = installs }, err
		}),
		run(func() (func(*Toolchains), error) {
			installs, err := discoverPython(opts)
			return func(t *Toolchains) { t.Python = installs }, err
		}),
		run(func() (func(*Toolchains), error) {
			installs, err := discoverGo(opts)
			return func(t *Toolchains) { t.Go = installs }, err
		}),
		run(func() (func(*Toolchains), error) {
			installs, err := discoverRuby(opts)
			return func(t *Toolchains) { t.Ruby = installs }, err
		}),
		run(func() (func(*Toolchains), error) {
			chains, err := discoverRust(opts.Home)
			return func(t *Toolchains) { t.Rust = chains }, err
		}),
		run(func() (func(*Toolchains), error) {
			snap := collectEnv()
			return func(t *Toolchains) { t.Env = snap }, nil
		}),
	}

	for _, task := range tasks {
		wg.Add(1)
		task := task
		go func() {
			defer wg.Done()
			task()
		}()
	}
	wg.Wait()
	return out
}

func withDefaults(o CollectOptions) CollectOptions {
	if o.Home == "" {
		h, _ := os.UserHomeDir()
		o.Home = h
	}
	if len(o.BrewCellars) == 0 {
		o.BrewCellars = []string{"/opt/homebrew/Cellar", "/usr/local/Cellar"}
	}
	if o.SystemJVMRoot == "" {
		o.SystemJVMRoot = "/Library/Java/JavaVirtualMachines"
	}
	if o.UserJVMRoot == "" {
		o.UserJVMRoot = filepath.Join(o.Home, "Library", "Java", "JavaVirtualMachines")
	}
	if o.CmdRunner == nil {
		o.CmdRunner = execRunner
	}
	return o
}
