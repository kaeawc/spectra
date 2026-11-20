package startup

import (
	"context"
	"fmt"
	"io"

	"github.com/kaeawc/spectra/internal/hostos"
)

// ErrNoLabel marks a plist that parses but defines no Label.
var ErrNoLabel = errNoLabel

// BTMRecord is one `sfltool dumpbtm` record with its Flags words. Flags is
// non-nil, possibly empty, when the record printed a Flags line, and nil
// when the line was missing.
type BTMRecord struct {
	BTM
	Flags []string
}

// ParseDumpBTM reads `sfltool dumpbtm` output into its records, minus
// developer group headers.
func ParseDumpBTM(out []byte) []BTMRecord {
	items := parseDumpBTM(out)
	got := make([]BTMRecord, 0, len(items))
	for _, it := range items {
		got = append(got, BTMRecord{BTM: it.BTM, Flags: it.flags})
	}
	return got
}

// HasBTMRecordHeader reports whether out contains at least one
// "Records for UID" header, which every real dumpbtm output has.
func HasBTMRecordHeader(out []byte) bool {
	return btmRecordsRE.Match(out)
}

// ParsePrintDisabled reads `launchctl print-disabled <domain>`: label to
// disabled.
func ParsePrintDisabled(out []byte) map[string]bool { return parsePrintDisabled(out) }

// ScanPlistLabel streams an XML plist and returns the top-level Label, or
// ErrNoLabel when its dict has none.
func ScanPlistLabel(r io.Reader) (string, error) { return scanPlistLabel(r) }

// SystemManualCommands are the `sudo launchctl` commands an administrator
// runs by hand to disable or enable a system-domain job.
func SystemManualCommands(action, label, plistPath string) []string {
	return manualCommands(action, &Item{Label: label, Domain: "system", PlistPath: plistPath}, false)
}

// SystemScopeRefusal is what `spectra startup disable|enable --scope
// system` reports when the privileged helper cannot make the change: the
// system item, why spectra did not change it, and the commands to run by
// hand. It only reads; no launchctl verb that changes state runs.
func SystemScopeRefusal(ctx context.Context, opts Options, action, label string) (ChangeResult, error) {
	res := ChangeResult{Action: action, Label: label, Domain: "system", Commands: []Command{}}
	if hostos.Resolve(opts.OS) != hostos.Darwin {
		return res, hostos.Unsupported("startup " + action)
	}
	e := newEnv(opts)
	e.opts.Scope = ""
	e.opts.IncludeApple = true
	e.ownersFor = label
	rep, err := e.list(ctx, btmRead{status: SourceStatus{Name: SourceBTM, Status: StatusSkipped, Detail: "not needed for a system-scope refusal"}})
	if err != nil {
		return res, err
	}
	var item *Item
	for i := range rep.Items {
		if rep.Items[i].Label == label && rep.Items[i].Domain == "system" && rep.Items[i].PlistPath != "" {
			it := rep.Items[i]
			item = &it
			break
		}
	}
	if item == nil {
		res.refuse(RefuseUnknown, fmt.Sprintf("no system launch daemon labelled %q; run `spectra startup list --scope system` to see labels", label))
		return res, nil
	}
	res.Before = item
	if isApple(label) {
		res.refuse(RefuseApple, fmt.Sprintf("refusing to %s Apple item %s: macOS components can break when disabled; run the command yourself if you are sure", action, label),
			manualCommands(action, item, e.root)...)
		return res, nil
	}
	res.refuse(RefuseSystem, e.systemMessage(label, item), manualCommands(action, item, e.root)...)
	return res, nil
}
