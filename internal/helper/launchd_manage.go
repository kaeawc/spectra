package helper

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/kaeawc/spectra/internal/plistread"
	"github.com/kaeawc/spectra/internal/startup"
)

// knownBTMFlags are the flag words pinned from sfltool fixtures; none marks
// management. Any other word, including "managed", refuses.
var knownBTMFlags = map[string]bool{"legacy": true, "curated": true, "sandboxed": true}

// checkManaged is check 9: configuration-profile service-management rules
// and BTM flags, both read as root.
func (s *launchdService) checkManaged(ctx context.Context, v *verified) error {
	files, err := s.serviceManagementFiles()
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := s.checkSMFile(f, v); err != nil {
			return err
		}
	}
	return s.checkBTM(ctx, v)
}

// serviceManagementFiles lists the payload files that exist: the machine
// one and one per user directory of /Library/Managed Preferences.
func (s *launchdService) serviceManagementFiles() ([]string, error) {
	unknown := func(format string, args ...any) error {
		return refuse(ReasonManagementUnknown, format, args...)
	}
	m, err := s.fs.Lstat(managedPrefsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, unknown("lstat %s: %v", managedPrefsDir, err)
	}
	if m.Mode&fs.ModeSymlink != 0 || !m.Mode.IsDir() {
		return nil, unknown("%s is not a directory", managedPrefsDir)
	}
	names, err := s.fs.ReadDir(managedPrefsDir)
	if err != nil {
		return nil, unknown("read %s: %v", managedPrefsDir, err)
	}
	candidates := []string{filepath.Join(managedPrefsDir, serviceManagementPlist)}
	for _, name := range names {
		p := filepath.Join(managedPrefsDir, name)
		em, err := s.fs.Lstat(p)
		switch {
		case err != nil:
			return nil, unknown("lstat %s: %v", p, err)
		case em.Mode&fs.ModeSymlink != 0:
			return nil, unknown("%s is a symbolic link", p)
		case em.Mode.IsDir():
			candidates = append(candidates, filepath.Join(p, serviceManagementPlist))
		}
	}
	var out []string
	for _, c := range candidates {
		if _, err := s.fs.Lstat(c); err == nil || !errors.Is(err, fs.ErrNotExist) {
			out = append(out, c)
		}
	}
	return out, nil
}

// checkSMFile reads one service-management payload and matches its Rules.
func (s *launchdService) checkSMFile(path string, v *verified) error {
	unknown := func(format string, args ...any) error {
		return refuse(ReasonManagementUnknown, "%s: %s", path, fmt.Sprintf(format, args...))
	}
	lst, err := s.fs.Lstat(path)
	if err != nil {
		return unknown("lstat: %v", err)
	}
	if err := checkPolicyFile(lst); err != nil {
		return unknown("%v", err)
	}
	data, _, err := readNoFollow(s.fs, path, lst, maxPlistBytes)
	if err != nil {
		return unknown("%v", err)
	}
	root, err := plistread.Decode(data)
	if err != nil || root.Kind != plistread.Dict {
		return unknown("not a property list dictionary")
	}
	rules, ok := root.Dict["Rules"]
	if !ok {
		return nil
	}
	if rules.Kind != plistread.Array {
		return unknown("Rules is not an array")
	}
	for i, r := range rules.Array {
		typ, val := stringKey(r, "RuleType"), stringKey(r, "RuleValue")
		if r.Kind != plistread.Dict || typ == "" {
			return unknown("rule %d is malformed", i)
		}
		hit, known := ruleMatches(typ, val, v)
		if !known {
			return unknown("rule %d has unknown RuleType %q", i, typ)
		}
		if hit {
			return refuse(ReasonManaged, "%s is managed by a configuration profile: %s rule %s=%q", v.label, path, typ, val)
		}
	}
	return nil
}

func ruleMatches(typ, val string, v *verified) (hit, known bool) {
	ids := append([]string{v.target.BundleID}, v.assoc...)
	anyID := func(match func(id string) bool) bool {
		for _, id := range ids {
			if id != "" && match(id) {
				return true
			}
		}
		return false
	}
	switch typ {
	case "Label":
		return strings.EqualFold(v.label, val), true
	case "LabelPrefix":
		return hasFoldPrefix(v.label, val), true
	case "BundleIdentifier":
		return anyID(func(id string) bool { return strings.EqualFold(id, val) }), true
	case "BundleIdentifierPrefix":
		return anyID(func(id string) bool { return hasFoldPrefix(id, val) }), true
	case "TeamIdentifier":
		return strings.EqualFold(v.target.TeamID, val), true
	}
	return false, false
}

// checkBTM runs sfltool dumpbtm as root and refuses a record for the
// target that is flagged managed or carries an unpinned flag word.
func (s *launchdService) checkBTM(ctx context.Context, v *verified) error {
	res, err := s.runCmd(ctx, sfltoolBin, "dumpbtm")
	switch {
	case err != nil:
		return refuse(ReasonManagementUnknown, "sfltool dumpbtm: %v", err)
	case res.ExitCode != 0:
		return refuse(ReasonManagementUnknown, "sfltool dumpbtm exited %d", res.ExitCode)
	case len(res.Stdout) > maxBTMOutput:
		return refuse(ReasonManagementUnknown, "sfltool dumpbtm output is larger than %d bytes", maxBTMOutput)
	case !startup.HasBTMRecordHeader(res.Stdout):
		return refuse(ReasonManagementUnknown, "sfltool dumpbtm output has no BTM records header")
	}
	for _, rec := range startup.ParseDumpBTM(res.Stdout) {
		if !btmSelects(rec, v) {
			continue
		}
		if rec.Flags == nil {
			return refuse(ReasonManagementUnknown, "the BTM record %s has no Flags line", rec.Identifier)
		}
		for _, f := range rec.Flags {
			if !knownBTMFlags[f] {
				return refuse(ReasonManaged, "the BTM record %s carries flag %q, which marks it managed or is not recognised", rec.Identifier, f)
			}
		}
	}
	return nil
}

func btmSelects(rec startup.BTMRecord, v *verified) bool {
	if strings.EqualFold(rec.Identifier, v.label) || strings.HasSuffix(rec.Identifier, "."+v.label) {
		return true
	}
	u, err := url.Parse(rec.URL)
	return err == nil && u.Scheme == "file" && strings.TrimSuffix(u.Path, "/") == v.target.PlistPath
}
