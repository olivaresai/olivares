// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeconfig

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// authSecret is a recognizable credential-shaped VALUE planted in every injected
// credential env var; no finding (Title, SubjectRef, or DetailHash preimage) may ever
// carry it — the auth-posture scanner reports presence/form, never the value.
const authSecret = "sk-ant-SECRET-NEVER-LEAK-0123456789abcdef"

var authAt = time.Unix(1_700_000_000, 0).UTC()

// envLookup adapts a map to the feeder's injectable lookupEnv source.
func envLookup(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

// newAuthFeeder builds a feeder wired with injected env/home/goos sources for the
// auth-posture path (no real process environment or home is touched).
func newAuthFeeder(env map[string]string, home, goos string, expected map[string]struct{}) *Feeder {
	return &Feeder{
		label:             "host-1",
		authPosture:       true,
		expectedAuthModes: expected,
		lookupEnv:         envLookup(env),
		homeDir:           func() (string, error) { return home, nil },
		goos:              goos,
	}
}

// runAuthPosture drives emitAuthPosture directly and returns the auth-posture findings.
func runAuthPosture(t *testing.T, f *Feeder) []model.FindingReport {
	t.Helper()
	sink := &findingSink{}
	if err := f.emitAuthPosture(context.Background(), sink, authAt); err != nil {
		t.Fatalf("emitAuthPosture: %v", err)
	}
	return sink.findings
}

// inventoryFinding returns the single inventory finding (emitted first, title prefix
// "Claude Code credential mode:").
func inventoryFinding(t *testing.T, fs []model.FindingReport) model.FindingReport {
	t.Helper()
	for _, f := range fs {
		if strings.HasPrefix(f.Title, "Claude Code credential mode:") {
			return f
		}
	}
	t.Fatalf("no inventory finding among %d: %s", len(fs), allTitles(fs))
	return model.FindingReport{}
}

// assertNoSecretLeak proves no finding leaks a credential value into ANY string-bearing
// field (Title, SubjectRef, OWASP/ATLAS taxonomy slices, Metadata) and that every DetailHash
// is a 64-char hex. The hash length alone does NOT prove the PREIMAGE is value-free (any
// preimage hashes to 64 hex) — TestAuthPosture_DetailHashPreimageIsValueFree pins that
// directly by reconstructing the exact preimage; this helper guards the cleartext fields.
func assertNoSecretLeak(t *testing.T, fs []model.FindingReport) {
	t.Helper()
	for _, f := range fs {
		if f.Kind != findingAuthPosture {
			t.Fatalf("unexpected finding kind %q", f.Kind)
		}
		// Every string-bearing field of the finding (the brief enumerates these as the leak
		// surface): no credential value, no obvious secret shape.
		fields := append([]string{f.Title, f.SubjectRef}, f.OWASPASI...)
		fields = append(fields, f.OWASPLLM...)
		fields = append(fields, f.ATLAS...)
		for _, v := range fields {
			if strings.Contains(v, authSecret) || strings.Contains(v, "sk-ant-") || strings.Contains(v, "gw.example") {
				t.Fatalf("secret/value leaked into a finding field: %q (title %q)", v, f.Title)
			}
		}
		if len(f.DetailHash) != 64 {
			t.Fatalf("DetailHash must be a 64-char hex SHA-256, got %d: %q", len(f.DetailHash), f.DetailHash)
		}
	}
}

// writeCreds writes a <home>/.claude/.credentials.json with the given mode and returns home.
func writeCreds(t *testing.T, mode os.FileMode) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".credentials.json")
	// Content is irrelevant — the scanner stats it, never reads it. Plant a secret anyway to
	// prove the contents never surface.
	if err := os.WriteFile(path, []byte(`{"token":"`+authSecret+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return home
}

// TestAuthPosture_ModeByPrecedence is the table per credential mode: each isolated source
// resolves to its effective mode, never leaking the value.
func TestAuthPosture_ModeByPrecedence(t *testing.T) {
	subHome := writeCreds(t, 0o600)
	helperHome := t.TempDir()
	writeFile(t, filepath.Join(helperHome, ".claude", "settings.json"),
		`{"apiKeyHelper":"/usr/local/bin/`+authSecret+`.sh"}`)

	cases := []struct {
		name string
		env  map[string]string
		home string
		want authMode
	}{
		{"cloud_provider", map[string]string{envUseBedrock: "1"}, t.TempDir(), authCloudProvider},
		{"auth_token", map[string]string{envAuthToken: authSecret}, t.TempDir(), authAuthToken},
		{"api_key", map[string]string{envAPIKey: authSecret}, t.TempDir(), authAPIKey},
		{"api_key_helper", map[string]string{}, helperHome, authAPIKeyHelper},
		{"oauth_token", map[string]string{envOAuthToken: authSecret}, t.TempDir(), authOAuthToken},
		{"subscription", map[string]string{}, subHome, authSubscription},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuthFeeder(tc.env, tc.home, "linux", nil)
			fs := runAuthPosture(t, f)
			assertNoSecretLeak(t, fs)
			inv := inventoryFinding(t, fs)
			if !strings.Contains(inv.Title, string(tc.want)) {
				t.Fatalf("want mode %q, title=%q", tc.want, inv.Title)
			}
			if inv.Severity != model.SeverityInfo {
				t.Fatalf("inventory severity must be Info without an expectation, got %v", inv.Severity)
			}
		})
	}
}

// TestAuthPosture_Precedence: when several sources coexist, the EFFECTIVE mode is the
// highest-precedence one and all coexisting sources are listed.
func TestAuthPosture_Precedence(t *testing.T) {
	home := writeCreds(t, 0o600) // subscription present (lowest precedence)
	env := map[string]string{
		envAPIKey:    authSecret, // level 3
		envAuthToken: authSecret, // level 2 — should WIN
	}
	fs := runAuthPosture(t, newAuthFeeder(env, home, "linux", nil))
	assertNoSecretLeak(t, fs)
	inv := inventoryFinding(t, fs)
	if !strings.Contains(inv.Title, "credential mode: auth_token") {
		t.Fatalf("auth_token must win precedence over api_key+subscription: %q", inv.Title)
	}
	for _, want := range []string{"auth_token", "api_key", "subscription"} {
		if !strings.Contains(inv.Title, want) {
			t.Fatalf("coexisting source %q must be listed: %q", want, inv.Title)
		}
	}
}

// TestAuthPosture_HonorsConfigDir: CLAUDE_CONFIG_DIR overrides ~/.claude for the
// .credentials.json lookup (a different home with no creds must not mask it).
func TestAuthPosture_HonorsConfigDir(t *testing.T) {
	credHome := writeCreds(t, 0o600)
	configDir := filepath.Join(credHome, ".claude")
	emptyHome := t.TempDir() // no .credentials.json here
	env := map[string]string{envConfigDir: configDir}
	fs := runAuthPosture(t, newAuthFeeder(env, emptyHome, "linux", nil))
	inv := inventoryFinding(t, fs)
	if !strings.Contains(inv.Title, "credential mode: subscription") {
		t.Fatalf("CLAUDE_CONFIG_DIR must be honored for .credentials.json: %q", inv.Title)
	}
}

// TestAuthPosture_OverPermissionedCreds: a .credentials.json broader than 0600 is a Medium
// posture finding naming the octal mode; a 0600 file is clean.
func TestAuthPosture_OverPermissionedCreds(t *testing.T) {
	open := writeCreds(t, 0o644)
	fs := runAuthPosture(t, newAuthFeeder(map[string]string{}, open, "linux", nil))
	assertNoSecretLeak(t, fs)
	if !hasSeverity(fs, model.SeverityMedium, "0644") || !hasTitle(fs, "broader than 0600") {
		t.Fatalf("over-permissioned creds must drift Medium naming the mode: %s", allTitles(fs))
	}
	// 0600 → no over-permission finding.
	tight := writeCreds(t, 0o600)
	fs = runAuthPosture(t, newAuthFeeder(map[string]string{}, tight, "linux", nil))
	if hasTitle(fs, "broader than 0600") {
		t.Fatalf("a 0600 creds file must not drift: %s", allTitles(fs))
	}
}

// TestAuthPosture_ShadowFootgun: an api_key/auth_token shadowing present subscription
// credentials is a Low finding (the documented disabled-org failure mode).
func TestAuthPosture_ShadowFootgun(t *testing.T) {
	home := writeCreds(t, 0o600)
	env := map[string]string{envAPIKey: authSecret}
	fs := runAuthPosture(t, newAuthFeeder(env, home, "linux", nil))
	assertNoSecretLeak(t, fs)
	if !hasSeverity(fs, model.SeverityLow, "takes precedence over its subscription credentials on disk") {
		t.Fatalf("api_key shadowing a subscription must drift Low: %s", allTitles(fs))
	}
}

// TestAuthPosture_ExpectationContradiction: with an operator allowlist, an effective mode
// outside it bumps the inventory finding to Medium; inside it stays Info.
func TestAuthPosture_ExpectationContradiction(t *testing.T) {
	env := map[string]string{envAPIKey: authSecret}
	expectSub := map[string]struct{}{string(authSubscription): {}}
	fs := runAuthPosture(t, newAuthFeeder(env, t.TempDir(), "linux", expectSub))
	inv := inventoryFinding(t, fs)
	if inv.Severity != model.SeverityMedium || !strings.Contains(inv.Title, "CONTRADICTS") {
		t.Fatalf("api_key against an expected-subscription policy must drift Medium: %q", inv.Title)
	}
	// api_key inside the allowlist → Info.
	expectKey := map[string]struct{}{string(authAPIKey): {}}
	fs = runAuthPosture(t, newAuthFeeder(env, t.TempDir(), "linux", expectKey))
	if inventoryFinding(t, fs).Severity != model.SeverityInfo {
		t.Fatalf("an allowed mode must stay Info")
	}
}

// TestAuthPosture_DarwinKeychain: on darwin with no .credentials.json, subscription is
// presumed (Keychain not introspectable) at Info, stated honestly.
func TestAuthPosture_DarwinKeychain(t *testing.T) {
	fs := runAuthPosture(t, newAuthFeeder(map[string]string{}, t.TempDir(), "darwin", nil))
	inv := inventoryFinding(t, fs)
	if !strings.Contains(inv.Title, "credential mode: subscription") || !strings.Contains(inv.Title, "Keychain") {
		t.Fatalf("darwin without a creds file must presume subscription via Keychain honestly: %q", inv.Title)
	}
	if inv.Severity != model.SeverityInfo {
		t.Fatalf("presumed-Keychain subscription must be Info")
	}
}

// TestAuthPosture_NoneObserved: a non-darwin host with no creds and no env reports mode
// none honestly (never invents subscription).
func TestAuthPosture_NoneObserved(t *testing.T) {
	fs := runAuthPosture(t, newAuthFeeder(map[string]string{}, t.TempDir(), "linux", nil))
	inv := inventoryFinding(t, fs)
	if !strings.Contains(inv.Title, "credential mode: none") {
		t.Fatalf("no creds observed must report none, not a guess: %q", inv.Title)
	}
	if inv.Severity != model.SeverityInfo {
		t.Fatalf("none is honest inventory, Info")
	}
}

// TestAuthPosture_BaseURLNoted: a host with ANTHROPIC_BASE_URL set surfaces the override as
// a coexisting fact in the inventory finding (presence only — never the URL).
func TestAuthPosture_BaseURLNoted(t *testing.T) {
	env := map[string]string{envAuthToken: authSecret, envBaseURL: "https://gw.example/" + authSecret}
	fs := runAuthPosture(t, newAuthFeeder(env, t.TempDir(), "linux", nil))
	assertNoSecretLeak(t, fs)
	inv := inventoryFinding(t, fs)
	if !strings.Contains(inv.Title, "ANTHROPIC_BASE_URL override set") {
		t.Fatalf("a base-URL override must be noted: %q", inv.Title)
	}
	if strings.Contains(inv.Title, "gw.example") {
		t.Fatalf("the base-URL value must never be emitted: %q", inv.Title)
	}
}

// TestAuthPosture_OpenRejectsUnknownMode: Open validates the expected_auth_modes allowlist
// loudly (a typo'd mode is an error, not a silent always-contradict).
func TestAuthPosture_OpenRejectsUnknownMode(t *testing.T) {
	f := New()
	err := f.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"root": t.TempDir(), "expected_auth_modes": "subscription, sbscription",
	}})
	if err == nil || !strings.Contains(err.Error(), "unknown credential mode") {
		t.Fatalf("an unknown expected mode must fail Open loudly, got: %v", err)
	}
}

// hasSeverity reports whether some finding has the given severity and title substring.
func hasSeverity(fs []model.FindingReport, sev model.Severity, sub string) bool {
	for _, f := range fs {
		if f.Severity == sev && strings.Contains(f.Title, sub) {
			return true
		}
	}
	return false
}

// TestAuthPosture_DetailHashPreimageIsValueFree pins the MINIMAL-DATA invariant on the HASH
// INPUT directly (not just on the cleartext fields): it plants secrets in every credential
// source, then reconstructs the EXACT value-free preimage of each emitted finding and asserts
// DetailHash == redact.Hash(preimage). A future refactor that interpolates an env value, the
// base-URL or a script path into the preimage changes the hash and fails here — which a
// len(DetailHash)==64 check can never catch.
func TestAuthPosture_DetailHashPreimageIsValueFree(t *testing.T) {
	home := writeCreds(t, 0o600) // subscription present → inventory + shadow both fire
	env := map[string]string{envAPIKey: authSecret, envBaseURL: "https://gw/" + authSecret}
	fs := runAuthPosture(t, newAuthFeeder(env, home, "linux", nil))
	assertNoSecretLeak(t, fs)

	const host = "host-1" // SanitizeDisplay("host-1") == "host-1"
	// effective mode api_key; present api_key+subscription; base_url true; keychain false.
	wantInv := "auth-posture host=" + host + " mode=api_key present=api_key+subscription base_url=true keychain=false"
	wantShadow := "auth-posture-shadow host=" + host + " mode=api_key keychain=false"
	for _, pre := range []string{wantInv, wantShadow} {
		if strings.Contains(pre, authSecret) || strings.Contains(pre, "gw/") {
			t.Fatalf("reconstructed preimage itself carries a value — test bug: %q", pre)
		}
	}
	var gotInv, gotShadow string
	for _, f := range fs {
		switch {
		case strings.HasPrefix(f.Title, "Claude Code credential mode:"):
			gotInv = f.DetailHash
		case strings.Contains(f.Title, "takes precedence over"):
			gotShadow = f.DetailHash
		}
	}
	if gotInv != redact.Hash(wantInv) {
		t.Fatalf("inventory DetailHash preimage drifted from the value-free reconstruction")
	}
	if gotShadow != redact.Hash(wantShadow) {
		t.Fatalf("shadow DetailHash preimage drifted from the value-free reconstruction")
	}
}

// TestAuthPosture_CloudVariants: every CLAUDE_CODE_USE_* variant resolves to cloud_provider,
// and a falsy value ("0") does NOT enable it (truthy-only flag detection).
func TestAuthPosture_CloudVariants(t *testing.T) {
	for _, key := range []string{envUseVertex, envUseFoundry, envUseAnthropicAWS, envUseMantle} {
		t.Run(key, func(t *testing.T) {
			fs := runAuthPosture(t, newAuthFeeder(map[string]string{key: "1"}, t.TempDir(), "linux", nil))
			if !strings.Contains(inventoryFinding(t, fs).Title, "credential mode: cloud_provider") {
				t.Fatalf("%s=1 must resolve to cloud_provider", key)
			}
		})
	}
	fs := runAuthPosture(t, newAuthFeeder(map[string]string{envUseBedrock: "0"}, t.TempDir(), "linux", nil))
	if strings.Contains(inventoryFinding(t, fs).Title, "cloud_provider") {
		t.Fatalf("CLAUDE_CODE_USE_BEDROCK=0 must NOT enable cloud mode")
	}
}

// TestAuthPosture_CloudWinsPrecedence: cloud_provider is the highest band and wins even when
// every lower source coexists; all coexisting sources are listed.
func TestAuthPosture_CloudWinsPrecedence(t *testing.T) {
	home := writeCreds(t, 0o600)
	env := map[string]string{envUseBedrock: "1", envAuthToken: authSecret, envAPIKey: authSecret, envOAuthToken: authSecret}
	fs := runAuthPosture(t, newAuthFeeder(env, home, "linux", nil))
	assertNoSecretLeak(t, fs)
	inv := inventoryFinding(t, fs)
	if !strings.Contains(inv.Title, "credential mode: cloud_provider") {
		t.Fatalf("cloud_provider must win the highest precedence band: %q", inv.Title)
	}
	for _, w := range []string{"cloud_provider", "auth_token", "api_key", "oauth_token", "subscription"} {
		if !strings.Contains(inv.Title, w) {
			t.Fatalf("coexisting source %q must be listed: %q", w, inv.Title)
		}
	}
}

// TestAuthPosture_DarwinShadow: on darwin the subscription is in the Keychain (no file), yet
// an api_key still shadows it — the Low advisory fires (gated on `subscription`, not the
// on-disk file) with honest "presumed Keychain" wording.
func TestAuthPosture_DarwinShadow(t *testing.T) {
	fs := runAuthPosture(t, newAuthFeeder(map[string]string{envAPIKey: authSecret}, t.TempDir(), "darwin", nil))
	assertNoSecretLeak(t, fs)
	if !hasSeverity(fs, model.SeverityLow, "presumed macOS Keychain subscription") {
		t.Fatalf("api_key must shadow a presumed Keychain subscription on darwin: %s", allTitles(fs))
	}
}

// TestAuthPosture_NoneWithExpectationNoBump: a logged-out host (mode none) with an allowlist
// set stays Info — absence is not a policy contradiction (never invent drift from absence).
func TestAuthPosture_NoneWithExpectationNoBump(t *testing.T) {
	expect := map[string]struct{}{string(authSubscription): {}}
	inv := inventoryFinding(t, runAuthPosture(t, newAuthFeeder(map[string]string{}, t.TempDir(), "linux", expect)))
	if !strings.Contains(inv.Title, "credential mode: none") || inv.Severity != model.SeverityInfo {
		t.Fatalf("none must stay Info even with an expectation set: %q sev=%v", inv.Title, inv.Severity)
	}
}

// TestAuthPosture_GatherIntegration drives the feeder through Open+Gather (not the direct
// emitAuthPosture call): it proves auth_posture is default-ON, the expected_auth_modes parsed
// at Open reaches the Gather emission, and the secret never leaks end-to-end.
func TestAuthPosture_GatherIntegration(t *testing.T) {
	f := New()
	if err := f.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"root": t.TempDir(), "expected_auth_modes": "subscription", // no auth_posture key → default ON
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	f.lookupEnv = envLookup(map[string]string{envAPIKey: authSecret})
	f.homeDir = func() (string, error) { return t.TempDir(), nil }
	f.goos = "linux"
	sink := &findingSink{}
	if err := f.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	assertNoSecretLeak(t, sink.findings)
	inv := inventoryFinding(t, sink.findings)
	if inv.Severity != model.SeverityMedium || !strings.Contains(inv.Title, "api_key") {
		t.Fatalf("Open-parsed allowlist must flag api_key against expected-subscription through Gather: %q", inv.Title)
	}
	// auth_posture=false must suppress it through the same path.
	f2 := New()
	if err := f2.Open(context.Background(), sdk.Config{Settings: map[string]string{"root": t.TempDir(), "auth_posture": "false"}}); err != nil {
		t.Fatal(err)
	}
	f2.lookupEnv = envLookup(map[string]string{envAPIKey: authSecret})
	f2.homeDir = func() (string, error) { return t.TempDir(), nil }
	sink2 := &findingSink{}
	if err := f2.Gather(context.Background(), sink2); err != nil {
		t.Fatal(err)
	}
	for _, fr := range sink2.findings {
		if fr.Kind == findingAuthPosture {
			t.Fatalf("auth_posture=false must emit no auth-posture finding: %q", fr.Title)
		}
	}
}

// TestAuthPosture_AllowlistParsing proves the parseNameList contract newly relies on:
// a deliberate "[]" is a non-nil empty lockdown allowlist (flags every credentialed host),
// but a MALFORMED array is nil (no allowlist) — never a silent deny-all.
func TestAuthPosture_AllowlistParsing(t *testing.T) {
	if got := parseNameList(`["subscription"`); got != nil {
		t.Fatalf("a malformed JSON array must yield nil (no allowlist), got %v", got)
	}
	if got := parseNameList("[]"); got == nil || len(got) != 0 {
		t.Fatalf(`"[]" must yield a non-nil EMPTY allowlist (deliberate lockdown), got %v`, got)
	}
	// End-to-end: "[]" flags a credentialed host Medium (consistent lockdown semantics).
	f := New()
	if err := f.Open(context.Background(), sdk.Config{Settings: map[string]string{"root": t.TempDir(), "expected_auth_modes": "[]"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	f.lookupEnv = envLookup(map[string]string{envAPIKey: authSecret})
	f.homeDir = func() (string, error) { return t.TempDir(), nil }
	f.goos = "linux"
	sink := &findingSink{}
	if err := f.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if inventoryFinding(t, sink.findings).Severity != model.SeverityMedium {
		t.Fatalf("an empty [] allowlist must flag a credentialed host Medium")
	}
}
