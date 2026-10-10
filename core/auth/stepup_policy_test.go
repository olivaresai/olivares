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

// setStepUpPolicy writes the deployment policy row directly, the way an
// administrator who already proved the level would leave it.
func setStepUpPolicy(t *testing.T, ctx context.Context, st store.Store, policy string) {
	t.Helper()
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		rows, _, err := as.AuthPolicy().List(ctx, model.Query{})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			_, err = as.AuthPolicy().Create(ctx, model.AuthPolicy{AdminStepUp: policy})
			return err
		}
		rows[0].AdminStepUp = policy
		_, err = as.AuthPolicy().Update(ctx, rows[0])
		return err
	}); err != nil {
		t.Fatalf("set step-up policy %q: %v", policy, err)
	}
}

func fixedPolicy(policy string) auth.StepUpSource {
	return func(context.Context) (string, error) { return policy, nil }
}

func TestFederatedMFASatisfiesTOTPNotPasskey(t *testing.T) {
	for _, aal := range []int{1, 2} {
		p := auth.Principal{Kind: auth.KindUser, AAL: aal, AMR: []string{"sso", "mfa"}}
		ctx := auth.WithStepUpSource(context.Background(), fixedPolicy(auth.StepUpTOTP))
		if got := auth.StepUpSatisfied(ctx, p); got != (aal == 2) {
			t.Errorf("federated MFA at AAL%d satisfies totp = %v", aal, got)
		}
		ctx = auth.WithStepUpSource(context.Background(), fixedPolicy(auth.StepUpPasskey))
		if auth.StepUpSatisfied(ctx, p) {
			t.Errorf("federated MFA at AAL%d must never satisfy passkey", aal)
		}
	}
}

func TestStepUpSatisfiedFollowsThePolicy(t *testing.T) {
	password := auth.Principal{Kind: auth.KindUser, AAL: auth.AAL1, AMR: []string{"pwd"}}
	withCode := auth.Principal{Kind: auth.KindUser, AAL: auth.AAL1, AMR: []string{"pwd", "totp"}}
	passkey := auth.Principal{Kind: auth.KindUser, AAL: auth.AAL3, AMR: []string{"pwd", "webauthn"}}
	token := auth.Principal{Kind: auth.KindToken}
	cases := []struct {
		name   string
		ctx    context.Context
		p      auth.Principal
		wantOK bool
	}{
		{"no policy in context is strict", context.Background(), password, false},
		{"no policy in context admits a passkey session", context.Background(), passkey, true},
		{"none admits a password session", auth.WithStepUpSource(context.Background(), fixedPolicy(auth.StepUpNone)), password, true},
		{"none still refuses a token", auth.WithStepUpSource(context.Background(), fixedPolicy(auth.StepUpNone)), token, false},
		{"totp refuses a password-only session", auth.WithStepUpSource(context.Background(), fixedPolicy(auth.StepUpTOTP)), password, false},
		{"totp admits a TOTP sign-in", auth.WithStepUpSource(context.Background(), fixedPolicy(auth.StepUpTOTP)), withCode, true},
		{"totp admits a passkey step-up", auth.WithStepUpSource(context.Background(), fixedPolicy(auth.StepUpTOTP)), passkey, true},
		{"passkey refuses a TOTP sign-in", auth.WithStepUpSource(context.Background(), fixedPolicy(auth.StepUpPasskey)), withCode, false},
		{"passkey admits a passkey step-up", auth.WithStepUpSource(context.Background(), fixedPolicy(auth.StepUpPasskey)), passkey, true},
		{"an unreadable policy is strict", auth.WithStepUpSource(context.Background(), func(context.Context) (string, error) {
			return "", errors.New("store down")
		}), password, false},
		{"an unknown policy value is strict", auth.WithStepUpSource(context.Background(), fixedPolicy("sometimes")), password, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := auth.StepUpSatisfied(tc.ctx, tc.p); got != tc.wantOK {
				t.Fatalf("StepUpSatisfied = %v, want %v", got, tc.wantOK)
			}
		})
	}
}

func TestStepUpSourceIsReadOncePerContext(t *testing.T) {
	calls := 0
	ctx := auth.WithStepUpSource(context.Background(), func(context.Context) (string, error) {
		calls++
		return auth.StepUpNone, nil
	})
	p := auth.Principal{Kind: auth.KindUser, AAL: auth.AAL1}
	for range 3 {
		auth.StepUpSatisfied(ctx, p)
	}
	if calls != 1 {
		t.Fatalf("policy read %d times, want 1", calls)
	}
}

func TestStepUpMemoIsReReadAfterTheCacheWindow(t *testing.T) {
	defer auth.SetStepUpCacheTTLForTest(30 * time.Millisecond)()
	policy := auth.StepUpNone
	ctx := auth.WithStepUpSource(context.Background(), func(context.Context) (string, error) { return policy, nil })
	p := auth.Principal{Kind: auth.KindUser, AAL: auth.AAL1}
	if !auth.StepUpSatisfied(ctx, p) {
		t.Fatal("none must admit a password session")
	}
	policy = auth.StepUpPasskey
	if !auth.StepUpSatisfied(ctx, p) {
		t.Fatal("within the window the request keeps the policy it started with")
	}
	time.Sleep(60 * time.Millisecond)
	if auth.StepUpSatisfied(ctx, p) {
		t.Fatal("a request older than the window must see the raised policy")
	}
}

func TestAdminStepUpDefaultsToNoneAndBlocksNoCredentialChange(t *testing.T) {
	f := newTOTPFixture(t)
	if got, err := f.a.AdminStepUp(f.ctx); err != nil || got != auth.StepUpNone {
		t.Fatalf("AdminStepUp on a fresh store = %q, %v; want none", got, err)
	}
	res, _ := f.loginAs(f.user.Email)
	f.enrolFactor(res.Token)
	principal, err := f.a.Authenticate(f.ctx, res.Token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	// A password session may replace its own factor under the default policy.
	if _, err := f.a.BeginTOTPEnrolment(f.ctx, principal, "Olivares AI"); err != nil {
		t.Fatalf("replace at AAL1 under the default policy: %v", err)
	}
}

func TestSetAdminStepUpCannotLockTheCallerOut(t *testing.T) {
	f := newTOTPFixture(t)
	res, err := f.loginAs(f.admin.Email)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	admin, err := f.a.Authenticate(f.ctx, res.Token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if err := f.a.SetAdminStepUp(f.ctx, admin, auth.StepUpPasskey); !errors.Is(err, auth.ErrStepUpPasskeyMissing) {
		t.Fatalf("passkey without an enrolled passkey err = %v, want ErrStepUpPasskeyMissing", err)
	}
	if err := f.a.SetAdminStepUp(f.ctx, admin, auth.StepUpTOTP); !errors.Is(err, auth.ErrStepUpTOTPUnproven) {
		t.Fatalf("totp from a password-only session err = %v, want ErrStepUpTOTPUnproven", err)
	}
	if err := f.a.SetAdminStepUp(f.ctx, admin, "sometimes"); !errors.Is(err, auth.ErrStepUpPolicyInvalid) {
		t.Fatalf("unknown value err = %v, want ErrStepUpPolicyInvalid", err)
	}
	if got, _ := f.a.AdminStepUp(f.ctx); got != auth.StepUpNone {
		t.Fatalf("a refused raise changed the policy to %q", got)
	}
	// Lowering needs the current level: a session below it cannot remove it.
	for _, from := range []string{auth.StepUpTOTP, auth.StepUpPasskey} {
		setStepUpPolicy(t, f.ctx, f.st, from)
		f.a.ReloadStepUp()
		if err := f.a.SetAdminStepUp(f.ctx, admin, auth.StepUpNone); !errors.Is(err, auth.ErrStepUpRequired) {
			t.Fatalf("turning %s off at AAL1 err = %v, want ErrStepUpRequired", from, err)
		}
		if got, _ := f.a.AdminStepUp(f.ctx); got != from {
			t.Fatalf("a refused lowering changed the policy from %s to %q", from, got)
		}
	}
	// A federated sign-in with MFA meets totp, so it may lower it.
	setStepUpPolicy(t, f.ctx, f.st, auth.StepUpTOTP)
	f.a.ReloadStepUp()
	federated := admin
	federated.AAL, federated.AMR = auth.AAL2, []string{"sso", "mfa"}
	if err := f.a.SetAdminStepUp(f.ctx, federated, auth.StepUpNone); err != nil {
		t.Fatalf("turning totp off from a federated MFA sign-in: %v", err)
	}
	setStepUpPolicy(t, f.ctx, f.st, auth.StepUpPasskey)
	f.a.ReloadStepUp()
	stepped := admin
	stepped.AAL = auth.AAL3
	if err := f.a.SetAdminStepUp(f.ctx, stepped, auth.StepUpNone); err != nil {
		t.Fatalf("turning passkey off after a passkey step-up: %v", err)
	}
	if got, _ := f.a.AdminStepUp(f.ctx); got != auth.StepUpNone {
		t.Fatalf("policy after turning it off = %q, want none", got)
	}
	var recorded bool
	refused := 0
	if err := f.st.AuthView(f.ctx, func(as store.AuthScope) error {
		return as.Audit().Walk(f.ctx, 0, func(ev model.AuditEvent) error {
			switch ev.Action {
			case "auth.stepup.policy":
				recorded = true
			case "auth.stepup.failed":
				refused++
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if !recorded {
		t.Fatal("the policy change left no auth.stepup.policy audit event")
	}
	if refused != 2 {
		t.Fatalf("refused lowerings left %d auth.stepup.failed audit events, want 2", refused)
	}
}
