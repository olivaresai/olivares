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

// Config-to-session commit ordering (ROOT-CONSTRUCTION-R5-1 §5): a config writer holds
// the capability lock through commit, and an absent-component session creator either
// commits before that writer or reads its committed demand after acquiring the lock. The
// schedule is driven by a decorator that pauses ONE real repository write inside the real
// transaction; every call is delegated to the real store.

// pausingStore holds the first matching repository write of an AuthMutate until release.
type pausingStore struct {
	store.Store
	pauseConfigs  bool
	pauseSessions bool
	// failHeld, when set, fails the held write after release instead of delegating it, so
	// the enclosing transaction rolls back.
	failHeld    error
	reached     chan struct{}
	release     chan struct{}
	pauseOnce   sync.Once
	releaseOnce sync.Once
}

func newPausingStore(t *testing.T, st store.Store, configs, sessions bool) *pausingStore {
	p := &pausingStore{
		Store: st, pauseConfigs: configs, pauseSessions: sessions,
		reached: make(chan struct{}), release: make(chan struct{}),
	}
	t.Cleanup(p.unpause)
	return p
}

func (p *pausingStore) unpause() { p.releaseOnce.Do(func() { close(p.release) }) }

// pause holds the first matching write until release and reports whether this call was
// the held one.
func (p *pausingStore) pause() bool {
	held := false
	p.pauseOnce.Do(func() {
		held = true
		close(p.reached)
		<-p.release
	})
	return held
}

func (p *pausingStore) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	return p.Store.AuthMutate(ctx, func(as store.AuthScope) error {
		return fn(pausingScope{AuthScope: as, p: p})
	})
}

type pausingScope struct {
	store.AuthScope
	p *pausingStore
}

func (s pausingScope) FederationConfigs() store.Repository[model.FederationConfig] {
	r := s.AuthScope.FederationConfigs()
	if !s.p.pauseConfigs {
		return r
	}
	return pausingRepo[model.FederationConfig]{Repository: r, p: s.p}
}

func (s pausingScope) Sessions() store.Repository[model.AuthSession] {
	r := s.AuthScope.Sessions()
	if !s.p.pauseSessions {
		return r
	}
	return pausingRepo[model.AuthSession]{Repository: r, p: s.p}
}

type pausingRepo[T any] struct {
	store.Repository[T]
	p *pausingStore
}

func (r pausingRepo[T]) Create(ctx context.Context, v T) (T, error) {
	if r.p.pause() && r.p.failHeld != nil {
		var zero T
		return zero, r.p.failHeld
	}
	return r.Repository.Create(ctx, v)
}

func (r pausingRepo[T]) Update(ctx context.Context, v T) (T, error) {
	if r.p.pause() && r.p.failHeld != nil {
		var zero T
		return zero, r.p.failHeld
	}
	return r.Repository.Update(ctx, v)
}

func waitReached(t *testing.T, p *pausingStore, what string) {
	t.Helper()
	select {
	case <-p.reached:
	case <-time.After(30 * time.Second):
		t.Fatalf("%s never reached its paused write", what)
	}
}

func receiveErr(t *testing.T, ch <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(30 * time.Second):
		t.Fatalf("%s did not finish within 30s", what)
	}
	return nil
}

func TestLoginComponentAbsent_ConfigWriterCommitsBeforeNewSession(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	user, err := a.BootstrapSuperadmin(ctx, r5Email, r5Password)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	recordLoginCapability(t, st)
	installClassified(t, a, auth.NewFederationService(st, fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, nil), false, false, nil)
	before := authSessionCount(t, st, user.ID)

	paused := newPausingStore(t, st, true, false)
	writer := auth.NewFederationService(paused, fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, nil)
	installClassified(t, auth.NewAuthenticator(paused, nil), writer, true, false, &fakeLoginPolicy{})
	in := oidcInput("https://idp.example", true)
	in.RequireSSO = true

	writeErr := make(chan error, 1)
	go func() {
		_, err := writer.PutConfig(ctx, fedTestActor(), auth.GlobalFederationScope, in)
		writeErr <- err
	}()
	waitReached(t, paused, "the config writer")

	loginErr := make(chan error, 1)
	go func() {
		_, _, err := a.Login(ctx, r5Email, r5Password, r5IP)
		loginErr <- err
	}()
	select {
	case err := <-loginErr:
		t.Fatalf("a new login finished while the config writer held its transaction: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	paused.unpause()

	if err := receiveErr(t, writeErr, "the config writer"); err != nil {
		t.Fatalf("config write: %v", err)
	}
	if err := receiveErr(t, loginErr, "the login"); !errors.Is(err, auth.ErrLoginEnforcementComponentAbsent) {
		t.Fatalf("login after the committed demand = %v, want ErrLoginEnforcementComponentAbsent", err)
	}
	if after := authSessionCount(t, st, user.ID); after != before {
		t.Fatalf("session count %d -> %d, want no new session", before, after)
	}
}

func TestLoginComponentAbsent_NewSessionCommitsBeforeConfigWriter(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	user, err := auth.NewAuthenticator(st, nil).BootstrapSuperadmin(ctx, r5Email, r5Password)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	recordLoginCapability(t, st)
	before := authSessionCount(t, st, user.ID)

	paused := newPausingStore(t, st, false, true)
	a := auth.NewAuthenticator(paused, nil)
	installClassified(t, a, auth.NewFederationService(paused, fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, nil), false, false, nil)
	writer := auth.NewFederationService(st, fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, nil)
	installClassified(t, auth.NewAuthenticator(st, nil), writer, true, false, &fakeLoginPolicy{})
	in := oidcInput("https://idp.example", true)
	in.RequireSSO = true

	loginErr := make(chan error, 1)
	go func() {
		_, _, err := a.Login(ctx, r5Email, r5Password, r5IP)
		loginErr <- err
	}()
	waitReached(t, paused, "the absent-component login")

	writeErr := make(chan error, 1)
	go func() {
		_, err := writer.PutConfig(ctx, fedTestActor(), auth.GlobalFederationScope, in)
		writeErr <- err
	}()
	select {
	case err := <-writeErr:
		t.Fatalf("the config writer finished while the session creator held its transaction: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	paused.unpause()

	if err := receiveErr(t, loginErr, "the login"); err != nil {
		t.Fatalf("login that read no demand: %v", err)
	}
	if err := receiveErr(t, writeErr, "the config writer"); err != nil {
		t.Fatalf("config write after the session: %v", err)
	}
	if after := authSessionCount(t, st, user.ID); after != before+1 {
		t.Fatalf("session count %d -> %d, want one new session", before, after)
	}
	assertGlobalPostureConfigured(t, writer, true)
}
