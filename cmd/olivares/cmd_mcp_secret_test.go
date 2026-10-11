// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// mcpSecretEngine boots a real engine behind HTTP and returns the CLI's
// --server/--token/--tenant for its first organization's admin.
func mcpSecretEngine(t *testing.T) (*engine, []string) {
	t.Helper()
	eng, err := boot(t.Context(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test", Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	srv := httptest.NewServer(eng.api.Handler())
	t.Cleanup(srv.Close)
	setup, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	code, created, _ := doDemoViewJSON(t, eng.api.Handler(), "POST", "/v1/setup", "", "",
		map[string]any{"token": setup, "email": "mcp-secret@olivares.ai", "password": "fixture-password-2026!", "organization": "MCP secret test"})
	if code != http.StatusCreated {
		t.Fatalf("setup = %d", code)
	}
	code, login, _ := doDemoViewJSON(t, eng.api.Handler(), "POST", "/v1/auth/login", "", "",
		map[string]any{"email": "mcp-secret@olivares.ai", "password": "fixture-password-2026!"})
	token, _ := login["token"].(string)
	organization, _ := created["organization"].(map[string]any)
	tenant, _ := organization["tenant_id"].(string)
	if code != http.StatusOK || token == "" || tenant == "" {
		t.Fatalf("login = %d, token set %v, tenant %q", code, token != "", tenant)
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	return eng, []string{"--server", srv.URL, "--token-file", tokenFile, "--tenant", tenant}
}

// #573: `secrets put` stores in the deployment-wide scope and an MCP server only
// resolves its own organization's secrets, so the CLI had no way to give `mcp add`
// the credential its help asked for. `mcp secret` stores it where `mcp add` looks.
func TestMCPSecretStoresWhatMCPAddReferences(t *testing.T) {
	eng, creds := mcpSecretEngine(t)
	valueFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(valueFile, []byte("ghp_fixture-value-573\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, string, error) {
		t.Helper()
		return execSessionCLI(t, nil, append(append([]string{"mcp", args[0]}, creds...), args[1:]...)...)
	}
	out, errb, err := run("secret", "github", "--value-file", valueFile)
	if err != nil {
		t.Fatalf("mcp secret: %v\n%s", err, errb)
	}
	if !strings.Contains(out, "store:mcp/github") || strings.Contains(out+errb, "ghp_fixture-value-573") {
		t.Fatalf("mcp secret output = %q (stderr %q): want the reference, never the value", out, errb)
	}
	out, errb, err = run("add", "github", "--no-test", "--secret-env", "GITHUB_TOKEN=store:mcp/github", "--", "/bin/cat")
	if err != nil || !strings.Contains(out, "Added github (off)") {
		t.Fatalf("mcp add after mcp secret: err=%v out=%q stderr=%q", err, out, errb)
	}
	// The value is sealed in the organization's scope, not the deployment's.
	if _, found, err := eng.secretStore.Get(t.Context(), auth.GlobalSecretScope, "mcp/github"); err != nil || found {
		t.Fatalf("global scope holds mcp/github: found=%v err=%v", found, err)
	}
}

// A reference the organization's store lacks names that store and the command that
// fills it, and says nothing about the deployment-wide store (a tenant admin may not
// read it, so its contents are not this answer's to reveal).
func TestMCPAddMissingSecretNamesTheOrganizationStore(t *testing.T) {
	eng, creds := mcpSecretEngine(t)
	// What `olivares secrets put --name mcp/github` stores: the deployment-wide scope.
	op, err := requireLocalActor(viaCLISecrets, "ops", "issue-573")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.secretStore.Put(t.Context(), op, auth.GlobalSecretScope, "mcp/github", "ghp_fixture-global-573", ""); err != nil {
		t.Fatal(err)
	}
	args := append(append([]string{"mcp", "add"}, creds...), "github", "--no-test", "--secret-env", "GITHUB_TOKEN=store:mcp/github", "--", "/bin/cat")
	_, errb, err := execSessionCLI(t, nil, args...)
	if exitcode.From(err) != exitcode.NotFound {
		t.Fatalf("mcp add with a missing secret: err=%v, want exit %d", err, exitcode.NotFound)
	}
	msg := err.Error() + errb
	for _, want := range []string{`"mcp/github"`, "organization", "olivares mcp secret <name> --value-file"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "global") || strings.Contains(msg, "deployment") {
		t.Fatalf("error %q reveals the deployment-wide store", msg)
	}
}

// The reference users copy (mcp/<name>) and stdin both work, the stored value is the
// file's without its trailing newline, and -o json reports the name and hint only.
func TestMCPSecretFromStdinWithReferenceNameAndJSON(t *testing.T) {
	eng, creds := mcpSecretEngine(t)
	args := append(append([]string{"mcp", "secret"}, creds...), "mcp/github", "--value-file", "-", "-o", "json")
	out, errb, err := execSessionCLI(t, strings.NewReader("ghp_fixture-stdin-573\n"), args...)
	if err != nil {
		t.Fatalf("mcp secret from stdin: %v\n%s", err, errb)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil || got["name"] != "mcp/github" || got["hint"] == "" || len(got) != 2 {
		t.Fatalf("json = %q (%v): want exactly name mcp/github and a hint", out, err)
	}
	tenant := model.TenantID(creds[slices.Index(creds, "--tenant")+1])
	if raw, err := eng.secretStore.Resolve(t.Context(), tenant, "mcp/github"); err != nil || string(raw) != "ghp_fixture-stdin-573" {
		t.Fatalf("stored value matches=%v err=%v", string(raw) == "ghp_fixture-stdin-573", err)
	}
}

// Nothing to store is a usage error before any request: no engine is needed.
func TestMCPSecretRefusesAnEmptyNameOrValue(t *testing.T) {
	dir := t.TempDir()
	empty, full := filepath.Join(dir, "empty"), filepath.Join(dir, "full")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("ghp_fixture-value-573"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, file, want string }{{"github", empty, "value is empty"}, {"mcp/", full, "Name the secret"}} {
		_, _, err := execSessionCLI(t, nil, mcpArgs("http://127.0.0.1:1", "secret", c.name, "--value-file", c.file)...)
		if exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("mcp secret %s: err=%v, want exit %d saying %q", c.name, err, exitcode.Usage, c.want)
		}
	}
}
