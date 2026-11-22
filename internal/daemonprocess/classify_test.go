package daemonprocess

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/classify"
	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/process"
)

func fakeClassify(calls *atomic.Int32) ClassifyFunc {
	return func(_ context.Context, procs []process.Info) error {
		calls.Add(1)
		for i := range procs {
			procs[i].Role = classify.RoleApp
			procs[i].Runtime = classify.Runtime("jvm")
			procs[i].App = &classify.App{Name: "App"}
			procs[i].Evidence = []string{"fake"}
		}
		return nil
	}
}

func newClassifySvc(collect, cls *atomic.Int32) *Service {
	return NewService(Options{
		Collect: countingCollect(collect), Classify: fakeClassify(cls),
		Clock: clock.NewFake(time.Unix(1000, 0)),
	})
}

func TestClassifySingleFlightAndCache(t *testing.T) {
	var collect, cls atomic.Int32
	release := make(chan struct{})
	s := NewService(Options{
		Collect: countingCollect(&collect), Clock: clock.NewFake(time.Unix(1000, 0)),
		Classify: func(ctx context.Context, p []process.Info) error {
			<-release
			return fakeClassify(&cls)(ctx, p)
		},
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snap, err := s.ListClassified(context.Background(), false)
			if err != nil || snap.Processes[0].Role != classify.RoleApp {
				t.Errorf("snap=%+v err=%v", snap, err)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if _, err := s.ListClassified(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if cls.Load() != 1 || collect.Load() != 1 {
		t.Fatalf("classify=%d collect=%d, want 1/1", cls.Load(), collect.Load())
	}
}

func TestPlainNeverClassifiesAndVariantsAreSeparate(t *testing.T) {
	var collect, cls atomic.Int32
	s := newClassifySvc(&collect, &cls)
	ctx := context.Background()
	plain, _ := s.List(ctx, false)
	if cls.Load() != 0 || plain.Processes[0].Role != "" {
		t.Fatalf("plain request classified: calls=%d %+v", cls.Load(), plain.Processes[0])
	}
	class, err := s.ListClassified(ctx, false)
	if err != nil || class.Processes[0].Role != classify.RoleApp || cls.Load() != 1 {
		t.Fatalf("classified: %+v err=%v calls=%d", class, err, cls.Load())
	}
	if collect.Load() != 1 {
		t.Fatalf("classified request recollected a fresh plain snapshot: %d", collect.Load())
	}
	again, _ := s.List(ctx, false)
	if again.Processes[0].Role != "" || again.Processes[0].App != nil {
		t.Fatalf("classification leaked into the plain cache: %+v", again.Processes[0])
	}
	if c2, _ := s.ListClassified(ctx, false); c2.Processes[0].Role == "" || cls.Load() != 1 {
		t.Fatalf("classified cache not served: %+v calls=%d", c2, cls.Load())
	}
}

func TestClassifiedPayloadMatchesCLIRows(t *testing.T) {
	in := func() []process.Info {
		return []process.Info{{PID: 1, Command: "a", RSSKiB: 100}, {PID: 2, Command: "b", RSSKiB: 900}}
	}
	var cls atomic.Int32
	s := NewService(Options{
		Collect:  func(context.Context) ([]process.Info, error) { return in(), nil },
		Classify: fakeClassify(&cls), Clock: clock.NewFake(time.Unix(1000, 0)),
	})
	snap, err := s.ListClassified(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	want := in()
	want[0], want[1] = want[1], want[0]
	_ = fakeClassify(&cls)(context.Background(), want)
	got, _ := json.Marshal(snap.Processes)
	exp, _ := json.Marshal(want)
	if string(got) != string(exp) {
		t.Fatalf("rows differ:\n%s\n%s", got, exp)
	}
	if snap.Warning != "" {
		t.Fatalf("unexpected warning %q", snap.Warning)
	}
}

func TestClassifierErrorKeepsRowsWithWarning(t *testing.T) {
	for name, fn := range map[string]ClassifyFunc{
		"error": func(context.Context, []process.Info) error { return errors.New("boom") },
		"panic": func(context.Context, []process.Info) error { panic("boom") },
	} {
		t.Run(name, func(t *testing.T) {
			var collect atomic.Int32
			s := NewService(Options{Collect: countingCollect(&collect), Classify: fn, Clock: clock.NewFake(time.Unix(1000, 0))})
			snap, err := s.ListClassified(context.Background(), false)
			if err != nil || len(snap.Processes) != 1 || snap.Processes[0].Role != "" || snap.Warning == "" {
				t.Fatalf("snap=%+v err=%v", snap, err)
			}
		})
	}
}

func TestClassifierTimeoutKeepsRowsWithWarning(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	var collect atomic.Int32
	s := NewService(Options{
		Collect: countingCollect(&collect), Timeout: 30 * time.Millisecond,
		Classify: func(context.Context, []process.Info) error { <-block; return nil },
	})
	snap, err := s.ListClassified(context.Background(), false)
	if err != nil || len(snap.Processes) != 1 || snap.Warning == "" {
		t.Fatalf("snap=%+v err=%v", snap, err)
	}
}

func TestClassifiedRequestPropagatesCollectionError(t *testing.T) {
	var cls atomic.Int32
	s := NewService(Options{
		Collect:  func(context.Context) ([]process.Info, error) { return nil, errors.New("ps failed") },
		Classify: fakeClassify(&cls),
	})
	if _, err := s.ListClassified(context.Background(), false); err == nil || cls.Load() != 0 {
		t.Fatalf("err=%v classify=%d", err, cls.Load())
	}
}

func TestSubscribeMixedVariants(t *testing.T) {
	var collect, cls atomic.Int32
	s := NewService(Options{
		Collect: countingCollect(&collect), Classify: fakeClassify(&cls),
		Interval: 40 * time.Millisecond, TTL: 20 * time.Millisecond,
	})
	done := make(chan struct{})
	defer close(done)
	plain, class := make(chan Snapshot, 16), make(chan Snapshot, 16)
	s.SubscribeClassified(done, false, func(_ string, p any) error { plain <- p.(Snapshot); return nil })
	s.SubscribeClassified(done, true, func(_ string, p any) error { class <- p.(Snapshot); return nil })
	for i := 0; i < 2; i++ {
		select {
		case snap := <-plain:
			if snap.Processes[0].Role != "" {
				t.Fatalf("plain subscriber got classified rows: %+v", snap)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no plain update")
		}
		select {
		case snap := <-class:
			if snap.Processes[0].Role != classify.RoleApp {
				t.Fatalf("classified subscriber got plain rows: %+v", snap)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no classified update")
		}
	}
	if c, n := cls.Load(), collect.Load(); c < 2 || c > n {
		t.Fatalf("classify=%d collect=%d, want one classification per collected tick", c, n)
	}
}

func TestSubscribePlainOnlyNeverClassifies(t *testing.T) {
	var collect, cls atomic.Int32
	s := NewService(Options{Collect: countingCollect(&collect), Classify: fakeClassify(&cls), Interval: 10 * time.Millisecond, TTL: time.Millisecond})
	done := make(chan struct{})
	defer close(done)
	got := make(chan Snapshot, 16)
	s.Subscribe(done, func(_ string, p any) error { got <- p.(Snapshot); return nil })
	<-got
	<-got
	if cls.Load() != 0 {
		t.Fatalf("classifier ran for plain-only subscribers: %d", cls.Load())
	}
}
