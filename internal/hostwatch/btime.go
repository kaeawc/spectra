package hostwatch

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kaeawc/spectra/internal/logger"
)

type bootTimer struct {
	mu     sync.Mutex
	read   func() ([]byte, error)
	log    logger.Logger
	btime  time.Time
	warned bool
}

func (t *bootTimer) get() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.btime.IsZero() {
		return t.btime
	}
	data, err := t.read()
	if err == nil {
		t.btime = parseBtime(data)
		if t.btime.IsZero() {
			err = errors.New("no btime line in /proc/stat")
		}
	}
	if err != nil && !t.warned {
		t.warned = true
		t.log.Warn("spawn probe cannot read boot time; process start times are unavailable until it can", "error", err)
	}
	return t.btime
}

func parseBtime(data []byte) time.Time {
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "btime "); ok {
			if secs, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64); err == nil && secs > 0 {
				return time.Unix(secs, 0)
			}
			return time.Time{}
		}
	}
	return time.Time{}
}
