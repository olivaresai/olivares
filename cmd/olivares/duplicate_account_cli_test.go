// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ed25519"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

func TestCLIDuplicateAccountCreationHasPlainRefusal(t *testing.T) {
	prepareBootstrapCLITest(t)
	ctx := context.Background()
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, err := sys.EnsureSystemTenant(ctx); return err }); err != nil {
		t.Fatal(err)
	}
	authr := auth.NewAuthenticator(st, nil)
	const password = "fixture-password-123"
	if _, err := authr.BootstrapSuperadmin(ctx, "admin@example.test", password); err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	server, err := api.New(api.Options{
		Store: st, Authenticator: authr, Authorizer: auth.NewAuthorizer(nil), Signer: signer,
		SetupToken: secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token")), Version: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	if _, _, err := execRootStdin(t, password, "auth", "login", "--server", httpServer.URL,
		"--email", "admin@example.test", "--password-file", "-"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := execRootStdin(t, password, "users", "create", "--email", "person@example.test", "--password-file", "-"); err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{"person@example.test", " Person@Example.test "} {
		out, stderr, err := execRootStdin(t, password, "users", "create", "--email", email, "--password-file", "-")
		if err == nil || exitcode.From(err) != exitcode.Conflict {
			t.Fatal("duplicate account must be a conflict")
		}
		message := err.Error() + stderr
		if err.Error() != "An account with that email already exists in this organization." {
			t.Error("duplicate does not show exactly the plain sentence")
		}
		for _, internal := range []string{"constraint", "users.tenant_id", "2067", "UNIQUE", "SQLITE", password} {
			if strings.Contains(out+message, internal) {
				t.Error("duplicate prints storage details or the password")
			}
		}
	}
}
