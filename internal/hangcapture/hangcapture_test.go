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

// Real `sample` output prefixes nested frames with "+" (and "!", ":", "|" at
// branches). These fixtures are trimmed from real macOS 26 captures.
func TestAnalyzeRealSampleMarkers(t *testing.T) {
	cases := []struct {
		name   string
		sample string
		want   Verdict
		leaf   string
	}{
		{
			name: "idle run loop",
			sample: `Call graph:
    170 Thread_6828   DispatchQueue_1: com.apple.main-thread  (serial)
    + 170 start  (in dyld) + 6992  [0x187b804e4]
    +   170 ???  (in Dock)  load address 0x1005e0000 + 0x60d0  [0x1005e60d0]
    +     170 _CFRunLoopRunSpecificWithOptions  (in CoreFoundation) + 532  [0x1880da264]
    +       170 __CFRunLoopRun  (in CoreFoundation) + 1188  [0x1880079f4]
    +         170 __CFRunLoopServiceMachPort  (in CoreFoundation) + 160  [0x188009108]
    +           170 mach_msg  (in libsystem_kernel.dylib) + 24  [0x187f07fc0]
    +             170 mach_msg2_trap  (in libsystem_kernel.dylib) + 8  [0x187f07c34]
    170 Thread_6829
    + 170 start_wqthread  (in libsystem_pthread.dylib) + 8  [0x187f45b80]
    +   170 __workq_kernreturn  (in libsystem_kernel.dylib) + 8  [0x187f09f0c]
`,
			want: VerdictIdle, leaf: "mach_msg2_trap",
		},
		{
			name: "lock with branch markers",
			sample: `Call graph:
    800 Thread_185646496   DispatchQueue_1: com.apple.main-thread  (serial)
    + 800 start  (in dyld) + 6992  [0x187b804e4]
    +   800 main  (in lock) + 52  [0x100003f34]  lock.c:20
    +     790 pthread_mutex_lock  (in libsystem_pthread.dylib) + 96  [0x187f46a1c]
    +     ! 790 __psynch_mutexwait  (in libsystem_kernel.dylib) + 8  [0x187f0a5f0]
    +     10 helper  (in lock) + 8  [0x100003f00]
    800 Thread_185646497
    + 800 thread_start  (in libsystem_pthread.dylib) + 8  [0x187f45b8c]
    +   800 __semwait_signal  (in libsystem_kernel.dylib) + 8  [0x187f0b2c0]
`,
			want: VerdictLockBlocked, leaf: "__psynch_mutexwait",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mt := Analyze(tc.sample).MainThread
			if mt.Verdict != tc.want || mt.Leaf != tc.leaf {
				t.Errorf("verdict=%s leaf=%q, want %s leaf=%q", mt.Verdict, mt.Leaf, tc.want, tc.leaf)
			}
		})
	}
}
