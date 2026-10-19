// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// A Business protection the operator configured must not be dropped in silence by a
// Community binary: serving refuses, names the key and Business, and leaves the data
// directory untouched. Each test pins the edition it speaks for with asEdition.

// businessProtectionInputs is written out here on purpose, apart from the refusal table
// in production: a table row without a case here, or a case without a row, fails
// TestBusinessProtectionKeysMatchTheTests. A seam that takes the environment and has no
// row is caught elsewhere: TestArtifactExemption_EveryEnvFedSeamRefusesOrIsADecision reads
// the seams from edition_ports.go. on is the value that turns the protection on: a config
// path, or true for the two switches the Business readers parse with strconv.ParseBool.
var businessProtectionInputs = []struct{ key, on string }{
	{"OLIVARES_SERVERTOOL_EGRESS_CONFIG", businessProtectionPath},
	{"OLIVARES_CONTENT_FIREWALL_CONFIG", businessProtectionPath},
	{"OLIVARES_HOOK_FIREWALL_CONFIG", businessProtectionPath},
	{"OLIVARES_THREATINTEL_CONFIG", businessProtectionPath},
	{"OLIVARES_INCIDENTLOOP_CONFIG", businessProtectionPath},
	{"OLIVARES_RETENTION_GOVERNOR_CONFIG", businessProtectionPath},
	{"OLIVARES_AUDIT_LEGALHOLD_RECONCILE", "true"},
	{"OLIVARES_RTBF_DEPTH_CONFIG", businessProtectionPath},
	{"OLIVARES_COMPUTER_USE_CONFIG", businessProtectionPath},
	{"OLIVARES_TOOL_PIN_CONFIG", businessProtectionPath},
	{"OLIVARES_RENDER_INSPECTOR_CONFIG", businessProtectionPath},
	{"OLIVARES_ELICITATION_MEDIATOR_CONFIG", businessProtectionPath},
	{"OLIVARES_CAEP_TRANSMITTER_CONFIG", businessProtectionPath},
	{"OLIVARES_CIRCUIT_BREAKER_CONFIG", "true"},
	{"OLIVARES_NIS2INCIDENT_CONFIG", businessProtectionPath},
	{"OLIVARES_REPORTING_CONFIG", businessProtectionPath},
	{"OLIVARES_PII_TOKENS_CONFIG", businessProtectionPath},
}

const businessProtectionPath = "/marker/business-protection-value.json"

// clearBusinessProtectionEnv blanks every key for the test, so an ambient value cannot decide it.
func clearBusinessProtectionEnv(t *testing.T) {
	t.Helper()
	for _, in := range businessProtectionInputs {
		t.Setenv(in.key, "")
	}
}

// bootAsEdition boots the real engine as the named edition.
func bootAsEdition(t *testing.T, edition string, serve bool, dir string) (*engine, error) {
	t.Helper()
	asEdition(t, edition)
	prepareCompositionTestBoot(t)
	eng, err := boot(context.Background(), bootConfig{DataDir: dir, Engine: "sqlite", Version: "test", ServeMode: serve, Logger: discardLog()})
	if err == nil {
		t.Cleanup(func() { _ = eng.Close() })
	}
	return eng, err
}

func TestCommunityServeRefusesConfiguredBusinessProtection(t *testing.T) {
	for _, in := range businessProtectionInputs {
		t.Run(in.key, func(t *testing.T) {
			clearBusinessProtectionEnv(t)
			t.Setenv(in.key, in.on)
			dir := t.TempDir()
			eng, err := bootAsEdition(t, "community", true, dir)
			if err == nil {
				t.Fatalf("Community served with %s configured", in.key)
			}
			if eng != nil {
				t.Fatal("a refused boot returned an engine")
			}
			if code := exitcode.From(err); code != exitcode.Edition {
				t.Fatalf("exit code = %d, want %d (Edition): %v", code, exitcode.Edition, err)
			}
			msg := err.Error()
			for _, want := range []string{in.key, "Business", pricingURL} {
				if !strings.Contains(msg, want) {
					t.Errorf("refusal %q does not name %q", msg, want)
				}
			}
			if strings.Contains(msg, businessProtectionPath) {
				t.Errorf("refusal %q carries the operator value", msg)
			}
			// Refused before anything is opened: no store, key or lock was created.
			if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
				t.Errorf("the refused boot touched the data directory: %v (err %v)", entries, err)
			}
		})
	}
}

// The default stays open: nothing configured, a blank path and a switch set to false
// protect nothing, so Community serves; a boot that does not serve (dr, audit) is never
// refused, so a restore never waits on these keys.
func TestCommunityStartsWithoutBusinessProtectionInForce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		env   map[string]string
		serve bool
	}{
		{"nothing configured", nil, true},
		{"blank path", map[string]string{"OLIVARES_COMPUTER_USE_CONFIG": "   "}, true},
		{"switch false", map[string]string{"OLIVARES_CIRCUIT_BREAKER_CONFIG": "false"}, true},
		{"switch 0", map[string]string{"OLIVARES_AUDIT_LEGALHOLD_RECONCILE": " 0 "}, true},
		{"configured but not serving", map[string]string{"OLIVARES_CONTENT_FIREWALL_CONFIG": businessProtectionPath}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearBusinessProtectionEnv(t)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			if _, err := bootAsEdition(t, "community", tc.serve, t.TempDir()); err != nil {
				t.Fatalf("Community refused a configuration with no Business protection in force: %v", err)
			}
		})
	}
}

// Business is not refused for the same keys: the refusal is the edition's, not the key's.
func TestBusinessIsNotRefusedForBusinessProtectionKeys(t *testing.T) {
	for _, in := range businessProtectionInputs {
		t.Run(in.key, func(t *testing.T) {
			clearBusinessProtectionEnv(t)
			t.Setenv(in.key, in.on)
			asEdition(t, "enterprise")
			if err := refuseBusinessProtectionsInCommunity(osGetenv); err != nil {
				t.Fatalf("the Business edition refused %s: %v", in.key, err)
			}
		})
	}
}

// The production table and this file's list name the same keys and the same two switches,
// so a key added to one alone fails here.
func TestBusinessProtectionKeysMatchTheTests(t *testing.T) {
	want := map[string]bool{}
	for _, in := range businessProtectionInputs {
		want[in.key] = in.on == "true"
	}
	if len(businessProtectionKeys) != len(want) {
		t.Fatalf("production lists %d keys, the tests list %d", len(businessProtectionKeys), len(want))
	}
	for _, k := range businessProtectionKeys {
		isSwitch, ok := want[k.env]
		if !ok {
			t.Errorf("%s is refused in production but has no test case", k.env)
		} else if isSwitch != k.switchKey {
			t.Errorf("%s: production switchKey=%v, the tests treat it as switch=%v", k.env, k.switchKey, isSwitch)
		}
	}
}

// The two switches follow the Business readers, which trim and use strconv.ParseBool: every
// true spelling turns the protection on (so Community refuses), anything else leaves it off.
// A path key is always a path: "false" or "0" is a file name, so it is refused.
func TestBusinessProtectionValueSpellings(t *testing.T) {
	asEdition(t, "community")
	for _, tc := range []struct {
		key, value string
		refused    bool
	}{
		{"OLIVARES_CIRCUIT_BREAKER_CONFIG", "TRUE", true},
		{"OLIVARES_CIRCUIT_BREAKER_CONFIG", " true ", true},
		{"OLIVARES_AUDIT_LEGALHOLD_RECONCILE", "1", true},
		{"OLIVARES_AUDIT_LEGALHOLD_RECONCILE", "t", true},
		{"OLIVARES_CIRCUIT_BREAKER_CONFIG", "yes", false},
		{"OLIVARES_AUDIT_LEGALHOLD_RECONCILE", "on", false},
		{"OLIVARES_CIRCUIT_BREAKER_CONFIG", "False", false},
		{"OLIVARES_COMPUTER_USE_CONFIG", " /etc/olivares/gate.json ", true},
		{"OLIVARES_COMPUTER_USE_CONFIG", "false", true},
		{"OLIVARES_TOOL_PIN_CONFIG", "0", true},
	} {
		getenv := func(key string) string {
			if key == tc.key {
				return tc.value
			}
			return ""
		}
		err := refuseBusinessProtectionsInCommunity(getenv)
		if (err != nil) != tc.refused {
			t.Errorf("%s=%q: refused=%v, want %v (%v)", tc.key, tc.value, err != nil, tc.refused, err)
		}
	}
}

// A Business add-on turned on with `olivares enterprise enable` lives only in the activation
// manifest of the data directory, not in the process environment. A Community binary
// started on that directory refuses to serve, and the check only reads the manifest.
func TestCommunityServeRefusesBusinessProtectionFromActivationManifest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   string
		refused bool
	}{
		{"active entry", ActivationActive, true},
		{"pending entry", ActivationPending, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearBusinessProtectionEnv(t)
			dir := t.TempDir()
			manifest := &ActivationManifest{Entries: []ActivationEntry{
				{Addon: "content-firewall", Env: "OLIVARES_CONTENT_FIREWALL_CONFIG", Value: businessProtectionPath, State: tc.state},
			}}
			if err := SaveActivationManifest(dir, manifest, time.Unix(1_700_000_000, 0)); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(ActivationManifestPath(dir))
			if err != nil {
				t.Fatal(err)
			}
			_, err = bootAsEdition(t, "community", true, dir)
			if tc.refused {
				if err == nil || exitcode.From(err) != exitcode.Edition || !strings.Contains(err.Error(), "OLIVARES_CONTENT_FIREWALL_CONFIG") {
					t.Fatalf("serve = %v (exit %d), want an Edition refusal that names the manifest's key", err, exitcode.From(err))
				}
				if entries, _ := os.ReadDir(dir); len(entries) != 1 {
					t.Errorf("the refused boot touched the data directory: %v", entries)
				}
			} else if err != nil {
				t.Fatalf("Community refused a manifest whose entry is not active: %v", err)
			}
			after, err := os.ReadFile(ActivationManifestPath(dir))
			if err != nil || string(after) != string(before) {
				t.Errorf("the manifest changed (read err %v)", err)
			}
		})
	}
}
