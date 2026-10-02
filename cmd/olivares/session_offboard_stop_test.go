// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

func TestSCIMDeleteStopsEveryOwnedIdleSessionAndKeepsOtherOwnerRunning(t *testing.T) {
	runSessionOwnerOffboard(t, "scim_delete", 5*time.Millisecond, 2*time.Second)
}

func TestDirectoryDisableStopsEveryOwnedIdleSessionAndKeepsOtherOwnerRunning(t *testing.T) {
	runSessionOwnerOffboard(t, "directory_disable", 5*time.Millisecond, 2*time.Second)
}

func TestGlobalDirectoryDisableStopsEveryOwnedIdleSessionAndKeepsOtherOwnerRunning(t *testing.T) {
	runSessionOwnerOffboard(t, "global_directory_disable", 5*time.Millisecond, 2*time.Second)
}

func TestStandingRevokeStopsEveryOwnedIdleSessionAndKeepsOtherOwnerRunning(t *testing.T) {
	runSessionOwnerOffboard(t, "standing_revoke", 5*time.Millisecond, 2*time.Second)
}

func TestOwnerOffboardStopAuditBindsNonSensitiveCause(t *testing.T) {
	runSessionOwnerOffboard(t, "scim_delete", 5*time.Millisecond, 2*time.Second)
}

func TestOwnerOffboardStopRefusesForeignScopesAndResumedSuccessor(t *testing.T) {
	runSessionOwnerOffboard(t, "foreign_successor", 5*time.Millisecond, 2*time.Second)
}

func TestOwnerOffboardAuditCauseSurvivesGracefulStopAdmissionFailure(t *testing.T) {
	runSessionOwnerOffboard(t, "ended_fallback", 5*time.Millisecond, 2*time.Second)
}

func TestOwnerStandingSweepStaysActiveWithEmergencySweepDisabled(t *testing.T) {
	runSessionOwnerOffboard(t, "scim_delete", 0, 7*time.Second)
}

func TestOwnerStandingFailureDoesNotPoisonOtherLiveChecks(t *testing.T) {
	runSessionOwnerOffboard(t, "unavailable", 5*time.Millisecond, 7*time.Second)
}

func TestOwnerSweepPreservesNonMultipleEmergencyStopCadence(t *testing.T) {
	runSessionOwnerOffboard(t, "kill_cadence", 6*time.Second, 7500*time.Millisecond)
}

func TestOwnerStandingReadCannotDelayEmergencyStopCheck(t *testing.T) {
	runSessionOwnerOffboard(t, "kill_slow", 6*time.Second, 7500*time.Millisecond)
}

func TestOwnerOffboardReasonSurvivesExpiredCheckContext(t *testing.T) {
	runSessionOwnerOffboard(t, "ended_deadline", 5*time.Millisecond, 7*time.Second)
}

type ownerCadenceStopGate struct {
	armed atomic.Bool
	seen  chan time.Time
}

func (g *ownerCadenceStopGate) Check(context.Context, model.TenantID, sessions.StopDims) (sessions.StopDecision, error) {
	if g.armed.Load() {
		select {
		case g.seen <- time.Now():
		default:
		}
	}
	return sessions.StopDecision{}, nil
}

func runSessionOwnerOffboard(t *testing.T, cause string, interval, wait time.Duration) {
	h := newHarness(t)
	ownerToken := h.newUser("offboard-owner@e2e.test", "offboard-owner-password", h.tenantA, auth.RoleAdmin)
	owner, err := h.authr.Authenticate(t.Context(), ownerToken)
	if err != nil {
		t.Fatal(err)
	}
	otherToken := h.newUser("offboard-other@e2e.test", "offboard-other-password", h.tenantA, auth.RoleAdmin)
	m := h.set.sessions
	sessions.WithRunner(approvalProjectionRunner{})(m)
	sessions.WithKillSwitchSweep(interval)(m)
	m.EnableProfiledLaunches()
	m.UseExecutionEnvironmentRef("offboard-test")
	var gate *ownerCadenceStopGate
	if cause == "kill_cadence" || cause == "kill_slow" {
		gate = &ownerCadenceStopGate{seen: make(chan time.Time, 1)}
		sessions.WithStopGate(gate)(m)
	}
	credentials := newSessionHookCredentials(h.authr, h.st, m, h.set.gov)
	m.UseLaunchGate(approvalProjectionLaunchGate(func(ctx context.Context, tenant model.TenantID, intent sessions.LaunchIntent) (sessions.LaunchDecision, error) {
		_, err := credentials.mint(ctx, tenant, intent)
		return sessions.LaunchDecision{Allowed: err == nil}, err
	}))
	var profile struct {
		Ref string `json:"profile_ref"`
	}
	if code := h.reqInto(http.MethodPost, "/v1/m/sessions/provider-profiles", h.adminToken, h.tenantA, map[string]any{"driver": "claude", "auth_source": "provider_account_home", "config_home": t.TempDir(), "user_home": t.TempDir()}, &profile); code != http.StatusCreated {
		t.Fatalf("profile=%d", code)
	}
	launch := func(token string) string {
		t.Helper()
		var run struct {
			Ref string `json:"run_ref"`
		}
		if code := h.reqInto(http.MethodPost, "/v1/m/sessions/runs", token, h.tenantA, map[string]any{"transport": "stream-json", "permission_mode": "plan", "isolation": "native", "provider_profile_ref": profile.Ref}, &run); code != http.StatusCreated {
			t.Fatalf("launch=%d", code)
		}
		t.Cleanup(func() { h.req(http.MethodPost, "/v1/m/sessions/runs/"+run.Ref+"/stop", h.adminToken, h.tenantA, nil) })
		return run.Ref
	}
	owned := []string{launch(ownerToken), launch(ownerToken)}
	other := launch(otherToken)

	var selected chan string
	if cause == "unavailable" {
		selected = make(chan string, 1)
		var first atomic.Bool
		m.UseSessionAccessCheck(func(ctx context.Context, tenant model.TenantID, ref string) (auth.SessionScope, string, error) {
			if first.CompareAndSwap(false, true) {
				selected <- ref
				<-ctx.Done()
				return auth.SessionScope{}, "", ctx.Err()
			}
			return credentials.CheckOwnerAccess(ctx, tenant, ref)
		})
	}
	if gate != nil {
		gate.armed.Store(true)
	}
	if cause == "kill_slow" {
		m.UseSessionAccessCheck(func(ctx context.Context, tenant model.TenantID, ref string) (auth.SessionScope, string, error) {
			select {
			case <-time.After(4 * time.Second):
			case <-ctx.Done():
				return auth.SessionScope{}, "", ctx.Err()
			}
			return credentials.CheckOwnerAccess(ctx, tenant, ref)
		})
	}
	if cause == "ended_deadline" {
		var first atomic.Bool
		m.UseSessionAccessCheck(func(ctx context.Context, tenant model.TenantID, ref string) (auth.SessionScope, string, error) {
			scope, user, err := credentials.CheckOwnerAccess(ctx, tenant, ref)
			if errors.Is(err, auth.ErrSessionAccessEnded) && first.CompareAndSwap(false, true) {
				<-ctx.Done()
			}
			return scope, user, err
		})
	}
	if cause == "ended_fallback" {
		m.UseSessionAccessCheck(func(ctx context.Context, tenant model.TenantID, ref string) (auth.SessionScope, string, error) {
			scope, user, err := credentials.CheckOwnerAccess(ctx, tenant, ref)
			if errors.Is(err, auth.ErrSessionAccessEnded) {
				// The native issuer proves the withdrawal, while a deliberately
				// mismatched runtime admission forces the captured-handle fallback.
				scope.WorkspaceID = model.NewID()
			}
			return scope, user, err
		})
	}
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if cause == "foreign_successor" {
		assertAccessEndedScopeAndSuccessor(t, h, credentials, m, ownerToken, owned[0], owned[1], other)
		return
	}
	if cause == "kill_cadence" || cause == "kill_slow" {
		select {
		case <-gate.seen:
		case <-time.After(wait):
			t.Fatal("existing emergency-stop cadence rounded up by the owner schedule")
		}
		return
	}
	if cause == "unavailable" {
		failed := <-selected
		deadline := time.Now().Add(wait)
		for {
			var view map[string]any
			h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+failed, h.adminToken, h.tenantA, nil, &view)
			if view["state"] == "stopped" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("unavailable standing did not fail closed")
			}
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(30 * time.Millisecond)
		for _, ref := range append(owned, other) {
			if ref == failed {
				continue
			}
			var view map[string]any
			h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+ref, h.adminToken, h.tenantA, nil, &view)
			if view["process_state"] != "running" {
				t.Fatalf("expired check poisoned another run: %v", view["process_state"])
			}
		}
		return
	}
	switch cause {
	case "scim_delete", "ended_deadline", "ended_fallback":
		if code, _ := h.req(http.MethodDelete, "/v1/scim/v2/Users/"+owner.UserID.String(), h.adminToken, h.tenantA, nil); code != http.StatusNoContent {
			t.Fatalf("SCIM DELETE=%d", code)
		}
	case "directory_disable":
		body := map[string]any{"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"}, "Operations": []map[string]any{{"op": "replace", "value": map[string]any{"active": false}}}}
		if code, _ := h.req(http.MethodPatch, "/v1/scim/v2/Users/"+owner.UserID.String(), h.adminToken, h.tenantA, body); code != http.StatusOK {
			t.Fatalf("directory disable=%d", code)
		}
	case "global_directory_disable":
		// The public auth store is the directory reconciler's resolved account
		// lifecycle seam. This is a disabled-account fixture, not an AD/LDAP wire test.
		if err := h.st.AuthMutate(t.Context(), func(as store.AuthScope) error {
			user, err := as.Users().Get(t.Context(), owner.UserID)
			if err != nil {
				return err
			}
			user.Status = model.StatusInactive
			_, err = as.Users().Update(t.Context(), user)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	case "standing_revoke":
		admin, err := h.authr.Authenticate(t.Context(), h.adminToken)
		if err != nil {
			t.Fatal(err)
		}
		if err = h.st.AuthMutate(t.Context(), func(as store.AuthScope) error {
			_, err := h.authr.OffboardFromTenant(t.Context(), as, admin, owner.UserID, model.TenantID(h.tenantA), "standing_withdrawal_test")
			return err
		}); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("unknown offboard cause")
	}
	// No hook, approval or session-credential resolution drives this lifecycle check.
	committed := time.Now()
	deadline := time.Now().Add(wait)
	for _, ref := range owned {
		var view map[string]any
		for {
			view = nil
			if code := h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+ref, h.adminToken, h.tenantA, nil, &view); code != http.StatusOK {
				t.Fatalf("run=%d", code)
			}
			if view["state"] == "stopped" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("offboarded idle run remains %v (process=%v)", view["state"], view["process_state"])
			}
			time.Sleep(10 * time.Millisecond)
		}
		want := "Access ended for " + owner.DisplayName
		if view["reason"] != want || view["pid"] != nil {
			t.Fatalf("unclean offboard stop: reason=%v pid=%v", view["reason"], view["pid"])
		}

		var events struct {
			Items []struct {
				Event  string `json:"event"`
				Detail string `json:"detail"`
			} `json:"items"`
		}
		if code := h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+ref+"/events", h.adminToken, h.tenantA, nil, &events); code != http.StatusOK {
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
		t.Logf("owner access-ended stop: cause=%s elapsed=%s", cause, time.Since(committed))
		assertOwnerEndedAudit(t, h.st, model.TenantID(h.tenantA), ref, owner)
		if code, _ := h.req(http.MethodPost, "/v1/m/sessions/runs/"+ref+"/resume", ownerToken, h.tenantA, nil); code != http.StatusUnauthorized && code != http.StatusForbidden && code != http.StatusNotFound {
			t.Fatalf("offboarded owner resume=%d", code)
		}
	}
	var otherView map[string]any
	h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+other, h.adminToken, h.tenantA, nil, &otherView)
	if otherView["process_state"] != "running" {
		t.Fatalf("other owner's process=%v", otherView["process_state"])
	}
	if current, err := h.authr.Authenticate(t.Context(), ownerToken); err == nil {
		// An account-scope login may still identify the user in another tenant. It
		// cannot recover withdrawn standing or launch/resume in this tenant.
		if _, admitted := current.RoleIn(model.TenantID(h.tenantA)); admitted && !current.ExcludedFrom(model.TenantID(h.tenantA)) {
			t.Fatal("offboarded owner retained tenant standing")
		}
	}
}

func assertAccessEndedScopeAndSuccessor(t *testing.T, h *harness, credentials *sessionHookCredentials, m *sessions.Module, ownerToken, ref, otherOwned, otherOwner string) {
	t.Helper()
	_, original, err := credentials.ResolveRun(t.Context(), model.TenantID(h.tenantA), ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*auth.SessionScope)
	}{
		{"tenant", func(s *auth.SessionScope) { s.TenantID = model.TenantID(h.tenantB) }},
		{"workspace", func(s *auth.SessionScope) { s.WorkspaceID = model.NewID() }},
		{"session", func(s *auth.SessionScope) { s.SessionRef = model.NewID().String() }},
		{"run", func(s *auth.SessionScope) { s.RunRef = otherOwned }},
		{"holder", func(s *auth.SessionScope) { s.Holder = model.NewID().String() }},
		{"fence", func(s *auth.SessionScope) { s.Fence++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			foreign := original
			test.change(&foreign)
			if err := m.StopForAccessEnded(t.Context(), foreign, "fixture owner"); err == nil {
				t.Fatal("foreign scope stopped an owned run")
			}
		})
	}
	assertRunning := func(ref string) {
		t.Helper()
		var view map[string]any
		if code := h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+ref, h.adminToken, h.tenantA, nil, &view); code != http.StatusOK || view["process_state"] != "running" {
			t.Fatalf("unaffected run=%s status=%d process=%v", ref, code, view["process_state"])
		}
	}
	for _, ref := range []string{ref, otherOwned, otherOwner} {
		assertRunning(ref)
	}
	if code, _ := h.req(http.MethodPost, "/v1/m/sessions/runs/"+ref+"/stop", ownerToken, h.tenantA, nil); code != http.StatusOK {
		t.Fatalf("operator stop=%d", code)
	}
	if code, _ := h.req(http.MethodPost, "/v1/m/sessions/runs/"+ref+"/resume", ownerToken, h.tenantA, nil); code != http.StatusOK {
		t.Fatalf("active owner resume=%d", code)
	}
	_, current, err := credentials.ResolveRun(t.Context(), model.TenantID(h.tenantA), ref)
	if err != nil || current.Fence == original.Fence {
		t.Fatalf("resume did not rotate generation: fence=%d err=%v", current.Fence, err)
	}
	if err := m.StopForAccessEnded(t.Context(), original, "fixture owner"); err == nil {
		t.Fatal("retired generation stopped its resumed successor")
	}
	for _, ref := range []string{ref, otherOwned, otherOwner} {
		assertRunning(ref)
	}
}

func assertOwnerEndedAudit(t *testing.T, st store.Store, tenant model.TenantID, ref string, owner auth.Principal) {
	t.Helper()
	if err := st.View(t.Context(), tenant, func(sc store.Scope) error {
		reader, ok := sc.Audit().(store.VerifiedAuditAnchorReader)
		if !ok {
			t.Fatal("store lacks the verified terminal audit reader")
		}
		var stopped []model.AuditEvent
		if err := sc.Audit().Walk(t.Context(), 1, func(event model.AuditEvent) error {
			if event.Action == "sessions.run.stopped" {
				stopped = append(stopped, event)
			}
			return nil
		}); err != nil {
			return err
		}
		matching := 0
		for _, event := range stopped {
			verified, canonical, found, err := reader.ReadVerifiedAuditAnchor(t.Context(), event.Seq)
			if err != nil {
				return err
			}
			if !found || verified.ID != event.ID {
				t.Fatal("terminal audit anchor was not retained")
			}
			var meta map[string]any
			if err := json.Unmarshal([]byte(canonical), &meta); err != nil {
				return err
			}
			if meta["run_ref"] != ref {
				continue
			}
			matching++
			if meta["stop_cause"] != "owner_access_ended" {
				t.Fatalf("terminal audit omits the access-ended cause: %v", meta)
			}
			if strings.Contains(canonical, owner.DisplayName) || strings.Contains(canonical, owner.UserID.String()) {
				t.Fatal("terminal audit retained the owner's name or subject ID")
			}
		}
		if matching != 1 {
			t.Fatalf("access-ended terminal audit anchors=%d", matching)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
