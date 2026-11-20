package uninstall

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/kaeawc/spectra/internal/proc"
)

// maxReceiptFileChecks bounds the per-file pkgutil lookups that decide
// whether a receipt-listed file is shared; files past it are reported as
// shared rather than guessed at.
const maxReceiptFileChecks = 200

// receiptIDsFor asks pkgutil which receipts list the bundle.
func receiptIDsFor(ctx context.Context, r proc.Runner, bundle string) []string {
	out, err := proc.Output(ctx, r, "pkgutil", "--file-info", bundle)
	if err != nil {
		return []string{}
	}
	return uniqueSorted(pkgIDLines(string(out)))
}

func pkgIDLines(out string) []string {
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		if id, ok := strings.CutPrefix(strings.TrimSpace(line), "pkgid: "); ok && validID(id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// receiptPrefix returns volume/location from `pkgutil --pkg-info`.
func receiptPrefix(ctx context.Context, r proc.Runner, id string) (string, bool) {
	out, err := proc.Output(ctx, r, "pkgutil", "--pkg-info", id)
	if err != nil {
		return "", false
	}
	var volume, location string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "volume: "); ok {
			volume = v
		} else if v, ok := strings.CutPrefix(line, "location: "); ok {
			location = v
		}
	}
	if volume == "" {
		return "", false
	}
	return filepath.Join(volume, location), true
}

// matchReceipts adds each receipt whose file list includes the bundle, and
// the files that receipt installed outside the bundle.
func (m *matcher) matchReceipts() {
	for _, id := range m.app.Receipts {
		prefix, ok := receiptPrefix(m.ctx, m.o.Runner, id)
		if !ok {
			continue
		}
		files, ok := m.receiptFiles(id, prefix)
		if !ok {
			continue
		}
		for _, ext := range []string{".bom", ".plist"} {
			if p, ok := idPath(m.o.Receipts, id+ext); ok {
				m.add(p, KindReceipt, ReasonReceipt, id)
			}
		}
		m.addReceiptFiles(id, files)
	}
}

// receiptFiles returns the absolute paths a receipt lists, and whether the
// list includes the bundle itself; a receipt that does not is not the app's.
func (m *matcher) receiptFiles(id, prefix string) ([]string, bool) {
	out, err := proc.Output(m.ctx, m.o.Runner, "pkgutil", "--files", id)
	if err != nil {
		return nil, false
	}
	bundle := filepath.Clean(m.app.Path)
	listsBundle := false
	var outside []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "..") {
			continue
		}
		p := filepath.Join(prefix, line)
		if p == bundle {
			listsBundle = true
		}
		if !inside(p, bundle) {
			outside = append(outside, p)
		}
	}
	return outside, listsBundle
}

// addReceiptFiles adds listed files (never directories, which installers
// share) and marks a file shared when another receipt also lists it. Every
// receipt file is report-only either way (see actionFor).
func (m *matcher) addReceiptFiles(id string, files []string) {
	checks := 0
	for _, p := range files {
		meta, err := m.o.FS.Meta(p)
		if err != nil || meta.Mode.IsDir() {
			continue
		}
		it := m.add(p, KindReceiptFile, ReasonReceipt, id)
		if it == nil || it.Kind != KindReceiptFile {
			continue
		}
		checks++
		if checks > maxReceiptFileChecks {
			it.Shared, it.Note = true, "ownership by other receipts not checked"
			continue
		}
		out, err := proc.Output(m.ctx, m.o.Runner, "pkgutil", "--file-info", p)
		owners := pkgIDLines(string(out))
		if err != nil || len(owners) != 1 || !strings.EqualFold(owners[0], id) {
			it.Shared, it.Note = true, "listed by more than one receipt, or ownership unknown"
		}
	}
}
