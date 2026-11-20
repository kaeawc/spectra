package classify

import (
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

// TestUnstrippedRustWithoutLangStart covers rustc -C lto=fat, which can
// inline std::rt::lang_start into main while other local symbols survive.
func TestUnstrippedRustWithoutLangStart(t *testing.T) {
	cases := []struct {
		name string
		syms []elfSym
		want Runtime
		ev   string
	}{
		{"LTO Rust: locals, no lang_start, no C++ runtime", []elfSym{
			{name: "_ZN4core3fmt5write17h0123456789abcdefE", local: true},
			{name: "_ZN5hello4main17hfedcba9876543210E", local: true},
			{name: "main"},
		}, RuntimeRust, "link-time optimization"},
		{"plain Rust: lang_start defined", []elfSym{
			{name: "_ZN3std2rt10lang_start17h0123456789abcdefE", local: true},
			{name: "main"},
		}, RuntimeRust, "defines std::rt::lang_start"},
		{"unstripped C++ main linking Rust (adb-like)", []elfSym{
			{name: "_ZN4core3fmt5write17h0123456789abcdefE", local: true},
			{name: "_ZNSt3__112basic_stringIcNS_11char_traitsIcEENS_9allocatorIcEEED1Ev"},
			{name: "__gxx_personality_v0"},
			{name: "main"},
		}, RuntimeNative, "with a C++ runtime"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := newFakeFS()
			files.add("/proc/10/exe", buildELFWithSymtab(rustData(), tc.syms...))
			c := New(Options{OS: hostos.Linux, FS: files, Runner: proc.NewFake()})
			r := classifyOne(t, c, Input{PID: 10, PPID: 1, UID: 1000, Command: "hello", ExecutablePath: "/opt/hello", CommandLine: "/opt/hello"})
			if r.Runtime != tc.want || !strings.Contains(strings.Join(r.Evidence, "\n"), tc.ev) {
				t.Fatalf("runtime=%s evidence=%v, want %s with %q", r.Runtime, r.Evidence, tc.want, tc.ev)
			}
		})
	}
}
