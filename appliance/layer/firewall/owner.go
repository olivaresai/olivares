// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package firewall is the host firewall's one owner: the only code that loads the nftables
// table inet olivares, keeps the confirmed policy and publishes what it measured.
//
// A change is confirm-or-revert. Apply records its window before any effect, then loads the
// candidate at runtime; the confirmed policy on disk changes only when the change is confirmed
// before its deadline, in the same boot. The guard reverts an unconfirmed window at its
// deadline, and a boot loads the last confirmed policy and closes the window it interrupted.
// After every load the owner reads the table back from the kernel and publishes the measurement
// the console's probe reads; a load or a read-back that fails withdraws the measurement, so the
// console falls back to loopback rather than trusting a stale one. Every effect runs under the
// network lock, which the NetworkManager plane shares, and never waits for it.
//
// An application's rows change only through that application's own act, which changes nothing
// else; a general change keeps them as they are.
package firewall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/firewall/policy"
	"github.com/olivaresai/olivares/appliance/layer/hostops"
)

// Where the owner keeps its state on an installed appliance.
const (
	// StateDir holds the confirmed policy and the windows, root-owned 0700
	// (StateDirectory=olivares-firewall).
	StateDir = "/var/lib/olivares-firewall"
	// RunDir holds the measurement for this boot, root-owned 0755
	// (RuntimeDirectory=olivares-firewall).
	RunDir = "/run/olivares-firewall"
	// MeasurementFile is the measurement the console's probe reads, root-owned 0644.
	MeasurementFile = RunDir + "/" + measuredName
	// MeasurementSchema is the measurement document's schema.
	MeasurementSchema = "olivares-firewall-measurement/v1"
	// WindowSchema is a window record's schema.
	WindowSchema = "olivares-firewall-window/v1"
)

const (
	confirmedName = "confirmed.json"
	pendingName   = "pending"
	measuredName  = "measured.json"
	// maxStateFile bounds every state file the owner reads back.
	maxStateFile = 64 * 1024
	// maxWindows bounds the windows the owner reads back.
	maxWindows = 256
)

// The revert window: from one to ten minutes, two when the request names none.
const (
	DefaultRevertAfter = 120 * time.Second
	MinRevertAfter     = 60 * time.Second
	MaxRevertAfter     = 600 * time.Second
)

// Kernel loads a rendered ruleset and reports the loaded table.
type Kernel interface {
	// Load loads ruleset as one transaction.
	Load(ctx context.Context, ruleset []byte) error
	// Table reports the table inet olivares as the kernel holds it.
	Table(ctx context.Context) (Table, error)
}

// Table is what the kernel reports of the table inet olivares.
type Table struct {
	// Comment is the table's comment, which carries the loaded policy's digest.
	Comment string
	// InputPolicy and ForwardPolicy are the base chains' default policies.
	InputPolicy   string
	ForwardPolicy string
	// InputRules and ForwardRules count each base chain's rules.
	InputRules   int
	ForwardRules int
}

// Lock is the network lock: one holder at a time, taken without waiting.
type Lock interface {
	Acquire() (release func(), err error)
}

// Clock reports this boot's id and CLOCK_BOOTTIME, which window deadlines use.
type Clock interface {
	Now() (boot string, boottime time.Duration, err error)
}

// Owner is the firewall's owner over its state, the kernel, the network lock and the clock.
type Owner struct {
	// StateDir and RunDir are StateDir and RunDir on an installed appliance.
	StateDir string
	RunDir   string
	Kernel   Kernel
	Lock     Lock
	Clock    Clock
	// Wall is the time a measurement records; time.Now when nil.
	Wall func() time.Time
}

// ApplyRequest is one change: the client's operation id, the whole candidate policy, the
// application whose own act this is ("" for a general change) and the revert window (zero for
// DefaultRevertAfter).
type ApplyRequest struct {
	OperationID string
	Candidate   policy.Document
	App         string
	RevertAfter time.Duration
}

// A window's states.
const (
	// WindowPending: the candidate is loaded at runtime and not confirmed.
	WindowPending = "pending"
	// WindowConfirmed: the candidate is the confirmed policy.
	WindowConfirmed = "confirmed"
	// WindowReverted: the confirmed policy was loaded again and measured.
	WindowReverted = "reverted"
	// WindowFailed: neither the candidate nor the confirmed policy could be measured.
	WindowFailed = "failed"
)

// Why a window was reverted.
const (
	ReasonDeadline   = "deadline"
	ReasonReboot     = "reboot"
	ReasonReload     = "reload"
	ReasonOperator   = "operator"
	ReasonLoadFailed = "load_failed"
)

// Closed codes. Each names what refused and never a value the caller sent.
const (
	CodeInputRefused      = "input_refused"
	CodeTargetLocked      = "target_locked"
	CodePlanChanged       = "plan_changed"
	CodeWindowExpired     = "window_expired"
	CodeWindowConfirmed   = "window_confirmed"
	CodeNoSuchWindow      = "no_such_window"
	CodeNoConfirmedPolicy = "no_confirmed_policy"
	CodeLoadFailed        = "load_failed"
	CodeMeasurementFailed = "measurement_failed"
	CodeAppRowNotOwnAct   = "app_row_not_own_act"
	CodeStateUnreadable   = "state_unreadable"
)

// Window is one confirm-or-revert window, recorded before its effect.
type Window struct {
	SchemaVersion string `json:"schema_version"`
	OperationID   string `json:"operation_id"`
	// App is the application whose own act this is, or "".
	App string `json:"app,omitempty"`
	// BootID and DeadlineNS bound the window: this boot, CLOCK_BOOTTIME nanoseconds.
	BootID          string          `json:"boot_id"`
	DeadlineNS      int64           `json:"deadline_boottime_ns"`
	CandidateDigest string          `json:"candidate_digest"`
	PreviousDigest  string          `json:"previous_digest"`
	Candidate       policy.Document `json:"candidate"`
	State           string          `json:"state"`
	Reason          string          `json:"reason,omitempty"`
	// Postconditions are the measured facts behind the state.
	Postconditions []string `json:"postconditions,omitempty"`
}

// Measurement is the published document: the console's probe reads exactly these members.
type Measurement struct {
	SchemaVersion string               `json:"schema_version"`
	BootID        string               `json:"boot_id"`
	MeasuredAt    string               `json:"measured_at"`
	PolicyDigest  string               `json:"policy_digest"`
	InputPolicy   string               `json:"input_policy"`
	Rows          []policy.MeasuredRow `json:"rows"`
}

// Refusal is a closed refusal. OperationID names the open window of a target_locked refusal.
type Refusal struct {
	Code        string
	Detail      string
	OperationID string
}

func (r *Refusal) Error() string {
	if r.Detail == "" {
		return r.Code
	}
	return r.Code + ": " + r.Detail
}

func refusal(code, detail string) error { return &Refusal{Code: code, Detail: detail} }

// Install loads the confirmed policy, or, when none was confirmed yet, confirms initial first:
// first boot's adapter asks for it once, through the owner's boot unit. It never replaces a
// confirmed policy.
func (o *Owner) Install(ctx context.Context, initial policy.Document) (Measurement, error) {
	if err := initial.Validate(); err != nil {
		return Measurement{}, refusal(CodeInputRefused, err.Error())
	}
	return o.bootLoad(ctx, &initial)
}

// BootLoad loads the last confirmed policy, never a pending one, and closes each window the load
// ended: a window of an earlier boot was ended by the reboot. With no confirmed policy it loads
// nothing, publishes nothing and refuses with no_confirmed_policy.
func (o *Owner) BootLoad(ctx context.Context) (Measurement, error) {
	return o.bootLoad(ctx, nil)
}

func (o *Owner) bootLoad(ctx context.Context, initial *policy.Document) (Measurement, error) {
	release, err := o.hold()
	if err != nil {
		return Measurement{}, err
	}
	defer release()
	if err := o.ensureDirs(); err != nil {
		return Measurement{}, err
	}
	confirmed, ok, err := o.Confirmed()
	if err != nil {
		return Measurement{}, err
	}
	if !ok {
		if initial == nil {
			o.withdraw()
			return Measurement{}, refusal(CodeNoConfirmedPolicy, "no policy was confirmed yet; first boot installs the first one")
		}
		if err := o.writeConfirmed(*initial); err != nil {
			return Measurement{}, err
		}
		confirmed = *initial
	}
	m, err := o.load(ctx, confirmed)
	if err != nil {
		return Measurement{}, err
	}
	windows, err := o.windows()
	if err != nil {
		return m, err
	}
	for _, w := range windows {
		if w.State != WindowPending {
			continue
		}
		reason := ReasonReload
		if w.BootID != m.BootID {
			reason = ReasonReboot
		}
		o.close(&w, WindowReverted, reason, reverted(confirmed))
	}
	return m, nil
}

// Apply records the window, then loads the candidate at runtime; the confirmed policy does not
// change. The same operation id with the same plan returns its window and loads nothing again;
// with another plan it refuses plan_changed. Another open window refuses target_locked naming
// it. A general change must keep the applications' rows; an application's act must change its
// own rows and nothing else. When the candidate does not load or measure, the confirmed policy
// is loaded again and the window records it.
func (o *Owner) Apply(ctx context.Context, r ApplyRequest) (Window, error) {
	after := r.RevertAfter
	if after == 0 {
		after = DefaultRevertAfter
	}
	switch {
	case !operationID(r.OperationID):
		return Window{}, refusal(CodeInputRefused, "the operation id is not 32 lowercase hexadecimal digits")
	case after < MinRevertAfter || after > MaxRevertAfter:
		return Window{}, refusal(CodeInputRefused, "the revert window is from 60 to 600 seconds")
	case r.App != "" && !policy.AppSlug(r.App):
		return Window{}, refusal(CodeInputRefused, "the application is not an application slug")
	}
	if err := r.Candidate.Validate(); err != nil {
		return Window{}, refusal(CodeInputRefused, err.Error())
	}
	digest := policy.Digest(r.Candidate)
	release, err := o.hold()
	if err != nil {
		return Window{}, err
	}
	defer release()
	if err := o.ensureDirs(); err != nil {
		return Window{}, err
	}
	if w, found, err := o.readWindow(r.OperationID); err != nil {
		return Window{}, err
	} else if found {
		if w.CandidateDigest != digest || w.App != r.App {
			return Window{}, refusal(CodePlanChanged, "this operation id was applied with another plan")
		}
		return w, nil
	}
	windows, err := o.windows()
	if err != nil {
		return Window{}, err
	}
	for _, w := range windows {
		if w.State == WindowPending {
			return Window{}, &Refusal{Code: CodeTargetLocked, Detail: "another firewall window is open", OperationID: w.OperationID}
		}
	}
	confirmed, ok, err := o.Confirmed()
	if err != nil {
		return Window{}, err
	}
	if !ok {
		return Window{}, refusal(CodeNoConfirmedPolicy, "no policy was confirmed yet; first boot installs the first one")
	}
	if err := ownAct(confirmed, r.Candidate, r.App); err != nil {
		return Window{}, err
	}
	if digest == policy.Digest(confirmed) {
		return Window{}, refusal(CodeInputRefused, "the candidate is the confirmed policy")
	}
	boot, now, err := o.Clock.Now()
	if err != nil {
		return Window{}, refusal(CodeStateUnreadable, "this boot's clock cannot be read; nothing was loaded")
	}
	w := Window{SchemaVersion: WindowSchema, OperationID: r.OperationID, App: r.App, BootID: boot, DeadlineNS: int64(now + after),
		CandidateDigest: digest, PreviousDigest: policy.Digest(confirmed), Candidate: r.Candidate, State: WindowPending}
	if err := o.writeWindow(w); err != nil {
		return Window{}, refusal(CodeStateUnreadable, "the window could not be recorded; nothing was loaded")
	}
	if _, err := o.load(ctx, r.Candidate); err != nil {
		if _, again := o.load(ctx, confirmed); again == nil {
			o.close(&w, WindowReverted, ReasonLoadFailed, reverted(confirmed))
		} else {
			o.close(&w, WindowFailed, ReasonLoadFailed, nil)
		}
		return w, err
	}
	w.Postconditions = []string{"runtime table " + digest, "confirmed policy unchanged " + w.PreviousDigest}
	if err := o.writeWindow(w); err != nil {
		return w, refusal(CodeStateUnreadable, "the loaded window could not be recorded; the guard reverts it at its deadline")
	}
	return w, nil
}

// Confirm makes a pending window's candidate the confirmed policy, before its deadline and in
// its boot, after reading the table back from the kernel. A confirmed window is returned as it
// is; a closed or expired one refuses window_expired and changes nothing.
func (o *Owner) Confirm(ctx context.Context, id string) (Window, error) {
	w, release, err := o.openWindow(id)
	if err != nil {
		return w, err
	}
	defer release()
	switch w.State {
	case WindowConfirmed:
		return w, nil
	case WindowPending:
	default:
		return w, refusal(CodeWindowExpired, "the window is closed; the confirmed policy is in force")
	}
	boot, now, err := o.Clock.Now()
	if err != nil {
		return w, refusal(CodeStateUnreadable, "this boot's clock cannot be read; nothing changed")
	}
	if boot != w.BootID || int64(now) >= w.DeadlineNS {
		return w, refusal(CodeWindowExpired, "the window's deadline has passed; the guard reverts it")
	}
	m, err := o.measure(ctx, w.Candidate)
	if err != nil {
		return w, err
	}
	if err := o.writeConfirmed(w.Candidate); err != nil {
		return w, err
	}
	o.close(&w, WindowConfirmed, "", []string{"confirmed policy " + w.CandidateDigest, "runtime table " + m.PolicyDigest})
	return w, nil
}

// Revert loads the confirmed policy again for a pending window. A reverted window is returned as
// it is; a confirmed one refuses window_confirmed.
func (o *Owner) Revert(ctx context.Context, id string) (Window, error) {
	w, release, err := o.openWindow(id)
	if err != nil {
		return w, err
	}
	defer release()
	switch w.State {
	case WindowReverted, WindowFailed:
		return w, nil
	case WindowConfirmed:
		return w, refusal(CodeWindowConfirmed, "the window was confirmed; a new change reverses it")
	}
	confirmed, ok, err := o.Confirmed()
	if err != nil {
		return w, err
	}
	if !ok {
		return w, refusal(CodeNoConfirmedPolicy, "no policy was confirmed yet")
	}
	if _, err := o.load(ctx, confirmed); err != nil {
		return w, err
	}
	o.close(&w, WindowReverted, ReasonOperator, reverted(confirmed))
	return w, nil
}

// Sweep is the guard's tick: it reverts each pending window whose deadline passed in this boot,
// and closes each window of an earlier boot, whose reboot loaded the confirmed policy, after
// measuring that the kernel holds it. It takes the network lock only when a window is due, since
// the NetworkManager plane's owner takes that lock without waiting; when the lock is busy it
// does nothing and the next tick tries again.
func (o *Owner) Sweep(ctx context.Context) ([]Window, error) {
	boot, now, err := o.Clock.Now()
	if err != nil {
		return nil, refusal(CodeStateUnreadable, "this boot's clock cannot be read")
	}
	due := func(w Window) bool {
		return w.State == WindowPending && (w.BootID != boot || int64(now) >= w.DeadlineNS)
	}
	windows, err := o.windows()
	if err != nil || !slices.ContainsFunc(windows, due) {
		return nil, err
	}
	if o.Lock == nil {
		return nil, refusal(CodeTargetLocked, "the network lock is unavailable")
	}
	release, err := o.Lock.Acquire()
	if err != nil {
		return nil, nil
	}
	defer release()
	windows, err = o.windows()
	if err != nil {
		return nil, err
	}
	var closed []Window
	for _, w := range windows {
		if !due(w) {
			continue
		}
		confirmed, ok, err := o.Confirmed()
		if err != nil {
			return closed, err
		}
		if !ok {
			return closed, refusal(CodeNoConfirmedPolicy, "a window is open without a confirmed policy")
		}
		reason := ReasonDeadline
		if w.BootID != boot {
			reason = ReasonReboot
		}
		if reason == ReasonDeadline || o.holds(ctx, confirmed) != nil {
			if _, err := o.load(ctx, confirmed); err != nil {
				return closed, err
			}
		}
		o.close(&w, WindowReverted, reason, reverted(confirmed))
		closed = append(closed, w)
	}
	return closed, nil
}

// Guard runs Sweep every tick until ctx ends.
func (o *Owner) Guard(ctx context.Context, tick time.Duration, report func([]Window, error)) {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		closed, err := o.Sweep(ctx)
		if report != nil && (err != nil || len(closed) > 0) {
			report(closed, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Window reads one window. It changes nothing.
func (o *Owner) Window(id string) (Window, error) {
	if !operationID(id) {
		return Window{}, refusal(CodeInputRefused, "the operation id is not 32 lowercase hexadecimal digits")
	}
	w, found, err := o.readWindow(id)
	if err != nil {
		return Window{}, err
	}
	if !found {
		return Window{}, refusal(CodeNoSuchWindow, "no firewall window has this operation id")
	}
	return w, nil
}

// Windows reads every window, in operation id order. It changes nothing.
func (o *Owner) Windows() ([]Window, error) { return o.windows() }

// Confirmed reads the confirmed policy; false when none was confirmed yet.
func (o *Owner) Confirmed() (policy.Document, bool, error) {
	data, found, err := readBounded(filepath.Join(o.StateDir, confirmedName))
	if err != nil {
		return policy.Document{}, false, refusal(CodeStateUnreadable, "the confirmed policy cannot be read")
	}
	if !found {
		return policy.Document{}, false, nil
	}
	d, err := policy.Decode(data)
	if err != nil {
		return policy.Document{}, false, refusal(CodeStateUnreadable, "the confirmed policy does not follow its schema")
	}
	return d, true, nil
}

// Observe maps a window to the operation read model's observation: pending is running with an
// unknown outcome; confirmed is performed with its measured facts; a revert after a load is a
// measured revert (rolled_back); a candidate that never loaded is not performed; and a window
// whose revert could not be measured is a failure that may have changed the host.
func Observe(w Window) hostops.Observation {
	facts := slices.Clone(w.Postconditions)
	switch {
	case w.State == WindowConfirmed:
		return hostops.Observation{P1: "performed", Postconditions: facts}
	case w.State == WindowReverted && w.Reason == ReasonLoadFailed:
		return hostops.Observation{P1: "failed"}
	case w.State == WindowReverted:
		return hostops.Observation{P1: "performed", MeasuredChange: true, RevertDefined: true, RevertMeasured: true, Postconditions: facts}
	case w.State == WindowFailed:
		return hostops.Observation{P1: "failed", MeasuredChange: true}
	}
	return hostops.Observation{P1: "unknown"}
}

// ownAct refuses a candidate that changes an application's rows outside that application's own
// act, and an application's act that changes anything else or nothing.
func ownAct(confirmed, candidate policy.Document, app string) error {
	if app == "" {
		kept := candidate
		kept.Apps = slices.Clone(confirmed.Apps)
		if policy.Digest(kept) != policy.Digest(candidate) {
			return refusal(CodeAppRowNotOwnAct, "an application's rows change only through its own act")
		}
		return nil
	}
	if policy.Digest(without(candidate, app)) != policy.Digest(without(confirmed, app)) {
		return refusal(CodeAppRowNotOwnAct, "an application's act changes its own rows and nothing else")
	}
	if policy.Digest(candidate) == policy.Digest(confirmed) {
		return refusal(CodeAppRowNotOwnAct, "the application's act changes none of its rows")
	}
	return nil
}

// without returns d without the rows of app.
func without(d policy.Document, app string) policy.Document {
	rows := []policy.AppRow{}
	for _, row := range d.Apps {
		if row.App != app {
			rows = append(rows, row)
		}
	}
	d.Apps = rows
	return d
}

// reverted are the facts a measured revert records.
func reverted(confirmed policy.Document) []string {
	digest := policy.Digest(confirmed)
	return []string{"runtime table " + digest, "confirmed policy " + digest}
}

// openWindow validates id, takes the network lock and reads the window. The caller releases.
func (o *Owner) openWindow(id string) (Window, func(), error) {
	if !operationID(id) {
		return Window{}, nil, refusal(CodeInputRefused, "the operation id is not 32 lowercase hexadecimal digits")
	}
	release, err := o.hold()
	if err != nil {
		return Window{}, nil, err
	}
	w, found, err := o.readWindow(id)
	if err != nil {
		release()
		return Window{}, nil, err
	}
	if !found {
		release()
		return Window{}, nil, refusal(CodeNoSuchWindow, "no firewall window has this operation id")
	}
	return w, release, nil
}

// hold takes the network lock without waiting.
func (o *Owner) hold() (func(), error) {
	if o.Lock == nil {
		return nil, refusal(CodeTargetLocked, "the network lock is unavailable")
	}
	release, err := o.Lock.Acquire()
	if err != nil {
		return nil, refusal(CodeTargetLocked, "another owner of the network target holds its lock")
	}
	return release, nil
}

// load renders and loads d, then measures it.
func (o *Owner) load(ctx context.Context, d policy.Document) (Measurement, error) {
	ruleset, err := policy.Render(d)
	if err != nil {
		return Measurement{}, refusal(CodeInputRefused, err.Error())
	}
	if err := o.Kernel.Load(ctx, ruleset); err != nil {
		o.withdraw()
		return Measurement{}, refusal(CodeLoadFailed, "nft did not load the table; the kernel keeps its previous one")
	}
	return o.measure(ctx, d)
}

// measure reads the table back and publishes the measurement of d, or withdraws it.
func (o *Owner) measure(ctx context.Context, d policy.Document) (Measurement, error) {
	table, err := o.Kernel.Table(ctx)
	if err != nil || !matches(table, d) {
		o.withdraw()
		return Measurement{}, refusal(CodeMeasurementFailed, "the kernel's table is not the loaded policy")
	}
	boot, _, err := o.Clock.Now()
	if err != nil {
		o.withdraw()
		return Measurement{}, refusal(CodeMeasurementFailed, "this boot's identity cannot be read")
	}
	wall := time.Now
	if o.Wall != nil {
		wall = o.Wall
	}
	m := Measurement{SchemaVersion: MeasurementSchema, BootID: boot, MeasuredAt: wall().UTC().Format(time.RFC3339),
		PolicyDigest: policy.Digest(d), InputPolicy: table.InputPolicy, Rows: policy.Rows(d)}
	data, err := json.MarshalIndent(m, "", "  ")
	if err == nil {
		err = os.MkdirAll(o.RunDir, 0o755)
	}
	if err == nil {
		err = writeAtomic(o.RunDir, measuredName, append(data, '\n'), 0o644)
	}
	if err != nil {
		o.withdraw()
		return Measurement{}, refusal(CodeMeasurementFailed, "the measurement could not be published")
	}
	return m, nil
}

// holds reports whether the kernel holds d, and publishes its measurement when it does.
func (o *Owner) holds(ctx context.Context, d policy.Document) error {
	_, err := o.measure(ctx, d)
	return err
}

// matches reports whether the kernel's table is d's: its digest, both default drops and its
// rule counts.
func matches(t Table, d policy.Document) bool {
	return t.Comment == policy.TableComment+policy.Digest(d) && t.InputPolicy == "drop" && t.ForwardPolicy == "drop" &&
		t.InputRules == len(policy.InputRules(d)) && t.ForwardRules == len(policy.ForwardRules())
}

// withdraw removes the measurement: unmeasured, never stale.
func (o *Owner) withdraw() { _ = os.Remove(filepath.Join(o.RunDir, measuredName)) }

// close records w's final state and facts. A record that cannot be written leaves the window
// pending on disk, and the guard's next tick closes it again from the kernel's state.
func (o *Owner) close(w *Window, state, reason string, facts []string) {
	w.State, w.Reason, w.Postconditions = state, reason, facts
	_ = o.writeWindow(*w)
}

func (o *Owner) ensureDirs() error {
	if err := os.MkdirAll(filepath.Join(o.StateDir, pendingName), 0o700); err != nil {
		return refusal(CodeStateUnreadable, "the firewall's state directory is unavailable")
	}
	return nil
}

func (o *Owner) writeConfirmed(d policy.Document) error {
	data, err := policy.Canonical(d)
	if err == nil {
		err = writeAtomic(o.StateDir, confirmedName, append(data, '\n'), 0o600)
	}
	if err != nil {
		return refusal(CodeStateUnreadable, "the confirmed policy could not be written")
	}
	return nil
}

func (o *Owner) writeWindow(w Window) error {
	data, err := json.Marshal(w)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(o.StateDir, pendingName), w.OperationID+".json", append(data, '\n'), 0o600)
}

// readWindow reads the window of id; false when there is none.
func (o *Owner) readWindow(id string) (Window, bool, error) {
	data, found, err := readBounded(filepath.Join(o.StateDir, pendingName, id+".json"))
	if err != nil {
		return Window{}, false, refusal(CodeStateUnreadable, "a window cannot be read")
	}
	if !found {
		return Window{}, false, nil
	}
	var w Window
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&w); err != nil || w.SchemaVersion != WindowSchema || w.OperationID != id || w.Candidate.Validate() != nil ||
		!slices.Contains([]string{WindowPending, WindowConfirmed, WindowReverted, WindowFailed}, w.State) {
		return Window{}, false, refusal(CodeStateUnreadable, "a window does not follow its schema")
	}
	return w, true, nil
}

// windows reads at most maxWindows windows, in operation id order.
func (o *Owner) windows() ([]Window, error) {
	entries, err := os.ReadDir(filepath.Join(o.StateDir, pendingName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, refusal(CodeStateUnreadable, "the windows cannot be listed")
	}
	var out []Window
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !operationID(id) {
			continue
		}
		if len(out) == maxWindows {
			return nil, refusal(CodeStateUnreadable, "more windows than the owner reads")
		}
		w, found, err := o.readWindow(id)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, w)
		}
	}
	return out, nil
}

// readBounded reads a regular file without following a link, at most maxStateFile bytes; false
// when it does not exist.
func readBounded(path string) ([]byte, bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, false, errors.New("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxStateFile+1))
	if err != nil || len(data) > maxStateFile {
		return nil, false, errors.New("unreadable or too large")
	}
	return data, true, nil
}

// writeAtomic writes name in dir through a temporary file: written, given mode, synced, renamed
// over name, and the directory synced.
func writeAtomic(dir, name string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, writeErr := f.Write(data)
	chmodErr := f.Chmod(mode)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, chmodErr, syncErr, closeErr); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// operationID reports whether s has an operation id's shape: 32 lowercase hexadecimal digits.
func operationID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if (s[i] < '0' || s[i] > '9') && (s[i] < 'a' || s[i] > 'f') {
			return false
		}
	}
	return true
}
