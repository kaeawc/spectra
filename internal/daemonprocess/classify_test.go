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
	s.grace = 20 * time.Millisecond
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

func TestClassifierTimeoutGraceKeepsPartialResults(t *testing.T) {
	var collect atomic.Int32
	s := NewService(Options{
		Collect: countingCollect(&collect), Timeout: 30 * time.Millisecond,
		Classify: func(ctx context.Context, procs []process.Info) error {
			procs[0].Role = classify.RoleApp
			<-ctx.Done()
			time.Sleep(40 * time.Millisecond) // finishing in-flight work after the deadline
			return ctx.Err()
		},
	})
	snap, err := s.ListClassified(context.Background(), false)
	if err != nil || snap.Warning == "" || snap.Processes[0].Role != classify.RoleApp {
		t.Fatalf("partial results lost: %+v err=%v", snap, err)
	}
}

func TestSlowClassificationDoesNotExpireCache(t *testing.T) {
	var collect, cls atomic.Int32
	clk := clock.NewFake(time.Unix(1000, 0))
	s := NewService(Options{
		Collect: countingCollect(&collect), Clock: clk,
		Classify: func(ctx context.Context, p []process.Info) error {
			clk.Advance(5 * DefaultTTL) // classification outlasts the TTL
			return fakeClassify(&cls)(ctx, p)
		},
	})
	first, _ := s.ListClassified(context.Background(), false)
	second, _ := s.ListClassified(context.Background(), false)
	if cls.Load() != 1 || collect.Load() != 1 || !second.SampledAt.Equal(first.SampledAt) {
		t.Fatalf("classify=%d collect=%d: slow classification stored an expired result", cls.Load(), collect.Load())
	}
	if !first.SampledAt.Equal(time.Unix(1000, 0)) {
		t.Fatalf("sampled_at moved off the collection time: %v", first.SampledAt)
	}
	clk.Advance(DefaultTTL)
	_, _ = s.ListClassified(context.Background(), false)
	if cls.Load() != 2 {
		t.Fatalf("classified cache never expires: %d", cls.Load())
	}
}

func TestSubscribeVariantsShareOneSnapshotPerTick(t *testing.T) {
	var collect, cls atomic.Int32
	s := NewService(Options{
		Collect: countingCollect(&collect), Classify: fakeClassify(&cls),
		Interval: 30 * time.Millisecond, TTL: time.Millisecond,
	})
	done := make(chan struct{})
	defer close(done)
	plain, class := make(chan Snapshot, 16), make(chan Snapshot, 16)
	s.SubscribeClassified(done, false, func(_ string, p any) error { plain <- p.(Snapshot); return nil })
	s.SubscribeClassified(done, true, func(_ string, p any) error { class <- p.(Snapshot); return nil })
	for i := 0; i < 3; i++ {
		a, b := <-plain, <-class
		if !a.SampledAt.Equal(b.SampledAt) || a.Processes[0].PID != b.Processes[0].PID {
			t.Fatalf("tick %d: variants differ: %v vs %v", i, a.SampledAt, b.SampledAt)
		}
	}
}

func TestClassifierRotates(t *testing.T) {
	clk := clock.NewFake(time.Unix(1000, 0))
	builds := 0
	r := &rotatingClassifier{clock: clk, maxAge: time.Minute, build: func() *classify.Classifier {
		builds++
		return classify.New(classify.Options{})
	}}
	a := r.current()
	clk.Advance(time.Minute - time.Second)
	if r.current() != a || builds != 1 {
		t.Fatalf("rebuilt too early: builds=%d", builds)
	}
	clk.Advance(time.Second)
	if b := r.current(); b == a || builds != 2 {
		t.Fatalf("not rebuilt after maxAge: builds=%d", builds)
	}
}
