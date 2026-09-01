package hangcapture

import "testing"

// buildSample wraps a main-thread call chain (leaf last) in a minimal sample
// "Call graph" with the given deepest-leaf symbols, plus an unrelated thread.
func idleSample() string {
	return `Call graph:
    500 Thread_1   DispatchQueue_2: com.apple.libdispatch  (serial)
      500 worker  (in App) + 1  [0x1]
    900 Thread_2   DispatchQueue_1: com.apple.main-thread  (serial)
      900 start  (in dyld) + 1  [0x2]
        900 CFRunLoopRunSpecific  (in CoreFoundation) + 1  [0x3]
          900 __CFRunLoopServiceMachPort  (in CoreFoundation) + 1  [0x4]
            900 mach_msg2_trap  (in libsystem_kernel.dylib) + 1  [0x5]
`
}

func TestAnalyzeIdle(t *testing.T) {
	a := Analyze(idleSample())
	if !a.MainThread.Found {
		t.Fatal("main thread not found")
	}
	if a.MainThread.Verdict != VerdictIdle {
		t.Errorf("verdict = %s, want idle (leaf %q)", a.MainThread.Verdict, a.MainThread.Leaf)
	}
}

func TestAnalyzeLockBlocked(t *testing.T) {
	sample := `Call graph:
    900 Thread_2   DispatchQueue_1: com.apple.main-thread  (serial)
      900 main  (in App) + 1  [0x1]
        900 -[Store save]  (in App) + 1  [0x2]
          900 pthread_mutex_lock  (in libsystem_pthread.dylib) + 1  [0x3]
            900 __psynch_mutexwait  (in libsystem_kernel.dylib) + 1  [0x4]
`
	a := Analyze(sample)
	if a.MainThread.Verdict != VerdictLockBlocked {
		t.Errorf("verdict = %s, want lock-blocked (leaf %q)", a.MainThread.Verdict, a.MainThread.Leaf)
	}
	if a.MainThread.Leaf != "__psynch_mutexwait" {
		t.Errorf("leaf = %q", a.MainThread.Leaf)
	}
}

func TestAnalyzeIOBlocked(t *testing.T) {
	sample := `Call graph:
    900 Thread_2   DispatchQueue_1: com.apple.main-thread  (serial)
      900 main  (in App) + 1  [0x1]
        900 -[Net fetch]  (in App) + 1  [0x2]
          900 read  (in libsystem_kernel.dylib) + 1  [0x3]
`
	if v := Analyze(sample).MainThread.Verdict; v != VerdictIOBlocked {
		t.Errorf("verdict = %s, want io-blocked", v)
	}
}

func TestAnalyzeSpinning(t *testing.T) {
	sample := `Call graph:
    900 Thread_2   DispatchQueue_1: com.apple.main-thread  (serial)
      900 main  (in App) + 1  [0x1]
        900 -[Layout compute]  (in App) + 1  [0x2]
          900 crunchNumbers  (in App) + 1  [0x3]
`
	a := Analyze(sample)
	if a.MainThread.Verdict != VerdictSpinning {
		t.Errorf("verdict = %s, want spinning", a.MainThread.Verdict)
	}
	if a.MainThread.Leaf != "crunchNumbers" {
		t.Errorf("leaf = %q", a.MainThread.Leaf)
	}
}

func TestAnalyzeMachMsgOutsideRunLoop(t *testing.T) {
	// mach_msg without a CFRunLoop in the stack is a synchronous IPC wait, not idle.
	sample := `Call graph:
    900 Thread_2   DispatchQueue_1: com.apple.main-thread  (serial)
      900 main  (in App) + 1  [0x1]
        900 xpc_connection_send_message_with_reply_sync  (in libxpc) + 1  [0x2]
          900 mach_msg2_trap  (in libsystem_kernel.dylib) + 1  [0x3]
`
	if v := Analyze(sample).MainThread.Verdict; v != VerdictLockBlocked {
		t.Errorf("verdict = %s, want lock-blocked (synchronous IPC)", v)
	}
}

func TestAnalyzeNoMainThread(t *testing.T) {
	sample := `Call graph:
    500 Thread_1   DispatchQueue_2: com.apple.libdispatch  (serial)
      500 worker  (in App) + 1  [0x1]
`
	a := Analyze(sample)
	if a.MainThread.Found {
		t.Error("should not find a main thread")
	}
	if a.MainThread.Verdict != VerdictUnknown {
		t.Errorf("verdict = %s, want unknown", a.MainThread.Verdict)
	}
}

func TestTopFramesLeafLast(t *testing.T) {
	a := Analyze(idleSample())
	frames := a.MainThread.TopFrames
	if len(frames) == 0 || frames[len(frames)-1] != "mach_msg2_trap" {
		t.Errorf("leaf should be last, got %v", frames)
	}
}
