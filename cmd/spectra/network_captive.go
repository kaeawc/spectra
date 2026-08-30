package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/kaeawc/spectra/internal/captiveportal"
)

const (
	captiveTimeout = 5 * time.Second
	captiveBodyCap = 64 * 1024
)

func runNetworkCaptive(args []string) int {
	return runNetworkCaptiveWithIO(args, os.Stdout, os.Stderr, defaultCaptiveFetcher)
}

func runNetworkCaptiveWithIO(args []string, stdout, stderr io.Writer, fetch captiveportal.Fetcher) int {
	fs := flag.NewFlagSet("spectra network captive", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: spectra network captive [--json]")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), captiveTimeout)
	defer cancel()
	result := captiveportal.Probe(ctx, fetch)

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return jsonExit(result)
	}
	printCaptiveResult(stdout, result)
	return captiveExitCode(result)
}

func printCaptiveResult(w io.Writer, r captiveportal.Result) {
	verdict := "CLEAR"
	if r.Portal {
		verdict = "CAPTIVE PORTAL"
	} else if r.Proxied {
		verdict = "PROXIED"
	}
	fmt.Fprintf(w, "captive-portal probe: %s\n", verdict)
	fmt.Fprintf(w, "  url:     %s\n", r.URL)
	if r.Error != "" {
		fmt.Fprintf(w, "  error:   %s\n", r.Error)
	} else {
		fmt.Fprintf(w, "  status:  %d (%dms)\n", r.StatusCode, r.ElapsedMS)
	}
	if r.Location != "" {
		fmt.Fprintf(w, "  location: %s\n", r.Location)
	}
	if r.Via != "" {
		fmt.Fprintf(w, "  via:     %s\n", r.Via)
	}
	if r.Server != "" {
		fmt.Fprintf(w, "  server:  %s\n", r.Server)
	}
	fmt.Fprintf(w, "  %s\n", r.Reason)
}

// captiveExitCode returns 1 when a portal is detected or the probe failed, so
// scripts can gate on connectivity; a clear or merely proxied link returns 0.
func captiveExitCode(r captiveportal.Result) int {
	if r.Portal || r.Error != "" {
		return 1
	}
	return 0
}

func jsonExit(r captiveportal.Result) int {
	return captiveExitCode(r)
}

func defaultCaptiveFetcher(ctx context.Context, url string) (captiveportal.Response, error) {
	client := &http.Client{
		// Never follow redirects: a captive portal's redirect is the signal.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return captiveportal.Response{}, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return captiveportal.Response{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, captiveBodyCap))
	return captiveportal.Response{
		StatusCode: resp.StatusCode,
		Location:   resp.Header.Get("Location"),
		Via:        resp.Header.Get("Via"),
		Server:     resp.Header.Get("Server"),
		Body:       string(body),
		ElapsedMS:  time.Since(start).Milliseconds(),
	}, nil
}
