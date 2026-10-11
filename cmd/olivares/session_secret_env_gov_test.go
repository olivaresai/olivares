// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// Vault secrets given to a session (design FH 016) are part of what a launch
// approval approves: an approval opened for one set of secret NAMES cannot be spent
// on a launch given more, and the receipt names them. Values never reach either.
func TestSessionLaunchPlanBindsVaultSecretNames(t *testing.T) {
	plain := sessions.LaunchIntent{Action: sessions.LaunchActionCreate, Actor: "user:a", PermissionMode: "default"}
	empty := plain
	empty.SecretEnv = []sessions.SecretEnvRef{}
	if sessionLaunchPlanHash(plain) != sessionLaunchPlanHash(empty) {
		t.Fatal("a launch without secrets must keep the hash it always had")
	}
	one := plain
	one.SecretEnv = []sessions.SecretEnvRef{{Env: "GITHUB_TOKEN", Secret: "env/github"}}
	two := plain
	two.SecretEnv = []sessions.SecretEnvRef{{Env: "GITHUB_TOKEN", Secret: "env/github"}, {Env: "AWS_KEY", Secret: "env/aws"}}
	other := plain
	other.SecretEnv = []sessions.SecretEnvRef{{Env: "GITHUB_TOKEN", Secret: "env/other"}}
	hashes := map[string]bool{}
	for _, intent := range []sessions.LaunchIntent{plain, one, two, other} {
		hashes[sessionLaunchPlanHash(intent)] = true
	}
	if len(hashes) != 4 {
		t.Fatal("adding, widening or re-pointing a session secret must invalidate the approval")
	}
	if got := describeLaunch(one); !strings.Contains(got, "secrets GITHUB_TOKEN from env/github") {
		t.Fatalf("approval receipt = %q, want the secret names", got)
	}
}

// The session credential carries the names it was minted for, so what a running
// session was given is part of its server-resolved boundary.
func TestSessionCredentialCarriesTheVaultSecretNames(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	tenant := model.TenantID(h.tenantA)
	p, err := h.authr.Authenticate(ctx, h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	c := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
	intent := claimHookTestSession(t, h, p, tenant, "secret-env-scope")
	intent.LauncherPrincipal = p
	intent.SecretEnv = []sessions.SecretEnvRef{{Env: "GITHUB_TOKEN", Secret: "env/github"}}
	token, err := c.mint(ctx, tenant, intent)
	if err != nil {
		t.Fatal(err)
	}
	_, scope, err := c.Resolve(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if want := "GITHUB_TOKEN<-env/github"; scope.SecretEnv != want {
		t.Fatalf("session scope secret_env = %v, want %v", scope.SecretEnv, want)
	}
}
