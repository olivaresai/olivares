// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

type launchSpeedRunner struct {
	*fakeRunner
	catalog *catalogToolPeer
}

func (r launchSpeedRunner) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	if slices.Contains(spec.Args, "--no-session-persistence") || (len(spec.Args) > 0 && spec.Args[0] == "auth") {
		return r.catalog.Launch(ctx, spec)
	}
	return r.fakeRunner.Launch(ctx, spec)
}

func TestLaunchSpeedHTTPClaudeFastMode(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "on", false: "off"}[enabled], func(t *testing.T) {
			fr := &fakeRunner{initSID: "speed-session"}
			runner := launchSpeedRunner{fakeRunner: fr, catalog: &catalogToolPeer{t: t, signed: true,
				claudeModels: []map[string]any{{"value": "default", "supportsFastMode": true}},
			}}
			m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred()))
			h := newHarness(t, m)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "speed")
			m.UseExecutionEnvironmentRef(testEnvRef)
			prof := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
			r := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{
				"provider_profile_ref": prof.Ref,
				"fast_mode":            enabled,
			}, tenantHdr(tenant))
			if r.code != http.StatusCreated || r.body["fast_mode"] != enabled {
				t.Fatalf("launch = %d %s; want saved fast_mode=%t", r.code, r.raw, enabled)
			}
			if enabled && r.body["model_ref"] != "claude-opus-5-5" {
				t.Fatalf("the catalog default checked for Fast mode was not pinned to its exact id: %s", r.raw)
			}
			ref := r.body["run_ref"].(string)
			got := h.do("GET", "/v1/m/sessions/runs/"+ref, admin, tenantHdr(tenant))
			if got.code != http.StatusOK || got.body["fast_mode"] != enabled {
				t.Fatalf("read = %d %s; want saved fast_mode=%t", got.code, got.raw, enabled)
			}
			fr.mu.Lock()
			spec := fr.specs[0]
			fr.mu.Unlock()
			settings := launchSpeedSettings(t, spec.Args)
			if settings["fastMode"] != enabled {
				t.Fatalf("child settings = %v; want fastMode=%t", settings, enabled)
			}
			waitFor(t, "the profile's conversation is bound before resume", func() bool {
				dto, err := m.getRun(context.Background(), tenant, ref)
				return err == nil && dto.ClaudeSessionID != ""
			})
			if stopped := h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/stop", admin, map[string]any{}, tenantHdr(tenant)); stopped.code != http.StatusOK {
				t.Fatalf("stop = %d %s", stopped.code, stopped.raw)
			}
			resumed := h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/resume", admin, map[string]any{}, tenantHdr(tenant))
			if resumed.code != http.StatusOK || resumed.body["fast_mode"] != enabled {
				t.Fatalf("resume = %d %s; want fast_mode=%t", resumed.code, resumed.raw, enabled)
			}
			if settings := launchSpeedSettings(t, fr.lastSpec().Args); settings["fastMode"] != enabled {
				t.Fatalf("resume discarded fastMode: %v", settings)
			}
		})
	}
}

func TestLaunchSpeedClaudeVendorEffortHasFinal400Contract(t *testing.T) {
	fr := &fakeRunner{}
	runner := launchSpeedRunner{fakeRunner: fr, catalog: &catalogToolPeer{t: t, signed: true,
		claudeModels: []map[string]any{{"value": "default", "supportedEffortLevels": []string{"vendor-max"}}},
	}}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred()))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "effort-contract")
	m.UseExecutionEnvironmentRef(testEnvRef)
	prof := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
	catalog := h.do("GET", "/v1/m/sessions/provider-profiles/"+prof.Ref+"/models", admin, tenantHdr(tenant))
	if catalog.code != http.StatusOK || !strings.Contains(catalog.raw, "vendor-max") {
		t.Fatalf("vendor effort was not discovered: %d %s", catalog.code, catalog.raw)
	}
	r := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{"provider_profile_ref": prof.Ref, "effort": "vendor-max"}, tenantHdr(tenant))
	if r.code != http.StatusBadRequest || !strings.Contains(r.raw, "invalid effort") {
		t.Fatalf("vendor effort launch = %d %s; want final 400 contract", r.code, r.raw)
	}
	if launchCount(fr) != 0 {
		t.Fatal("refused effort spawned a runtime child")
	}
}

func launchSpeedSettings(t *testing.T, args []string) map[string]any {
	t.Helper()
	var settings map[string]any
	count := 0
	for i := 0; i+1 < len(args); i++ {
		if args[i] == claudeSettingsFlag {
			count++
			if err := json.Unmarshal([]byte(args[i+1]), &settings); err != nil {
				t.Fatalf("settings JSON: %v", err)
			}
		}
	}
	if count != 1 {
		t.Fatalf("want one settings argument, got %d: %v", count, args)
	}
	return settings
}

func TestLaunchSpeedClaudeKeepsBoundSettings(t *testing.T) {
	var p CreateRunParams
	if err := json.Unmarshal([]byte(`{"fast_mode":false}`), &p); err != nil {
		t.Fatal(err)
	}
	p.ProviderHome = &ProviderHomeSnapshot{Driver: providerDriverClaude}
	m := New()
	spec := m.childSpec(p, childDecision{cred: Credential{bound: BoundProvider{Kind: "anthropic"}}})
	settings := launchSpeedSettings(t, spec.Args)
	if settings["fastMode"] != false {
		t.Fatalf("fast mode off was discarded: %v", settings)
	}
	env, _ := settings["env"].(map[string]any)
	if env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] != "1" || env["CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL"] != "1" {
		t.Fatalf("provider settings lost: %v", settings)
	}
	dataDir := t.TempDir()
	spec.Env = append(spec.Env, EnvVar{Name: envHookPEPURL, Value: "http://127.0.0.1:8447/"}, EnvVar{Name: envHookPEPToken, Value: "fixture-only-token"})
	if err := ConfigureClaudeHookPEP(&spec, dataDir, "speed-hook", "/opt/olivares"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, "run", "speed-hook", "pep-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	settings = nil
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	env, _ = settings["env"].(map[string]any)
	if settings["fastMode"] != false || settings["disableAllHooks"] != false || env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] != "1" || env["CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL"] != "1" {
		t.Fatalf("PEP settings lost speed or endpoint protection: %v", settings)
	}
	hooks, _ := settings["hooks"].(map[string]any)
	for _, event := range []string{"PreToolUse", "PostToolUse", "PostToolUseFailure"} {
		if hooks[event] == nil {
			t.Fatalf("PEP lost %s", event)
		}
	}
	if strings.Contains(string(raw), "fixture-only-token") {
		t.Fatal("PEP settings leaked the credential")
	}
}

func TestLaunchSpeedClaudeRefusesUnknownOrUnavailableCapability(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		models      []map[string]any
		signed      bool
		status      int
	}{
		{"missing capability", "default", []map[string]any{{"value": "default"}}, true, http.StatusBadRequest},
		{"unsupported model", "default", []map[string]any{{"value": "default", "supportsFastMode": false}}, true, http.StatusBadRequest},
		{"unknown model", "unknown-model", []map[string]any{{"value": "default", "supportsFastMode": true}}, true, http.StatusBadRequest},
		{"failed catalog", "default", []map[string]any{{"value": "default", "supportsFastMode": true}}, false, http.StatusServiceUnavailable},
		{"empty catalog", "default", []map[string]any{}, true, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRunner{}
			runner := launchSpeedRunner{fakeRunner: fr, catalog: &catalogToolPeer{t: t, signed: tc.signed, claudeModels: tc.models}}
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner))
			m.UseExecutionEnvironmentRef(testEnvRef)
			prof := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
			enabled := true
			_, err := m.createRun(context.Background(), tenant, CreateRunParams{ProviderProfileRef: prof.Ref, Model: tc.model, FastMode: &enabled})
			var refusal *runErr
			if !errors.As(err, &refusal) || refusal.status != tc.status {
				t.Fatalf("refusal=%v; want %d", err, tc.status)
			}
			if launchCount(fr) != 0 {
				t.Fatal("an unsupported capability spawned a session")
			}
		})
	}
}

func TestLaunchSpeedClaudeBoundProviderRefusesBeforeDiscovery(t *testing.T) {
	fr := &fakeRunner{}
	m, _, tenant, _, _ := providerHarness(t, WithRunner(fr))
	record := mustCreateRecord(t, m, tenant, anthropicInput("Bound speed provider"))
	prof := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir(), AuthSource: AuthSourceManagedInjection, ProviderRecordRef: record.Ref})
	enabled := true
	_, err := m.createRun(context.Background(), tenant, CreateRunParams{ProviderProfileRef: prof.Ref, FastMode: &enabled})
	var refusal *runErr
	if !errors.As(err, &refusal) || refusal.status != http.StatusBadRequest || !strings.Contains(err.Error(), "Fast mode cannot be enabled") {
		t.Fatalf("bound provider refusal = %v", err)
	}
	if launchCount(fr) != 0 {
		t.Fatal("bound provider launched a discovery or runtime child")
	}
}

func TestLaunchSpeedRejectsMalformedTierAndWrongDriver(t *testing.T) {
	m := New()
	for _, tier := range []string{strings.Repeat("x", 201), "priority\n", " priority", "priority\u2003"} {
		p := CreateRunParams{ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverCodex}, ServiceTier: tier}
		if err := m.validateLaunchSpeed(context.Background(), model.TenantID(""), &p); err == nil {
			t.Fatalf("accepted malformed tier %q", tier)
		}
	}
	disabled := false
	p := CreateRunParams{ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverCodex}, FastMode: &disabled}
	if err := m.validateLaunchSpeed(context.Background(), model.TenantID(""), &p); err == nil {
		t.Fatal("Codex discarded Fast mode off")
	}
}

func TestLaunchSpeedDefaultsPreserveUnprofiledTerms(t *testing.T) {
	p := CreateRunParams{}
	if err := New().validateLaunchSpeed(context.Background(), model.TenantID(""), &p); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "fast_mode") || strings.Contains(string(raw), "service_tier") {
		t.Fatal("omitted speed choices changed the existing launch digest input")
	}
}

func TestLaunchSpeedCodexThreadStartAndResume(t *testing.T) {
	for _, resume := range []string{"", "thread-speed"} {
		t.Run(map[bool]string{true: "resume", false: "start"}[resume != ""], func(t *testing.T) {
			peer := newCodexPeer(t, func(cfg *DriverSessionConfig) {
				if err := json.Unmarshal([]byte(`{"ServiceTier":"future-priority"}`), cfg); err != nil {
					t.Fatal(err)
				}
				cfg.ResumeConversationID = resume
			})
			defer peer.session.Close(nil)
			done := make(chan error, 1)
			go func() {
				_, err := peer.session.Handshake(context.Background())
				done <- err
			}()
			id, _, _ := peer.nextRequest()
			peer.reply(id, map[string]any{"userAgent": "fixture"})
			id, _, _ = peer.nextRequest()
			peer.reply(id, map[string]any{"account": map[string]any{"type": "apiKey"}, "requiresOpenaiAuth": true})
			id, method, params := peer.nextRequest()
			want := codexMethodThreadStart
			if resume != "" {
				want = codexMethodThreadResume
			}
			if method != want || params["serviceTier"] != "future-priority" {
				t.Errorf("%s params = %v; want %s with serviceTier=future-priority", method, params, want)
			}
			peer.reply(id, map[string]any{"thread": map[string]any{"id": "thread-speed"}, "model": "tool-model", "modelProvider": "openai", "serviceTier": "future-priority"})
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLaunchSpeedCodexRefusesTierFallback(t *testing.T) {
	peer := newCodexPeer(t, func(cfg *DriverSessionConfig) {
		if err := json.Unmarshal([]byte(`{"ServiceTier":"priority"}`), cfg); err != nil {
			t.Fatal(err)
		}
	})
	defer peer.session.Close(nil)
	if _, err := peer.answerHandshake("tier-refused", map[string]any{"type": "apiKey"}, true); err == nil {
		t.Fatal("native serviceTier fallback must be refused before any prompt")
	}
}

func TestLaunchSpeedUnsupportedChoiceIsRefusedBeforeSpawn(t *testing.T) {
	fr := &fakeRunner{initSID: "unused"}
	m, _, tenant, prof, _ := profiledHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	var p CreateRunParams
	if err := json.Unmarshal([]byte(`{"service_tier":"priority"}`), &p); err != nil {
		t.Fatal(err)
	}
	p.ProviderProfileRef, p.Actor, p.ActorKind = prof.Ref, "user:u1", model.ActorUser
	if _, err := m.createRun(context.Background(), tenant, p); err == nil {
		t.Fatal("Claude must refuse a service tier it cannot carry")
	}
	if launchCount(fr) != 0 {
		t.Fatal("unsupported choice spawned a child")
	}
}

func TestLaunchSpeedCodexRuntimePersistsTierForResume(t *testing.T) {
	m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome)
	record := setCodexFixture(t, prof, codexFixture{ThreadID: "speed-tier", Account: "apikey"})
	var p CreateRunParams
	if err := json.Unmarshal([]byte(`{"service_tier":"future-priority"}`), &p); err != nil {
		t.Fatal(err)
	}
	p.ProviderProfileRef, p.Actor, p.ActorKind = prof.Ref, "user:u1", model.ActorUser
	ctx := context.Background()
	dto, err := m.createRun(ctx, tenant, p)
	if err != nil {
		t.Fatal(err)
	}
	if peer := readFixtureRecord(t, record); len(peer.ServiceTiers) != 1 || peer.ServiceTiers[0] != "future-priority" {
		t.Fatalf("native thread/start discarded service tier: %v", peer.ServiceTiers)
	}
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, p.Actor, p.ActorKind); err != nil {
		t.Fatal(err)
	}
	if _, err := m.resumeRun(ctx, tenant, dto.RunRef, p.Actor, p.ActorKind, ""); err != nil {
		t.Fatal(err)
	}
	if peer := readFixtureRecord(t, record); len(peer.ServiceTiers) != 1 || peer.ServiceTiers[0] != "future-priority" {
		t.Fatalf("native thread/resume discarded saved service tier: %v", peer.ServiceTiers)
	}
}

func TestLaunchSpeedHTTPCodexTier(t *testing.T) {
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(NewProcRunner()),
		WithProviderDriver(NewCodexDriver()), WithDriverProgram(providerDriverCodex, os.Args[0]))
	m.UseExecutionEnvironmentRef(testEnvRef)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "speed-http-codex")
	prof := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: providerDriverCodex,
		ConfigHome: t.TempDir(), UserHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
	record := setCodexFixture(t, prof, codexFixture{ThreadID: "tier-http", Account: "apikey"})
	r := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{
		"provider_profile_ref": prof.Ref, "service_tier": "future-priority",
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated || r.body["service_tier"] != "future-priority" {
		t.Fatalf("launch = %d %s; want saved service_tier=future-priority", r.code, r.raw)
	}
	if peer := readFixtureRecord(t, record); len(peer.ServiceTiers) != 1 || peer.ServiceTiers[0] != "future-priority" {
		t.Fatalf("HTTP launch did not carry native serviceTier: %v", peer.ServiceTiers)
	}
}
