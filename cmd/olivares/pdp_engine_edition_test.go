// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// An external policy engine (Cedar file or OPA, OLIVARES_PDP_ENGINE) is a Business
// capability. Community refuses to SERVE with one selected (boot with ServeMode, which
// is `serve` and `quickstart`); the offline commands that also reach boot (dr, audit)
// and buildModules (db, openapi) enforce no policy and are not refused. Each test pins
// the edition it speaks for with asEdition, so it holds in the Community build and in
// the Business overlay build alike.

func setPDPEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, key := range []string{"OLIVARES_PDP_ENGINE", "OLIVARES_PDP_CEDAR_FILE", "OLIVARES_PDP_OPA_URL", "OLIVARES_PDP_OPA_PATH", "OLIVARES_PDP_OPA_TOKEN"} {
		t.Setenv(key, env[key])
	}
}

// bootPDP boots the real engine as the named edition with exactly this PDP environment.
func bootPDP(t *testing.T, edition string, serve bool, dir string, env map[string]string) (*engine, error) {
	t.Helper()
	asEdition(t, edition)
	setPDPEnv(t, env)
	prepareCompositionTestBoot(t)
	eng, err := boot(context.Background(), bootConfig{DataDir: dir, Engine: "sqlite", Version: "test", ServeMode: serve, Logger: discardLog()})
	if err == nil {
		t.Cleanup(func() { _ = eng.Close() })
	}
	return eng, err
}

func TestCommunityServeRefusesExternalPDPEngine(t *testing.T) {
	valid := pdpPolicyFile(t, "valid.cedar", pdpForbidSecret)
	missing := filepath.Join(t.TempDir(), "absent.cedar")
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"cedar with a valid policy", map[string]string{"OLIVARES_PDP_ENGINE": "cedar", "OLIVARES_PDP_CEDAR_FILE": valid}},
		{"cedar without a policy file", map[string]string{"OLIVARES_PDP_ENGINE": "cedar"}},
		// Refused before the file is read: the class is the edition, not the unreadable file.
		{"cedar with a missing policy file", map[string]string{"OLIVARES_PDP_ENGINE": "cedar", "OLIVARES_PDP_CEDAR_FILE": missing}},
		{"mixed-case padded cedar", map[string]string{"OLIVARES_PDP_ENGINE": "  CeDaR  ", "OLIVARES_PDP_CEDAR_FILE": valid}},
		{"opa", map[string]string{"OLIVARES_PDP_ENGINE": "opa",
			"OLIVARES_PDP_OPA_URL": pdpMarkerURL, "OLIVARES_PDP_OPA_PATH": "olivares/allow", "OLIVARES_PDP_OPA_TOKEN": pdpMarkerToken}},
		{"opa without a decision path", map[string]string{"OLIVARES_PDP_ENGINE": "opa", "OLIVARES_PDP_OPA_URL": pdpMarkerURL}},
		{"unsupported engine", map[string]string{"OLIVARES_PDP_ENGINE": pdpMarkerEngine}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			eng, err := bootPDP(t, "community", true, dir, tc.env)
			if err == nil {
				t.Fatal("Community served with an external policy engine configured")
			}
			if eng != nil {
				t.Fatal("a refused boot returned an engine")
			}
			if code := exitcode.From(err); code != exitcode.Edition {
				t.Fatalf("exit code = %d, want %d (Edition): %v", code, exitcode.Edition, err)
			}
			msg := err.Error()
			for _, want := range []string{"OLIVARES_PDP_ENGINE", "Business", pricingURL} {
				if !strings.Contains(msg, want) {
					t.Errorf("refusal %q does not name %q", msg, want)
				}
			}
			for _, leak := range []string{pdpMarkerEngine, pdpMarkerURL, pdpMarkerToken, "absent.cedar", "valid.cedar"} {
				if strings.Contains(msg, leak) {
					t.Errorf("refusal %q carries the operator value %q", msg, leak)
				}
			}
			// Refused before anything is opened: no store, key or lock was created.
			if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
				t.Errorf("the refused boot touched the data directory: %v (err %v)", entries, err)
			}
		})
	}
}

// The real command: exit code 9 reaches the process exit path before anything binds.
func TestCommunityServeCommandRefusesExternalPDPEngine(t *testing.T) {
	asEdition(t, "community")
	setPDPEnv(t, map[string]string{"OLIVARES_PDP_ENGINE": "cedar", "OLIVARES_PDP_CEDAR_FILE": pdpPolicyFile(t, "valid.cedar", pdpForbidSecret)})
	prepareCompositionTestBoot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := newRootCmd()
	root.SetArgs([]string{"serve", "--insecure", "--listen", "127.0.0.1:0", "--grpc-listen", "127.0.0.1:0", "--data-dir", t.TempDir()})
	err := root.ExecuteContext(ctx)
	if err == nil || exitcode.From(err) != exitcode.Edition || !strings.Contains(err.Error(), "Business") {
		t.Fatalf("serve = %v (exit %d), want an Edition refusal that names Business", err, exitcode.From(err))
	}
}

// The default stays open: no engine, a blank one and "none" are the native engine
// alone, and the other OLIVARES_PDP_* keys without an engine stay inert.
func TestCommunityServeStartsWithoutExternalPDPEngine(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"unset", nil},
		{"blank", map[string]string{"OLIVARES_PDP_ENGINE": "   "}},
		{"none", map[string]string{"OLIVARES_PDP_ENGINE": "none"}},
		{"mixed-case padded none", map[string]string{"OLIVARES_PDP_ENGINE": "  NoNe  "}},
		{"cedar file without an engine", map[string]string{"OLIVARES_PDP_CEDAR_FILE": filepath.Join(t.TempDir(), "absent.cedar")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := bootPDP(t, "community", true, t.TempDir(), tc.env); err != nil {
				t.Fatalf("Community refused a configuration with no external engine: %v", err)
			}
		})
	}
}

// Commands that boot an engine without serving (dr, audit) enforce no policy: a restore
// never waits on this setting, and Community keeps the behavior it had.
func TestCommunityOfflineBootIsNotRefusedForExternalPDPEngine(t *testing.T) {
	env := map[string]string{"OLIVARES_PDP_ENGINE": "cedar", "OLIVARES_PDP_CEDAR_FILE": pdpPolicyFile(t, "valid.cedar", pdpForbidSecret)}
	if _, err := bootPDP(t, "community", false, t.TempDir(), env); err != nil {
		t.Fatalf("a non-serving Community boot was refused: %v", err)
	}
}

// Business serves with the same configuration: the refusal is the edition's, not the
// configuration's, and the overlay keeps the capability as it was.
func TestBusinessServeStartsWithExternalPDPEngine(t *testing.T) {
	valid := pdpPolicyFile(t, "valid.cedar", pdpForbidSecret)
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"cedar", map[string]string{"OLIVARES_PDP_ENGINE": "cedar", "OLIVARES_PDP_CEDAR_FILE": valid}},
		{"opa", map[string]string{"OLIVARES_PDP_ENGINE": "opa",
			"OLIVARES_PDP_OPA_URL": pdpMarkerURL, "OLIVARES_PDP_OPA_PATH": "olivares/allow"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := bootPDP(t, "enterprise", true, t.TempDir(), tc.env); err != nil {
				t.Fatalf("the Business edition refused a valid external engine: %v", err)
			}
		})
	}
}
