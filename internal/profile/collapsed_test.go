package profile

import (
	"strings"
	"testing"
)

func TestParseCollapsedStacksAndHotMethods(t *testing.T) {
	input := strings.NewReader(`
main;svc.handle;db.query 7
main;svc.handle;json.encode 3
main;idle 2
`)
	samples, err := ParseCollapsedStacks(input)
	if err != nil {
		t.Fatalf("ParseCollapsedStacks: %v", err)
	}
	got := HotMethods(samples)
	if len(got) < 2 {
		t.Fatalf("hot methods too short: %#v", got)
	}
	if got[0].Name != "main" || got[0].Inclusive != 12 || got[0].Exclusive != 0 {
		t.Fatalf("top method = %#v", got[0])
	}
	if got[1].Name != "svc.handle" || got[1].Inclusive != 10 {
		t.Fatalf("second method = %#v", got[1])
	}
}

func TestBuildCallTree(t *testing.T) {
	samples := []StackSample{
		{Frames: []string{"main", "svc", "db"}, Value: 7},
		{Frames: []string{"main", "svc", "json"}, Value: 3},
	}
	tree := BuildCallTree(samples)
	if tree.Value != 10 || len(tree.Children) != 1 {
		t.Fatalf("root = %#v", tree)
	}
	main := tree.Children[0]
	if main.Name != "main" || main.Value != 10 || len(main.Children) != 1 {
		t.Fatalf("main = %#v", main)
	}
	svc := main.Children[0]
	if svc.Name != "svc" || svc.Value != 10 || len(svc.Children) != 2 {
		t.Fatalf("svc = %#v", svc)
	}
}

func TestParseCollapsedStacksRejectsBadValue(t *testing.T) {
	_, err := ParseCollapsedStacks(strings.NewReader("main;work nope\n"))
	if err == nil {
		t.Fatal("expected bad value error")
	}
}
