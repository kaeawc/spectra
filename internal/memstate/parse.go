package memstate

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

var vmStatLineRE = regexp.MustCompile(`^(.+):\s+([0-9.]+)\.?$`)

// ParseVMStat parses vm_stat(1) output into the fields that overlap
// host_statistics64. It is used by fixture tests and by humans comparing
// Spectra samples with vm_stat output.
func ParseVMStat(r io.Reader, pageSizeBytes, physicalBytes uint64) (MemoryState, error) {
	ms := MemoryState{
		PageSizeBytes: pageSizeBytes,
		PhysicalBytes: physicalBytes,
		PressureLevel: PressureUnknown,
	}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		key, pages, ok := parseVMStatLine(sc.Text())
		if !ok {
			continue
		}
		applyVMStatPages(&ms, key, pages)
	}
	if err := sc.Err(); err != nil {
		return MemoryState{}, fmt.Errorf("parse vm_stat: %w", err)
	}
	return finish(ms), nil
}

// ParseXSWUsage decodes the stable leading fields of Darwin's xsw_usage
// sysctl struct from a little-endian fixture.
func ParseXSWUsage(data []byte) (SwapUsage, error) {
	if len(data) < 28 {
		return SwapUsage{}, fmt.Errorf("xsw_usage fixture too short: %d bytes", len(data))
	}
	return SwapUsage{
		TotalBytes: binary.LittleEndian.Uint64(data[0:8]),
		FreeBytes:  binary.LittleEndian.Uint64(data[8:16]),
		UsedBytes:  binary.LittleEndian.Uint64(data[16:24]),
		Encrypted:  binary.LittleEndian.Uint32(data[24:28]) != 0,
	}, nil
}

func parseVMStatLine(line string) (string, uint64, bool) {
	line = strings.TrimSpace(line)
	m := vmStatLineRE.FindStringSubmatch(line)
	if len(m) != 3 {
		return "", 0, false
	}
	value := strings.ReplaceAll(m[2], ".", "")
	pages, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return "", 0, false
	}
	return strings.ToLower(m[1]), pages, true
}

func applyVMStatPages(ms *MemoryState, key string, pages uint64) {
	bytes := pages * ms.PageSizeBytes
	switch key {
	case "pages free":
		ms.Free = bytes
	case "pages active":
		ms.Active = bytes
	case "pages inactive":
		ms.Inactive = bytes
	case "pages speculative":
		ms.Speculative = bytes
	case "pages wired down":
		ms.Wired = bytes
	case "pages purgeable":
		ms.Purgeable = bytes
	case "file-backed pages":
		ms.FileBacked = bytes
	case "anonymous pages":
		ms.Anonymous = bytes
	case "pages occupied by compressor":
		ms.CompressorOccupied = bytes
	case "pages stored in compressor":
		ms.CompressorStored = bytes
	case "compressions":
		ms.Compressions = pages
	case "decompressions":
		ms.Decompressions = pages
	case "swapins":
		ms.SwapIns = pages
	case "swapouts":
		ms.SwapOuts = pages
	}
}
