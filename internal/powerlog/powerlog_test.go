package powerlog

import (
	"errors"
	"strings"
	"testing"
)

const logFixture = `2026-08-06 00:15:03 -0500 Sleep               	Entering Sleep state due to 'Software Sleep pid=123':TCPKeepAlive=1
2026-08-06 01:00:00 -0500 DarkWake            	DarkWake to FullWake from Deep Idle [CDNVA] due to HID Activity
2026-08-06 01:15:00 -0500 Wake Requests       	[*process=dasd request=SleepService]
2026-08-06 01:30:03 -0500 Assertions          	PID 335(powerd) Summary PreventUserIdleSystemSleep "x" 04:46:14 id:0x1
2026-08-06 02:00:00 -0500 Wake                	Wake from Deep Idle [CDNVA] due to EC.LidOpen/HID Activity
some junk line that should be skipped
Total Sleep/Wakes since boot at 2026-07-31 04:04:49 -0500 :172`

const assertFixture = `Assertion status system-wide:
   PreventUserIdleSystemSleep     1
   pid 335(powerd): [0x00043c8200019fc7] 05:14:01 PreventUserIdleSystemSleep named: "Powerd - Prevent sleep while display is on"
   pid 400(WindowServer): [0x00040ef900099a91] 01:18:24 UserIsActive named: "keyboard tickle"  `

func TestParseLog(t *testing.T) {
	events := ParseLog(logFixture)
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3 (Sleep, DarkWake, Wake): %+v", len(events), events)
	}
	if events[0].Type != "Sleep" || events[1].Type != "DarkWake" || events[2].Type != "Wake" {
		t.Errorf("types = %s,%s,%s", events[0].Type, events[1].Type, events[2].Type)
	}
	if !strings.Contains(events[2].Detail, "due to") {
		t.Errorf("wake detail = %q, want a 'due to' reason", events[2].Detail)
	}
	if events[0].Time.IsZero() {
		t.Error("timestamp should parse to a non-zero time")
	}
}

func TestParseAssertions(t *testing.T) {
	blockers := ParseAssertions(assertFixture)
	if len(blockers) != 1 {
		t.Fatalf("blockers = %d, want 1 (only the Prevent* holder): %+v", len(blockers), blockers)
	}
	b := blockers[0]
	if b.PID != 335 || b.Process != "powerd" || b.Type != "PreventUserIdleSystemSleep" {
		t.Errorf("blocker = %+v", b)
	}
	if !strings.Contains(b.Reason, "Powerd") || b.Held != "05:14:01" {
		t.Errorf("blocker reason/held = %q / %q", b.Reason, b.Held)
	}
}

func TestCollect(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "log" {
			return []byte(logFixture), nil
		}
		return []byte(assertFixture), nil
	}
	r, err := Collect(run)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Events) != 3 || len(r.SleepBlockers) != 1 {
		t.Errorf("report = %d events, %d blockers", len(r.Events), len(r.SleepBlockers))
	}
}

func TestCollectError(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) { return nil, errors.New("boom") }
	if _, err := Collect(run); err == nil {
		t.Fatal("expected error when pmset fails")
	}
}
