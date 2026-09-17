// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx" for the lock observer
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// R5 caller ordering on PostgreSQL (ROOT-CONSTRUCTION-R5-1 §4-§5). Each test opens its own
// isolated split-owner database (owner, app and admin roles) and drives the real auth
// callers through the app role with several pool connections, so the two transactions
// run on separate backends. A separate observer connection reads pg_locks for the
// capability advisory key only (no query text). The second party must be seen WAITING on
// that key; if it finishes instead, the test fails on that behavior, which is what a
// caller that omits L produces.

const loginCapabilityLockKey = "core.login.capability"

func openLoginCapabilityPostgres(t *testing.T) (store.Store, *sql.DB) {
	t.Helper()
	if !pgtest.Available(t) {
		t.Skip("no Postgres configured")
	}
	dsns := pgtest.Isolate(t, sqlstore.ProvisionPostgres, pgtest.SplitOwner)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	st, err := sqlstore.Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, AdminDSN: dsns.Admin, MaxConns: 8,
	}, nil)
	if err != nil {
		t.Fatalf("open split-owner postgres store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	observer, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("open lock observer: %v", err)
	}
	observer.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = observer.Close() })
	if err := observer.PingContext(ctx); err != nil {
		t.Fatalf("reach lock observer: %v", err)
	}
	return st, observer
}

func federationOn(st store.Store) *auth.FederationService {
	return auth.NewFederationService(st, fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, nil)
}

// waitCapabilityWaiter polls pg_locks until the capability advisory key has one granted
// holder and at least one ungranted waiter in this database. If the second party (done)
// finishes first, the lock did not serialize it and the test fails.
func waitCapabilityWaiter(t *testing.T, observer *sql.DB, done <-chan error, what string) {
	t.Helper()
	start := time.Now()
	deadline := start.Add(15 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("%s finished while the other transaction was still open (err=%v): the capability lock did not make it wait", what, err)
		default:
		}
		var granted, waiting int
		if err := observer.QueryRowContext(context.Background(), `
SELECT count(*) FILTER (WHERE granted), count(*) FILTER (WHERE NOT granted)
FROM pg_locks
WHERE locktype = 'advisory' AND objsubid = 1
  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
  AND ((classid::bigint << 32) | objid::bigint) = hashtextextended($1, 0)`, loginCapabilityLockKey).Scan(&granted, &waiting); err != nil {
			t.Fatalf("observe the capability lock: %v", err)
		}
		if granted == 1 && waiting >= 1 {
			t.Logf("PG_CAPABILITY_LOCK_WAIT|%s|granted=%d|waiting=%d|observed_after_ms=%d", what, granted, waiting, time.Since(start).Milliseconds())
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: capability lock granted=%d waiting=%d after 15s, want granted=1 waiting>=1", what, granted, waiting)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func globalDemand(t *testing.T, st store.Store) bool {
	t.Helper()
	ctx := context.Background()
	var demand bool
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		d, err := auth.GlobalDefaultLoginDemand(ctx, as)
		demand = d
		return err
	}); err != nil {
		t.Fatalf("read global demand: %v", err)
	}
	return demand
}

func assertObservationUnchanged(t *testing.T, st store.Store, first store.LoginCapabilityObservation) {
	t.Helper()
	after := readLoginCapability(t, st)
	if after.Present != first.Present || !after.FirstObservedAt.Equal(first.FirstObservedAt) || after.ObservationCount != first.ObservationCount {
		t.Fatalf("capability observation = %+v, want the recorded %+v unchanged", after, first)
	}
}

func requireSSOInput() auth.FederationConfigInput {
	in := oidcInput("https://idp.example", true)
	in.RequireSSO = true
	return in
}

func TestLoginComponentAbsent_PostgresConfigWriterHoldsCapabilityLock(t *testing.T) {
	for _, tc := range []struct {
		name     string
		history  bool
		failHeld bool
	}{
		{name: "recorded history refuses the waiting login", history: true},
		{name: "absent row still serializes", history: false},
		{name: "rolled back writer leaves no demand", history: true, failHeld: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st, observer := openLoginCapabilityPostgres(t)
			a := auth.NewAuthenticator(st, nil)
			user, err := a.BootstrapSuperadmin(ctx, r5Email, r5Password)
			if err != nil {
				t.Fatalf("bootstrap: %v", err)
			}
			if tc.history {
				recordLoginCapability(t, st)
			}
			first := readLoginCapability(t, st)
			installClassified(t, a, federationOn(st), false, false, nil)
			before := authSessionCount(t, st, user.ID)

			injected := errors.New("r5 test: the held config write failed")
			paused := newPausingStore(t, st, true, false)
			if tc.failHeld {
				paused.failHeld = injected
			}
			writer := federationOn(paused)
			installClassified(t, auth.NewAuthenticator(paused, nil), writer, true, false, &fakeLoginPolicy{})

			writeErr := make(chan error, 1)
			go func() {
				_, err := writer.PutConfig(ctx, fedTestActor(), auth.GlobalFederationScope, requireSSOInput())
				writeErr <- err
			}()
			waitReached(t, paused, "the config writer")

			loginErr := make(chan error, 1)
			go func() {
				_, _, err := a.Login(ctx, r5Email, r5Password, r5IP)
				loginErr <- err
			}()
			waitCapabilityWaiter(t, observer, loginErr, "the absent login")
			paused.unpause()

			werr := receiveErr(t, writeErr, "the config writer")
			lerr := receiveErr(t, loginErr, "the login")
			if tc.failHeld {
				if !errors.Is(werr, injected) {
					t.Fatalf("config write = %v, want the injected failure", werr)
				}
			} else if werr != nil {
				t.Fatalf("config write: %v", werr)
			}
			after := authSessionCount(t, st, user.ID)
			if tc.history && !tc.failHeld {
				if !errors.Is(lerr, auth.ErrLoginEnforcementComponentAbsent) {
					t.Fatalf("login after the committed demand = %v, want ErrLoginEnforcementComponentAbsent", lerr)
				}
				if after != before {
					t.Fatalf("session count %d -> %d, want no new session", before, after)
				}
			} else {
				if lerr != nil {
					t.Fatalf("login = %v, want a session", lerr)
				}
				if after != before+1 {
					t.Fatalf("session count %d -> %d, want one new session", before, after)
				}
			}
			if got, want := globalDemand(t, st), !tc.failHeld; got != want {
				t.Fatalf("global demand = %t, want %t", got, want)
			}
			assertObservationUnchanged(t, st, first)
		})
	}
}

func TestLoginComponentAbsent_PostgresNewSessionHoldsCapabilityLock(t *testing.T) {
	for _, history := range []bool{true, false} {
		t.Run(fmt.Sprintf("history=%t", history), func(t *testing.T) {
			ctx := context.Background()
			st, observer := openLoginCapabilityPostgres(t)
			user, err := auth.NewAuthenticator(st, nil).BootstrapSuperadmin(ctx, r5Email, r5Password)
			if err != nil {
				t.Fatalf("bootstrap: %v", err)
			}
			if history {
				recordLoginCapability(t, st)
			}
			first := readLoginCapability(t, st)
			before := authSessionCount(t, st, user.ID)

			paused := newPausingStore(t, st, false, true)
			a := auth.NewAuthenticator(paused, nil)
			installClassified(t, a, federationOn(paused), false, false, nil)
			writer := federationOn(st)
			installClassified(t, auth.NewAuthenticator(st, nil), writer, true, false, &fakeLoginPolicy{})

			loginErr := make(chan error, 1)
			go func() {
				_, _, err := a.Login(ctx, r5Email, r5Password, r5IP)
				loginErr <- err
			}()
			waitReached(t, paused, "the absent-component login")

			writeErr := make(chan error, 1)
			go func() {
				_, err := writer.PutConfig(ctx, fedTestActor(), auth.GlobalFederationScope, requireSSOInput())
				writeErr <- err
			}()
			waitCapabilityWaiter(t, observer, writeErr, "the wired config writer")
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
			if !globalDemand(t, st) {
				t.Fatal("global demand = false, want the committed require-SSO posture")
			}
			assertObservationUnchanged(t, st, first)
		})
	}
}

func TestLoginComponentAbsent_PostgresRefusalsAndOperatorRecovery(t *testing.T) {
	const inviteePassword = "invitee-chosen-pw-1"
	ctx := context.Background()
	st, _ := openLoginCapabilityPostgres(t)
	a := auth.NewAuthenticator(st, nil)
	fed := federationOn(st)
	admin, err := a.BootstrapSuperadmin(ctx, r5Email, r5Password)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	tenant := provisionTenant(t, st, "r5-pg-invite")
	invite, err := a.OnboardMember(ctx, fedTestActor(), tenant, auth.OnboardInput{
		Email: "r5-invitee@acme.test", DisplayName: "Invitee", Role: auth.RoleViewer, Invite: true,
	})
	if err != nil || invite.InviteToken == "" {
		t.Fatalf("onboard in invite mode: created=%t err=%v", invite.Created, err)
	}
	configureGlobalLoginPosture(t, st)
	recordLoginCapability(t, st)
	installClassified(t, a, fed, false, false, nil)
	id := auth.FederatedIdentity{Email: "r5-sso@x.io", Subject: "ext-pg-1"}

	if _, _, err := a.Login(ctx, r5Email, r5Password, r5IP); !errors.Is(err, auth.ErrLoginEnforcementComponentAbsent) {
		t.Fatalf("password login = %v, want ErrLoginEnforcementComponentAbsent", err)
	}
	if _, _, err := a.CompleteSSO(ctx, id, r5IP, "", false); !errors.Is(err, auth.ErrLoginEnforcementComponentAbsent) {
		t.Fatalf("SSO completion = %v, want ErrLoginEnforcementComponentAbsent", err)
	}
	if _, _, err := a.AcceptInvite(ctx, invite.InviteToken, inviteePassword, r5IP); !errors.Is(err, auth.ErrLoginEnforcementComponentAbsent) {
		t.Fatalf("AcceptInvite = %v, want ErrLoginEnforcementComponentAbsent", err)
	}
	ssoUser, found := userByEmail(t, st, "r5-sso@x.io")
	if !found {
		t.Fatal("the JIT-provisioned user is missing; the documented residual changed")
	}
	users := map[string]model.ID{"password": admin.ID, "sso": ssoUser.ID, "invite": invite.User.ID}
	for name, userID := range users {
		if n := authSessionCount(t, st, userID); n != 0 {
			t.Fatalf("refused %s login left %d sessions, want 0", name, n)
		}
	}

	// Host break-glass on the artifact without the component accepts all three.
	installClassified(t, a, fed, false, true, nil)
	if _, _, err := a.Login(ctx, r5Email, r5Password, r5IP); err != nil {
		t.Fatalf("password login under break-glass: %v", err)
	}
	if _, _, err := a.CompleteSSO(ctx, id, r5IP, "", false); err != nil {
		t.Fatalf("SSO completion under break-glass: %v", err)
	}
	if _, _, err := a.AcceptInvite(ctx, invite.InviteToken, inviteePassword, r5IP); err != nil {
		t.Fatalf("AcceptInvite under break-glass: %v", err)
	}
	for name, userID := range users {
		if n := authSessionCount(t, st, userID); n != 1 {
			t.Fatalf("break-glass %s login left %d sessions, want 1", name, n)
		}
	}
}
