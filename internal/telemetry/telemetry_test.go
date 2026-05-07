package telemetry

import (
	"testing"
	"time"
)

type testSample struct {
	subject Subject
	at      time.Time
	value   int
}

func (s testSample) TelemetrySubject() Subject { return s.subject }
func (s testSample) TelemetryTakenAt() time.Time {
	return s.at
}

func TestLiveCollectorRecentPID(t *testing.T) {
	c := NewLiveCollector()
	base := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		c.Add(testSample{
			subject: Subject{Kind: "process", Runtime: "jvm", PID: 42},
			at:      base.Add(time.Duration(i) * time.Second),
			value:   i,
		})
	}

	got := c.RecentPID("process", 42, 2)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].(testSample).value != 3 || got[1].(testSample).value != 4 {
		t.Fatalf("values = %#v, want 3 and 4", got)
	}
}

func TestLiveCollectorRecentAllKeepsSubjectsSeparate(t *testing.T) {
	c := NewLiveCollector()
	at := time.Now().UTC()
	c.Add(testSample{subject: Subject{Kind: "process", Runtime: "jvm", PID: 42}, at: at, value: 1})
	c.Add(testSample{subject: Subject{Kind: "process", Runtime: "electron", PID: 43}, at: at, value: 2})

	got := c.RecentAll(10)
	if len(got) != 2 {
		t.Fatalf("subjects = %d, want 2", len(got))
	}
	if len(got["process:pid:42"]) != 1 || len(got["process:pid:43"]) != 1 {
		t.Fatalf("unexpected samples: %#v", got)
	}
}

func TestLiveCollectorFlushEvictsStaleSubjects(t *testing.T) {
	c := NewLiveCollector()
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	c.Add(testSample{subject: Subject{Kind: "process", PID: 42}, at: now.Add(-time.Hour), value: 1})
	c.Add(testSample{subject: Subject{Kind: "process", PID: 43}, at: now, value: 2})

	c.Flush(30*time.Minute, now)
	if got := c.RecentPID("process", 42, 1); got != nil {
		t.Fatalf("stale subject retained: %#v", got)
	}
	if got := c.RecentPID("process", 43, 1); len(got) != 1 {
		t.Fatalf("fresh subject missing: %#v", got)
	}
}
