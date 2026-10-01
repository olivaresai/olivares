// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

func TestSSOEmailFallbackNeverAdoptsABoundAccount(t *testing.T) {
	for _, scopeName := range []string{"deployment", "empty deployment", "tenant"} {
		for _, assertingIssuer := range []string{"https://issuer-b.example", "https://issuer-a.example"} {
			t.Run(scopeName+"/"+assertingIssuer, func(t *testing.T) {
				ctx := context.Background()
				f := newScopeFixture(t)
				u, err := f.a.CreateUser(ctx, f.super, auth.NewUser{Email: "victim@corp.example", Password: "fixture-local-password"})
				if err != nil {
					t.Fatal(err)
				}
				scope := auth.GlobalFederationScope
				if scopeName == "empty deployment" {
					scope = ""
				} else if scopeName == "tenant" {
					scope = f.tA
					seedConfig(t, f.st, scope, "default", assertingIssuer, "corp.example")
					f.seedMembership(t, u.ID, scope, auth.RoleEditor)
				}
				f.link(t, u.ID, "https://issuer-a.example", "original-subject")
				before := f.sessions(t, u.ID)
				token, session, err := f.a.CompleteSSO(ctx, auth.FederatedIdentity{Protocol: auth.ProtocolOIDC, EmailVerified: true, Issuer: assertingIssuer, Subject: "different-subject", Email: u.Email}, scopeIP, scope, false)
				if !errors.Is(err, auth.ErrUnauthenticated) || token != "" || !session.ID.IsZero() {
					t.Fatalf("email adopted a bound account: err=%v token=%t session=%s", err, token != "", session.ID)
				}
				if f.sessions(t, u.ID) != before {
					t.Fatal("refused adoption issued a session")
				}
				after, ok := f.account(t, u.Email)
				if !ok || after.ID != u.ID || after.SsoSubject != qualified("https://issuer-a.example", "original-subject") || after.Status != model.StatusActive {
					t.Fatal("refused adoption changed the account")
				}
			})
		}
	}
}

func TestSSOEmailFallbackVerificationBootstrapAndExactSubject(t *testing.T) {
	for _, scopeName := range []string{"deployment", "tenant"} {
		for _, protocol := range []string{auth.ProtocolOIDC, auth.ProtocolSAML} {
			t.Run(scopeName+"/"+protocol, func(t *testing.T) {
				ctx := context.Background()
				f := newScopeFixture(t)
				u, err := f.a.CreateUser(ctx, f.super, auth.NewUser{Email: "bootstrap@corp.example", Password: "fixture-local-password"})
				if err != nil {
					t.Fatal(err)
				}
				scope := auth.GlobalFederationScope
				if scopeName == "tenant" {
					scope = f.tA
					seedConfig(t, f.st, scope, "default", "https://issuer.example", "corp.example")
					f.seedMembership(t, u.ID, scope, auth.RoleViewer)
				}
				id := auth.FederatedIdentity{Protocol: protocol, Issuer: "https://issuer.example", Subject: "immutable-subject", Email: u.Email}
				if protocol == auth.ProtocolOIDC {
					before := f.sessions(t, u.ID)
					token, session, err := f.a.CompleteSSO(ctx, id, scopeIP, scope, false)
					if !errors.Is(err, auth.ErrUnauthenticated) || token != "" || !session.ID.IsZero() || f.sessions(t, u.ID) != before {
						t.Fatal("unverified OIDC email bootstrapped an account", err)
					}
					if row, _ := f.account(t, u.Email); row.SsoSubject != "" {
						t.Fatal("refused verification stamped a binding")
					}
					id.EmailVerified = true
				}
				token, session, err := f.a.CompleteSSO(ctx, id, scopeIP, scope, false)
				if err != nil || token == "" || session.UserID != u.ID {
					t.Fatal("permitted unbound bootstrap refused", err)
				}
				if row, _ := f.account(t, u.Email); row.SsoSubject != id.QualifiedSubject() {
					t.Fatal("bootstrap did not bind exact subject")
				}
				id.Email, id.EmailVerified = "renamed@corp.example", false
				token, session, err = f.a.CompleteSSO(ctx, id, scopeIP, scope, false)
				if err != nil || token == "" || session.UserID != u.ID {
					t.Fatal("exact subject path changed for omitted verification", err)
				}
			})
		}
	}
}
