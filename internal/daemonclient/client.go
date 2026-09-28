package daemonclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/jsonrpc"
)

type RPCError struct {
	Code    int
	Message string
}

func (e *RPCError) Error() string { return fmt.Sprintf("daemon RPC %d: %s", e.Code, e.Message) }

type reply struct {
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
}

type Client struct {
	conn           net.Conn
	writeMu        sync.Mutex
	mu             sync.Mutex
	next           uint64
	pending        map[uint64]chan reply
	onNotification func(string, json.RawMessage)
	closed         chan struct{}
	closeOnce      sync.Once
	notifyMu       sync.Mutex
	notifyCond     *sync.Cond
	notifications  []notification
}

type notification struct {
	fn     func(string, json.RawMessage)
	method string
	params json.RawMessage
}

func Dial(ctx context.Context, socket string) (*Client, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("daemon client dial: %w", err)
	}
	c := &Client{conn: conn, pending: make(map[uint64]chan reply), closed: make(chan struct{})}
	c.notifyCond = sync.NewCond(&c.notifyMu)
	go c.dispatchNotifications()
	go c.readLoop()
	return c, nil
}

func Discover(paths daemon.Paths) (*Client, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	c, err := Dial(ctx, paths.Socket)
	return c, err == nil
}

func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	select {
	case <-c.closed:
		return net.ErrClosed
	default:
	}
	c.mu.Lock()
	c.next++
	id := c.next
	ch := make(chan reply, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	msg := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      uint64 `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{"2.0", id, method, params}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("daemon client marshal: %w", err)
	}
	c.writeMu.Lock()
	_, err = c.conn.Write(append(data, '\n'))
	c.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("daemon client write: %w", err)
	}
	select {
	case result := <-ch:
		if result.Error != nil {
			return result.Error
		}
		if out != nil && len(result.Result) > 0 {
			if err := json.Unmarshal(result.Result, out); err != nil {
				return fmt.Errorf("daemon client decode: %w", err)
			}
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return net.ErrClosed
	}
}

func (c *Client) OnNotification(fn func(string, json.RawMessage)) {
	c.mu.Lock()
	c.onNotification = fn
	c.mu.Unlock()
}

func (c *Client) Status(ctx context.Context) (daemon.StatusResult, error) {
	var st daemon.StatusResult
	err := c.Call(ctx, daemon.MethodStatus, nil, &st)
	return st, err
}

func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		_ = c.conn.Close()
		c.notifyCond.Broadcast()
	})
	return nil
}

// Notifications are delivered in read order; callbacks may still be running when Call returns.
func (c *Client) dispatchNotifications() {
	for {
		c.notifyMu.Lock()
		for len(c.notifications) == 0 {
			select {
			case <-c.closed:
				c.notifyMu.Unlock()
				return
			default:
			}
			c.notifyCond.Wait()
		}
		n := c.notifications[0]
		c.notifications[0] = notification{}
		c.notifications = c.notifications[1:]
		c.notifyMu.Unlock()
		n.fn(n.method, n.params)
	}
}

func (c *Client) readLoop() {
	defer c.Close()
	r := bufio.NewReader(c.conn)
	for {
		data, err := jsonrpc.ReadMessage(r)
		if err != nil {
			return
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *RPCError       `json:"error"`
		}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		if msg.Method != "" {
			c.mu.Lock()
			fn := c.onNotification
			c.mu.Unlock()
			if fn != nil {
				c.notifyMu.Lock()
				c.notifications = append(c.notifications, notification{fn, msg.Method, msg.Params})
				c.notifyCond.Signal()
				c.notifyMu.Unlock()
			}
			continue
		}
		var id uint64
		if json.Unmarshal(msg.ID, &id) != nil {
			continue
		}
		c.mu.Lock()
		ch := c.pending[id]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- reply{msg.Result, msg.Error}:
			default:
			}
		}
	}
}

func IsUnavailable(err error) bool {
	if err == nil {
		return false
	}
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) {
		return false
	}
	var op *net.OpError
	return errors.Is(err, net.ErrClosed) || errors.As(err, &op)
}

var ErrVersionMismatch = errors.New("daemon client: version mismatch")

func CheckVersion(st daemon.StatusResult, clientVersion string) error {
	if st.Protocol != daemon.ProtocolVersion {
		return fmt.Errorf("%w: protocol %q", ErrVersionMismatch, st.Protocol)
	}
	if st.Version == "" || st.Version == "dev" || clientVersion == "" || clientVersion == "dev" {
		return nil
	}
	if st.Version != clientVersion {
		return fmt.Errorf("%w: daemon %s, client %s", ErrVersionMismatch, st.Version, clientVersion)
	}
	return nil
}
