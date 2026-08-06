package sysinfo

import (
	"errors"
	"testing"
)

func stubFDLimit(out string, err error) CmdRunner {
	return func(name string, args ...string) ([]byte, error) {
		if name != "launchctl" || len(args) != 2 || args[0] != "limit" || args[1] != "maxfiles" {
			return nil, errors.New("unexpected command")
		}
		if err != nil {
			return nil, err
		}
		return []byte(out), nil
	}
}

func TestCollectFDLimit(t *testing.T) {
	tests := []struct {
		name string
		out  string
		err  error
		want FDLimit
	}{
		{
			name: "soft numeric, hard unlimited",
			out:  "\tmaxfiles    256            unlimited\n",
			want: FDLimit{Soft: 256, HardUnlimited: true},
		},
		{
			name: "both numeric",
			out:  "\tmaxfiles    10240          12288\n",
			want: FDLimit{Soft: 10240, Hard: 12288},
		},
		{
			name: "both unlimited",
			out:  "\tmaxfiles    unlimited      unlimited\n",
			want: FDLimit{HardUnlimited: true},
		},
		{
			name: "empty output",
			out:  "",
			want: FDLimit{},
		},
		{
			name: "runner error",
			err:  errors.New("permission denied"),
			want: FDLimit{},
		},
		{
			name: "irregular whitespace",
			out:  "   maxfiles\t 200 \t 400 \n",
			want: FDLimit{Soft: 200, Hard: 400},
		},
		{
			name: "line does not start with maxfiles",
			out:  "\tmaxproc     2000           4000\n",
			want: FDLimit{},
		},
		{
			name: "too few columns",
			out:  "\tmaxfiles    256\n",
			want: FDLimit{},
		},
		{
			name: "non-numeric column",
			out:  "\tmaxfiles    abc            def\n",
			want: FDLimit{},
		},
		{
			name: "multi-line, header ignored",
			out:  "Some header line\n\tmaxfiles    512            1024\n",
			want: FDLimit{Soft: 512, Hard: 1024},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CollectFDLimit(stubFDLimit(tt.out, tt.err))
			if got != tt.want {
				t.Errorf("CollectFDLimit() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
