package hostload

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/plistread"
	"github.com/kaeawc/spectra/internal/storagestate"
)

// NeverIndexMarker is the file whose presence in a directory (or an
// ancestor) asks Spotlight not to index it.
const NeverIndexMarker = ".metadata_never_index"

// Privacy list states.
const (
	PrivacyListed     = "listed"
	PrivacyNotListed  = "not_listed"
	PrivacyUnreadable = "unreadable"
)

// dataVolume is where macOS mounts the user data volume; Spotlight
// exclusions may name a path with or without this prefix.
const dataVolume = "/System/Volumes/Data"

// RootSize is a bounded walk of a root.
type RootSize struct {
	Files int64  `json:"files"`
	Bytes uint64 `json:"bytes"`
	// Partial is set when the walk stopped at its time bound.
	Partial bool `json:"partial"`
}

// RootSpotlight is the Spotlight state of one --root, reported when
// indexing leads the CPU table.
type RootSpotlight struct {
	Root string `json:"root"`
	// Unsupported is set off macOS, and then nothing else is.
	Unsupported string `json:"unsupported,omitempty"`
	// Excluded is null when it cannot be told: no marker and a privacy list
	// that cannot be read without root.
	Excluded     *bool                         `json:"excluded"`
	Marker       string                        `json:"marker,omitempty"`
	PrivacyList  string                        `json:"privacy_list,omitempty"`
	PrivacyEntry string                        `json:"privacy_entry,omitempty"`
	Volume       *storagestate.SpotlightVolume `json:"volume,omitempty"`
	VolumeError  string                        `json:"volume_error,omitempty"`
	Size         *RootSize                     `json:"size,omitempty"`
	SizeError    string                        `json:"size_error,omitempty"`
	Hint         string                        `json:"hint,omitempty"`
}

// SpotlightProbe reads the per-root Spotlight state. Every field is a seam;
// nil Exists/ReadFile/Volume/Size skip that part of the report.
type SpotlightProbe struct {
	OS       hostos.Kind
	Exists   func(string) bool
	ReadFile func(string) ([]byte, error)
	Volume   func(ctx context.Context, path string) (storagestate.SpotlightVolume, error)
	Size     func(ctx context.Context, path string) (RootSize, error)
}

// Inspect reports each root. It only reads: no marker is written and no
// setting is changed.
func (p SpotlightProbe) Inspect(ctx context.Context, roots []Root) []RootSpotlight {
	out := make([]RootSpotlight, 0, len(roots))
	for _, r := range roots {
		if k := hostos.Resolve(p.OS); k != hostos.Darwin {
			// hostos.Unsupported names the running host; name the selected OS.
			err := fmt.Errorf("spotlight root state: %w (%s)", hostos.ErrUnsupported, k)
			out = append(out, RootSpotlight{Root: r.Path, Unsupported: err.Error()})
			continue
		}
		out = append(out, p.inspect(ctx, r))
	}
	return out
}

func (p SpotlightProbe) inspect(ctx context.Context, r Root) RootSpotlight {
	dir := rootDir(r)
	rs := RootSpotlight{Root: r.Path, Marker: p.marker(dir)}
	if p.Volume != nil {
		if v, err := p.Volume(ctx, dir); err != nil {
			rs.VolumeError = err.Error()
		} else {
			rs.Volume = &v
		}
	}
	rs.PrivacyList, rs.PrivacyEntry = p.privacy(dir, rs.Volume)
	rs.Excluded = excluded(rs)
	if p.Size != nil {
		if s, err := p.Size(ctx, dir); err != nil {
			rs.SizeError = err.Error()
		} else {
			rs.Size = &s
		}
	}
	rs.Hint = spotlightHint(rs)
	return rs
}

func rootDir(r Root) string {
	if r.Resolved != "" {
		return filepath.Clean(r.Resolved)
	}
	return filepath.Clean(r.Path)
}

// marker returns the nearest NeverIndexMarker at dir or above, or "".
func (p SpotlightProbe) marker(dir string) string {
	if p.Exists == nil {
		return ""
	}
	for d := dir; ; d = path.Dir(d) {
		if m := path.Join(d, NeverIndexMarker); p.Exists(m) {
			return m
		}
		if d == "/" || d == "." || d == path.Dir(d) {
			return ""
		}
	}
}

// privacy reads the volume's Spotlight exclusion list. It is root-only on
// current macOS, so unreadable is the usual answer for a normal user.
func (p SpotlightProbe) privacy(dir string, v *storagestate.SpotlightVolume) (string, string) {
	if p.ReadFile == nil {
		return PrivacyUnreadable, ""
	}
	mount := dataVolume
	if v != nil && v.MountPoint != "" {
		mount = v.MountPoint
	}
	data, err := p.ReadFile(path.Join(mount, ".Spotlight-V100", "VolumeConfiguration.plist"))
	if err != nil {
		return PrivacyUnreadable, ""
	}
	entries, err := SpotlightExclusions(data)
	if err != nil {
		return PrivacyUnreadable, ""
	}
	if e := MatchExclusion(dir, entries); e != "" {
		return PrivacyListed, e
	}
	return PrivacyNotListed, ""
}

// SpotlightExclusions returns the Exclusions array of a
// VolumeConfiguration.plist.
func SpotlightExclusions(data []byte) ([]string, error) {
	v, err := plistread.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("decode spotlight volume configuration: %w", err)
	}
	var out []string
	for _, e := range v.Dict["Exclusions"].Array {
		if e.Kind == plistread.String && e.String != "" {
			out = append(out, e.String)
		}
	}
	return out, nil
}

// MatchExclusion returns the entry that covers dir (dir itself or an
// ancestor), comparing with the data-volume prefix stripped from both.
func MatchExclusion(dir string, entries []string) string {
	d := stripDataVolume(dir)
	for _, e := range entries {
		if underRoot(d, stripDataVolume(filepath.Clean(e))) {
			return e
		}
	}
	return ""
}

func stripDataVolume(p string) string {
	if rest, ok := strings.CutPrefix(p, dataVolume); ok && (rest == "" || rest[0] == '/') {
		if rest == "" {
			return "/"
		}
		return rest
	}
	return p
}

func excluded(rs RootSpotlight) *bool {
	yes, no := true, false
	switch {
	case rs.Marker != "" || rs.PrivacyList == PrivacyListed:
		return &yes
	case rs.PrivacyList == PrivacyNotListed:
		return &no
	default:
		return nil
	}
}

func volumeIndexing(rs RootSpotlight) bool {
	return rs.Volume == nil || rs.Volume.Status == storagestate.SpotlightEnabled || rs.Volume.Status == storagestate.SpotlightUnknown
}

// spotlightHint names the action for one root: exclude it, or shrink it.
func spotlightHint(rs RootSpotlight) string {
	size := ""
	if rs.Size != nil {
		more := ""
		if rs.Size.Partial {
			more = " so far"
		}
		size = fmt.Sprintf(" (%d files, %.1f GiB%s)", rs.Size.Files, float64(rs.Size.Bytes)/gib, more)
	}
	switch {
	case !volumeIndexing(rs):
		return fmt.Sprintf("indexing is %s on %s, so this root is not what the indexer is working on", rs.Volume.Status, rs.Volume.MountPoint)
	case rs.Excluded != nil && *rs.Excluded:
		return fmt.Sprintf("%s is already excluded from Spotlight; shrink it%s: remove finished worktrees", rs.Root, size)
	case rs.Excluded == nil:
		return fmt.Sprintf("%s has no %s marker and the Spotlight privacy list is not readable without root; exclude it (System Settings > Spotlight > Search Privacy, or a %s file in it) or shrink it%s", rs.Root, NeverIndexMarker, NeverIndexMarker, size)
	default:
		return fmt.Sprintf("%s is not excluded from Spotlight; exclude it (System Settings > Spotlight > Search Privacy, or a %s file in it) or shrink it%s", rs.Root, NeverIndexMarker, size)
	}
}
