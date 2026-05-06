package helper

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaeawc/spectra/internal/netcap"
)

const (
	defaultNetCaptureDir = "/var/tmp/spectra-netcap"
	netCaptureStopWait   = 3 * time.Second
)

type netCaptureStarter func(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) (netCaptureProcess, error)

type netCaptureProcess interface {
	Wait() error
}

type netCaptureManager struct {
	mu       sync.Mutex
	next     atomic.Uint64
	starter  netCaptureStarter
	baseDir  string
	sessions map[string]*netCaptureSession
}

type netCaptureSession struct {
	cancel context.CancelFunc
	done   chan error
	output string
	uid    uint32
	buf    lockedBuffer
}

type netCaptureStartParams struct {
	Interface  string `json:"interface"`
	DurationMS int    `json:"duration_ms"`
	SnapLen    int    `json:"snap_len"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Proto      string `json:"proto"`
}

type netCaptureStopParams struct {
	Handle string `json:"handle"`
}

func newNetCaptureManager(starter netCaptureStarter, baseDir string) *netCaptureManager {
	if starter == nil {
		starter = startNetCaptureProcess
	}
	if baseDir == "" {
		baseDir = defaultNetCaptureDir
	}
	return &netCaptureManager{starter: starter, baseDir: baseDir, sessions: make(map[string]*netCaptureSession)}
}

func (m *netCaptureManager) start(uid uint32, p netCaptureStartParams) (map[string]any, error) {
	duration := time.Duration(p.DurationMS) * time.Millisecond
	if duration == 0 {
		duration = netcap.DefaultDuration
	}
	handle := fmt.Sprintf("netcap-%d", m.next.Add(1))
	outputDir := filepath.Join(m.baseDir, fmt.Sprint(uid))
	output := filepath.Join(outputDir, handle+".pcap")
	opts := netcap.Options{
		Interface: p.Interface,
		Output:    output,
		Duration:  duration,
		SnapLen:   p.SnapLen,
		Host:      p.Host,
		Port:      p.Port,
		Proto:     p.Proto,
	}
	args, err := netcap.BuildTCPDumpArgs(opts)
	if err != nil {
		return nil, fmt.Errorf("helper.net_capture.start: %w", err)
	}
	if err := ensureCaptureDir(outputDir, uid); err != nil {
		return nil, fmt.Errorf("create capture dir: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	sess := &netCaptureSession{cancel: cancel, done: make(chan error, 1), output: output, uid: uid}
	proc, err := m.starter(ctx, &sess.buf, &sess.buf, "tcpdump", args...)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("tcpdump start: %w", err)
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
		"output_path": output,
		"duration_ms": duration.Milliseconds(),
		"interface":   p.Interface,
	}, nil
}

func (m *netCaptureManager) stop(p netCaptureStopParams) (map[string]any, error) {
	if p.Handle == "" {
		return nil, fmt.Errorf("helper.net_capture.stop requires {\"handle\": \"...\"}")
	}
	m.mu.Lock()
	sess, ok := m.sessions[p.Handle]
	if ok {
		delete(m.sessions, p.Handle)
	}
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("helper.net_capture.stop unknown handle %q", p.Handle)
	}

	sess.cancel()
	waitErr := waitNetCapture(sess.done)
	ownerErr := makeCaptureReadableByOwner(sess.output, sess.uid)
	info, statErr := os.Stat(sess.output)
	size := int64(0)
	if statErr == nil {
		size = info.Size()
	}
	result := map[string]any{
		"handle":      p.Handle,
		"output_path": sess.output,
		"stopped":     true,
		"size_bytes":  size,
	}
	if waitErr != "" {
		result["wait_error"] = waitErr
	}
	if statErr != nil && !os.IsNotExist(statErr) {
		result["stat_error"] = statErr.Error()
	}
	if ownerErr != nil && !os.IsNotExist(ownerErr) {
		result["owner_error"] = ownerErr.Error()
	}
	return result, nil
}

func ensureCaptureDir(path string, uid uint32) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	if os.Geteuid() == 0 || int(uid) != os.Getuid() {
		if err := os.Chown(path, int(uid), -1); err != nil {
			return err
		}
	}
	return os.Chmod(path, 0o700)
}

func makeCaptureReadableByOwner(path string, uid uint32) error {
	if os.Geteuid() == 0 || int(uid) != os.Getuid() {
		if err := os.Chown(path, int(uid), -1); err != nil {
			return err
		}
	}
	return os.Chmod(path, 0o600)
}

func waitNetCapture(done <-chan error) string {
	select {
	case err := <-done:
		if err != nil {
			return err.Error()
		}
	case <-time.After(netCaptureStopWait):
		return "timeout waiting for tcpdump to stop"
	}
	return ""
}

func (m *netCaptureManager) forget(handle string) {
	m.mu.Lock()
	delete(m.sessions, handle)
	m.mu.Unlock()
}

type execNetCaptureProcess struct {
	cmd *exec.Cmd
}

func startNetCaptureProcess(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) (netCaptureProcess, error) {
	// #nosec G204 -- tcpdump is invoked by the helper with validated structured args only.
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execNetCaptureProcess{cmd: cmd}, nil
}

func (p *execNetCaptureProcess) Wait() error {
	return p.cmd.Wait()
}
