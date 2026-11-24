package hostload

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
)

func fakeFiles(files map[string]string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		return nil, errors.New("missing " + p)
	}
}

const scope = "/sys/fs/cgroup/agent.slice/work.scope"

func TestHostCgroup(t *testing.T) {
	tests := []struct {
		name  string
		os    hostos.Kind
		files map[string]string
		want  string // JSON of the result; "null" when nil
	}{
		{
			name: "v2 limit on ancestor, quota on scope, inactive cache excluded",
			os:   hostos.Linux,
			files: map[string]string{
				"/proc/self/cgroup":                         "0::/agent.slice/work.scope\n",
				scope + "/cgroup.controllers":               "cpu memory\n",
				scope + "/cpu.max":                          "150000 100000\n",
				scope + "/memory.max":                       "max\n",
				scope + "/cpu.pressure":                     "some avg10=12.50 avg60=1.00 avg300=0.50 total=9\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
				scope + "/memory.pressure":                  "some avg10=0.25 avg60=0.00 avg300=0.00 total=1\n",
				"/sys/fs/cgroup/agent.slice/memory.max":     "8589934592\n",
				"/sys/fs/cgroup/agent.slice/memory.high":    "max\n",
				"/sys/fs/cgroup/agent.slice/memory.current": "6442450944\n",
				"/sys/fs/cgroup/agent.slice/memory.stat":    "anon 1\ninactive_file 2147483648\n",
				"/sys/fs/cgroup/agent.slice/cpu.max":        "max 100000\n",
			},
			want: `{"version":2,"path":"/agent.slice/work.scope","cpu_quota_cpus":1.5,"memory_limit_bytes":8589934592,"memory_used_bytes":4294967296,"memory_available_bytes":4294967296,"cpu_pressure":12.5,"memory_pressure":0.25}`,
		},
		{
			name: "v2 memory.high tighter than memory.max",
			os:   hostos.Linux,
			files: map[string]string{
				"/proc/self/cgroup":           "0::/agent.slice/work.scope\n",
				scope + "/cgroup.controllers": "memory\n",
				scope + "/memory.max":         "4096\n",
				scope + "/memory.high":        "3072\n",
				scope + "/memory.current":     "4000\n",
			},
			want: `{"version":2,"path":"/agent.slice/work.scope","memory_limit_bytes":3072,"memory_used_bytes":4000,"memory_available_bytes":0}`,
		},
		{
			name: "v2 everything max",
			os:   hostos.Linux,
			files: map[string]string{
				"/proc/self/cgroup":           "0::/agent.slice/work.scope\n",
				scope + "/cgroup.controllers": "cpu memory\n",
				scope + "/cpu.max":            "max 100000\n",
				scope + "/memory.max":         "max\n",
				scope + "/memory.high":        "max\n",
				scope + "/memory.current":     "1000\n",
				scope + "/cpu.pressure":       "some avg10=1.00 avg60=1.00 avg300=1.00 total=1\n",
			},
			want: "null",
		},
		{
			name: "v2 namespaced mount hides host path",
			os:   hostos.Linux,
			files: map[string]string{
				"/proc/self/cgroup":                 "0::/docker/abc\n",
				"/sys/fs/cgroup/cgroup.controllers": "cpu memory\n",
				"/sys/fs/cgroup/cpu.max":            "200000 100000\n",
				"/sys/fs/cgroup/memory.max":         "2048\n",
				"/sys/fs/cgroup/memory.current":     "1024\n",
				"/sys/fs/cgroup/docker/abc/cpu.max": "1 100000\n",
				"/sys/fs/cgroup/docker/memory.max":  "1\n",
			},
			want: `{"version":2,"path":"/docker/abc","cpu_quota_cpus":2,"memory_limit_bytes":2048,"memory_used_bytes":1024,"memory_available_bytes":1024}`,
		},
		{
			name: "v1 cpu and memory",
			os:   hostos.Linux,
			files: map[string]string{
				"/proc/self/cgroup": "12:memory:/ci/job\n4:cpu,cpuacct:/ci/job\n1:name=systemd:/ci/job\n",
				"/sys/fs/cgroup/cpu,cpuacct/ci/job/cpu.cfs_quota_us":  "200000\n",
				"/sys/fs/cgroup/cpu,cpuacct/ci/job/cpu.cfs_period_us": "100000\n",
				"/sys/fs/cgroup/memory/ci/job/memory.limit_in_bytes":  "4096\n",
				"/sys/fs/cgroup/memory/ci/job/memory.usage_in_bytes":  "3000\n",
				"/sys/fs/cgroup/memory/ci/job/memory.stat":            "cache 5\ntotal_inactive_file 1000\n",
			},
			want: `{"version":1,"path":"/ci/job","cpu_quota_cpus":2,"memory_limit_bytes":4096,"memory_used_bytes":2000,"memory_available_bytes":2096}`,
		},
		{
			name: "v1 unlimited in a namespaced container",
			os:   hostos.Linux,
			files: map[string]string{
				"/proc/self/cgroup":                            "12:memory:/docker/abc\n4:cpu,cpuacct:/docker/abc\n",
				"/sys/fs/cgroup/cpu,cpuacct/cpu.cfs_quota_us":  "-1\n",
				"/sys/fs/cgroup/cpu,cpuacct/cpu.cfs_period_us": "100000\n",
				"/sys/fs/cgroup/memory/memory.limit_in_bytes":  "9223372036854771712\n",
			},
			want: "null",
		},
		{
			name: "v1 namespaced container with a memory limit",
			os:   hostos.Linux,
			files: map[string]string{
				"/proc/self/cgroup":                           "12:memory:/docker/abc\n",
				"/sys/fs/cgroup/memory/memory.limit_in_bytes": "4096\n",
			},
			want: `{"version":1,"path":"/docker/abc","memory_limit_bytes":4096}`,
		},
		{
			name:  "missing /proc/self/cgroup",
			os:    hostos.Linux,
			files: map[string]string{},
			want:  "null",
		},
		{
			name: "v2 path without cgroup files",
			os:   hostos.Linux,
			files: map[string]string{
				"/proc/self/cgroup": "0::/agent.slice/work.scope\n",
			},
			want: "null",
		},
		{
			name: "not linux",
			os:   hostos.Darwin,
			files: map[string]string{
				"/proc/self/cgroup":           "0::/agent.slice/work.scope\n",
				scope + "/cgroup.controllers": "cpu\n",
				scope + "/cpu.max":            "100000 100000\n",
			},
			want: "null",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Host{OS: tc.os, ReadFile: fakeFiles(tc.files)}.Cgroup()
			b, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != tc.want {
				t.Fatalf("got  %s\nwant %s", b, tc.want)
			}
		})
	}
}

func TestEffectiveSlice(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	u := func(v uint64) *uint64 { return &v }
	tests := []struct {
		name      string
		cg        *Cgroup
		r         Reading
		wantCPUs  *float64
		wantAvail *uint64
	}{
		{"no cgroup", nil, Reading{CPUs: 8, AvailBytes: 100, MemKnown: true}, nil, nil},
		{"quota below host", &Cgroup{CPUQuotaCPUs: f(2.5)}, Reading{CPUs: 8}, f(2.5), nil},
		{"quota above host", &Cgroup{CPUQuotaCPUs: f(16)}, Reading{CPUs: 8}, f(8), nil},
		{"cgroup memory tighter", &Cgroup{MemoryLimitBytes: u(50), MemoryAvailableBytes: u(40)}, Reading{CPUs: 8, AvailBytes: 100, MemKnown: true}, nil, u(40)},
		{"host memory tighter", &Cgroup{MemoryLimitBytes: u(500), MemoryAvailableBytes: u(400)}, Reading{CPUs: 8, AvailBytes: 100, MemKnown: true}, nil, u(100)},
		{"host memory unknown", &Cgroup{MemoryLimitBytes: u(50), MemoryAvailableBytes: u(40)}, Reading{CPUs: 8}, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cpus, avail := effectiveSlice(tc.cg, tc.r)
			if (cpus == nil) != (tc.wantCPUs == nil) || (cpus != nil && *cpus != *tc.wantCPUs) {
				t.Errorf("cpus = %v, want %v", deref(cpus), deref(tc.wantCPUs))
			}
			if (avail == nil) != (tc.wantAvail == nil) || (avail != nil && *avail != *tc.wantAvail) {
				t.Errorf("avail = %v, want %v", deref(avail), deref(tc.wantAvail))
			}
		})
	}
}

func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}
