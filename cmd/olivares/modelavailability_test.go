// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secret"
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
