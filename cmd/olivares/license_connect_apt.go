// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/license/aptrefresh"
	"github.com/olivaresai/olivares/core/license/connectv1"
)

// license_connect_apt.go is `license connect apt-refresh`: the connect-v1 operation that obtains an APT
// download credential for one cycle of the appliance helper and hands it over in one file (Interface Q3
// r1-r3). The rules that stand alone — the cycle, the invocation, the answer checks, the outcomes and the
// handoff writer — are core/license/aptrefresh; this file keeps the pending operation.
//
// For an issued answer the order is: the answer checks, the handoff's atomic publication, then clearing
// the pending operation. A crash before the publication, or between it and the clearing, leaves the
// operation pending; the next cycle repeats it with a fresh challenge and the service answers from its
// stored result — the same credential, never a second mint. The license file, ota.token and the last
// license credential are never written, and no credential is printed. A kept apt-refresh never blocks
// another operation: every other operation supersedes it (supersedeAptRefresh in license_connect.go).

// aptHandoffDir is the directory apt-refresh publishes into: aptrefresh.HandoffDir, compiled in. No flag,
// environment variable or configuration sets it; only tests replace it.
var aptHandoffDir = aptrefresh.HandoffDir

// aptRefreshTimeout bounds the exchange with the service; the template unit's own limit is 180 s.
const aptRefreshTimeout = 60 * time.Second

// aptRefreshIntent is the pending operation's intent and phase; its challenge operation is
// connectv1.OpAptRefresh, the same name.
const aptRefreshIntent = connectv1.OpAptRefresh

// publishAptOutcome publishes the handoff of an attempt that issued nothing and returns cause, naming the
// outcome. A handoff that cannot be published is reported with it; the helper then finds no result.
func publishAptOutcome(cycle string, invocation *string, o aptrefresh.Outcome, code string, now time.Time, cause error) error {
	err := fmt.Errorf("apt-refresh %s: %w", o, cause)
	if perr := aptrefresh.Publish(aptHandoffDir, aptrefresh.NotIssued(cycle, invocation, o, code, now)); perr != nil {
		return errors.Join(err, fmt.Errorf("the handoff of cycle %s was not published: %w", cycle, perr))
	}
	return err
}

// aptRefreshOpenFailed handles a data directory that could not be opened: busy (row R12) when another
// command holds it, not_bound (row R1) when it was never connected. Any other failure publishes nothing.
func aptRefreshOpenFailed(cycle string, err error) error {
	var o aptrefresh.Outcome
	switch {
	case errors.Is(err, errConnectBusy):
		o = aptrefresh.OutcomeBusy
	case errors.Is(err, errConnectNoState):
		o = aptrefresh.OutcomeNotBound
	default:
		return err
	}
	return publishAptOutcome(cycle, aptrefresh.Invocation(osGetenv), o, "", time.Now(), err)
}

// aptRefresh runs the apt-refresh step for cycle and publishes its handoff. It mirrors refresh: the same
// persisted operation, the bound key as the only signer, and no owner evidence, portal session or
// provider credential. Only an issued answer returns a report; every other outcome returns its error
// after its handoff is published.
func (r *connectRun) aptRefresh(ctx context.Context, cycle string) (connectReport, error) {
	invocation := aptrefresh.Invocation(r.getenv)
	notIssued := func(o aptrefresh.Outcome, code string, cause error) (connectReport, error) {
		return nil, publishAptOutcome(cycle, invocation, o, code, r.now(), cause)
	}
	if err := r.requirePendingIntent(aptRefreshIntent); err != nil {
		return notIssued(aptrefresh.OutcomePendingOtherOperation, "", err)
	}
	b, err := r.activeBinding()
	if err != nil {
		return notIssued(aptrefresh.OutcomeNotBound, "", err)
	}
	id, ok, err := r.store.loadIdentity("current")
	if err != nil {
		return notIssued(aptrefresh.OutcomeIdentityMismatch, "", err)
	}
	if !ok || id.KID != b.PopKID {
		return notIssued(aptrefresh.OutcomeIdentityMismatch, "", exitcode.New(exitcode.Auth,
			errors.New("this data directory's identity is not the key bound to its deployment: run `olivares license connect recover`")))
	}
	if r.st.Pending == nil {
		body, err := connectAptRefreshBody(b.DeploymentID, connectv1.EncodePublicKey(id.Public))
		if err != nil {
			return nil, err
		}
		op, err := r.store.newPending(aptRefreshIntent, aptRefreshIntent, connectv1.OpAptRefresh, b.DeploymentID, b.BindingEpoch, "current", false, body)
		if err != nil {
			return nil, err
		}
		r.st.Pending = op
		if err := r.store.saveState(r.st); err != nil {
			return nil, err
		}
	}
	_, obj, err := r.execute(ctx)
	if err != nil {
		// The operation ends exactly when the handoff says refused. That is settle's rule (a 4xx other
		// than 401) with one fail-safe exception: authority_denied without an R7 reason is unknown and kept.
		o, code := aptrefresh.OutcomeOf(err)
		if o == aptrefresh.OutcomeRefused {
			if serr := r.end(false); serr != nil {
				err = errors.Join(err, serr)
			}
		}
		return notIssued(o, code, err)
	}
	a, err := aptrefresh.CheckAnswer(obj, aptrefresh.Binding{DeploymentID: b.DeploymentID, PopKID: b.PopKID,
		BindingEpoch: b.BindingEpoch}, r.now())
	if err != nil {
		// The operation stays pending: the next cycle repeats it and receives the same stored answer.
		return notIssued(aptrefresh.OutcomeUnknown, "", &connectUnknownError{reason: "the apt-refresh answer failed its checks", err: err})
	}
	connectStep("apt-answer-checked")
	if err := aptrefresh.Publish(aptHandoffDir, aptrefresh.Issued(cycle, invocation, a, r.now())); err != nil {
		return nil, fmt.Errorf("publish the handoff of cycle %s (nothing was handed over; the operation stays pending and the next cycle repeats it): %w", cycle, err)
	}
	connectStep("apt-handoff-published")
	if err := r.end(false); err != nil {
		return nil, fmt.Errorf("the handoff of cycle %s is published but the pending operation was not cleared (the next cycle repeats it and receives the same credential): %w", cycle, err)
	}
	var inv any // null unless valid, as in the handoff
	if invocation != nil {
		inv = *invocation
	}
	rep := r.report(string(aptrefresh.OutcomeIssued))
	rep["apt"] = map[string]any{"cycle": cycle, "invocation": inv, "handoff": filepath.Join(aptHandoffDir, cycle+".json"),
		"class": a.Class, "set": a.Set, "suites": a.Suites, "context": a.Context, "expires_at": time.Unix(a.Exp, 0).UTC().Format(time.RFC3339)}
	return rep, nil
}
