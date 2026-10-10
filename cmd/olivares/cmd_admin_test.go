// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// `olivares admin recover` gives someone who lost their only passkey a way back
// from the host. It needs --yes and an attributed operator before it changes anything,
// refuses an unknown person like every other host command, and reports the counts and
// the person's untouched API tokens, with no email in the JSON.
func TestAdminRecoverCLI(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	eng, err := boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Logger: slog.Default()})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if _, err := eng.authr.BootstrapSuperadmin(ctx, "a@acme.test", "supersecret-pw"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	passkeys := func() int {
		t.Helper()
		var n int
		if err := eng.store.AuthView(ctx, func(as store.AuthScope) error {
			rows, _, err := as.WebAuthnCredentials().List(ctx, model.Query{Limit: 10})
			n = len(rows)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if err := eng.store.AuthMutate(ctx, func(as store.AuthScope) error {
		us, _, err := as.Users().List(ctx, model.Query{Limit: 1})
		if err != nil || len(us) != 1 {
			t.Fatalf("users = %v %v", us, err)
		}
		_, err = as.WebAuthnCredentials().Create(ctx, model.WebAuthnCredential{UserID: us[0].ID, CredentialID: "bG9zdC1rZXk", Credential: []byte(`{}`)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = eng.Close()

	exec := func(args ...string) (string, error) {
		cmd := newAdminCmd()
		var out, errOut bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetContext(ctx)
		cmd.SetArgs(append([]string{"recover", "--data-dir", dir}, args...))
		err := cmd.Execute()
		return out.String(), err
	}
	who := []string{"--actor", "ops-oncall", "--reason", "lost passkey"}

	if _, err := exec(append(who, "--email", "a@acme.test")...); exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), "pass --yes") {
		t.Fatalf("without --yes = %v, want a usage error that asks for --yes", err)
	}
	if _, err := exec("--email", "a@acme.test", "--yes"); err == nil || !strings.Contains(err.Error(), "--actor") {
		t.Fatalf("without --actor = %v, want the attribution refusal", err)
	}
	if _, err := exec(append(who, "--email", "ghost@acme.test", "--yes")...); err == nil || !strings.Contains(err.Error(), "no user found") {
		t.Fatalf("unknown email = %v, want the missing-person refusal", err)
	}

	eng, err = boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Logger: slog.Default()})
	if err != nil {
		t.Fatalf("reboot: %v", err)
	}
	if n := passkeys(); n != 1 {
		t.Fatalf("passkeys after the refusals = %d, want 1", n)
	}
	_ = eng.Close()

	out, err := exec(append(who, "--email", "a@acme.test", "--yes", "--format", "json")...)
	if err != nil {
		t.Fatalf("recover: %v\n%s", err, out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("recover JSON is invalid: %v\n%s", err, out)
	}
	tokens, isList := got["active_tokens"].([]any)
	if got["passkeys_removed"] != float64(1) || got["user_id"] == "" || !isList || len(tokens) != 0 || strings.Contains(out, "a@acme.test") {
		t.Fatalf("recover JSON = %s, want 1 passkey removed, the user id, an empty token list and no email", out)
	}

	out, err = exec(append(who, "--email", "a@acme.test", "--yes")...)
	if err != nil || !strings.Contains(out, "Removed 0 passkeys of a@acme.test") ||
		!strings.Contains(out, "registers a new passkey") || !strings.Contains(out, "API tokens are unchanged: a@acme.test owns none.") {
		t.Fatalf("recover text = %v\n%s", err, out)
	}
}
