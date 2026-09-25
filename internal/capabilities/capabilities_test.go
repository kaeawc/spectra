package capabilities

import (
	"encoding/json"
	"reflect"
	"testing"
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
	if len(shape) != 5 {
		t.Fatalf("manifest keys = %v", shape)
	}
	interfaces := shape["interfaces"].([]any)
	if len(interfaces) != 4 {
		t.Fatalf("interfaces = %v", interfaces)
	}
	version := interfaces[0].(map[string]any)
	if _, ok := version["result_schema"]; ok {
		t.Fatal("text version interface has a result schema")
	}
	if !reflect.DeepEqual(got.Interfaces[1].Argv, []string{"--json", "<app_path>..."}) {
		t.Fatalf("inspect argv = %v", got.Interfaces[1].Argv)
	}
}
