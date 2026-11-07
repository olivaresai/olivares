// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// freshnessAnswers is a port whose answer per user the test sets.
type freshnessAnswers struct {
	mu  sync.Mutex
	at  map[model.ID]time.Time
	err error
}

func (f *freshnessAnswers) set(id model.ID, notAfter time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.at[id] = notAfter
}

func (f *freshnessAnswers) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *freshnessAnswers) source(_ context.Context, id model.ID) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.at[id], f.err
}

// staleRefusalsAudited counts the ledger rows that record a refusal for the stale reason.
func staleRefusalsAudited(t *testing.T, f *totpFixture) (blocked, stepUp int) {
	t.Helper()
	if err := f.st.AuthView(f.ctx, func(as store.AuthScope) error {
		walker, ok := as.Audit().(store.CanonicalWalker)
		if !ok {
			return errors.New("audit lacks canonical metadata")
		}
		return walker.WalkCanonical(f.ctx, 1, func(e model.AuditEvent, meta string, _ []byte) error {
			if err := json.Unmarshal([]byte(meta), &e.Meta); err != nil {
				return err
			}
			switch {
			case e.Action == "auth.login.blocked" && e.Meta["reason"] == "rights_data_stale":
				blocked++
			case e.Action == "auth.stepup.failed" && e.Meta["reason"] == "rights_data_stale":
				stepUp++
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("walk audit events: %v", err)
	}
	return blocked, stepUp
}

// TestFreshnessPortBoundsNewSignInsAndAdminStepUp holds the whole contract of the
// optional freshness port: past its instant a new sign-in by password, TOTP
// code or passkey step-up and the administrative step-up are refused with
// ErrRightsDataStale, while the session already issued and the account stay as
// they are; no instant, or no port, changes nothing.
func TestFreshnessPortBoundsNewSignInsAndAdminStepUp(t *testing.T) {
	f := newTOTPFixture(t)
	answers := &freshnessAnswers{at: map[model.ID]time.Time{}}

	// Sessions issued before any port exists: an administrator, and a user whose
	// TOTP factor is enrolled.
	adminRes, err := f.loginAs(f.admin.Email)
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}
	userRes, err := f.loginAs(f.user.Email)
	if err != nil {
		t.Fatalf("user login: %v", err)
	}
	f.enrolFactor(userRes.Token)
	admin, err := f.a.Authenticate(f.ctx, adminRes.Token)
	if err != nil {
		t.Fatalf("authenticate admin: %v", err)
	}
	policy := auth.WithStepUpSource(f.ctx, fixedPolicy(auth.StepUpNone))
	if !auth.StepUpSatisfied(policy, admin) {
		t.Fatal("precondition: an administrator session meets the none policy")
	}

	// The enrolled user holds a code challenge from before the answer goes stale.
	pending, err := f.loginAs(f.user.Email)
	if err != nil || pending.MFAToken == "" {
		t.Fatalf("precondition: a challenge for the enrolled user: %+v, %v", pending, err)
	}
	seed := currentTOTPSecret(t, f, f.user.ID)

	f.a.WithFreshnessSource(answers.source)

	// A future instant changes nothing.
	answers.set(f.admin.ID, time.Now().Add(time.Hour))
	if _, err := f.loginAs(f.admin.Email); err != nil {
		t.Fatalf("login inside the freshness bound: %v", err)
	}
	if p, err := f.a.Authenticate(f.ctx, adminRes.Token); err != nil || !auth.StepUpSatisfied(policy, p) {
		t.Fatalf("step-up inside the bound: err=%v", err)
	}

	// The instants pass.
	answers.set(f.admin.ID, time.Now().Add(-time.Second))
	answers.set(f.user.ID, time.Now().Add(-time.Second))

	if _, err := f.loginAs(f.admin.Email); !errors.Is(err, auth.ErrRightsDataStale) {
		t.Errorf("password sign-in past the instant err = %v, want ErrRightsDataStale", err)
	}
	if _, err := f.loginAs(f.user.Email); !errors.Is(err, auth.ErrRightsDataStale) {
		t.Errorf("password sign-in of a TOTP user past the instant err = %v, want ErrRightsDataStale (no challenge is issued)", err)
	}
	if _, _, err := f.a.CompleteTOTPLogin(f.ctx, pending.MFAToken, totpCodeFor(t, seed, time.Now()), "127.0.0.1", nil); !errors.Is(err, auth.ErrRightsDataStale) {
		t.Errorf("TOTP completion past the instant err = %v, want ErrRightsDataStale", err)
	}
	if _, err := f.a.ElevateSession(f.ctx, admin, "webauthn", auth.AAL3); !errors.Is(err, auth.ErrRightsDataStale) {
		t.Errorf("passkey step-up past the instant err = %v, want ErrRightsDataStale", err)
	}

	// Each refusal leaves a ledger row: three sign-ins (password x2, TOTP completion) and the step-up.
	if blocked, stepUp := staleRefusalsAudited(t, f); blocked != 3 || stepUp != 1 {
		t.Errorf("audited refusals = %d sign-ins, %d step-ups; want 3 and 1", blocked, stepUp)
	}

	// The session already issued still authenticates and keeps its rights; only
	// the administrative step-up is refused, with the same stable error.
	staleAdmin, err := f.a.Authenticate(f.ctx, adminRes.Token)
	if err != nil {
		t.Fatalf("an issued session must keep working past the instant: %v", err)
	}
	if staleAdmin.UserID != admin.UserID || staleAdmin.Superadmin != admin.Superadmin || staleAdmin.AAL != admin.AAL {
		t.Errorf("principal changed past the instant: %+v, before %+v", staleAdmin, admin)
	}
	if auth.StepUpSatisfied(policy, staleAdmin) {
		t.Error("the administrative step-up passed past the instant")
	}
	if got := auth.StepUpRefusal(policy, staleAdmin); !errors.Is(got, auth.ErrRightsDataStale) {
		t.Errorf("step-up refusal = %v, want ErrRightsDataStale", got)
	}
	if got := auth.StepUpRefusal(policy, auth.Principal{Kind: auth.KindUser, AAL: auth.AAL1}); !errors.Is(got, auth.ErrStepUpRequired) {
		t.Errorf("an ordinary refusal changed: %v", got)
	}

	// A port that cannot answer refuses a new sign-in rather than admitting it.
	answers.set(f.admin.ID, time.Time{})
	answers.fail(errors.New("source unreachable"))
	if _, err := f.loginAs(f.admin.Email); err == nil {
		t.Error("a port that cannot answer must not admit a new sign-in")
	}

	// Refreshed data (no instant any more) lifts every refusal.
	answers.fail(nil)
	if _, err := f.loginAs(f.admin.Email); err != nil {
		t.Errorf("sign-in after the data is refreshed: %v", err)
	}
	if p, err := f.a.Authenticate(f.ctx, adminRes.Token); err != nil || !auth.StepUpSatisfied(policy, p) {
		t.Errorf("step-up after the data is refreshed: err=%v", err)
	}
}
