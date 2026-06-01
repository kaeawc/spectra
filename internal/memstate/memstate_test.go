package memstate

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestParseVMStat(t *testing.T) {
	const page = 4096
	in := `Mach Virtual Memory Statistics: (page size of 4096 bytes)
Pages free:                               10.
Pages active:                             20.
Pages inactive:                           30.
Pages speculative:                        40.
Pages wired down:                         50.
Pages purgeable:                          60.
File-backed pages:                        70.
Anonymous pages:                          80.
Pages stored in compressor:               90.
Pages occupied by compressor:             45.
Compressions:                             1.234.
Decompressions:                           234.
Swapins:                                  12.
Swapouts:                                 34.
`

	got, err := ParseVMStat(strings.NewReader(in), page, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if got.Free != 10*page || got.Active != 20*page || got.Inactive != 30*page {
		t.Fatalf("basic page fields = %+v", got)
	}
	if got.Speculative != 40*page || got.Wired != 50*page || got.Purgeable != 60*page {
		t.Fatalf("wired/speculative/purgeable = %+v", got)
	}
	if got.FileBacked != 70*page || got.Anonymous != 80*page {
		t.Fatalf("file/anonymous = %+v", got)
	}
	if got.CompressorStored != 90*page || got.CompressorOccupied != 45*page || got.CompressorRatio != 2 {
		t.Fatalf("compressor = %+v", got)
	}
	if got.Compressions != 1234 || got.Decompressions != 234 || got.SwapIns != 12 || got.SwapOuts != 34 {
		t.Fatalf("counters = %+v", got)
	}
}

func TestParseXSWUsage(t *testing.T) {
	var buf bytes.Buffer
	for _, v := range []uint64{100, 40, 60} {
		if err := binary.Write(&buf, binary.LittleEndian, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := binary.Write(&buf, binary.LittleEndian, uint32(1)); err != nil {
		t.Fatal(err)
	}

	got, err := ParseXSWUsage(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalBytes != 100 || got.FreeBytes != 40 || got.UsedBytes != 60 || !got.Encrypted {
		t.Fatalf("ParseXSWUsage = %+v", got)
	}
}

func TestPressureLevel(t *testing.T) {
	cases := []struct {
		raw  uint64
		want PressureLevel
	}{
		{1, PressureNormal},
		{2, PressureWarning},
		{4, PressureCritical},
		{99, PressureUnknown},
	}
	for _, tc := range cases {
		if got := pressureLevel(tc.raw); got != tc.want {
			t.Fatalf("pressureLevel(%d) = %s, want %s", tc.raw, got, tc.want)
		}
	}
}
