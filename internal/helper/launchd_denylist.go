package helper

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/kaeawc/spectra/internal/bundleid"
	"github.com/kaeawc/spectra/internal/plistread"
)

// denylist is the parsed helper denylist plus the built-in defaults.
type denylist struct {
	labels  []string // exact, or prefix when ending in "*"
	teamIDs []string
	source  string
}

// defaultDenylistSource names the compiled-in denylist in refusals.
const defaultDenylistSource = "the built-in default denylist"

// loadDenylist is check 0. A missing file falls back to the compiled-in
// DefaultDenylistPlist entries plus the built-ins (the helper's label and
// com.apple, enforced by checkLabel) with an audit warning; a file that
// exists but fails any integrity check or does not parse refuses every
// change.
func (s *launchdService) loadDenylist() (denylist, error) {
	d := denylist{source: DenylistPath}
	invalid := func(format string, args ...any) error {
		return refuse(ReasonDenylistInvalid, "%s: %s; every change through the helper is refused until it is fixed", DenylistPath, fmt.Sprintf(format, args...))
	}
	_, err := s.fs.Lstat(DenylistDir)
	if errors.Is(err, fs.ErrNotExist) {
		return s.defaultDenylist()
	}
	if err != nil {
		return d, invalid("lstat %s: %v", DenylistDir, err)
	}
	if _, err := rootDir(s.fs, DenylistDir); err != nil {
		return d, invalid("%v", err)
	}
	if resolved, err := s.fs.EvalSymlinks(DenylistDir); err != nil || resolved != DenylistDir {
		return d, invalid("%s resolves through a symbolic link", DenylistDir)
	}
	lst, err := s.fs.Lstat(DenylistPath)
	if errors.Is(err, fs.ErrNotExist) {
		return s.defaultDenylist()
	}
	if err != nil {
		return d, invalid("lstat: %v", err)
	}
	if err := checkPolicyFile(lst); err != nil {
		return d, invalid("%v", err)
	}
	data, meta, err := readNoFollow(s.fs, DenylistPath, lst, maxPlistBytes)
	if err != nil {
		return d, invalid("%v", err)
	}
	if err := checkPolicyFile(meta); err != nil {
		return d, invalid("%v", err)
	}
	if err := d.parse(data); err != nil {
		return d, invalid("parse: %v", err)
	}
	return d, nil
}

// defaultDenylist is the fallback when the denylist file is absent: the
// entries install-helper would have written.
func (s *launchdService) defaultDenylist() (denylist, error) {
	s.warn("launchd.system.denylist", DenylistPath+" does not exist; the built-in default denylist applies")
	d := denylist{source: defaultDenylistSource}
	if err := d.parse([]byte(DefaultDenylistPlist)); err != nil {
		return d, fmt.Errorf("built-in default denylist: %w", err)
	}
	return d, nil
}

// checkPolicyFile is the denylist and managed-preferences file rule: a
// regular file, not a symlink, owned by root, mode 0644 or stricter.
func checkPolicyFile(m FileMeta) error {
	switch {
	case m.Mode&fs.ModeSymlink != 0:
		return errors.New("is a symbolic link")
	case !m.Mode.IsRegular():
		return errors.New("is not a regular file")
	case m.UID != 0:
		return fmt.Errorf("is owned by uid %d, not root", m.UID)
	case m.Mode.Perm()&^0o644 != 0:
		return fmt.Errorf("mode %s is looser than 0644", m.Mode.Perm())
	}
	return nil
}

func (d *denylist) parse(data []byte) error {
	root, err := plistread.Decode(data)
	if err != nil {
		return err
	}
	if root.Kind != plistread.Dict {
		return errors.New("top level is not a dictionary")
	}
	labels, err := strings0(root, "Labels")
	if err != nil {
		return err
	}
	for _, l := range labels {
		if !validDenyLabel(l) {
			return fmt.Errorf("invalid Labels entry %q", l)
		}
	}
	teams, err := strings0(root, "TeamIDs")
	if err != nil {
		return err
	}
	for _, t := range teams {
		if !validTeamID(t) {
			return fmt.Errorf("invalid TeamIDs entry %q", t)
		}
	}
	d.labels, d.teamIDs = labels, teams
	return nil
}

// strings0 reads an optional array of strings; any other shape is invalid.
func strings0(root plistread.Value, key string) ([]string, error) {
	v, ok := root.Dict[key]
	if !ok {
		return nil, nil
	}
	if v.Kind != plistread.Array {
		return nil, fmt.Errorf("%s is not an array", key)
	}
	out := make([]string, 0, len(v.Array))
	for _, e := range v.Array {
		if e.Kind != plistread.String {
			return nil, fmt.Errorf("%s holds a non-string entry", key)
		}
		out = append(out, e.String)
	}
	return out, nil
}

func validDenyLabel(l string) bool {
	if prefix, ok := strings.CutSuffix(l, "*"); ok {
		return prefix != "" && bundleid.Valid(prefix)
	}
	return validLabel(l)
}

func (d denylist) matchLabel(label string) error {
	for _, e := range d.labels {
		prefix, isPrefix := strings.CutSuffix(e, "*")
		if (isPrefix && hasFoldPrefix(label, prefix)) || (!isPrefix && strings.EqualFold(label, e)) {
			return refuse(ReasonDenylisted, "%s matches denylist entry %q (kind label) in %s", label, e, d.source)
		}
	}
	return nil
}

func (d denylist) matchTeam(team string) error {
	for _, e := range d.teamIDs {
		if strings.EqualFold(team, e) {
			return refuse(ReasonDenylisted, "team id %s matches denylist entry %q (kind team_id) in %s", team, e, d.source)
		}
	}
	return nil
}

// DefaultDenylistPlist is the file `sudo spectra install-helper` writes
// when none exists. The entries are label prefixes of common endpoint
// security, device-management and backup agents, proposed for review; a
// prefix match only ever refuses a change. The helper also applies these
// entries when the file is absent. TeamIDs ships empty on purpose: no
// vendor team id here is verified against the vendors' own signed
// releases, and a guessed id would either refuse an unrelated vendor or
// give false assurance.
const DefaultDenylistPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Labels</key>
  <array>
    <!-- the Spectra helper itself -->
    <string>dev.spectra.helper</string>
    <!-- endpoint detection and response -->
    <string>com.crowdstrike.*</string>
    <string>com.sentinelone.*</string>
    <string>com.microsoft.wdav*</string>
    <string>com.microsoft.fresno*</string>
    <string>com.carbonblack.*</string>
    <string>com.vmware.carbonblack.*</string>
    <string>com.paloaltonetworks.*</string>
    <string>com.sophos.*</string>
    <string>com.jamf.protect*</string>
    <string>com.elastic.*</string>
    <!-- mobile device management and enrollment -->
    <string>com.jamf.*</string>
    <string>com.jamfsoftware.*</string>
    <string>io.kandji.*</string>
    <string>com.kandji.*</string>
    <string>com.microsoft.intune*</string>
    <string>com.mosyle.*</string>
    <string>com.addigy.*</string>
    <string>com.airwatch.*</string>
    <string>com.vmware.hub*</string>
    <string>com.github.macadmins.*</string>
    <!-- backup agents -->
    <string>com.backblaze.*</string>
    <string>com.code42.*</string>
    <string>com.carbonite.*</string>
    <string>com.crashplan.*</string>
  </array>
  <key>TeamIDs</key>
  <array>
  </array>
</dict>
</plist>
`
