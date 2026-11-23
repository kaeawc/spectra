package capabilities

import (
	"encoding/json"
	"reflect"
	"testing"

	protocol "github.com/kaeawc/spectra-protocol/protocol/v1"
)

func TestBuild(t *testing.T) {
	got := Build("test-version", "darwin", "arm64")
	if got.SpectraVersion != "test-version" || got.OS != "darwin" || got.Arch != "arm64" {
		t.Fatalf("build identity = %+v", got)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var shape map[string]any
	if err := json.Unmarshal(data, &shape); err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.DecodeSpectraCapabilities(data); err != nil {
		t.Fatalf("protocol rejected built capabilities: %v", err)
	}
	if len(shape) != 5 {
		t.Fatalf("manifest keys = %v", shape)
	}
	interfaces := shape["interfaces"].([]any)
	if want := 4 + len(commandEntries); len(interfaces) != want {
		t.Fatalf("interfaces = %d, want %d: %v", len(interfaces), want, interfaces)
	}
	version := interfaces[0].(map[string]any)
	if _, ok := version["result_schema"]; ok {
		t.Fatal("text version interface has a result schema")
	}
	if !reflect.DeepEqual(got.Interfaces[1].Argv, []string{"--json", "<app_path>..."}) {
		t.Fatalf("inspect argv = %v", got.Interfaces[1].Argv)
	}
	if last := got.Interfaces[len(got.Interfaces)-1]; last.Name != protocol.InterfaceCapabilities {
		t.Fatalf("last interface = %q, want capabilities", last.Name)
	}
}

func TestBuildDeclaresWaaaghCommands(t *testing.T) {
	want := map[string]protocol.SchemaRef{
		"load":            {Name: "spectra.load", Version: 1},
		"orphans":         {Name: "spectra.orphans", Version: 1},
		"owner":           {Name: "spectra.owner", Version: 1},
		"owner.under":     {Name: "spectra.owner.under", Version: 1},
		"lineage":         {Name: "spectra.lineage", Version: 1},
		"spawns":          {Name: "spectra.spawns", Version: 1},
		"idle":            {Name: "spectra.idle", Version: 1},
		"devices":         {Name: "spectra.devices", Version: 1},
		"startup":         {Name: "spectra.startup", Version: 1},
		"startup.disable": {Name: "spectra.startup.change", Version: 1},
		"startup.enable":  {Name: "spectra.startup.change", Version: 1},
		"uninstall.plan":  {Name: "spectra.uninstall.plan", Version: 1},
		"uninstall.apply": {Name: "spectra.uninstall.apply", Version: 1},
	}
	m := Build("v", "darwin", "arm64")
	for name, schema := range want {
		iface, ok := m.Interface(name)
		if !ok {
			t.Errorf("missing interface %q", name)
			continue
		}
		if iface.Output != protocol.OutputJSON || iface.ResultSchema == nil || *iface.ResultSchema != schema {
			t.Errorf("%s = %+v, want json %v", name, iface, schema)
		}
		if len(iface.Argv) == 0 || iface.Argv[len(iface.Argv)-1] == "" {
			t.Errorf("%s argv = %v", name, iface.Argv)
		}
	}
	owner, _ := m.Interface("owner.under")
	if !reflect.DeepEqual(owner.Argv[:3], []string{"owner", "--under", "--json"}) {
		t.Fatalf("owner.under argv = %v", owner.Argv)
	}
}

func TestBuildOmitsMacOSOnlyCommandsOffMacOS(t *testing.T) {
	m := Build("test-version", "linux", "amd64")
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.DecodeSpectraCapabilities(data); err != nil {
		t.Fatalf("protocol rejected linux capabilities: %v", err)
	}
	for _, name := range []string{"inspect", "startup", "startup.disable", "startup.enable", "uninstall.plan", "uninstall.apply"} {
		if _, ok := m.Interface(name); ok {
			t.Errorf("linux manifest advertises %s", name)
		}
	}
	for _, name := range []string{"load", "orphans", "owner", "owner.under", "lineage", "spawns", "idle", "devices"} {
		if _, ok := m.Interface(name); !ok {
			t.Errorf("linux manifest omits %s", name)
		}
	}
}

func TestResultSchemaVersion(t *testing.T) {
	for _, name := range []string{protocol.SchemaInspect, protocol.SchemaSnapshot, protocol.SchemaCapabilities, SchemaLoad, SchemaUninstallApply} {
		if v, ok := ResultSchemaVersion(name); !ok || v < 1 {
			t.Errorf("ResultSchemaVersion(%q) = %d, %v", name, v, ok)
		}
	}
	if _, ok := ResultSchemaVersion("spectra.nope"); ok {
		t.Fatal("unknown schema reported as supported")
	}
	for name := range LocalSchemas() {
		if _, ok := protocol.SupportedResultSchemaVersion(name); ok {
			t.Errorf("%s shadows a protocol-owned schema", name)
		}
	}
}
