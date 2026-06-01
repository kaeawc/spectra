package updates

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const preparedLine = `2026-05-01 10:00:00-05 mac softwareupdated[100]: SUOSUBackgroundDownloadEvaluator: <SUOSUMajorProduct: MSU_UPDATE_25F71_patch_26.5_major>(Title:macOS Tahoe 26.5 Version:26.5, Identifier:(null), IconSize:1052006, Size:123456789, Deferred:0) is already prepared`
const previousLine = `2026-06-01 13:14:29-05 mac softwareupdated[93419]: Previous: SUMacControllerDescriptor(UUID:7B97 SU:macOS Sequoia 15.7.7 24G720 (Customer)(SU) SFR:macOS Sequoia 15.7.7 24G720 (Customer) BRAIN:24G720)`
const currentLine = `2026-06-01 13:14:29-05 mac softwareupdated[93419]: Current: SUMacControllerDescriptor(UUID:7B97 SU:macOS Tahoe 26.5 25F71 (Customer)(SU) SFR:macOS Tahoe 26.5 25F71 (Customer) BRAIN:25F71)`

func TestParseInstallLogLine(t *testing.T) {
	entry, ok := ParseInstallLogLine(preparedLine)
	if !ok {
		t.Fatal("line did not parse")
	}
	if entry.Process != "softwareupdated" || entry.PID != 100 || entry.Hostname != "mac" {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.Timestamp.IsZero() {
		t.Fatal("timestamp not parsed")
	}
}

func TestParseMajorUpdatePrepared(t *testing.T) {
	got, ok := ParseMajorUpdatePrepared(preparedLine)
	if !ok {
		t.Fatal("prepared line did not parse")
	}
	if got.AssetID != "MSU_UPDATE_25F71_patch_26.5_major" || got.Title != "macOS Tahoe 26.5" || got.Version != "26.5" || got.SizeBytes != 123456789 {
		t.Fatalf("prepared = %+v", got)
	}
}

func TestParseOSControllerTransition(t *testing.T) {
	prev, ok := ParseOSControllerTransition(previousLine)
	if !ok || prev.PreviousLabel != "macOS Sequoia 15.7.7 24G720" {
		t.Fatalf("previous = %+v ok=%v", prev, ok)
	}
	cur, ok := ParseOSControllerTransition(currentLine)
	if !ok || cur.CurrentLabel != "macOS Tahoe 26.5 25F71" {
		t.Fatalf("current = %+v ok=%v", cur, ok)
	}
}

func TestQueryInstallLogReadsGzip(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "install.log")
	gzPath := filepath.Join(dir, "install.log.1.gz")
	if err := os.WriteFile(plain, []byte(preparedLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeGzip(t, gzPath, currentLine+"\n")
	result, err := QueryInstallLog(Query{
		Since:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		MaxLines: 10,
		Paths:    []string{plain, gzPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 2 || len(result.FilesRead) != 2 {
		t.Fatalf("result = %+v", result)
	}
}

func writeGzip(t *testing.T, path, data string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz := gzip.NewWriter(file)
	if _, err := gz.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}
