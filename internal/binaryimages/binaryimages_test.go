package binaryimages

import "testing"

func TestParseLine(t *testing.T) {
	cases := []struct {
		line string
		want Image
	}{
		{ // sample(1), system framework listed by bundle ID
			"       0x18e0b2000 -        0x18f4f6fff  com.apple.AppKit (6.9 - 2487.20.107) <0b4a1d3e-5c6f-3a2b-9c0d-1e2f3a4b5c6d> /System/Library/Frameworks/AppKit.framework/Versions/C/AppKit",
			Image{Name: "AppKit", Path: "/System/Library/Frameworks/AppKit.framework/Versions/C/AppKit", UUID: "0B4A1D3E-5C6F-3A2B-9C0D-1E2F3A4B5C6D", Start: 0x18e0b2000, End: 0x18f4f6fff},
		},
		{ // sample(1), app-owned image with "+"
			"       0x104f00000 -        0x104f0ffff +com.example.Helper (1.0 - 1) <11111111-2222-3333-4444-555555555555> /Applications/Example.app/Contents/MacOS/Helper Tool",
			Image{Name: "Helper Tool", Path: "/Applications/Example.app/Contents/MacOS/Helper Tool", UUID: "11111111-2222-3333-4444-555555555555", Start: 0x104f00000, End: 0x104f0ffff},
		},
		{ // spindump, unknown end address
			"           0x1a2b00000 -                ???  dyld 1231.3 (1231.3)               <B1C2D3E4-F5A6-3B7C-8D9E-0F1A2B3C4D5E>  /usr/lib/dyld",
			Image{Name: "dyld", Path: "/usr/lib/dyld", UUID: "B1C2D3E4-F5A6-3B7C-8D9E-0F1A2B3C4D5E", Start: 0x1a2b00000},
		},
		{ // spindump kernel image, no path
			"   *0xfffffe0007004000 - 0xfffffe0007c0ffff  kernel.release.t8103 (11215.41.3) <D3E4F5A6-B7C8-3D9E-0F1A-2B3C4D5E6F70>",
			Image{Name: "kernel.release.t8103", UUID: "D3E4F5A6-B7C8-3D9E-0F1A-2B3C4D5E6F70", Start: 0xfffffe0007004000, End: 0xfffffe0007c0ffff},
		},
	}
	for _, c := range cases {
		got, ok := ParseLine(c.line)
		if !ok || got != c.want {
			t.Errorf("ParseLine(%q) = %+v %v, want %+v", c.line, got, ok, c.want)
		}
	}
	for _, bad := range []string{"", "Binary Images:", "  100 main  (in App) + 1  [0x1]", "0x2000 - 0x1000 backwards <11111111-2222-3333-4444-555555555555> /x"} {
		if _, ok := ParseLine(bad); ok {
			t.Errorf("ParseLine(%q) accepted", bad)
		}
	}
}

func TestLookup(t *testing.T) {
	table := NewTable([]Image{
		{Name: "b", Path: "/b", Start: 0x3000}, // open-ended
		{Name: "a", Path: "/a", UUID: "U", Start: 0x1000, End: 0x1fff},
		{Name: "c", Path: "/c", Start: 0x5000, End: 0x5fff},
	}, "X86-64")
	cases := []struct {
		addr   uint64
		image  string
		offset string
	}{
		{0x1000, "a", "0x0"},
		{0x1fff, "a", "0xfff"},
		{0x2000, "", ""}, // gap
		{0x0fff, "", ""}, // below every image
		{0x4abc, "b", "0x1abc"},
		{0x5010, "c", "0x10"},
		{0x6000, "", ""},
	}
	for _, c := range cases {
		loc, ok := table.Lookup(c.addr)
		if ok != (c.image != "") || loc.Image != c.image || loc.ImageOffset != c.offset {
			t.Errorf("Lookup(%#x) = %+v %v, want %s %s", c.addr, loc, ok, c.image, c.offset)
		}
	}
	loc, _ := table.Lookup(0x1010)
	want := Location{Image: "a", ImagePath: "/a", ImageUUID: "U", Arch: "x86_64", ImageBase: "0x1000", ImageOffset: "0x10", Address: "0x1010"}
	if loc != want {
		t.Errorf("Lookup = %+v, want %+v", loc, want)
	}
	var empty *Table
	if _, ok := empty.Lookup(1); ok || empty.Len() != 0 {
		t.Error("nil table resolved an address")
	}
}

func TestLookupFrame(t *testing.T) {
	table := NewTable([]Image{{Name: "Foo", Path: "/Foo", Start: 0x104a00000, End: 0x104afffff}}, "arm64e")
	loc, ok := table.LookupFrame("main + 128 (Foo + 4096) [0x104a01000]")
	if !ok || loc.ImageOffset != "0x1000" || loc.Arch != "arm64e" {
		t.Errorf("spindump frame = %+v %v", loc, ok)
	}
	if _, ok := table.LookupFrame("Thread_1   DispatchQueue_1"); ok {
		t.Error("frame without address resolved")
	}
}

func TestFromReport(t *testing.T) {
	report := `Process:         X [1]
Code Type:       ARM64

Call graph:
    1 main  (in X) + 1  [0x1010]

Binary Images:
       0x1000 -        0x1fff  X (1.0) <11111111-2222-3333-4444-555555555555> /x/X

Some trailer
       0x9000 -        0x9fff  late <11111111-2222-3333-4444-555555555556> /late
`
	table := FromReport(report)
	if table.Len() != 1 {
		t.Fatalf("images = %d, want 1 (section ends at the first non-image line)", table.Len())
	}
	if loc, ok := table.Lookup(0x1010); !ok || loc.Arch != "arm64" || loc.Image != "X" {
		t.Errorf("lookup = %+v %v", loc, ok)
	}
	if FromReport("no images").Len() != 0 {
		t.Error("report without section produced images")
	}
}

func TestNormalizeArch(t *testing.T) {
	for in, want := range map[string]string{"ARM64": "arm64", "arm64e": "arm64e", "X86-64": "x86_64", "x86_64h": "x86_64h", "???": "", "": ""} {
		if got := NormalizeArch(in); got != want {
			t.Errorf("NormalizeArch(%q) = %q, want %q", in, got, want)
		}
	}
}
