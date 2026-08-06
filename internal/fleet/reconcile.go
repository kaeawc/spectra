package fleet

import (
	"fmt"
	"sort"

	"github.com/kaeawc/spectra/internal/toolchain"
)

// ReconcileStep is one advisory step in a reconciliation plan. Every step is
// descriptive text, never a runnable command — the plan is print-only.
type ReconcileStep struct {
	Category string `json:"category"` // jdk, brew, node, python, go, ruby, env
	Action   string `json:"action"`   // install, upgrade, note
	Detail   string `json:"detail"`
}

// Reconcile produces an ordered, advisory set of steps to make the `from`
// toolchain match `to`. It names target vendors/releases rather than asserting
// exact install commands, because the output is print-only and users may paste
// it into a shell — nothing here is meant to be executed as-is.
func Reconcile(from, to toolchain.Toolchains) []ReconcileStep {
	var steps []ReconcileStep
	steps = append(steps, reconcileJDKs(from, to)...)
	steps = append(steps, reconcileBrew(from, to)...)
	steps = append(steps, reconcileRuntime("node", from.Node, to.Node)...)
	steps = append(steps, reconcileRuntime("python", from.Python, to.Python)...)
	steps = append(steps, reconcileRuntime("go", from.Go, to.Go)...)
	steps = append(steps, reconcileRuntime("ruby", from.Ruby, to.Ruby)...)
	steps = append(steps, reconcileEnv(from, to)...)
	return steps
}

func reconcileJDKs(from, to toolchain.Toolchains) []ReconcileStep {
	fromMajors := jdkMajorSet(from.JDKs)
	toByMajor := jdkByMajor(to.JDKs)
	var steps []ReconcileStep
	for _, m := range sortedInts(toByMajor) {
		if !fromMajors[m] {
			j := toByMajor[m]
			steps = append(steps, ReconcileStep{"jdk", "install",
				fmt.Sprintf("install a JDK %d to match target (target has %s, vendor %s, via %s)",
					m, jdkRelease(j), orNA(j.Vendor), orNA(j.Source))})
		}
	}
	toMajors := jdkMajorSet(to.JDKs)
	for _, m := range sortedInts(jdkByMajor(from.JDKs)) {
		if !toMajors[m] {
			steps = append(steps, ReconcileStep{"jdk", "note",
				fmt.Sprintf("you have JDK %d that the target does not", m)})
		}
	}
	return steps
}

func reconcileBrew(from, to toolchain.Toolchains) []ReconcileStep {
	fromF := brewByName(from.Brew.Formulae)
	tf := append([]toolchain.BrewFormula(nil), to.Brew.Formulae...)
	sort.Slice(tf, func(i, j int) bool { return tf[i].Name < tf[j].Name })
	var steps []ReconcileStep
	for _, f := range tf {
		cur, ok := fromF[f.Name]
		switch {
		case !ok:
			steps = append(steps, ReconcileStep{"brew", "install",
				fmt.Sprintf("install Homebrew formula %s (target %s)", f.Name, orNA(f.Version))})
		case f.Version != "" && cur.Version != f.Version:
			steps = append(steps, ReconcileStep{"brew", "upgrade",
				fmt.Sprintf("align Homebrew formula %s: target %s, you have %s", f.Name, f.Version, orNA(cur.Version))})
		}
	}
	return steps
}

func reconcileRuntime(lang string, from, to []toolchain.RuntimeInstall) []ReconcileStep {
	tv := activeRuntimeVersion(to)
	fv := activeRuntimeVersion(from)
	if tv == "" || tv == fv {
		return nil
	}
	return []ReconcileStep{{lang, "note",
		fmt.Sprintf("target uses %s %s; you have %s", lang, tv, orNA(fv))}}
}

func reconcileEnv(from, to toolchain.Toolchains) []ReconcileStep {
	var steps []ReconcileStep
	if to.Env.JavaHome != "" && from.Env.JavaHome != to.Env.JavaHome {
		steps = append(steps, ReconcileStep{"env", "note",
			fmt.Sprintf("target JAVA_HOME points at %s; yours is %s", to.Env.JavaHome, orNA(from.Env.JavaHome))})
	}
	if to.ActiveJVMManager != "" && from.ActiveJVMManager != to.ActiveJVMManager {
		steps = append(steps, ReconcileStep{"env", "note",
			fmt.Sprintf("target's active JVM manager is %s; yours is %s", to.ActiveJVMManager, orNA(from.ActiveJVMManager))})
	}
	return steps
}

// --- helpers ---

func jdkMajorSet(jdks []toolchain.JDKInstall) map[int]bool {
	m := map[int]bool{}
	for _, j := range jdks {
		m[j.VersionMajor] = true
	}
	return m
}

func jdkByMajor(jdks []toolchain.JDKInstall) map[int]toolchain.JDKInstall {
	m := map[int]toolchain.JDKInstall{}
	for _, j := range jdks {
		if _, ok := m[j.VersionMajor]; !ok {
			m[j.VersionMajor] = j
		}
	}
	return m
}

func sortedInts(m map[int]toolchain.JDKInstall) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func brewByName(fs []toolchain.BrewFormula) map[string]toolchain.BrewFormula {
	m := map[string]toolchain.BrewFormula{}
	for _, f := range fs {
		m[f.Name] = f
	}
	return m
}

func activeRuntimeVersion(rs []toolchain.RuntimeInstall) string {
	for _, r := range rs {
		if r.Active {
			return r.Version
		}
	}
	if len(rs) > 0 {
		return rs[0].Version
	}
	return ""
}

func jdkRelease(j toolchain.JDKInstall) string {
	if j.ReleaseString != "" {
		return j.ReleaseString
	}
	return fmt.Sprintf("%d.%d.%d", j.VersionMajor, j.VersionMinor, j.VersionPatch)
}

func orNA(s string) string {
	if s == "" {
		return "n/a"
	}
	return s
}
