package shutdown

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestHooksRunInLIFOOrder(t *testing.T) {
	c := New(time.Second)
	var order []string
	c.Register("first", func(context.Context) error {
		order = append(order, "first")
		return nil
	})
	c.Register("second", func(context.Context) error {
		order = append(order, "second")
		return nil
	})
	c.Register("third", func(context.Context) error {
		order = append(order, "third")
		return nil
	})

	if err := c.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	want := []string{"third", "second", "first"}
	for i, n := range want {
		if order[i] != n {
			t.Errorf("order[%d] = %q, want %q", i, order[i], n)
		}
	}
}

func TestShutdownIsIdempotent(t *testing.T) {
	c := New(time.Second)
	var calls atomic.Int32
	c.Register("once", func(context.Context) error {
		calls.Add(1)
		return nil
	})

	for i := 0; i < 5; i++ {
		if err := c.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown #%d: %v", i, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("hook called %d times, want 1", got)
	}
}

func TestHookTimeoutIsBounded(t *testing.T) {
	c := New(20 * time.Millisecond)
	c.OnHookError(func(string, error) {})

	c.Register("slow", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	start := time.Now()
	err := c.Shutdown(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from slow hook")
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("shutdown took %v, expected ≤200ms", elapsed)
	}
}

func TestHookErrorIsCollectedNotFatal(t *testing.T) {
	c := New(time.Second)
	c.OnHookError(func(string, error) {})
	var bRan atomic.Bool

	c.Register("a", func(context.Context) error { return errors.New("boom") })
	c.Register("b", func(context.Context) error { bRan.Store(true); return nil })

	err := c.Shutdown(context.Background())
	if err == nil || !bRan.Load() {
		t.Fatalf("err=%v, bRan=%v — wanted err set and b to run", err, bRan.Load())
	}
}

func TestPanicInHookIsCaught(t *testing.T) {
	c := New(time.Second)
	c.OnHookError(func(string, error) {})
	c.Register("panicky", func(context.Context) error { panic("nope") })

	err := c.Shutdown(context.Background())
	if err == nil {
		t.Fatal("expected error from panicking hook")
	}
}

func TestDoneChannel(t *testing.T) {
	c := New(time.Second)
	c.Register("h", func(context.Context) error { return nil })

	go c.Shutdown(context.Background())

	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("Done channel did not close within 1s")
	}
}

func TestDeregister(t *testing.T) {
	c := New(time.Second)
	var ran atomic.Bool
	dereg := c.Register("h", func(context.Context) error { ran.Store(true); return nil })
	dereg()

	if err := c.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if ran.Load() {
		t.Error("deregistered hook should not have run")
	}
}
