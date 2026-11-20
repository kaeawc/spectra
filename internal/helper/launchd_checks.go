package helper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/kaeawc/spectra/internal/bundleid"
	"github.com/kaeawc/spectra/internal/plistread"
	"github.com/kaeawc/spectra/internal/startup"
)

// verified is the outcome of every check passing for one label and action.
type verified struct {
	action, label string
	target        LaunchdTarget
	plistMeta     FileMeta
	assoc         []string
	state         LaunchdState
	planned       [][]string
}

// verify runs checks 0-9 of the design from scratch. A *Refusal error is a
// failed check; any other error is a fault. Nothing here changes state.
func (s *launchdService) verify(ctx context.Context, label, action string) (*verified, error) {
	deny, err := s.loadDenylist()
	if err != nil {
		return nil, err
	}
	if err := deny.matchLabel(label); err != nil {
		return nil, err
	}
	if err := checkLabel(label); err != nil {
		return nil, err
	}
	v := &verified{action: action, label: label}
	root, err := s.readTarget(v)
	if err != nil {
		return nil, err
	}
	if err := s.checkDuplicates(label, v.target.PlistPath); err != nil {
		return nil, err
	}
	if err := s.resolveProgram(v, root); err != nil {
		return nil, err
	}
	if err := s.checkSigning(ctx, v); err != nil {
		return nil, err
	}
	if err := deny.matchTeam(v.target.TeamID); err != nil {
		return nil, err
	}
	if err := s.checkLaunchd(ctx, v); err != nil {
		return nil, err
	}
	if err := s.checkManaged(ctx, v); err != nil {
		return nil, err
	}
	v.planned = plan(action, label, v.target.PlistPath, v.state.Loaded)
	return v, nil
}

// plan is the exact argv list a change runs.
func plan(action, label, plist string, loaded bool) [][]string {
	target := "system/" + label
	if action == "disable" {
		out := [][]string{{launchctlBin, "disable", target}}
		if loaded {
			out = append(out, []string{launchctlBin, "bootout", target})
		}
		return out
	}
	out := [][]string{{launchctlBin, "enable", target}}
	if !loaded {
		out = append(out, []string{launchctlBin, "bootstrap", "system", plist})
	}
	return out
}

// validLabel is check 1's shape rule: the uninstall.validID rule.
func validLabel(s string) bool {
	if len(s) > maxLabelLen || !bundleid.Valid(s) {
		return false
	}
	segs := strings.Split(s, ".")
	if len(segs) < 2 {
		return false
	}
	for _, seg := range segs {
		if seg == "" {
			return false
		}
	}
	return true
}

func hasFoldPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// checkLabel is check 1.
func checkLabel(label string) error {
	switch {
	case !validLabel(label):
		return refuse(ReasonUnknownLabel, "%q is not a valid launchd label", label)
	case hasFoldPrefix(label, "com.apple."):
		return refuse(ReasonAppleLabel, "refusing to change Apple job %s: macOS components can break when disabled", label)
	case strings.EqualFold(label, HelperLabel):
		return refuse(ReasonDenylisted, "refusing to change %s: it is the Spectra helper itself (built-in denylist entry, kind label)", label)
	}
	return nil
}

// plistPathFor is check 2: the one path a label's plist may have, sitting
// directly in /Library/LaunchDaemons.
func plistPathFor(label string) (string, bool) {
	if !validLabel(label) {
		return "", false
	}
	name := label + ".plist"
	p := filepath.Join(launchDaemonsDir, name)
	if filepath.Dir(p) != launchDaemonsDir || filepath.Base(p) != name {
		return "", false
	}
	return p, true
}

// readTarget runs checks 2-4 and returns the parsed plist.
func (s *launchdService) readTarget(v *verified) (plistread.Value, error) {
	plist, ok := plistPathFor(v.label)
	if !ok {
		return plistread.Value{}, refuse(ReasonLabelFileMismatch, "no plist path can be built for %q", v.label)
	}
	if _, err := rootDir(s.fs, launchDaemonsDir); err != nil {
		return plistread.Value{}, refuse(ReasonPlistPermissions, "%v", err)
	}
	if resolved, err := s.fs.EvalSymlinks(launchDaemonsDir); err != nil || resolved != launchDaemonsDir {
		return plistread.Value{}, refuse(ReasonSymlink, "%s resolves through a symbolic link", launchDaemonsDir)
	}
	lst, err := s.fs.Lstat(plist)
	if errors.Is(err, fs.ErrNotExist) {
		return plistread.Value{}, s.missingPlist(v.label, plist)
	}
	if err != nil {
		return plistread.Value{}, refuse(ReasonUnreadablePlist, "lstat %s: %v", plist, err)
	}
	if err := checkPlistFile(plist, lst); err != nil {
		return plistread.Value{}, err
	}
	data, meta, err := readNoFollow(s.fs, plist, lst, maxPlistBytes)
	if err != nil {
		return plistread.Value{}, refuse(ReasonUnreadablePlist, "%v", err)
	}
	if err := checkPlistFile(plist, meta); err != nil {
		return plistread.Value{}, err
	}
	sum := sha256.Sum256(data)
	v.plistMeta = meta
	v.target.PlistPath, v.target.PlistSHA256 = plist, hex.EncodeToString(sum[:])
	root, err := plistread.Decode(data)
	if err != nil || root.Kind != plistread.Dict {
		return plistread.Value{}, refuse(ReasonUnreadablePlist, "%s is not a property list dictionary", plist)
	}
	if got := stringKey(root, "Label"); got != v.label {
		return plistread.Value{}, refuse(ReasonLabelFileMismatch, "%s defines label %q, not %q", plist, got, v.label)
	}
	if _, ok := root.Dict["BundleProgram"]; ok {
		return plistread.Value{}, refuse(ReasonBundleProgram, "%s uses BundleProgram (an SMAppService job); those are out of scope", plist)
	}
	return root, nil
}

// checkPlistFile is check 3 for the plist itself.
func checkPlistFile(path string, m FileMeta) error {
	switch {
	case m.Mode&fs.ModeSymlink != 0:
		return refuse(ReasonSymlink, "%s is a symbolic link", path)
	case !m.Mode.IsRegular():
		return refuse(ReasonPlistPermissions, "%s is not a regular file", path)
	case m.UID != 0 || m.GID != 0:
		return refuse(ReasonPlistPermissions, "%s is owned by %d:%d, not root:wheel", path, m.UID, m.GID)
	case writableByOthers(m):
		return refuse(ReasonPlistPermissions, "%s is writable by group or others (mode %s)", path, m.Mode.Perm())
	case m.Size > maxPlistBytes:
		return refuse(ReasonUnreadablePlist, "%s is larger than %d bytes", path, maxPlistBytes)
	}
	return nil
}

// missingPlist distinguishes a label defined under another file name
// (label_file_mismatch) from one no plist defines.
func (s *launchdService) missingPlist(label, plist string) error {
	names, err := s.fs.ReadDir(launchDaemonsDir)
	if err != nil {
		return refuse(ReasonUnreadablePlist, "read %s: %v", launchDaemonsDir, err)
	}
	var others []string
	for _, name := range names {
		p := filepath.Join(launchDaemonsDir, name)
		if !candidatePlist(name) {
			continue
		}
		if got, _, _ := s.peekLabel(p); strings.EqualFold(got, label) {
			others = append(others, p)
		}
	}
	if len(others) > 0 {
		return refuse(ReasonLabelFileMismatch, "%s does not exist; the label is defined by %s, and the helper acts only on %s", plist, strings.Join(others, ", "), plist)
	}
	return refuse(ReasonUnknownLabel, "no system launch daemon plist %s", plist)
}

// checkDuplicates is check 5: no other plist in a system-domain directory
// defines the label, and none it cannot read might.
func (s *launchdService) checkDuplicates(label, plist string) error {
	var unknown []string
	for _, dir := range []string{launchDaemonsDir, systemLaunchDaemonsDir} {
		names, err := s.fs.ReadDir(dir)
		if err != nil {
			return refuse(ReasonUnreadablePlist, "read %s: %v", dir, err)
		}
		for _, name := range names {
			p := filepath.Join(dir, name)
			if p == plist || !candidatePlist(name) {
				continue
			}
			got, inert, err := s.peekLabel(p)
			switch {
			case strings.EqualFold(got, label):
				return refuse(ReasonDuplicateLabel, "%s also defines %s and launchd loads only one job per label; remove or rename the extra plist first", p, label)
			case err == nil, inert, got != "":
			default:
				unknown = append(unknown, p)
			}
		}
	}
	if len(unknown) > 0 {
		return refuse(ReasonUnreadablePlist, "could not read the label of %s, so a second job with this label cannot be ruled out", strings.Join(unknown, ", "))
	}
	return nil
}

// candidatePlist reports whether a directory entry is a job plist the
// duplicate scan must read: a name ending in .plist that is not a dotfile.
// Anything else (.DS_Store, README) is ignored. A candidate that is not a
// regular file still counts, and blocks as unreadable: launchd may follow
// it, so the helper does not guess what it defines.
func candidatePlist(name string) bool {
	return strings.HasSuffix(name, ".plist") && !strings.HasPrefix(name, ".")
}

// peekLabel reads the Label of the plist at path. err is set when the file
// could not be fully read or parsed; label may still be set from a partial
// scan of a large file, and inert marks a file that defines no job (empty,
// or a dict with no Label).
func (s *launchdService) peekLabel(path string) (label string, inert bool, err error) {
	lst, err := s.fs.Lstat(path)
	if err != nil {
		return "", false, err
	}
	if !lst.Mode.IsRegular() {
		return "", false, fmt.Errorf("%s is not a regular file", path)
	}
	if lst.Size == 0 {
		return "", true, nil
	}
	if lst.Size > maxPlistBytes {
		return s.scanLargeLabel(path, lst)
	}
	data, _, err := readNoFollow(s.fs, path, lst, maxPlistBytes)
	if err != nil {
		return "", false, err
	}
	root, err := plistread.Decode(data)
	if err != nil {
		return "", false, err
	}
	if root.Kind != plistread.Dict {
		return "", false, fmt.Errorf("%s is not a dictionary", path)
	}
	v, ok := root.Dict["Label"]
	switch {
	case !ok:
		return "", true, nil
	case v.Kind != plistread.String:
		return "", false, fmt.Errorf("%s: Label is not a string", path)
	}
	return v.String, false, nil
}

func (s *launchdService) scanLargeLabel(path string, lst FileMeta) (string, bool, error) {
	rc, meta, err := s.fs.OpenNoFollow(path)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = rc.Close() }()
	tooLarge := fmt.Errorf("%s: %w", path, errTooLarge)
	if !sameFile(lst, meta) {
		return "", false, fmt.Errorf("%s changed between lstat and open", path)
	}
	label, err := startup.ScanPlistLabel(io.LimitReader(rc, maxLabelScan))
	switch {
	case errors.Is(err, startup.ErrNoLabel):
		return "", true, tooLarge
	case err != nil:
		return "", false, tooLarge
	}
	return label, false, tooLarge
}

func stringKey(root plistread.Value, key string) string {
	if v, ok := root.Dict[key]; ok && v.Kind == plistread.String {
		return v.String
	}
	return ""
}

// stringList reads a string or an array of strings.
func stringList(root plistread.Value, key string) []string {
	v, ok := root.Dict[key]
	if !ok {
		return nil
	}
	if v.Kind == plistread.String {
		return []string{v.String}
	}
	var out []string
	if v.Kind == plistread.Array {
		for _, e := range v.Array {
			if e.Kind == plistread.String {
				out = append(out, e.String)
			}
		}
	}
	return out
}

// systemPrefixes are the trees a program may never live in. They are
// compared ignoring case, as the default APFS volume compares names.
var systemPrefixes = []string{"/System", "/usr", "/bin", "/sbin", "/Library/Apple"}

func underFold(path, root string) bool {
	return strings.EqualFold(path, root) || hasFoldPrefix(path, root+"/")
}

// jobProgram is Program, else the first ProgramArguments element.
func jobProgram(root plistread.Value) string {
	if p := stringKey(root, "Program"); p != "" {
		return p
	}
	if a, ok := root.Dict["ProgramArguments"]; ok && a.Kind == plistread.Array && len(a.Array) > 0 && a.Array[0].Kind == plistread.String {
		return a.Array[0].String
	}
	return ""
}

// checkProgramFile requires a clean absolute path outside the system
// trees, with no symlink, to a regular file that only root can replace.
func (s *launchdService) checkProgramFile(program string) error {
	if program == "" || !filepath.IsAbs(program) || filepath.Clean(program) != program {
		return refuse(ReasonProgramNotInBundle, "the job's program %q is not a clean absolute path", program)
	}
	for _, p := range systemPrefixes {
		if underFold(program, p) {
			return refuse(ReasonSystemPath, "the program %s is under %s", program, p)
		}
	}
	if resolved, err := s.fs.EvalSymlinks(program); err != nil || resolved != program {
		return refuse(ReasonSymlink, "the program path %s is or passes through a symbolic link (or does not resolve)", program)
	}
	if pm, err := s.fs.Lstat(program); err != nil || !pm.Mode.IsRegular() {
		return refuse(ReasonProgramNotInBundle, "the program %s is not a regular file", program)
	}
	return s.checkProgramPath(program)
}

// resolveProgram is check 6: the program and the bundle that owns it.
func (s *launchdService) resolveProgram(v *verified, root plistread.Value) error {
	program := jobProgram(root)
	v.assoc = stringList(root, "AssociatedBundleIdentifiers")
	v.target.Program = program
	if err := s.checkProgramFile(program); err != nil {
		return err
	}
	switch {
	case strings.HasPrefix(program, applicationsDir+"/"):
		return s.appProgram(v, program)
	case filepath.Dir(program) == privilegedHelperToolsDir:
		return s.helperToolProgram(v, program)
	}
	return refuse(ReasonProgramNotInBundle, "the program %s is neither inside an app in %s nor directly in %s", program, applicationsDir, privilegedHelperToolsDir)
}

// appProgram handles a program inside /Applications/<Name>.app.
func (s *launchdService) appProgram(v *verified, program string) error {
	first, rest, _ := strings.Cut(strings.TrimPrefix(program, applicationsDir+"/"), "/")
	if !strings.HasSuffix(first, ".app") || rest == "" {
		return refuse(ReasonProgramNotInBundle, "the program %s is not inside an .app directly under %s", program, applicationsDir)
	}
	bundle := filepath.Join(applicationsDir, first)
	id, err := s.bundleID(bundle)
	if err != nil {
		return err
	}
	v.target.BundlePath, v.target.BundleID = bundle, id
	return nil
}

// adminGID is the macOS admin group. /Applications is root:admin 0775, and
// an admin can already act as root, so admin group write on a directory
// does not let anyone less privileged replace the program.
const adminGID = 80

// checkProgramPath protects the verified program until launchd runs it:
// launchd runs whatever sits at that path as root, on this launch and every
// later one. The program and every ancestor directory up to / must be owned
// by root, with no symlink, not world-writable, and not group-writable
// except a directory whose group is admin. Re-checked at execute with
// every other check.
func (s *launchdService) checkProgramPath(program string) error {
	for p := program; ; p = filepath.Dir(p) {
		m, err := s.fs.Lstat(p)
		if err != nil {
			return refuse(ReasonProgramWritable, "lstat %s: %v", p, err)
		}
		groupOK := m.Mode.IsDir() && m.GID == adminGID
		switch {
		case m.Mode&fs.ModeSymlink != 0:
			return refuse(ReasonSymlink, "%s on the program path is a symbolic link", p)
		case m.UID != 0:
			return refuse(ReasonProgramWritable, "%s on the program path is owned by uid %d, not root; a non-root user could replace what launchd runs as root", p, m.UID)
		case m.Mode.Perm()&0o002 != 0, m.Mode.Perm()&0o020 != 0 && !groupOK:
			return refuse(ReasonProgramWritable, "%s on the program path is writable by group or others (mode %s, gid %d)", p, m.Mode.Perm(), m.GID)
		}
		if p == "/" {
			return nil
		}
	}
}

// helperToolProgram handles /Library/PrivilegedHelperTools/<tool>, owned by
// the one app in /Applications named by AssociatedBundleIdentifiers.
func (s *launchdService) helperToolProgram(v *verified, program string) error {
	if _, err := rootDir(s.fs, privilegedHelperToolsDir); err != nil {
		return refuse(ReasonProgramNotInBundle, "%v", err)
	}
	var ids []string
	for _, id := range v.assoc {
		if validLabel(id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return refuse(ReasonProgramNotInBundle, "the program %s is in %s but the plist names no AssociatedBundleIdentifiers", program, privilegedHelperToolsDir)
	}
	names, err := s.fs.ReadDir(applicationsDir)
	if err != nil {
		return refuse(ReasonProgramNotInBundle, "read %s: %v", applicationsDir, err)
	}
	var matches []string
	matchedID := ""
	for _, name := range names {
		if !strings.HasSuffix(name, ".app") {
			continue
		}
		bundle := filepath.Join(applicationsDir, name)
		m, err := s.fs.Lstat(bundle)
		if err != nil || !m.Mode.IsDir() {
			continue
		}
		id, err := s.bundleID(bundle)
		if err != nil {
			continue
		}
		for _, want := range ids {
			if strings.EqualFold(id, want) {
				matches, matchedID = append(matches, bundle), id
				break
			}
		}
	}
	switch len(matches) {
	case 0:
		return refuse(ReasonProgramNotInBundle, "no app in %s has bundle id %s", applicationsDir, strings.Join(ids, " or "))
	case 1:
		v.target.BundlePath, v.target.BundleID = matches[0], matchedID
		return nil
	}
	return refuse(ReasonProgramNotInBundle, "more than one app matches %s (%s); the owner is ambiguous", strings.Join(ids, ", "), strings.Join(matches, ", "))
}

// bundleID reads CFBundleIdentifier of a real (not symlinked) bundle
// directory outside /System.
func (s *launchdService) bundleID(bundle string) (string, error) {
	if underFold(bundle, "/System") {
		return "", refuse(ReasonSystemPath, "the bundle %s is under /System", bundle)
	}
	m, err := s.fs.Lstat(bundle)
	switch {
	case err != nil:
		return "", refuse(ReasonProgramNotInBundle, "lstat %s: %v", bundle, err)
	case m.Mode&fs.ModeSymlink != 0:
		return "", refuse(ReasonSymlink, "the bundle %s is a symbolic link", bundle)
	case !m.Mode.IsDir():
		return "", refuse(ReasonProgramNotInBundle, "the bundle %s is not a directory", bundle)
	}
	if resolved, err := s.fs.EvalSymlinks(bundle); err != nil || resolved != bundle {
		return "", refuse(ReasonSymlink, "the bundle path %s passes through a symbolic link", bundle)
	}
	info := filepath.Join(bundle, "Contents", "Info.plist")
	lst, err := s.fs.Lstat(info)
	if err != nil || !lst.Mode.IsRegular() {
		return "", refuse(ReasonProgramNotInBundle, "the bundle %s has no readable Info.plist", bundle)
	}
	data, _, err := readNoFollow(s.fs, info, lst, maxPlistBytes)
	if err != nil {
		return "", refuse(ReasonProgramNotInBundle, "the bundle %s has no readable Info.plist: %v", bundle, err)
	}
	root, err := plistread.Decode(data)
	if err != nil || root.Kind != plistread.Dict {
		return "", refuse(ReasonProgramNotInBundle, "the bundle %s has an unparseable Info.plist", bundle)
	}
	id := stringKey(root, "CFBundleIdentifier")
	switch {
	case !validLabel(id):
		return "", refuse(ReasonProgramNotInBundle, "the bundle %s has no usable bundle id", bundle)
	case hasFoldPrefix(id, "com.apple.") || strings.EqualFold(id, "com.apple"):
		return "", refuse(ReasonAppleLabel, "the bundle %s is an Apple bundle (%s)", bundle, id)
	}
	return id, nil
}
