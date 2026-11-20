// Package startup lists what launches at login or boot on macOS, one row per
// item, merged from launchd plists, launchctl's disabled and loaded state,
// and Background Task Management (the Login Items list in System Settings),
// and changes user-scope launchd items with launchctl.
package startup

import "time"

// Scope says who an item belongs to and therefore what it takes to change it.
type Scope string

const (
	// ScopeUser is an item in ~/Library/LaunchAgents or a per-user BTM item:
	// the user can change it without root.
	ScopeUser Scope = "user"
	// ScopeSystem is an item installed for every user (/Library/LaunchAgents,
	// /Library/LaunchDaemons, /System/Library/...): changing it needs root.
	ScopeSystem Scope = "system"
)

// Trigger values summarise when launchd starts a job.
const (
	TriggerLogin     = "login"      // RunAtLoad in a LaunchAgent
	TriggerBoot      = "boot"       // RunAtLoad in a LaunchDaemon
	TriggerKeepAlive = "keep_alive" // KeepAlive: relaunched whenever it exits (or on a condition)
	TriggerInterval  = "interval"   // StartInterval
	TriggerCalendar  = "calendar"   // StartCalendarInterval
	TriggerOnDemand  = "on_demand"  // none of the above: started by a socket, Mach port, path or by hand
)

// Kind is where an item comes from.
const (
	KindLaunchAgent  = "launch_agent"
	KindLaunchDaemon = "launch_daemon"
	// KindLoginItem is a BTM item with no launchd plist in the scanned
	// directories: an app opened at login, or an SMAppService job embedded in
	// an app bundle.
	KindLoginItem = "login_item"
)

// Source names, used in Item.Sources and Report.Sources.
const (
	SourcePlist         = "launchd_plist"
	SourcePrintDisabled = "launchctl_print_disabled"
	SourceLaunchctl     = "launchctl_print"
	SourceBTM           = "btm"
	// SourceBTMStore is BTM read from the store file, without root.
	SourceBTMStore = "btm_store"
	SourceOwner    = "owner"
)

// Item is one thing that launches at login or boot.
type Item struct {
	Label string `json:"label"`
	// Name is the display name BTM shows in System Settings, when known.
	Name  string `json:"name,omitempty"`
	Kind  string `json:"kind"`
	Scope Scope  `json:"scope"`
	// Domain is the launchd domain the job loads into: gui/<uid> or system.
	Domain           string   `json:"domain,omitempty"`
	PlistPath        string   `json:"plist_path,omitempty"`
	PlistOwner       string   `json:"plist_owner,omitempty"`
	Program          string   `json:"program,omitempty"`
	ProgramArguments []string `json:"program_arguments,omitempty"`
	// Trigger is the dominant start condition; Triggers lists all of them.
	Trigger   string   `json:"trigger,omitempty"`
	Triggers  []string `json:"triggers,omitempty"`
	RunAtLoad bool     `json:"run_at_load,omitempty"`
	// Disabled is the effective state: the launchctl override when one
	// exists, else the plist Disabled key, else BTM's disposition.
	Disabled   bool   `json:"disabled"`
	DisabledBy string `json:"disabled_by,omitempty"`
	Loaded     bool   `json:"loaded"`
	Running    bool   `json:"running"`
	PID        int    `json:"pid,omitempty"`
	LastExit   string `json:"last_exit,omitempty"`
	Apple      bool   `json:"apple,omitempty"`
	Owner      *Owner `json:"owner,omitempty"`
	BTM        *BTM   `json:"btm,omitempty"`
	// Sources lists which inputs contributed to this row.
	Sources []string `json:"sources"`
	// DuplicateOf lists the other plists defining the same label in the same
	// domain. launchd loads one job per label, so loaded and disabled state
	// on these rows cannot be told apart.
	DuplicateOf []string `json:"duplicate_of,omitempty"`

	assoc    []string // AssociatedBundleIdentifiers, for owner attribution only
	appleDir bool     // the plist is under /System/Library
}

// Owner is the app an item belongs to.
type Owner struct {
	App        string `json:"app,omitempty"`
	BundlePath string `json:"bundle_path,omitempty"`
	BundleID   string `json:"bundle_id,omitempty"`
	TeamID     string `json:"team_id,omitempty"`
	Developer  string `json:"developer,omitempty"`
	// Via says how the owner was found: program_in_bundle,
	// associated_bundle_identifier, bundle_id_prefix, or btm.
	Via string `json:"via"`
}

// BTM is what Background Task Management records for an item.
type BTM struct {
	Identifier       string   `json:"identifier"`
	Type             string   `json:"type"`
	Disposition      []string `json:"disposition,omitempty"`
	URL              string   `json:"url,omitempty"`
	ExecutablePath   string   `json:"executable_path,omitempty"`
	BundleID         string   `json:"bundle_id,omitempty"`
	TeamID           string   `json:"team_id,omitempty"`
	DeveloperName    string   `json:"developer_name,omitempty"`
	ParentIdentifier string   `json:"parent_identifier,omitempty"`
	AssocBundleIDs   []string `json:"assoc_bundle_ids,omitempty"`
	// UID is the BTM record the item came from: a user's uid, or 0 / -2
	// for records kept for the whole machine.
	UID int `json:"uid"`
}

// SourceStatus reports whether one input could be read.
type SourceStatus struct {
	Name string `json:"name"`
	// Status is ok, skipped, needs_root, or error.
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Source status values.
const (
	StatusOK        = "ok"
	StatusSkipped   = "skipped"
	StatusNeedsRoot = "needs_root"
	StatusError     = "error"
)

// PlistError is a plist that could not be read or parsed; it is reported,
// not fatal.
type PlistError struct {
	Path  string `json:"path"`
	Error string `json:"error"`
	// Label is what a streaming scan read from a plist too large to parse.
	Label string `json:"label,omitempty"`

	gui   bool // in a LaunchAgents directory: its jobs load into gui/<uid>
	inert bool // empty, or a dict with no Label: launchd loads no job from it
}

// Report is the result of List.
type Report struct {
	UID         int            `json:"uid"`
	Items       []Item         `json:"items"`
	Sources     []SourceStatus `json:"sources"`
	Errors      []PlistError   `json:"errors,omitempty"`
	CollectedAt time.Time      `json:"collected_at"`
}
