// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProfileModelCatalogHTTP(t *testing.T) {
	for _, driver := range []string{"claude", "codex"} {
		t.Run(driver, func(t *testing.T) {
			peer := &catalogToolPeer{t: t, signed: true,
				claudeModels: []map[string]any{{"value": "future-model", "displayName": "Future model", "supportedEffortLevels": []string{"medium", "vendor-max"}}},
				codexModels:  []map[string]any{{"model": "future-model", "displayName": "Future model", "isDefault": true, "availabilityNux": map[string]any{"message": "Try it"}, "upgrade": nil, "supportedReasoningEfforts": []map[string]string{{"reasoningEffort": "medium"}, {"reasoningEffort": "vendor-max"}}, "defaultReasoningEffort": "medium"}},
			}
			m := New(WithRunner(peer), WithProviderDriver(NewCodexDriver()))
			m.UseExecutionEnvironmentRef(testEnvRef)
			h := newHarness(t, m)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "catalog")
			profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: driver, UserHome: t.TempDir(), ConfigHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
			path := "/v1/m/sessions/provider-profiles/" + profile.Ref + "/models"
			r := h.do("GET", path, admin, tenantHdr(tenant))
			if r.code != http.StatusOK {
				t.Fatalf("catalog = %d %s", r.code, r.raw)
			}
			wantDefault := "future-model"
			if driver == "claude" {
				wantDefault = "" // Claude Code marks no default but its "default" alias
			}
			if r.body["profile_ref"] != profile.Ref || r.body["state"] != "fresh" || r.body["default_model"] != wantDefault || r.body["free_text"] != true {
				t.Fatalf("catalog contract: %s", r.raw)
			}
			rows, _ := r.body["models"].([]any)
			if len(rows) != 1 {
				t.Fatalf("models: %s", r.raw)
			}
			md := rows[0].(map[string]any)
			efforts, _ := md["effort_levels"].([]any)
			if md["id"] != "future-model" || len(efforts) != 2 || efforts[1] != "vendor-max" {
				t.Fatalf("metadata lost: %s", r.raw)
			}
			if driver == "codex" && (md["is_new"] != true || md["is_default"] != true || md["is_legacy"] != false || md["default_effort"] != "medium") {
				t.Fatalf("codex markers lost: %s", r.raw)
			}
			if driver == "claude" && (md["is_new"] != nil || md["is_legacy"] != nil || md["is_default"] != nil || md["default_effort"] != nil) {
				t.Fatalf("claude marker invented: %s", r.raw)
			}
			before := len(peer.specs)
			peer.modelIDs = []string{"upstream-added"}
			peer.claudeModels = nil
			peer.codexModels = nil
			cached := h.do("GET", path, admin, tenantHdr(tenant))
			if cached.code != 200 || len(peer.specs) != before || !strings.Contains(cached.raw, "future-model") {
				t.Fatalf("cache: %s", cached.raw)
			}
			refreshed := h.do("GET", path+"?refresh=true", admin, tenantHdr(tenant))
			if refreshed.code != 200 || len(peer.specs) <= before || !strings.Contains(refreshed.raw, "upstream-added") {
				t.Fatalf("upstream update: %s", refreshed.raw)
			}
			for _, q := range []string{"?refresh=bad", "?refresh=true&refresh=false", "?unexpected=true"} {
				if bad := h.do("GET", path+q, admin, tenantHdr(tenant)); bad.code != 400 {
					t.Fatalf("invalid query = %d", bad.code)
				}
			}
			if anonymous := h.do("GET", path, "", tenantHdr(tenant)); anonymous.code != 401 {
				t.Fatalf("anonymous = %d", anonymous.code)
			}
			other := h.createOrg(admin, "other")
			outsider := h.viewerToken(admin, other, "other@example.com")
			before = len(peer.specs)
			if denied := h.do("GET", path, outsider, tenantHdr(tenant)); denied.code != 403 {
				t.Fatalf("outsider = %d", denied.code)
			}
			if absent := h.do("GET", path, admin, tenantHdr(other)); absent.code != 404 {
				t.Fatalf("other tenant = %d", absent.code)
			}
			if len(peer.specs) != before {
				t.Fatal("unauthorized request launched tool")
			}
			legacy := h.do("GET", "/v1/m/sessions/provider-profiles/"+profile.Ref, admin, tenantHdr(tenant))
			if legacy.code != 200 || legacy.body["profile_ref"] != profile.Ref || legacy.body["auth_source"] != AuthSourceAccountHome {
				t.Fatalf("published profile changed: %s", legacy.raw)
			}
		})
	}
}

func TestProfileModelCatalogTTLAndRevocation(t *testing.T) {
	peer := &catalogToolPeer{t: t, signed: true, modelIDs: []string{"first-model"}}
	m, _, tenant, clk := newRuntimeHarness(t, WithRunner(peer), WithProviderDriver(NewCodexDriver()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "codex", UserHome: t.TempDir(), ConfigHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
	first, err := m.ProfileModelCatalog(context.Background(), tenant, profile.Ref, false)
	if err != nil || len(first.Models) != 1 {
		t.Fatalf("catalog: %+v %v", first, err)
	}
	peer.modelIDs = []string{"second-model"}
	clk.set(baseTime.Add(3 * time.Minute))
	next, err := m.ProfileModelCatalog(context.Background(), tenant, profile.Ref, false)
	if err != nil || next.Models[0].ID != "second-model" {
		t.Fatalf("expired catalog: %+v %v", next, err)
	}
	state := ProfileDisabled
	if _, err = m.PatchProfile(context.Background(), tenant, profile.Ref, ProfilePatch{State: &state}); err != nil {
		t.Fatal(err)
	}
	if _, err = m.ProfileModelCatalog(context.Background(), tenant, profile.Ref, false); err != ErrProfileDisabled {
		t.Fatalf("disabled cache used: %v", err)
	}
}

func TestProfileModelCatalogAPIContract(t *testing.T) {
	doc := api.ModuleOpenAPIDocument([]api.Module{New()})
	paths := doc["paths"].(map[string]any)
	op := paths["/v1/m/sessions/provider-profiles/{ref}/models"].(map[string]any)["get"].(map[string]any)
	if op["x-required-permission"] != string(permProfileRead) {
		t.Fatal("catalog permission missing from OpenAPI")
	}
	responses := op["responses"].(map[string]any)
	content := responses["200"].(map[string]any)["content"].(map[string]any)
	schema := content["application/json"].(map[string]any)["schema"].(map[string]any)
	properties := schema["properties"].(map[string]any)
	fields := properties["models"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	for _, name := range []string{"id", "display_name", "effort_levels", "default_effort", "is_default", "is_new", "is_legacy", "supports_fast_mode", "service_tiers"} {
		if fields[name] == nil {
			t.Errorf("OpenAPI omits %s", name)
		}
	}
	params, _ := op["parameters"].([]any)
	found := false
	for _, raw := range params {
		p := raw.(map[string]any)
		if p["name"] == "refresh" {
			found = p["schema"].(map[string]any)["type"] == "boolean"
		}
	}
	if !found {
		t.Fatal("refresh query is not documented")
	}
}

func TestProfileModelCatalogBoundProviderAndFallback(t *testing.T) {
	probe := &fakeProbe{result: ProviderProbeResult{Models: []string{"provider-current"}}}
	m, _, tenant, _ := newRuntimeHarness(t, WithProviderSecretVault(newFakeVault()), WithProviderProbe(probe), WithProviderDriver(NewCodexDriver()), WithProviderDriver(NewGeminiDriver()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{Kind: ProviderKindOpenAI, DisplayName: "Catalog provider", APIKey: testProviderKey})
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "codex", UserHome: t.TempDir(), ConfigHome: t.TempDir(), AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref})
	out, err := m.ProfileModelCatalog(t.Context(), tenant, profile.Ref, false)
	if err != nil || out.State != "fresh" || len(out.Models) != 1 || out.Models[0].ID != "provider-current" {
		t.Fatalf("bound provider: %+v %v", out, err)
	}
	updated, err := m.GetProviderRecord(t.Context(), tenant, rec.Ref)
	if err != nil || updated.Version != rec.Version || updated.ProbedAt != "" {
		t.Fatal("catalog changed provider test receipt")
	}
	probe.result.Models = []string{"provider-added"}
	out, err = m.ProfileModelCatalog(t.Context(), tenant, profile.Ref, true)
	if err != nil || out.Models[0].ID != "provider-added" {
		t.Fatalf("provider refresh: %+v %v", out, err)
	}
	if _, err = m.RevokeProviderRecord(t.Context(), testActor(), tenant, rec.Ref); err != nil {
		t.Fatal(err)
	}
	if _, err = m.ProfileModelCatalog(t.Context(), tenant, profile.Ref, false); err != ErrProviderRecordRevoked {
		t.Fatalf("revoked cached provider used: %v", err)
	}
	// A tool with no safe adapter remains usable with native default/free text.
	fallback := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: providerDriverGemini, UserHome: t.TempDir(), ConfigHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
	out, err = m.ProfileModelCatalog(t.Context(), tenant, fallback.Ref, false)
	if err != nil || out.State != "unsupported" || !out.FreeText || out.DefaultModel != "" || len(out.Models) != 0 || out.Message == "" {
		t.Fatalf("fallback: %+v %v", out, err)
	}
}

func TestProfileModelCatalogOpenCodeFallbackDoesNotLaunchPlugins(t *testing.T) {
	peer := &catalogToolPeer{t: t}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(peer), WithProviderDriver(NewOpenCodeDriver()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	home, config := t.TempDir(), t.TempDir()
	marker := filepath.Join(config, "plugin-ran")
	plugin := filepath.Join(config, "plugin.js")
	if err := os.WriteFile(plugin, []byte("export const CatalogPlugin = async () => { await Bun.write("+strconv.Quote(marker)+", 'ran'); return {}; };"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "opencode.json"), []byte(`{"plugin":["file://`+plugin+`"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "opencode", UserHome: home, ConfigHome: config, AuthSource: AuthSourceAccountHome})
	out, err := m.ProfileModelCatalog(t.Context(), tenant, profile.Ref, true)
	if err != nil || out.State != "unsupported" || !out.FreeText || out.DefaultModel != "" || len(out.Models) != 0 {
		t.Fatalf("OpenCode fallback: %+v %v", out, err)
	}
	if len(peer.specs) != 0 {
		t.Fatal("catalog launched plugin-capable OpenCode command")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("catalog executed a configured plugin")
	}
}

func TestProfileModelCatalogInvalidMetadataAndLoginChange(t *testing.T) {
	peer := &catalogToolPeer{t: t, signed: true, claudeModels: []map[string]any{{"value": "model", "supportedEffortLevels": []string{"contains space"}}}}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(peer))
	m.UseExecutionEnvironmentRef(testEnvRef)
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", UserHome: t.TempDir(), ConfigHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
	out, err := m.ProfileModelCatalog(t.Context(), tenant, profile.Ref, false)
	if err != nil || out.State != "failed" || len(out.Models) != 0 {
		t.Fatalf("invalid effort published: %+v %v", out, err)
	}
	peer.claudeModels = []map[string]any{{"value": "default", "supportedEffortLevels": []string{"future-effort"}}}
	out, err = m.ProfileModelCatalog(t.Context(), tenant, profile.Ref, true)
	if err != nil || out.State != "fresh" {
		t.Fatalf("refresh: %+v %v", out, err)
	}
	// Returned metadata cannot mutate the cached result.
	*out.Models[0].IsDefault = false
	out.Models[0].EffortLevels[0] = "mutated"
	cached, err := m.ProfileModelCatalog(t.Context(), tenant, profile.Ref, false)
	if err != nil || !*cached.Models[0].IsDefault || cached.Models[0].EffortLevels[0] != "future-effort" {
		t.Fatal("caller changed cache")
	}
	peer.signed = false
	if err = os.WriteFile(filepath.Join(profile.ConfigHome, ".credentials.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = m.ProfileModelCatalog(t.Context(), tenant, profile.Ref, false)
	if err != nil || out.State != "failed" || len(out.Models) != 0 || out.DefaultModel != "" {
		t.Fatalf("changed login kept old catalog: %+v %v", out, err)
	}
}

func TestProfileModelCatalogReadAudit(t *testing.T) {
	peer := &catalogToolPeer{t: t, signed: true}
	m := New(WithRunner(peer))
	m.UseExecutionEnvironmentRef(testEnvRef)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "catalog-audit")
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", UserHome: t.TempDir(), ConfigHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
	r := h.do("GET", "/v1/m/sessions/provider-profiles/"+profile.Ref+"/models", admin, tenantHdr(tenant))
	if r.code != 200 {
		t.Fatal(r.raw)
	}
	count := 0
	if err := h.st.View(t.Context(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(t.Context(), 0, func(entry model.AuditEvent) error {
			if entry.Action == "sessions.provider_profile.models.read" {
				count++
				if entry.TargetKind != providerProfileKind || entry.TargetID != profile.ID {
					t.Errorf("catalog read audit target = %v/%v, want %v/%v", entry.TargetKind, entry.TargetID, providerProfileKind, profile.ID)
				}
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("catalog read audits=%d", count)
	}
}

type pausedCatalogRunner struct {
	peer    *catalogToolPeer
	entered chan struct{}
	release chan struct{}
}

func (p *pausedCatalogRunner) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	close(p.entered)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.release:
	}
	return p.peer.Launch(ctx, spec)
}

func TestProfileModelCatalogCoalescesAndDiscardsChangedProfile(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(strconv.FormatBool(change), func(t *testing.T) {
			peer := &catalogToolPeer{t: t, signed: true}
			runner := &pausedCatalogRunner{peer: peer, entered: make(chan struct{}), release: make(chan struct{})}
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithProviderDriver(NewCodexDriver()))
			m.UseExecutionEnvironmentRef(testEnvRef)
			profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "codex", UserHome: t.TempDir(), ConfigHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
			type result struct {
				catalog ProfileModelCatalog
				err     error
			}
			first := make(chan result, 1)
			go func() {
				out, err := m.ProfileModelCatalog(t.Context(), tenant, profile.Ref, false)
				first <- result{out, err}
			}()
			<-runner.entered
			if change {
				state := ProfileDisabled
				if _, err := m.PatchProfile(t.Context(), tenant, profile.Ref, ProfilePatch{State: &state}); err != nil {
					t.Fatal(err)
				}
				close(runner.release)
				got := <-first
				if got.err != ErrProfileDisabled {
					t.Fatalf("profile changed while tool replied: %+v", got)
				}
				return
			}
			// An already running discovery must obey the waiting request's cancellation.
			canceled, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := m.ProfileModelCatalog(canceled, tenant, profile.Ref, true); err == nil {
				t.Fatal("canceled read succeeded")
			}
			second := make(chan result, 1)
			go func() {
				out, err := m.ProfileModelCatalog(t.Context(), tenant, profile.Ref, false)
				second <- result{out, err}
			}()
			close(runner.release)
			got := <-first
			other := <-second
			if other.err != nil || other.catalog.State != "fresh" {
				t.Fatalf("concurrent read: %+v", other)
			}
			if got.err != nil || got.catalog.State != "fresh" {
				t.Fatalf("first read: %+v", got)
			}
			cached, err := m.ProfileModelCatalog(t.Context(), tenant, profile.Ref, false)
			if err != nil || cached.State != "fresh" || len(peer.specs) != 1 {
				t.Fatalf("cache did not coalesce: %+v %v launches=%d", cached, err, len(peer.specs))
			}
		})
	}
}

func TestProfileModelCatalogClaudeNativeDefault(t *testing.T) {
	peer := &catalogToolPeer{t: t, signed: true, claudeModels: []map[string]any{{"value": "default", "displayName": "Default", "supportedEffortLevels": []string{"medium", "future-max"}, "supportsFastMode": true}}}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(peer))
	m.UseExecutionEnvironmentRef(testEnvRef)
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", UserHome: t.TempDir(), ConfigHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
	out, err := m.ProfileModelCatalog(t.Context(), tenant, profile.Ref, false)
	if err != nil || out.DefaultModel != "default" || len(out.Models) != 1 || out.Models[0].IsDefault == nil || !*out.Models[0].IsDefault || out.Models[0].SupportsFastMode == nil || !*out.Models[0].SupportsFastMode {
		t.Fatalf("native default/capability lost: %+v %v", out, err)
	}
}

type canceledCatalogRunner struct {
	peer    *catalogToolPeer
	pause   bool
	entered chan struct{}
}

func (r *canceledCatalogRunner) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	if r.pause {
		close(r.entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return r.peer.Launch(ctx, spec)
}
func TestProfileModelCatalogCanceledRefreshDoesNotReuseOldCache(t *testing.T) {
	peer := &catalogToolPeer{t: t, signed: true, modelIDs: []string{"before-cancel"}}
	runner := &canceledCatalogRunner{peer: peer, entered: make(chan struct{})}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithProviderDriver(NewCodexDriver()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "codex", UserHome: t.TempDir(), ConfigHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
	if _, err := m.ProfileModelCatalog(t.Context(), tenant, profile.Ref, false); err != nil {
		t.Fatal(err)
	}
	peer.modelIDs = []string{"after-cancel"}
	runner.pause = true
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, err := m.ProfileModelCatalog(ctx, tenant, profile.Ref, true); finished <- err }()
	<-runner.entered
	cancel()
	if err := <-finished; err != context.Canceled {
		t.Fatalf("canceled refresh: %v", err)
	}
	runner.pause = false
	// A waiter resumes as an ordinary read after the canceled flight. Its old
	// catalog must not satisfy the upstream refresh that flight was to perform.
	out, err := m.ProfileModelCatalog(t.Context(), tenant, profile.Ref, false)
	if err != nil || out.State != "fresh" || len(out.Models) != 1 || out.Models[0].ID != "after-cancel" {
		t.Fatalf("canceled refresh reused old cache: %+v %v", out, err)
	}
}

// An empty list is not "this account has no models": it falls back to the tool
// default and free text, and says so.
func TestProfileModelCatalogEmptyListIsNotFresh(t *testing.T) {
	probe := &fakeProbe{result: ProviderProbeResult{Models: []string{}}}
	m, _, tenant, _ := newRuntimeHarness(t, WithProviderSecretVault(newFakeVault()), WithProviderProbe(probe), WithProviderDriver(NewCodexDriver()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{Kind: ProviderKindOpenAI, DisplayName: "Empty provider", APIKey: testProviderKey})
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "codex", UserHome: t.TempDir(), ConfigHome: t.TempDir(), AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref})
	out, err := m.ProfileModelCatalog(t.Context(), tenant, profile.Ref, false)
	if err != nil || out.State != "unsupported" || !out.FreeText || len(out.Models) != 0 || out.Message == "" {
		t.Fatalf("empty list reported as fresh: %+v %v", out, err)
	}
}

// A failed read is not cached: the next plain read asks the provider again and
// shows the recovery instead of the old failure.
func TestProfileModelCatalogFailureIsNotCached(t *testing.T) {
	probe := &fakeProbe{err: errors.New("provider unreachable")}
	m, _, tenant, _ := newRuntimeHarness(t, WithProviderSecretVault(newFakeVault()), WithProviderProbe(probe), WithProviderDriver(NewCodexDriver()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{Kind: ProviderKindOpenAI, DisplayName: "Flaky provider", APIKey: testProviderKey})
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "codex", UserHome: t.TempDir(), ConfigHome: t.TempDir(), AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref})
	out, err := m.ProfileModelCatalog(t.Context(), tenant, profile.Ref, false)
	if err != nil || out.State != "failed" {
		t.Fatalf("first read: %+v %v", out, err)
	}
	probe.mu.Lock()
	probe.err = nil
	probe.result = ProviderProbeResult{Models: []string{"recovered-model"}}
	probe.mu.Unlock()
	out, err = m.ProfileModelCatalog(t.Context(), tenant, profile.Ref, false)
	if err != nil || out.State != "fresh" || len(out.Models) != 1 || out.Models[0].ID != "recovered-model" {
		t.Fatalf("failure was cached: %+v %v", out, err)
	}
}

// readCatalogRecords loads records copied verbatim from the real tool's answer.
func readCatalogRecords(t *testing.T, name string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var records []map[string]any
	if err = json.Unmarshal(raw, &records); err != nil {
		t.Fatal(err)
	}
	return records
}

// Records copied from codex-cli 0.162.1 `model/list`. Codex sends no isNew or
// isLegacy: an older model names its successor in `upgrade`, a model it wants
// tried carries `availabilityNux`. Those are the only new/legacy signals.
func TestProfileModelCatalogCodexRealRecords(t *testing.T) {
	peer := &catalogToolPeer{t: t, signed: true, codexModels: readCatalogRecords(t, "codex-0.162.1-model-list.json")}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(peer), WithProviderDriver(NewCodexDriver()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "codex", UserHome: t.TempDir(), ConfigHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
	out, err := m.ProfileModelCatalog(t.Context(), tenant, profile.Ref, false)
	if err != nil || out.State != "fresh" || len(out.Models) != 3 {
		t.Fatalf("catalog: %+v %v", out, err)
	}
	want := map[string]struct{ isDefault, isNew, isLegacy bool }{
		"gpt-6.1-sol": {true, true, false},
		"gpt-6-astra": {false, false, false},
		"gpt-5.5":     {false, false, true},
	}
	for _, md := range out.Models {
		w, ok := want[md.ID]
		if !ok || md.IsDefault == nil || md.IsNew == nil || md.IsLegacy == nil ||
			*md.IsDefault != w.isDefault || *md.IsNew != w.isNew || *md.IsLegacy != w.isLegacy {
			t.Errorf("%s markers: %+v", md.ID, md)
		}
	}
	first := out.Models[0]
	if first.DisplayName != "GPT-6.1-Sol" || first.DefaultEffort != "low" || strings.Join(first.EffortLevels, ",") != "low,medium,high,xhigh,max,ultra" || strings.Join(first.ServiceTiers, ",") != "priority" {
		t.Errorf("field mapping: %+v", first)
	}
	if out.DefaultModel != "gpt-6.1-sol" || out.RefreshIntervalSeconds != 120 || out.CheckedAt == "" {
		t.Fatalf("envelope: %+v", out)
	}
}

// Records copied from Claude Code 2.1.296 `initialize`. It reports no default,
// new, legacy or default-effort marker, so none may appear; only the "default"
// alias is the tool's own default, and supportsFastMode is sent only where true.
func TestProfileModelCatalogClaudeRealRecords(t *testing.T) {
	peer := &catalogToolPeer{t: t, signed: true, claudeModels: readCatalogRecords(t, "claude-2.1.296-initialize-models.json")}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(peer))
	m.UseExecutionEnvironmentRef(testEnvRef)
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", UserHome: t.TempDir(), ConfigHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
	out, err := m.ProfileModelCatalog(t.Context(), tenant, profile.Ref, false)
	if err != nil || out.State != "fresh" || len(out.Models) != 2 {
		t.Fatalf("catalog: %+v %v", out, err)
	}
	for _, md := range out.Models {
		if md.IsNew != nil || md.IsLegacy != nil || md.DefaultEffort != "" || strings.Join(md.EffortLevels, ",") != "low,medium,high,xhigh,max" {
			t.Errorf("%s: %+v", md.ID, md)
		}
	}
	alias, fable := out.Models[0], out.Models[1]
	if alias.ID != "default" || alias.IsDefault == nil || !*alias.IsDefault || alias.SupportsFastMode == nil || !*alias.SupportsFastMode {
		t.Errorf("default alias: %+v", alias)
	}
	if fable.ID != "fable" || fable.IsDefault != nil || fable.SupportsFastMode != nil {
		t.Errorf("fable: %+v", fable)
	}
	if out.DefaultModel != "default" {
		t.Fatalf("default model = %q", out.DefaultModel)
	}
}

// The cache is per profile and lasts the TTL, no longer.
func TestProfileModelCatalogCacheKeyAndTTLWindow(t *testing.T) {
	peer := &catalogToolPeer{t: t, signed: true, modelIDs: []string{"model-a"}}
	m, _, tenant, clk := newRuntimeHarness(t, WithRunner(peer), WithProviderDriver(NewCodexDriver()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	a := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "codex", UserHome: t.TempDir(), ConfigHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
	b := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "codex", UserHome: t.TempDir(), ConfigHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
	read := func(ref string) {
		t.Helper()
		if _, err := m.ProfileModelCatalog(t.Context(), tenant, ref, false); err != nil {
			t.Fatal(err)
		}
	}
	read(a.Ref)
	read(b.Ref)
	if len(peer.specs) != 2 {
		t.Fatalf("two profiles shared one cache entry: %d launches", len(peer.specs))
	}
	clk.set(baseTime.Add(90 * time.Second))
	read(a.Ref)
	if len(peer.specs) != 2 {
		t.Fatalf("entry expired before its TTL: %d launches", len(peer.specs))
	}
	clk.set(baseTime.Add(121 * time.Second))
	read(a.Ref)
	if len(peer.specs) != 3 {
		t.Fatalf("entry outlived its TTL: %d launches", len(peer.specs))
	}
}

// The wire answer and the published schema are the same contract: every key the
// engine sends is declared, every required key is sent, and the query is strict.
func TestProfileModelCatalogWireMatchesSchema(t *testing.T) {
	peer := &catalogToolPeer{t: t, signed: true, codexModels: []map[string]any{{
		"model": "m1", "displayName": "M1", "isDefault": true, "availabilityNux": map[string]any{"message": "Try it"}, "upgrade": nil,
		"supportedReasoningEfforts": []map[string]string{{"reasoningEffort": "low"}}, "defaultReasoningEffort": "low",
		"serviceTiers": []map[string]string{{"id": "priority"}},
	}}}
	m := New(WithRunner(peer), WithProviderDriver(NewCodexDriver()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "catalog-wire")
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "codex", UserHome: t.TempDir(), ConfigHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
	path := "/v1/m/sessions/provider-profiles/" + profile.Ref + "/models"

	doc := api.ModuleOpenAPIDocument([]api.Module{New()})
	op := doc["paths"].(map[string]any)["/v1/m/sessions/provider-profiles/{ref}/models"].(map[string]any)["get"].(map[string]any)
	schema := op["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	check := func(where string, body map[string]any, schema map[string]any) {
		t.Helper()
		props := schema["properties"].(map[string]any)
		for key := range body {
			if props[key] == nil {
				t.Errorf("%s: wire key %q is not in the published schema", where, key)
			}
		}
		for _, key := range schema["required"].([]string) {
			if _, ok := body[key]; !ok {
				t.Errorf("%s: required key %q is missing from the wire answer", where, key)
			}
		}
	}
	r := h.do("GET", path, admin, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("catalog = %d %s", r.code, r.raw)
	}
	check("catalog", r.body, schema)
	item := schema["properties"].(map[string]any)["models"].(map[string]any)["items"].(map[string]any)
	check("model", r.body["models"].([]any)[0].(map[string]any), item)
	for _, code := range []string{"400", "401", "403", "404", "409", "422"} {
		if op["responses"].(map[string]any)[code] == nil {
			t.Errorf("response %s is not documented", code)
		}
	}

	for query, want := range map[string]int{"?refresh=false": 200, "?refresh=true": 200, "?refresh=": 400, "?refresh=1": 400, "?refresh=true&refresh=true": 400, "?other=1": 400, "?refresh=%zz": 400} {
		if got := h.do("GET", path+query, admin, tenantHdr(tenant)).code; got != want {
			t.Errorf("query %q = %d, want %d", query, got, want)
		}
	}
	if got := h.do("GET", "/v1/m/sessions/provider-profiles/ppf_missing/models", admin, tenantHdr(tenant)).code; got != http.StatusNotFound {
		t.Errorf("unknown profile = %d, want 404", got)
	}
}

// A tool's answer may carry keys of a shape another tool or an older script
// used; only the keys the tool really sends are read.
func TestProfileModelCatalogIgnoresKeysTheToolsNeverSend(t *testing.T) {
	for _, driver := range []string{"claude", "codex"} {
		t.Run(driver, func(t *testing.T) {
			invented := map[string]any{"isDefault": true, "isNew": true, "isLegacy": true, "defaultEffort": "high"}
			peer := &catalogToolPeer{t: t, signed: true}
			if driver == "claude" {
				peer.claudeModels = []map[string]any{{"value": "opus", "supportedEffortLevels": []string{"low"}}}
				for k, v := range invented {
					peer.claudeModels[0][k] = v
				}
			} else {
				// Codex does send isDefault; the other keys it never sends.
				peer.codexModels = []map[string]any{{"model": "m", "upgrade": nil, "availabilityNux": nil}}
				for k, v := range invented {
					if k != "isDefault" {
						peer.codexModels[0][k] = v
					}
				}
			}
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(peer), WithProviderDriver(NewCodexDriver()))
			m.UseExecutionEnvironmentRef(testEnvRef)
			profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: driver, UserHome: t.TempDir(), ConfigHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
			out, err := m.ProfileModelCatalog(t.Context(), tenant, profile.Ref, false)
			if err != nil || out.State != "fresh" || len(out.Models) != 1 {
				t.Fatalf("catalog: %+v %v", out, err)
			}
			md := out.Models[0]
			if (md.IsNew != nil && *md.IsNew) || (md.IsLegacy != nil && *md.IsLegacy) || md.DefaultEffort != "" || out.DefaultModel != "" {
				t.Fatalf("a key the tool never sends was read: %+v default=%q", md, out.DefaultModel)
			}
		})
	}
}
