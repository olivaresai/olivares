// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/model"
)

// deployRefusal is the body the engine answers every Community deploy route with.
const deployRefusal = `{"error":{"code":"module_error","message":"Deployment is a Business feature: https://olivares.ai/pricing"}}`

func TestCommunityHelpHidesDeploy(t *testing.T) {
	command, _, err := newRootCmd().Find([]string{"deploy"})
	if err != nil || command == nil || command.Name() != "deploy" {
		t.Fatalf("published command deploy disappeared: %v", err)
	}
	if !command.Hidden {
		t.Error("Community advertises deploy in help")
	}
}

// TestCommunityDeployCommandsExplainEditionRefusal pins the CLI answer to the
// engine's 501: the Business sentence and exit 9, for a read and for a mutation.
func TestCommunityDeployCommandsExplainEditionRefusal(t *testing.T) {
	for _, args := range [][]string{
		{"deploy", "definitions", "ls"}, {"deploy", "apply", "dep-1"}, {"deploy", "retire", "dep-1", "--yes"},
		{"deploy", "definitions", "rm", "dep-1", "--yes"}, {"deploy", "wirings"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			server := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotImplemented)
				_, _ = w.Write([]byte(deployRefusal))
			})
			stdout, _, err := execRoot(t, lot3Args(server.URL, args...)...)
			if exitcode.From(err) != exitcode.Edition || err == nil || !strings.Contains(err.Error(), "Deployment is a Business feature: "+pricingURL) {
				t.Fatalf("deploy refusal = %v; want the Business sentence and exit 9", err)
			}
			if strings.TrimSpace(stdout) != "" {
				t.Errorf("a refused command printed %q", stdout)
			}
		})
	}
}

// TestCommunityDeployRoutesAnswerBusiness runs the real composition: every published
// route answers one documented status, 501, with the Business sentence.
func TestCommunityDeployRoutesAnswerBusiness(t *testing.T) {
	h := newHarness(t)
	id := model.NewID().String()
	def := "/v1/m/deploy/definitions/" + id
	for _, route := range []struct{ method, path string }{
		{"GET", "/v1/m/deploy/definitions"},
		{"POST", "/v1/m/deploy/definitions"},
		{"GET", def},
		{"PUT", def},
		{"DELETE", def},
		{"GET", def + "/revisions"},
		{"POST", def + "/rollback"},
		{"POST", def + "/plan"},
		{"POST", def + "/verify"},
		{"POST", def + "/apply"},
		{"POST", def + "/retire"},
		{"GET", "/v1/m/deploy/wirings"},
		{"GET", "/v1/m/deploy/operations"},
		{"GET", "/v1/m/deploy/executor"},
		{"GET", "/v1/m/deploy/executor/config"},
		{"PUT", "/v1/m/deploy/executor/config"},
		{"POST", "/v1/m/deploy/executor/test"},
	} {
		code, raw := h.req(route.method, route.path, h.adminToken, h.tenantA, map[string]any{})
		if code != http.StatusNotImplemented || strings.TrimSpace(string(raw)) != deployRefusal {
			t.Errorf("%s %s = %d %s; want 501 %s", route.method, route.path, code, raw, deployRefusal)
		}
		// The permission check still runs first: no token, no answer about the edition.
		if code, _ := h.req(route.method, route.path, "", h.tenantA, map[string]any{}); code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token = %d; want 401", route.method, route.path, code)
		}
	}
}

// TestCommunityCatalogActivationStopsAtTheBusinessRefusal: activating a catalog
// instance reaches the deploy module, answers its 501, and never records active.
func TestCommunityCatalogActivationStopsAtTheBusinessRefusal(t *testing.T) {
	h := newHarness(t)
	spec := map[string]any{"image": "catalog-agent:1"}
	entry := h.catalogCreate(t, "/entries", map[string]any{
		"kind": "agent", "name": "catalog agent", "slug": "catalog-agent", "version": "1.0.0", "spec": spec,
	})
	if code, raw := h.req("POST", "/v1/m/catalog/entries/"+entry+"/approve", h.adminToken, h.tenantA, nil); code != http.StatusOK {
		t.Fatalf("approve = %d %s", code, raw)
	}
	instance := h.catalogCreate(t, "/entries/"+entry+"/instantiate", map[string]any{"name": "catalog-agent", "target_ref": "deployment:" + model.NewID().String()})
	path := "/v1/m/catalog/instances/" + instance + "/transition"
	if code, raw := h.req("POST", path, h.adminToken, h.tenantA, map[string]any{"status": "approved"}); code != http.StatusOK {
		t.Fatalf("approve instance = %d %s", code, raw)
	}
	if code, raw := h.req("POST", path, h.adminToken, h.tenantA, map[string]any{"status": "active"}); code != http.StatusNotImplemented {
		t.Fatalf("activation = %d %s; want the deploy module's 501", code, raw)
	}
	assertEq(t, "instance retained", h.getJSON(h.adminToken, h.tenantA, "/v1/m/catalog/instances/"+instance)["status"], "approved")
}

// TestCommunityCatalogActivationCommandExplainsEditionRefusal: the CLI turns the same
// 501 into the Business sentence and exit 9, not a server failure.
func TestCommunityCatalogActivationCommandExplainsEditionRefusal(t *testing.T) {
	server := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(deployRefusal))
	})
	_, _, err := execRoot(t, lot3Args(server.URL, "catalog", "instances", "transition", "ci_1", "--status", "active", "--yes")...)
	if exitcode.From(err) != exitcode.Edition || err == nil || !strings.Contains(err.Error(), "Deploying a catalog entry is a Business feature: "+pricingURL) {
		t.Fatalf("catalog activation refusal = %v; want the Business sentence and exit 9", err)
	}
}

// TestCommunityIgnoresADeployExecutorFile: the operator file is read and validated, but
// it builds no executor, binder or drift loop, and the log says why. Business keeps it.
func TestCommunityIgnoresADeployExecutorFile(t *testing.T) {
	t.Setenv("OLIVARES_DEPLOY_EXECUTOR_CONFIG", "/etc/olivares/deploy.json")
	cfg := deployExecutorConfig{Docker: &dockerCfgJSON{}}
	cfg.Drift.Tenants = []driftTenantCfg{{}}
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	for _, edition := range []string{"community", "enterprise"} {
		thisEdition.name = edition
		var logged bytes.Buffer
		got := editionDeployExecutorConfig(cfg, slog.New(slog.NewTextHandler(&logged, nil)))
		if community := edition == "community"; (got.Docker == nil && len(got.Drift.Tenants) == 0) != community {
			t.Errorf("%s: the config was kept=%v; want kept=%v", edition, got.Docker != nil, !community)
		} else if community != strings.Contains(logged.String(), "Business capability") {
			t.Errorf("%s: log %q", edition, logged.String())
		}
	}
	t.Setenv("OLIVARES_DEPLOY_EXECUTOR_CONFIG", "")
	thisEdition.name = "community"
	if got := editionDeployExecutorConfig(cfg, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))); got.Docker == nil {
		t.Error("an unset variable must leave the config alone")
	}
}
