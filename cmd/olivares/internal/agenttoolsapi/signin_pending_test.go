// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// Reloading the page loses its flow ID. Status must recover the same safe flow
// view, and only for the selected tenant, driver and account.
func TestSignInStatusResumesPendingFlowOnlyForSelectedAccount(t *testing.T) {
	call, home := newSignInServer(t)
	const ref = "ppf_reload"
	loginHome := filepath.Join(home, "codex", ref)
	if err := os.MkdirAll(loginHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(loginHome, ".slow-login"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, flow := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "codex", "account_ref": ref})
	if code != 202 || flow["state"] != "waiting" {
		t.Fatalf("start = %d %v", code, flow)
	}
	statusPath := "/v1/m/agenttools/sign-in?driver=codex&account_ref=" + ref
	code, status := call("GET", statusPath, nil)
	if code != 200 || !reflect.DeepEqual(status["pending"], flow) {
		t.Fatalf("reload status = %d %v, want pending %v", code, status, flow)
	}
	for _, path := range []string{
		"/v1/m/agenttools/sign-in?driver=codex",
		"/v1/m/agenttools/sign-in?driver=codex&account_ref=ppf_other",
		"/v1/m/agenttools/sign-in?driver=claude&account_ref=" + ref,
		statusPath + "&tenant_id=" + model.NewID().String(),
	} {
		code, status := call("GET", path, nil)
		if _, present := status["pending"]; code != 200 || present {
			t.Fatalf("another selection recovered the flow: %s = %d %v", path, code, status)
		}
	}
	if code, _ := call("DELETE", "/v1/m/agenttools/sign-in/"+flow["id"].(string), nil); code != 200 {
		t.Fatalf("cancel = %d", code)
	}
	code, status = call("GET", statusPath, nil)
	if _, present := status["pending"]; code != 200 || present {
		t.Fatalf("cancelled flow returned as pending = %d %v", code, status)
	}
}

func TestSignInStatusPendingContainsOnlyLiveRegistryFlows(t *testing.T) {
	var module *Module
	call, home := newSignInServer(t, func(m *Module, _ string) { module = m })
	id, tenant := model.NewID(), model.TenantID(filepath.Base(home))
	for _, tc := range []struct {
		state   string
		expired bool
		pending bool
	}{
		{signInStarting, false, true},
		{signInNeedCode, false, true},
		{signInWaiting, false, true},
		{signInChecking, false, true},
		{signInDone, false, false},
		{signInFailed, false, false},
		{signInWaiting, true, false},
	} {
		t.Run(tc.state+map[bool]string{true: "_expired"}[tc.expired], func(t *testing.T) {
			expires := time.Now().Add(time.Minute)
			if tc.expired {
				expires = time.Now().Add(-time.Minute)
			}
			module.mu.Lock()
			module.signIns = map[model.ID]*SignIn{id: {ID: id, tenant: tenant, Driver: "claude", State: tc.state, expires: expires}}
			module.mu.Unlock()
			code, status := call("GET", "/v1/m/agenttools/sign-in?driver=claude", nil)
			pending, present := status["pending"].(map[string]any)
			if code != 200 || present != tc.pending {
				t.Fatalf("status = %d %v, want pending %v", code, status, tc.pending)
			}
			if tc.pending && (pending["id"] != id.String() || pending["state"] != tc.state || len(pending) != 3) {
				t.Fatalf("pending view contains different or private fields: %v", pending)
			}
		})
	}
}
