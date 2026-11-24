package crashreport

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"
)

const (
	appPath     = "/Applications/MyApp.app/Contents/MacOS/MyApp"
	updatedPath = "/Applications/MyApp.app/Contents/Frameworks/Updated.framework/Versions/A/Updated"
	kernelPath  = "/usr/lib/system/libsystem_kernel.dylib"
)

func loadFixture(t *testing.T) *Report {
	t.Helper()
	data, err := os.ReadFile("testdata/frames.ips")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return r
}

// thinMachO returns a minimal 64-bit Mach-O whose only load command is LC_UUID.
func thinMachO(cpu macho.Cpu, sub uint32, uuid string) []byte {
	raw, err := hex.DecodeString(strings.ReplaceAll(uuid, "-", ""))
	if err != nil || len(raw) != 16 {
		panic("bad uuid " + uuid)
	}
	le := binary.LittleEndian
	var b bytes.Buffer
	_ = binary.Write(&b, le, []uint32{macho.Magic64, uint32(cpu), sub, uint32(macho.TypeDylib), 1, 24, 0, 0})
	_ = binary.Write(&b, le, []uint32{uint32(loadCmdUUID), 24})
	b.Write(raw)
	return b.Bytes()
}

type slice struct {
	cpu  macho.Cpu
	sub  uint32
	data []byte
}

func fatMachO(slices ...slice) []byte {
	be := binary.BigEndian
	var b bytes.Buffer
	_ = binary.Write(&b, be, []uint32{macho.MagicFat, uint32(len(slices))})
	off := 4096
	for _, s := range slices {
		_ = binary.Write(&b, be, []uint32{uint32(s.cpu), s.sub, uint32(off), uint32(len(s.data)), 12})
		off += 4096
	}
	for _, s := range slices {
		pad := make([]byte, 4096-b.Len()%4096)
		b.Write(pad)
		b.Write(s.data)
	}
	return b.Bytes()
}

type memFile struct{ *bytes.Reader }

func (memFile) Close() error { return nil }

func memOpener(files map[string][]byte) OpenFunc {
	return func(p string) (BinaryFile, error) {
		data, ok := files[p]
		if !ok {
			return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
		}
		return memFile{bytes.NewReader(data)}, nil
	}
}

// TestFixtureFramesUnchanged pins Thread.Frames to the strings origin/main
// produced for testdata/frames.ips before frame_details existed.
func TestFixtureFramesUnchanged(t *testing.T) {
	r := loadFixture(t)
	want := [][]string{
		{"MyApp`main + 0"},
		{
			"libsystem_kernel.dylib`__pthread_kill + 8",
			"Updated`UpdatedThing.run() + 120",
			"Gone + 0x1234",
			"MyApp + 0x29f50",
			"??? + 0x75bcd15",
			"??? + 0x1000",
		},
	}
	if len(r.Threads) != len(want) {
		t.Fatalf("threads = %d, want %d", len(r.Threads), len(want))
	}
	for i, th := range r.Threads {
		if !reflect.DeepEqual(th.Frames, want[i]) {
			t.Errorf("thread %d frames = %q, want %q", i, th.Frames, want[i])
		}
		if len(th.FrameDetails) != len(th.Frames) {
			t.Errorf("thread %d: %d frame_details for %d frames", i, len(th.FrameDetails), len(th.Frames))
		}
	}
}

func i64(v int64) *int64 { return &v }

func TestFixtureFrameDetails(t *testing.T) {
	got := loadFixture(t).Threads[1].FrameDetails
	want := []FrameDetail{
		{Image: "libsystem_kernel.dylib", ImagePath: kernelPath, ImageUUID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Arch: "arm64e",
			ImageBase: "0x18e0a4000", ImageOffset: "0x92cc", Address: "0x18e0ad2cc", Symbol: "__pthread_kill", SymbolOffset: i64(8)},
		{Image: "Updated", ImagePath: updatedPath, ImageUUID: "33333333-4444-5555-6666-777777777777", Arch: "arm64",
			ImageBase: "0x101100000", ImageOffset: "0x12110", Address: "0x101112110", Symbol: "UpdatedThing.run()", SymbolOffset: i64(120)},
		{Image: "Gone", ImagePath: "/Applications/MyApp.app/Contents/PlugIns/Gone.bundle/Contents/MacOS/Gone", ImageUUID: "22222222-3333-4444-5555-666666666666",
			Arch: "arm64", ImageBase: "0x101000000", ImageOffset: "0x1234", Address: "0x101001234"},
		{Image: "MyApp", ImagePath: appPath, ImageUUID: "11111111-2222-3333-4444-555555555555", Arch: "arm64",
			ImageBase: "0x100000000", ImageOffset: "0x29f50", Address: "0x100029f50"},
		{ImageOffset: "0x75bcd15"}, // imageIndex -1
		{ImageOffset: "0x1000"},    // nameless image with zero base and zero UUID
	}
	if !reflect.DeepEqual(got, want) {
		gj, _ := json.MarshalIndent(got, "", " ")
		t.Errorf("frame_details =\n%s", gj)
	}
	main := loadFixture(t).Threads[0].FrameDetails[0]
	if main.SymbolOffset == nil || *main.SymbolOffset != 0 {
		t.Errorf("symbol_offset at +0 must be present, got %v", main.SymbolOffset)
	}
}

func TestFrameDetailsJSONOmitsAbsentFields(t *testing.T) {
	b, err := json.Marshal(loadFixture(t).Threads[1].FrameDetails[4])
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"image_offset":"0x75bcd15"}`; got != want {
		t.Errorf("json = %s, want %s", got, want)
	}
}

func TestCheckImagesOnDisk(t *testing.T) {
	r := loadFixture(t)
	r.CheckImagesOnDisk(memOpener(map[string][]byte{
		appPath: thinMachO(macho.CpuArm64, 0, "11111111-2222-3333-4444-555555555555"),
		// rebuilt since the crash: the arm64 slice has a new UUID; the x86_64
		// slice carries the old one to prove the arch-matching slice is read.
		updatedPath: fatMachO(
			slice{macho.CpuAmd64, 3, thinMachO(macho.CpuAmd64, 3, "33333333-4444-5555-6666-777777777777")},
			slice{macho.CpuArm64, 0, thinMachO(macho.CpuArm64, 0, "99999999-4444-5555-6666-777777777777")},
		),
	}))
	got := map[string]string{}
	for _, im := range r.Images {
		got[im.Name] = im.OnDisk
	}
	want := map[string]string{
		"MyApp":                  OnDiskMatch,
		"libsystem_kernel.dylib": OnDiskSharedCache,
		"Gone":                   OnDiskMissing,
		"Updated":                OnDiskChanged,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("image_on_disk = %v, want %v", got, want)
	}
	if r.Images[0].Base != "0x100000000" || r.Images[0].UUID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("image[0] = %+v", r.Images[0])
	}
}

func TestImageOnDiskSystemPathAbsent(t *testing.T) {
	im := Image{Name: "AppKit", Path: "/System/Library/Frameworks/AppKit.framework/Versions/C/AppKit", UUID: "44444444-0000-0000-0000-000000000000"}
	if got := imageOnDisk(im, memOpener(nil)); got != OnDiskSharedCache {
		t.Errorf("absent system image = %q, want shared_cache", got)
	}
	im.Path = kernelPath
	present := memOpener(map[string][]byte{kernelPath: thinMachO(macho.CpuArm64, 2, im.UUID)})
	if got := imageOnDisk(im, present); got != OnDiskMatch {
		t.Errorf("present system image = %q, want match", got)
	}
}

func TestImageOnDiskUnknown(t *testing.T) {
	denied := func(string) (BinaryFile, error) { return nil, fs.ErrPermission }
	im := Image{Path: appPath, UUID: "11111111-2222-3333-4444-555555555555", Arch: "arm64"}
	if got := imageOnDisk(im, denied); got != "" {
		t.Errorf("unreadable = %q, want empty", got)
	}
	notMachO := memOpener(map[string][]byte{appPath: []byte("\x7fELF not a mach-o binary at all....")})
	if got := imageOnDisk(im, notMachO); got != "" {
		t.Errorf("non-Mach-O = %q, want empty", got)
	}
	im.UUID = ""
	if got := imageOnDisk(im, memOpener(nil)); got != "" {
		t.Errorf("no report uuid = %q, want empty", got)
	}
}

func TestMachOUUIDFatPicksSubtype(t *testing.T) {
	fat := fatMachO(
		slice{macho.CpuArm64, 0, thinMachO(macho.CpuArm64, 0, "aaaaaaaa-0000-0000-0000-000000000000")},
		slice{macho.CpuArm64, 2, thinMachO(macho.CpuArm64, 2, "bbbbbbbb-0000-0000-0000-000000000000")},
	)
	for arch, want := range map[string]string{
		"arm64":  "aaaaaaaa-0000-0000-0000-000000000000",
		"arm64e": "bbbbbbbb-0000-0000-0000-000000000000",
	} {
		got, err := MachOUUID(bytes.NewReader(fat), arch)
		if err != nil || got != want {
			t.Errorf("MachOUUID(%s) = %q, %v; want %q", arch, got, err, want)
		}
	}
	if _, err := MachOUUID(bytes.NewReader(fat), "x86_64"); err == nil {
		t.Error("x86_64 from an arm64-only fat binary should fail")
	}
}

func TestMachOUUIDNoLoadCommand(t *testing.T) {
	le := binary.LittleEndian
	var b bytes.Buffer
	_ = binary.Write(&b, le, []uint32{macho.Magic64, uint32(macho.CpuArm64), 0, uint32(macho.TypeExec), 0, 0, 0, 0})
	if _, err := MachOUUID(bytes.NewReader(b.Bytes()), "arm64"); !errors.Is(err, errNoUUID) {
		t.Errorf("err = %v, want errNoUUID", err)
	}
}

func TestSharedCacheSourceFlag(t *testing.T) {
	imgs := buildImages([]ipsImage{{Name: "Foundation", Path: "/System/Library/Frameworks/Foundation.framework/Foundation", Source: "S", Base: 1}}, ipsCache{})
	if len(imgs) != 1 || imageOnDisk(imgs[0], memOpener(nil)) != OnDiskSharedCache {
		t.Errorf("source S image = %+v, want shared_cache", imgs)
	}
}
