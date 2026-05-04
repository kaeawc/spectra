// Package shutdown coordinates graceful teardown of a process with multiple
// subsystems. Hooks run in reverse registration order (LIFO) so dependents
// close before their dependencies. Each hook is bounded by a timeout so a
// stuck subsystem cannot hang the whole shutdown.
//
// Typical use in an HTTP server:
//
//	c := shutdown.New(10 * time.Second)
//	c.Register("http-server", server.Shutdown)
//	c.Register("db-pool",     func(ctx context.Context) error { db.Close(); return nil })
//
//	sigs := make(chan os.Signal, 1)
//	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
//	<-sigs
//	c.Shutdown(context.Background())
package shutdown

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Hook is a teardown function. It receives a context whose deadline is the
// hook's timeout; honoring it lets the hook abort its own work cleanly.
type Hook func(ctx context.Context) error

// HookErrorFunc is invoked when a hook fails or times out. The default
// implementation logs to stderr; pass a custom one to integrate with your
// logger or metric system.
type HookErrorFunc func(name string, err error)

// Coordinator runs registered hooks in LIFO order on Shutdown.
type Coordinator struct {
	hookTimeout time.Duration
	onHookError HookErrorFunc

	mu       sync.Mutex
	hooks    []registered
	started  bool
	doneCh   chan struct{}
	doneOnce sync.Once
}

type registered struct {
	name string
	hook Hook
}

// New returns a Coordinator that bounds each hook to hookTimeout. A zero or
// negative timeout disables the per-hook bound.
func New(hookTimeout time.Duration) *Coordinator {
	return &Coordinator{
		hookTimeout: hookTimeout,
		onHookError: func(name string, err error) {
			fmt.Printf("[shutdown] hook %q failed: %v\n", name, err)
		},
		doneCh: make(chan struct{}),
	}
}

// OnHookError replaces the default error handler. Safe to call before any
// Shutdown but not concurrently with one.
func (c *Coordinator) OnHookError(fn HookErrorFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onHookError = fn
}

// Register adds a hook. Returns a deregister function that removes the hook
// if Shutdown has not yet been invoked.
func (c *Coordinator) Register(name string, h Hook) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hooks = append(c.hooks, registered{name: name, hook: h})
	idx := len(c.hooks) - 1
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.started || idx >= len(c.hooks) || c.hooks[idx].name != name {
			return
		}
		c.hooks = append(c.hooks[:idx], c.hooks[idx+1:]...)
	}
}

// Size returns the number of currently registered hooks.
func (c *Coordinator) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.hooks)
}

// IsShuttingDown reports whether Shutdown has been invoked.
func (c *Coordinator) IsShuttingDown() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started
}

// Done returns a channel that closes when Shutdown has finished running every
// hook. Useful for main() to block on shutdown completion.
func (c *Coordinator) Done() <-chan struct{} { return c.doneCh }

// Shutdown runs every registered hook in reverse order. Calling Shutdown more
// than once is safe — subsequent calls block on the original run and return
// the same error. The provided context bounds the entire shutdown; individual
// hooks are further bounded by the per-hook timeout.
func (c *Coordinator) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		<-c.doneCh
		return nil
	}
	c.started = true
	hooks := make([]registered, len(c.hooks))
	copy(hooks, c.hooks)
	onErr := c.onHookError
	timeout := c.hookTimeout
	c.mu.Unlock()

	defer c.doneOnce.Do(func() { close(c.doneCh) })

	var errs []error
	for i := len(hooks) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			errs = append(errs, fmt.Errorf("shutdown aborted before %q: %w", hooks[i].name, err))
			break
		}
		if err := runHook(ctx, hooks[i], timeout); err != nil {
			onErr(hooks[i].name, err)
			errs = append(errs, fmt.Errorf("hook %q: %w", hooks[i].name, err))
		}
	}
	return errors.Join(errs...)
}

func runHook(parent context.Context, r registered, timeout time.Duration) error {
	ctx := parent
	cancel := func() {}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, timeout)
	}
	defer cancel()

	done := make(chan error, 1)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				done <- fmt.Errorf("panic: %v", rec)
			}
		}()
		done <- r.hook(ctx)
	}()

	select {
	case e := <-done:
		return e
	case <-ctx.Done():
		return fmt.Errorf("timed out after %s: %w", timeout, ctx.Err())
	}
}
