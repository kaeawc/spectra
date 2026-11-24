package flamegraph

import (
	"strings"
	"testing"
)

const imagesSection = `
Binary Images:
       0x100000000 -        0x100003fff +com.example.App (1.0 - 1) <11111111-2222-3333-4444-555555555555> /Applications/App.app/Contents/MacOS/App
`

const graphWithAddrs = `Code Type:       X86-64

Call graph:
    100 Thread_1   DispatchQueue_1: main-thread  (serial)
      100 start  (in dyld) + 10  [0x7ff800001000]
        100 main  (in App) + 20  [0x100001000]
          60 work  (in App) + 4  [0x100002010]

Total number in stack:
      100 something else
`

func TestFoldFrameDetails(t *testing.T) {
	folded := Fold(graphWithAddrs + imagesSection)
	byLeaf := map[string]Folded{}
	for _, f := range folded {
		byLeaf[f.Frames[len(f.Frames)-1]] = f
	}
	work := byLeaf["work"]
	if len(work.FrameDetails) != 4 {
		t.Fatalf("frame_details = %+v", work.FrameDetails)
	}
	if !work.FrameDetails[0].IsZero() || !work.FrameDetails[1].IsZero() {
		t.Errorf("thread header and dyld (not in Binary Images) should be empty: %+v", work.FrameDetails[:2])
	}
	d := work.FrameDetails[3]
	if d.Image != "App" || d.ImagePath != "/Applications/App.app/Contents/MacOS/App" || d.ImageUUID != "11111111-2222-3333-4444-555555555555" ||
		d.ImageOffset != "0x2010" || d.Arch != "x86_64" || d.Address != "0x100002010" || d.ImageBase != "0x100000000" {
		t.Errorf("work details = %+v", d)
	}
	if byLeaf["main"].FrameDetails[2].ImageOffset != "0x1000" {
		t.Errorf("main self stack details = %+v", byLeaf["main"].FrameDetails)
	}
}

func TestFoldWithoutImagesHasNoDetails(t *testing.T) {
	for _, f := range Fold(graphWithAddrs) {
		if f.FrameDetails != nil {
			t.Errorf("details without a Binary Images section: %+v", f)
		}
	}
}

func TestRenderSVGIgnoresFrameDetails(t *testing.T) {
	with := RenderSVG(Fold(graphWithAddrs+imagesSection), "t")
	without := RenderSVG(Fold(graphWithAddrs), "t")
	if with != without {
		t.Error("SVG changed when the report carried Binary Images")
	}
	if !strings.Contains(with, "<title>work (60 samples") {
		t.Errorf("unexpected SVG:\n%s", with)
	}
}
