// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestInviteLoginPolicyRefusal(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		policy       fakeLoginPolicy
		want         error
		identified   bool
	}{
		{"network", "network_not_allowed", fakeLoginPolicy{networkErr: auth.ErrNetworkNotAllowed}, auth.ErrNetworkNotAllowed, false},
		{"require_sso", "sso_required", fakeLoginPolicy{ssoErr: auth.ErrSSORequired}, auth.ErrSSORequired, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newInviteExpiryFixture(t)
			f.a.WithLoginPolicy(&tc.policy)
			before := f.state(t)
			token, session, err := f.a.AcceptInvite(context.Background(), f.token, inviteExpiryPassword, inviteExpiryIP)
			if !errors.Is(err, tc.want) {
				t.Errorf("invite acceptance error = %v, want %v", err, tc.want)
			}
			if token != "" || !session.ID.IsZero() {
				t.Error("policy refusal returned a session credential")
			}
			after := f.state(t)
			if after.invite.AcceptedAt != nil || after.user.PasswordHash != before.user.PasswordHash || after.user.Status != before.user.Status || after.sessions != before.sessions {
				t.Error("policy refusal consumed invitation, changed account or created a session")
			}
			var events []model.AuditEvent
			if err := f.st.AuthView(context.Background(), func(as store.AuthScope) error {
				walker, ok := as.Audit().(store.CanonicalWalker)
				if !ok {
					return errors.New("audit lacks canonical metadata")
				}
				return walker.WalkCanonical(context.Background(), before.auditSeq+1, func(e model.AuditEvent, meta string, _ []byte) error {
					if err := json.Unmarshal([]byte(meta), &e.Meta); err != nil {
						return err
					}
					events = append(events, e)
					return nil
				})
			}); err != nil {
				t.Fatal(err)
			}
			actor := "anonymous"
			if tc.identified {
				actor = "user:" + f.userID.String()
			}
			if len(events) != 1 || events[0].Action != "auth.login.blocked" || events[0].Actor != actor || events[0].ActorKind != model.ActorUser || len(events[0].Meta) != 2 || events[0].Meta["reason"] != tc.reason || events[0].Meta["ip"] != inviteExpiryIP {
				t.Error("refusal must append exactly the existing login-blocked audit shape")
			}
			tc.policy.networkErr, tc.policy.ssoErr = nil, nil
			token, session, err = f.a.AcceptInvite(context.Background(), f.token, inviteExpiryPassword, inviteExpiryIP)
			if err != nil || token == "" || session.UserID != f.userID {
				t.Errorf("retained invitation cannot be accepted after policy permits it: %v", err)
			}
		})
	}
}

// These callbacks vary only the public policy port. In particular, reading or
// changing the store here proves admission does not hold a transaction open.
type inviteAdmissionPolicy struct {
	network  func(context.Context, string) error
	password func(context.Context, model.User) error
}

func (p inviteAdmissionPolicy) AllowNetwork(ctx context.Context, ip string) error {
	if p.network != nil {
		return p.network(ctx, ip)
	}
	return nil
}
func (p inviteAdmissionPolicy) RequireSSO(ctx context.Context, user model.User) error {
	if p.password != nil {
		return p.password(ctx, user)
	}
	return nil
}

func TestInviteLoginPolicyAllowedAndNil(t *testing.T) {
	for _, wired := range []bool{false, true} {
		t.Run(map[bool]string{false: "nil", true: "allowed"}[wired], func(t *testing.T) {
			f := newInviteExpiryFixture(t)
			before := f.state(t)
			var networkCalls, passwordCalls int
			if wired {
				f.a.WithLoginPolicy(inviteAdmissionPolicy{
					network: func(ctx context.Context, ip string) error {
						networkCalls++
						if ip != "10.1.2.3" {
							return auth.ErrNetworkNotAllowed
						}
						return nil
					},
					password: func(ctx context.Context, user model.User) error {
						passwordCalls++
						if user.ID != before.user.ID || user.Version != before.user.Version || user.IsSuperadmin != before.user.IsSuperadmin {
							t.Error("policy did not receive stored user authority")
						}
						return f.st.AuthView(ctx, func(as store.AuthScope) error { _, err := as.Users().Get(ctx, user.ID); return err })
					},
				})
			}
			token, session, err := f.a.AcceptInvite(context.Background(), f.token, inviteExpiryPassword, "10.1.2.3")
			if err != nil || token == "" || session.UserID != f.userID || len(session.AMR) != 1 || session.AMR[0] != "pwd" || session.AAL != 1 {
				t.Fatalf("permitted invitation acceptance failed: %v", err)
			}
			if _, err := f.a.Authenticate(context.Background(), token); err != nil {
				t.Fatalf("issued session cannot authenticate: %v", err)
			}
			if wired && (networkCalls != 1 || passwordCalls != 1) {
				t.Errorf("policy calls network=%d password=%d, want 1 each", networkCalls, passwordCalls)
			}
			if f.state(t).invite.AcceptedAt == nil {
				t.Error("successful acceptance did not consume invitation")
			}
		})
	}
}

func TestInviteLoginPolicyStorageErrorsRefuse(t *testing.T) {
	unavailable := errors.New("policy storage unavailable")
	for _, network := range []bool{true, false} {
		t.Run(map[bool]string{true: "network", false: "password"}[network], func(t *testing.T) {
			f := newInviteExpiryFixture(t)
			policy := &fakeLoginPolicy{ssoErr: unavailable}
			if network {
				policy.networkErr = unavailable
			}
			f.a.WithLoginPolicy(policy)
			before := f.state(t)
			token, _, err := f.a.AcceptInvite(context.Background(), f.token, inviteExpiryPassword, inviteExpiryIP)
			if !errors.Is(err, unavailable) || token != "" {
				t.Fatalf("unreadable policy must refuse: %v", err)
			}
			after := f.state(t)
			if after.invite.AcceptedAt != nil || after.user.PasswordHash != before.user.PasswordHash || after.sessions != before.sessions {
				t.Error("policy failure mutated invitation or account")
			}
		})
	}
}

func TestInviteLoginPolicyRevalidatesStoredUser(t *testing.T) {
	f := newInviteExpiryFixture(t)
	before := f.state(t)
	f.a.WithLoginPolicy(inviteAdmissionPolicy{password: func(ctx context.Context, user model.User) error {
		return f.st.AuthMutate(ctx, func(as store.AuthScope) error {
			user.Status = model.StatusInactive
			_, err := as.Users().Update(ctx, user)
			return err
		})
	}})
	token, _, err := f.a.AcceptInvite(context.Background(), f.token, inviteExpiryPassword, inviteExpiryIP)
	if !errors.Is(err, auth.ErrInviteInvalid) || token != "" {
		t.Fatalf("changed user must invalidate the admission snapshot: %v", err)
	}
	after := f.state(t)
	if after.invite.AcceptedAt != nil || after.user.PasswordHash != before.user.PasswordHash || after.sessions != before.sessions || after.user.Status != model.StatusInactive {
		t.Error("stale admission consumed invite or undid the concurrent user change")
	}
}

func TestInviteLoginPolicyInvalidTokenDoesNotRevealSSO(t *testing.T) {
	f := newInviteExpiryFixture(t)
	p := &fakeLoginPolicy{ssoErr: auth.ErrSSORequired}
	f.a.WithLoginPolicy(p)
	_, _, err := f.a.AcceptInvite(context.Background(), "invalid", inviteExpiryPassword, inviteExpiryIP)
	if !errors.Is(err, auth.ErrInviteInvalid) || p.sawSSO != 0 {
		t.Errorf("unproved invitation exposed password policy: %v, calls=%d", err, p.sawSSO)
	}
}
