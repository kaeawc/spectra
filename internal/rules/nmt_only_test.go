package rules

import "testing"

func TestNMTGrowthFiresFromNMTOnlySamples(t *testing.T) {
	s := nmtSnap(nmtArgs, longAgo, map[string][]int64{"Internal": steady(100, 20, 5)})
	for i := range s.JVMHistory {
		s.JVMHistory[i].NMTOnly = true
	}
	if f := ruleJVMNativeMemoryGrowth().MatchFn(s); len(f) != 1 {
		t.Fatalf("findings = %v, want 1", f)
	}
}
