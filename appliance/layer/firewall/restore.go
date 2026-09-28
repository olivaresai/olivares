// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package firewall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/firewall/policy"
)

// A restoration's states. pending is recorded before any load; restored when the target loaded, was
// measured and became the confirmed policy; failed when it did not, after the confirmed policy was
// loaded again.
const (
	RestorationPending  = "pending"
	RestorationRestored = "restored"
	RestorationFailed   = "failed"
)

// The closed codes of a restoration, beside the owner's.
const (
	// CodeAnswersInvalid: first boot has not published the console selection, or the published
	// selection is missing, unreadable or gives no valid policy.
	CodeAnswersInvalid = "answers_invalid"
	// CodeSSHPortUnmeasured: sshd did not report the one port the policy admits.
	CodeSSHPortUnmeasured = "ssh_port_unmeasured"
	// CodeLinksUnmeasured: the DHCPv6 client links could not be listed.
	CodeLinksUnmeasured = "links_unmeasured"
	// CodeAlreadyInForce: the target is the confirmed policy.
	CodeAlreadyInForce = "already_in_force"
)

// restorationsName is the directory of the restoration records in StateDir.
const restorationsName = "restorations"

// Restoration is one restoration's record, StateDir/restorations/<operation id>.json, root-owned 0600.
// It is created exclusively before any load and names the target and the policy it replaces; its
// state then closes restored or failed, and a record is never replaced by another operation's.
type Restoration struct {
	OperationID    string `json:"operation_id"`
	BootID         string `json:"boot_id"`
	CreatedAt      string `json:"created_at"`
	TargetDigest   string `json:"target_digest"`
	PreviousDigest string `json:"previous_digest"`
	State          string `json:"state"`
	// Reason is a failed restoration's closed code: load_failed, measurement_failed or
	// state_unreadable.
	Reason string `json:"reason,omitempty"`
	// MeasuredDigest is the policy the kernel was measured to hold when the record closed: the target
	// when restored, the confirmed policy loaded again when failed, and "" when that was not measured
	// either.
	MeasuredDigest string `json:"measured_digest,omitempty"`
}

// Answers are what a restoration derives its target from, read on this host when it runs and never
// sent by its caller. They are first boot's own inputs to the first confirmed policy: whether first
// boot completed generate-product-config, the console selection it published from the validated
// answers, the one SSH port sshd reports and the DHCPv6 client links.
type Answers struct {
	// Published reports whether first boot's record shows generate-product-config completed.
	Published func() (bool, error)
	// Selection reads the published selection. present is false when there is no file; reason names
	// the check a present file failed.
	Selection func() (sel Selection, present bool, reason string)
	// SSHPort measures the one port sshd reports.
	SSHPort func(ctx context.Context) (int, error)
	// Links lists the DHCPv6 client links.
	Links func() ([]string, error)
}

// Target derives a restoration's target as first boot derives the first confirmed policy: Initial of
// the published selection, the measured SSH port and the client links, validated. It carries no
// application row: an application's row returns through that application's own act. A missing or
// invalid input refuses with its closed code, and nothing is derived.
func (a Answers) Target(ctx context.Context) (policy.Document, error) {
	if a.Published == nil || a.Selection == nil {
		return policy.Document{}, refusal(CodeAnswersInvalid, "the appliance answers cannot be read on this host")
	}
	if published, err := a.Published(); err != nil || !published {
		return policy.Document{}, refusal(CodeAnswersInvalid, "first boot has not published the console selection from the validated answers")
	}
	sel, present, reason := a.Selection()
	switch {
	case !present:
		return policy.Document{}, refusal(CodeAnswersInvalid, "the published console selection is missing")
	case reason != "":
		return policy.Document{}, refusal(CodeAnswersInvalid, reason)
	}
	if a.SSHPort == nil {
		return policy.Document{}, refusal(CodeSSHPortUnmeasured, "the operator's SSH port cannot be measured on this host")
	}
	port, err := a.SSHPort(ctx)
	if err != nil {
		return policy.Document{}, refusal(CodeSSHPortUnmeasured, "the operator's SSH port is unmeasured: "+err.Error())
	}
	if a.Links == nil {
		return policy.Document{}, refusal(CodeLinksUnmeasured, "the DHCPv6 client links cannot be listed on this host")
	}
	links, err := a.Links()
	if err != nil {
		return policy.Document{}, refusal(CodeLinksUnmeasured, "the DHCPv6 client links are unmeasured: "+err.Error())
	}
	target := Initial(sel, port, links)
	if err := target.Validate(); err != nil {
		return policy.Document{}, refusal(CodeAnswersInvalid, "the published answers give no valid policy")
	}
	return target, nil
}

// Restore returns the confirmed policy to target, the policy Answers.Target derived from the validated
// appliance answers. It is its own explicit act and not a revert: it opens no window, and it replaces
// a confirmed policy.
//
// Under the network lock, an open window refuses target_locked naming it, because its operator
// reverts it first, and a target that is the confirmed policy refuses already_in_force. Then the
// record is created, exclusively and pending, before any load. The target is loaded and measured,
// becomes the confirmed policy, and the record closes restored with the measured digest. When the
// target does not load or measure, or cannot be confirmed, the confirmed policy is loaded again and
// the record closes failed with the reason: it never reads restored. The same operation id with the
// same target returns its record and loads nothing again; with another target it refuses
// plan_changed.
func (o *Owner) Restore(ctx context.Context, id string, target policy.Document) (Restoration, error) {
	switch {
	case !operationID(id):
		return Restoration{}, refusal(CodeInputRefused, "the operation id is not 32 lowercase hexadecimal digits")
	case target.Validate() != nil:
		return Restoration{}, refusal(CodeInputRefused, "the target does not follow the policy schema")
	case len(target.Apps) != 0:
		return Restoration{}, refusal(CodeInputRefused, "a restoration's target carries no application row")
	}
	digest := policy.Digest(target)
	release, err := o.hold()
	if err != nil {
		return Restoration{}, err
	}
	defer release()
	if err := o.ensureDirs(); err != nil {
		return Restoration{}, err
	}
	if r, found, err := o.readRestoration(id); err != nil {
		return Restoration{}, err
	} else if found {
		if r.TargetDigest != digest {
			return Restoration{}, refusal(CodePlanChanged, "this operation id was restored to another target")
		}
		return r, nil
	}
	windows, err := o.windows()
	if err != nil {
		return Restoration{}, err
	}
	for _, w := range windows {
		if w.State == WindowPending {
			return Restoration{}, &Refusal{Code: CodeTargetLocked, Detail: "a firewall window is open; revert it first", OperationID: w.OperationID}
		}
	}
	confirmed, ok, err := o.Confirmed()
	if err != nil {
		return Restoration{}, err
	}
	if !ok {
		return Restoration{}, refusal(CodeNoConfirmedPolicy, "no policy was confirmed yet; first boot installs the first one")
	}
	previous := policy.Digest(confirmed)
	if digest == previous {
		return Restoration{}, refusal(CodeAlreadyInForce, "the target is the confirmed policy")
	}
	boot, _, err := o.Clock.Now()
	if err != nil {
		return Restoration{}, refusal(CodeStateUnreadable, "this boot's clock cannot be read; nothing was loaded")
	}
	r := Restoration{OperationID: id, BootID: boot, CreatedAt: o.wallNow().UTC().Format(time.RFC3339), TargetDigest: digest,
		PreviousDigest: previous, State: RestorationPending}
	if err := o.createRestoration(r); err != nil {
		return Restoration{}, refusal(CodeStateUnreadable, "the restoration could not be recorded; nothing was loaded")
	}
	m, err := o.load(ctx, target)
	if err == nil {
		err = o.writeConfirmed(target)
	}
	if err != nil {
		r.State, r.Reason = RestorationFailed, codeOf(err)
		if again, reloadErr := o.load(ctx, confirmed); reloadErr == nil {
			r.MeasuredDigest = again.PolicyDigest
		}
		_ = o.writeRestoration(r)
		return r, err
	}
	restored := r
	restored.State, restored.MeasuredDigest = RestorationRestored, m.PolicyDigest
	if err := o.writeRestoration(restored); err != nil {
		return r, refusal(CodeStateUnreadable, "the target is loaded, measured and confirmed, and its result could not be recorded: the record stays pending")
	}
	return restored, nil
}

// wallNow is the time a record states.
func (o *Owner) wallNow() time.Time {
	if o.Wall != nil {
		return o.Wall()
	}
	return time.Now()
}

// codeOf is err's closed code: the refusal's, or load_failed.
func codeOf(err error) string {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Code
	}
	return CodeLoadFailed
}

// createRestoration creates r's record exclusively, root-owned 0600, without following a link: written,
// synced, then its directory synced. A file of that name, a link included, is never replaced.
func (o *Owner) createRestoration(r Restoration) error {
	dir := filepath.Join(o.StateDir, restorationsName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, r.OperationID+".json")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(data, '\n'))
	chmodErr := f.Chmod(0o600)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, chmodErr, syncErr, closeErr); err != nil {
		_ = os.Remove(path)
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// writeRestoration records r's closed state over its record.
func (o *Owner) writeRestoration(r Restoration) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(o.StateDir, restorationsName), r.OperationID+".json", append(data, '\n'), 0o600)
}

// readRestoration reads the record of id; false when there is none. A record that is not a regular
// file, is a link, or does not follow its closed schema is unreadable.
func (o *Owner) readRestoration(id string) (Restoration, bool, error) {
	data, found, err := readBounded(filepath.Join(o.StateDir, restorationsName, id+".json"))
	if err != nil {
		return Restoration{}, false, refusal(CodeStateUnreadable, "a restoration record cannot be read")
	}
	if !found {
		return Restoration{}, false, nil
	}
	var r Restoration
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil || r.OperationID != id || r.TargetDigest == "" || r.PreviousDigest == "" ||
		!slices.Contains([]string{RestorationPending, RestorationRestored, RestorationFailed}, r.State) {
		return Restoration{}, false, refusal(CodeStateUnreadable, "a restoration record does not follow its schema")
	}
	return r, true, nil
}
