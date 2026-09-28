package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/fsutil"
	"github.com/kaeawc/spectra/internal/jsonrpc"
	"github.com/kaeawc/spectra/internal/logger"
	"github.com/kaeawc/spectra/internal/peercred"
)

type Options struct {
	Paths               Paths
	Version             string
	Logger              logger.Logger
	Clock               clock.Clock
	IdleTimeout         time.Duration
	SocketCheckInterval time.Duration
	AllowUID            func(uint32) bool
}

type Server struct {
	opts         Options
	mu           sync.RWMutex
	handlers     map[string]Handler
	started      time.Time
	lastActivity time.Time
	stop         chan struct{}
	stopOnce     sync.Once
	conns        map[net.Conn]struct{}
	connWG       sync.WaitGroup
	handlerWG    sync.WaitGroup
}

func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = logger.Discard()
	}
	if opts.Clock == nil {
		opts.Clock = clock.System{}
	}
	if opts.SocketCheckInterval <= 0 {
		opts.SocketCheckInterval = 5 * time.Second
	}
	if opts.AllowUID == nil {
		// #nosec G115 -- UIDs returned by the host OS fit the kernel's uint32 UID field.
		opts.AllowUID = func(uid uint32) bool { return uid == uint32(os.Getuid()) }
	}
	return &Server{opts: opts, handlers: map[string]Handler{}, stop: make(chan struct{}), conns: map[net.Conn]struct{}{}}
}

func (s *Server) Register(method string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if method == "" || h == nil || len(method) >= 7 && method[:7] == "daemon." {
		panic("daemon: reserved or empty method")
	}
	if _, ok := s.handlers[method]; ok {
		panic("daemon: duplicate method: " + method)
	}
	s.handlers[method] = h
}

func (s *Server) Run(ctx context.Context) error {
	p := s.opts.Paths
	if p.Dir == "" || p.Socket == "" || p.Lock == "" || p.PID == "" || len([]byte(p.Socket)) >= 104 {
		return fmt.Errorf("daemon: invalid paths")
	}
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return fmt.Errorf("daemon: create directory: %w", err)
	}
	if err := os.Chmod(p.Dir, 0o700); err != nil { // #nosec G302 -- directory must be owner-searchable; 0700 is the most restrictive usable mode.
		return fmt.Errorf("daemon: secure directory: %w", err)
	}
	lock, err := acquireLock(p.Lock, p.PID)
	if err != nil {
		return err
	}
	defer func() { unlockFile(lock); _ = lock.Close() }()
	if err := s.prepareSocket(p.Socket); err != nil {
		return err
	}
	ln, err := net.Listen("unix", p.Socket)
	if err != nil {
		return fmt.Errorf("daemon: listen: %w", err)
	}
	defer ln.Close()
	owned, err := os.Lstat(p.Socket)
	if err != nil {
		return fmt.Errorf("daemon: stat socket: %w", err)
	}
	defer s.removeOwnedSocket(owned)
	if err := os.Chmod(p.Socket, 0o600); err != nil {
		return fmt.Errorf("daemon: secure socket: %w", err)
	}
	if err := fsutil.WriteFileAtomic(p.PID, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		return fmt.Errorf("daemon: write pid: %w", err)
	}
	defer os.Remove(p.PID)
	return s.serve(ctx, ln, owned)
}

func (s *Server) serve(ctx context.Context, ln net.Listener, owned os.FileInfo) error {
	s.mu.Lock()
	s.started = s.opts.Clock.Now()
	s.lastActivity = s.started
	s.mu.Unlock()
	s.opts.Logger.Info("daemon started", "socket", s.opts.Paths.Socket)
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	acceptErr := make(chan error, 1)
	acceptDone := make(chan struct{})
	go func() { defer close(acceptDone); s.accept(serveCtx, ln, acceptErr) }()
	interval := s.opts.SocketCheckInterval
	if s.opts.IdleTimeout > 0 && s.opts.IdleTimeout < interval {
		interval = s.opts.IdleTimeout
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var runErr error
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-s.stop:
			break loop
		case err := <-acceptErr:
			runErr = err
			break loop
		case <-ticker.C:
			if !s.socketOwned(owned) {
				s.opts.Logger.Warn("daemon socket removed or replaced")
				break loop
			}
			if s.idle() {
				s.opts.Logger.Info("daemon idle timeout")
				break loop
			}
		}
	}
	_ = ln.Close()
	cancel()
	<-acceptDone
	s.closeConnections()
	done := make(chan struct{})
	go func() { s.connWG.Wait(); s.handlerWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		s.opts.Logger.Warn("daemon connection drain timed out")
	}
	s.opts.Logger.Info("daemon stopped")
	return runErr
}

func (s *Server) prepareSocket(path string) error {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("daemon: inspect socket: %w", err)
	}
	c, dialErr := net.DialTimeout("unix", path, 200*time.Millisecond)
	if dialErr == nil {
		_ = c.Close()
		return ErrAlreadyRunning
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("daemon: remove stale socket: %w", err)
	}
	return nil
}

func (s *Server) socketOwned(original os.FileInfo) bool {
	current, err := os.Lstat(s.opts.Paths.Socket)
	return err == nil && os.SameFile(original, current)
}

func (s *Server) removeOwnedSocket(original os.FileInfo) {
	if s.socketOwned(original) {
		_ = os.Remove(s.opts.Paths.Socket)
	}
}

func (s *Server) idle() bool {
	if s.opts.IdleTimeout <= 0 {
		return false
	}
	s.mu.RLock()
	last := s.lastActivity
	connected := len(s.conns) > 0
	s.mu.RUnlock()
	return !connected && s.opts.Clock.Now().Sub(last) >= s.opts.IdleTimeout
}

func (s *Server) accept(ctx context.Context, ln net.Listener, errorsOut chan<- error) {
	var delay time.Duration
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			if retryableAcceptError(err) {
				if delay == 0 {
					delay = 5 * time.Millisecond
				} else {
					delay = min(delay*2, time.Second)
				}
				s.opts.Logger.Warn("daemon accept temporarily failed", "error", err, "retry_in", delay)
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				continue
			}
			select {
			case errorsOut <- fmt.Errorf("daemon: accept: %w", err):
			case <-ctx.Done():
			}
			return
		}
		delay = 0
		uid, err := peercred.PeerUID(c)
		if err != nil || !s.opts.AllowUID(uid) {
			s.opts.Logger.Warn("daemon peer rejected", "uid", uid, "error", err)
			_ = c.Close()
			continue
		}
		s.mu.Lock()
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		s.connWG.Add(1)
		go s.serveConn(ctx, c, uid)
	}
}

func retryableAcceptError(err error) bool {
	return errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) || errors.Is(err, syscall.ECONNABORTED)
}

func (s *Server) closeConnections() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.conns {
		_ = c.Close()
	}
}

func (s *Server) serveConn(ctx context.Context, c net.Conn, uid uint32) {
	defer s.connWG.Done()
	done := make(chan struct{})
	defer func() { close(done); _ = c.Close(); s.mu.Lock(); delete(s.conns, c); s.mu.Unlock() }()
	var writeMu sync.Mutex
	sem := make(chan struct{}, 32)
	r := bufio.NewReader(c)
	for {
		raw, err := jsonrpc.ReadMessage(r)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.opts.Logger.Debug("daemon read failed", "error", err)
			}
			return
		}
		var msg struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			jsonrpc.SendResponse(c, &writeMu, nil, nil, &jsonrpc.Error{Code: CodeParseError, Message: "parse error"})
			continue
		}
		if msg.JSONRPC != "2.0" || msg.Method == "" {
			jsonrpc.SendResponse(c, &writeMu, msg.ID, nil, &jsonrpc.Error{Code: CodeInvalidRequest, Message: "invalid request"})
			continue
		}
		if len(msg.ID) == 0 {
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		s.handlerWG.Add(1)
		go func() {
			defer s.handlerWG.Done()
			defer func() { <-sem }()
			s.handle(ctx, c, &writeMu, done, uid, msg.Method, msg.Params, msg.ID)
		}()
	}
}

func (s *Server) handle(ctx context.Context, c net.Conn, mu *sync.Mutex, done <-chan struct{}, uid uint32, method string, params, id json.RawMessage) {
	defer func() {
		if v := recover(); v != nil {
			s.opts.Logger.Error("daemon handler panicked", "method", method, "panic", v)
			jsonrpc.SendResponse(c, mu, id, nil, &jsonrpc.Error{Code: CodeInternalError, Message: "internal error"})
		}
	}()
	s.mu.Lock()
	s.lastActivity = s.opts.Clock.Now()
	s.mu.Unlock()
	if method == MethodStatus {
		jsonrpc.SendResponse(c, mu, id, s.status(), nil)
		return
	}
	if method == MethodShutdown {
		jsonrpc.SendResponse(c, mu, id, map[string]bool{"ok": true}, nil)
		s.stopOnce.Do(func() { close(s.stop) })
		return
	}
	s.mu.RLock()
	h := s.handlers[method]
	s.mu.RUnlock()
	if h == nil {
		jsonrpc.SendResponse(c, mu, id, nil, &jsonrpc.Error{Code: CodeMethodNotFound, Message: "method not found"})
		return
	}
	notify := func(method string, params any) error {
		select {
		case <-done:
			return net.ErrClosed
		default:
		}
		payload, err := json.Marshal(jsonrpc.Notification{JSONRPC: "2.0", Method: method, Params: params})
		if err != nil {
			return fmt.Errorf("daemon notify: %w", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if _, err := c.Write(append(payload, '\n')); err != nil {
			return fmt.Errorf("daemon notify: %w", err)
		}
		return nil
	}
	result, err := h(ctx, &Request{Method: method, Params: params, PeerUID: uid, Notify: notify, Done: done})
	if err != nil {
		code := CodeInternalError
		var invalid invalidParamsError
		if errors.As(err, &invalid) {
			code = CodeInvalidParams
		}
		jsonrpc.SendResponse(c, mu, id, nil, &jsonrpc.Error{Code: code, Message: err.Error()})
		return
	}
	jsonrpc.SendResponse(c, mu, id, result, nil)
}

func (s *Server) status() StatusResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	methods := []string{MethodShutdown, MethodStatus}
	for method := range s.handlers {
		methods = append(methods, method)
	}
	sort.Strings(methods)
	return StatusResult{Protocol: ProtocolVersion, Version: s.opts.Version, PID: os.Getpid(), StartedAt: s.started, UptimeSeconds: s.opts.Clock.Now().Sub(s.started).Seconds(), Socket: s.opts.Paths.Socket, Methods: methods}
}
