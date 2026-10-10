// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// A Claude Code session on a key from Providers is told that key's endpoint by name, with
// Claude Code's host pin, and its non-essential traffic is off.
func TestClaudeKeyLaunchIsHeldToItsEndpointAndQuiet(t *testing.T) {
	for _, tc := range []struct{ name, baseURL, want string }{
		{"vendor API", "", "https://api.anthropic.com"},
		{"record's own address", "https://anthropic-gw.example.com/", "https://anthropic-gw.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{}
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(&countingCredentialSource{}),
				WithProviderSecretVault(newFakeVault()))
			m.UseExecutionEnvironmentRef(testEnvRef)
			rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{Kind: ProviderKindAnthropic, DisplayName: "Anthropic",
				APIKey: testProviderKey, BaseURL: tc.baseURL})
			configHome, userHome, _, _ := twoHomes(t)
			prof := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: providerDriverClaude, ConfigHome: configHome, UserHome: userHome,
				DisplayName: "bound", AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref})
			if _, err := launchBound(m, tenant, prof); err != nil {
				t.Fatalf("launch: %v", err)
			}
			spec := runner.lastSpec()
			if got := envValues(spec.Env, "ANTHROPIC_BASE_URL"); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("ANTHROPIC_BASE_URL = %q, want exactly %q", got, tc.want)
			}
			for _, item := range claudeBoundProviderEnv {
				if got := envValues(spec.Env, item.Name); len(got) != 1 || got[0] != "1" {
					t.Errorf("%s = %q, want 1", item.Name, got)
				}
			}
		})
	}
}

// A Claude Code session on the person's own sign-in keeps its own settings: no endpoint and
// none of the host switches (Remote Control needs the feature flags they turn off).
func TestClaudeOwnLoginLaunchKeepsItsOwnSettings(t *testing.T) {
	runner := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(&countingCredentialSource{}))
	m.UseExecutionEnvironmentRef(testEnvRef)
	configHome, userHome, _, _ := twoHomes(t)
	prof := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: providerDriverClaude, ConfigHome: configHome, UserHome: userHome,
		DisplayName: "own", AuthSource: AuthSourceAccountHome})
	if _, err := createProfiledTestRun(t, m, context.Background(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:d19", ActorKind: model.ActorUser, ProviderProfileRef: prof.Ref}); err != nil {
		t.Fatalf("launch: %v", err)
	}
	spec := runner.lastSpec()
	for _, name := range []string{"ANTHROPIC_BASE_URL", "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
		"CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL"} {
		if got := envValues(spec.Env, name); len(got) != 0 {
			t.Errorf("an own-login launch sets %s = %q", name, got)
		}
	}
}

// argAfter returns the value after the last occurrence of flag, and how many times it occurs.
func argAfter(args []string, flag string) (string, int) {
	value, n := "", 0
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			value, n = args[i+1], n+1
		}
	}
	return value, n
}

// The quiet switches also travel as flag settings, which outrank a saved user or project
// settings env; an own-login launch gets none.
func TestClaudeKeyLaunchCarriesTheQuietSwitchesAsFlagSettings(t *testing.T) {
	runner := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(&countingCredentialSource{}), WithProviderSecretVault(newFakeVault()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	rec := mustCreateRecord(t, m, tenant, anthropicInput("Anthropic"))
	configHome, userHome, otherConfig, otherUser := twoHomes(t)
	bound := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: providerDriverClaude, ConfigHome: configHome, UserHome: userHome,
		DisplayName: "bound", AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref})
	if _, err := launchBound(m, tenant, bound); err != nil {
		t.Fatalf("launch: %v", err)
	}
	value, n := argAfter(runner.lastSpec().Args, claudeSettingsFlag)
	var settings struct{ Env map[string]string }
	if n != 1 || json.Unmarshal([]byte(value), &settings) != nil ||
		settings.Env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] != "1" || settings.Env["CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL"] != "1" {
		t.Fatalf("--settings = %q (%d), want the two quiet switches as flag settings", value, n)
	}
	own := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: providerDriverClaude, ConfigHome: otherConfig, UserHome: otherUser,
		DisplayName: "own", AuthSource: AuthSourceAccountHome})
	if _, err := launchBound(m, tenant, own); err != nil {
		t.Fatalf("own-login launch: %v", err)
	}
	if _, n := argAfter(runner.lastSpec().Args, claudeSettingsFlag); n != 0 {
		t.Fatal("an own-login launch carries flag settings")
	}
}

// The governed hook launch writes one settings file; the launch's own flag settings join it
// rather than being dropped.
func TestClaudeHookSettingsKeepTheLaunchsOwnFlagSettings(t *testing.T) {
	dataDir := t.TempDir()
	spec := LaunchSpec{Args: []string{"--print", claudeSettingsFlag, claudeBoundSettings}, Env: []EnvVar{
		{Name: "CLAUDE_CONFIG_DIR", Value: t.TempDir()},
		{Name: "OLIVARES_HOOK_PEP_URL", Value: "http://127.0.0.1:8447/"},
		{Name: "OLIVARES_HOOK_PEP_TOKEN", Value: "test-only-secret"},
	}}
	if err := ConfigureClaudeHookPEP(&spec, dataDir, "run-bound", "/opt/olivares"); err != nil {
		t.Fatal(err)
	}
	path, n := argAfter(spec.Args, claudeSettingsFlag)
	if n != 1 || path != filepath.Join(dataDir, "run", "run-bound", "pep-settings.json") {
		t.Fatalf("args = %v, want one --settings naming the hook file", spec.Args)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Env   map[string]string
		Hooks map[string]any
	}
	if err := json.Unmarshal(raw, &settings); err != nil || len(settings.Hooks) == 0 ||
		settings.Env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] != "1" || settings.Env["CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL"] != "1" {
		t.Fatalf("hook settings = %s, want the hooks and the quiet switches", raw)
	}
}

// A key-bound launch is refused, naming the file, when this host's managed settings leave a
// quiet switch at anything but "1", judged on its effective value across the documents in
// Claude Code's merge order (SR2C 071, Root); the administrator's files are left as they are,
// and an own-login launch is unaffected.
func TestClaudeKeyLaunchRefusesAManagedPolicyThatUndoesItsQuietSwitches(t *testing.T) {
	dir := t.TempDir()
	useClaudeManagedSettingsDir(t, dir)
	runner := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(&countingCredentialSource{}), WithProviderSecretVault(newFakeVault()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	rec := mustCreateRecord(t, m, tenant, anthropicInput("Anthropic"))
	configHome, userHome, otherConfig, otherUser := twoHomes(t)
	bound := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: providerDriverClaude, ConfigHome: configHome, UserHome: userHome,
		DisplayName: "bound", AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref})
	own := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: providerDriverClaude, ConfigHome: otherConfig, UserHome: otherUser,
		DisplayName: "own", AuthSource: AuthSourceAccountHome})
	main, fragment := filepath.Join(dir, "managed-settings.json"), filepath.Join(dir, "managed-settings.d", "50-it.json")
	if err := os.MkdirAll(filepath.Dir(fragment), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	launches := func(what string) {
		t.Helper()
		if _, err := launchBound(m, tenant, bound); err != nil {
			t.Fatalf("%s: key launch = %v, want it to start", what, err)
		}
	}
	refusedNaming := func(what, path string) {
		t.Helper()
		_, err := launchBound(m, tenant, bound)
		if statusOf(err) != http.StatusConflict || !strings.Contains(err.Error(), path) ||
			!strings.Contains(err.Error(), "cannot be kept to its provider's endpoint under this host policy") {
			t.Fatalf("%s: key launch = %v, want the 409 policy refusal naming %s", what, err, path)
		}
	}
	write(main, `{"env":{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":""}}`)
	refusedNaming("base clears the switch", main)
	if _, err := launchBound(m, tenant, own); err != nil {
		t.Fatalf("own-login launch under the same policy = %v", err)
	}
	write(main, `{"env":{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":"0"}}`)
	write(fragment, `{"env":{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":"1"}}`)
	launches("base 0, a later fragment 1")
	write(main, `{"env":{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":"1"},"permissions":{"deny":["WebFetch"]}}`)
	write(fragment, `{"env":{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":"0"}}`)
	refusedNaming("base 1, a later fragment 0", fragment)
	write(fragment, `{"env":{"CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL":"0"}}`)
	refusedNaming("the marketplace switch", fragment)
	write(fragment, `{"env":`)
	refusedNaming("an unreadable fragment", fragment)
	write(fragment, `{"env":{"OTHER":"x"}}`)
	launches("a policy that keeps both switches")
}

// An unreadable or invalid managed document, or a fragments directory that cannot be listed,
// refuses with a remedy (SR5C on fb69f711): fix it, remove it, or use the tool's own sign-in.
func TestClaudeQuietPolicyRefusalsNameARemedy(t *testing.T) {
	for name, setup := range map[string]func(dir string) error{
		"invalid document": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "managed-settings.json"), []byte(`{"env":`), 0o600)
		},
		"fragments that cannot be listed": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "managed-settings.d"), []byte("not a directory"), 0o600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			useClaudeManagedSettingsDir(t, dir)
			if err := setup(dir); err != nil {
				t.Fatal(err)
			}
			err := CheckClaudeQuietHostPolicy()
			if err == nil || !strings.Contains(err.Error(), "fix its permissions or contents, or remove it, or use Claude Code's own sign-in") {
				t.Fatalf("refusal = %v, want one that names the remedy", err)
			}
		})
	}
}
