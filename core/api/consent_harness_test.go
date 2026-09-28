// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The consent tests run every case on SQLite and on PostgreSQL. Each engine gets
// a fresh harness, so no row of one case can explain another's result.

// capturedInvite is one invitation the capturing mailer was asked to deliver.
type capturedInvite struct {
	email string
	tok   string
}

// token returns the redemption token handed to the mailer.
func (c capturedInvite) token() string { return c.tok }

// capturingInviteSender records invitations instead of mailing them. It stands
// in for the deployment's invitation mailer.
type capturingInviteSender struct {
	mu   sync.Mutex
	sent []capturedInvite
}

func (c *capturingInviteSender) SendInvite(_ context.Context, email, token string, _ time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, capturedInvite{email: email, tok: token})
	return nil
}

func (c *capturingInviteSender) all() []capturedInvite {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]capturedInvite(nil), c.sent...)
}

// consentStoreOpener opens a fresh store on engine. PostgreSQL gets an isolated
// database with a least-privilege application role; without a configured
// server the PostgreSQL leg skips.
func consentStoreOpener(engine store.Engine) harnessStoreOpener {
	return func(t *testing.T) store.Store {
		t.Helper()
		if engine == store.EngineSQLite {
			return defaultHarnessStore(t)
		}
		dsns := pgtest.Isolate(t, sqlstore.ProvisionPostgres, pgtest.SplitOwner)
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		st, err := sqlstore.Open(ctx, store.Config{
			Engine: engine, DSN: dsns.App, OwnerDSN: dsns.Owner, AdminDSN: dsns.Admin,
			Debug: true, MaxConns: 8,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		if err := st.System(ctx, func(sys store.SystemScope) error {
			_, err := sys.EnsureSystemTenant(ctx)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return st
	}
}

// consentEngines are the engines every consent case runs on.
var consentEngines = []store.Engine{store.EngineSQLite, store.EnginePostgres}

// onEachEngine runs body against a fresh harness per engine, as subtests named
// after the engine.
func onEachEngine(t *testing.T, configure func(*api.Options), body func(t *testing.T, h *harness)) {
	t.Helper()
	for _, engine := range consentEngines {
		t.Run(string(engine), func(t *testing.T) {
			h := newHarnessOptsFromStoreSource(t, harnessStoreSource{open: consentStoreOpener(engine)}, configure)
			body(t, h)
		})
	}
}

// onEachEngineWithMailer is onEachEngine with a capturing invitation mailer
// wired into the server.
func onEachEngineWithMailer(t *testing.T, body func(t *testing.T, h *harness, mail *capturingInviteSender)) {
	t.Helper()
	for _, engine := range consentEngines {
		t.Run(string(engine), func(t *testing.T) {
			mail := &capturingInviteSender{}
			h := newHarnessOptsFromStoreSource(t, harnessStoreSource{open: consentStoreOpener(engine)},
				func(o *api.Options) { o.InviteSender = mail })
			body(t, h, mail)
		})
	}
}

// elevatedAdmin runs first-boot setup and returns a superadmin session raised to
// AAL3, which onboarding requires.
func (h *harness) elevatedAdmin() string {
	h.t.Helper()
	tok := h.adminLogin()
	h.elevate(tok)
	return tok
}

// principalOf authenticates a bearer token.
func (h *harness) principalOf(tok string) auth.Principal {
	h.t.Helper()
	p, err := h.authr.Authenticate(context.Background(), tok)
	if err != nil {
		h.t.Fatalf("authenticate: %v", err)
	}
	return p
}

// userByEmail reads an account through the auth partition.
func (h *harness) userByEmail(email string) (model.User, bool) {
	h.t.Helper()
	ctx := context.Background()
	var out model.User
	var found bool
	if err := h.st.AuthView(ctx, func(as store.AuthScope) error {
		us, _, err := as.Users().List(ctx, model.Query{Filters: []model.Filter{
			{Column: "email", Op: model.OpEq, Value: strings.ToLower(strings.TrimSpace(email))},
		}, Limit: 1})
		if len(us) > 0 {
			out, found = us[0], true
		}
		return err
	}); err != nil {
		h.t.Fatalf("read user: %v", err)
	}
	return out, found
}

// userByID reads an account through the auth partition.
func (h *harness) userByID(id model.ID) model.User {
	h.t.Helper()
	ctx := context.Background()
	var out model.User
	if err := h.st.AuthView(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Get(ctx, id)
		out = u
		return err
	}); err != nil {
		h.t.Fatalf("read user %s: %v", id, err)
	}
	return out
}

// memberOf reports whether the account holds a membership in tenant.
func (h *harness) memberOf(user model.ID, tenant model.TenantID) bool {
	h.t.Helper()
	ctx := context.Background()
	var found bool
	if err := h.st.AuthView(ctx, func(as store.AuthScope) error {
		ms, _, err := as.Memberships().List(ctx, model.Query{Filters: []model.Filter{
			{Column: "user_id", Op: model.OpEq, Value: user.String()},
			{Column: "target_tenant_id", Op: model.OpEq, Value: tenant.String()},
		}, Limit: 1})
		found = len(ms) > 0
		return err
	}); err != nil {
		h.t.Fatalf("read membership: %v", err)
	}
	return found
}

// membershipCount counts the account's memberships.
func (h *harness) membershipCount(user model.ID) int {
	h.t.Helper()
	ctx := context.Background()
	var n int
	if err := h.st.AuthView(ctx, func(as store.AuthScope) error {
		ms, _, err := as.Memberships().List(ctx, model.Query{Filters: []model.Filter{
			{Column: "user_id", Op: model.OpEq, Value: user.String()},
		}, Limit: 100})
		n = len(ms)
		return err
	}); err != nil {
		h.t.Fatalf("read memberships: %v", err)
	}
	return n
}

// liveSessionCount counts the account's unrevoked sessions.
func (h *harness) liveSessionCount(user model.ID) int {
	h.t.Helper()
	ctx := context.Background()
	var n int
	if err := h.st.AuthView(ctx, func(as store.AuthScope) error {
		ss, _, err := as.Sessions().List(ctx, model.Query{Filters: []model.Filter{
			{Column: "user_id", Op: model.OpEq, Value: user.String()},
		}, Limit: 100})
		for _, s := range ss {
			if !s.Revoked {
				n++
			}
		}
		return err
	}); err != nil {
		h.t.Fatalf("read sessions: %v", err)
	}
	return n
}

// seedMembership writes a membership through the store, bypassing every product
// path. It models a membership that predates the consent rule (an account
// already shared at upgrade), which no product path can create any longer.
func (h *harness) seedMembership(user model.ID, tenant model.TenantID, role string) {
	h.t.Helper()
	ctx := context.Background()
	if err := h.st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.Memberships().Create(ctx, model.Membership{UserID: user, TargetTenantID: tenant, Role: role})
		return err
	}); err != nil {
		h.t.Fatalf("seed membership: %v", err)
	}
}

// mutateUser rewrites an account through the store.
func (h *harness) mutateUser(id model.ID, change func(*model.User)) {
	h.t.Helper()
	ctx := context.Background()
	if err := h.st.AuthMutate(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Get(ctx, id)
		if err != nil {
			return err
		}
		change(&u)
		_, err = as.Users().Update(ctx, u)
		return err
	}); err != nil {
		h.t.Fatalf("mutate user: %v", err)
	}
}

// onboardPassword onboards a new person into tenant with an administrator-set
// password and returns the account id.
func (h *harness) onboardPassword(admin string, tenant model.TenantID, email, role, password string) model.ID {
	h.t.Helper()
	r := h.do("POST", "/v1/onboard", admin, map[string]any{
		"email": email, "role": role, "mode": "password", "password": password,
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("onboard %s = %d %s", email, r.code, r.raw)
	}
	u, _ := r.body["user"].(map[string]any)
	id, _ := u["id"].(string)
	if id == "" {
		h.t.Fatalf("onboard %s returned no user id: %s", email, r.raw)
	}
	return model.ID(id)
}

// createDeploymentUser creates an account through the deployment's own user
// route, with no membership, and returns its id.
func (h *harness) createDeploymentUser(admin, email, password string) model.ID {
	h.t.Helper()
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": password}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("create user %s = %d %s", email, r.code, r.raw)
	}
	id, _ := r.body["id"].(string)
	return model.ID(id)
}

// login signs in with a password and returns the session token, failing the
// test on anything but 200.
func (h *harness) login(email, password string) string {
	h.t.Helper()
	r := h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": password}, nil)
	if r.code != http.StatusOK {
		h.t.Fatalf("login %s = %d %s", email, r.code, r.raw)
	}
	tok, _ := r.body["token"].(string)
	return tok
}

// actsIn reports the status of a read the caller's role permits in tenant: 200
// when the credential carries authority there.
func (h *harness) actsIn(tok string, tenant model.TenantID) int {
	h.t.Helper()
	return h.do("GET", "/v1/agents", tok, nil, tenantHdr(tenant)).code
}
