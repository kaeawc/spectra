package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/owner"
)

func TestRunOwner(t *testing.T) {
	found := owner.Result{Holders: []owner.Holder{{
		PID: 20, User: "jason", Command: "app", Hold: owner.HoldListen, Detail: "tcp *:8080",
		Chain: []owner.Link{{PID: 10, Command: "sshd"}, {PID: 1, Command: "launchd"}},
	}}}
	incomplete := owner.Result{Incomplete: true, Hint: "not running as root"}
	tests := []struct {
		name     string
		args     []string
		res      owner.Result
		err      error
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{name: "found", args: []string{":8080"}, res: found, wantCode: 0, wantOut: "20  jason  app  listen tcp *:8080"},
		{name: "chain", args: []string{":8080"}, res: found, wantCode: 0, wantOut: "sshd (10) <- launchd (1)"},
		{name: "none", args: []string{":8080"}, wantCode: 3, wantOut: "No owner found"},
		{name: "incomplete", args: []string{":8080"}, res: incomplete, wantCode: 3, wantOut: "may be incomplete"},
		{name: "error", args: []string{":8080"}, err: errors.New("lsof: boom"), wantCode: 1, wantErr: "boom"},
		{name: "no args", wantCode: 2, wantErr: "Exit codes"},
		{name: "bad target", args: []string{"garbage"}, wantCode: 2, wantErr: "unrecognised"},
		{name: "bad flag", args: []string{"--nope", "1"}, wantCode: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			find := func(context.Context, owner.Target) (owner.Result, error) { return tt.res, tt.err }
			if code := runOwnerWithIO(tt.args, &out, &errb, "/work", find, nil); code != tt.wantCode {
				t.Fatalf("code = %d, want %d (out=%q err=%q)", code, tt.wantCode, out.String(), errb.String())
			}
			if !strings.Contains(out.String(), tt.wantOut) || !strings.Contains(errb.String(), tt.wantErr) {
				t.Errorf("out=%q err=%q", out.String(), errb.String())
			}
		})
	}
}

func TestRunOwnerJSON(t *testing.T) {
	var out bytes.Buffer
	find := func(_ context.Context, tg owner.Target) (owner.Result, error) {
		return owner.Result{Target: tg}, nil
	}
	if code := runOwnerWithIO([]string{"--json", "tcp:80"}, &out, &bytes.Buffer{}, "/work", find, nil); code != 3 {
		t.Fatalf("code = %d", code)
	}
	var got struct {
		Target  struct{ Kind, Proto string }
		Holders []any
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Target.Kind != "port" || got.Target.Proto != "tcp" || got.Holders == nil {
		t.Errorf("json = %s", out.String())
	}
}

func TestRunOwnerUnder(t *testing.T) {
	busy := owner.UnderResult{Holders: []owner.Holder{{
		PID: 20, User: "jason", Command: "gradle", Hold: owner.HoldCwd, Cwd: "/work/repo/app",
		Chain: []owner.Link{{PID: 10, Command: "zsh"}},
	}}}
	tests := []struct {
		name     string
		args     []string
		res      owner.UnderResult
		err      error
		wantDirs []string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{name: "found", args: []string{"--under", "/work/repo"}, res: busy, wantDirs: []string{"/work/repo"}, wantCode: 0, wantOut: "cwd: /work/repo/app"},
		{name: "relative", args: []string{"--under", "repo", "/b/../c"}, wantDirs: []string{"/work/repo", "/c"}, wantCode: 3, wantOut: "No process is working under /work/repo, /c"},
		{name: "incomplete", args: []string{"--under", "/x"}, res: owner.UnderResult{Incomplete: true, Hint: "denied"}, wantCode: 3, wantOut: "may be incomplete: denied"},
		{name: "deadline", args: []string{"--under", "--timeout", "1ms", "/x"}, err: context.DeadlineExceeded, wantCode: 1, wantErr: "deadline"},
		{name: "no dirs", args: []string{"--under"}, wantCode: 2, wantErr: "--under"},
		{name: "bad timeout", args: []string{"--under", "--timeout", "0s", "/x"}, wantCode: 2, wantErr: "positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			var gotDirs []string
			under := func(_ context.Context, dirs []string) (owner.UnderResult, error) {
				gotDirs = dirs
				return tt.res, tt.err
			}
			if code := runOwnerWithIO(tt.args, &out, &errb, "/work", nil, under); code != tt.wantCode {
				t.Fatalf("code = %d, want %d (out=%q err=%q)", code, tt.wantCode, out.String(), errb.String())
			}
			if !strings.Contains(out.String(), tt.wantOut) || !strings.Contains(errb.String(), tt.wantErr) {
				t.Errorf("out=%q err=%q", out.String(), errb.String())
			}
			if tt.wantDirs != nil && strings.Join(gotDirs, ",") != strings.Join(tt.wantDirs, ",") {
				t.Errorf("dirs = %v, want %v", gotDirs, tt.wantDirs)
			}
		})
	}
}

func TestRunOwnerUnderJSON(t *testing.T) {
	var out bytes.Buffer
	under := func(_ context.Context, dirs []string) (owner.UnderResult, error) {
		return owner.UnderResult{Dirs: dirs, Incomplete: true}, nil
	}
	if code := runOwnerWithIO([]string{"--under", "--json", "/w"}, &out, &bytes.Buffer{}, "/", nil, under); code != 3 {
		t.Fatalf("code = %d", code)
	}
	var got struct {
		Dirs       []string `json:"dirs"`
		Holders    []any    `json:"holders"`
		Incomplete bool     `json:"incomplete"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Dirs) != 1 || got.Holders == nil || !got.Incomplete {
		t.Errorf("json = %s", out.String())
	}
}
