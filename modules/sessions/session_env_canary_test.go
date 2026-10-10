// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// TestSessionChildEnvCarriesNoEngineSecret pins that a session's child process
// sees an allow-listed environment: engine secrets placed in the engine's own
// environment (database URL, signing and vault keys, provider keys the session is
// not bound to) never reach it, while the session's bound credential does. The
// child is a real OS process that dumps its whole environment. The env_allow case
// is the run creator naming those secrets in the launch request: the launch is
// refused with a 400 that names one, or its child must not see them.
func TestSessionChildEnvCarriesNoEngineSecret(t *testing.T) {
	canaries := map[string]string{
		"DATABASE_URL":                "postgres://olv:canary-db@db/olivares",
		"PGPASSWORD":                  "canary-pgpassword",
		"OLIVARES_DSN":                "canary-olivares-dsn",
		"OLIVARES_AUDIT_SIGNING_KEY":  "canary-audit-signing",
		"OLIVARES_POLICY_SIGNING_KEY": "canary-policy-signing",
		"OLIVARES_SECRET_STORE_KEY":   "canary-secret-store",
		"OLIVARES_MASTER_KEY":         "canary-master",
		"VAULT_TOKEN":                 "canary-vault",
		"AWS_ACCESS_KEY_ID":           "canary-aws-key-id",
		"AWS_SECRET_ACCESS_KEY":       "canary-aws-secret",
		"AWS_SESSION_TOKEN":           "canary-aws-session",
		"OLIVARES_TOTP_SEED_KEY":      "canary-totp-seed",
		"OLIVARES_SSO_SECRET_KEY":     "canary-sso",
		"ANTHROPIC_API_KEY":           "canary-anthropic",
		"OPENAI_API_KEY":              "canary-openai",
		"XAI_API_KEY":                 "canary-xai",
		"GROK_API_KEY":                "canary-grok",
		"CODEX_API_KEY":               "canary-codex",
		// A name no deny list knows: only an allow-list keeps it out.
		"OLV_UNLISTED_CANARY": "canary-unlisted",
	}
	for name, value := range canaries {
		t.Setenv(name, value)
		// A value inside another would name the wrong secret as leaked.
		for other, v := range canaries {
			if other != name && strings.Contains(v, value) {
				t.Fatalf("canary %s (%q) is inside %s (%q)", name, value, other, v)
			}
		}
	}
	for _, tc := range []struct {
		name     string
		envAllow []string
	}{
		{name: "inherited"},
		// Names the engine reads as its own secrets: the DSN reference, the ledger
		// signer's AWS keys, a vault token.
		{name: "env_allow", envAllow: []string{"DATABASE_URL", "PGPASSWORD", "VAULT_TOKEN", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen, err := launchAndDumpChildEnv(t, tc.envAllow)
			if err != nil {
				// A refused launch starts no child, so nothing reaches one. Only a 400
				// that names a requested secret is that refusal.
				var refused *runErr
				if !errors.As(err, &refused) || refused.status != http.StatusBadRequest ||
					!slices.ContainsFunc(tc.envAllow, func(n string) bool { return strings.Contains(refused.msg, n) }) {
					t.Fatalf("the launch failed, but not as a refusal of a named engine secret: %v", err)
				}
				return
			}
			var leaked []string
			for name, value := range canaries {
				if strings.Contains(seen, value) {
					leaked = append(leaked, name)
				}
			}
			slices.Sort(leaked)
			if len(leaked) > 0 {
				t.Errorf("engine secrets reached the session child: %v", leaked)
			}
		})
	}
}

// launchAndDumpChildEnv launches a run whose child writes its whole environment
// to a file, and returns that dump, or the launch error.
func launchAndDumpChildEnv(t *testing.T, envAllow []string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "child-env")
	script := filepath.Join(dir, "fixture-claude.sh")
	body := "#!/bin/sh\n" +
		"trap 'exit 0' TERM\n" +
		"env > '" + out + "'\n" +
		"printf '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"sess-env\"}\\n'\n" +
		"while IFS= read -r line; do :; done\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	m, _, tenant, _ := newRuntimeHarness(t,
		WithRunner(NewProcRunner()), WithProgram(script),
		WithCredentialSource(staticCred()), WithStopWaitDelay(2*time.Second))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, PermissionMode: "default", Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser, EnvAllow: envAllow,
	})
	if err != nil {
		if len(envAllow) == 0 {
			t.Fatalf("createRun: %v", err)
		}
		return "", err
	}
	waitFor(t, "fixture init capture", func() bool {
		d, err := m.getRun(ctx, tenant, dto.RunRef)
		if err != nil {
			t.Fatalf("getRun: %v", err)
		}
		return d.ClaudeSessionID == "sess-env"
	})
	defer func() {
		if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
			t.Errorf("stop: %v", err)
		}
	}()

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the child wrote no environment: %v", err)
	}
	seen := string(raw)
	// Control: the dump is the child's real environment and carries the bound credential.
	if !strings.Contains(seen, "ANTHROPIC_AUTH_TOKEN=tok-secret\n") {
		var names []string
		for _, line := range strings.Split(seen, "\n") {
			if name, _, ok := strings.Cut(line, "="); ok {
				names = append(names, name)
			}
		}
		t.Fatalf("the bound session credential did not reach the child; it saw only %v", names)
	}
	return seen, nil
}
