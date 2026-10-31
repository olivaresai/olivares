// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/models"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions"
)

func TestAutomaticProviderCatalogDecodingPaginationAndRedaction(t *testing.T) {
	for _, body := range []string{`not json`, `{}`, `{"data":null}`, `{"data":[{}]}`, `{"data":[null]}`, `{"data":[{"id":""}]}`, `{"data":[{"id":"has space"}]}`, `{"data":[{"id":"has\u0001control"}]}`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer srv.Close()
			if _, err := newProviderProbe().Probe(context.Background(), sessions.ProviderProbeRequest{Kind: sessions.ProviderKindAnthropic, BaseURL: srv.URL, APIKey: "fixture-key-value"}); err == nil {
				t.Fatal("malformed catalog became a successful empty list")
			}
		})
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.Header.Get("x-api-key") != "fixture-key-value" {
			t.Error("not a read-only authenticated model request")
		}
		if r.URL.Query().Get("after_id") == "page1" {
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-two"}],"has_more":false}`))
		} else {
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-one"}],"last_id":"page1","has_more":true}`))
		}
	}))
	defer srv.Close()
	result, err := newProviderProbe().Probe(context.Background(), sessions.ProviderProbeRequest{Kind: sessions.ProviderKindAnthropic, BaseURL: srv.URL, APIKey: "fixture-key-value"})
	if err != nil || len(result.Models) != 2 || calls != 2 {
		t.Fatalf("catalog pagination: %+v %v calls=%d", result, err, calls)
	}
	unsafe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "fixture-key-value"}}})
	}))
	defer unsafe.Close()
	if _, err := newProviderProbe().Probe(context.Background(), sessions.ProviderProbeRequest{Kind: sessions.ProviderKindOpenAI, BaseURL: unsafe.URL, APIKey: "fixture-key-value"}); err == nil {
		t.Fatal("credential echo became a model ID")
	}
}

func TestAutomaticModelSourcesKeepInvalidActiveAccounts(t *testing.T) {
	ctx := context.Background()
	ss, _, tenant := newSessionsStore(t)
	ss.UseExecutionEnvironmentRef("catalog-fixture")
	configHome, userHome := t.TempDir(), t.TempDir()
	profile, err := ss.CreateProfile(ctx, tenant, sessions.CreateProfileInput{Driver: "claude", ConfigHome: configHome, UserHome: userHome, AuthSource: sessions.AuthSourceAccountHome})
	if err != nil {
		t.Fatal(err)
	}
	discovery := configuredModelDiscovery{sessions: ss}
	before, err := discovery.Sources(ctx, tenant)
	if err != nil || len(before) != 1 {
		t.Fatalf("initial source: %+v %v", before, err)
	}
	if err := os.Remove(configHome); err != nil {
		t.Fatal(err)
	}
	after, err := discovery.Sources(ctx, tenant)
	if err != nil || len(after) != 1 || after[0].AccountRef != profile.Ref || after[0].Driver != "claude" || after[0].Revision == before[0].Revision {
		t.Fatalf("invalid active account disappeared or kept its old observation: %+v %v", after, err)
	}
	if ids, err := discovery.Discover(ctx, tenant, after[0]); err == nil || len(ids) != 0 {
		t.Fatalf("invalid home advertised models: %v %v", ids, err)
	}
	if err := os.Mkdir(configHome, 0700); err != nil {
		t.Fatal(err)
	}
	recovered, err := discovery.Sources(ctx, tenant)
	if err != nil || len(recovered) != 1 || recovered[0].Revision == after[0].Revision {
		t.Fatalf("home recovery did not invalidate the failed observation: %+v %v", recovered, err)
	}
}

type catalogRoutes map[string]api.ModuleHandler

func (r catalogRoutes) Handle(method, pattern string, _ auth.Permission, handler api.ModuleHandler) {
	r[method+" "+pattern] = handler
}
func (r catalogRoutes) HandleEntity(method, pattern string, perm auth.Permission, _ api.EntityRef, handler api.ModuleHandler) {
	r.Handle(method, pattern, perm, handler)
}

func TestConfiguredConnectorCatalogConsumption(t *testing.T) {
	const credential = "fixture-catalog-key-opaque"
	t.Setenv("OLIVARES_TEST_CATALOG_KEY", credential)
	tenant, foreign := model.TenantID(model.NewID()), model.TenantID(model.NewID())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/local/api/tags":
			w.Write([]byte(`{"models":[{"name":"shared-model"},{"name":"shared-model"}]}`))
			return
		case "/local/api/show":
			if r.Method != "POST" {
				t.Error("Ollama metadata must use show")
			}
			w.Write([]byte(`{"capabilities":["tools"],"model_info":{"llama.context_length":8192}}`))
			return
		case "/local/v1/models":
			w.Write([]byte(`{"data":[{"id":"shared-model"},{"id":"shared-model"}]}`))
			return
		}
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer "+credential {
			t.Error("catalog was not an authenticated metadata GET")
		}
		switch r.URL.Path {
		case "/live/models":
			w.Write([]byte(`{"data":[{"id":"deepseek-chat"}]}`))
		case "/failed/models":
			http.Error(w, credential, http.StatusForbidden)
		case "/echo/models":
			w.Write([]byte(`{"data":[{"id":"` + credential + `"}]}`))
		default:
			t.Errorf("unconfigured/foreign/disabled source contacted: %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	sr, st, _ := newReconcilerHarness(t)
	sr.resolver = secret.NewResolver(map[string]secret.Handler{secret.SchemeEnv: secret.EnvHandler{}})
	sr.useEnvironmentRef("catalog-test-environment")
	for _, name := range []string{"live", "offline", "failed", "echo", "disabled", "foreign", "stale"} {
		config := map[string]string{"base_url": srv.URL + "/" + name, "api_key": "env:OLIVARES_TEST_CATALOG_KEY"}
		if name == "offline" {
			delete(config, "api_key")
		}
		scope := tenant
		if name == "foreign" {
			scope = foreign
		}
		putRow(t, st, model.SourceDef{Name: name, Kind: "deepseek", Tenant: scope.String(), Enabled: name != "disabled", Config: config})
	}
	putRow(t, st, model.SourceDef{Name: "local", Kind: "local", Tenant: tenant.String(), Enabled: true, Config: map[string]string{"ollama_url": srv.URL + "/local", "vllm_url": srv.URL + "/local"}})
	ctx := context.Background()
	if report, err := sr.reconcile(ctx); err != nil || len(report.Rejected) != 0 {
		t.Fatalf("reconcile failed: %v %v", report, err)
	}
	// A saved edit not applied to the source must not be read with new credentials.
	putRow(t, st, model.SourceDef{Name: "stale", Kind: "deepseek", Tenant: tenant.String(), Enabled: true, Config: map[string]string{"base_url": srv.URL + "/stale-new", "api_key": "env:OLIVARES_TEST_CATALOG_KEY"}})
	m := models.New()
	wireModelAvailability(moduleSet{models: m}, nil, sr)
	routes := catalogRoutes{}
	m.APIRoutes(routes)
	for _, path := range []string{"/catalog", "/features"} {
		rr := httptest.NewRecorder()
		routes["GET "+path](rr, httptest.NewRequest("GET", path, nil), api.ModuleContext{Tenant: tenant})
		var body struct {
			Catalogs []struct {
				Source string `json:"source_ref"`
				State  string `json:"state"`
				Models []struct {
					Family       string   `json:"family"`
					Provider     string   `json:"provider_ref"`
					Capabilities []string `json:"capabilities"`
					Context      int64    `json:"context_window"`
					Source       string   `json:"capability_source"`
				} `json:"models"`
			} `json:"connector_catalogs"`
		}
		if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &body) != nil || len(body.Catalogs) != 6 {
			t.Fatalf("%s missing configured catalogs: %d %s", path, rr.Code, rr.Body.String())
		}
		for _, catalog := range body.Catalogs {
			switch catalog.Source {
			case "local":
				if catalog.State != "reference" || len(catalog.Models) != 2 {
					t.Fatalf("%s local provider identities lost: %+v", path, catalog)
				}
				seen := map[string]bool{}
				for _, md := range catalog.Models {
					if md.Family != "shared-model" || seen[md.Provider] {
						t.Fatalf("%s duplicate or renamed local model: %+v", path, md)
					}
					seen[md.Provider] = true
					switch md.Provider {
					case "ollama":
						if md.Context != 8192 || !slices.Equal(md.Capabilities, []string{"tool_use"}) || md.Source != "live" {
							t.Fatalf("%s lost Ollama metadata: %+v", path, md)
						}
					case "vllm":
						if md.Context != 0 || len(md.Capabilities) != 0 || md.Source != "unknown" {
							t.Fatalf("%s invented vLLM metadata: %+v", path, md)
						}
					default:
						t.Fatalf("%s local provider identity replaced: %+v", path, md)
					}
				}
			case "live", "offline":
				want := "live"
				if catalog.Source == "offline" {
					want = "declared"
				}
				if catalog.State != "reference" || len(catalog.Models) == 0 || catalog.Models[0].Source != want {
					t.Fatalf("%s lost provenance: %+v", path, catalog)
				}
			default:
				if catalog.State != "unavailable" || len(catalog.Models) != 0 {
					t.Fatalf("%s published failed source: %+v", path, catalog)
				}
			}
		}
		if strings.Contains(rr.Body.String(), credential) || strings.Contains(rr.Body.String(), srv.URL) || strings.Contains(rr.Body.String(), "foreign") || strings.Contains(rr.Body.String(), "disabled") {
			t.Fatal("catalog leaked secrets, endpoints or unrelated sources")
		}
	}
	available, err := m.AvailableModels(ctx, tenant)
	if err != nil || len(available) != 0 {
		t.Fatalf("reference models became executable availability: %+v %v", available, err)
	}
	// Disabling a source takes effect without restarting the consumer.
	putRow(t, st, model.SourceDef{Name: "live", Kind: "deepseek", Tenant: tenant.String(), Enabled: false})
	entries, err := (configuredModelDiscovery{catalogSources: sr}).Catalogs(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.SourceRef == "live" {
			t.Fatal("disabled source still exposed")
		}
	}
}

// mapProviderVault keeps sealed fixture keys in memory; each Seal gets its own locator.
type mapProviderVault map[string][]byte

func (v mapProviderVault) Seal(_ context.Context, _ auth.Principal, _ model.TenantID, name string, value []byte) (string, error) {
	v[name] = slices.Clone(value)
	return name, nil
}
func (v mapProviderVault) Open(_ context.Context, _ model.TenantID, locator string) ([]byte, error) {
	return slices.Clone(v[locator]), nil
}
func (v mapProviderVault) Revoke(_ context.Context, _ auth.Principal, _ model.TenantID, locator string) error {
	delete(v, locator)
	return nil
}

// endpointModels answers a probe with the models of the endpoint it was asked about.
type endpointModels map[string][]string

func (p endpointModels) Probe(_ context.Context, req sessions.ProviderProbeRequest) (sessions.ProviderProbeResult, error) {
	return sessions.ProviderProbeResult{Models: p[req.BaseURL]}, nil
}

// A connection test, a new name or a new default model leave what the provider serves
// unchanged, so its Available row stays Ready. A new endpoint or key does change it, so
// the row stops offering the old list until discovery reads the provider again.
func TestProviderAvailabilityChangesOnlyWithEndpointOrKey(t *testing.T) {
	ctx := context.Background()
	const first, second = "https://first.example.com/v1", "https://second.example.com/v1"
	ss := sessions.New(sessions.WithProviderSecretVault(mapProviderVault{}),
		sessions.WithProviderProbe(endpointModels{first: {"first-model"}, second: {"second-model"}}))
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, func(reg store.ExtensionRegistry) error {
		if err := models.New().RegisterSchema(reg); err != nil {
			return err
		}
		return ss.RegisterSchema(reg)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ss.UseData(api.NewModuleData(st))
	m := models.New()
	m.UseData(api.NewModuleData(st))
	m.UseAvailabilitySource(configuredModelDiscovery{sessions: ss}, func(context.Context) ([]model.TenantID, error) { return nil, nil })
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	rec, err := ss.CreateProviderRecord(ctx, tenant, sessions.CreateProviderRecordInput{
		Kind: sessions.ProviderKindOpenAICompatible, DisplayName: "Gateway", BaseURL: first, APIKey: "fixture-provider-key-0001"})
	if err != nil {
		t.Fatal(err)
	}
	row := func(step string) models.Availability {
		t.Helper()
		rows, err := m.AvailableModels(ctx, tenant)
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s: rows = %+v %v", step, rows, err)
		}
		return rows[0]
	}
	ready := func(step, want string) {
		t.Helper()
		got := row(step)
		if got.State != "fresh" || len(got.Models) != 1 || got.Models[0].ID != want {
			t.Fatalf("%s: row = %s %+v %q, want Ready with %s", step, got.State, got.Models, got.Message, want)
		}
	}
	refreshed := func(step, want string) {
		t.Helper()
		if got := row(step); got.State == "fresh" {
			t.Fatalf("%s: the row still offers %+v from before the change", step, got.Models)
		}
		if err := m.RefreshAvailability(ctx, tenant); err != nil {
			t.Fatal(err)
		}
		ready(step+", after discovery", want)
	}
	if err := m.RefreshAvailability(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	ready("first discovery", "first-model")

	if _, err := ss.TestProviderRecord(ctx, tenant, rec.Ref); err != nil {
		t.Fatal(err)
	}
	ready("Test connection", "first-model")
	name, none := "Gateway two", ""
	if _, err := ss.PatchProviderRecord(ctx, tenant, rec.Ref, sessions.ProviderRecordPatch{DisplayName: &name}); err != nil {
		t.Fatal(err)
	}
	ready("rename", "first-model")
	if _, err := ss.PatchProviderRecord(ctx, tenant, rec.Ref, sessions.ProviderRecordPatch{DefaultModel: &none}); err != nil {
		t.Fatal(err)
	}
	ready("default model", "first-model")

	url := second
	if _, err := ss.PatchProviderRecord(ctx, tenant, rec.Ref, sessions.ProviderRecordPatch{BaseURL: &url}); err != nil {
		t.Fatal(err)
	}
	refreshed("new endpoint", "second-model")
	key := "fixture-provider-key-0002"
	if _, err := ss.PatchProviderRecord(ctx, tenant, rec.Ref, sessions.ProviderRecordPatch{APIKey: &key}); err != nil {
		t.Fatal(err)
	}
	refreshed("new key", "second-model")
}
