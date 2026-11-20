// Package uninstall plans and applies the removal of a macOS app and the
// files that belong to it. Planning is read-only. Applying acts only on the
// items of a plan file, re-verifies each one, and moves user-owned items to
// the user's Trash; it never deletes, and never touches root-owned or shared
// items, which are reported with the command a user could run instead.
//
// Matching is deliberately conservative: a path is tied to the app only by
// its exact bundle id, a helper bundle id the app declares, the app's
// application-group entitlements, a launchd job that runs from the bundle or
// carries a declared label, or a pkgutil receipt that lists the bundle. A
// folder that merely shares the app's name is reported at low confidence and
// is never acted on unless asked for.
package uninstall

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
)

// PlanSchema identifies the plan file format.
const PlanSchema = "spectra.uninstall.plan/v1"

// Kind classifies a planned item.
type Kind string

// Item kinds.
const (
	KindBundle           Kind = "bundle"
	KindSupport          Kind = "support"
	KindCache            Kind = "cache"
	KindPreferences      Kind = "preferences"
	KindContainer        Kind = "container"
	KindGroupContainer   Kind = "group_container"
	KindSavedState       Kind = "saved_state"
	KindHTTPStorage      Kind = "http_storage"
	KindWebKit           Kind = "webkit"
	KindLogs             Kind = "logs"
	KindCookies          Kind = "cookies"
	KindLaunchAgent      Kind = "launch_agent"
	KindLaunchDaemon     Kind = "launch_daemon"
	KindPrivilegedHelper Kind = "privileged_helper"
	KindLoginItem        Kind = "login_item"
	KindReceipt          Kind = "receipt"
	KindReceiptFile      Kind = "receipt_file"
	KindOther            Kind = "other"
)

// Reason records how an item was tied to the app.
type Reason string

// Match reasons.
const (
	ReasonBundleID         Reason = "bundle_id"
	ReasonTeamID           Reason = "team_id"
	ReasonEntitlementGroup Reason = "entitlement_group"
	ReasonReceipt          Reason = "receipt"
	ReasonHelperOfBundle   Reason = "helper_of_bundle"
	ReasonLaunchdProgram   Reason = "launchd_program_in_bundle"
	ReasonName             Reason = "name"
)

// Confidence levels.
const (
	ConfidenceHigh = "high"
	ConfidenceLow  = "low"
)

// Action is what apply does with an item.
type Action string

// Actions.
const (
	ActionTrash      Action = "trash"
	ActionNeedsRoot  Action = "needs_root"
	ActionReportOnly Action = "report_only"
)

// Item is one path that belongs to the app.
type Item struct {
	Path            string `json:"path"`
	Kind            Kind   `json:"kind"`
	SizeBytes       uint64 `json:"size_bytes"`
	OwnerUID        int64  `json:"owner_uid"`
	RemovableByUser bool   `json:"removable_by_user"`
	Reason          Reason `json:"reason"`
	Shared          bool   `json:"shared"`
	Confidence      string `json:"confidence"`
	Action          Action `json:"action"`
	Symlink         bool   `json:"symlink,omitempty"`
	// Detail is the identifier the match used: bundle id, label, pkgid or group id.
	Detail  string `json:"detail,omitempty"`
	Command string `json:"command,omitempty"`
	Note    string `json:"note,omitempty"`
}

// App is the identity an uninstall plan was built from.
type App struct {
	Path          string   `json:"path"`
	BundleID      string   `json:"bundle_id"`
	Name          string   `json:"name"`
	Version       string   `json:"version,omitempty"`
	TeamID        string   `json:"team_id,omitempty"`
	HelperIDs     []string `json:"helper_bundle_ids"`
	DeclaredJobs  []string `json:"declared_launchd_labels"`
	AppGroups     []string `json:"app_groups"`
	Receipts      []string `json:"receipts"`
	DisplayNames  []string `json:"display_names"`
	BundlePresent bool     `json:"-"`
}

// Process is a running process of the app.
type Process struct {
	PID       int       `json:"pid"`
	Name      string    `json:"name"`
	StartTime time.Time `json:"start_time,omitempty"`
}

// Plan is the read-only result of planning an uninstall.
type Plan struct {
	Schema        string    `json:"schema"`
	CreatedAt     time.Time `json:"created_at"`
	Home          string    `json:"home"`
	UID           int64     `json:"uid"`
	App           App       `json:"app"`
	Items         []Item    `json:"items"`
	Processes     []Process `json:"processes"`
	LaunchdLabels []string  `json:"launchd_labels"`
	Limitations   []string  `json:"limitations"`
}

// Options injects every root path, identity and side-effect seam so the
// planner and applier can run against a fixture tree.
type Options struct {
	Home string
	// Trash is the user's Trash; default Home/.Trash.
	Trash string
	// AppRoots are searched to resolve a bundle id and to find other apps
	// that may share a name or an app group. Default /Applications,
	// /Applications/Utilities and Home/Applications.
	AppRoots []string
	// UserLibrary defaults to Home/Library; SystemLibrary to /Library.
	UserLibrary   string
	SystemLibrary string
	// Receipts is the pkgutil receipt database, default /var/db/receipts.
	Receipts string
	// SystemRoots hold sealed system apps that are never planned (/System).
	SystemRoots []string
	// SystemAppRoots are scanned, never planned, when deciding whether an id
	// or an app group is also used by another installed app. Default
	// /System/Applications and /System/Applications/Utilities.
	SystemAppRoots []string

	UID  int64
	GIDs []int64

	Runner proc.Runner
	FS     FS
	Procs  func(ctx context.Context) []process.Info
	Now    func() time.Time
	Sleep  func(ctx context.Context, d time.Duration)
	// SelfPID is never signalled.
	SelfPID int
	OS      hostos.Kind
}

// DefaultOptions returns options for the real host and current user.
func DefaultOptions() Options {
	home, _ := os.UserHomeDir()
	gids := []int64{int64(os.Getgid())}
	if groups, err := os.Getgroups(); err == nil {
		for _, g := range groups {
			gids = append(gids, int64(g))
		}
	}
	runner := proc.Runner(proc.OS{})
	return Options{
		Home:   home,
		UID:    int64(os.Getuid()),
		GIDs:   gids,
		Runner: runner,
		FS:     OSFS{},
		Procs: func(ctx context.Context) []process.Info {
			return process.CollectAll(ctx, process.CollectOptions{Runner: runner})
		},
		Now:     time.Now,
		SelfPID: os.Getpid(),
	}
}

func (o Options) withDefaults() Options {
	if o.Trash == "" {
		o.Trash = filepath.Join(o.Home, ".Trash")
	}
	if o.AppRoots == nil {
		o.AppRoots = []string{"/Applications", "/Applications/Utilities", filepath.Join(o.Home, "Applications")}
	}
	if o.UserLibrary == "" {
		o.UserLibrary = filepath.Join(o.Home, "Library")
	}
	if o.SystemLibrary == "" {
		o.SystemLibrary = "/Library"
	}
	if o.Receipts == "" {
		o.Receipts = "/var/db/receipts"
	}
	if o.SystemRoots == nil {
		o.SystemRoots = []string{"/System"}
	}
	if o.SystemAppRoots == nil {
		o.SystemAppRoots = []string{"/System/Applications", "/System/Applications/Utilities"}
	}
	if o.Runner == nil {
		o.Runner = proc.OS{}
	}
	if o.FS == nil {
		o.FS = OSFS{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = func(ctx context.Context, d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		}
	}
	if o.Procs == nil {
		o.Procs = func(context.Context) []process.Info { return nil }
	}
	return o
}

func supported(o Options) error {
	if hostos.Resolve(o.OS) != hostos.Darwin {
		return hostos.Unsupported("app uninstall")
	}
	return nil
}

// planLimitations is attached to every plan.
var planLimitations = []string{
	"Only paths tied to the app by bundle id, declared helper ids, app-group entitlements, launchd jobs or pkgutil receipts are planned; nothing is matched by substring.",
	"A declared launchd label, helper id or app group that is not under the app's own bundle id (or its verified team id, for groups) is reported as shared and never acted on.",
	"If another installed app has the same bundle id (ignoring case), every id-derived item is reported as shared.",
	"Files a pkgutil receipt installed are reported only; another tool may have replaced them.",
	"Name-only matches are low confidence and are acted on only with --include-low-confidence.",
	"Items are moved to the Trash, never deleted. Root-owned items are listed with a sudo command and never touched.",
	"Login items registered through Background Task Management (no plist on disk) and ACL-based permissions are not inspected.",
	"Preferences may be rewritten by cfprefsd if the app or a helper is still running.",
}
