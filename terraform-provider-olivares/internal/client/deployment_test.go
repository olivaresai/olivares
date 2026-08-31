// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sampleDeploymentDTO is a representative definition DTO from the fake server.
func sampleDeploymentDTO(id string) map[string]any {
	return map[string]any{
		"id": id, "subject_kind": "agent", "subject_ref": "acme-bot", "name": "billing-agent",
		"environment": "prod", "target": "docker.host/node1", "runtime": "docker",
		"desired_status": "active", "current_version": float64(1), "applied_version": float64(0),
		"spec_hash": "abc123", "source_ref": "git:repo#deadbeef",
	}
}

func TestCreateDeployment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/m/deploy/definitions" {
			t.Errorf("got %s %s, want POST /v1/m/deploy/definitions", r.Method, r.URL.Path)
		}
		assertCommonHeaders(t, r, testTenant)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["subject_kind"] != "agent" || body["spec"] == nil {
			t.Errorf("create body missing subject_kind/spec: %v", body)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(sampleDeploymentDTO("dep-1"))
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	got, err := c.CreateDeployment(context.Background(), "", Deployment{
		SubjectKind: "agent", SubjectRef: "acme-bot", Name: "billing-agent", Environment: "prod",
		Target: "docker.host/node1", Runtime: "docker", Spec: json.RawMessage(`{"image":"img:1"}`),
	})
	if err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	if got.ID != "dep-1" || got.CurrentVersion != 1 {
		t.Errorf("unexpected deployment: %+v", got)
	}
}

// TestDeleteDeploymentWhileApplied verifies the engine's 409 (retire-first) is
// surfaced as an error rather than swallowed.
func TestDeleteDeploymentWhileApplied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "conflict", "message": "deployment is still applied"}})
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	err := c.DeleteDeployment(context.Background(), "", "dep-1")
	if err == nil || !strings.Contains(err.Error(), "still applied") {
		t.Fatalf("DeleteDeployment error = %v, want a 409 'still applied' error", err)
	}
}
