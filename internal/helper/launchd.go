package helper

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/startup"
)

// The launchd mutation methods. See docs/design/helper-launchd-mutation.md.
const (
	MethodLaunchdPrepare = "helper.launchd.system.prepare"
	MethodLaunchdDisable = "helper.launchd.system.disable"
	MethodLaunchdEnable  = "helper.launchd.system.enable"
)

// HelperLabel is the helper's own launchd label; it is never changed
// through the helper.
const HelperLabel = "dev.spectra.helper"

// Fixed system paths. None of them comes from a caller.
const (
	launchDaemonsDir         = "/Library/LaunchDaemons"
	systemLaunchDaemonsDir   = "/System/Library/LaunchDaemons"
	privilegedHelperToolsDir = "/Library/PrivilegedHelperTools"
	applicationsDir          = "/Applications"
	managedPrefsDir          = "/Library/Managed Preferences"
	serviceManagementPlist   = "com.apple.servicemanagement.plist"

	// DenylistDir and DenylistPath hold the root-owned helper denylist.
	DenylistDir  = "/Library/Application Support/Spectra"
	DenylistPath = DenylistDir + "/helper-denylist.plist"

	launchctlBin    = "/bin/launchctl"
	codesignBin     = "/usr/bin/codesign"
	sfltoolBin      = "/usr/bin/sfltool"
	dsmemberutilBin = "/usr/bin/dsmemberutil"
)

const (
	maxPlistBytes   = 1 << 20
	maxLabelScan    = 64 << 20
	maxBTMOutput    = 16 << 20
	maxLabelLen     = 200
	tokenBytes      = 16
	tokenTTL        = 60 * time.Second
	maxLiveTokens   = 4
	launchdDeadline = 150 * time.Second
	commandTimeout  = 60 * time.Second
)

// Refusal reason codes, as in the design's failure-mode table.
const (
	ReasonNotAdmin           = "not_admin"
	ReasonDenylisted         = "denylisted"
	ReasonDenylistInvalid    = "denylist_invalid"
	ReasonAppleLabel         = "apple_label"
	ReasonAppleSigned        = "apple_signed"
	ReasonSystemPath         = "system_path"
	ReasonSymlink            = "symlink"
	ReasonPlistPermissions   = "plist_permissions"
	ReasonLabelFileMismatch  = "label_file_mismatch"
	ReasonDuplicateLabel     = "duplicate_label"
	ReasonUnreadablePlist    = "unreadable_plist"
	ReasonUnknownLabel       = "unknown_label"
	ReasonBundleProgram      = "bundle_program"
	ReasonProgramNotInBundle = "program_not_in_bundle"
	ReasonProgramWritable    = "program_writable"
	ReasonUnsigned           = "unsigned"
	ReasonTeamMismatch       = "team_mismatch"
	ReasonLaunchdMismatch    = "launchd_mismatch"
	ReasonManaged            = "managed"
	ReasonManagementUnknown  = "management_unknown"
	ReasonTokenInvalid       = "token_invalid"
	ReasonTokenExpired       = "token_expired"
	ReasonTargetChanged      = "target_changed"
)

// Outcome values of a change.
const (
	OutcomeOK       = "ok"
	OutcomeRefused  = "refused"
	OutcomeMismatch = "mismatch"
	OutcomeError    = "error"
)

// LaunchdTarget is what the helper derived for a label from disk and
// codesign.
type LaunchdTarget struct {
	PlistPath   string `json:"plist_path"`
	PlistSHA256 string `json:"plist_sha256"`
	Program     string `json:"program"`
	BundlePath  string `json:"bundle_path"`
	BundleID    string `json:"bundle_id"`
	TeamID      string `json:"team_id"`
}

// LaunchdState is launchd's view of the job.
type LaunchdState struct {
	Loaded bool `json:"loaded"`
	// Running is set only when launchd reports a process; a loaded
	// on-demand job is not running until something triggers it.
	Running  bool   `json:"running"`
	Disabled bool   `json:"disabled"`
	PID      int    `json:"pid"`
	Path     string `json:"path,omitempty"`
	Program  string `json:"program,omitempty"`
}

// LaunchdPrepareResult is the result of helper.launchd.system.prepare.
type LaunchdPrepareResult struct {
	Label     string         `json:"label"`
	Action    string         `json:"action"`
	Refused   bool           `json:"refused"`
	Reason    string         `json:"reason,omitempty"`
	Message   string         `json:"message,omitempty"`
	Suggested []string       `json:"suggested_commands,omitempty"`
	Token     string         `json:"token,omitempty"`
	ExpiresAt string         `json:"expires_at,omitempty"`
	Target    *LaunchdTarget `json:"target,omitempty"`
	Before    *LaunchdState  `json:"before,omitempty"`
	Planned   [][]string     `json:"planned,omitempty"`
}

// LaunchdChangeResult is the result of helper.launchd.system.disable and
// .enable: startup.ChangeResult's shape plus target, via and outcome.
type LaunchdChangeResult struct {
	Action    string            `json:"action"`
	Label     string            `json:"label"`
	Domain    string            `json:"domain"`
	Via       string            `json:"via"`
	Refused   bool              `json:"refused"`
	Reason    string            `json:"reason,omitempty"`
	Message   string            `json:"message,omitempty"`
	Suggested []string          `json:"suggested_commands,omitempty"`
	Target    *LaunchdTarget    `json:"target,omitempty"`
	Commands  []startup.Command `json:"commands"`
	// Skipped are planned commands left out because they were no longer
	// needed (a bootstrap for a job something else loaded meanwhile).
	Skipped  [][]string    `json:"skipped,omitempty"`
	Pending  bool          `json:"pending"`
	Before   *LaunchdState `json:"before,omitempty"`
	After    *LaunchdState `json:"after,omitempty"`
	Mismatch string        `json:"mismatch"`
	Outcome  string        `json:"outcome"`
}

// Refusal is a check that failed: a normal result, not a fault.
type Refusal struct {
	Reason  string
	Message string
}

func (r *Refusal) Error() string { return r.Reason + ": " + r.Message }

func refuse(reason, format string, args ...any) error {
	return &Refusal{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// rpcError carries a specific JSON-RPC error code out of a handler.
type rpcError struct {
	code int
	msg  string
}

func (e *rpcError) Error() string { return e.msg }

const (
	rpcInvalidParams = -32602
	rpcRateLimited   = -32001
)

// LaunchdOptions configures the launchd methods. Zero values pick the real
// host, commands, filesystem, clock and random source.
type LaunchdOptions struct {
	OS     hostos.Kind
	Runner proc.Runner
	FS     LaunchdFS
	Now    func() time.Time
	Rand   io.Reader
	// IsAdmin reports whether uid is in the admin group. uid 0 never
	// reaches it. Default: dsmemberutil checkmembership.
	IsAdmin func(ctx context.Context, uid uint32) (bool, error)
}

type launchdService struct {
	osKind  hostos.Kind
	runner  proc.Runner
	fs      LaunchdFS
	now     func() time.Time
	rand    io.Reader
	isAdmin func(ctx context.Context, uid uint32) (bool, error)
	audit   func(any)

	tokens       *tokenStore
	prepareLimit *slidingLimiter
	changeLimit  *slidingLimiter
	// changeMu allows one mutation in flight across the helper.
	changeMu sync.Mutex
}

func newLaunchdService(opts LaunchdOptions, audit func(any)) *launchdService {
	s := &launchdService{
		osKind: hostos.Resolve(opts.OS), runner: opts.Runner, fs: opts.FS,
		now: opts.Now, rand: opts.Rand, isAdmin: opts.IsAdmin, audit: audit,
		tokens:       newTokenStore(),
		prepareLimit: newSlidingLimiter(30, time.Minute),
		changeLimit:  newSlidingLimiter(10, 10*time.Minute),
	}
	if s.runner == nil {
		s.runner = proc.OS{}
	}
	if s.fs == nil {
		s.fs = osLaunchdFS{}
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.rand == nil {
		s.rand = rand.Reader
	}
	if s.isAdmin == nil {
		s.isAdmin = dsmemberAdmin(s.runner)
	}
	if s.audit == nil {
		s.audit = func(any) {}
	}
	return s
}

// registerLaunchd adds the three launchd methods to d.
func registerLaunchd(d *Dispatcher, opts LaunchdOptions) *launchdService {
	s := newLaunchdService(opts, d.writeAudit)
	d.Register(MethodLaunchdPrepare, s.handlePrepare)
	d.Register(MethodLaunchdDisable, s.handleExecute("disable"))
	d.Register(MethodLaunchdEnable, s.handleExecute("enable"))
	return s
}

type prepareParams struct {
	Label  string `json:"label"`
	Action string `json:"action"`
}

type executeParams struct {
	Token string `json:"token"`
}

// decodeStrict decodes exactly one JSON object with no unknown fields.
func decodeStrict(params json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(params))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data after params")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after params")
	}
	return nil
}

func decodePrepareParams(params json.RawMessage) (prepareParams, error) {
	var p prepareParams
	if err := decodeStrict(params, &p); err != nil {
		return p, &rpcError{rpcInvalidParams, fmt.Sprintf("%s invalid params: %v", MethodLaunchdPrepare, err)}
	}
	if p.Action != "disable" && p.Action != "enable" {
		return p, &rpcError{rpcInvalidParams, fmt.Sprintf("%s: action must be disable or enable", MethodLaunchdPrepare)}
	}
	if !validLabel(p.Label) {
		return p, &rpcError{rpcInvalidParams, fmt.Sprintf("%s: label must be a reverse-DNS id of at most %d bytes", MethodLaunchdPrepare, maxLabelLen)}
	}
	return p, nil
}

func decodeExecuteParams(method string, params json.RawMessage) (executeParams, error) {
	var p executeParams
	if err := decodeStrict(params, &p); err != nil {
		return p, &rpcError{rpcInvalidParams, fmt.Sprintf("%s invalid params: %v", method, err)}
	}
	if p.Token == "" || len(p.Token) > 64 {
		return p, &rpcError{rpcInvalidParams, method + " requires {\"token\":\"...\"}"}
	}
	return p, nil
}

func (s *launchdService) unsupported() error {
	if s.osKind != hostos.Darwin {
		return hostos.Unsupported("helper.launchd.system")
	}
	return nil
}

// authorize requires uid 0 or admin group membership, on every call.
func (s *launchdService) authorize(ctx context.Context, uid uint32) error {
	if uid == 0 {
		return nil
	}
	ok, err := s.isAdmin(ctx, uid)
	switch {
	case err != nil:
		return refuse(ReasonNotAdmin, "changing system daemons needs an administrator account; membership of admin could not be confirmed: %v", err)
	case !ok:
		return refuse(ReasonNotAdmin, "changing system daemons needs an administrator account")
	}
	return nil
}

func (s *launchdService) handlePrepare(uid uint32, params json.RawMessage) (any, error) {
	if err := s.unsupported(); err != nil {
		return nil, err
	}
	p, err := decodePrepareParams(params)
	if err != nil {
		return nil, err
	}
	if !s.prepareLimit.allow(uid, s.now()) {
		return nil, &rpcError{rpcRateLimited, "too many prepare requests; try again in a minute"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), launchdDeadline)
	defer cancel()
	return s.prepare(ctx, uid, p.Label, p.Action)
}

func (s *launchdService) prepare(ctx context.Context, uid uint32, label, action string) (*LaunchdPrepareResult, error) {
	res := &LaunchdPrepareResult{Label: label, Action: action}
	line := auditLine{UID: uid, Event: "launchd.system.prepare", Action: action, Label: label}
	err := s.authorize(ctx, uid)
	var v *verified
	if err == nil {
		v, err = s.verify(ctx, label, action)
	}
	var ref *Refusal
	switch {
	case errors.As(err, &ref):
		res.Refused, res.Reason, res.Message = true, ref.Reason, ref.Message
		res.Suggested = manualCommands(action, label)
		line.Outcome, line.Reason, line.Message = OutcomeRefused, ref.Reason, ref.Message
		s.writeAudit(line)
		return res, nil
	case err != nil:
		line.Outcome, line.Message = OutcomeError, err.Error()
		s.writeAudit(line)
		return nil, err
	}
	token, err := s.newToken()
	if err != nil {
		line.Outcome, line.Message = OutcomeError, err.Error()
		s.writeAudit(line)
		return nil, err
	}
	now := s.now()
	b := v.binding(uid, action, now)
	s.tokens.put(tokenKey(token), b, now)
	target, before := v.target, v.state
	res.Token = token
	res.ExpiresAt = b.expires.UTC().Format(time.RFC3339)
	res.Target, res.Before, res.Planned = &target, &before, v.planned
	line.TokenHash = tokenHashPrefix(token)
	line.setTarget(&target)
	line.Before, line.Planned, line.Outcome = &before, v.planned, OutcomeOK
	s.writeAudit(line)
	return res, nil
}

func (s *launchdService) newToken() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := io.ReadFull(s.rand, buf); err != nil {
		return "", fmt.Errorf("token: read random: %w", err)
	}
	return encodeToken(buf), nil
}

func (s *launchdService) handleExecute(action string) HandlerFunc {
	method := MethodLaunchdDisable
	if action == "enable" {
		method = MethodLaunchdEnable
	}
	return func(uid uint32, params json.RawMessage) (any, error) {
		if err := s.unsupported(); err != nil {
			return nil, err
		}
		p, err := decodeExecuteParams(method, params)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), launchdDeadline)
		defer cancel()
		return s.execute(ctx, uid, action, p.Token)
	}
}

func (s *launchdService) execute(ctx context.Context, uid uint32, action, token string) (*LaunchdChangeResult, error) {
	// The token is gone before anything else happens, whatever the outcome.
	b, found := s.tokens.take(tokenKey(token))
	res := &LaunchdChangeResult{Action: action, Domain: "system", Via: "spectra-helper", Commands: []startup.Command{}}
	line := auditLine{UID: uid, Event: "launchd.system." + action, TokenHash: tokenHashPrefix(token), Action: action}
	now := s.now()
	if !s.changeLimit.allow(uid, now) {
		line.Outcome, line.Message = OutcomeError, "rate limit exceeded"
		s.writeAudit(line)
		return nil, &rpcError{rpcRateLimited, "too many changes; try again in 10 minutes"}
	}
	if !s.changeMu.TryLock() {
		line.Outcome, line.Message = OutcomeError, "another change is in progress"
		s.writeAudit(line)
		return nil, &rpcError{rpcRateLimited, "another system daemon change is in progress; try again shortly"}
	}
	defer s.changeMu.Unlock()

	err := s.authorize(ctx, uid)
	if err == nil {
		err = checkBinding(b, found, uid, action, now)
	}
	var v *verified
	if err == nil {
		res.Label, line.Label = b.facts.label, b.facts.label
		v, err = s.verify(ctx, b.facts.label, action)
	}
	if err == nil && v.facts() != b.facts {
		err = refuse(ReasonTargetChanged, "the daemon, its files or launchd's state changed since the change was prepared; run the command again to see the new plan")
	}
	var ref *Refusal
	switch {
	case errors.As(err, &ref):
		res.Refused, res.Reason, res.Message, res.Outcome = true, ref.Reason, ref.Message, OutcomeRefused
		if res.Label != "" {
			res.Suggested = manualCommands(action, res.Label)
		}
		line.Outcome, line.Reason, line.Message = OutcomeRefused, ref.Reason, ref.Message
		s.writeAudit(line)
		return res, nil
	case err != nil:
		line.Outcome, line.Message = OutcomeError, err.Error()
		s.writeAudit(line)
		return nil, err
	}
	target, before := v.target, v.state
	res.Target, res.Before = &target, &before
	line.setTarget(&target)
	s.run(ctx, res, v)
	line.Commands, line.Outcome, line.Reason, line.Mismatch, line.Message = res.Commands, res.Outcome, res.Reason, res.Mismatch, res.Message
	s.writeAudit(line)
	return res, nil
}

// checkBinding validates a looked-up token against the caller and method.
func checkBinding(b binding, found bool, uid uint32, action string, now time.Time) error {
	switch {
	case !found:
		return refuse(ReasonTokenInvalid, "the confirmation token is unknown or was already used; run the command again")
	case b.uid != uid:
		return refuse(ReasonTokenInvalid, "the confirmation token belongs to another user; run the command again")
	case b.facts.action != action:
		return refuse(ReasonTokenInvalid, "the confirmation token was prepared for %s, not %s; run the command again", b.facts.action, action)
	case !now.Before(b.expires):
		return refuse(ReasonTokenExpired, "the confirmation token expired; run the command again")
	}
	return nil
}

// manualCommands are the sudo launchctl commands for an administrator to
// run by hand. The plist path is the fixed one for the label.
func manualCommands(action, label string) []string {
	plist, ok := plistPathFor(label)
	if !ok {
		return nil
	}
	return startup.SystemManualCommands(action, label, plist)
}

// auditLine is one launchd audit record. It never carries the token.
type auditLine struct {
	Time        string            `json:"time"`
	UID         uint32            `json:"uid"`
	Event       string            `json:"event"`
	TokenHash   string            `json:"token_hash,omitempty"`
	Action      string            `json:"action,omitempty"`
	Label       string            `json:"label,omitempty"`
	Plist       string            `json:"plist,omitempty"`
	PlistSHA256 string            `json:"plist_sha256,omitempty"`
	Program     string            `json:"program,omitempty"`
	BundlePath  string            `json:"bundle_path,omitempty"`
	BundleID    string            `json:"bundle_id,omitempty"`
	TeamID      string            `json:"team_id,omitempty"`
	Before      *LaunchdState     `json:"before,omitempty"`
	Planned     [][]string        `json:"planned,omitempty"`
	Commands    []startup.Command `json:"commands,omitempty"`
	Outcome     string            `json:"outcome"`
	Reason      string            `json:"reason,omitempty"`
	Message     string            `json:"message,omitempty"`
	Mismatch    string            `json:"mismatch,omitempty"`
}

func (l *auditLine) setTarget(t *LaunchdTarget) {
	l.Plist, l.PlistSHA256, l.Program = t.PlistPath, t.PlistSHA256, t.Program
	l.BundlePath, l.BundleID, l.TeamID = t.BundlePath, t.BundleID, t.TeamID
}

func (s *launchdService) writeAudit(l auditLine) {
	l.Time = s.now().UTC().Format(time.RFC3339Nano)
	s.audit(l)
}

func (s *launchdService) warn(event, msg string) {
	s.writeAudit(auditLine{Event: event, Outcome: "warning", Message: msg})
}

// cmdEnv is the fixed environment for every command the launchd methods
// run, so output parsing does not depend on root's locale.
var cmdEnv = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C"}

func (s *launchdService) runCmd(ctx context.Context, name string, args ...string) (proc.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	return s.runner.Run(ctx, proc.Cmd{Name: name, Args: args, Env: cmdEnv})
}

func joinOutput(r proc.Result) string {
	return strings.TrimSpace(string(r.Stderr) + "\n" + string(r.Stdout))
}
