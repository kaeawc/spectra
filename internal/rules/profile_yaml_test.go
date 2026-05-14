package rules

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kaeawc/spectra/internal/jvm"
)

func writeYAML(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestLoadProfilesFromYAML_TopLevelList(t *testing.T) {
	path := writeYAML(t, "profiles.yml", `
- id: my-server
  name: Acme App Server
  main_class_contains: com.acme.server.Main
  tags: [tight_heap_expected]
- id: ephemeral-tool
  java_home_contains: /tmp/ephemeral
  tags: [build_tool_daemon]
`)
	got, err := LoadProfilesFromYAML([]string{path})
	if err != nil {
		t.Fatalf("LoadProfilesFromYAML: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(got))
	}
	if got[0].ID != "my-server" || got[0].MainClassContains != "com.acme.server.Main" {
		t.Errorf("first profile: %+v", got[0])
	}
	if !HasTag(&got[0], TagTightHeapExpected) {
		t.Errorf("first profile should have tight_heap_expected")
	}
}

func TestLoadProfilesFromYAML_MappingWithProfilesKey(t *testing.T) {
	path := writeYAML(t, "profiles.yml", `
profiles:
  - id: only-one
    java_home_contains: /opt/only
    tags: [ide]
`)
	got, err := LoadProfilesFromYAML([]string{path})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 1 || got[0].ID != "only-one" {
		t.Errorf("got %v", got)
	}
}

func TestLoadProfilesFromYAML_RejectsNoMatchers(t *testing.T) {
	path := writeYAML(t, "profiles.yml", `
- id: phantom
  name: Phantom
  tags: [danger]
`)
	if _, err := LoadProfilesFromYAML([]string{path}); err == nil {
		t.Error("expected error for profile with no matchers")
	}
}

func TestLoadProfilesFromYAML_RejectsMissingID(t *testing.T) {
	path := writeYAML(t, "profiles.yml", `
- main_class_contains: x
  tags: [a]
`)
	if _, err := LoadProfilesFromYAML([]string{path}); err == nil {
		t.Error("expected error for profile missing id")
	}
}

func TestLoadProfilesFromYAML_RejectsUnknownField(t *testing.T) {
	// strict YAML: unknown fields error out
	path := writeYAML(t, "profiles.yml", `
- id: x
  main_class_contains: y
  bogus_field: 1
`)
	if _, err := LoadProfilesFromYAML([]string{path}); err == nil {
		t.Error("expected strict YAML error for unknown field")
	}
}

func TestMergeProfiles_UserOverridesBuiltinByID(t *testing.T) {
	user := []AppProfile{{
		ID:               "jetbrains-toolbox",
		Name:             "MY Toolbox Override",
		JavaHomeContains: "different-path",
		Tags:             []string{"my_tag"},
	}}
	merged := MergeProfiles(BuiltinProfiles(), user)
	for _, p := range merged {
		if p.ID == "jetbrains-toolbox" {
			if p.Name != "MY Toolbox Override" || !HasTag(&p, "my_tag") {
				t.Errorf("user override didn't replace builtin: %+v", p)
			}
			return
		}
	}
	t.Error("jetbrains-toolbox not present after merge")
}

func TestMergeProfiles_AppendsNew(t *testing.T) {
	user := []AppProfile{{
		ID:                "custom-server",
		MainClassContains: "com.example",
		Tags:              []string{"x"},
	}}
	merged := MergeProfiles(BuiltinProfiles(), user)
	if len(merged) != len(BuiltinProfiles())+1 {
		t.Fatalf("expected %d profiles, got %d", len(BuiltinProfiles())+1, len(merged))
	}
	if merged[len(merged)-1].ID != "custom-server" {
		t.Errorf("custom profile should be appended last")
	}
}

func TestResolveProfileCatalog_NoPaths(t *testing.T) {
	got, err := ResolveProfileCatalog(nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != len(BuiltinProfiles()) {
		t.Errorf("nil paths should yield builtins only")
	}
}

func TestResolveProfileCatalog_EndToEndMatching(t *testing.T) {
	path := writeYAML(t, "profiles.yml", `
- id: acme-runtime
  main_class_contains: com.acme.Runtime
  tags: [tight_heap_expected]
`)
	catalog, err := ResolveProfileCatalog([]string{path})
	if err != nil {
		t.Fatalf("ResolveProfileCatalog: %v", err)
	}
	j := jvm.Info{MainClass: "com.acme.Runtime$App"}
	got := MatchProfile(j, catalog)
	if got == nil || got.ID != "acme-runtime" {
		t.Fatalf("expected acme-runtime match, got %v", got)
	}
	if !HasTag(got, TagTightHeapExpected) {
		t.Error("acme-runtime should carry tight_heap_expected from YAML")
	}
}
