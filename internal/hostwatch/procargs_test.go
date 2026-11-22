package hostwatch

import (
	"encoding/binary"
	"strings"
	"testing"
)

func procArgs2Fixture(argc int, exe string, pad int, args, env []string) []byte {
	b := binary.NativeEndian.AppendUint32(nil, uint32(argc))
	b = append(b, exe...)
	b = append(b, make([]byte, 1+pad)...)
	for _, a := range append(append([]string{}, args...), env...) {
		b = append(b, a...)
		b = append(b, 0)
	}
	return b
}

func TestJoinProcArgs2(t *testing.T) {
	cases := []struct {
		name    string
		raw     []byte
		want    string
		wantErr bool
	}{
		{"basic", procArgs2Fixture(3, "/bin/zsh", 3, []string{"zsh", "-c", "ls"}, []string{"A=1"}), "zsh -c ls", false},
		{"argc zero", procArgs2Fixture(0, "/bin/x", 0, nil, nil), "", false},
		{"short", []byte{1, 0}, "", true},
		{"no exe terminator", append(binary.NativeEndian.AppendUint32(nil, 1), "abc"...), "", true},
		{"argc beyond data", procArgs2Fixture(5, "/bin/x", 0, []string{"a", "b"}, nil), "a b", false},
		{"empty arg", procArgs2Fixture(3, "/bin/x", 0, []string{"a", "", "b"}, nil), "a  b", false},
	}
	for _, c := range cases {
		got, err := joinProcArgs2(c.raw)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("%s: got %q err=%v want %q wantErr=%v", c.name, got, err, c.want, c.wantErr)
		}
	}
}

func TestJoinProcArgs2ReusedBuffer(t *testing.T) {
	buf := make([]byte, 4096)
	n := copy(buf, procArgs2Fixture(2, "/usr/bin/first", 2, []string{"first", "--alpha"}, []string{"K=V"}))
	first, err := joinProcArgs2(buf[:n])
	if err != nil {
		t.Fatal(err)
	}
	clear(buf)
	n = copy(buf, procArgs2Fixture(2, "/usr/bin/second", 0, []string{"second", "--beta"}, nil))
	second, err := joinProcArgs2(buf[:n])
	if err != nil {
		t.Fatal(err)
	}
	if first != "first --alpha" || second != "second --beta" {
		t.Fatalf("first=%q second=%q", first, second)
	}
}

func BenchmarkJoinProcArgs2(b *testing.B) {
	env := make([]string, 200)
	for i := range env {
		env[i] = "VAR=" + strings.Repeat("x", 100)
	}
	raw := procArgs2Fixture(4, "/usr/bin/java", 4, []string{"java", "-Xmx1g", "-jar", "app.jar"}, env)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := joinProcArgs2(raw); err != nil {
			b.Fatal(err)
		}
	}
}
