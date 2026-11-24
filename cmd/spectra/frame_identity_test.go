package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/binaryimages"
	"github.com/kaeawc/spectra/internal/nativeprofile"
	"github.com/kaeawc/spectra/internal/process"
)

func sampleDepsFrom(t *testing.T, fixture string) sampleDeps {
	t.Helper()
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	return sampleDeps{capture: func(_ context.Context, o process.SampleOptions) (process.SampleResult, error) {
		return process.SampleResult{PID: o.PID, TakenAt: time.Unix(1700000000, 0).UTC(),
			DurationMS: o.Duration.Milliseconds(), IntervalMS: o.IntervalMS, Output: string(data)}, nil
	}}
}

// A report without a Binary Images section must encode exactly as before
// frame identity was added.
func TestSampleJSONWithoutImagesIsByteIdentical(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runSampleWithIO([]string{"--json", "412"}, &out, &errb, sampleDepsFrom(t, "testdata/sample_report.txt")); code != 0 {
		t.Fatalf("code %d stderr %s", code, errb.String())
	}
	want, err := os.ReadFile("testdata/sample_report.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != string(want) {
		t.Errorf("sample --json changed:\n%s", out.String())
	}
}

func TestSampleJSONFrameDetails(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runSampleWithIO([]string{"--json", "412"}, &out, &errb, sampleDepsFrom(t, "testdata/sample_report_images.txt")); code != 0 {
		t.Fatalf("code %d stderr %s", code, errb.String())
	}
	var rep nativeprofile.SampleReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	byLeaf := map[string]nativeprofile.SampleStack{}
	for _, s := range rep.Stacks {
		byLeaf[s.Frames[len(s.Frames)-1]] = s
	}
	hot := byLeaf["mach_msg2_trap"]
	if strings.Join(hot.Frames, ";") != "Thread_8812;start;main;mach_msg2_trap" {
		t.Fatalf("frames = %v", hot.Frames)
	}
	if len(hot.FrameDetails) != len(hot.Frames) {
		t.Fatalf("frame_details not parallel to frames: %+v", hot.FrameDetails)
	}
	if !hot.FrameDetails[0].IsZero() {
		t.Errorf("thread header got details: %+v", hot.FrameDetails[0])
	}
	want := []binaryimages.Location{
		{Image: "dyld", ImagePath: "/usr/lib/dyld", ImageUUID: "B1C2D3E4-F5A6-3B7C-8D9E-0F1A2B3C4D5E", Arch: "arm64", ImageBase: "0x18a3cb000", ImageOffset: "0x6b98", Address: "0x18a3d1b98"},
		{Image: "Finder", ImagePath: "/System/Library/CoreServices/Finder.app/Contents/MacOS/Finder", ImageUUID: "8A2D1D4C-5E6F-3A2B-9C0D-1E2F3A4B5C6D", Arch: "arm64", ImageBase: "0x100a4c000", ImageOffset: "0x4078", Address: "0x100a50078"},
		{Image: "libsystem_kernel.dylib", ImagePath: "/usr/lib/system/libsystem_kernel.dylib", ImageUUID: "C2D3E4F5-A6B7-3C8D-9E0F-1A2B3C4D5E6F", Arch: "arm64", ImageBase: "0x18a6e2000", ImageOffset: "0x2c34", Address: "0x18a6e4c34"},
	}
	for i, w := range want {
		if got := hot.FrameDetails[i+1]; got != w {
			t.Errorf("frame %d (%s) = %+v, want %+v", i+1, hot.Frames[i+1], got, w)
		}
	}
	unknown := byLeaf["???"]
	if n := len(unknown.FrameDetails); n != 4 || !unknown.FrameDetails[3].IsZero() || unknown.FrameDetails[2].ImageOffset != "0x4078" {
		t.Errorf("frame outside every image should have empty details: %+v", unknown.FrameDetails)
	}
	if !strings.Contains(out.String(), `"image_offset": "0x4a28"`) {
		t.Errorf("worker frame offset missing:\n%s", out.String())
	}
}

func TestFlamegraphJSONCarriesFrameDetails(t *testing.T) {
	data, err := os.ReadFile("testdata/sample_report_images.txt")
	if err != nil {
		t.Fatal(err)
	}
	written := map[string]string{}
	var out, errb bytes.Buffer
	code := runFlamegraphWithIO([]string{"--json", "--input", "/s.txt", "--out", "/g.svg"}, &out, &errb,
		fgDeps("", nil, map[string]string{"/s.txt": string(data)}, written))
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, errb.String())
	}
	if !strings.HasPrefix(written["/g.svg"], "<svg") {
		t.Fatalf("svg not written")
	}
	var rep nativeprofile.FlamegraphReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("decode: %v\n%s", err, out.String())
	}
	if rep.SVG != "/g.svg" || rep.StackCount != 4 || len(rep.Stacks) != 4 {
		t.Fatalf("report = %+v", rep)
	}
	found := false
	for _, s := range rep.Stacks {
		for i, d := range s.FrameDetails {
			if s.Frames[i] == "main" && d.ImageOffset == "0x4078" && d.ImageUUID == "8A2D1D4C-5E6F-3A2B-9C0D-1E2F3A4B5C6D" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("main frame details missing:\n%s", out.String())
	}
}

func TestFlamegraphTextModeUnchangedByImages(t *testing.T) {
	written := map[string]string{}
	var out, errb bytes.Buffer
	if code := runFlamegraphWithIO([]string{"4012"}, &out, &errb, fgDeps(fgSample, nil, nil, written)); code != 0 {
		t.Fatalf("code %d", code)
	}
	if out.String() != "wrote 4012.flamegraph.svg (1 folded stacks)\n" {
		t.Errorf("stdout = %q", out.String())
	}
}
