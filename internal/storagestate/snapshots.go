package storagestate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"howett.net/plist"
)

type SnapshotKind int

const (
	SnapshotUnknown SnapshotKind = iota
	SnapshotOSUpdate
	SnapshotMSUPrepare
	SnapshotTMLocal
	SnapshotTMRemote
)

type APFSSnapshot struct {
	UUID         string       `json:"uuid,omitempty"`
	Name         string       `json:"name"`
	XID          uint64       `json:"xid,omitempty"`
	Purgeable    bool         `json:"purgeable,omitempty"`
	PinsCapacity bool         `json:"pins_capacity,omitempty"`
	Kind         SnapshotKind `json:"kind"`
	CreatedAt    time.Time    `json:"created_at,omitzero,omitempty"`
}

type VolumeSnapshots struct {
	Device     string         `json:"device,omitempty"`
	MountPoint string         `json:"mount_point"`
	Snapshots  []APFSSnapshot `json:"snapshots"`
}

var (
	reOSUpdate   = regexp.MustCompile(`^com\.apple\.os\.update-[A-F0-9]{64,}$`)
	reMSUPrepare = regexp.MustCompile(`^com\.apple\.os\.update-MSUPrepareUpdate$`)
	reTMLocal    = regexp.MustCompile(`^com\.apple\.TimeMachine\.(\d{4}-\d{2}-\d{2}-\d{6})\.local$`)
	reTMRemote   = regexp.MustCompile(`^com\.apple\.TimeMachine\.(\d{4}-\d{2}-\d{2}-\d{6})$`)
)

func (k SnapshotKind) String() string {
	switch k {
	case SnapshotOSUpdate:
		return "os_update"
	case SnapshotMSUPrepare:
		return "msu_prepare"
	case SnapshotTMLocal:
		return "tm_local"
	case SnapshotTMRemote:
		return "tm_remote"
	default:
		return "unknown"
	}
}

func (k SnapshotKind) MarshalJSON() ([]byte, error) {
	return json.Marshal(k.String())
}

func ClassifySnapshotName(name string) (SnapshotKind, time.Time) {
	switch {
	case reOSUpdate.MatchString(name):
		return SnapshotOSUpdate, time.Time{}
	case reMSUPrepare.MatchString(name):
		return SnapshotMSUPrepare, time.Time{}
	case reTMLocal.MatchString(name):
		return SnapshotTMLocal, snapshotTimeFromName(reTMLocal, name)
	case reTMRemote.MatchString(name):
		return SnapshotTMRemote, snapshotTimeFromName(reTMRemote, name)
	default:
		return SnapshotUnknown, time.Time{}
	}
}

func ParseAPFSSnapshots(data []byte) ([]APFSSnapshot, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var root any
	if _, err := plist.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	items := snapshotItems(root)
	out := make([]APFSSnapshot, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		snap := snapshotFromMap(m)
		if snap.Name != "" {
			out = append(out, snap)
		}
	}
	return out, nil
}

func applyAPFSSnapshots(volumes []Volume, run CmdRunner) {
	for i := range volumes {
		if !shouldCollectSnapshots(volumes[i]) {
			continue
		}
		out, err := run("diskutil", "apfs", "listSnapshots", volumes[i].MountPoint, "-plist")
		if err != nil {
			continue
		}
		snapshots, err := ParseAPFSSnapshots(out)
		if err == nil {
			volumes[i].Snapshots = snapshots
		}
	}
}

func shouldCollectSnapshots(v Volume) bool {
	if !strings.EqualFold(v.FSType, "apfs") {
		return false
	}
	return v.MountPoint == "/" || !v.ReadOnly
}

func snapshotItems(root any) []any {
	switch v := root.(type) {
	case []any:
		return v
	case map[string]any:
		if raw, ok := v["Snapshots"]; ok {
			return anySlice(raw)
		}
		if raw, ok := v["snapshots"]; ok {
			return anySlice(raw)
		}
		return []any{v}
	default:
		return nil
	}
}

func snapshotFromMap(m map[string]any) APFSSnapshot {
	name := stringValue(m, "SnapshotName", "Name", "name")
	kind, created := ClassifySnapshotName(name)
	if t, ok := dateValue(m, "SnapshotCreationDate", "CreationDate", "CreatedAt", "Date"); ok {
		created = t
	}
	return APFSSnapshot{
		UUID:         stringValue(m, "SnapshotUUID", "UUID", "uuid"),
		Name:         name,
		XID:          uintValue(m, "SnapshotXID", "XID", "xid"),
		Purgeable:    boolValue(m, "Purgeable", "purgeable"),
		PinsCapacity: boolValue(m, "LimitingContainerShrink", "PinsCapacity", "pins_capacity"),
		Kind:         kind,
		CreatedAt:    created,
	}
}

func snapshotTimeFromName(re *regexp.Regexp, name string) time.Time {
	match := re.FindStringSubmatch(name)
	if len(match) != 2 {
		return time.Time{}
	}
	t, err := time.ParseInLocation("2006-01-02-150405", match[1], time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

func anySlice(raw any) []any {
	switch v := raw.(type) {
	case []any:
		return v
	case nil:
		return nil
	default:
		return []any{v}
	}
}

func firstAny(m map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			return v, true
		}
	}
	return nil, false
}

func stringValue(m map[string]any, keys ...string) string {
	raw, ok := firstAny(m, keys...)
	if !ok || raw == nil {
		return ""
	}
	if v, ok := raw.(string); ok {
		return v
	}
	return strings.TrimSpace(fmt.Sprint(raw))
}

func boolValue(m map[string]any, keys ...string) bool {
	raw, ok := firstAny(m, keys...)
	if !ok {
		return false
	}
	switch v := raw.(type) {
	case bool:
		return v
	case uint64:
		return v != 0
	case int64:
		return v != 0
	case int:
		return v != 0
	default:
		return false
	}
}

func uintValue(m map[string]any, keys ...string) uint64 {
	raw, ok := firstAny(m, keys...)
	if !ok {
		return 0
	}
	switch v := raw.(type) {
	case uint64:
		return v
	case uint:
		return uint64(v)
	case int:
		if v > 0 {
			return uint64(v)
		}
	case int64:
		if v > 0 {
			return uint64(v)
		}
	}
	return 0
}

func dateValue(m map[string]any, keys ...string) (time.Time, bool) {
	raw, ok := firstAny(m, keys...)
	if !ok {
		return time.Time{}, false
	}
	switch v := raw.(type) {
	case time.Time:
		return v, true
	case string:
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05 -0700 MST", "2006-01-02 15:04:05"} {
			if t, err := time.Parse(layout, v); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}
