//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// The enterprise-system connectors are Business (docs/editions.md): SAP OData,
// Salesforce, ServiceNow CMDB, SharePoint, Oracle audit, KMIP and the Claude
// Compliance API. The Community build does not construct them and says so by name.
var (
	businessObservationKindsTest = []string{"claude-compliance", "kmip", "oracle-audit", "servicenow-cmdb", "conjur"}
	businessContentKindsTest     = []string{"sharepoint", "sap_odata", "salesforce"}
)

func TestCommunityDoesNotConstructBusinessSourceKinds(t *testing.T) {
	for _, kind := range businessObservationKindsTest {
		if _, ok := buildInProcSource(kind); ok {
			t.Errorf("Community constructs the Business observation source %q", kind)
		}
		if _, listed := inProcSourceFactories[kind]; listed {
			t.Errorf("Community catalog offers the Business observation source %q", kind)
		}
	}
	for _, kind := range businessContentKindsTest {
		if _, ok := buildContentSource(kind, nil); ok {
			t.Errorf("Community constructs the Business document source %q", kind)
		}
	}
	offered, err := (&sourceReconciler{}).ListConnectors(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range offered {
		for _, kind := range businessObservationKindsTest {
			if info.Kind == kind {
				t.Errorf("connector onboarding offers the Business kind %q", kind)
			}
		}
	}
	// The neighbors stay: the cut is the seven kinds, nothing around them.
	for _, kind := range []string{"pgaudit", "snowflake-audit", "vault-audit", "claude-api", "mssql-audit"} {
		if _, ok := buildInProcSource(kind); !ok {
			t.Errorf("Community lost the observation source %q", kind)
		}
	}
	for _, kind := range []string{"gdrive", "confluence", "notion", "s3content", "snowflake", "azure_ai_search", "postgres", "filesystem"} {
		if _, ok := buildContentSource(kind, nil); !ok {
			t.Errorf("Community lost the document source %q", kind)
		}
	}
}

// writeSourcesConfig points OLIVARES_SOURCES_CONFIG at body for one test.
func writeSourcesConfig(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sources.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLIVARES_SOURCES_CONFIG", path)
}

// A configured Business kind refuses the boot and names Business, the way a
// configured directory source does (identity_edition_test.go).
func TestCommunityBootRefusesConfiguredBusinessSourceKinds(t *testing.T) {
	type bootCase struct{ kind, body string }
	var cases []bootCase
	for _, kind := range businessObservationKindsTest {
		cases = append(cases, bootCase{kind, `{"sources":[{"name":"x","kind":"` + kind + `","tenant":"acme"}]}`})
	}
	for _, kind := range businessContentKindsTest {
		cases = append(cases, bootCase{kind, `{"documents":[{"name":"x","kind":"` + kind + `"}]}`})
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			writeSourcesConfig(t, c.body)
			prepareCompositionTestBoot(t)
			eng, err := boot(context.Background(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Version: "test"})
			if eng != nil {
				eng.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "Business") || !strings.Contains(err.Error(), c.kind) {
				t.Fatalf("Community boot with a configured %q source = %v, want a refusal naming %q and Business", c.kind, err, c.kind)
			}
		})
	}
}

// An external plugin that happens to carry a Business kind name is the operator's own
// binary, admitted (or refused) by its trust gate; the kind is not consulted.
func TestCommunityBusinessKindCheckIgnoresExternalPlugins(t *testing.T) {
	cfg := sourcesConfig{
		Sources:   []sourceSpec{{Kind: "oracle-audit", Plugin: &externalPluginSpec{}}},
		Documents: []documentSpec{{Kind: "sharepoint", Plugin: &externalPluginSpec{}}},
	}
	if err := validateBusinessSourcesConfig(cfg); err != nil {
		t.Fatalf("external plugin sources must ignore first-party kind names: %v", err)
	}
	if err := checkInlineSecrets(model.SourceDef{Kind: "kmip", Plugin: &model.SourcePluginRef{}}); err != nil {
		t.Fatalf("an external plugin write must ignore first-party kind names: %v", err)
	}
}

// The connector onboarding API, the sources plan, the source roster write and the
// reconciler's apply path all answer with the one Business refusal (501
// connector_unavailable through the API) instead of "unknown kind".
func TestCommunityBusinessKindRefusalsNameBusiness(t *testing.T) {
	for _, kind := range businessObservationKindsTest {
		for name, err := range map[string]error{
			"onboarding":   requireKnownOnboardKind(kind),
			"roster write": checkInlineSecrets(model.SourceDef{Kind: kind}),
		} {
			if !errors.Is(err, api.ErrConnectorUnavailable) || !strings.Contains(err.Error(), "Business") {
				t.Errorf("%s of %q = %v, want ErrConnectorUnavailable naming Business", name, kind, err)
			}
		}
		problems := checkSourceKind(model.SourceDef{Kind: kind})
		if len(problems) != 1 || !strings.Contains(problems[0], "Business") {
			t.Errorf("sources plan for %q = %v, want one problem naming Business", kind, problems)
		}
		if _, _, reason := (&sourceReconciler{}).defaultPrepare(context.Background(), model.SourceDef{Kind: kind}); !strings.Contains(reason, "Business") {
			t.Errorf("apply of %q = %q, want a reason naming Business", kind, reason)
		}
	}
	// A real typo stays an unknown kind: only the seven name Business.
	for name, err := range map[string]error{
		"onboarding":   requireKnownOnboardKind("orcle-audit"),
		"roster write": checkInlineSecrets(model.SourceDef{Kind: "orcle-audit"}),
	} {
		if name == "roster write" {
			if err != nil {
				t.Errorf("roster write of a typo = %v, want nil (the apply path reports it)", err)
			}
			continue
		}
		if err == nil || errors.Is(err, api.ErrConnectorUnavailable) || strings.Contains(err.Error(), "Business") {
			t.Errorf("%s of a typo = %v, want the unknown-kind refusal", name, err)
		}
	}
}

// Workspace ingest answers 501, the one status for a Business capability, with a body that
// names Business, and never opens the source.
func TestCommunityWorkspaceIngestRefusesBusinessDocumentKinds(t *testing.T) {
	eng, token, tenant := wireBoot(t, wireBackends(t)[0], t.TempDir())
	request := func(t *testing.T, method, path string, body any, want int) map[string]any {
		t.Helper()
		code, out, raw := doDemoViewJSON(t, eng.api.Handler(), method, path, token, tenant.String(), body)
		if code != want {
			t.Fatalf("%s %s = %d, want %d (%s)", method, path, code, want, raw)
		}
		if want == http.StatusNotImplemented && !strings.Contains(raw, "Business") {
			t.Fatalf("%s %s refusal %s does not name Business", method, path, raw)
		}
		return out
	}
	for _, kind := range businessContentKindsTest {
		t.Run(kind, func(t *testing.T) {
			kb := request(t, "POST", "/v1/m/knowledge/kbs", map[string]any{"name": "kb-" + kind, "classification": "public", "embed_policy": "local_only"}, http.StatusCreated)
			request(t, "POST", "/v1/m/sourcescope/bindings", map[string]any{"source_type": "knowledge", "source_ref": kb["id"].(string), "scope_tree": "workspace", "scope_ref": "default", "enabled": true}, http.StatusCreated)
			request(t, "POST", "/v1/m/sourcescope/workspace-connectors", map[string]any{"name": kind, "kind": kind, "workspace_ref": "default", "enabled": true, "config": map[string]string{"mode": "live"}, "secrets": map[string]string{}}, http.StatusCreated)
			request(t, "POST", "/v1/m/knowledge/kbs/"+kb["id"].(string)+"/ingest", map[string]any{"source": "workspace:" + kind}, http.StatusNotImplemented)
		})
	}
}
