package helper

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/kaeawc/spectra/internal/netcap"
	"golang.org/x/sys/unix"
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
	ownerUID int
	random   io.Reader
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

func newNetCaptureManager(starter netCaptureStarter, baseDir string, geteuid ...func() int) *netCaptureManager {
	if starter == nil {
		starter = startNetCaptureProcess
	}
	if baseDir == "" {
		baseDir = defaultNetCaptureDir
	}
	currentEUID := os.Geteuid
	if len(geteuid) != 0 {
		currentEUID = geteuid[0]
	}
	return &netCaptureManager{starter: starter, baseDir: baseDir, ownerUID: currentEUID(), random: rand.Reader, sessions: make(map[string]*netCaptureSession)}
}

func (m *netCaptureManager) start(uid uint32, p netCaptureStartParams) (map[string]any, error) {
	duration := time.Duration(p.DurationMS) * time.Millisecond
	if duration == 0 {
		duration = netcap.DefaultDuration
	}
	handle := fmt.Sprintf("netcap-%d", m.next.Add(1))
	outputDir := filepath.Join(m.baseDir, fmt.Sprint(uid))
	var suffix [16]byte
	if _, err := io.ReadFull(m.random, suffix[:]); err != nil {
		return nil, fmt.Errorf("generate capture name: %w", err)
	}
	output := filepath.Join(outputDir, handle+"-"+hex.EncodeToString(suffix[:])+".pcap")
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
	if err := ensureCaptureDir(m.baseDir, outputDir, m.ownerUID); err != nil {
		return nil, fmt.Errorf("create capture dir: %w", err)
	}
	if err := precreateCaptureFile(output); err != nil {
		return nil, fmt.Errorf("create capture file: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	sess := &netCaptureSession{cancel: cancel, done: make(chan error, 1), output: output, uid: uid}
	proc, err := m.starter(ctx, &sess.buf, &sess.buf, "tcpdump", args...)
	if err != nil {
		cancel()
		return nil, errors.Join(fmt.Errorf("tcpdump start: %w", err), os.Remove(output))
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

func precreateCaptureFile(path string) (err error) {
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("open capture: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(path))
		}
	}()
	f := os.NewFile(uintptr(fd), path)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("chmod capture: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close capture: %w", err)
	}
	return nil
}

func (m *netCaptureManager) stop(uid uint32, p netCaptureStopParams) (map[string]any, error) {
	if p.Handle == "" {
		return nil, fmt.Errorf("helper.net_capture.stop requires {\"handle\": \"...\"}")
	}
	m.mu.Lock()
	sess, ok := m.sessions[p.Handle]
	if ok && (uid == 0 || uid == sess.uid) {
		delete(m.sessions, p.Handle)
	}
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("helper.net_capture.stop unknown handle %q", p.Handle)
	}
	if uid != 0 && uid != sess.uid {
		return nil, fmt.Errorf("helper.net_capture.stop handle %q belongs to another UID", p.Handle)
	}

	sess.cancel()
	waitErr := waitNetCapture(sess.done)
	ownerErr := makeCaptureReadableByOwner(sess.output, sess.uid, m.ownerUID)
	info, statErr := os.Lstat(sess.output)
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
	if ownerErr != nil && !errors.Is(ownerErr, os.ErrNotExist) {
		return nil, fmt.Errorf("finalize capture: %w", ownerErr)
	}
	return result, nil
}

func ensureCaptureDir(base, path string, ownerUID int) error {
	for _, dir := range []string{base, path} {
		if err := ensureSingleCaptureDir(dir, ownerUID); err != nil {
			return fmt.Errorf("capture directory %s: %w", dir, err)
		}
	}
	return nil
}

func ensureSingleCaptureDir(path string, ownerUID int) error {
	if err := os.Mkdir(path, 0o711); err != nil && !os.IsExist(err) {
		return fmt.Errorf("create: %w", err)
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || int(stat.Uid) != ownerUID || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("unsafe directory ownership or permissions")
	}
	if err := f.Chmod(0o711); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}
	info, err = f.Stat()
	if err != nil {
		return fmt.Errorf("stat after chmod: %w", err)
	}
	return verifyCaptureDir(info, ownerUID)
}

func verifyCaptureDir(info os.FileInfo, ownerUID int) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || int(stat.Uid) != ownerUID || info.Mode().Perm()&0o022 != 0 || info.Mode().Perm()&0o001 == 0 {
		return fmt.Errorf("must be an owner-controlled searchable directory without group or world write access")
	}
	return nil
}

func makeCaptureReadableByOwner(path string, uid uint32, ownerUID int) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open capture: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat capture: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Nlink != 1 || int(stat.Uid) != ownerUID {
		return fmt.Errorf("capture is not a singly-linked regular file owned by the helper")
	}
	if info.Mode()&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky)&^os.FileMode(0o600) != 0 {
		return fmt.Errorf("capture permissions were widened before finalization")
	}
	if int(uid) != ownerUID {
		if err := f.Chown(int(uid), int(stat.Gid)); err != nil {
			return fmt.Errorf("chown capture: %w", err)
		}
	}
	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod capture: %w", err)
	}
	return nil
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
