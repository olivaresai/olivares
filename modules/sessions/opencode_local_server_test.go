// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A LOCAL OPENAI-COMPATIBLE SERVER (vLLM, llama.cpp) IS A PROVIDER OPENCODE CAN BE HELD TO.
// Ollama had its own kind; every other local server is an openai_compatible record at a
// loopback or private-network address, with the key Olivares gave it.

// openCodeLocalServerLaunch binds an OpenCode profile to an openai_compatible record at
// baseURL whose last test listed models, and returns the launch's environment or the
// mint's error.
func openCodeLocalServerLaunch(t *testing.T, baseURL string, models []string, afterTest ...func(*fakeProbe)) (map[string]string, error) {
	t.Helper()
	probe := &fakeProbe{result: ProviderProbeResult{Models: models}}
	m, _, tenant, _ := newRuntimeHarness(t, WithProviderDriver(NewOpenCodeDriver()), WithProviderSecretVault(newFakeVault()), WithProviderProbe(probe))
	m.UseExecutionEnvironmentRef(testEnvRef)
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{Kind: ProviderKindOpenAICompatible, DisplayName: "llama.cpp", APIKey: testProviderKey, BaseURL: baseURL})
	if _, err := m.TestProviderRecord(context.Background(), tenant, rec.Ref); err != nil {
		t.Fatal(err)
	}
	for _, after := range afterTest {
		after(probe) // what the server does once the record has been tested
	}
	profile, err := m.CreateProfile(context.Background(), tenant, CreateProfileInput{Driver: providerDriverOpenCode, ConfigHome: t.TempDir(), UserHome: t.TempDir(),
		AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref})
	if err != nil {
		return nil, err
	}
	p := CreateRunParams{ProviderProfileRef: profile.Ref}
	if err := m.resolveLaunchProfileInto(context.Background(), tenant, &p); err != nil {
		t.Fatal(err)
	}
	cred, env, err := m.mintLaunchAuthority(context.Background(), tenant, "", p)
	if err != nil {
		return nil, err
	}
	return envMap(m.childSpec(p, childDecision{cred: cred, providerEnv: env}).Env), nil
}

func liveLocalServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	return srv.URL + "/v1"
}

func TestOpenCodeOnALocalOpenAICompatibleServerReachesOnlyThatServer(t *testing.T) {
	base := liveLocalServer(t)
	env, err := openCodeLocalServerLaunch(t, base, []string{"gemma-3-1b", "qwen3-0.6b"})
	if err != nil {
		t.Fatalf("launch on a local openai_compatible record = %v", err)
	}
	assertOpenCodeConfined(t, env, "olivares_local")
	cfg := openCodeInline(t, env)
	provider, _ := cfg["provider"].(map[string]any)
	local, _ := provider["olivares_local"].(map[string]any)
	options, _ := local["options"].(map[string]any)
	if local["npm"] != "@ai-sdk/openai-compatible" || options["baseURL"] != base {
		t.Fatalf("provider.olivares_local = %v, want the OpenAI-compatible adapter pinned to %s", local, base)
	}
	if listed, _ := local["models"].(map[string]any); len(listed) != 2 || listed["gemma-3-1b"] == nil {
		t.Fatalf("provider.olivares_local.models = %v, want exactly what the server's last test listed", local["models"])
	}
	if cfg["model"] != "olivares_local/gemma-3-1b" || cfg["small_model"] != cfg["model"] {
		t.Fatalf("model = %v, small_model = %v, want both the server's first model (OpenCode's default is hosted)", cfg["model"], cfg["small_model"])
	}
	// The key reaches OpenCode as an environment variable its configuration names; the
	// configuration, which other tools may log, never carries the value.
	if options["apiKey"] != "{env:OPENAI_API_KEY}" || env["OPENAI_API_KEY"] != testProviderKey {
		t.Fatalf("apiKey = %v, OPENAI_API_KEY set = %v, want the key by reference only", options["apiKey"], env["OPENAI_API_KEY"] == testProviderKey)
	}
	if strings.Contains(env[envOpenCodeConfigContent], testProviderKey) {
		t.Fatal("the inline configuration carries the key's value")
	}
}

func TestOpenCodeRefusesALocalServerThatListsNoModel(t *testing.T) {
	_, err := openCodeLocalServerLaunch(t, liveLocalServer(t), nil)
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusConflict || !strings.Contains(re.msg, "lists no model") {
		t.Fatalf("launch = %v, want 409 saying the server lists no model (OpenCode would answer on its hosted default)", err)
	}
}

// A server that was started again has a new key, and its old address can be taken by another
// process: the launch must not hand the key over to something that refuses it.
func TestOpenCodeRefusesALocalServerThatRefusesItsKey(t *testing.T) {
	_, err := openCodeLocalServerLaunch(t, liveLocalServer(t), []string{"gemma-3-1b"}, func(p *fakeProbe) {
		p.mu.Lock()
		p.err = fmt.Errorf("%w: the provider answered 401 for this credential", ErrProviderRefused)
		p.mu.Unlock()
	})
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusConflict || !strings.Contains(re.msg, "refused its key") {
		t.Fatalf("launch = %v, want 409 saying the server refused its key", err)
	}
}

// The model a record saves as its default belongs to the server, so it is qualified with the
// provider OpenCode knows it by; a model the person names is kept as chosen.
func TestOpenCodeLocalServerSavedDefaultModelIsQualified(t *testing.T) {
	probe := &fakeProbe{result: ProviderProbeResult{Models: []string{"gemma-3-1b", "qwen3-0.6b"}}}
	m, _, tenant, _ := newRuntimeHarness(t, WithProviderDriver(NewOpenCodeDriver()), WithProviderSecretVault(newFakeVault()), WithProviderProbe(probe))
	m.UseExecutionEnvironmentRef(testEnvRef)
	base := liveLocalServer(t)
	saved := "gemma-3-1b"
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{Kind: ProviderKindOpenAICompatible, DisplayName: "llama.cpp", APIKey: testProviderKey, BaseURL: base, DefaultModel: &saved})
	if _, err := m.TestProviderRecord(t.Context(), tenant, rec.Ref); err != nil {
		t.Fatal(err)
	}
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: providerDriverOpenCode,
		ConfigHome: t.TempDir(), UserHome: t.TempDir(), AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref})
	for _, tc := range []struct{ name, explicit, want string }{
		{"the record's saved default", "", "olivares_local/gemma-3-1b"},
		{"a model the person names", "qwen3-0.6b", "olivares_local/qwen3-0.6b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := CreateRunParams{ProviderProfileRef: profile.Ref, Model: tc.explicit}
			if err := m.resolveLaunchProfileInto(t.Context(), tenant, &p); err != nil {
				t.Fatal(err)
			}
			if err := m.resolveProviderDefaultModel(t.Context(), tenant, &p); err != nil {
				t.Fatal(err)
			}
			env := envMap(openCodeDriver{}.LaunchEnv(DriverLaunch{Model: p.Model, LocalModels: probe.result.Models,
				BoundProvider: BoundProvider{Kind: ProviderKindOpenAICompatible, Endpoint: base}}))
			if cfg := openCodeInline(t, env); cfg["model"] != tc.want || cfg["small_model"] != tc.want {
				t.Fatalf("model = %v, small_model = %v, want both %s", cfg["model"], cfg["small_model"], tc.want)
			}
		})
	}
}

func TestOpenCodeRefusesALocalServerThatIsDown(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	down := "http://" + l.Addr().String() + "/v1"
	_ = l.Close()
	_, err = openCodeLocalServerLaunch(t, down, []string{"gemma-3-1b"})
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusConflict || !strings.Contains(re.msg, "does not answer") {
		t.Fatalf("launch = %v, want 409 saying the server does not answer, not OpenCode's raw connection errors", err)
	}
}
