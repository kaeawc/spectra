package fleet

import (
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/toolchain"
)

func TestReconcileInstallsMissingJDK(t *testing.T) {
	from := toolchain.Toolchains{JDKs: []toolchain.JDKInstall{{VersionMajor: 17, ReleaseString: "17.0.10", Vendor: "Zulu"}}}
	to := toolchain.Toolchains{JDKs: []toolchain.JDKInstall{
		{VersionMajor: 17, ReleaseString: "17.0.10", Vendor: "Zulu"},
		{VersionMajor: 21, ReleaseString: "21.0.6", Vendor: "Temurin", Source: "brew"},
	}}
	steps := Reconcile(from, to)
	if len(steps) != 1 {
		t.Fatalf("steps = %d, want 1: %+v", len(steps), steps)
	}
	s := steps[0]
	if s.Category != "jdk" || s.Action != "install" || !strings.Contains(s.Detail, "JDK 21") || !strings.Contains(s.Detail, "Temurin") {
		t.Errorf("step = %+v, want install JDK 21 (Temurin)", s)
	}
}

func TestReconcileBrewInstallAndUpgrade(t *testing.T) {
	from := toolchain.Toolchains{Brew: toolchain.BrewInventory{Formulae: []toolchain.BrewFormula{{Name: "foo", Version: "1.0"}}}}
	to := toolchain.Toolchains{Brew: toolchain.BrewInventory{Formulae: []toolchain.BrewFormula{
		{Name: "foo", Version: "2.0"},
		{Name: "bar", Version: "3.1"},
	}}}
	steps := Reconcile(from, to)
	var install, upgrade bool
	for _, s := range steps {
		if s.Category == "brew" && s.Action == "install" && strings.Contains(s.Detail, "bar") {
			install = true
		}
		if s.Category == "brew" && s.Action == "upgrade" && strings.Contains(s.Detail, "foo") && strings.Contains(s.Detail, "2.0") {
			upgrade = true
		}
	}
	if !install || !upgrade {
		t.Errorf("expected a brew install(bar) and upgrade(foo->2.0); got %+v", steps)
	}
}

func TestReconcileRuntimeAndEnv(t *testing.T) {
	from := toolchain.Toolchains{
		Node: []toolchain.RuntimeInstall{{Version: "18.19.0", Active: true}},
		Env:  toolchain.EnvSnapshot{JavaHome: "/old/jdk17"},
	}
	to := toolchain.Toolchains{
		Node: []toolchain.RuntimeInstall{{Version: "20.11.0", Active: true}},
		Env:  toolchain.EnvSnapshot{JavaHome: "/new/jdk21"},
	}
	steps := Reconcile(from, to)
	var node, java bool
	for _, s := range steps {
		if s.Category == "node" && strings.Contains(s.Detail, "20.11.0") {
			node = true
		}
		if s.Category == "env" && strings.Contains(s.Detail, "JAVA_HOME") && strings.Contains(s.Detail, "/new/jdk21") {
			java = true
		}
	}
	if !node || !java {
		t.Errorf("expected node-version and JAVA_HOME notes; got %+v", steps)
	}
}

func TestReconcileIdenticalIsEmpty(t *testing.T) {
	tc := toolchain.Toolchains{
		JDKs: []toolchain.JDKInstall{{VersionMajor: 21, ReleaseString: "21.0.6"}},
		Brew: toolchain.BrewInventory{Formulae: []toolchain.BrewFormula{{Name: "foo", Version: "1.0"}}},
	}
	if steps := Reconcile(tc, tc); len(steps) != 0 {
		t.Errorf("identical toolchains should yield no steps, got %+v", steps)
	}
}
