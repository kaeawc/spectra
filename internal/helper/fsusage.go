package helper

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultFSUsageDuration = 30 * time.Second
	maxFSUsageDuration     = time.Minute
	fsUsageStopTimeout     = 3 * time.Second
)

type fsUsageStarter func(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) (fsUsageProcess, error)

type fsUsageProcess interface {
	Wait() error
}

type fsUsageManager struct {
	mu       sync.Mutex
	next     atomic.Uint64
	starter  fsUsageStarter
	sessions map[string]*fsUsageSession
}

type fsUsageSession struct {
	cancel context.CancelFunc
	done   chan error
	buf    lockedBuffer
}

type fsUsageStartParams struct {
	PID        int    `json:"pid"`
	Mode       string `json:"mode"`
	DurationMS int    `json:"duration_ms"`
}

type fsUsageStopParams struct {
	Handle string `json:"handle"`
}

func newFSUsageManager(starter fsUsageStarter) *fsUsageManager {
	if starter == nil {
		starter = startFSUsageProcess
	}
	return &fsUsageManager{starter: starter, sessions: make(map[string]*fsUsageSession)}
}

func (m *fsUsageManager) start(p fsUsageStartParams) (map[string]any, error) {
	if p.PID <= 0 {
		return nil, fmt.Errorf("helper.fs_usage.start requires {\"pid\": <pid>}")
	}
	mode := p.Mode
	if mode == "" {
		mode = "filesys"
	}
	if !validFSUsageMode(mode) {
		return nil, fmt.Errorf("helper.fs_usage.start rejects invalid mode %q", mode)
	}
	duration := defaultFSUsageDuration
	if p.DurationMS > 0 {
		duration = time.Duration(p.DurationMS) * time.Millisecond
	}
	if duration <= 0 || duration > maxFSUsageDuration {
		return nil, fmt.Errorf("helper.fs_usage.start requires duration_ms <= %d", maxFSUsageDuration.Milliseconds())
	}

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	sess := &fsUsageSession{cancel: cancel, done: make(chan error, 1)}
	handle := fmt.Sprintf("fsu-%d", m.next.Add(1))
	args := []string{"-w", "-f", mode, strconv.Itoa(p.PID)}
	proc, err := m.starter(ctx, &sess.buf, &sess.buf, "fs_usage", args...)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("fs_usage start: %w", err)
	}
	m.mu.Lock()
	m.sessions[handle] = sess
	m.mu.Unlock()
	go func() {
		sess.done <- proc.Wait()
		if ctx.Err() == context.DeadlineExceeded {
			m.forget(handle)
		}
	}()

	return map[string]any{
		"handle":      handle,
		"pid":         p.PID,
		"mode":        mode,
		"duration_ms": duration.Milliseconds(),
	}, nil
}

func (m *fsUsageManager) stop(p fsUsageStopParams) (map[string]any, error) {
	if p.Handle == "" {
		return nil, fmt.Errorf("helper.fs_usage.stop requires {\"handle\": \"...\"}")
	}
	m.mu.Lock()
	sess, ok := m.sessions[p.Handle]
	if ok {
		delete(m.sessions, p.Handle)
	}
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("helper.fs_usage.stop unknown handle %q", p.Handle)
	}

	sess.cancel()
	timedOut := false
	select {
	case <-sess.done:
	case <-time.After(fsUsageStopTimeout):
		timedOut = true
	}
	result := map[string]any{
		"handle":     p.Handle,
		"raw_output": sess.buf.String(),
		"stopped":    true,
	}
	if timedOut {
		result["wait_error"] = "timeout waiting for fs_usage to stop"
	}
	return result, nil
}

func (m *fsUsageManager) forget(handle string) {
	m.mu.Lock()
	delete(m.sessions, handle)
	m.mu.Unlock()
}

func validFSUsageMode(mode string) bool {
	switch mode {
	case "network", "filesys", "pathname", "exec", "diskio":
		return true
	default:
		return false
	}
}

type execFSUsageProcess struct {
	cmd *exec.Cmd
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

func startFSUsageProcess(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) (fsUsageProcess, error) {
	// #nosec G204 -- helper fs_usage runs a fixed binary with allowlisted flags and numeric PID.
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execFSUsageProcess{cmd: cmd}, nil
}

func (p *execFSUsageProcess) Wait() error {
	return p.cmd.Wait()
}
