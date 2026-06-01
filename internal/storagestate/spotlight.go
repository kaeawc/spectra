package storagestate

import (
	"encoding/json"
	"regexp"
	"strconv"
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

func collectSpotlight(run CmdRunner, mounts []Mount) []SpotlightVolume {
	if run == nil {
		return nil
	}
	out, err := run("/usr/bin/mdutil", "-sa")
	if err != nil {
		return nil
	}
	volumes := parseMdutilStatus(out)
	byMount := spotlightByMount(volumes)
	progress := parseMdutilProgressOutput(run)
	candidates := writableAPFSMounts(mounts)
	if len(candidates) == 0 {
		return attachSpotlightProgress(dedupeSpotlight(volumes), progress)
	}
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
	return attachSpotlightProgress(filtered, progress)
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

func parseMdutilProgressOutput(run CmdRunner) map[string]*SpotlightProgress {
	out, err := run("/usr/bin/mdutil", "-p")
	if err != nil {
		return nil
	}
	return parseMdutilProgress(out)
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

var (
	spotlightPhaseRE   = regexp.MustCompile(`(?i)\b(scanning|indexing|idle)\b`)
	spotlightPercentRE = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)%`)
)

func parseMdutilProgress(data []byte) map[string]*SpotlightProgress {
	progress := map[string]*SpotlightProgress{}
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
		if parsed := parseSpotlightProgressLine(line); parsed != nil {
			progress[current] = parsed
		}
	}
	return progress
}

func parseSpotlightProgressLine(line string) *SpotlightProgress {
	match := spotlightPhaseRE.FindStringSubmatch(line)
	if len(match) < 2 {
		return nil
	}
	progress := &SpotlightProgress{Phase: titleASCII(match[1])}
	if percentMatch := spotlightPercentRE.FindStringSubmatch(line); len(percentMatch) == 2 {
		if percent, err := strconv.ParseFloat(percentMatch[1], 64); err == nil {
			progress.Percent = percent
		}
	}
	return progress
}

func titleASCII(s string) string {
	s = strings.ToLower(s)
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
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

func attachSpotlightProgress(volumes []SpotlightVolume, progress map[string]*SpotlightProgress) []SpotlightVolume {
	for i := range volumes {
		if p := progress[volumes[i].MountPoint]; p != nil {
			volumes[i].Progress = p
		}
	}
	return volumes
}
