package startup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

// LoginItemsSettingsURL opens System Settings > General > Login Items.
const LoginItemsSettingsURL = "x-apple.systempreferences:com.apple.LoginItems-Settings.extension"

// Refusal reasons.
const (
	RefuseScope     = "unsupported_scope"
	RefuseApple     = "apple_label"
	RefuseAppleDir  = "apple_plist"
	RefuseDuplicate = "duplicate_label"
	RefuseUnknown   = "unknown_label"
	RefuseSystem    = "system_scope"
	RefuseBTMOnly   = "btm_only"
)

// Command is one launchctl invocation a change ran.
type Command struct {
	Argv     []string `json:"argv"`
	ExitCode int      `json:"exit_code"`
	Stderr   string   `json:"stderr,omitempty"`
}

// ChangeResult reports a disable or enable: what ran and the state re-read
// afterwards, or why nothing ran.
type ChangeResult struct {
	Action  string `json:"action"`
	Label   string `json:"label"`
	Domain  string `json:"domain,omitempty"`
	Refused bool   `json:"refused"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	// Suggested are commands for the user to run themselves (root, Apple
	// items, or System Settings for BTM items); Spectra never runs them.
	Suggested []string  `json:"suggested_commands,omitempty"`
	Commands  []Command `json:"commands"`
	// Pending is set when launchctl bootout reported the job is still
	// stopping, so After may still show it loaded.
	Pending bool  `json:"pending,omitempty"`
	Before  *Item `json:"before,omitempty"`
	After   *Item `json:"after,omitempty"`
	// Mismatch says how the re-read state differs from what was asked, or
	// is empty when the change took effect.
	Mismatch string `json:"mismatch,omitempty"`
}

// Disable stops a user-scope launchd item from launching:
// `launchctl disable gui/<uid>/<label>`, then `launchctl bootout` when it is
// loaded. scope must be "" or "user".
func Disable(ctx context.Context, opts Options, label, scope string) (ChangeResult, error) {
	return change(ctx, opts, "disable", label, scope)
}

// Enable lets a user-scope launchd item launch again:
// `launchctl enable gui/<uid>/<label>`, then `launchctl bootstrap gui/<uid>
// <plist>` when it is not loaded. Bootstrap loads the job; it starts it only
// when the plist has RunAtLoad, so this undoes a disable for interval,
// calendar and on-demand jobs too.
func Enable(ctx context.Context, opts Options, label, scope string) (ChangeResult, error) {
	return change(ctx, opts, "enable", label, scope)
}

func change(ctx context.Context, opts Options, action, label, scope string) (ChangeResult, error) {
	res := ChangeResult{Action: action, Label: label, Commands: []Command{}}
	if hostos.Resolve(opts.OS) != hostos.Darwin {
		return res, hostos.Unsupported("startup " + action)
	}
	e := newEnv(opts)
	e.opts.Scope = ""
	// Read /System too, so a user plist reusing an Apple job's label is
	// refused rather than acted on as if it were the only job by that name.
	e.opts.IncludeApple = true
	e.ownersFor = label
	btm := e.readBTM(ctx)
	rep, err := e.list(ctx, btm)
	if err != nil {
		return res, err
	}
	// Without launchd's own view every job reads as not loaded and enabled, so
	// a change would skip steps and then report success it can't see.
	if err := launchdReadable(rep); err != nil {
		return res, fmt.Errorf("%s %s: %w", action, label, err)
	}
	item := find(rep.Items, label, e.guiDomain())
	if item != nil {
		res.Domain = item.Domain
		res.Before = item
	}
	if e.refuse(&res, item, plistRows(rep.Items, label), scope) {
		return res, nil
	}
	mutErr := e.mutate(ctx, &res, *item)
	after, err := e.list(ctx, btm)
	if err != nil {
		return res, errors.Join(mutErr, fmt.Errorf("re-read after %s: %w", action, err))
	}
	if err := launchdReadable(after); err != nil {
		return res, errors.Join(mutErr, fmt.Errorf("re-read after %s: %w", action, err))
	}
	res.After = find(after.Items, label, item.Domain)
	res.Mismatch = res.verify()
	return res, mutErr
}

func launchdReadable(rep Report) error {
	for _, s := range rep.Sources {
		if (s.Name == SourceLaunchctl || s.Name == SourcePrintDisabled) && s.Status != StatusOK {
			return fmt.Errorf("can't read launchd state (%s: %s %s)", s.Name, s.Status, s.Detail)
		}
	}
	return nil
}

// find prefers the user's GUI-domain row when a label exists in several.
func find(items []Item, label, gui string) *Item {
	var hit *Item
	for i := range items {
		if items[i].Label != label {
			continue
		}
		if hit == nil || (items[i].Domain == gui && items[i].Scope == ScopeUser) {
			it := items[i]
			hit = &it
		}
	}
	return hit
}

// plistRows are the rows for label that come from a launchd plist.
func plistRows(items []Item, label string) []Item {
	var out []Item
	for _, it := range items {
		if it.Label == label && it.PlistPath != "" {
			out = append(out, it)
		}
	}
	return out
}

// verify compares the re-read state with what the change asked for.
func (r ChangeResult) verify() string {
	a := r.After
	switch {
	case a == nil:
		return "the item was not found when re-read after the change"
	case r.Action == "disable" && !a.Disabled:
		return "launchctl still reports the item enabled"
	case r.Action == "disable" && a.Loaded && !r.Pending:
		return "the item is still loaded"
	case r.Action == "enable" && a.Disabled:
		return "launchctl still reports the item disabled"
	case r.Action == "enable" && !a.Loaded:
		return "the item is not loaded"
	}
	return ""
}

// refuse fills res and returns true when the change must not run.
func (e env) refuse(res *ChangeResult, item *Item, copies []Item, scope string) bool {
	switch {
	case scope != "" && scope != string(ScopeUser):
		return res.refuse(RefuseScope, fmt.Sprintf("only --scope user can be changed by spectra; %s-scope items need root", scope))
	case isApple(res.Label):
		return res.refuse(RefuseApple, fmt.Sprintf("refusing to %s Apple item %s: macOS components can break when disabled; run the command yourself if you are sure", res.Action, res.Label),
			manualCommands(res.Action, item, e.root)...)
	}
	if refuseCopies(res, copies) {
		return true
	}
	switch {
	case item == nil:
		return e.refuseUnknown(res)
	case item.PlistPath == "":
		return res.refuse(RefuseBTMOnly, fmt.Sprintf("%s is a Background Task Management login item with no launchd plist; change it in System Settings > General > Login Items", res.Label),
			`open "`+LoginItemsSettingsURL+`"`)
	case item.Scope != ScopeUser:
		return res.refuse(RefuseSystem, e.systemMessage(res.Label, item), manualCommands(res.Action, item, e.root)...)
	}
	return false
}

func (r *ChangeResult) refuse(reason, msg string, suggested ...string) bool {
	r.Refused, r.Reason, r.Message, r.Suggested = true, reason, msg, suggested
	return true
}

// refuseCopies refuses a label defined under /System or by more than one
// plist: launchd runs one job per label, so spectra cannot tell which file
// a change would act on.
func refuseCopies(res *ChangeResult, copies []Item) bool {
	paths := make([]string, 0, len(copies))
	for _, c := range copies {
		paths = append(paths, c.PlistPath)
	}
	for _, c := range copies {
		if c.appleDir {
			return res.refuse(RefuseAppleDir, fmt.Sprintf("refusing to %s %s: a macOS job with this label is defined in %s (plists: %s)",
				res.Action, res.Label, c.PlistPath, strings.Join(paths, ", ")))
		}
	}
	if len(copies) > 1 {
		return res.refuse(RefuseDuplicate, fmt.Sprintf("refusing to %s %s: the label is defined by %d plists (%s) and launchd loads only one; remove or rename the extra plist first",
			res.Action, res.Label, len(copies), strings.Join(paths, ", ")))
	}
	return false
}

func (e env) refuseUnknown(res *ChangeResult) bool {
	if e.root {
		return res.refuse(RefuseUnknown, fmt.Sprintf("no startup item labelled %q; run `spectra startup list` to see labels", res.Label))
	}
	return res.refuse(RefuseUnknown, fmt.Sprintf("no launchd plist labelled %q. It may be a Login Items entry: those are read only as root (`sudo spectra startup list`) and changed in System Settings > General > Login Items", res.Label),
		`open "`+LoginItemsSettingsURL+`"`)
}

func (e env) systemMessage(label string, item *Item) string {
	switch {
	case item.Domain != "system":
		return fmt.Sprintf("%s is installed for every user (%s); spectra changes only items in ~/Library/LaunchAgents. To change it for your session, run:", label, item.PlistPath)
	case e.root:
		return fmt.Sprintf("%s is a system-scope item (%s); spectra does not change system-scope items, even as root. Run:", label, item.PlistPath)
	}
	return fmt.Sprintf("%s is a system-scope item (%s); changing it needs root, which spectra does not use. Run:", label, item.PlistPath)
}

// manualCommands are the launchctl commands for the user to run by hand.
// Only the system domain needs sudo, and not when already root: an override
// in gui/<uid> is the user's own.
func manualCommands(action string, item *Item, root bool) []string {
	if item == nil || item.PlistPath == "" {
		return nil
	}
	prefix := ""
	if item.Domain == "system" && !root {
		prefix = "sudo "
	}
	target := item.Domain + "/" + item.Label
	if action == "disable" {
		return []string{
			prefix + "launchctl disable " + shellQuote(target),
			prefix + "launchctl bootout " + shellQuote(target),
		}
	}
	return []string{
		prefix + "launchctl enable " + shellQuote(target),
		prefix + "launchctl bootstrap " + shellQuote(item.Domain) + " " + shellQuote(item.PlistPath),
	}
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`!*?[]{}();&|<>~#") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// mutate runs the launchctl verbs for a validated user-scope item. These
// are the only mutating commands in the package.
func (e env) mutate(ctx context.Context, res *ChangeResult, item Item) error {
	target := e.guiDomain() + "/" + item.Label
	if res.Action == "disable" {
		if err := e.launchctl(ctx, res, "disable", target); err != nil {
			return err
		}
		if item.Loaded {
			return e.bootout(ctx, res, target)
		}
		return nil
	}
	if err := e.launchctl(ctx, res, "enable", target); err != nil {
		return err
	}
	// enable only clears the override; a job disable booted out stays
	// unloaded until bootstrapped, whatever its trigger.
	if !item.Loaded {
		return e.launchctl(ctx, res, "bootstrap", e.guiDomain(), item.PlistPath)
	}
	return nil
}

// bootout unloads target. A job that exited between the listing and the
// bootout is already what was asked for; one launchd is still stopping is
// reported as pending.
func (e env) bootout(ctx context.Context, res *ChangeResult, target string) error {
	cmd, err := e.exec(ctx, res, "bootout", target)
	if err != nil {
		return err
	}
	switch {
	case cmd.ExitCode == 0:
	case cmd.ExitCode == 3 || strings.Contains(cmd.Stderr, "No such process"),
		cmd.ExitCode == 113 || strings.Contains(cmd.Stderr, "Could not find specified service"):
		res.Message = "the job was no longer loaded when bootout ran"
	case cmd.ExitCode == 36 || strings.Contains(cmd.Stderr, "Operation now in progress"):
		res.Pending = true
		res.Message = "bootout is in progress: launchd is still stopping the job, so it may still show as loaded"
	default:
		return exitError(cmd)
	}
	return nil
}

func (e env) launchctl(ctx context.Context, res *ChangeResult, args ...string) error {
	cmd, err := e.exec(ctx, res, args...)
	if err != nil {
		return err
	}
	if cmd.ExitCode != 0 {
		return exitError(cmd)
	}
	return nil
}

// exec runs launchctl and records the command; err is only a failure to run.
func (e env) exec(ctx context.Context, res *ChangeResult, args ...string) (Command, error) {
	out, err := e.runner.Run(ctx, proc.Cmd{Name: launchctlPath, Args: args})
	cmd := Command{Argv: append([]string{launchctlPath}, args...), ExitCode: out.ExitCode, Stderr: strings.TrimSpace(string(out.Stderr))}
	res.Commands = append(res.Commands, cmd)
	if err != nil {
		return cmd, fmt.Errorf("launchctl %s: %w", args[0], err)
	}
	return cmd, nil
}

func exitError(cmd Command) error {
	return fmt.Errorf("launchctl %s exited %d: %s", strings.Join(cmd.Argv[1:], " "), cmd.ExitCode, cmd.Stderr)
}
