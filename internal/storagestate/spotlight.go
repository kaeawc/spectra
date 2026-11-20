package storagestate

import (
	"encoding/json"
	"fmt"
	"strings"
)

type SpotlightStatus int

const (
	SpotlightUnknown SpotlightStatus = iota
	SpotlightEnabled
	SpotlightDisabled
	SpotlightError
	SpotlightNoIndex
)

type SpotlightVolume struct {
	MountPoint string             `json:"mount_point"`
	Status     SpotlightStatus    `json:"status"`
	Detail     string             `json:"detail,omitempty"`
	Progress   *SpotlightProgress `json:"progress,omitempty"`
}

// SpotlightProgress is no longer populated: its only source, `mdutil -p`,
// flushes caches. The field stays so existing JSON consumers keep parsing.
type SpotlightProgress struct {
	Phase   string  `json:"phase"`
	Percent float64 `json:"percent,omitempty"`
}

func (s SpotlightStatus) String() string {
	switch s {
	case SpotlightEnabled:
		return "enabled"
	case SpotlightDisabled:
		return "disabled"
	case SpotlightError:
		return "error"
	case SpotlightNoIndex:
		return "no_index"
	default:
		return "unknown"
	}
}

func (s SpotlightStatus) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

// collectSpotlight reads Spotlight status with `mdutil -sa` and, per mount,
// `mdutil -s`. It never runs `mdutil -p`, which flushes network caches, so the
// command stays a read; Progress is therefore left unset.
func collectSpotlight(run CmdRunner, mounts []Mount) []SpotlightVolume {
	if run == nil {
		return nil
	}
	out, err := run("/usr/bin/mdutil", "-sa")
	if err != nil {
		return nil
	}
	volumes := parseMdutilStatus(out)
	candidates := writableAPFSMounts(mounts)
	if len(candidates) == 0 {
		return dedupeSpotlight(volumes)
	}
	byMount := spotlightByMount(volumes)
	filtered := make([]SpotlightVolume, 0, len(candidates))
	for _, mountPoint := range candidates {
		volume, ok := byMount[mountPoint]
		if !ok {
			volume = collectSpotlightMount(run, mountPoint)
		}
		if volume.MountPoint != "" {
			filtered = append(filtered, volume)
		}
	}
	return filtered
}

func collectSpotlightMount(run CmdRunner, mountPoint string) SpotlightVolume {
	out, err := run("/usr/bin/mdutil", "-s", mountPoint)
	if err != nil {
		return SpotlightVolume{}
	}
	volumes := parseMdutilStatus(out)
	if len(volumes) == 0 {
		return SpotlightVolume{}
	}
	return volumes[0]
}

func parseMdutilStatus(data []byte) []SpotlightVolume {
	var out []SpotlightVolume
	var current string
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasSuffix(line, ":") {
			current = strings.TrimSuffix(line, ":")
			continue
		}
		if current == "" {
			continue
		}
		out = append(out, SpotlightVolume{
			MountPoint: current,
			Status:     classifySpotlightStatus(line),
			Detail:     line,
		})
		current = ""
	}
	return out
}

func classifySpotlightStatus(detail string) SpotlightStatus {
	lower := strings.ToLower(detail)
	switch {
	case strings.Contains(lower, "error"):
		return SpotlightError
	case strings.Contains(lower, "no index") || strings.Contains(lower, "not indexed"):
		return SpotlightNoIndex
	case strings.Contains(lower, "disabled"):
		return SpotlightDisabled
	case strings.Contains(lower, "enabled"):
		return SpotlightEnabled
	default:
		return SpotlightUnknown
	}
}

func writableAPFSMounts(mounts []Mount) []string {
	out := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		if mount.FSType != "apfs" || mount.ReadOnly || mount.APFSRole == "simulator" {
			continue
		}
		out = append(out, mount.MountPoint)
	}
	return out
}

func spotlightByMount(volumes []SpotlightVolume) map[string]SpotlightVolume {
	out := make(map[string]SpotlightVolume, len(volumes))
	for _, volume := range volumes {
		if _, exists := out[volume.MountPoint]; !exists {
			out[volume.MountPoint] = volume
		}
	}
	return out
}

func dedupeSpotlight(volumes []SpotlightVolume) []SpotlightVolume {
	seen := map[string]bool{}
	out := make([]SpotlightVolume, 0, len(volumes))
	for _, volume := range volumes {
		if seen[volume.MountPoint] {
			continue
		}
		seen[volume.MountPoint] = true
		out = append(out, volume)
	}
	return out
}

// SpotlightForPath reports the Spotlight state of the volume holding path,
// read like `storage --spotlight`: df names the mount point, then mdutil -s
// reads that volume. It never runs `mdutil -p`, which flushes caches and so is
// not a read; Progress is therefore not set.
func SpotlightForPath(run CmdRunner, path string) (SpotlightVolume, error) {
	if run == nil {
		run = execRunner
	}
	mount, err := MountPointForPath(run, path)
	if err != nil {
		return SpotlightVolume{}, err
	}
	volume := collectSpotlightMount(run, mount)
	if volume.MountPoint == "" {
		return SpotlightVolume{MountPoint: mount}, fmt.Errorf("mdutil -s %s: no status", mount)
	}
	volume.MountPoint = mount
	return volume, nil
}

// MountPointForPath names the mount point holding path, from df.
func MountPointForPath(run CmdRunner, path string) (string, error) {
	if run == nil {
		run = execRunner
	}
	out, err := run("df", "-Pk", path)
	if err != nil {
		return "", fmt.Errorf("df %s: %w", path, err)
	}
	mount := dfMountPoint(string(out))
	if mount == "" {
		return "", fmt.Errorf("df %s: no mount point in output", path)
	}
	return mount, nil
}

// dfMountPoint returns the mount column of the first data row of df -P
// output, joining the remaining fields so a mount point with spaces survives.
func dfMountPoint(out string) string {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[0] == "Filesystem" {
			continue
		}
		return strings.Join(fields[5:], " ")
	}
	return ""
}
