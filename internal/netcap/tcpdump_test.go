package netcap

import (
	"reflect"
	"testing"
	"time"
)

func TestBuildTCPDumpArgsDefaults(t *testing.T) {
	args, err := BuildTCPDumpArgs(Options{
		Interface: "en0",
		Output:    "/tmp/spectra.pcap",
	})
	if err != nil {
		t.Fatalf("BuildTCPDumpArgs: %v", err)
	}
	want := []string{"-i", "en0", "-n", "-s", "262144", "-w", "/tmp/spectra.pcap"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
}

func TestBuildTCPDumpArgsWithFilter(t *testing.T) {
	args, err := BuildTCPDumpArgs(Options{
		Interface: "utun3",
		Output:    "/tmp/spectra.pcap",
		Duration:  5 * time.Second,
		SnapLen:   4096,
		Proto:     "TCP",
		Host:      "api.example.com",
		Port:      443,
	})
	if err != nil {
		t.Fatalf("BuildTCPDumpArgs: %v", err)
	}
	want := []string{"-i", "utun3", "-n", "-s", "4096", "-w", "/tmp/spectra.pcap", "tcp", "and", "host", "api.example.com", "and", "port", "443"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
}

func TestBuildTCPDumpArgsRejectsInvalidOptions(t *testing.T) {
	tests := []Options{
		{Output: "/tmp/out.pcap"},
		{Interface: "en0;rm", Output: "/tmp/out.pcap"},
		{Interface: "en0"},
		{Interface: "en0", Output: "/tmp/out.pcap", Duration: MaxDuration + time.Millisecond},
		{Interface: "en0", Output: "/tmp/out.pcap", SnapLen: 64},
		{Interface: "en0", Output: "/tmp/out.pcap", Proto: "icmp"},
		{Interface: "en0", Output: "/tmp/out.pcap", Host: "example.com or port 22"},
		{Interface: "en0", Output: "/tmp/out.pcap", Port: 70000},
	}
	for _, tt := range tests {
		if _, err := BuildTCPDumpArgs(tt); err == nil {
			t.Fatalf("BuildTCPDumpArgs(%+v) succeeded, want error", tt)
		}
	}
}
