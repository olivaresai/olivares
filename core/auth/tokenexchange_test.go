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
)

// exchangeFixture builds a tenant with an editor user holding a bound editor
// token, and an admin user holding a bound admin token, returning the
// authenticator, tenant, and the two token strings + ids.
type exchangeFixture struct {
	a           *auth.Authenticator
	tenant      model.TenantID
	editorTok   string
	editorTokID model.ID
	adminTok    string
}

func newExchangeFixture(t *testing.T) exchangeFixture {
	t.Helper()
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")

	editor, err := a.CreateUser(ctx, super, auth.NewUser{Email: "editor@acme.com", Password: "editor-pass-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.GrantMembership(ctx, super, editor.ID, tenant, auth.RoleEditor, model.ID("")); err != nil {
		t.Fatal(err)
	}
	adminU, err := a.CreateUser(ctx, super, auth.NewUser{Email: "admin@acme.com", Password: "admin-pass-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.GrantMembership(ctx, super, adminU.ID, tenant, auth.RoleAdmin, model.ID("")); err != nil {
		t.Fatal(err)
	}

	editorTok, editorStored, err := a.IssueToken(ctx, super, auth.TokenSpec{Name: "editor-tok", BoundTenant: tenant, Role: auth.RoleEditor})
	if err != nil {
		t.Fatal(err)
	}
	adminTok, _, err := a.IssueToken(ctx, super, auth.TokenSpec{Name: "admin-tok", BoundTenant: tenant, Role: auth.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	return exchangeFixture{a: a, tenant: tenant, editorTok: editorTok, editorTokID: editorStored.ID, adminTok: adminTok}
}

func accessReq(subject string) auth.ExchangeRequest {
	return auth.ExchangeRequest{SubjectToken: subject, SubjectTokenType: auth.TokenTypeAccessToken}
}

func TestExchangeDownScopesRoleAndBindsAudience(t *testing.T) {
	ctx := context.Background()
	f := newExchangeFixture(t)
	caller, err := f.a.Authenticate(ctx, f.editorTok)
	if err != nil {
		t.Fatal(err)
	}

	req := accessReq(f.editorTok)
	req.Scope = []string{"read"}
	req.Resources = []string{"https://mcp.example.com/github"}
	res, err := f.a.ExchangeToken(ctx, caller, req)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if !res.Narrowed {
		t.Error("expected a narrowed (read) grant from an editor subject")
	}
	if res.IssuedTokenType != auth.TokenTypeAccessToken || res.TokenType != "Bearer" {
		t.Errorf("issued_token_type=%q token_type=%q", res.IssuedTokenType, res.TokenType)
	}
	if res.Stored.Role != auth.RoleViewer {
		t.Errorf("child role = %q, want viewer (read down-scope)", res.Stored.Role)
	}

	// The child authenticates with the lower role AND carries the audience binding.
	child, err := f.a.Authenticate(ctx, res.AccessToken)
	if err != nil {
		t.Fatalf("authenticate child: %v", err)
	}
	if role, _ := child.RoleIn(f.tenant); role != auth.RoleViewer {
		t.Errorf("child principal role = %q, want viewer", role)
	}
	if !child.HasAudience("https://mcp.example.com/github") {
		t.Error("child should be bound to the requested resource audience")
	}
	if child.HasAudience("https://mcp.example.com/other") {
		t.Error("child must NOT be bound to an unrequested audience (confused-deputy defense)")
	}
}

func TestExchangeCannotEscalateAboveSubject(t *testing.T) {
	ctx := context.Background()
	f := newExchangeFixture(t)
	// A viewer-tier subject requesting write must NOT receive write.
	caller, _ := f.a.Authenticate(ctx, f.editorTok)
	// Exchange editor->viewer first, then try to escalate the viewer child to write.
	low := accessReq(f.editorTok)
	low.Scope = []string{"read"}
	lowRes, err := f.a.ExchangeToken(ctx, caller, low)
	if err != nil {
		t.Fatal(err)
	}
	viewerPrincipal, _ := f.a.Authenticate(ctx, lowRes.AccessToken)

	up := accessReq(lowRes.AccessToken)
	up.Scope = []string{"write"}
	upRes, err := f.a.ExchangeToken(ctx, viewerPrincipal, up)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if upRes.Stored.Role != auth.RoleViewer {
		t.Errorf("escalation leaked: child role = %q, want viewer", upRes.Stored.Role)
	}
}

func TestExchangeDelegationAndCeiling(t *testing.T) {
	ctx := context.Background()
	f := newExchangeFixture(t)
	admin, _ := f.a.Authenticate(ctx, f.adminTok)

	// admin (actor) acting for the editor (subject): allowed (admin >= editor).
	req := accessReq(f.editorTok)
	req.ActorToken = f.adminTok
	req.ActorTokenType = auth.TokenTypeAccessToken
	res, err := f.a.ExchangeToken(ctx, admin, req)
	if err != nil {
		t.Fatalf("delegation: %v", err)
	}
	if res.Stored.ActAsUserID.IsZero() {
		t.Error("delegated token must record act_as (the subject)")
	}

	// editor (actor) acting for an admin (subject): role-ceiling violation.
	editor, _ := f.a.Authenticate(ctx, f.editorTok)
	bad := accessReq(f.adminTok)
	bad.ActorToken = f.editorTok
	bad.ActorTokenType = auth.TokenTypeAccessToken
	if _, err := f.a.ExchangeToken(ctx, editor, bad); !errors.Is(err, auth.ErrRoleCeiling) {
		t.Errorf("editor acting for admin: err = %v, want ErrRoleCeiling", err)
	}
}

func TestExchangeRequestValidation(t *testing.T) {
	ctx := context.Background()
	f := newExchangeFixture(t)
	caller, _ := f.a.Authenticate(ctx, f.editorTok)

	cases := []struct {
		name string
		req  auth.ExchangeRequest
		want error
	}{
		{"missing subject type", auth.ExchangeRequest{SubjectToken: f.editorTok}, auth.ErrInvalidExchange},
		{"bad subject type", auth.ExchangeRequest{SubjectToken: f.editorTok, SubjectTokenType: "urn:bogus"}, auth.ErrInvalidSubjectToken},
		{"actor without type", func() auth.ExchangeRequest {
			r := accessReq(f.editorTok)
			r.ActorToken = f.adminTok
			return r
		}(), auth.ErrInvalidExchange},
		{"fragment resource", func() auth.ExchangeRequest {
			r := accessReq(f.editorTok)
			r.Resources = []string{"https://mcp.example.com/x#frag"}
			return r
		}(), auth.ErrInvalidTarget},
		{"relative resource", func() auth.ExchangeRequest {
			r := accessReq(f.editorTok)
			r.Resources = []string{"/relative/path"}
			return r
		}(), auth.ErrInvalidTarget},
		{"empty audience", func() auth.ExchangeRequest {
			r := accessReq(f.editorTok)
			r.Audiences = []string{"  "}
			return r
		}(), auth.ErrInvalidTarget},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.a.ExchangeToken(ctx, caller, tc.req); !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestExchangeRejectsSuperadminSubject(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	sysTok, _, err := a.IssueToken(ctx, super, auth.TokenSpec{Name: "sys", Superadmin: true})
	if err != nil {
		t.Fatal(err)
	}
	caller, _ := a.Authenticate(ctx, sysTok)
	if _, err := a.ExchangeToken(ctx, caller, accessReq(sysTok)); !errors.Is(err, auth.ErrInvalidExchange) {
		t.Errorf("superadmin subject: err = %v, want ErrInvalidExchange", err)
	}
}

func TestExchangeClampsExpiryToSubject(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	// A subject token that expires in 2 minutes — shorter than the 15m exchange TTL.
	soon := model.NewTimestamp(time.Now().Add(2 * time.Minute))
	subTok, _, err := a.IssueToken(ctx, super, auth.TokenSpec{Name: "short", BoundTenant: tenant, Role: auth.RoleEditor, ExpiresAt: &soon})
	if err != nil {
		t.Fatal(err)
	}
	caller, _ := a.Authenticate(ctx, subTok)
	res, err := a.ExchangeToken(ctx, caller, accessReq(subTok))
	if err != nil {
		t.Fatal(err)
	}
	if res.ExpiresIn <= 0 || res.ExpiresIn > 130 {
		t.Errorf("child expires_in = %d, want clamped to ~120s of the subject", res.ExpiresIn)
	}
}

func TestExchangeCascadeRevoke(t *testing.T) {
	ctx := context.Background()
	f := newExchangeFixture(t)

	caller, _ := f.a.Authenticate(ctx, f.editorTok)
	res, err := f.a.ExchangeToken(ctx, caller, accessReq(f.editorTok))
	if err != nil {
		t.Fatal(err)
	}
	if res.Stored.ParentTokenID != f.editorTokID {
		t.Fatalf("child parent = %s, want exact API token %s", res.Stored.ParentTokenID, f.editorTokID)
	}
	// The child authenticates before revocation.
	if _, err := f.a.Authenticate(ctx, res.AccessToken); err != nil {
		t.Fatalf("child should authenticate pre-revoke: %v", err)
	}
	// Revoking the PARENT (the editor token the child was exchanged from) cascades.
	// (The actor is audit attribution only; authorization is the API layer's job.)
	if err := f.a.RevokeToken(ctx, caller, f.editorTokID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.Authenticate(ctx, res.AccessToken); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("child should be revoked via cascade: err = %v", err)
	}
}
