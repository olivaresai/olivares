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

// TestTokenAuthenticationRecordsLastUse pins last-use tracking on the bearer
// path: an API token that authenticates gets LastUsedAt, a busy token is
// written at most once a minute, and the stamp never changes the row's version
// or updated_at, so a credential pinned to the token's version stays valid.
func TestTokenAuthenticationRecordsLastUse(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	clock := newStepClock()
	a := auth.NewAuthenticator(st, clock)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "last-use")
	token, issued, err := a.IssueToken(ctx, super, auth.TokenSpec{
		Name: "audit-ci", BoundTenant: tenant, Role: auth.RoleViewer,
	})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if issued.LastUsedAt != nil {
		t.Fatalf("a fresh token already has LastUsedAt %v", issued.LastUsedAt)
	}

	use := func() model.APIToken {
		t.Helper()
		if _, err := a.Authenticate(ctx, token); err != nil {
			t.Fatalf("authenticate token: %v", err)
		}
		got, err := a.GetToken(ctx, issued.ID)
		if err != nil {
			t.Fatalf("read token: %v", err)
		}
		if got.Version != issued.Version || !got.UpdatedAt.Time().Equal(issued.UpdatedAt.Time()) {
			t.Fatalf("recording a use changed version/updated_at: %d/%v, was %d/%v",
				got.Version, got.UpdatedAt, issued.Version, issued.UpdatedAt)
		}
		return got
	}

	first := clock.Now()
	got := use()
	if got.LastUsedAt == nil || !got.LastUsedAt.Time().Equal(first.Time()) {
		t.Fatalf("LastUsedAt after the first use = %v, want %v", got.LastUsedAt, first)
	}
	clock.advance(30 * time.Second)
	if got = use(); !got.LastUsedAt.Time().Equal(first.Time()) {
		t.Fatalf("a use inside the minute rewrote LastUsedAt to %v, want %v", got.LastUsedAt, first)
	}
	clock.advance(31 * time.Second)
	if got = use(); !got.LastUsedAt.Time().Equal(clock.Now().Time()) {
		t.Fatalf("a use past the minute left LastUsedAt at %v, want %v", got.LastUsedAt, clock.Now())
	}
}

// lastUseWriteRefusingStore reads through and refuses every auth write,
// counting the attempts.
type lastUseWriteRefusingStore struct {
	store.Store
	attempts *int
}

func (s lastUseWriteRefusingStore) AuthMutate(context.Context, func(store.AuthScope) error) error {
	*s.attempts++
	return errors.New("writer unavailable")
}

// TestTokenUseWriteFailureStillAuthenticates pins recordTokenUse's choices: a
// token that proved itself authenticates even when its last use cannot be
// written, and a refused write is retried once a minute, not on every request.
func TestTokenUseWriteFailureStillAuthenticates(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	clock := newStepClock()
	a := auth.NewAuthenticator(st, clock)
	token, issued, err := a.IssueToken(ctx, mustSuperadmin(t, ctx, a), auth.TokenSpec{
		Name: "ci", Superadmin: true,
	})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	var attempts int
	refusing := auth.NewAuthenticator(lastUseWriteRefusingStore{st, &attempts}, clock)
	for i, wantAttempts := range []int{1, 1} {
		p, err := refusing.Authenticate(ctx, token)
		if err != nil || p.CredID != issued.ID {
			t.Fatalf("authenticate #%d with the last-use write refused = %v (cred %s), want the token principal",
				i+1, err, p.CredID)
		}
		if attempts != wantAttempts {
			t.Fatalf("after authentication #%d: last-use write attempts = %d, want %d", i+1, attempts, wantAttempts)
		}
	}
	clock.advance(61 * time.Second)
	if _, err := refusing.Authenticate(ctx, token); err != nil || attempts != 2 {
		t.Fatalf("a minute later: authenticate = %v, attempts = %d, want nil and 2", err, attempts)
	}
}

// TestRefusedTokenRecordsNoUse: a presentation that does not authenticate
// writes nothing, so a refused attempt never reads as a use and a caller who
// holds only a selector has no write path.
func TestRefusedTokenRecordsNoUse(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	clock := newStepClock()
	a := auth.NewAuthenticator(st, clock)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "refused-use")
	issue := func(name string, expires *model.Timestamp) (string, model.ID) {
		t.Helper()
		token, stored, err := a.IssueToken(ctx, super, auth.TokenSpec{
			Name: name, BoundTenant: tenant, Role: auth.RoleViewer, ExpiresAt: expires,
		})
		if err != nil {
			t.Fatalf("issue %s: %v", name, err)
		}
		return token, stored.ID
	}
	live, _ := issue("live", nil)
	revoked, revokedID := issue("revoked", nil)
	if err := a.RevokeToken(ctx, super, revokedID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	soon := model.NewTimestamp(clock.Now().Time().Add(30 * time.Second))
	expired, _ := issue("expired", &soon)
	clock.advance(time.Minute)

	var attempts int
	refusing := auth.NewAuthenticator(lastUseWriteRefusingStore{st, &attempts}, clock)
	for name, bearer := range map[string]string{
		"wrong secret": live + "0",
		"revoked":      revoked,
		"expired":      expired,
	} {
		if _, err := refusing.Authenticate(ctx, bearer); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Errorf("%s: authenticate = %v, want ErrUnauthenticated", name, err)
		}
	}
	if attempts != 0 {
		t.Fatalf("refused presentations attempted %d last-use writes, want 0", attempts)
	}
}

// TestAuthenticatePEPRecordsLastUse is the same contract on the PEP transport
// credential, the other path that authenticates an API token.
func TestAuthenticatePEPRecordsLastUse(t *testing.T) {
	f := newPEPFixture(t)
	service := f.register(t, "last-use-pep", testPDPAudience, nil)
	bearer, token := f.bind(t, service, testPDPAudience)
	if _, err := f.a.AuthenticatePEP(f.ctx, bearer); err != nil {
		t.Fatalf("AuthenticatePEP: %v", err)
	}
	got, err := f.a.GetToken(f.ctx, token.ID)
	if err != nil {
		t.Fatalf("read PEP credential: %v", err)
	}
	if got.LastUsedAt == nil {
		t.Fatal("AuthenticatePEP left the credential's LastUsedAt unset")
	}

	// The same bearer is refused as an ordinary API credential, and that refusal
	// writes nothing.
	var attempts int
	refusing := auth.NewAuthenticator(lastUseWriteRefusingStore{f.st, &attempts}, nil)
	if _, err := refusing.Authenticate(f.ctx, bearer); !errors.Is(err, auth.ErrUnauthenticated) || attempts != 0 {
		t.Fatalf("ordinary authentication of a PEP credential = %v with %d last-use writes, want ErrUnauthenticated and 0",
			err, attempts)
	}
}
