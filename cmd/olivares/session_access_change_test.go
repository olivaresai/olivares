// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

func TestSessionGroupClosureChangeStopsCleanlyAndResumeUsesCurrentAccess(t *testing.T) {
	runSessionAccessChange(t, false)
}

func TestSessionAccessChangeDoesNotWaitForItsOwnSessionCall(t *testing.T) {
	runSessionAccessChange(t, true)
}

func runSessionAccessChange(t *testing.T, activeCall bool) {
	h := newHarness(t)
	tenant := model.TenantID(h.tenantA)
	admin, err := h.authr.Authenticate(t.Context(), h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	userToken := h.newUser("access-change@e2e.test", "access-change-password", h.tenantA, auth.RoleAdmin)
	user, err := h.authr.Authenticate(t.Context(), userToken)
	if err != nil {
		t.Fatal(err)
	}
	group, err := h.authr.SCIMCreateGroup(t.Context(), admin, tenant, auth.SCIMGroupInput{DisplayName: "Original group", Members: []model.ID{user.UserID}})
	if err != nil {
		t.Fatal(err)
	}
	m := h.set.sessions
	sessions.WithRunner(approvalProjectionRunner{})(m)
	m.UseExecutionEnvironmentRef("access-change-test")
	credentials := newSessionHookCredentials(h.authr, h.st, m, h.set.gov)
	var token string
	sessions.WithLaunchGate(approvalProjectionLaunchGate(func(ctx context.Context, tenant model.TenantID, intent sessions.LaunchIntent) (sessions.LaunchDecision, error) {
		var err error
		token, err = credentials.mint(ctx, tenant, intent)
		return sessions.LaunchDecision{Allowed: err == nil}, err
	}))(m)
	var profile struct {
		Ref string `json:"profile_ref"`
	}
	if code := h.reqInto(http.MethodPost, "/v1/m/sessions/provider-profiles", h.adminToken, h.tenantA, map[string]any{"driver": "claude", "auth_source": "provider_account_home", "config_home": t.TempDir(), "user_home": t.TempDir()}, &profile); code != http.StatusCreated {
		t.Fatalf("profile=%d", code)
	}
	var run struct {
		Ref string `json:"run_ref"`
	}
	if code := h.reqInto(http.MethodPost, "/v1/m/sessions/runs", userToken, h.tenantA, map[string]any{"transport": "stream-json", "permission_mode": "plan", "isolation": "native", "provider_profile_ref": profile.Ref}, &run); code != http.StatusCreated {
		t.Fatalf("launch=%d", code)
	}
	t.Cleanup(func() { h.req(http.MethodPost, "/v1/m/sessions/runs/"+run.Ref+"/stop", h.adminToken, h.tenantA, nil) })
	originalToken := token
	principal, scope, err := credentials.Resolve(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	// A directory version or label change with the same subjects must not stop a run.
	if _, err = h.authr.SCIMReplaceGroup(t.Context(), admin, tenant, group.Group.ID, auth.SCIMGroupInput{DisplayName: "Renamed group", Members: []model.ID{user.UserID}}, group.Group.Version); err != nil {
		t.Fatal(err)
	}
	if _, _, err = credentials.Resolve(t.Context(), token); err != nil {
		t.Fatal("same group closure revoked the session")
	}
	var view map[string]any
	h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+run.Ref, userToken, h.tenantA, nil, &view)
	if view["process_state"] != "running" {
		t.Fatalf("same closure process=%v", view["process_state"])
	}
	if _, err = h.authr.SCIMCreateGroup(t.Context(), admin, tenant, auth.SCIMGroupInput{DisplayName: "New restriction subjects", Members: []model.ID{user.UserID}}); err != nil {
		t.Fatal(err)
	}
	if activeCall {
		callCtx, endCall, callErr := m.BeginSessionCall(t.Context(), principal)
		if callErr != nil {
			t.Fatal(callErr)
		}
		defer endCall()
		done := make(chan error, 1)
		go func() { _, _, err := credentials.Resolve(callCtx, originalToken); done <- err }()
		select {
		case err = <-done:
			endCall()
		case <-time.After(time.Second):
			endCall()
			<-done
			t.Fatal("access-change resolution waited for its own session call to return")
		}
	} else {
		_, _, err = credentials.Resolve(t.Context(), originalToken)
	}
	if err == nil {
		t.Fatal("changed group closure retained launch authority")
	}
	want := "Access changed for " + user.DisplayName + "; resume to continue with the new access"
	deadline := time.Now().Add(3 * time.Second)
	for {
		view = nil
		h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+run.Ref, userToken, h.tenantA, nil, &view)
		if view["state"] == "stopped" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("access-change teardown did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if view["state"] != "stopped" || view["reason"] != want || view["pid"] != nil {
		t.Fatalf("access change did not stop cleanly with its reason: %v", view)
	}
	var events struct {
		Items []struct {
			Event  string `json:"event"`
			Detail string `json:"detail"`
		} `json:"items"`
	}
	if code := h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+run.Ref+"/events", userToken, h.tenantA, nil, &events); code != http.StatusOK {
		t.Fatalf("events=%d", code)
	}
	terminal := 0
	for _, event := range events.Items {
		if event.Event == "stopped" {
			terminal++
			if event.Detail != want {
				t.Fatalf("terminal detail=%q", event.Detail)
			}
		}
	}
	if terminal != 1 {
		t.Fatalf("terminal events=%d", terminal)
	}
	if _, _, err = credentials.Resolve(t.Context(), originalToken); err == nil {
		t.Fatal("revoked generation revived")
	}
	if code, _ := h.req(http.MethodPost, "/v1/m/sessions/runs/"+run.Ref+"/resume", userToken, h.tenantA, nil); code != http.StatusOK {
		t.Fatalf("resume=%d", code)
	}
	resumed, _, err := credentials.Resolve(t.Context(), token)
	if err != nil || token == originalToken || len(resumed.GroupsIn(tenant)) != 2 {
		t.Fatalf("resume did not re-mint current closure: groups=%d err=%v", len(resumed.GroupsIn(tenant)), err)
	}
	if err = m.StopForAccessChange(t.Context(), scope, user.DisplayName); err == nil {
		t.Fatal("old scope could stop a resumed generation")
	}
	if _, _, err = credentials.Resolve(t.Context(), originalToken); err == nil {
		t.Fatal("old credential revived after resume")
	}
	view = nil
	h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+run.Ref, userToken, h.tenantA, nil, &view)
	if view["process_state"] != "running" {
		t.Fatalf("resumed process=%v", view["process_state"])
	}
}
