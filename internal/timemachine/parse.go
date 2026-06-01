package timemachine

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"howett.net/plist"
)

// ParseDestinations parses `tmutil destinationinfo -X` output.
func ParseDestinations(data []byte) ([]TMDestination, error) {
	root, err := decodePlist(data)
	if err != nil {
		return nil, err
	}
	var items []any
	switch v := root.(type) {
	case []any:
		items = v
	case map[string]any:
		if len(v) == 0 {
			return []TMDestination{}, nil
		}
		if raw, ok := firstAny(v, "Destinations", "destinations", "Destination"); ok {
			items = anySlice(raw)
		} else {
			items = []any{v}
		}
	default:
		return nil, fmt.Errorf("destination root %T", root)
	}
	out := make([]TMDestination, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		d := destinationFromMap(m)
		if !destinationEmpty(d) {
			out = append(out, d)
		}
	}
	return out, nil
}

// ParseStatus parses `tmutil status -X` output.
func ParseStatus(data []byte) (TMStatus, error) {
	root, err := decodePlist(data)
	if err != nil {
		return TMStatus{}, err
	}
	m, ok := root.(map[string]any)
	if !ok {
		return TMStatus{}, fmt.Errorf("status root %T", root)
	}
	return TMStatus{
		Running:               boolValue(m, "Running", "running"),
		Percent:               floatValue(m, "Percent", "percent"),
		ClientID:              stringValue(m, "ClientID", "Client ID", "client_id"),
		BackupPhase:           stringValue(m, "BackupPhase", "Backup Phase", "backup_phase"),
		DestinationID:         stringValue(m, "DestinationID", "Destination ID", "destination_id"),
		DestinationMountPoint: stringValue(m, "DestinationMountPoint", "Destination Mount Point", "destination_mount_point"),
		FirstBackup:           boolValue(m, "FirstBackup", "First Backup", "first_backup"),
	}, nil
}

// ParseLocalSnapshots parses `tmutil listlocalsnapshots / -X` output.
func ParseLocalSnapshots(data []byte) ([]TMLocalSnapshot, error) {
	root, err := decodePlist(data)
	if err != nil {
		return nil, err
	}
	items := localSnapshotItems(root)
	out := make([]TMLocalSnapshot, 0, len(items))
	for _, item := range items {
		switch v := item.(type) {
		case string:
			out = append(out, localSnapshotFromName(v, ""))
		case map[string]any:
			name := stringValue(v, "Name", "Snapshot", "snapshot", "name")
			if name == "" {
				continue
			}
			snap := localSnapshotFromName(name, stringValue(v, "Volume", "volume"))
			if date, ok := dateValue(v, "Date", "date"); ok {
				snap.Date = date
			}
			out = append(out, snap)
		}
	}
	return out, nil
}

func decodePlist(data []byte) (any, error) {
	var root any
	if _, err := plist.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	return root, nil
}

func destinationFromMap(m map[string]any) TMDestination {
	return TMDestination{
		ID:             stringValue(m, "ID", "DestinationID", "Destination ID", "UUID"),
		Name:           stringValue(m, "Name", "DestinationName", "Destination Name"),
		Kind:           stringValue(m, "Kind", "DestinationKind", "Destination Kind"),
		MountPoint:     stringValue(m, "MountPoint", "Mount Point", "DestinationMountPoint"),
		URL:            stringValue(m, "URL", "NetworkURL", "Network URL"),
		BytesAvailable: uintValue(m, "BytesAvailable", "Bytes Available", "AvailableBytes"),
		BytesUsed:      uintValue(m, "BytesUsed", "Bytes Used", "UsedBytes"),
		LastBackup:     timeValue(m, "LastBackup", "Last Backup", "LastBackupDate"),
		NextBackup:     timeValue(m, "NextBackup", "Next Backup", "NextBackupDate"),
		QuotaGB:        uintValue(m, "QuotaGB", "Quota GB"),
		Encrypted:      boolValue(m, "Encrypted", "IsEncrypted"),
	}
}

func destinationEmpty(d TMDestination) bool {
	return d.ID == "" &&
		d.Name == "" &&
		d.Kind == "" &&
		d.MountPoint == "" &&
		d.URL == "" &&
		d.BytesAvailable == 0 &&
		d.BytesUsed == 0
}

func localSnapshotItems(root any) []any {
	switch v := root.(type) {
	case []any:
		return v
	case map[string]any:
		for _, key := range []string{"Snapshots", "LocalSnapshots", "local_snapshots", "snapshots"} {
			if raw, ok := v[key]; ok {
				return anySlice(raw)
			}
		}
		return []any{v}
	default:
		return nil
	}
}

var snapshotNameDateRE = regexp.MustCompile(`(\d{4}-\d{2}-\d{2}-\d{6})`)

func localSnapshotFromName(name, volume string) TMLocalSnapshot {
	s := TMLocalSnapshot{Name: name, Volume: volume}
	if volume == "" {
		s.Volume = filepath.Dir(name)
		if s.Volume == "." {
			s.Volume = ""
		}
	}
	if match := snapshotNameDateRE.FindStringSubmatch(name); len(match) == 2 {
		if t, err := time.ParseInLocation("2006-01-02-150405", match[1], time.Local); err == nil {
			s.Date = t
		}
	}
	return s
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
	if !ok {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
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

func floatValue(m map[string]any, keys ...string) float64 {
	raw, ok := firstAny(m, keys...)
	if !ok {
		return 0
	}
	switch v := raw.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case uint64:
		return float64(v)
	default:
		return 0
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

func timeValue(m map[string]any, keys ...string) time.Time {
	t, _ := dateValue(m, keys...)
	return t
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
