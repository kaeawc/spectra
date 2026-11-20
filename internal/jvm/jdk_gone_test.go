package jvm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testGradleMain = "org.gradle.launcher.daemon.bootstrap.GradleDaemon"
	testKotlinMain = "org.jetbrains.kotlin.daemon.KotlinCompileDaemon"
)

func TestDetectJDKGoneAndExplain(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "removed-jdk")
	healthy := t.TempDir()
	if err := os.MkdirAll(filepath.Join(healthy, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(healthy, "bin", "java"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	noLink := func(string) (string, error) { return "", fmt.Errorf("none") }
	deleted := func(string) (string, error) { return "/x/bin/java (deleted)", nil }
	cases := []struct {
		name, home, main string
		link             func(string) (string, error)
		wantObs          bool
	}{
		{"gradle missing home", missing, testGradleMain, noLink, true},
		{"kotlin missing home", missing, testKotlinMain, noLink, true},
		{"gradle exe deleted", healthy, testGradleMain, deleted, true},
		{"gradle healthy", healthy, testGradleMain, noLink, false},
		{"other jvm missing home", missing, "svc.App", noLink, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info := Info{PID: 7, MainClass: c.main, JavaHome: c.home}
			detectJDKGone(&info, CollectOptions{ReadLink: c.link})
			e := &Explanation{}
			explainJDKGone(e, &info)
			found := false
			for _, o := range e.Observations {
				if o.ID != "build-daemon-jdk-gone" {
					continue
				}
				found = true
				if !strings.Contains(o.Recommendation, "kill 7") || !strings.Contains(o.Recommendation, "Do not run `gradle --stop`") {
					t.Errorf("recommendation = %q", o.Recommendation)
				}
			}
			if found != c.wantObs {
				t.Fatalf("observation present = %v, want %v (%+v)", found, c.wantObs, info)
			}
		})
	}
}

func TestCollectExplanationFlagsMissingJavaHome(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "removed-jdk")
	run := func(name string, args ...string) ([]byte, error) {
		if name == "jcmd" && len(args) >= 2 {
			switch args[1] {
			case "VM.version":
				return []byte("OpenJDK 64-Bit Server VM version 21"), nil
			case "VM.system_properties":
				return []byte("java.home=" + missing + "\njava.version=21.0.1\n"), nil
			}
		}
		return nil, fmt.Errorf("unavailable")
	}
	info := collectOne(context.Background(), 7, testGradleMain, run, CollectOptions{})
	if !info.JavaHomeMissing {
		t.Fatalf("collectOne should flag the missing java.home: %+v", info)
	}
	e := &Explanation{}
	explainJDKGone(e, &info)
	if len(e.Observations) != 1 {
		t.Fatalf("observations = %+v", e.Observations)
	}
}
