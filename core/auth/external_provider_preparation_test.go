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

// This continuation delegates to the actual opaque native preparation. It adds
// no principal or permit, and never re-runs the installed provider's verifier.
type preparedExternalContinuation struct {
	admission      *auth.ExternalLoginAdmission
	afterPreflight func()
}

func (c *preparedExternalContinuation) SessionScope() model.TenantID {
	return c.admission.SessionScope()
}
func (c *preparedExternalContinuation) ProofHorizon() time.Time { return c.admission.ProofHorizon() }
func (c *preparedExternalContinuation) RevalidateCurrent(ctx context.Context, st store.Store) error {
	if err := c.admission.RevalidateCurrent(ctx, st); err != nil {
		return err
	}
	if c.afterPreflight != nil {
		c.afterPreflight()
	}
	return nil
}
func (c *preparedExternalContinuation) RevalidateSessionTx(ctx context.Context, as store.AuthScope, accountID model.ID, scope model.TenantID) error {
	return c.admission.RevalidateSessionTx(ctx, as, accountID, scope)
}

func externalComplete(t *testing.T, f *externalFixture, user model.User, admission *auth.ExternalLoginAdmission) auth.LoginResult {
	t.Helper()
	result, err := f.a.CompleteExternalLogin(context.Background(), user, "10.0.0.4", &preparedExternalContinuation{admission: admission}, "sso.login", nil)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func externalEnrolFactor(t *testing.T, f *externalFixture) []string {
	t.Helper()
	f.a.WithTOTPSeedSealer(newTOTPTestSealer(t))
	user, admission, err := f.prepare()
	if err != nil {
		t.Fatal(err)
	}
	result := externalComplete(t, f, user, admission)
	principal, err := f.a.Authenticate(context.Background(), result.Token)
	if err != nil {
		t.Fatal(err)
	}
	enrol, err := f.a.BeginTOTPEnrolment(context.Background(), principal, "Installed provider")
	if err != nil {
		t.Fatal(err)
	}
	codes, err := f.a.FinishTOTPEnrolment(context.Background(), principal, totpCodeFor(t, enrol.Secret, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	return codes
}

func TestExternalProviderPreparedAccountUsesNativeFactorGate(t *testing.T) {
	for _, factor := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "pending"}[factor], func(t *testing.T) {
			f := newExternalFixture(t)
			f.activate(t)
			var codes []string
			if factor {
				codes = externalEnrolFactor(t, f)
			}
			_, beforeSessions, _ := externalAccountCounts(t, f)
			user, admission, err := f.prepare()
			if err != nil {
				t.Fatal(err)
			}
			if _, sessions, _ := externalAccountCounts(t, f); sessions != beforeSessions {
				t.Fatal("preparation minted a login credential")
			}
			builds, verifies := f.builder.builds, f.builder.verifies
			result := externalComplete(t, f, user, admission)
			if factor {
				if !result.RequiresMFA() || result.Token != "" || !result.Session.ID.IsZero() {
					t.Fatal("factor gate minted a credential before completing MFA")
				}
				if _, sessions, _ := externalAccountCounts(t, f); sessions != beforeSessions {
					t.Fatal("pending login persisted a session")
				}
				result.Token, result.Session, err = f.a.CompleteTOTPLogin(context.Background(), result.MFAToken, codes[0], "10.0.0.4", nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			if result.Token == "" || result.Session.UserID != user.ID || result.Session.TenantScope != f.tenant || result.Session.AAL != auth.AAL1 || result.Session.AMR[0] != "external" {
				t.Fatal("native issuance lost prepared account/scope/method")
			}
			principal, err := f.a.Authenticate(context.Background(), result.Token)
			if err != nil || principal.SessionScope() != f.tenant || principal.AAL != auth.AAL1 {
				t.Fatalf("native authentication = %v", err)
			}
			if f.builder.builds != builds || f.builder.verifies != verifies {
				t.Fatal("completion rebuilt or reverified the original provider")
			}
			if err := f.a.RevokeSession(context.Background(), principal, result.Session.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func externalChangeUser(t *testing.T, f *externalFixture, id model.ID, change func(*model.User)) {
	t.Helper()
	if err := f.st.AuthMutate(context.Background(), func(as store.AuthScope) error {
		if err := as.(store.AuthUserAuthorityWriter).PrepareUserAuthorityWrite(context.Background(), []model.ID{id}); err != nil {
			return err
		}
		user, err := as.Users().Get(context.Background(), id)
		if err != nil {
			return err
		}
		change(&user)
		_, err = as.Users().Update(context.Background(), user)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func externalWithdrawMembership(t *testing.T, f *externalFixture, id model.ID) {
	t.Helper()
	if err := f.st.AuthMutate(context.Background(), func(as store.AuthScope) error {
		if err := as.(store.AuthUserAuthorityWriter).PrepareUserAuthorityWrite(context.Background(), []model.ID{id}); err != nil {
			return err
		}
		rows, _, err := as.Memberships().List(context.Background(), model.Query{Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: id.String()}, {Column: "target_tenant_id", Op: model.OpEq, Value: f.tenant.String()}}, Limit: 2})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return store.ErrNotFound
		}
		return as.Memberships().Delete(context.Background(), rows[0].ID)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestExternalProviderPreparedAdmissionRefusesCurrentWithdrawal(t *testing.T) {
	for _, change := range []string{"generation", "disable", "delete", "domains", "issuer", "reference", "subject", "disabled-account", "superadmin", "membership", "grant"} {
		t.Run(change, func(t *testing.T) {
			f := newExternalFixture(t)
			f.activate(t)
			user, admission, err := f.prepare()
			if err != nil {
				t.Fatal(err)
			}
			builds, verifies := f.builder.builds, f.builder.verifies
			switch change {
			case "generation", "disable", "domains", "issuer", "reference":
				in := externalInput(false)
				switch change {
				case "generation":
					in.ExternalConnectorGeneration++
					in.Enabled = true
					f.builder.revision = in.ExternalConnectorGeneration
				case "domains":
					in.ClaimedDomains = []string{"other.example"}
				case "issuer":
					in.ExternalIssuer = "urn:provider:new-owner"
				case "reference":
					in.ExternalConnectorRef = "different-owner"
				}
				if _, err := f.service.PutConfigIdP(context.Background(), f.super, f.tenant, "corp", in); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if err := f.service.DeleteConfigIdP(context.Background(), f.super, f.tenant, "corp"); err != nil {
					t.Fatal(err)
				}
			case "subject":
				externalChangeUser(t, f, user.ID, func(u *model.User) { u.SsoSubject = "v1:other:subject" })
			case "disabled-account":
				externalChangeUser(t, f, user.ID, func(u *model.User) { u.Status = model.StatusInactive })
			case "superadmin":
				externalChangeUser(t, f, user.ID, func(u *model.User) { u.IsSuperadmin = true })
			case "membership":
				externalWithdrawMembership(t, f, user.ID)
			case "grant":
				f.builder.admissionErr = auth.ErrUnauthenticated
			}
			builds = f.builder.builds // only completion must avoid Build/Verify.
			beforeU, beforeS, beforeM := externalAccountCounts(t, f)
			if result, err := f.a.CompleteExternalLogin(context.Background(), user, "10.0.0.4", &preparedExternalContinuation{admission: admission}, "sso.login", nil); !errors.Is(err, auth.ErrUnauthenticated) || result.Token != "" || result.RequiresMFA() {
				t.Fatalf("withdrawn original admitted: %v", err)
			}
			if users, sessions, members := externalAccountCounts(t, f); users != beforeU || sessions != beforeS || members != beforeM {
				t.Fatal("withdrawal completion recreated account/membership or minted a session")
			}
			if f.builder.builds != builds || f.builder.verifies != verifies {
				t.Fatal("withdrawal completion repeated verification")
			}
		})
	}
}

func TestExternalProviderPreparedAdmissionBindsExactAccountAndTenant(t *testing.T) {
	f := newExternalFixture(t)
	f.activate(t)
	user, admission, err := f.prepare()
	if err != nil {
		t.Fatal(err)
	}
	for _, wrong := range []string{"account", "tenant", "zero"} {
		t.Run(wrong, func(t *testing.T) {
			account, tenant := user.ID, f.tenant
			switch wrong {
			case "account":
				account = f.super.UserID
			case "tenant":
				tenant = model.NewTenantID()
			case "zero":
				account = ""
			}
			if err := f.st.AuthMutate(context.Background(), func(as store.AuthScope) error {
				return admission.RevalidateSessionTx(context.Background(), as, account, tenant)
			}); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("mismatched target admitted: %v", err)
			}
		})
	}
	if err := f.st.AuthMutate(context.Background(), func(as store.AuthScope) error {
		return admission.RevalidateSessionTx(context.Background(), as, user.ID, f.tenant)
	}); err != nil {
		t.Fatal(err)
	}
	if _, sessions, _ := externalAccountCounts(t, f); sessions != 0 {
		t.Fatal("revalidator minted a credential")
	}
}

func TestExternalProviderPreparedAdmissionCapturesOriginalFiniteHorizon(t *testing.T) {
	for _, name := range []string{"zero", "expired", "retained"} {
		t.Run(name, func(t *testing.T) {
			f := newExternalFixture(t)
			f.activate(t)
			f.builder.ignoreExpiry = true
			switch name {
			case "zero":
				f.builder.zeroHorizon = true
			case "expired":
				f.builder.expiresAt = time.Now().Add(-time.Second)
			case "retained":
				f.builder.expiresAt = time.Now().Add(250 * time.Millisecond)
			}
			user, admission, err := f.prepare()
			if name != "retained" {
				if !errors.Is(err, auth.ErrUnauthenticated) || !user.ID.IsZero() || admission != nil {
					t.Fatalf("missing/expired horizon prepared: %v", err)
				}
				if users, sessions, members := externalAccountCounts(t, f); users != 1 || sessions != 0 || members != 0 {
					t.Fatal("invalid horizon persisted preparation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			original := admission.ProofHorizon()
			f.builder.extendedHorizon = time.Now().Add(time.Hour)
			time.Sleep(time.Until(original) + 2*time.Millisecond)
			if !admission.ProofHorizon().Equal(original) {
				t.Fatal("admission renewed the producer-issued horizon")
			}
			if err := admission.RevalidateCurrent(context.Background(), f.st); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("expired original admitted: %v", err)
			}
			if f.builder.horizonCalls != 1 {
				t.Fatalf("producer horizon observed %d times, want once", f.builder.horizonCalls)
			}
		})
	}
}

func TestExternalProviderPreparedPendingExpiresWithoutReverification(t *testing.T) {
	f := newExternalFixture(t)
	f.activate(t)
	codes := externalEnrolFactor(t, f)
	f.builder.expiresAt = time.Now().Add(250 * time.Millisecond)
	user, admission, err := f.prepare()
	if err != nil {
		t.Fatal(err)
	}
	builds, verifies := f.builder.builds, f.builder.verifies
	_, beforeSessions, _ := externalAccountCounts(t, f)
	pending := externalComplete(t, f, user, admission)
	if !pending.RequiresMFA() || pending.Token != "" {
		t.Fatal("missing native factor challenge")
	}
	time.Sleep(time.Until(admission.ProofHorizon()) + 2*time.Millisecond)
	if token, _, err := f.a.CompleteTOTPLogin(context.Background(), pending.MFAToken, codes[0], "10.0.0.4", nil); !errors.Is(err, auth.ErrTOTPVerification) || token != "" {
		t.Fatalf("expired pending original admitted: %v", err)
	}
	if _, sessions, _ := externalAccountCounts(t, f); sessions != beforeSessions {
		t.Fatal("expired pending minted a session")
	}
	if f.builder.builds != builds || f.builder.verifies != verifies {
		t.Fatal("expired pending reverified the primary source")
	}
}

func TestExternalProviderPreparedPendingRefusesWithdrawalWithoutRecreatingMembership(t *testing.T) {
	for _, change := range []string{"generation", "membership", "grant"} {
		t.Run(change, func(t *testing.T) {
			f := newExternalFixture(t)
			f.activate(t)
			codes := externalEnrolFactor(t, f)
			user, admission, err := f.prepare()
			if err != nil {
				t.Fatal(err)
			}
			pending := externalComplete(t, f, user, admission)
			if !pending.RequiresMFA() || pending.Token != "" {
				t.Fatal("factor challenge required")
			}
			builds, verifies := f.builder.builds, f.builder.verifies
			switch change {
			case "generation":
				in := externalInput(true)
				in.ExternalConnectorGeneration++
				f.builder.revision = in.ExternalConnectorGeneration
				if _, err := f.service.PutConfigIdP(context.Background(), f.super, f.tenant, "corp", in); err != nil {
					t.Fatal(err)
				}
				builds = f.builder.builds // activation readiness is separate from login completion.
			case "membership":
				externalWithdrawMembership(t, f, user.ID)
			case "grant":
				f.builder.admissionErr = auth.ErrUnauthenticated
			}
			beforeU, beforeS, beforeM := externalAccountCounts(t, f)
			if token, _, err := f.a.CompleteTOTPLogin(context.Background(), pending.MFAToken, codes[0], "10.0.0.4", nil); !errors.Is(err, auth.ErrUnauthenticated) || token != "" {
				t.Fatalf("withdrawn pending admitted: %v", err)
			}
			if users, sessions, members := externalAccountCounts(t, f); users != beforeU || sessions != beforeS || members != beforeM {
				t.Fatal("withdrawn pending recreated authority or minted a credential")
			}
			if f.builder.builds != builds || f.builder.verifies != verifies {
				t.Fatal("pending completion rebuilt or reverified the source")
			}
			if token, _, err := f.a.CompleteTOTPLogin(context.Background(), pending.MFAToken, codes[0], "10.0.0.4", nil); !errors.Is(err, auth.ErrTOTPVerification) || token != "" {
				t.Fatalf("withdrawn pending was reusable: %v", err)
			}
		})
	}
}

func externalLoginAuditCount(t *testing.T, f *externalFixture) int {
	t.Helper()
	count := 0
	if err := f.st.AuthView(context.Background(), func(as store.AuthScope) error {
		return as.Audit().Walk(context.Background(), 1, func(event model.AuditEvent) error {
			if event.Action == "sso.login" {
				count++
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestExternalProviderPreparedAdmissionRevalidatesInsideSessionTransaction(t *testing.T) {
	for _, name := range []string{"slot-after-preflight", "expiry-after-login-audit"} {
		t.Run(name, func(t *testing.T) {
			f := newExternalFixture(t)
			f.activate(t)
			f.builder.expiresAt = time.Now().Add(250 * time.Millisecond)
			user, admission, err := f.prepare()
			if err != nil {
				t.Fatal(err)
			}
			cont := &preparedExternalContinuation{admission: admission}
			if name == "slot-after-preflight" {
				cont.afterPreflight = func() {
					if _, err := f.service.PutConfigIdP(context.Background(), f.super, f.tenant, "corp", externalInput(false)); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				wrapped := &externalIssuanceStore{Store: f.st, loginAuditAction: "sso.login", beforeLoginAudit: func() error { time.Sleep(time.Until(admission.ProofHorizon()) + 2*time.Millisecond); return nil }}
				f.a = auth.NewAuthenticator(wrapped, nil)
			}
			_, beforeSessions, _ := externalAccountCounts(t, f)
			beforeAudits := externalLoginAuditCount(t, f)
			result, err := f.a.CompleteExternalLogin(context.Background(), user, "10.0.0.4", cont, "sso.login", nil)
			if !errors.Is(err, auth.ErrUnauthenticated) || result.Token != "" || !result.Session.ID.IsZero() {
				t.Fatalf("final original currency was not enforced: session-present %t / %v", result.Token != "", err)
			}
			if _, sessions, _ := externalAccountCounts(t, f); sessions != beforeSessions {
				t.Fatal("refused final admission persisted a credential")
			}
			if externalLoginAuditCount(t, f) != beforeAudits {
				t.Fatal("refused final admission persisted a success audit")
			}
		})
	}
}
