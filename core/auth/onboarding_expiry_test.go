// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The onboarding invitation expiry boundary (onboarding.go: AcceptInvite and
// ListPendingInvites). An invitation is VALID strictly before its stored
// expires_at and EXPIRED from that instant onward: the window is half-open,
// [created, expires_at). The exact expiration instant belongs to the expired
// side, so redemption at now == expires_at is refused and the invitation is
// absent from the pending list at that same instant.
//
// PRECISION. expires_at is persisted as the canonical timestamp text
// (core/model tsLayout: RFC3339 in UTC with NINE fixed fractional digits), so
// the boundary is exact to the NANOSECOND on both engines and the adjacent
// cases below are one nanosecond apart, not one second. The fixture clock is
// deliberately offset onto a non-zero nanosecond so a predicate that only ever
// saw whole seconds cannot pass by accident, and the fixture asserts the
// store round trip keeps every digit it reasons about.
//
// The refusal stays the coarse, pre-existing ErrInviteInvalid (unknown, expired
// and already-used are one error, so the accept leg is never an existence
// oracle); these tests pin the boundary, not a new error.

const (
	inviteExpiryEmail    = "expiry-invitee@acme.test"
	inviteExpiryPassword = "invitee-chosen-pw-1"
	inviteExpiryIP       = "203.0.113.7"
	// inviteExpiryNanos offsets the fixture clock off a whole second.
	inviteExpiryNanos = 123456789
)

// inviteExpiryFixture is one issued, unaccepted invitation on a fresh in-memory
// SQLite store, with the real Authenticator driven by a manually advanced clock.
type inviteExpiryFixture struct {
	st       store.Store
	a        *auth.Authenticator
	clock    *stepClock
	tenant   model.TenantID
	userID   model.ID
	inviteID model.ID
	token    string
	expires  model.Timestamp
}

func newInviteExpiryFixture(t *testing.T) *inviteExpiryFixture {
	t.Helper()
	ctx := context.Background()
	st := testStore(t)
	clk := newStepClock()
	clk.advance(inviteExpiryNanos * time.Nanosecond)
	a := auth.NewAuthenticator(st, clk)
	tenant := provisionTenant(t, st, "acme")

	res, err := a.OnboardMember(ctx, fedTestActor(), tenant, auth.OnboardInput{
		Email: inviteExpiryEmail, DisplayName: "Invitee", Role: auth.RoleViewer, Invite: true,
	})
	if err != nil {
		t.Fatalf("onboard in invite mode: %v", err)
	}
	if !res.Created || res.InviteToken == "" || res.InviteID.IsZero() || res.ExpiresAt == nil {
		t.Fatalf("invite onboarding must create the account and mint a token: %+v", res)
	}
	f := &inviteExpiryFixture{
		st: st, a: a, clock: clk, tenant: tenant,
		userID: res.User.ID, inviteID: res.InviteID, token: res.InviteToken, expires: *res.ExpiresAt,
	}

	// The predicate compares the clock against the STORED instant, so the boundary
	// cases are only exact if the persisted text keeps every digit.
	stored := f.state(t).invite
	if got, want := stored.ExpiresAt.String(), f.expires.String(); got != want {
		t.Fatalf("persisted expires_at = %s, want %s (nanosecond precision must survive the store round trip)", got, want)
	}
	if got := stored.ExpiresAt.Time().Nanosecond(); got != inviteExpiryNanos {
		t.Fatalf("persisted expires_at nanoseconds = %d, want %d", got, inviteExpiryNanos)
	}
	return f
}

// inviteExpiryState is everything a redemption is allowed to move: the account's
// credential and status, the invitation's single-use marker, the account's
// sessions and the system-tenant evidence chain tip.
type inviteExpiryState struct {
	user      model.User
	invite    model.UserInvite
	sessions  int
	auditSeq  int64
	auditHash []byte
}

func (f *inviteExpiryFixture) state(t *testing.T) inviteExpiryState {
	t.Helper()
	ctx := context.Background()
	var s inviteExpiryState
	if err := f.st.AuthView(ctx, func(as store.AuthScope) error {
		inv, err := as.Invites().Get(ctx, f.inviteID)
		if err != nil {
			return err
		}
		u, err := as.Users().Get(ctx, f.userID)
		if err != nil {
			return err
		}
		sessions, _, err := as.Sessions().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: f.userID.String()}},
			Limit:   100,
		})
		if err != nil {
			return err
		}
		head, _, err := as.Audit().Head(ctx)
		if err != nil {
			return err
		}
		s = inviteExpiryState{
			user: u, invite: inv, sessions: len(sessions),
			auditSeq: head.Seq, auditHash: bytes.Clone(head.Hash),
		}
		return nil
	}); err != nil {
		t.Fatalf("read invite state: %v", err)
	}
	return s
}

// setClock moves the fixture clock to exactly ts (it only ever moves forward).
func (f *inviteExpiryFixture) setClock(t *testing.T, ts model.Timestamp) {
	t.Helper()
	d := ts.Time().Sub(f.clock.Now().Time())
	if d < 0 {
		t.Fatalf("the fixture clock only moves forward: %s is before %s", ts, f.clock.Now())
	}
	f.clock.advance(d)
	if got := f.clock.Now().String(); got != ts.String() {
		t.Fatalf("clock = %s, want %s", got, ts)
	}
}

// shiftTS offsets a timestamp, preserving nanosecond precision.
func shiftTS(ts model.Timestamp, d time.Duration) model.Timestamp {
	return model.NewTimestamp(ts.Time().Add(d))
}

// assertInviteStateUnchanged proves a refused redemption wrote nothing: no
// password, no status change, no AcceptedAt, no session and no audit event.
func assertInviteStateUnchanged(t *testing.T, before, after inviteExpiryState) {
	t.Helper()
	if after.user.PasswordHash != before.user.PasswordHash {
		t.Errorf("refused redemption changed the account password hash (%q -> %q)",
			before.user.PasswordHash, after.user.PasswordHash)
	}
	if after.user.Status != before.user.Status {
		t.Errorf("refused redemption changed the account status (%s -> %s)", before.user.Status, after.user.Status)
	}
	if after.user.Version != before.user.Version || after.user.UpdatedAt.String() != before.user.UpdatedAt.String() {
		t.Errorf("refused redemption wrote the account row (version %d/%s -> %d/%s)",
			before.user.Version, before.user.UpdatedAt, after.user.Version, after.user.UpdatedAt)
	}
	if after.invite.AcceptedAt != nil {
		t.Errorf("refused redemption marked the invitation accepted at %s", after.invite.AcceptedAt)
	}
	if after.invite.Version != before.invite.Version || after.invite.UpdatedAt.String() != before.invite.UpdatedAt.String() {
		t.Errorf("refused redemption wrote the invitation row (version %d/%s -> %d/%s)",
			before.invite.Version, before.invite.UpdatedAt, after.invite.Version, after.invite.UpdatedAt)
	}
	if after.sessions != before.sessions {
		t.Errorf("refused redemption minted a session (%d -> %d sessions)", before.sessions, after.sessions)
	}
	if after.auditSeq != before.auditSeq || !bytes.Equal(after.auditHash, before.auditHash) {
		t.Errorf("refused redemption appended evidence (audit tip seq %d -> %d)", before.auditSeq, after.auditSeq)
	}
}

// pendingHasInvite reports whether the console's pending list carries id.
func pendingHasInvite(invites []model.UserInvite, id model.ID) bool {
	for _, inv := range invites {
		if inv.ID == id {
			return true
		}
	}
	return false
}

// TestOnboardingInviteIsValidStrictlyBeforeExpiry is the discriminating positive
// control for the boundary: one nanosecond before expires_at the invitation is
// still listed and still redeemable, and the redemption really ACTIVATES the
// account and really hands back a working session — not merely a nil error.
func TestOnboardingInviteIsValidStrictlyBeforeExpiry(t *testing.T) {
	for _, tc := range []struct {
		name string
		when func(f *inviteExpiryFixture) model.Timestamp
	}{
		{"at issue time", func(f *inviteExpiryFixture) model.Timestamp { return f.clock.Now() }},
		{"one nanosecond before expiry", func(f *inviteExpiryFixture) model.Timestamp {
			return shiftTS(f.expires, -time.Nanosecond)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newInviteExpiryFixture(t)
			f.setClock(t, tc.when(f))

			// The invited account exists and is already StatusActive, but it was created
			// with NO password hash, so the password the invitee is about to choose does
			// not work yet. That is what makes the acceptance below an observed
			// activation rather than a nil error over an already-usable account.
			if _, _, err := f.a.Login(ctx, inviteExpiryEmail, inviteExpiryPassword, inviteExpiryIP); !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Fatalf("login before acceptance = %v, want ErrInvalidCredentials (the account has no password yet)", err)
			}

			pending, err := f.a.ListPendingInvites(ctx, f.tenant)
			if err != nil {
				t.Fatalf("list pending invites: %v", err)
			}
			if !pendingHasInvite(pending, f.inviteID) {
				t.Fatalf("pending invites = %d entries without %s, want the unexpired invitation listed", len(pending), f.inviteID)
			}

			token, sess, err := f.a.AcceptInvite(ctx, f.token, inviteExpiryPassword, inviteExpiryIP)
			if err != nil {
				t.Fatalf("AcceptInvite strictly before expires_at (%s at %s): %v", f.expires, f.clock.Now(), err)
			}
			if token == "" || sess.ID.IsZero() {
				t.Fatalf("acceptance must return a minted session; token empty=%v session=%+v", token == "", sess)
			}
			if sess.UserID != f.userID {
				t.Fatalf("minted session user = %s, want the invited account %s", sess.UserID, f.userID)
			}

			// Session receipt: the returned bearer really resolves to the invited account.
			p, err := f.a.Authenticate(ctx, token)
			if err != nil {
				t.Fatalf("the returned session token must authenticate: %v", err)
			}
			if p.UserID != f.userID || p.CredID != sess.ID {
				t.Fatalf("authenticated principal = (user %s, cred %s), want (user %s, cred %s)", p.UserID, p.CredID, f.userID, sess.ID)
			}

			// Activation: the invitee can now log in with the password they just set.
			if _, _, err := f.a.Login(ctx, inviteExpiryEmail, inviteExpiryPassword, inviteExpiryIP); err != nil {
				t.Fatalf("login after acceptance with the chosen password: %v", err)
			}

			after := f.state(t)
			if after.user.PasswordHash == "" {
				t.Errorf("acceptance must persist the chosen password hash")
			}
			if after.user.Status != model.StatusActive {
				t.Errorf("accepted account status = %s, want %s", after.user.Status, model.StatusActive)
			}
			if after.invite.AcceptedAt == nil {
				t.Fatalf("acceptance must mark the invitation used (single use)")
			}
			if got, want := after.invite.AcceptedAt.String(), f.clock.Now().String(); got != want {
				t.Errorf("accepted_at = %s, want the redemption instant %s", got, want)
			}

			// A redeemed invitation leaves the pending list (it is no longer unaccepted).
			pending, err = f.a.ListPendingInvites(ctx, f.tenant)
			if err != nil {
				t.Fatalf("list pending invites after acceptance: %v", err)
			}
			if pendingHasInvite(pending, f.inviteID) {
				t.Errorf("the redeemed invitation must not stay in the pending list")
			}
		})
	}
}

// TestOnboardingInviteIsExpiredFromTheExactExpirationInstant pins the boundary
// itself. At now == expires_at the invitation is already expired: it is absent
// from the pending list and its redemption is refused with the coarse
// ErrInviteInvalid, writing nothing. The one-nanosecond case is the adjacent
// instant to the positive control above; the later cases prove it stays refused.
func TestOnboardingInviteIsExpiredFromTheExactExpirationInstant(t *testing.T) {
	for _, tc := range []struct {
		name   string
		offset time.Duration
	}{
		{"exactly at the stored expiration instant", 0},
		{"one nanosecond after expiry", time.Nanosecond},
		{"a day after expiry", 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newInviteExpiryFixture(t)
			f.setClock(t, shiftTS(f.expires, tc.offset))
			before := f.state(t)

			pending, err := f.a.ListPendingInvites(ctx, f.tenant)
			if err != nil {
				t.Fatalf("list pending invites: %v", err)
			}
			if pendingHasInvite(pending, f.inviteID) {
				t.Errorf("the invitation is expired at %s (expires_at %s) and must not be listed as pending", f.clock.Now(), f.expires)
			}

			token, sess, err := f.a.AcceptInvite(ctx, f.token, inviteExpiryPassword, inviteExpiryIP)
			if !errors.Is(err, auth.ErrInviteInvalid) {
				t.Errorf("AcceptInvite at %s (expires_at %s) = %v, want ErrInviteInvalid", f.clock.Now(), f.expires, err)
			}
			if token != "" || !sess.ID.IsZero() {
				t.Errorf("a refused redemption must return no credential; token empty=%v session=%+v", token == "", sess)
			}

			assertInviteStateUnchanged(t, before, f.state(t))

			// And the account is still unusable: the refused redemption set no password.
			if _, _, err := f.a.Login(ctx, inviteExpiryEmail, inviteExpiryPassword, inviteExpiryIP); !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Errorf("login after a refused redemption = %v, want ErrInvalidCredentials", err)
			}
		})
	}
}
