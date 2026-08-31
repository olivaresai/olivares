// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// stepClock is a manually-advanced model.Clock for assurance-freshness tests.
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func newStepClock() *stepClock {
	return &stepClock{t: time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)}
}

func (c *stepClock) Now() model.Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return model.NewTimestamp(c.t)
}

func (c *stepClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// seedUser creates an active user with a real argon2id password hash.
func seedUser(t *testing.T, st store.Store, email, password string) model.ID {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	var id model.ID
	if err := st.AuthMutate(context.Background(), func(as store.AuthScope) error {
		u, err := as.Users().Create(context.Background(), model.User{
			Email: email, DisplayName: "Op", Status: model.StatusActive, PasswordHash: hash,
		})
		id = u.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestElevationLifecycle pins the assurance state machine end to end: a fresh
// password session reads AAL1/[pwd]; a verified step-up raises it to AAL3 and
// appends the method once; past the freshness window the EFFECTIVE level
// degrades back to AAL1 (fail-closed) while the AMR history stays; a dead
// session cannot be elevated.
func TestElevationLifecycle(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	clock := newStepClock()
	a := auth.NewAuthenticator(st, clock)
	seedUser(t, st, "op@x.io", "supersecret1")

	token, _, err := a.Login(ctx, "op@x.io", "supersecret1", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if p.AAL != 1 || len(p.AMR) != 1 || p.AMR[0] != "pwd" {
		t.Fatalf("fresh session AAL/AMR = %d %v, want 1 [pwd]", p.AAL, p.AMR)
	}

	// Elevate twice with the same method: level raised once, method deduped.
	if _, err := a.ElevateSession(ctx, p, "webauthn", auth.AAL3); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ElevateSession(ctx, p, "webauthn", auth.AAL3); err != nil {
		t.Fatal(err)
	}
	p, err = a.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if p.AAL != 3 || len(p.AMR) != 2 || p.AMR[1] != "webauthn" {
		t.Fatalf("elevated AAL/AMR = %d %v, want 3 [pwd webauthn]", p.AAL, p.AMR)
	}

	// Just inside the freshness window the elevation holds...
	clock.advance(auth.StepUpTTL - time.Minute)
	if p, err = a.Authenticate(ctx, token); err != nil || p.AAL != 3 {
		t.Fatalf("inside window AAL = %d (err %v), want 3", p.AAL, err)
	}
	// ...past it the EFFECTIVE level degrades to AAL1; the method history stays.
	clock.advance(2 * time.Minute)
	if p, err = a.Authenticate(ctx, token); err != nil {
		t.Fatal(err)
	}
	if p.AAL != 1 {
		t.Fatalf("expired step-up AAL = %d, want 1 (fail-closed degrade)", p.AAL)
	}
	if len(p.AMR) != 2 {
		t.Fatalf("AMR history after degrade = %v, want [pwd webauthn]", p.AMR)
	}

	// A re-step-up re-opens the window.
	if _, err := a.ElevateSession(ctx, p, "piv", auth.AAL3); err != nil {
		t.Fatal(err)
	}
	if p, err = a.Authenticate(ctx, token); err != nil || p.AAL != 3 || len(p.AMR) != 3 {
		t.Fatalf("re-elevated AAL/AMR = %d %v (err %v), want 3 [pwd webauthn piv]", p.AAL, p.AMR, err)
	}

	// A revoked session cannot be elevated.
	if err := a.RevokeSession(ctx, p, p.CredID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ElevateSession(ctx, p, "webauthn", auth.AAL3); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("elevate revoked session err = %v, want ErrUnauthenticated", err)
	}
}

// TestElevateSessionRefusesNonSessions pins the principal rule: only a session
// (KindUser) credential can carry a human assurance.
func TestElevateSessionRefusesNonSessions(t *testing.T) {
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	if _, err := a.ElevateSession(context.Background(), auth.Principal{Kind: auth.KindToken}, "webauthn", auth.AAL3); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("elevate token principal err = %v, want ErrUnauthenticated", err)
	}
}
