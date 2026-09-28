package hostwatch

import (
	"strings"

	"github.com/kaeawc/spectra/internal/memstate"
)

func collectDarwinMemory() (string, float64, float64, error) {
	ms, err := memstate.Collect()
	if err != nil {
		return "unknown", 0, 0, err
	}
	p := strings.ToLower(string(ms.PressureLevel))
	if p == "warning" {
		p = "warn"
	}
	return p, float64(ms.Swap.UsedBytes) / 1e6, ms.PressureFreePercent, nil
}
