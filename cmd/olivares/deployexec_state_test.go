// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime/executor"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/deploy"
)

func TestDeploySavedExecutorBootAndTenantCredential(t *testing.T) {
	ctx := context.Background()
	eng, token, tenant := wireBoot(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, t.TempDir())
	request := func(method, path string, body any, want int) map[string]any {
		t.Helper()
		code, out, raw := doDemoViewJSON(t, eng.api.Handler(), method, path, token, tenant.String(), body)
		if code != want {
			t.Fatalf("%s %s = %d %s, want %d", method, path, code, raw, want)
		}
		if strings.Contains(raw, "synthetic-deploy-lease") {
			t.Fatal("response exposed the credential")
		}
		return out
	}
	request("PUT", "/v1/m/deploy/executor/config", map[string]any{"credential_ref": "store:env/DEPLOY_LEASE"}, http.StatusOK)
	readiness := request("GET", "/v1/m/deploy/executor", nil, http.StatusOK)
	if readiness["configured"] != true {
		t.Fatal("production boot did not wire saved executor setup")
	}
	code, _, raw := doDemoViewJSON(t, eng.api.Handler(), "POST", "/v1/m/deploy/executor/test", token, tenant.String(), nil)
	if code != http.StatusBadGateway || !strings.Contains(raw, "credential reference could not be resolved") {
		t.Fatalf("missing credential test = %d %s", code, raw)
	}
	request("PUT", "/v1/console/secrets?scope=tenant", map[string]any{"name": "env/DEPLOY_LEASE", "value": "synthetic-deploy-lease"}, http.StatusOK)

	socket := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	calls := make(chan string, 4)
	daemon := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.Close {
			t.Error("request-owned backend retained a keepalive connection")
		}
		calls <- r.Method + " " + r.URL.Path
		if r.Header.Get("Authorization") != "" {
			t.Error("local Docker received secret material")
		}
		switch r.URL.Path {
		case "/_ping":
			_, _ = w.Write([]byte("OK"))
		case "/containers/json":
			_, _ = w.Write([]byte("[]"))
		default:
			t.Error("unexpected daemon request")
			w.WriteHeader(http.StatusBadRequest)
		}
	})}
	go func() { _ = daemon.Serve(listener) }()
	t.Cleanup(func() { _ = daemon.Close() })
	cfg := deploy.ExecutorConfig{SocketPath: socket, CredentialRef: "store:env/DEPLOY_LEASE"}
	setup := &deployExecutorSetup{secrets: eng.secretStore, log: quietLog()}
	if err := setup.Test(ctx, tenant, cfg); err != nil {
		t.Fatal(err)
	}
	if got := <-calls; got != "GET /_ping" {
		t.Fatalf("test = %q", got)
	}
	adapter, err := setup.Build(ctx, tenant, cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := deploy.ExecRequest{Tenant: tenant, Runtime: "docker", SubjectRef: "setup-agent", Environment: "stage"}
	req.Spec.Image = "setup-agent:1"
	if changes, err := adapter.Plan(ctx, req); err != nil || len(changes) != 1 {
		t.Fatalf("production executor Plan = %v %v", changes, err)
	}
	if got := <-calls; got != "GET /containers/json" {
		t.Fatalf("plan = %q", got)
	}
	if err := setup.Test(ctx, model.TenantID(model.NewID()), cfg); err == nil {
		t.Fatal("another tenant consumed this credential")
	}
	if _, err := setup.credentialSource(tenant, deploy.ExecutorConfig{CredentialRef: "file:/etc/shadow"}).Mint(ctx, executor.MintRequest{}); err == nil {
		t.Fatal("arbitrary file credential accepted")
	}
	select {
	case got := <-calls:
		t.Fatalf("refused credential reached the daemon: %q", got)
	default:
	}
}
