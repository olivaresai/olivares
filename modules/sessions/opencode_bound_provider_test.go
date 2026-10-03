// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// A SESSION BOUND TO A PROVIDER RECORD REACHES ONLY THAT PROVIDER (HU2 019, Root 21:16Z).
// An OpenCode session on an Anthropic key answered on OpenCode's own hosted model
// (opencode/big-pickle): the launch gave OpenCode the key and nothing that confined it.

// openCodeInline parses the launch's OPENCODE_CONFIG_CONTENT.
func openCodeInline(t *testing.T, env map[string]string) map[string]any {
	t.Helper()
	raw, ok := env[envOpenCodeConfigContent]
	if !ok {
		t.Fatalf("the launch carries no %s", envOpenCodeConfigContent)
	}
	cfg := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("%s is not JSON: %v", envOpenCodeConfigContent, err)
	}
	return cfg
}

func envMap(env []EnvVar) map[string]string {
	out := map[string]string{}
	for _, item := range env {
		out[item.Name] = item.Value
	}
	return out
}

// assertOpenCodeConfined checks the confinement a record-bound launch must carry.
func assertOpenCodeConfined(t *testing.T, env map[string]string, provider string) {
	t.Helper()
	cfg := openCodeInline(t, env)
	if got := cfg["enabled_providers"]; !reflect.DeepEqual(got, []any{provider}) {
		t.Errorf("enabled_providers = %v, want exactly [%s]", got, provider)
	}
	if got := cfg["disabled_providers"]; !reflect.DeepEqual(got, []any{openCodeHostedProviderID}) {
		t.Errorf("disabled_providers = %v, want OpenCode's hosted provider by name", got)
	}
	if cfg["share"] != "disabled" {
		t.Errorf("share = %v, want disabled", cfg["share"])
	}
	for name, want := range map[string]string{envOpenCodeDisableModelsFetch: "1", envOpenCodeDisableLSPDownload: "1", envNPMConfigOffline: "true",
		envOpenCodeDisableShare: "1"} {
		if env[name] != want {
			t.Errorf("%s = %q, want %q (a request to a host other than the bound provider)", name, env[name], want)
		}
	}
}

// openCodeKeyLaunch binds an OpenCode profile to a key record and returns the launch's
// environment, or the mint's error.
func openCodeKeyLaunch(t *testing.T, in CreateProviderRecordInput) (map[string]string, error) {
	t.Helper()
	m, _, tenant, _ := newRuntimeHarness(t, WithProviderDriver(NewOpenCodeDriver()), WithProviderSecretVault(newFakeVault()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	rec := mustCreateRecord(t, m, tenant, in)
	profile, err := m.CreateProfile(context.Background(), tenant, CreateProfileInput{Driver: providerDriverOpenCode, ConfigHome: t.TempDir(), UserHome: t.TempDir(),
		AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref})
	if err != nil {
		return nil, err // a record OpenCode cannot be held to is refused when it is bound
	}
	p := CreateRunParams{ProviderProfileRef: profile.Ref}
	if err := m.resolveLaunchProfileInto(context.Background(), tenant, &p); err != nil {
		t.Fatal(err)
	}
	cred, env, err := m.mintLaunchAuthority(context.Background(), tenant, "", p)
	if err != nil {
		return nil, err
	}
	spec := m.buildLaunchSpec(p, cred, WorkSessionCredential{}, CommunicationSessionCredential{}, "", nil, nil, env)
	return envMap(spec.Env), nil
}

func TestOpenCodeOnAKeyRecordReachesOnlyThatProvider(t *testing.T) {
	for _, tc := range []struct{ kind, provider, keyEnv, baseURL string }{
		{ProviderKindAnthropic, "anthropic", "ANTHROPIC_API_KEY", "https://api.anthropic.com/v1"},
		{ProviderKindOpenAI, "openai", "OPENAI_API_KEY", "https://api.openai.com/v1"},
		{ProviderKindXAI, "xai", "XAI_API_KEY", "https://api.x.ai/v1"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			env, err := openCodeKeyLaunch(t, CreateProviderRecordInput{Kind: tc.kind, DisplayName: "Key", APIKey: testProviderKey})
			if err != nil {
				t.Fatalf("launch on a %s key = %v", tc.kind, err)
			}
			assertOpenCodeConfined(t, env, tc.provider)
			// The address is pinned with the provider, so a baseURL saved for it in a profile
			// or a project cannot move the key and the prompt elsewhere.
			provider, _ := openCodeInline(t, env)["provider"].(map[string]any)
			pinned, _ := provider[tc.provider].(map[string]any)
			options, _ := pinned["options"].(map[string]any)
			if options["baseURL"] != tc.baseURL {
				t.Fatalf("provider.%s.options.baseURL = %v, want %s", tc.provider, options["baseURL"], tc.baseURL)
			}
			if env[tc.keyEnv] == "" {
				t.Fatalf("the key does not reach the child as %s", tc.keyEnv)
			}
		})
	}
}

func TestOpenCodeRefusesARecordItCannotBeConfinedTo(t *testing.T) {
	for name, in := range map[string]CreateProviderRecordInput{
		"openai-compatible endpoint":   {Kind: ProviderKindOpenAICompatible, DisplayName: "Gateway", APIKey: testProviderKey, BaseURL: "https://llm.example.com/v1"},
		"anthropic at its own address": {Kind: ProviderKindAnthropic, DisplayName: "Proxy", APIKey: testProviderKey, BaseURL: "https://anthropic.example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := openCodeKeyLaunch(t, in)
			var re *runErr
			if !errors.As(err, &re) || re.status != http.StatusUnprocessableEntity || !strings.Contains(re.msg, "OpenCode runs only on") ||
				!strings.Contains(re.msg, "does not start") {
				t.Fatalf("launch = %v, want 422 naming what OpenCode runs on", err)
			}
		})
	}
}

func TestOpenCodeOnALocalModelPinsTheSmallModelAndDisablesTheHostedProvider(t *testing.T) {
	env := envMap(openCodeDriver{}.LaunchEnv(DriverLaunch{LocalModelEndpoint: "http://127.0.0.1:11434/v1", LocalModels: []string{"qwen3:8b"}}))
	assertOpenCodeConfined(t, env, openCodeLocalProviderID)
	cfg := openCodeInline(t, env)
	if cfg["model"] != "olivares_ollama/qwen3:8b" || cfg["small_model"] != cfg["model"] {
		t.Fatalf("model = %v, small_model = %v, want both the local model (titles and summaries use small_model)", cfg["model"], cfg["small_model"])
	}
}

func TestOpenCodeWithNoRecordKeepsItsOwnConfiguration(t *testing.T) {
	env := envMap(openCodeDriver{}.LaunchEnv(DriverLaunch{Preset: PresetAsk}))
	cfg := openCodeInline(t, env)
	for _, key := range []string{"enabled_providers", "disabled_providers", "share", "model"} {
		if _, ok := cfg[key]; ok {
			t.Errorf("a launch on the tool's own sign-in sets %s; only a record-bound launch is confined", key)
		}
	}
	for _, name := range []string{envOpenCodeDisableModelsFetch, envOpenCodeDisableLSPDownload, envNPMConfigOffline} {
		if _, ok := env[name]; ok {
			t.Errorf("a launch on the tool's own sign-in sets %s", name)
		}
	}
}

func TestOpenCodeAnUnmappedBoundKindEnablesNoProvider(t *testing.T) {
	env := envMap(openCodeDriver{}.LaunchEnv(DriverLaunch{BoundProvider: BoundProvider{Kind: "something-new"}}))
	assertOpenCodeConfined(t, env, openCodeUnconfinedProviderID)
}

// The child itself receives the confinement, on the first launch and on resume (the
// fixture records its environment).
func TestOpenCodeRuntimeKeyRecordConfinesTheChildOnLaunchAndResume(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t, openCodeRuntimeOptions(WithProviderSecretVault(newFakeVault()))...)
	m.UseExecutionEnvironmentRef(testEnvRef)
	rec := mustCreateRecord(t, m, tenant, anthropicInput("Anthropic"))
	prof := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: providerDriverOpenCode, ConfigHome: t.TempDir(), UserHome: t.TempDir(),
		DisplayName: "OpenCode (Anthropic)", AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref})
	first := setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "ses-key"})
	run, err := openCodeLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	peer := readOpenCodeFixtureRecord(t, first)
	assertOpenCodeConfined(t, peer.Env, "anthropic")
	if !peer.Present["ANTHROPIC_API_KEY"] {
		t.Fatal("the bound key did not reach the child")
	}
	if _, err := m.stopRun(context.Background(), tenant, run.RunRef, "user:u1", model.ActorUser); err != nil {
		t.Fatalf("stop: %v", err)
	}
	resumeRecord := setOpenCodeFixture(t, prof, openCodeFixture{RecordPath: t.TempDir() + "/resume.json", ResumeEmptyObject: true})
	resumed, err := m.resumeRun(context.Background(), tenant, run.RunRef, "user:u1", model.ActorUser, "")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, resumed.RunRef, "user:u1", model.ActorUser) })
	assertOpenCodeConfined(t, readOpenCodeFixtureRecord(t, resumeRecord).Env, "anthropic")
}

// A carrier naming any address but the vendor's own enables no provider.
func TestOpenCodeAKeyCarrierAtAnotherAddressEnablesNoProvider(t *testing.T) {
	env := envMap(openCodeDriver{}.LaunchEnv(DriverLaunch{BoundProvider: BoundProvider{Kind: ProviderKindAnthropic, Endpoint: "https://elsewhere.example.com"}}))
	assertOpenCodeConfined(t, env, openCodeUnconfinedProviderID)
}

// OpenCode merges the host's managed configuration after the launch's own, and can read a
// provider out of it in ways a classifier here cannot follow; so a record-bound launch (key or
// local, start or resume) does not start while that file has any content or cannot be read
// (Root 2026-10-02 23:20Z). An absent or empty file changes nothing, and an own-login launch is
// never refused for it.
func TestOpenCodeRefusesAnyHostManagedConfig(t *testing.T) {
	dir := t.TempDir()
	old := openCodeManagedConfigDir
	openCodeManagedConfigDir = dir
	t.Cleanup(func() { openCodeManagedConfigDir = old })
	managed := filepath.Join(dir, "opencode.jsonc")
	refused := func(err error) bool {
		var re *runErr
		return errors.As(err, &re) && re.status == http.StatusConflict && strings.Contains(re.msg, managed)
	}
	if _, err := openCodeKeyLaunch(t, anthropicInput("Anthropic")); err != nil {
		t.Fatalf("no managed file: %v", err)
	}
	if err := os.WriteFile(managed, []byte(" \n\t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openCodeKeyLaunch(t, anthropicInput("Anthropic")); err != nil {
		t.Fatalf("an empty managed file: %v", err)
	}
	for name, body := range map[string]string{
		"plain provider": `{"provider":{"anthropic":{"options":{"baseURL":"https://elsewhere.example.com/v1"}}}}`,
		"escaped key":    `{"\u0070rovider":{"anthropic":{}}}`,
		"substitution":   `{"{env:KEY_NAME}": {"anthropic": {}}, "theme": "{file:./theme.txt}"}`,
		"CR comment":     "{\"theme\": \"x\" // a comment\r\"provider\": {}}",
		"malformed":      `{"theme": `,
		"theme only":     `{"theme": "opencode"}`,
	} {
		if err := os.WriteFile(managed, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := openCodeKeyLaunch(t, anthropicInput("Anthropic")); !refused(err) {
			t.Errorf("%s: key launch = %v, want 409 naming %s", name, err, managed)
		}
	}
	if _, _, err := openCodeLocalLaunch(t, []string{"qwen3:8b"}, ""); !refused(err) {
		t.Errorf("local launch under a managed file = %v, want 409 naming %s", err, managed)
	}
	if err := os.Chmod(managed, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(managed); err == nil {
		t.Log("running as a user who can read a mode-0 file; the unreadable case is not observable here")
	} else if _, err := openCodeKeyLaunch(t, anthropicInput("Anthropic")); !refused(err) {
		t.Errorf("an unreadable managed file: key launch = %v, want 409", err)
	}
	// An own-login launch never reads the record mint, so the file does not stop it.
	m, _, tenant, _ := newRuntimeHarness(t, WithProviderDriver(NewOpenCodeDriver()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	own := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: providerDriverOpenCode, ConfigHome: t.TempDir(), UserHome: t.TempDir(),
		AuthSource: AuthSourceAccountHome})
	p := CreateRunParams{ProviderProfileRef: own.Ref}
	if err := m.resolveLaunchProfileInto(context.Background(), tenant, &p); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.mintLaunchAuthority(context.Background(), tenant, "", p); err != nil {
		t.Fatalf("own-login launch under a managed file = %v", err)
	}
}
