//go:build darwin

package snapshot

import (
	"encoding/binary"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

func collectBootTime() (time.Time, error) {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(tv.Sec, int64(tv.Usec)*1000), nil
}

func collectLoadAverages(at time.Time) (LoadAverages, error) {
	raw, err := unix.SysctlRaw("vm.loadavg")
	if err != nil {
		return LoadAverages{}, err
	}
	if len(raw) < 24 {
		return LoadAverages{}, fmt.Errorf("vm.loadavg: short sysctl payload")
	}
	fscale := binary.LittleEndian.Uint64(raw[16:24])
	if fscale == 0 {
		return LoadAverages{}, fmt.Errorf("vm.loadavg: zero scale")
	}
	return LoadAverages{
		OneMinute:     float64(binary.LittleEndian.Uint32(raw[0:4])) / float64(fscale),
		FiveMinute:    float64(binary.LittleEndian.Uint32(raw[4:8])) / float64(fscale),
		FifteenMinute: float64(binary.LittleEndian.Uint32(raw[8:12])) / float64(fscale),
		At:            at,
	}, nil
}
