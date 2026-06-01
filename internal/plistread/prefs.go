package plistread

import "time"

// TMPrefs is the subset of Time Machine preferences Spectra collectors need.
type TMPrefs struct {
	AutoBackup         bool
	Destinations       []map[string]any
	SkipPaths          []string
	RequiresACPower    bool
	PreferencesVersion int
}

// SoftwareUpdatePrefs is the stable subset of software update preferences.
type SoftwareUpdatePrefs struct {
	AutomaticCheckEnabled            bool
	AutomaticDownload                bool
	AutomaticallyInstallMacOSUpdates bool
	ConfigDataInstall                bool
	CriticalUpdateInstall            bool
	LastAttemptSystemVersion         string
	LastRecommendedMajorOSBundleID   string
	LastSuccessfulDate               time.Time
	LastFullSuccessfulDate           time.Time
	LastRecommendedUpdatesAvailable  int
	RecommendedUpdates               []map[string]any
	PrimaryLanguages                 []string
	PreferencesVersion               int
}

// SpotlightPrefs is the subset of Spotlight preferences useful to storage and
// indexing collectors.
type SpotlightPrefs struct {
	IndexingEnabled    bool
	MenuItemHidden     bool
	StoresDisabled     bool
	OrderedItems       []map[string]any
	Exclusions         []string
	Volumes            []string
	PreferencesVersion int
}

// ReadTimeMachinePrefs reads /Library/Preferences/com.apple.TimeMachine.plist.
func ReadTimeMachinePrefs() (TMPrefs, error) {
	path, err := ResolveDomain("com.apple.TimeMachine", false)
	if err != nil {
		return TMPrefs{}, err
	}
	d, err := ReadPath(path)
	if err != nil {
		return TMPrefs{}, err
	}
	return timeMachinePrefsFromValue(d.Root), nil
}

// ReadSoftwareUpdatePrefs reads
// /Library/Preferences/com.apple.SoftwareUpdate.plist.
func ReadSoftwareUpdatePrefs() (SoftwareUpdatePrefs, error) {
	path, err := ResolveDomain("com.apple.SoftwareUpdate", false)
	if err != nil {
		return SoftwareUpdatePrefs{}, err
	}
	d, err := ReadPath(path)
	if err != nil {
		return SoftwareUpdatePrefs{}, err
	}
	return softwareUpdatePrefsFromValue(d.Root), nil
}

// ReadSpotlightPrefs reads /Library/Preferences/com.apple.Spotlight.plist.
func ReadSpotlightPrefs() (SpotlightPrefs, error) {
	path, err := ResolveDomain("com.apple.Spotlight", false)
	if err != nil {
		return SpotlightPrefs{}, err
	}
	d, err := ReadPath(path)
	if err != nil {
		return SpotlightPrefs{}, err
	}
	return spotlightPrefsFromValue(d.Root), nil
}

func timeMachinePrefsFromValue(root Value) TMPrefs {
	return TMPrefs{
		AutoBackup:         boolKey(root, "AutoBackup", "AutoBackupEnabled"),
		Destinations:       dictArrayKey(root, "Destinations"),
		SkipPaths:          stringArrayKey(root, "SkipPaths", "ExcludeByPath", "ExcludedPaths"),
		RequiresACPower:    boolKey(root, "RequiresACPower"),
		PreferencesVersion: intKey(root, "PreferencesVersion"),
	}
}

func softwareUpdatePrefsFromValue(root Value) SoftwareUpdatePrefs {
	return SoftwareUpdatePrefs{
		AutomaticCheckEnabled:            boolKey(root, "AutomaticCheckEnabled"),
		AutomaticDownload:                boolKey(root, "AutomaticDownload"),
		AutomaticallyInstallMacOSUpdates: boolKey(root, "AutomaticallyInstallMacOSUpdates"),
		ConfigDataInstall:                boolKey(root, "ConfigDataInstall"),
		CriticalUpdateInstall:            boolKey(root, "CriticalUpdateInstall"),
		LastAttemptSystemVersion:         stringKey(root, "LastAttemptSystemVersion"),
		LastRecommendedMajorOSBundleID:   stringKey(root, "LastRecommendedMajorOSBundleIdentifier"),
		LastSuccessfulDate:               dateKey(root, "LastSuccessfulDate", "LastSuccessfulDateCheck"),
		LastFullSuccessfulDate:           dateKey(root, "LastFullSuccessfulDate", "LastFullSuccessfulDateCheck"),
		LastRecommendedUpdatesAvailable:  intKey(root, "LastRecommendedUpdatesAvailable"),
		RecommendedUpdates:               dictArrayKey(root, "RecommendedUpdates"),
		PrimaryLanguages:                 stringArrayKey(root, "PrimaryLanguages"),
		PreferencesVersion:               intKey(root, "PreferencesVersion"),
	}
}

func spotlightPrefsFromValue(root Value) SpotlightPrefs {
	return SpotlightPrefs{
		IndexingEnabled:    boolKey(root, "IndexingEnabled", "Enabled"),
		MenuItemHidden:     boolKey(root, "MenuItemHidden"),
		StoresDisabled:     boolKey(root, "StoresDisabled"),
		OrderedItems:       dictArrayKey(root, "orderedItems", "OrderedItems"),
		Exclusions:         stringArrayKey(root, "Exclusions", "PrivacyExclusions"),
		Volumes:            stringArrayKey(root, "Volumes"),
		PreferencesVersion: intKey(root, "PreferencesVersion"),
	}
}

func firstKey(root Value, keys ...string) (Value, bool) {
	for _, key := range keys {
		if v, ok := root.Lookup(key); ok {
			return v, true
		}
	}
	return Value{}, false
}

func boolKey(root Value, keys ...string) bool {
	v, ok := firstKey(root, keys...)
	return ok && v.Kind == Bool && v.Bool
}

func stringKey(root Value, keys ...string) string {
	v, ok := firstKey(root, keys...)
	if !ok || v.Kind != String {
		return ""
	}
	return v.String
}

func intKey(root Value, keys ...string) int {
	v, ok := firstKey(root, keys...)
	if !ok || v.Kind != Int {
		return 0
	}
	return int(v.Int)
}

func dateKey(root Value, keys ...string) time.Time {
	v, ok := firstKey(root, keys...)
	if !ok || v.Kind != Date {
		return time.Time{}
	}
	return v.Date
}

func stringArrayKey(root Value, keys ...string) []string {
	v, ok := firstKey(root, keys...)
	if !ok || v.Kind != Array {
		return nil
	}
	out := make([]string, 0, len(v.Array))
	for _, elem := range v.Array {
		if elem.Kind == String {
			out = append(out, elem.String)
		}
	}
	return out
}

func dictArrayKey(root Value, keys ...string) []map[string]any {
	v, ok := firstKey(root, keys...)
	if !ok || v.Kind != Array {
		return nil
	}
	out := make([]map[string]any, 0, len(v.Array))
	for _, elem := range v.Array {
		if elem.Kind != Dict {
			continue
		}
		m := make(map[string]any, len(elem.Dict))
		for k, child := range elem.Dict {
			m[k] = child.Any()
		}
		out = append(out, m)
	}
	return out
}
