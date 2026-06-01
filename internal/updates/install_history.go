package updates

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"howett.net/plist"
)

type InstallEntry struct {
	Name        string    `json:"name"`
	Version     string    `json:"version,omitempty"`
	Source      string    `json:"source,omitempty"`
	InstallDate time.Time `json:"install_date"`
	PackageIDs  []string  `json:"package_ids,omitempty"`
}

type InstallHistory struct {
	Entries     []InstallEntry `json:"entries"`
	CollectedAt time.Time      `json:"collected_at"`
}

type HistoryQuery struct {
	Since  time.Time
	Source string
	Grep   string
}

type historyRunner func(context.Context, string, ...string) ([]byte, error)

func Collect(ctx context.Context) (InstallHistory, error) {
	return collectHistoryWith(ctx, runHistoryCommand, time.Now)
}

func collectHistoryWith(ctx context.Context, run historyRunner, now func() time.Time) (InstallHistory, error) {
	out, err := run(ctx, "system_profiler", "-xml", "SPInstallHistoryDataType")
	if err != nil {
		return InstallHistory{}, err
	}
	history, err := ParseInstallHistory(out, now())
	if err != nil {
		return InstallHistory{}, err
	}
	return history, nil
}

func runHistoryCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.Output()
}

func ParseInstallHistory(data []byte, collectedAt time.Time) (InstallHistory, error) {
	items, err := installHistoryItems(data)
	if err != nil {
		return InstallHistory{}, err
	}
	entries := make([]InstallEntry, 0, len(items))
	for _, item := range items {
		entry := installEntryFromItem(item)
		if entry.Name == "" || entry.InstallDate.IsZero() {
			continue
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].InstallDate.Before(entries[j].InstallDate)
	})
	return InstallHistory{Entries: entries, CollectedAt: collectedAt}, nil
}

func installHistoryItems(data []byte) ([]map[string]any, error) {
	var root []map[string]any
	if _, err := plist.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("decode install history plist: %w", err)
	}
	if len(root) == 0 {
		return nil, nil
	}
	rawItems, ok := root[0]["_items"].([]any)
	if !ok {
		return nil, nil
	}
	items := make([]map[string]any, 0, len(rawItems))
	for _, raw := range rawItems {
		if item, ok := raw.(map[string]any); ok {
			items = append(items, item)
		}
	}
	return items, nil
}

func installEntryFromItem(item map[string]any) InstallEntry {
	return InstallEntry{
		Name:        historyString(item["_name"]),
		Version:     historyString(item["install_version"]),
		Source:      normalizeHistorySource(historyString(item["package_source"])),
		InstallDate: historyTime(item["install_date"]),
		PackageIDs:  historyStringSlice(firstPresent(item, "package_ids", "packageIdentifiers", "package_identifiers", "_packageIdentifiers")),
	}
}

func FilterHistory(history InstallHistory, q HistoryQuery) (InstallHistory, error) {
	source := normalizeSourceFilter(q.Source)
	if q.Source != "" && source == "" {
		return InstallHistory{}, fmt.Errorf("source must be apple or third-party")
	}
	var re *regexp.Regexp
	if q.Grep != "" {
		compiled, err := regexp.Compile(q.Grep)
		if err != nil {
			return InstallHistory{}, fmt.Errorf("grep: %w", err)
		}
		re = compiled
	}
	filtered := InstallHistory{CollectedAt: history.CollectedAt}
	for _, entry := range history.Entries {
		if !q.Since.IsZero() && entry.InstallDate.Before(q.Since) {
			continue
		}
		if source != "" && entry.Source != source {
			continue
		}
		if re != nil && !re.MatchString(historySearchText(entry)) {
			continue
		}
		filtered.Entries = append(filtered.Entries, entry)
	}
	return filtered, nil
}

func historySearchText(entry InstallEntry) string {
	return strings.Join([]string{
		entry.Name,
		entry.Version,
		entry.Source,
		strings.Join(entry.PackageIDs, " "),
	}, " ")
}

func normalizeSourceFilter(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return ""
	case "apple":
		return "Apple"
	case "third-party", "third party", "3rd-party", "3rd party":
		return "3rd Party"
	default:
		return ""
	}
}

func normalizeHistorySource(raw string) string {
	lower := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case lower == "":
		return ""
	case strings.Contains(lower, "apple"):
		return "Apple"
	case strings.Contains(lower, "third") || strings.Contains(lower, "3rd") || strings.Contains(lower, "other"):
		return "3rd Party"
	default:
		return strings.TrimSpace(raw)
	}
}

func firstPresent(item map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := item[key]; ok {
			return value
		}
	}
	return nil
}

func historyString(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func historyTime(v any) time.Time {
	t, _ := v.(time.Time)
	return t
}

func historyStringSlice(v any) []string {
	switch values := v.(type) {
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if s := historyString(value); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return append([]string(nil), values...)
	case string:
		if strings.TrimSpace(values) == "" {
			return nil
		}
		return []string{strings.TrimSpace(values)}
	default:
		return nil
	}
}
