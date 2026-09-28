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
	"github.com/olivaresai/olivares/core/license/connectv1"
	"github.com/olivaresai/olivares/core/license/dnfrefresh"
)

// license_connect_dnf.go is `license connect dnf-refresh`: the connect-v1 operation that obtains a DNF
// download credential for one cycle of the appliance helper and hands it over in one file. The rules that
// stand alone — the cycle, the invocation, the answer checks, the outcomes and the handoff writer — are
// core/license/dnfrefresh; this file keeps the pending operation.
//
// For an issued answer the order is: the answer checks, the handoff's atomic publication, then clearing
// the pending operation. A crash before the publication, or between it and the clearing, leaves the
// operation pending; the next cycle repeats it with a fresh challenge and the service answers from its
// stored result — the same credential, never a second mint. The license file, ota.token and the last
// license credential are never written, and no credential is printed.

// dnfHandoffDir is the directory dnf-refresh publishes into: dnfrefresh.HandoffDir, compiled in. No flag,
// environment variable or configuration sets it; only tests replace it.
var dnfHandoffDir = dnfrefresh.HandoffDir

// dnfRefreshTimeout bounds the exchange with the service.
const dnfRefreshTimeout = 60 * time.Second

// dnfRefreshIntent is the pending operation's intent and phase; its challenge operation is
// connectv1.OpDnfRefresh, the same name.
const dnfRefreshIntent = connectv1.OpDnfRefresh

func publishDnfOutcome(cycle string, invocation *string, o dnfrefresh.Outcome, code string, now time.Time, cause error) error {
	err := fmt.Errorf("dnf-refresh %s: %w", o, cause)
	if perr := dnfrefresh.Publish(dnfHandoffDir, dnfrefresh.NotIssued(cycle, invocation, o, code, now)); perr != nil {
		return errors.Join(err, fmt.Errorf("the handoff of cycle %s was not published: %w", cycle, perr))
	}
	return err
}

// dnfRefreshOpenFailed handles a data directory that could not be opened: busy when another command
// holds it, not_bound when it was never connected. Any other failure publishes nothing.
func dnfRefreshOpenFailed(cycle string, err error) error {
	var o dnfrefresh.Outcome
	switch {
	case errors.Is(err, errConnectBusy):
		o = dnfrefresh.OutcomeBusy
	case errors.Is(err, errConnectNoState):
		o = dnfrefresh.OutcomeNotBound
	default:
		return err
	}
	return publishDnfOutcome(cycle, dnfrefresh.Invocation(osGetenv), o, "", time.Now(), err)
}

// dnfRefresh runs the dnf-refresh step for cycle and publishes its handoff. It mirrors apt-refresh:
// the same persisted operation, the bound key as the only signer, and no owner evidence, portal
// session or provider credential. Only an issued answer returns a report; every other outcome returns
// its error after its handoff is published. The credential is never an argument and never logged.
func (r *connectRun) dnfRefresh(ctx context.Context, cycle string) (connectReport, error) {
	invocation := dnfrefresh.Invocation(r.getenv)
	notIssued := func(o dnfrefresh.Outcome, code string, cause error) (connectReport, error) {
		return nil, publishDnfOutcome(cycle, invocation, o, code, r.now(), cause)
	}
	if err := r.requirePendingIntent(dnfRefreshIntent); err != nil {
		return notIssued(dnfrefresh.OutcomePendingOtherOperation, "", err)
	}
	b, err := r.activeBinding()
	if err != nil {
		return notIssued(dnfrefresh.OutcomeNotBound, "", err)
	}
	id, ok, err := r.store.loadIdentity("current")
	if err != nil {
		return notIssued(dnfrefresh.OutcomeIdentityMismatch, "", err)
	}
	if !ok || id.KID != b.PopKID {
		return notIssued(dnfrefresh.OutcomeIdentityMismatch, "", exitcode.New(exitcode.Auth,
			errors.New("this data directory's identity is not the key bound to its deployment: run `olivares license connect recover`")))
	}
	if r.st.Pending == nil {
		// The body is {deployment_id, public_key}, the same shape apt-refresh sends.
		body, err := connectAptRefreshBody(b.DeploymentID, connectv1.EncodePublicKey(id.Public))
		if err != nil {
			return nil, err
		}
		op, err := r.store.newPending(dnfRefreshIntent, dnfRefreshIntent, connectv1.OpDnfRefresh, b.DeploymentID, b.BindingEpoch, "current", false, body)
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
		// The operation ends exactly when the handoff says refused.
		o, code := dnfrefresh.OutcomeOf(err)
		if o == dnfrefresh.OutcomeRefused {
			if serr := r.end(false); serr != nil {
				err = errors.Join(err, serr)
			}
		}
		return notIssued(o, code, err)
	}
	a, err := dnfrefresh.CheckAnswer(obj, dnfrefresh.Binding{DeploymentID: b.DeploymentID, PopKID: b.PopKID,
		BindingEpoch: b.BindingEpoch}, r.now())
	if err != nil {
		return notIssued(dnfrefresh.OutcomeUnknown, "", &connectUnknownError{reason: "the dnf-refresh answer failed its checks", err: err})
	}
	connectStep("dnf-answer-checked")
	if err := dnfrefresh.Publish(dnfHandoffDir, dnfrefresh.Issued(cycle, invocation, a, r.now())); err != nil {
		return nil, fmt.Errorf("publish the handoff of cycle %s (nothing was handed over; the operation stays pending and the next cycle repeats it): %w", cycle, err)
	}
	connectStep("dnf-handoff-published")
	if err := r.end(false); err != nil {
		return nil, fmt.Errorf("the handoff of cycle %s is published but the pending operation was not cleared (the next cycle repeats it and receives the same credential): %w", cycle, err)
	}
	var inv any
	if invocation != nil {
		inv = *invocation
	}
	rep := r.report(string(dnfrefresh.OutcomeIssued))
	rep["dnf"] = map[string]any{"cycle": cycle, "invocation": inv, "handoff": filepath.Join(dnfHandoffDir, cycle+".json"),
		"class": a.Class, "set": a.Set, "suites": a.Suites, "context": a.Context, "expires_at": time.Unix(a.Exp, 0).UTC().Format(time.RFC3339)}
	return rep, nil
}
