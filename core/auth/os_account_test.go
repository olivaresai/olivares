// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/nativepam"
	"github.com/olivaresai/olivares/core/store"
)

// Only the physical NSS/PAM boundary is substituted; bindings, exact native
// product credentials, authorization, lifecycle and audit use the real store.
type osAccountNative struct {
	uid         uint32
	login       string
	checks      int
	deny        bool
	accountDeny bool
	after       func()
}

func (n *osAccountNative) UID(context.Context, string) (uint32, error)  { return n.uid, nil }
func (n *osAccountNative) Name(context.Context, uint32) (string, error) { return n.login, nil }
func (n *osAccountNative) Verify(_ context.Context, login string, password []byte) (nativepam.Result, error) {
	n.checks++
	if n.after != nil {
		n.after()
	}
	defer clear(password)
	if n.deny {
		return nativepam.Result{}, nativepam.ErrRefused
	}
	return nativepam.Result{Authenticated: true, AccountAllowed: !n.accountDeny, Login: login, UID: n.uid}, nil
}
func osAdminSession(f *credentialBindingFixture) Principal {
	p, _, _ := f.session(f.admin, func(s *model.AuthSession) {
		s.AAL = AAL3
		s.AMR = []string{"webauthn"}
		at := model.NewTimestamp(time.Now().Add(-time.Second))
		until := model.NewTimestamp(time.Now().Add(time.Minute))
		s.AALAuthenticatedAt = &at
		s.AALExpiresAt = &until
	})
	return p
}
func osBindings(f *credentialBindingFixture, n *osAccountNative) *OSAccountBindings {
	return NewOSAccountBindings(f.a, NewAuthorizer(nil), n)
}
func TestOSAccountBindingRequiresSeparateFreshSubjectProof(t *testing.T) {
	f := newCredentialBindingFixture(t)
	n := &osAccountNative{uid: 1201, login: "native-alice"}
	b := osBindings(f, n)
	admin := osAdminSession(f)
	subject, _, session := f.session(f.userA, nil)
	ceremony, err := b.Begin(f.deadline(), admin, f.tenant, f.userA.ID, n.login)
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("native-secret")
	if _, err = b.Complete(f.deadline(), admin, ceremony.ID, secret); err == nil || n.checks != 0 {
		t.Fatalf("admin cannot authenticate subject: %v calls=%d", err, n.checks)
	}
	for _, v := range secret {
		if v != 0 {
			t.Fatal("refused request retained secret")
		}
	}
	ceremony, err = b.Begin(f.deadline(), admin, f.tenant, f.userA.ID, n.login)
	if err != nil {
		t.Fatal(err)
	}
	mapping, err := b.Complete(f.deadline(), subject, ceremony.ID, []byte("native-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if mapping.User != f.userA.ID || mapping.UID != 1201 || mapping.Account != n.login || mapping.Digest == "" {
		t.Fatalf("mapping=%+v", mapping)
	}
	ref, tenant, err := b.Resolve(f.deadline(), 1201, n.login)
	if err != nil || tenant != f.tenant || pinnedRevision(ref) != pinnedRevision(subject.credentialRef) {
		t.Fatalf("exact subject reference: %v", err)
	}
	if _, err = b.Complete(f.deadline(), subject, ceremony.ID, []byte("replay")); err == nil {
		t.Fatal("ceremony replay admitted")
	}
	if err = f.st.AuthMutate(f.ctx, func(as store.AuthScope) error {
		s, e := as.Sessions().Get(f.ctx, session.ID)
		if e != nil {
			return e
		}
		s.Revoked = true
		_, e = as.Sessions().Update(f.ctx, s)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = b.Resolve(f.deadline(), 1201, n.login); err == nil {
		t.Fatal("revoked exact subject credential resolved")
	}
}
func TestOSAccountBindingImmutableOwnershipAndRevocation(t *testing.T) {
	f := newCredentialBindingFixture(t)
	n := &osAccountNative{uid: 1202, login: "native-bob"}
	b := osBindings(f, n)
	admin := osAdminSession(f)
	subject, _, _ := f.session(f.userA, nil)
	create := func(user model.ID, p Principal) error {
		c, e := b.Begin(f.deadline(), admin, f.tenant, user, n.login)
		if e != nil {
			return e
		}
		_, e = b.Complete(f.deadline(), p, c.ID, []byte("secret"))
		return e
	}
	if err := create(f.userA.ID, subject); err != nil {
		t.Fatal(err)
	}
	if err := create(f.userA.ID, subject); err != nil {
		t.Fatalf("identical current tuple: %v", err)
	}
	if err := create(f.admin.ID, admin); !errors.Is(err, ErrCredentialBindingConflict) {
		t.Fatalf("UID reassignment: %v", err)
	}
	if err := b.Revoke(f.deadline(), admin, f.tenant, f.userA.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Resolve(f.deadline(), n.uid, n.login); err == nil {
		t.Fatal("revoked mapping resolved")
	}
	if err := create(f.userA.ID, subject); err == nil {
		t.Fatal("revoked owner reservation reopened")
	}
	if err := create(f.admin.ID, admin); err == nil {
		t.Fatal("revoked UID reservation reassigned")
	}
}
func TestOSAccountBindingRefusesWeakChangedOrUnprovenControl(t *testing.T) {
	f := newCredentialBindingFixture(t)
	n := &osAccountNative{uid: 1203, login: "native-carol"}
	b := osBindings(f, n)
	admin := osAdminSession(f)
	subject, _, row := f.session(f.userA, nil)
	weak, _, _ := f.session(f.admin, nil)
	if _, e := b.Begin(WithStepUpSource(f.deadline(), func(context.Context) (string, error) { return StepUpPasskey, nil }), weak, f.tenant, f.userA.ID, n.login); !errors.Is(e, ErrStepUpRequired) {
		t.Fatalf("weak admin: %v", e)
	}
	n.uid = 0
	if _, e := b.Begin(f.deadline(), admin, f.tenant, f.userA.ID, n.login); e == nil {
		t.Fatal("UID0 admitted")
	}
	n.uid = 1203
	c, e := b.Begin(f.deadline(), admin, f.tenant, f.userA.ID, n.login)
	if e != nil {
		t.Fatal(e)
	}
	fake := Principal{Kind: KindUser, UserID: f.userA.ID, CredID: row.ID, AAL: AAL3}
	if _, e = b.Complete(f.deadline(), fake, c.ID, []byte("secret")); e == nil || n.checks != 0 {
		t.Fatal("copied principal authenticated subject")
	}
	c, e = b.Begin(f.deadline(), admin, f.tenant, f.userA.ID, n.login)
	if e != nil {
		t.Fatal(e)
	}
	n.uid++
	if _, e = b.Complete(f.deadline(), subject, c.ID, []byte("secret")); e == nil || n.checks != 0 {
		t.Fatal("changed UID presented to PAM")
	}
	n.uid--
	c, e = b.Begin(f.deadline(), admin, f.tenant, f.userA.ID, n.login)
	if e != nil {
		t.Fatal(e)
	}
	n.deny = true
	if _, e = b.Complete(f.deadline(), subject, c.ID, []byte("secret")); e == nil {
		t.Fatal("failed PAM control bound account")
	}
	ctx, cancel := context.WithDeadline(f.ctx, time.Now().Add(-time.Second))
	defer cancel()
	if _, e = b.Begin(ctx, admin, f.tenant, f.userA.ID, n.login); e == nil {
		t.Fatal("expired request admitted")
	}
}

func TestOSAccountBindingRechecksBothProofsAfterPAM(t *testing.T) {
	for _, which := range []string{"admin", "subject", "account"} {
		t.Run(which, func(t *testing.T) {
			f := newCredentialBindingFixture(t)
			n := &osAccountNative{uid: 1401, login: "native-proof"}
			b := osBindings(f, n)
			admin := osAdminSession(f)
			subject, _, _ := f.session(f.userA, nil)
			c, err := b.Begin(f.deadline(), admin, f.tenant, f.userA.ID, n.login)
			if err != nil {
				t.Fatal(err)
			}
			if which == "account" {
				n.accountDeny = true
			} else {
				user := f.userA
				if which == "admin" {
					user = f.admin
				}
				n.after = func() {
					if err := f.st.AuthMutate(f.ctx, func(as store.AuthScope) error {
						current, e := as.Users().Get(f.ctx, user.ID)
						if e != nil {
							return e
						}
						current.Status = model.StatusInactive
						_, e = as.Users().Update(f.ctx, current)
						return e
					}); err != nil {
						t.Fatal(err)
					}
				}
			}
			secret := []byte("native-secret")
			if _, err = b.Complete(f.deadline(), subject, c.ID, secret); err == nil {
				t.Fatal("changed authority or failed Account admitted")
			}
			if n.checks != 1 {
				t.Fatalf("native checks=%d", n.checks)
			}
			for _, v := range secret {
				if v != 0 {
					t.Fatal("secret retained")
				}
			}
			if _, _, err = b.Resolve(f.deadline(), n.uid, n.login); err == nil {
				t.Fatal("refused proof persisted authority")
			}
		})
	}
}
