// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// fakeContinuation is an installed external verifier's stand-in: it remembers
// the tenant it selected, a proof horizon, and whether its source/grant is
// still current at completion time.
type fakeContinuation struct {
	scope    model.TenantID
	horizon  time.Time
	refuse   error
	rechecks int
}

func (c *fakeContinuation) SessionScope() model.TenantID { return c.scope }
func (c *fakeContinuation) ProofHorizon() time.Time      { return c.horizon }
func (c *fakeContinuation) RevalidateCurrent(_ context.Context, _ store.Store) error {
	c.rechecks++
	return c.refuse
}

var errSourceWithdrawn = errors.New("auth: the verifying source is no longer current")

func TestTOTPContinuationLocalPathIsUnchanged(t *testing.T) {
	f := newTOTPFixture(t)
	// The local password path passes no continuation: every existing journey
	// test already pins it; here the negative space is the point — the gate
	// and completion compile and behave with the seam present, and a local
	// session still lands in the account's custody scope.
	res, err := f.loginAs(f.user.Email)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	f.enrolFactor(res.Token)
	gated, err := f.loginAs(f.user.Email)
	if err != nil {
		t.Fatalf("gated login: %v", err)
	}
	seed := currentTOTPSecret(t, f, f.user.ID)
	token, sess, err := f.a.CompleteTOTPLogin(f.ctx, gated.MFAToken, totpCodeFor(t, seed, time.Now()), "127.0.0.1", nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if token == "" || sess.TenantScope != f.user.CustodyScope() {
		t.Fatalf("local completion scope = %q, want the account custody scope", sess.TenantScope)
	}
}

func TestTOTPContinuationCarriesExternalContext(t *testing.T) {
	f := newTOTPFixture(t)
	tenant := f.tenantFor(t, "external-ctx")
	res, _ := f.loginAs(f.user.Email)
	f.enrolFactor(res.Token)

	cont := &fakeContinuation{scope: tenant, horizon: time.Now().Add(TOTPTestHorizon)}
	out, err := f.a.CompleteExternalLogin(f.ctx, f.user, "10.2.3.4", cont, "", []string{"card"})
	if err != nil {
		t.Fatalf("external login: %v", err)
	}
	if !out.RequiresMFA() || out.MFAEnrolmentRequired {
		t.Fatalf("external gate = %+v, want a code challenge", out)
	}

	// The challenge completes in the EXTERNAL scope, not the custody scope,
	// with the continuation revalidated exactly once and never re-verified.
	seed := currentTOTPSecret(t, f, f.user.ID)
	token, sess, err := f.a.CompleteTOTPLogin(f.ctx, out.MFAToken, totpCodeFor(t, seed, time.Now()), "127.0.0.1", nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if token == "" {
		t.Fatal("no session token")
	}
	if sess.TenantScope != tenant {
		t.Fatalf("completion scope = %q, want the external tenant %q", sess.TenantScope, tenant)
	}
	if cont.rechecks != 1 {
		t.Fatalf("revalidated %d times, want exactly 1", cont.rechecks)
	}
	hasTOTP, hasExternal := false, false
	for _, m := range sess.AMR {
		if m == "totp" {
			hasTOTP = true
		}
		if m == "external" {
			hasExternal = true
		}
	}
	if !hasTOTP || !hasExternal {
		t.Fatalf("session AMR = %v, want external+totp", sess.AMR)
	}
}

func TestTOTPContinuationForcedEnrolmentCarriesContext(t *testing.T) {
	f := newTOTPFixture(t)
	tenant := f.tenantFor(t, "external-enrol")
	principal := auth.Principal{Kind: auth.KindUser, UserID: f.admin.ID, CredID: model.NewID()}
	if err := f.a.SetRequireTOTPForAdmins(f.ctx, principal, true); err != nil {
		t.Fatalf("policy: %v", err)
	}

	cont := &fakeContinuation{scope: tenant, horizon: time.Now().Add(TOTPTestHorizon)}
	out, err := f.a.CompleteExternalLogin(f.ctx, f.admin, "10.2.3.5", cont, "", nil)
	if err != nil {
		t.Fatalf("external login: %v", err)
	}
	if !out.RequiresMFA() || !out.MFAEnrolmentRequired {
		t.Fatalf("external gate = %+v, want forced enrolment", out)
	}
	enrol, err := f.a.BeginTOTPEnrolmentForLogin(f.ctx, out.MFAToken, "Olivares AI")
	if err != nil {
		t.Fatalf("begin enrolment: %v", err)
	}
	token, sess, codes, err := f.a.FinishTOTPEnrolmentForLogin(f.ctx, out.MFAToken,
		totpCodeFor(t, enrol.Secret, time.Now()), "127.0.0.1", nil)
	if err != nil {
		t.Fatalf("finish enrolment: %v", err)
	}
	if token == "" || len(codes) != 10 {
		t.Fatalf("enrolment completion = %q, %d codes", token, len(codes))
	}
	if sess.TenantScope != tenant {
		t.Fatalf("enrolment completion scope = %q, want %q", sess.TenantScope, tenant)
	}
	if cont.rechecks != 1 {
		t.Fatalf("revalidated %d times, want exactly 1", cont.rechecks)
	}
}

func TestTOTPContinuationRefusesWithdrawnSource(t *testing.T) {
	f := newTOTPFixture(t)
	tenant := f.tenantFor(t, "external-withdrawn")
	res, _ := f.loginAs(f.user.Email)
	f.enrolFactor(res.Token)

	cont := &fakeContinuation{scope: tenant, horizon: time.Now().Add(TOTPTestHorizon)}
	out, err := f.a.CompleteExternalLogin(f.ctx, f.user, "10.2.3.6", cont, "", nil)
	if err != nil {
		t.Fatalf("external login: %v", err)
	}
	// The source is withdrawn between challenge and completion.
	cont.refuse = errSourceWithdrawn
	seed := currentTOTPSecret(t, f, f.user.ID)
	if _, _, err := f.a.CompleteTOTPLogin(f.ctx, out.MFAToken, totpCodeFor(t, seed, time.Now()), "127.0.0.1", nil); !errors.Is(err, errSourceWithdrawn) {
		t.Fatalf("withdrawn source err = %v", err)
	}
	// Deny-closed consumed the challenge: a later attempt (source restored)
	// cannot reuse the pending credential.
	cont.refuse = nil
	if _, _, err := f.a.CompleteTOTPLogin(f.ctx, out.MFAToken, totpCodeFor(t, seed, time.Now()), "127.0.0.1", nil); !errors.Is(err, auth.ErrTOTPVerification) {
		t.Fatalf("consumed challenge err = %v, want ErrTOTPVerification", err)
	}
}

func TestTOTPContinuationDirectMintWithoutFactor(t *testing.T) {
	f := newTOTPFixture(t)
	tenant := f.tenantFor(t, "external-direct")
	cont := &fakeContinuation{scope: tenant, horizon: time.Now().Add(TOTPTestHorizon)}
	// f.user has NO factor: the external login mints directly, revalidated,
	// in the external scope.
	out, err := f.a.CompleteExternalLogin(f.ctx, f.user, "10.2.3.7", cont, "", nil)
	if err != nil {
		t.Fatalf("external login: %v", err)
	}
	if out.RequiresMFA() {
		t.Fatalf("factorless external login = %+v, want a session", out)
	}
	if out.Session.TenantScope != tenant {
		t.Fatalf("direct mint scope = %q, want %q", out.Session.TenantScope, tenant)
	}
	if cont.rechecks != 1 {
		t.Fatalf("revalidated %d times, want exactly 1", cont.rechecks)
	}
	// A withdrawn source refuses before any session exists.
	cont2 := &fakeContinuation{scope: tenant, horizon: time.Now().Add(TOTPTestHorizon), refuse: errSourceWithdrawn}
	if _, err := f.a.CompleteExternalLogin(f.ctx, f.user, "10.2.3.8", cont2, "", nil); !errors.Is(err, errSourceWithdrawn) {
		t.Fatalf("withdrawn source direct mint err = %v", err)
	}
}

func TestTOTPContinuationHorizonCapsTheChallenge(t *testing.T) {
	f := newTOTPFixture(t)
	tenant := f.tenantFor(t, "external-horizon")
	res, _ := f.loginAs(f.user.Email)
	f.enrolFactor(res.Token)

	// A horizon one second out: the pending entry dies with it.
	cont := &fakeContinuation{scope: tenant, horizon: time.Now().Add(time.Second)}
	out, err := f.a.CompleteExternalLogin(f.ctx, f.user, "10.2.3.9", cont, "", nil)
	if err != nil {
		t.Fatalf("external login: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	seed := currentTOTPSecret(t, f, f.user.ID)
	if _, _, err := f.a.CompleteTOTPLogin(f.ctx, out.MFAToken, totpCodeFor(t, seed, time.Now()), "127.0.0.1", nil); !errors.Is(err, auth.ErrTOTPVerification) {
		t.Fatalf("expired horizon err = %v, want ErrTOTPVerification", err)
	}
}

// tenantFor provisions (or reuses) a business tenant id for a test.
func (f *totpFixture) tenantFor(t *testing.T, slug string) model.TenantID {
	t.Helper()
	var id model.TenantID
	if err := f.st.System(f.ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(f.ctx, model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
		if err != nil {
			return err
		}
		id = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("create org: %v", err)
	}
	return id
}

// TOTPTestHorizon is a comfortably long proof horizon for tests.
const TOTPTestHorizon = 10 * time.Minute

// txContinuation implements the OPTIONAL transactional half: it records every
// in-transaction consult and can refuse the Nth one, standing in for a source
// withdrawn between revalidation and issuance or a proof expiring under the
// write.
type txContinuation struct {
	fakeContinuation
	txRefuseOn int
	txCalls    int
	txAccount  model.ID
	txScope    model.TenantID
}

func (c *txContinuation) RevalidateSessionTx(_ context.Context, as store.AuthScope, accountID model.ID, scope model.TenantID) error {
	c.txCalls++
	c.txAccount = accountID
	c.txScope = scope
	if c.txCalls == c.txRefuseOn {
		return errSourceWithdrawn
	}
	return nil
}

// A withdrawal that lands between the pre-mint revalidation and the issuance
// writes REFUSES the login and rolls the whole transaction back: no session,
// and the factor's replay window and recovery-code spend never happened.
func TestTOTPContinuationTxWithdrawalBetweenCheckAndMintRollsBack(t *testing.T) {
	f := newTOTPFixture(t)
	tenant := f.tenantFor(t, "tx-withdrawn")
	res, _ := f.loginAs(f.user.Email)
	f.enrolFactor(res.Token)

	// txRefuseOn 2 = the AFTER-writes consult refuses: the pre-activation
	// consult passed, the session and audit rows were written, and the
	// refusal must still roll all of it back.
	cont := &txContinuation{fakeContinuation: fakeContinuation{scope: tenant, horizon: time.Now().Add(TOTPTestHorizon)}, txRefuseOn: 2}
	out, err := f.a.CompleteExternalLogin(f.ctx, f.user, "10.9.9.1", cont, "", nil)
	if err != nil {
		t.Fatalf("external login: %v", err)
	}
	seed := currentTOTPSecret(t, f, f.user.ID)
	code := totpCodeFor(t, seed, time.Now())
	if _, _, err := f.a.CompleteTOTPLogin(f.ctx, out.MFAToken, code, "127.0.0.1", nil); !errors.Is(err, errSourceWithdrawn) {
		t.Fatalf("withdrawal err = %v", err)
	}
	if cont.txCalls != 2 {
		t.Fatalf("tx consults = %d, want 2 (pre-activation and post-write)", cont.txCalls)
	}
	// Rollback proof 1: the replay window did NOT advance — the very same
	// code still verifies on a later, healthy login.
	cont.txRefuseOn = 0
	out2, err := f.a.CompleteExternalLogin(f.ctx, f.user, "10.9.9.2", cont, "", nil)
	if err != nil {
		t.Fatalf("second external login: %v", err)
	}
	if _, _, err := f.a.CompleteTOTPLogin(f.ctx, out2.MFAToken, code, "127.0.0.1", nil); err != nil {
		t.Fatalf("the refused mint advanced the replay window: %v", err)
	}
	// Rollback proof 2: the refused issuance left no row — the account holds
	// exactly the sessions it had before (the enrolment login's) plus the one
	// healthy completion.
	if n := f.countSessions(t, f.user.ID); n != 2 {
		t.Fatalf("sessions after a rolled-back mint = %d, want 2 (baseline + the healthy one)", n)
	}
}

// An expiry that lands DURING issuance (the pre-activation consult passes,
// the post-write consult refuses) is the same refusal and the same rollback.
func TestTOTPContinuationTxExpiryDuringIssuance(t *testing.T) {
	f := newTOTPFixture(t)
	tenant := f.tenantFor(t, "tx-expiry")
	res, _ := f.loginAs(f.user.Email)
	f.enrolFactor(res.Token)
	cont := &txContinuation{fakeContinuation: fakeContinuation{scope: tenant, horizon: time.Now().Add(TOTPTestHorizon)}, txRefuseOn: 2}
	out, err := f.a.CompleteExternalLogin(f.ctx, f.user, "10.9.9.3", cont, "", nil)
	if err != nil {
		t.Fatalf("external login: %v", err)
	}
	seed := currentTOTPSecret(t, f, f.user.ID)
	if _, _, err := f.a.CompleteTOTPLogin(f.ctx, out.MFAToken, totpCodeFor(t, seed, time.Now()), "127.0.0.1", nil); !errors.Is(err, errSourceWithdrawn) {
		t.Fatalf("expiry err = %v", err)
	}
	// The refused issuance left no row: only the enrolment login's baseline
	// session remains.
	if n := f.countSessions(t, f.user.ID); n != 1 {
		t.Fatalf("sessions after a rolled-back mint = %d, want the 1 baseline", n)
	}
}

// The tx consult sees the EXACT account and scope, so a swap mid-flight is
// itself a refusal the continuation can make.
func TestTOTPContinuationTxSeesExactAccountAndScope(t *testing.T) {
	f := newTOTPFixture(t)
	tenant := f.tenantFor(t, "tx-exact")
	res, _ := f.loginAs(f.user.Email)
	f.enrolFactor(res.Token)
	cont := &txContinuation{fakeContinuation: fakeContinuation{scope: tenant, horizon: time.Now().Add(TOTPTestHorizon)}}
	out, err := f.a.CompleteExternalLogin(f.ctx, f.user, "10.9.9.4", cont, "", nil)
	if err != nil {
		t.Fatalf("external login: %v", err)
	}
	seed := currentTOTPSecret(t, f, f.user.ID)
	if _, _, err := f.a.CompleteTOTPLogin(f.ctx, out.MFAToken, totpCodeFor(t, seed, time.Now()), "127.0.0.1", nil); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if cont.txAccount != f.user.ID || cont.txScope != tenant {
		t.Fatalf("tx consult saw account=%s scope=%s", cont.txAccount, cont.txScope)
	}
}

// countSessions counts the account's live sessions (the rollback witness).
func (f *totpFixture) countSessions(t *testing.T, accountID model.ID) int {
	t.Helper()
	n := 0
	if err := f.st.AuthView(f.ctx, func(as store.AuthScope) error {
		rows, _, err := as.Sessions().List(f.ctx, model.Query{
			Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: accountID.String()}},
		})
		if err != nil {
			return err
		}
		n = len(rows)
		return nil
	}); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	return n
}
