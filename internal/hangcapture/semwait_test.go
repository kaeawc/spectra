package hangcapture

import "testing"

// realSleepSample is the call graph `sample` produced for `sleep 60` on
// macOS 26.6.
const realSleepSample = `Call graph:
    871 Thread_1492604   DispatchQueue_1: com.apple.main-thread  (serial)
      871 start  (in dyld) + 6992  [0x184e784e4]
        871 ???  (in sleep)  load address 0x100038000 + 0x740  [0x100038740]
          871 nanosleep  (in libsystem_c.dylib) + 220  [0x1850decc0]
            871 __semwait_signal  (in libsystem_kernel.dylib) + 8  [0x185203308]

Total number in stack (recursive counted multiple, when >=5):
`

func TestAnalyzeTimedSleepIsNotIOBlocked(t *testing.T) {
	a := Analyze(realSleepSample)
	if a.MainThread.Leaf != "__semwait_signal" {
		t.Fatalf("leaf = %q, want __semwait_signal", a.MainThread.Leaf)
	}
	if a.MainThread.Verdict != VerdictIdle {
		t.Errorf("verdict = %s, want idle (reason %q)", a.MainThread.Verdict, a.MainThread.Reason)
	}
}

func TestAnalyzeSemwaitWithoutSleepIsLockWait(t *testing.T) {
	sample := `Call graph:
    900 Thread_2   DispatchQueue_1: com.apple.main-thread  (serial)
      900 main  (in App) + 1  [0x1]
        900 semaphore_timedwait_signal  (in libsystem_kernel.dylib) + 1  [0x2]
          900 __semwait_signal  (in libsystem_kernel.dylib) + 1  [0x3]
`
	a := Analyze(sample)
	if a.MainThread.Verdict != VerdictLockBlocked {
		t.Errorf("verdict = %s, want lock-blocked (reason %q)", a.MainThread.Verdict, a.MainThread.Reason)
	}
}
