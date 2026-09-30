// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package sessions

import (
	"context"
	"strings"
	"testing"
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
	for _, driver := range []string{"codex"} {
		authority, env, err := m.mintFromProviderRecord(context.Background(), tenant, driver, rec.Ref)
		if err != nil {
			t.Fatal(err)
		}

		if authority.localModelEndpoint != "http://127.0.0.1:11434/v1" || len(env) != 0 || authority.Token != "" {
			t.Fatalf("local endpoint authority: %+v %+v", authority, env)
		}
	}
	if _, _, err := m.mintFromProviderRecord(context.Background(), tenant, "opencode", rec.Ref); err == nil {
		t.Fatal("incompatible driver accepted")
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
	spec := m.buildLaunchSpec(p, cred, WorkSessionCredential{}, CommunicationSessionCredential{}, "", nil, nil, env)
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
