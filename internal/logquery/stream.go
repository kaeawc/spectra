package logquery

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os/exec"
)

func Stream(ctx context.Context, q Query) (<-chan LogEntry, <-chan error, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if q.Predicate != "" && !q.AllowUnsafePredicate {
		return nil, nil, ErrUnsafePredicate
	}
	args := buildStreamArgs(q)
	cmd := exec.CommandContext(ctx, "/usr/bin/log", args...)
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	entries, errs := StreamReader(ctx, stdout)
	waitErrs := make(chan error, 1)
	go func() {
		if err := cmd.Wait(); err != nil && ctx.Err() == nil {
			waitErrs <- err
		}
		close(waitErrs)
	}()
	return entries, mergeStreamErrors(errs, waitErrs), nil
}

func StreamReader(ctx context.Context, r io.Reader) (<-chan LogEntry, <-chan error) {
	entries := make(chan LogEntry, 256)
	errs := make(chan error, 1)
	go func() {
		defer close(entries)
		defer close(errs)
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if !bytes.HasPrefix(line, []byte("{")) {
				continue
			}
			entry, err := parseLogEntry(line)
			if err != nil {
				errs <- err
				return
			}
			select {
			case entries <- entry:
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			errs <- err
		}
	}()
	return entries, errs
}

func buildStreamArgs(q Query) []string {
	args := []string{"stream", "--style", "ndjson", "--info"}
	if predicate := buildPredicate(q); predicate != "" {
		args = append(args, "--predicate", predicate)
	}
	return args
}

func mergeStreamErrors(a, b <-chan error) <-chan error {
	out := make(chan error, 2)
	go func() {
		defer close(out)
		for a != nil || b != nil {
			select {
			case err, ok := <-a:
				if !ok {
					a = nil
					continue
				}
				if err != nil {
					out <- err
				}
			case err, ok := <-b:
				if !ok {
					b = nil
					continue
				}
				if err != nil {
					out <- err
				}
			}
		}
	}()
	return out
}
