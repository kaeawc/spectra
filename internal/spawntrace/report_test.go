package spawntrace

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/process"
)

func traceDeps(t0 time.Time) Deps {
	clk := clock.NewFake(t0)
	ticks := make(chan time.Time, 10)
	for i := 0; i < 10; i++ {
		ticks <- t0
	}
	base := []process.Info{{PID: 1, Command: "launchd", StartTime: t0.Add(-time.Hour)}}
	two := append(append([]process.Info{}, base...),
		process.Info{PID: 20, PPID: 1, Command: "a", StartTime: t0}, process.Info{PID: 21, PPID: 1, Command: "b", StartTime: t0})
	tables := [][]process.Info{base, two, two}
	n := 0
	return Deps{
		Collect: func(context.Context) []process.Info {
			clk.Advance(time.Second)
			i := n
			n++
			return tables[min(i, len(tables)-1)]
		},
		Ticker: func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} },
		Clock:  clk,
	}
}

func TestTraceCapsEventsButSummaryCountsAll(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rep, err := Trace(context.Background(), Options{Interval: time.Second, Duration: 3 * time.Second}, traceDeps(t0), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Events) != 1 || rep.EventsDropped != 1 || rep.Summary.Starts != 2 {
		t.Fatalf("events %d dropped %d starts %d", len(rep.Events), rep.EventsDropped, rep.Summary.Starts)
	}
	if _, err := Trace(context.Background(), Options{Interval: time.Second, Duration: 3 * time.Second, PID: 4242}, traceDeps(t0), 0); !errors.Is(err, ErrRootNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestOptionsValidate(t *testing.T) {
	for _, tc := range []struct {
		o    Options
		want error
	}{
		{DefaultOptions(), nil},
		{Options{Interval: 0, Duration: time.Second}, ErrInvalidOptions},
		{Options{Interval: time.Second, Duration: time.Second, PID: -1}, ErrInvalidOptions},
		{Options{Interval: 2 * time.Second, Duration: time.Second}, ErrIntervalTooLong},
	} {
		if err := tc.o.Validate(); !errors.Is(err, tc.want) {
			t.Errorf("%+v: err = %v, want %v", tc.o, err, tc.want)
		}
	}
}
