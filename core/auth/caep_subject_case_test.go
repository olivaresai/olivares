// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// An e-mail subject names the SCIM member whose stored address it reads as,
// whatever the letter case of either spelling. SCIM stores a userName through
// SCIMUserNameKey, and an IdP that provisioned Bob@Example.com keeps sending
// that spelling in the security events it publishes about the account.
func TestCAEPEventFindsSCIMMemberRegardlessOfEmailCase(t *testing.T) {
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			st := caepSubjectCaseStore(t, engine)
			a := auth.NewAuthenticator(st, nil)
			actor := auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID(), Superadmin: true}
			tenant := provisionTenant(t, st, "acme")
			signer := newES256Signer(t)
			enableCAEP(t, ctx, a, actor, tenant, signer)

			for _, tc := range []struct {
				name, userName, subject string
			}{
				{"lower_case_subject", "Alice@Example.com", "alice@example.com"},
				{"provisioned_spelling", "Bob@Example.com", "Bob@Example.com"},
				{"upper_case_subject", "carol@example.com", "CAROL@EXAMPLE.COM"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					u, created, err := a.SCIMProvisionUser(ctx, actor, tenant, auth.SCIMUserInput{UserName: tc.userName, Active: true})
					if err != nil || !created {
						t.Fatalf("provision %q = created %t, err %v", tc.userName, created, err)
					}
					sessTok, _ := mintUserCreds(t, st, u.ID, tenant)
					env := caepEnvFromSET(t, signer.signSET(t, "k1", caepEventByEmail(
						"https://schemas.openid.net/secevent/caep/event-type/session-revoked",
						tc.subject, "jti-"+tc.name,
					)), auth.CAEPSessionRevoke)

					res, err := a.CAEPReceiveEvent(ctx, actor, tenant, env)
					if err != nil {
						t.Fatalf("event for %q with member %q stored = %v, want the member found", tc.subject, u.Email, err)
					}
					if res.UserID != u.ID {
						t.Errorf("resolved user = %s, want %s", res.UserID, u.ID)
					}
					// A tenant's event excludes the account-scope session from
					// the tenant and leaves it valid for the account's others.
					assertExcludedFrom(t, ctx, a, sessTok, tenant, "session after the event")
				})
			}
		})
	}
}

// caepSubjectCaseStore opens the engine a case runs on; the PostgreSQL leg
// skips when no server is configured.
func caepSubjectCaseStore(t *testing.T, engine store.Engine) store.Store {
	t.Helper()
	if engine == store.EnginePostgres {
		st, _ := openLoginCapabilityPostgres(t)
		return st
	}
	return testStore(t)
}
