package daemonclient

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
)

type SpawnOptions struct {
	Exe     string
	Args    []string
	Wait    time.Duration
	LogPath string
}

func EnsureRunning(ctx context.Context, paths daemon.Paths, opts SpawnOptions) (*Client, error) {
	if c, ok := Discover(paths); ok {
		return c, nil
	}
	if opts.Wait <= 0 {
		opts.Wait = 5 * time.Second
	}
	if opts.Exe == "" {
		return nil, fmt.Errorf("daemon spawn: empty executable")
	}
	if err := os.MkdirAll(filepath.Dir(opts.LogPath), 0o700); err != nil {
		return nil, fmt.Errorf("daemon spawn log directory: %w", err)
	}
	log, err := os.OpenFile(opts.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("daemon spawn log: %w", err)
	}
	defer log.Close()
	cmd := exec.Command(opts.Exe, opts.Args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, log, log
	setDetached(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("daemon spawn: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	timer := time.NewTimer(opts.Wait)
	defer timer.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if c, ok := Discover(paths); ok {
			return c, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, fmt.Errorf("daemon spawn: timed out waiting for %s", paths.Socket)
		case <-tick.C:
		}
	}
}
