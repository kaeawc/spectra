package powerreport

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/sysinfo"
)

func TestFillStateReportsMissingBattery(t *testing.T) {
	no := false
	var r Report
	FillState(&r, sysinfo.PowerState{HasBattery: &no})
	if r.Battery == nil || r.Battery.Present == nil || *r.Battery.Present {
		t.Fatalf("battery = %+v, want present false", r.Battery)
	}
	b, _ := json.Marshal(r.Battery)
	if !strings.Contains(string(b), `"present":false`) {
		t.Errorf("JSON = %s", b)
	}

	var unknown Report
	FillState(&unknown, sysinfo.PowerState{})
	if unknown.Battery != nil {
		t.Errorf("unknown battery = %+v, want omitted", unknown.Battery)
	}
}
