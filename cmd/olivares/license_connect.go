// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/license/connectv1"
)

// license_connect.go is the connected client's operation engine (connect-v1 contract §6, Root
// direction r115-connect-bc-coordination). Every user operation is a sequence of PERSISTED
// protocol steps:
//
//	bind        request (bind_pending) ──approved replay──▶ complete (bind)
//	refresh     refresh
//	rotate      rotate-key with the old-key proof and the new-key proof
//	recover     request (proposed key) ──approved replay──▶ recover on rotate-key
//	reactivate  request (proposed key) ──approved replay──▶ reactivate on rotate-key
//	delete      delete with the bound PoP, or request ──approved replay──▶ delete with approval
//
// A step is written to state.json before it is sent. A step whose outcome is unknown stays
// pending and is repeated with the same bytes and Idempotency-Key. A definitive refusal ends the
// step. A returned credential is verified against the data directory's license trust, must name
// this deployment and purpose and must confer a current right BEFORE license.key, ota.token or
// the binding change; otherwise the previous credential, token and binary stay in place. A verified
// rotate, recover or reactivate is recorded before its key is promoted (commitKeyTransition).

// connectStepHook, when set, is called at each named durable boundary below. Only the crash and
// write-failure tests set it, in a child process.
var connectStepHook func(boundary string)

func connectStep(boundary string) {
	if connectStepHook != nil {
		connectStepHook(boundary)
	}
}

// connectTransitionDone is the reported status of each completed key transition.
var connectTransitionDone = map[string]string{"rotate": "rotated", "recover": "recovered", "reactivate": "reactivated"}

// connectRun is one invocation over a leased store.
type connectRun struct {
	store           *connectStore
	st              *connectState
	ep              *connectEndpoint
	dataDir         string
	licenseExplicit string
	getenv          func(string) string
	now             func() time.Time
}

// connectNoRightError reports a verified credential that confers no current right.
var errConnectNoRight = errors.New("the returned credential confers no current right")

// connectAcceptError is a returned credential that was not accepted. keep says whether the
// pending step is kept (a trust or consistency failure) or ended (no current right).
type connectAcceptError struct {
	keep bool
	err  error
}

func (e *connectAcceptError) Error() string {
	return "the returned credential was NOT installed; the previous license, token and binary are unchanged: " + e.err.Error()
}

func (e *connectAcceptError) Unwrap() error { return e.err }

// connectExit maps a connect error onto the CLI exit-code contract.
func connectExit(err error) error {
	if err == nil || exitcode.Has(err) {
		return err
	}
	var ref *connectRefusal
	var unk *connectUnknownError
	var acc *connectAcceptError
	switch {
	case errors.As(err, &ref):
		switch {
		case ref.status == http.StatusUnauthorized || ref.status == http.StatusForbidden:
			return exitcode.New(exitcode.Auth, err)
		case ref.status == http.StatusConflict:
			return exitcode.New(exitcode.Conflict, err)
		case ref.status == http.StatusNotFound:
			return exitcode.New(exitcode.NotFound, err)
		case ref.status >= 500:
			return exitcode.New(exitcode.Server, err)
		}
		return exitcode.New(exitcode.Err, err)
	case errors.As(err, &unk):
		return exitcode.New(exitcode.Indeterminate, err)
	case errors.As(err, &acc):
		if errors.Is(err, errConnectNoRight) {
			return exitcode.New(exitcode.Auth, err)
		}
		return exitcode.New(exitcode.Err, err)
	case errors.Is(err, errConnectBusy):
		return exitcode.New(exitcode.Conflict, err)
	case errors.Is(err, errConnectNoState):
		return exitcode.New(exitcode.Usage, err)
	}
	return err
}

// execute sends the pending step once. It counts the attempt durably before sending and never
// ends the step itself: the caller decides from the result.
func (r *connectRun) execute(ctx context.Context) (int, map[string]any, error) {
	if r.st.Completion != nil {
		return 0, nil, fmt.Errorf("%w: a verified %s is recorded; its step is never sent again", errConnectStateUnsafe, r.st.Completion.Intent)
	}
	op := r.st.Pending
	signer, ok, err := r.store.loadIdentity(op.SignerRole)
	if err != nil {
		return 0, nil, err
	}
	if !ok {
		return 0, nil, fmt.Errorf("%w: the %s identity that signs the pending %s step is missing", errConnectStateUnsafe, op.SignerRole, op.Phase)
	}
	var next *connectIdentity
	if op.NewKeyProof {
		n, ok, err := r.store.loadIdentity("next")
		if err != nil {
			return 0, nil, err
		}
		if !ok {
			return 0, nil, fmt.Errorf("%w: the proposed identity for the pending rotation is missing", errConnectStateUnsafe)
		}
		next = &n
	}
	op.Attempts++
	if err := r.store.saveState(r.st); err != nil {
		return 0, nil, fmt.Errorf("record the attempt before sending (nothing was sent): %w", err)
	}
	return r.ep.send(ctx, op, signer, next)
}

// end finishes the pending step (and, when asked, the owner-approval request it belongs to).
func (r *connectRun) end(alsoRequest bool) error {
	r.st.Pending = nil
	if alsoRequest {
		r.st.Request = nil
	}
	return r.store.saveState(r.st)
}

// settle applies the common rule to a failed attempt: a definitive refusal ends the step, anything
// else keeps it. It returns the original error.
func (r *connectRun) settle(err error, alsoRequest bool) error {
	var ref *connectRefusal
	if errors.As(err, &ref) && ref.definitive() {
		if serr := r.end(alsoRequest); serr != nil {
			return errors.Join(err, serr)
		}
	}
	return err
}

// settleApprovedCompletion settles a failed completion of an owner-approved bind, recover or
// reactivate. The service refuses such a completion with 409 quota_exhausted or
// order_form_policy_required before it consumes the owner's approval, and it re-checks the
// approval, the current commercial authority and capacity on every attempt. So that refusal keeps
// the approved request and this exact pending completion (same body, signer, approval and
// Idempotency-Key): running the same command again once the contract has capacity completes with the
// same approval, under a fresh challenge. Nothing is retried here and the approval's lifetime stays
// the service's: an expired approval, a lost authority and every other refusal end as before.
func (r *connectRun) settleApprovedCompletion(err error) error {
	var ref *connectRefusal
	if errors.As(err, &ref) && ref.status == http.StatusConflict &&
		(ref.code == connectv1.ErrQuotaExhausted || ref.code == connectv1.ErrOrderFormPolicyRequired) {
		return fmt.Errorf("%w; the owner's approval and this completion are kept: run the same command again once the contract has deployment capacity", err)
	}
	return r.settle(err, true)
}

// verifyCredential checks a returned credential without installing it.
func (r *connectRun) verifyCredential(a connectCredentialAnswer, wantKID string, wantEpoch int64, wantDeployment string) (license.Verified, error) {
	if wantDeployment != "" && a.deploymentID != wantDeployment {
		return license.Verified{}, &connectAcceptError{keep: true, err: fmt.Errorf("the answer names deployment %s, not %s", a.deploymentID, wantDeployment)}
	}
	if a.popKID != wantKID {
		return license.Verified{}, &connectAcceptError{keep: true, err: errors.New("the answer binds another PoP key than the one that signed")}
	}
	if wantEpoch > 0 && a.bindingEpoch != wantEpoch {
		return license.Verified{}, &connectAcceptError{keep: true, err: fmt.Errorf("the answer carries binding epoch %d, not %d", a.bindingEpoch, wantEpoch)}
	}
	kr, err := licenseKeyringForDataDir(r.dataDir)
	if err != nil {
		return license.Verified{}, &connectAcceptError{keep: true, err: withLicenseTrustAction(err)}
	}
	now := r.now().UTC()
	v, err := kr.Verify(a.credential, now)
	if err != nil {
		return license.Verified{}, &connectAcceptError{keep: true, err: withLicenseTrustAction(err)}
	}
	if !v.IsCredentialV3() {
		return license.Verified{}, &connectAcceptError{keep: true, err: errors.New("a connect-v1 credential is an " + license.CredentialSchemaV3)}
	}
	if v.Credential.Deployment != a.deploymentID || v.Credential.Purpose != r.st.Purpose {
		return license.Verified{}, &connectAcceptError{keep: true, err: fmt.Errorf("the signed credential is for deployment %s (%s), not %s (%s)",
			v.Credential.Deployment, v.Credential.Purpose, a.deploymentID, r.st.Purpose)}
	}
	// The unsigned summary fields must describe the signed document, never replace it.
	if (a.serial != "" && a.serial != v.Credential.Serial) || (a.issueSeq > 0 && a.issueSeq != int64(v.Credential.IssueSeq)) {
		return license.Verified{}, &connectAcceptError{keep: true, err: errors.New("the answer's credential serial or issue sequence is not the signed one")}
	}
	if base, ok := v.Credential.BaseGrant(); a.phase != "" && (!ok || string(base.Phase) != a.phase) {
		return license.Verified{}, &connectAcceptError{keep: true, err: errors.New("the answer's phase is not the signed base grant's phase")}
	}
	switch st := v.Status(now); st {
	case license.StatusValid, license.StatusGrace:
	default:
		return license.Verified{}, &connectAcceptError{keep: false, err: fmt.Errorf("%w (status %s)", errConnectNoRight, st)}
	}
	return v, nil
}

// install persists an accepted credential and token. The license is written first: a crash
// before the state update repeats the same step, and the service returns the same result.
func (r *connectRun) install(v license.Verified, a connectCredentialAnswer, phase string) error {
	if err := writeKeyFile(licenseDataDirPath(r.dataDir), []byte(a.credential+"\n"), 0o600, true, "the installed license"); err != nil {
		return fmt.Errorf("install the verified credential: %w", err)
	}
	// writeKeyFile syncs the staged bytes, not the rename. A key transition records its completion
	// right after this, so the installed credential is made durable first.
	if err := syncDir(r.dataDir); err != nil {
		return fmt.Errorf("install the verified credential: %w", err)
	}
	if err := r.store.writeToken(a.ota); err != nil {
		return fmt.Errorf("persist the download token: %w", err)
	}
	sum := sha256.Sum256([]byte(a.credential))
	now := r.now().UTC()
	r.st.Last = &connectLastResult{Phase: phase, CredentialSerial: v.Credential.Serial, CredentialSHA256: hex.EncodeToString(sum[:]),
		Version: a.version, OTAExp: a.otaExp, EffectiveUntil: connectRefreshBoundary(v, now).UTC().Format(time.RFC3339), At: now.Format(time.RFC3339)}
	return nil
}

// connectRefreshBoundary is the local refresh PLANNING boundary of a verified credential: the
// earliest EffectiveBoundary among the grants that confer a right at now. Credential.ActiveGrants
// already applies each line's phase rule and the base dependency, so no rule is added here. A mixed
// credential therefore plans from its shortest active line — an add-on's provisional lease — and
// never from the base line's later term, which is what the answer's unsigned
// credential_effective_until carries. It is not read: an unsigned summary can neither postpone
// nor move this instant. The boundary changes no right and does not say that every line stays
// valid until then; each line keeps its own. Without an active line it is the base's RightEnds.
func connectRefreshBoundary(v license.Verified, now time.Time) time.Time {
	var boundary time.Time
	if v.IsCredentialV3() {
		for _, g := range v.Credential.ActiveGrants(now) {
			if b := g.EffectiveBoundary(); boundary.IsZero() || b.Before(boundary) {
				boundary = b
			}
		}
	}
	if boundary.IsZero() {
		return v.RightEnds()
	}
	return boundary
}

// acceptOrSettle verifies and installs, ending or keeping the step according to the failure.
func (r *connectRun) acceptOrSettle(obj map[string]any, wantKID string, wantEpoch int64, wantDeployment, phase string, alsoRequest bool) (connectCredentialAnswer, license.Verified, error) {
	a, err := parseCredentialAnswer(obj)
	if err != nil {
		return a, license.Verified{}, err
	}
	v, err := r.verifyCredential(a, wantKID, wantEpoch, wantDeployment)
	if err != nil {
		var acc *connectAcceptError
		if errors.As(err, &acc) && !acc.keep {
			if serr := r.end(alsoRequest); serr != nil {
				return a, v, errors.Join(err, serr)
			}
		}
		return a, v, err
	}
	connectStep("credential-verified")
	if err := r.install(v, a, phase); err != nil {
		return a, v, err
	}
	connectStep("credential-installed")
	return a, v, nil
}

// refuseOverride refuses a credential-installing operation that a higher-precedence license
// source would shadow, before anything is sent.
func (r *connectRun) refuseOverride() error {
	if kind, detail, present := licenseOverridePresent(r.licenseExplicit, r.getenv); present {
		return exitcode.New(exitcode.Usage, fmt.Errorf("refusing: a %s license override (%s) OUTRANKS %s, so a connected credential written there would change nothing the engine reads — remove the override first",
			kind, detail, licenseDataDirPath(r.dataDir)))
	}
	return nil
}

func (r *connectRun) requirePendingIntent(intent string) error {
	if c := r.st.Completion; c != nil && c.Intent != intent {
		return exitcode.New(exitcode.Conflict, fmt.Errorf("a verified %s is recorded in this data directory: re-run `olivares license connect %s` to complete it", c.Intent, connectTransitionCommand(c.Intent)))
	}
	if r.st.Pending != nil && r.st.Pending.Intent != intent {
		return exitcode.New(exitcode.Conflict, fmt.Errorf("a %s operation is pending in this data directory: re-run its command to finish it, or `olivares license connect abandon` to discard it", r.st.Pending.Intent))
	}
	return nil
}

// connectTransitionCommand is the command that completes a key transition intent.
func connectTransitionCommand(intent string) string {
	if intent == "rotate" {
		return "rotate-key"
	}
	return intent
}

func (r *connectRun) scopeTarget() string {
	return "scope:" + r.st.Provider + "/" + r.st.BusinessID + "/" + r.st.HolderID
}

func (r *connectRun) activeBinding() (*connectBinding, error) {
	b := r.st.Binding
	if b == nil || b.Status != "active" {
		return nil, exitcode.New(exitcode.Usage, errors.New("this data directory has no active binding: run `olivares license connect start`, or `license connect recover` / `reactivate`"))
	}
	return b, nil
}

// connectReport is the printed outcome: identifiers and states only, never a credential, token,
// approval reference, private key or Idempotency-Key.
type connectReport map[string]any

func (r *connectRun) report(status string) connectReport {
	out := connectReport{"status": status, "data_dir": r.dataDir, "endpoint": r.st.Endpoint}
	if id, ok, _ := r.store.loadIdentity("current"); ok {
		out["pop_kid"] = id.KID
		out["fingerprint"], _ = connectv1.Fingerprint(id.Public)
	}
	if b := r.st.Binding; b != nil {
		out["deployment_id"], out["binding_epoch"], out["binding_status"] = b.DeploymentID, b.BindingEpoch, b.Status
	}
	if q := r.st.Request; q != nil {
		out["request"] = map[string]any{"operation": q.Operation, "request_id": q.RequestID, "approval_url": q.ApprovalURL}
	}
	if p := r.st.Pending; p != nil {
		out["pending"] = map[string]any{"intent": p.Intent, "phase": p.Phase, "attempts": p.Attempts, "created_at": p.CreatedAt}
	}
	if c := r.st.Completion; c != nil {
		out["completion"] = map[string]any{"intent": c.Intent, "deployment_id": c.DeploymentID, "pop_kid": c.PopKID, "binding_epoch": c.BindingEpoch, "verified_at": c.VerifiedAt}
	}
	if l := r.st.Last; l != nil {
		last := map[string]any{"phase": l.Phase, "credential_serial": l.CredentialSerial, "credential_sha256": l.CredentialSHA256, "at": l.At}
		if l.Version != "" {
			last["version"] = l.Version
		}
		if l.OTAExp > 0 {
			last["ota_expires_at"] = time.Unix(l.OTAExp, 0).UTC().Format(time.RFC3339)
		}
		if l.EffectiveUntil != "" {
			// The refresh PLANNING boundary (connectRefreshBoundary): refresh well before it — the
			// canon's refresh target is 12 hours ahead. It is not the end of every line's right.
			last["effective_until"] = l.EffectiveUntil
		}
		out["last"] = last
	}
	return out
}

// ---- approval requests (bind, recover, reactivate, delete) --------------------------------

// runRequest sends (or repeats) the persisted request step. It returns the approval reference
// once the replay reports the owner's approval; otherwise the request stays pending.
func (r *connectRun) runRequest(ctx context.Context, operation, signerRole, target string) (approvalID string, err error) {
	signer, ok, err := r.store.loadIdentity(signerRole)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%w: the %s identity that made this request is missing", errConnectStateUnsafe, signerRole)
	}
	fingerprint, _ := connectv1.Fingerprint(signer.Public)
	status, obj, err := r.execute(ctx)
	if err != nil {
		return "", r.settle(err, true)
	}
	a, err := r.ep.parseRequestAnswer(status, obj, connectRequestExpect{operation: operation, target: target, fingerprint: fingerprint})
	if err != nil {
		return "", err
	}
	req := r.st.Request
	if req == nil || req.RequestID != a.requestID {
		req = &connectApprovalRequest{Operation: operation, RequestID: a.requestID, TargetDeploymentID: target, SignerRole: signerRole,
			CreatedAt: r.now().UTC().Format(time.RFC3339)}
	}
	if a.approvalURL != "" {
		req.ApprovalURL = a.approvalURL
	}
	r.st.Request = req
	if a.status == "pending" {
		// The request step stays pending on purpose: repeating it after the owner approves is
		// how the client learns of the approval without anyone pasting a reference.
		return "", r.store.saveState(r.st)
	}
	return a.approvalID, nil
}

// beginRequest persists a new request step for operation.
func (r *connectRun) beginRequest(intent, operation, signerRole, targetDeployment, evidence string) error {
	signer, _, err := r.store.ensureIdentity(signerRole)
	if err != nil {
		return err
	}
	body, err := connectRequestBody(connectRequestInput{
		provider: r.st.Provider, businessID: r.st.BusinessID, holderID: r.st.HolderID, licenseID: r.st.LicenseID,
		purpose: r.st.Purpose, parent: r.st.Parent, label: r.st.Label, publicKey: connectv1.EncodePublicKey(signer.Public),
		evidence: evidence, operation: operation, targetDeployment: targetDeployment,
	})
	if err != nil {
		return err
	}
	op, err := r.store.newPending(intent, "request", connectv1.OpBindPending, r.scopeTarget(), 0, signerRole, false, body)
	if err != nil {
		return err
	}
	r.st.Pending, r.st.Request = op, nil
	return r.store.saveState(r.st)
}

// ---- bind -------------------------------------------------------------------------------

func (r *connectRun) bind(ctx context.Context, evidence func() (string, error)) (connectReport, error) {
	if err := r.requirePendingIntent("bind"); err != nil {
		return nil, err
	}
	if r.st.Pending == nil {
		if b := r.st.Binding; b != nil && b.Status == "active" {
			return r.report("bound"), nil
		}
		if err := r.refuseOverride(); err != nil {
			return nil, err
		}
		ev, err := evidence()
		if err != nil {
			return nil, err
		}
		if err := r.beginRequest("bind", connectv1.BindOperationBind, "current", "", ev); err != nil {
			return nil, err
		}
	}
	if r.st.Pending.Phase == "request" {
		approvalID, err := r.runRequest(ctx, connectv1.BindOperationBind, "current", "")
		if err != nil {
			return nil, err
		}
		if approvalID == "" {
			return r.report("approval_pending"), nil
		}
		id, _, err := r.store.loadIdentity("current")
		if err != nil {
			return nil, err
		}
		body, err := connectCompleteBindBody(r.st.Request.RequestID, approvalID, connectv1.EncodePublicKey(id.Public), r.st.Channel)
		if err != nil {
			return nil, err
		}
		op, err := r.store.newPending("bind", "complete", connectv1.OpBind, r.st.Request.RequestID, 0, "current", false, body)
		if err != nil {
			return nil, err
		}
		r.st.Pending = op
		if err := r.store.saveState(r.st); err != nil {
			return nil, err
		}
	}
	id, ok, err := r.store.loadIdentity("current")
	if err != nil || !ok {
		return nil, fmt.Errorf("%w: the bind identity is missing", errConnectStateUnsafe)
	}
	_, obj, err := r.execute(ctx)
	if err != nil {
		return nil, r.settleApprovedCompletion(err)
	}
	a, _, err := r.acceptOrSettle(obj, id.KID, 0, "", "bind", true)
	if err != nil {
		return nil, err
	}
	r.st.Binding = &connectBinding{DeploymentID: a.deploymentID, PopKID: a.popKID, BindingEpoch: a.bindingEpoch, Slot: a.slot, Status: "active"}
	if err := r.end(true); err != nil {
		return nil, err
	}
	return r.report("bound"), nil
}

// ---- refresh ----------------------------------------------------------------------------

func (r *connectRun) refresh(ctx context.Context, channel string) (connectReport, string, error) {
	if err := r.requirePendingIntent("refresh"); err != nil {
		return nil, "", err
	}
	b, err := r.activeBinding()
	if err != nil {
		return nil, "", err
	}
	id, ok, err := r.store.loadIdentity("current")
	if err != nil {
		return nil, "", err
	}
	if !ok || id.KID != b.PopKID {
		return nil, "", exitcode.New(exitcode.Auth, errors.New("this data directory's identity is not the key bound to its deployment: run `olivares license connect recover`"))
	}
	if err := r.refuseOverride(); err != nil {
		return nil, "", err
	}
	if r.st.Pending == nil {
		body, err := connectRefreshBody(b.DeploymentID, channel, connectv1.EncodePublicKey(id.Public))
		if err != nil {
			return nil, "", err
		}
		op, err := r.store.newPending("refresh", "refresh", connectv1.OpRefresh, b.DeploymentID, b.BindingEpoch, "current", false, body)
		if err != nil {
			return nil, "", err
		}
		r.st.Pending = op
		if err := r.store.saveState(r.st); err != nil {
			return nil, "", err
		}
	}
	_, obj, err := r.execute(ctx)
	if err != nil {
		return nil, "", r.settle(err, false)
	}
	a, _, err := r.acceptOrSettle(obj, b.PopKID, b.BindingEpoch, b.DeploymentID, "refresh", false)
	if err != nil {
		return nil, "", err
	}
	if err := r.end(false); err != nil {
		return nil, "", err
	}
	return r.report("refreshed"), a.ota, nil
}

// ---- rotate -----------------------------------------------------------------------------

func (r *connectRun) rotate(ctx context.Context) (connectReport, error) {
	if err := r.requirePendingIntent("rotate"); err != nil {
		return nil, err
	}
	if r.st.Completion != nil {
		return r.completeKeyTransition(r.st)
	}
	b, err := r.activeBinding()
	if err != nil {
		return nil, err
	}
	if err := r.refuseOverride(); err != nil {
		return nil, err
	}
	cur, ok, err := r.store.loadIdentity("current")
	if err != nil {
		return nil, err
	}
	if r.st.Pending == nil {
		if !ok || cur.KID != b.PopKID {
			return nil, exitcode.New(exitcode.Auth, errors.New("rotation needs the currently bound key; without it use `olivares license connect recover`"))
		}
		next, _, err := r.store.ensureIdentity("next")
		if err != nil {
			return nil, err
		}
		body, err := connectRotateBody(b.DeploymentID, connectv1.EncodePublicKey(next.Public), r.st.Channel, connectv1.EncodePublicKey(cur.Public))
		if err != nil {
			return nil, err
		}
		op, err := r.store.newPending("rotate", "rotate", connectv1.OpRotateKey, b.DeploymentID, b.BindingEpoch, "current", true, body)
		if err != nil {
			return nil, err
		}
		r.st.Pending = op
		if err := r.store.saveState(r.st); err != nil {
			return nil, err
		}
	}
	next, ok, err := r.store.loadIdentity("next")
	if err != nil || !ok {
		return nil, fmt.Errorf("%w: the proposed rotation identity is missing", errConnectStateUnsafe)
	}
	// A repeat after a lost or unknown answer is the SAME operation: the same bytes and
	// Idempotency-Key, the epoch it was first signed under, the former key's proof and the proposed
	// key's proof again. The service serves the stored result of a committed rotation; the client
	// substitutes nothing for that replay (B PROTOCOL.md "Retries and unknown completion").
	_, obj, err := r.execute(ctx)
	if err != nil {
		var ref *connectRefusal
		if errors.As(err, &ref) && ref.definitive() && r.st.Pending.Attempts > 1 {
			// An earlier attempt may have committed. The refusal does not prove it did not, so the
			// step and both keys stay until an authenticated outcome says otherwise.
			return nil, fmt.Errorf("%w — an earlier attempt of this rotation may have committed and the service did not serve its result; "+
				"the pending rotation and both keys are kept. To finish with the proposed key: `olivares license connect abandon --yes`, "+
				"then `olivares license connect recover` with the owner's approval", err)
		}
		return nil, r.settle(err, false)
	}
	return r.finishRotation(obj, next, b)
}

func (r *connectRun) finishRotation(obj map[string]any, next connectIdentity, b *connectBinding) (connectReport, error) {
	a, _, err := r.acceptOrSettle(obj, next.KID, b.BindingEpoch+1, b.DeploymentID, "rotate", false)
	if err != nil {
		return nil, err
	}
	return r.commitKeyTransition("rotate", a, b.Slot)
}

// commitKeyTransition finishes a rotate, recover or reactivate whose answer verified and whose
// credential install has written. The durable order, and what a restart finds after each step:
//
//  1. license.key (data directory synced) and ota.token hold the verified answer. state.json still
//     names the pending step and both identity files exist: re-running the command repeats the SAME
//     step — bytes, Idempotency-Key, original epoch and proofs — and the service serves its stored
//     result.
//  2. state.json is published (temp, fsync, rename, directory fsync) with the completion record and
//     the pending step still in it. THIS IS THE COMMIT POINT: from here nothing is sent again, and
//     re-running the command completes the transition locally. A write that fails before its rename
//     leaves step 1; one whose rename is visible but whose directory sync failed leaves a record that
//     is NOT confirmed. Either way no key is promoted, and completeKeyTransition publishes a record
//     again, in the same call, before every promotion — also for a record a later run loads.
//  3. identity.next.key is renamed over identity.key and the directory is synced (promoteIdentity).
//  4. state.json is published with the new binding and without the completion, the pending step or,
//     for recover and reactivate, the owner-approval request.
//
// Between 2 and 4 every other command refuses with the command that completes the transition, and
// abandon refuses to discard it. The test boundaries are credential-verified and credential-installed
// (inside acceptOrSettle, before and after step 1), transition-recorded and transition-promoted.
func (r *connectRun) commitKeyTransition(intent string, a connectCredentialAnswer, slot *int64) (connectReport, error) {
	recorded := *r.st
	recorded.Completion = &connectCompletion{Intent: intent, DeploymentID: a.deploymentID, PopKID: a.popKID,
		BindingEpoch: a.bindingEpoch, Slot: slot, VerifiedAt: r.now().UTC().Format(time.RFC3339)}
	return r.completeKeyTransition(&recorded)
}

// completeKeyTransition performs steps 2 to 4 of commitKeyTransition for recorded, whose completion
// is new or was loaded from state.json. It sends nothing and can be repeated after a failure at any
// point.
//
// Step 2 runs for EVERY call, including a record that is already visible. A visible record is not a
// durable one: an earlier call may have renamed it into place and then failed its directory sync,
// and a later sync of the directory can report success without that rename having reached the disk.
// So the record is published again — for a loaded record, byte for byte the same — and only a
// publication whose write, file sync, rename and directory sync all succeed in this call permits the
// rename that destroys a key. When it fails, both key files, the pending step and the record stay as
// they were.
func (r *connectRun) completeKeyTransition(recorded *connectState) (connectReport, error) {
	c := recorded.Completion
	if err := r.store.saveState(recorded); err != nil {
		return nil, fmt.Errorf("record the verified %s durably before promoting its key: %w — no further request was sent during local confirmation, no key was replaced and the pending step is kept; re-run `olivares license connect %s`",
			c.Intent, err, connectTransitionCommand(c.Intent))
	}
	*r.st = *recorded
	connectStep("transition-recorded")
	if err := r.store.promoteIdentity(c.PopKID); err != nil {
		return nil, fmt.Errorf("promote the key of the verified %s (its completion stays recorded; re-run `olivares license connect %s`): %w", c.Intent, connectTransitionCommand(c.Intent), err)
	}
	connectStep("transition-promoted")
	done := *r.st
	done.Binding = &connectBinding{DeploymentID: c.DeploymentID, PopKID: c.PopKID, BindingEpoch: c.BindingEpoch, Slot: c.Slot, Status: "active"}
	done.Completion, done.Pending = nil, nil
	if c.Intent != "rotate" {
		done.Request = nil
	}
	if err := r.store.saveState(&done); err != nil {
		// A failed write normally leaves the completion recorded, but a failed directory sync after
		// the rename may already show the new binding, where re-running the command would start
		// ANOTHER transition. So the operator reads the state first.
		return nil, fmt.Errorf("record the binding of the verified %s: %w — run `olivares license connect status` first: while it shows the completion, "+
			"`olivares license connect %s` finishes it without contacting the service; once it shows binding epoch %d, the %s is complete",
			c.Intent, err, connectTransitionCommand(c.Intent), c.BindingEpoch, c.Intent)
	}
	*r.st = done
	return r.report(connectTransitionDone[c.Intent]), nil
}

// ---- recover / reactivate ----------------------------------------------------------------

// approvedRotation runs recover or reactivate: a request signed by a proposed key, the owner's
// approval, and completion on the rotate-key route with that key as the primary proof.
func (r *connectRun) approvedRotation(ctx context.Context, intent, deploymentID string, bindingEpoch int64, evidence func() (string, error)) (connectReport, error) {
	if err := r.requirePendingIntent(intent); err != nil {
		return nil, err
	}
	if r.st.Completion != nil {
		return r.completeKeyTransition(r.st)
	}
	if err := r.refuseOverride(); err != nil {
		return nil, err
	}
	if !connectv1.ValidDeploymentID(deploymentID) {
		return nil, exitcode.New(exitcode.Usage, errors.New("--deployment must name the deployment id"))
	}
	if r.st.Pending == nil {
		ev, err := evidence()
		if err != nil {
			return nil, err
		}
		if err := r.beginRequest(intent, intent, "next", deploymentID, ev); err != nil {
			return nil, err
		}
	}
	if r.st.Pending.Phase == "request" {
		approvalID, err := r.runRequest(ctx, intent, "next", deploymentID)
		if err != nil {
			return nil, err
		}
		if approvalID == "" {
			return r.report("approval_pending"), nil
		}
		next, _, err := r.store.loadIdentity("next")
		if err != nil {
			return nil, err
		}
		body, err := connectApprovedRotateBody(intent, deploymentID, connectv1.EncodePublicKey(next.Public), r.st.Channel, approvalID)
		if err != nil {
			return nil, err
		}
		op, err := r.store.newPending(intent, "complete", intent, deploymentID, bindingEpoch, "next", false, body)
		if err != nil {
			return nil, err
		}
		r.st.Pending = op
		if err := r.store.saveState(r.st); err != nil {
			return nil, err
		}
	}
	next, ok, err := r.store.loadIdentity("next")
	if err != nil || !ok {
		return nil, fmt.Errorf("%w: the proposed identity is missing", errConnectStateUnsafe)
	}
	// The rotate-key commit advances the binding epoch by one. When the epoch the step was signed
	// under is known, the answer must carry exactly the next one, checked before anything is
	// installed; an unknown epoch (a lost data directory) accepts any epoch >= 1.
	var wantEpoch int64
	if r.st.Pending.BindingEpoch > 0 {
		wantEpoch = r.st.Pending.BindingEpoch + 1
	}
	_, obj, err := r.execute(ctx)
	if err != nil {
		return nil, r.settleApprovedCompletion(err)
	}
	a, _, err := r.acceptOrSettle(obj, next.KID, wantEpoch, deploymentID, intent, true)
	if err != nil {
		return nil, err
	}
	return r.commitKeyTransition(intent, a, a.slot)
}

// ---- delete -----------------------------------------------------------------------------

func (r *connectRun) deactivate(ctx context.Context, deploymentID string, bindingEpoch int64, ownerApproval bool, evidence func() (string, error)) (connectReport, error) {
	if err := r.requirePendingIntent("delete"); err != nil {
		return nil, err
	}
	if !connectv1.ValidDeploymentID(deploymentID) {
		return nil, exitcode.New(exitcode.Usage, errors.New("name the deployment to deactivate"))
	}
	if r.st.Pending == nil {
		if ownerApproval {
			ev, err := evidence()
			if err != nil {
				return nil, err
			}
			if err := r.beginRequest("delete", "delete", "current", deploymentID, ev); err != nil {
				return nil, err
			}
		} else {
			cur, ok, err := r.store.loadIdentity("current")
			if err != nil {
				return nil, err
			}
			if !ok || r.st.Binding == nil || r.st.Binding.DeploymentID != deploymentID || cur.KID != r.st.Binding.PopKID {
				return nil, exitcode.New(exitcode.Auth, errors.New("only the bound key can deactivate by proof of possession; use --owner-approval"))
			}
			body, err := connectDeleteBody(deploymentID, connectv1.EncodePublicKey(cur.Public), "")
			if err != nil {
				return nil, err
			}
			op, err := r.store.newPending("delete", "delete", connectv1.OpDelete, deploymentID, bindingEpoch, "current", false, body)
			if err != nil {
				return nil, err
			}
			r.st.Pending = op
			if err := r.store.saveState(r.st); err != nil {
				return nil, err
			}
		}
	}
	if r.st.Pending.Phase == "request" {
		approvalID, err := r.runRequest(ctx, "delete", "current", deploymentID)
		if err != nil {
			return nil, err
		}
		if approvalID == "" {
			return r.report("approval_pending"), nil
		}
		cur, _, err := r.store.loadIdentity("current")
		if err != nil {
			return nil, err
		}
		body, err := connectDeleteBody(deploymentID, connectv1.EncodePublicKey(cur.Public), approvalID)
		if err != nil {
			return nil, err
		}
		op, err := r.store.newPending("delete", "delete", connectv1.OpDelete, deploymentID, bindingEpoch, "current", false, body)
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
		return nil, r.settle(err, true)
	}
	if err := parseDeleteAnswer(obj, deploymentID); err != nil {
		return nil, err
	}
	if b := r.st.Binding; b != nil && b.DeploymentID == deploymentID {
		b.Status = "deleted"
	}
	if err := r.end(true); err != nil {
		return nil, err
	}
	return r.report("deactivated"), nil
}

// ---- evidence ---------------------------------------------------------------------------

// connectOwnerRequestEvidence is the purchase evidence a NEW recover, reactivate or owner-approved
// deactivate request presents. It is never taken from the installed license: after bind or refresh
// that file holds the connected deployment credential, which the service does not accept as purchase
// evidence, and this client does not guess a credential's class from its contents. Only the lazy
// callback that builds a NEW request calls this; a recorded request or completion is repeated with
// its exact bytes and never reads evidence again, so a later, different or unreadable --evidence
// cannot rewrite it.
func connectOwnerRequestEvidence(dataDir, arg, operation string, stdin io.Reader, now time.Time) (string, error) {
	if strings.TrimSpace(arg) == "" {
		return "", exitcode.New(exitcode.Usage, fmt.Errorf("a new %s request needs the current purchase credential: pass --evidence <file> with the commercial credential of the current purchase (from the customer portal or the purchase email), or --evidence - to read it from standard input; the license installed in the data directory is not used as evidence for this request, and nothing was sent", operation))
	}
	return connectEvidence(dataDir, arg, stdin, now)
}

// connectEvidence returns the purchase evidence a request presents: a v3 credential verified
// against the data directory's license trust. Its expiry is NOT required — a lapsed provisional
// credential is exactly what a buyer holds when reconnecting — but its signer, key id and epoch
// are.
func connectEvidence(dataDir, arg string, stdin io.Reader, now time.Time) (string, error) {
	var blob string
	switch arg {
	case "":
		src, err := resolveLicense("", dataDir, osGetenv)
		if err != nil {
			return "", err
		}
		if src.Blob == "" {
			return "", exitcode.New(exitcode.Usage, errors.New("there is no installed license to present as purchase evidence: pass --evidence <file> (or - for stdin) with the credential from your purchase"))
		}
		blob = src.Blob
	case "-":
		b, err := io.ReadAll(io.LimitReader(stdin, license.MaxLicenseBlobBytes+1))
		if err != nil {
			return "", fmt.Errorf("read evidence from stdin: %w", err)
		}
		blob = strings.TrimSpace(string(b))
	default:
		b, err := os.ReadFile(arg)
		if err != nil {
			return "", fmt.Errorf("read --evidence: %w", err)
		}
		blob = strings.TrimSpace(string(b))
	}
	kr, err := licenseKeyringForDataDir(dataDir)
	if err != nil {
		return "", withLicenseTrustAction(err)
	}
	v, err := kr.Verify(blob, now)
	if err != nil {
		return "", fmt.Errorf("the purchase evidence does not verify: %w", withLicenseTrustAction(err))
	}
	if !v.IsCredentialV3() {
		return "", exitcode.New(exitcode.Usage, errors.New("purchase evidence must be an "+license.CredentialSchemaV3+" credential"))
	}
	return blob, nil
}
