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
			if code := runOwnerWithIO(tt.args, &out, &errb, "/work", find); code != tt.wantCode {
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
	if code := runOwnerWithIO([]string{"--json", "tcp:80"}, &out, &bytes.Buffer{}, "/work", find); code != 3 {
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
