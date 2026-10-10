// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package sessions

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestProviderOllamaNeedsNoVaultAndMintsOnlyCompatibleDrivers(t *testing.T) {
	probe := &fakeProbe{result: ProviderProbeResult{Models: []string{"qwen3:8b"}}}
	m, _, tenant, _ := newRuntimeHarness(t, WithProviderProbe(probe))
	m.UseExecutionEnvironmentRef(testEnvRef)
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{Kind: "ollama", DisplayName: "Local", BaseURL: "http://127.0.0.1:11434"})
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "codex", ConfigHome: t.TempDir(), UserHome: t.TempDir()})
	authSource := AuthSourceManagedInjection
	bound, err := m.PatchProfile(context.Background(), tenant, profile.Ref, ProfilePatch{ProviderRecordRef: &rec.Ref, AuthSource: &authSource})
	if err != nil || bound.ProviderRecordRef != rec.Ref || bound.AuthSource != AuthSourceManagedInjection {
		t.Fatalf("binding: %+v %v", bound, err)
	}
	if rec.SecretRef != "" || rec.KeyHint != "" {
		t.Fatal("local endpoint stored a credential locator")
	}
	got, err := m.TestProviderRecord(context.Background(), tenant, rec.Ref)
	if err != nil || got.ProbeState != ProbeOK {
		t.Fatalf("probe: %v %+v", err, got)
	}
	if len(probe.keys) != 1 || probe.keys[0] != "" {
		t.Fatal("probe received a credential")
	}
	// The compatible drivers: Codex and OpenCode both consume the keyless local
	// endpoint (<base_url>/v1) through their own launch config (HU-R14).
	for _, driver := range []string{"codex", "opencode"} {
		authority, env, err := m.mintFromProviderRecord(context.Background(), tenant, driver, rec.Ref)
		if err != nil {
			t.Fatalf("%s: %v", driver, err)
		}

		if authority.localModelEndpoint != "http://127.0.0.1:11434/v1" || len(env) != 0 || authority.Token != "" {
			t.Fatalf("%s local endpoint authority: %+v %+v", driver, authority, env)
		}
	}
	// Every other driver is still refused: a keyless local endpoint never mints
	// for Claude or Grok.
	for _, driver := range []string{"claude", "grok"} {
		if _, _, err := m.mintFromProviderRecord(context.Background(), tenant, driver, rec.Ref); err == nil {
			t.Fatalf("incompatible driver %q accepted", driver)
		}
	}
	endpoint := "http://localhost:11435"
	updated, err := m.PatchProviderRecord(context.Background(), tenant, rec.Ref, ProviderRecordPatch{BaseURL: &endpoint})
	if err != nil || updated.ProbeState != ProbeNever || len(updated.Models) != 0 {
		t.Fatalf("stale endpoint verdict: %+v %v", updated, err)
	}
}

func TestProviderOllamaRejectsCredentialsAndUnsafeEndpoints(t *testing.T) {
	m, _, tenant, _, _ := providerHarness(t)
	for _, endpoint := range []string{"", "http://example.com", "http://user:key@localhost:11434", "http://localhost:11434?key=secret", "http://localhost:11434/#x"} {
		if _, err := m.CreateProviderRecord(context.Background(), tenant, CreateProviderRecordInput{Kind: "ollama", DisplayName: "Local", BaseURL: endpoint}); err == nil {
			t.Fatalf("accepted %q", endpoint)
		}
	}
	if _, err := m.CreateProviderRecord(context.Background(), tenant, CreateProviderRecordInput{Kind: "ollama", DisplayName: "Local", BaseURL: "http://localhost:11434", APIKey: testProviderKey}); err == nil {
		t.Fatal("accepted local credential")
	}
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{Kind: "ollama", DisplayName: "Local", BaseURL: "http://192.168.1.2:11434"})
	key := testProviderKey
	if _, err := m.PatchProviderRecord(context.Background(), tenant, rec.Ref, ProviderRecordPatch{APIKey: &key}); err == nil {
		t.Fatal("local key rotation accepted")
	}
}

func TestProviderOllamaCompilesBoundEndpointIntoCodexLaunch(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t, WithProviderDriver(NewCodexDriver()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{Kind: "ollama", DisplayName: "Local", BaseURL: "http://127.0.0.1:11435"})
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "codex", ConfigHome: t.TempDir(), UserHome: t.TempDir(), AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref})
	p := CreateRunParams{ProviderProfileRef: profile.Ref}
	if err := m.resolveLaunchProfileInto(context.Background(), tenant, &p); err != nil {
		t.Fatal(err)
	}
	cred, env, err := m.mintLaunchAuthority(context.Background(), tenant, "", p)
	if err != nil {
		t.Fatal(err)
	}
	spec := m.childSpec(p, childDecision{cred: cred, providerEnv: env})
	args := strings.Join(spec.Args, " ")
	if !strings.Contains(args, `model_provider="olivares_ollama"`) || !strings.Contains(args, `base_url="http://127.0.0.1:11435/v1"`) || !strings.Contains(args, `requires_openai_auth=false`) {
		t.Fatalf("bound endpoint absent from actual launch: %s", args)
	}
	for _, item := range spec.Env {
		if item.Name == "OPENAI_API_KEY" || item.Name == "OPENAI_BASE_URL" {
			t.Fatalf("invented credential/base-url env: %s", item.Name)
		}
	}
}

// ARCH driver table item 4 (2026-10-02): the OpenCode driver's inline config for the
// bound local endpoint (HU-R14) reached LaunchEnv but was dropped as a profile-owned
// name before the spawn, so the child never saw its local provider. The launch now
// carries it; an OpenCode launch with no local model still carries none.
func TestProviderOllamaCompilesBoundEndpointIntoOpenCodeLaunch(t *testing.T) {
	probe := &fakeProbe{result: ProviderProbeResult{Models: []string{"qwen3:8b"}}}
	m, _, tenant, _ := newRuntimeHarness(t, WithProviderDriver(NewOpenCodeDriver()), WithProviderProbe(probe))
	m.UseExecutionEnvironmentRef(testEnvRef)
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{Kind: "ollama", DisplayName: "Local", BaseURL: "http://127.0.0.1:11435"})
	// An OpenCode launch needs the endpoint's models (FH 085): the probe lists them.
	if _, err := m.TestProviderRecord(context.Background(), tenant, rec.Ref); err != nil {
		t.Fatal(err)
	}
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "opencode", ConfigHome: t.TempDir(), UserHome: t.TempDir(), AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref})
	p := CreateRunParams{ProviderProfileRef: profile.Ref}
	if err := m.resolveLaunchProfileInto(context.Background(), tenant, &p); err != nil {
		t.Fatal(err)
	}
	cred, env, err := m.mintLaunchAuthority(context.Background(), tenant, "", p)
	if err != nil {
		t.Fatal(err)
	}
	spec := m.childSpec(p, childDecision{cred: cred, providerEnv: env})
	inline := ""
	for _, item := range spec.Env {
		if item.Name == envOpenCodeConfigContent {
			inline = item.Value
		}
	}
	if !strings.Contains(inline, `"baseURL":"http://127.0.0.1:11435/v1"`) || !strings.Contains(inline, `"olivares_ollama"`) {
		t.Fatalf("OPENCODE_CONFIG_CONTENT in the actual launch = %q, want the bound local provider", inline)
	}
}

// openCodeLocalLaunch binds an OpenCode profile to a local Ollama record, probes it
// when models are given, and returns the launch's inline OpenCode config and error.
type openCodeLocal struct {
	m      *Module
	tenant model.TenantID
	record string
}

func openCodeLocalLaunch(t *testing.T, models []string, runModel string) (string, openCodeLocal, error) {
	t.Helper()
	probe := &fakeProbe{result: ProviderProbeResult{Models: models}}
	m, _, tenant, _ := newRuntimeHarness(t, WithProviderDriver(NewOpenCodeDriver()), WithProviderProbe(probe))
	m.UseExecutionEnvironmentRef(testEnvRef)
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{Kind: "ollama", DisplayName: "GPU", BaseURL: "http://192.168.8.59:11434"})
	if len(models) > 0 {
		if _, err := m.TestProviderRecord(context.Background(), tenant, rec.Ref); err != nil {
			t.Fatal(err)
		}
	}
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "opencode", ConfigHome: t.TempDir(), UserHome: t.TempDir(), AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref})
	p := CreateRunParams{ProviderProfileRef: profile.Ref, Model: runModel}
	at := openCodeLocal{m: m, tenant: tenant, record: rec.Ref}
	if err := m.resolveLaunchProfileInto(context.Background(), tenant, &p); err != nil {
		t.Fatal(err)
	}
	cred, env, err := m.mintLaunchAuthority(context.Background(), tenant, "", p)
	if err != nil {
		return "", at, err
	}
	spec := m.childSpec(p, childDecision{cred: cred, providerEnv: env})
	for _, item := range spec.Env {
		if item.Name == envOpenCodeConfigContent {
			return item.Value, at, nil
		}
	}
	t.Fatal("no OPENCODE_CONFIG_CONTENT in the launch")
	return "", at, nil
}

// FH 085 (fix C proof on a GPU Ollama, OpenCode 1.18.34): with the endpoint's models
// unlisted, a session with no model ran on OpenCode's own hosted default
// (opencode/big-pickle, OpenCode Zen) and the prompt left the server. A session
// bound to the local endpoint now defaults to the local model, and no other
// provider is enabled.
func TestOpenCodeLocalLaunchWithNoModelGivenUsesTheLocalDefaultNeverAHostedOne(t *testing.T) {
	inline, _, err := openCodeLocalLaunch(t, []string{"qwen3:8b", "llama3.2:1b"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Model            string   `json:"model"`
		EnabledProviders []string `json:"enabled_providers"`
		Provider         map[string]struct {
			Options map[string]string `json:"options"`
			Models  map[string]any    `json:"models"`
		} `json:"provider"`
	}
	if err := json.Unmarshal([]byte(inline), &cfg); err != nil {
		t.Fatalf("inline config %q: %v", inline, err)
	}
	if cfg.Model != "olivares_ollama/qwen3:8b" {
		t.Fatalf("default model = %q, want the endpoint's first model", cfg.Model)
	}
	if !reflect.DeepEqual(cfg.EnabledProviders, []string{"olivares_ollama"}) || len(cfg.Provider) != 1 {
		t.Fatalf("providers = %v enabled %v, want only the local one", cfg.Provider, cfg.EnabledProviders)
	}
	local := cfg.Provider["olivares_ollama"]
	listed := make([]string, 0, len(local.Models))
	for name := range local.Models {
		listed = append(listed, name)
	}
	sort.Strings(listed)
	if !reflect.DeepEqual(listed, []string{"llama3.2:1b", "qwen3:8b"}) || local.Options["baseURL"] != "http://192.168.8.59:11434/v1" {
		t.Fatalf("local provider = %+v", local)
	}
	if strings.Contains(inline, "opencode/") {
		t.Fatalf("the launch names a hosted OpenCode model: %s", inline)
	}
}

// With no model listed, OpenCode would fall back to its hosted default: the launch is
// refused before spawn, by name, and Codex on the same record is not affected.
func TestOpenCodeLocalLaunchIsRefusedWhenTheEndpointListsNoModel(t *testing.T) {
	_, at, err := openCodeLocalLaunch(t, nil, "")
	if statusOf(err) != http.StatusConflict || !strings.Contains(err.Error(), `"GPU" lists no model`) {
		t.Fatalf("launch on an endpoint with no listed model: %v", err)
	}
	if _, _, err := at.m.mintFromProviderRecord(context.Background(), at.tenant, "codex", at.record); err != nil {
		t.Fatalf("codex on the same record: %v", err)
	}
}
