// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime/executor"
	"github.com/olivaresai/olivares/modules/deploy"
)

// The target is a Docker Engine protocol fixture, without a Docker daemon or
// remote effects. This exercises the production adapter and backend together;
// scripts/test-agent-lan-journey.sh adds the real engine, approvals and CLI.
func TestDeployDockerLoopbackJourney(t *testing.T) {
	var container map[string]any
	mutations := 0
	var mu sync.Mutex
	snapshot := func() (string, int) {
		mu.Lock()
		defer mu.Unlock()
		if container == nil {
			return "absent", mutations
		}
		return container["State"].(string), mutations
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/containers/json":
			items := []map[string]any{}
			if container != nil {
				items = append(items, container)
			}
			_ = json.NewEncoder(w).Encode(items)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json") && container != nil:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Config": map[string]any{"Image": container["Image"], "Labels": container["Labels"]},
				"State":  map[string]any{"Status": container["State"]},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/containers/create":
			var spec map[string]any
			if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
				t.Errorf("decode target spec: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			container = map[string]any{"Id": "loopback-agent", "Image": spec["Image"],
				"Names": []string{"/" + r.URL.Query().Get("name")}, "Labels": spec["Labels"], "State": "created"}
			mutations++
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "loopback-agent"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start") && container != nil:
			container["State"] = "running"
			mutations++
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stop") && container != nil:
			container["State"] = "exited"
			mutations++
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && container != nil:
			container = nil
			mutations++
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer target.Close()
	backend := executor.NewDockerBackend(executor.DockerConfig{RemoteBaseURL: target.URL, RemoteInsecure: true})
	adapter := &deployExecutor{e: executor.New(executor.WithBackend(backend, "docker"),
		executor.WithCredentialSource(alwaysMint()))}
	req := deploy.ExecRequest{Tenant: model.TenantID("tenant-loopback"), Environment: "test",
		Target: "docker.host/loopback", Runtime: "docker", SubjectKind: "agent", SubjectRef: "fixture-agent"}
	req.Spec.Image = "fixture-agent:1"
	req.Spec.Command = "agent"
	ctx := context.Background()
	plan, err := adapter.Plan(ctx, req)
	_, count := snapshot()
	if err != nil || len(plan) != 1 || count != 0 {
		t.Fatalf("dry-run: changes=%v mutations=%d err=%v", plan, count, err)
	}
	for round := 0; round < 2; round++ {
		if _, err := adapter.Apply(ctx, req); err != nil {
			t.Fatalf("apply round %d: %v", round, err)
		}
		if state, _ := snapshot(); state != "running" {
			t.Fatalf("round %d did not start the target", round)
		}
		verified, err := adapter.Verify(ctx, req)
		if err != nil || len(verified.Changes) != 0 {
			t.Fatalf("connection verification: %+v err=%v", verified, err)
		}
		_, before := snapshot()
		_, err = adapter.Apply(ctx, req)
		if _, after := snapshot(); err != nil || after != before {
			t.Fatalf("idempotent apply actuated target: mutations=%d->%d err=%v", before, after, err)
		}
		_, err = adapter.Retire(ctx, req)
		if state, _ := snapshot(); err != nil || state != "absent" {
			t.Fatalf("stop round %d: target=%s err=%v", round, state, err)
		}
	}
	denied := &deployExecutor{e: executor.New(executor.WithBackend(backend, "docker"))}
	_, before := snapshot()
	_, err = denied.Apply(ctx, req)
	if _, after := snapshot(); err == nil || after != before {
		t.Fatalf("unprovisioned target actuated: mutations=%d->%d err=%v", before, after, err)
	}
}
