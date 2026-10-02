// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestRuntimeAPI_HTTP drives the operate endpoints through the REAL api.Server
// (auth + tenant resolution + RBAC + route mounting), proving the wiring rather
// than the lifecycle logic (which the white-box tests cover).
func TestWorkspaceAPIDefaultHasNoDLPLabel(t *testing.T) {
	h := newHarness(t, New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "workspace-default")
	r := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{
		"root_path": t.TempDir(),
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("register = %d %s", r.code, r.raw)
	}
	if got := r.body["dlp_mode"]; got != "off" {
		t.Fatalf("unlabeled folder dlp_mode = %v; want off", got)
	}
	ref := r.body["workspace_ref"].(string)
	r = h.do("GET", "/v1/m/sessions/workspaces/"+ref, admin, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["dlp_mode"] != "off" {
		t.Fatalf("saved unlabeled folder = %d %s", r.code, r.raw)
	}
}

func TestRuntimeAPI_HTTP(t *testing.T) {
	fr := &fakeRunner{initSID: "sess-h"}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(fr), WithCredentialSource(staticCred()))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	tenantB := h.createOrg(admin, "globex")

	// Register a workspace (admin tier), then launch against it (ref→path).
	wr := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{
		"root_path": t.TempDir(), "name": "ws-http",
	}, tenantHdr(tenantA))
	if wr.code != http.StatusCreated {
		t.Fatalf("workspace create = %d %s", wr.code, wr.raw)
	}
	wsRef, _ := wr.body["workspace_ref"].(string)
	if wsRef == "" {
		t.Fatalf("workspace_ref missing: %s", wr.raw)
	}

	// Create (write tier).
	r := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{
		"transport": "stream-json", "permission_mode": "default", "isolation": "native",
		"workspace_ref": wsRef, "name": "via-http",
	}, tenantHdr(tenantA))
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	ref, _ := r.body["run_ref"].(string)
	if ref == "" || r.body["state"] != stateRunning {
		t.Fatalf("create response: %s", r.raw)
	}

	// Get (read tier).
	if r := h.do("GET", "/v1/m/sessions/runs/"+ref, admin, tenantHdr(tenantA)); r.code != http.StatusOK {
		t.Fatalf("get = %d %s", r.code, r.raw)
	}

	// Lifecycle ledger is queryable.
	r = h.do("GET", "/v1/m/sessions/runs/"+ref+"/events", admin, tenantHdr(tenantA))
	if r.code != http.StatusOK {
		t.Fatalf("events = %d %s", r.code, r.raw)
	}
	if items, _ := r.body["items"].([]any); len(items) < 2 {
		t.Fatalf("events items = %d, want >=2 (created, launched): %s", len(items), r.raw)
	}

	// Validation: a bad permission mode is rejected at the API.
	if r := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{
		"transport": "stream-json", "permission_mode": "nonsense", "isolation": "native",
	}, tenantHdr(tenantA)); r.code != http.StatusBadRequest {
		t.Errorf("bad permission_mode = %d, want 400", r.code)
	}

	// Tenant isolation: the same ref does not exist under another tenant.
	if r := h.do("GET", "/v1/m/sessions/runs/"+ref, admin, tenantHdr(tenantB)); r.code != http.StatusNotFound {
		t.Errorf("cross-tenant get = %d, want 404", r.code)
	}

	// Unauthenticated.
	if r := h.do("GET", "/v1/m/sessions/runs", "", tenantHdr(tenantA)); r.code != http.StatusUnauthorized {
		t.Errorf("no-auth = %d, want 401", r.code)
	}

	// Stop (write tier) → terminal.
	if r := h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/stop", admin, nil, tenantHdr(tenantA)); r.code != http.StatusOK {
		t.Fatalf("stop = %d %s", r.code, r.raw)
	}

	// Attach AFTER stop returns the buffered tail + an end marker and completes
	// (the ring is closed, so the SSE handler does not block).
	r = h.do("GET", "/v1/m/sessions/runs/"+ref+"/attach", admin, tenantHdr(tenantA))
	if r.code != http.StatusOK {
		t.Fatalf("attach = %d", r.code)
	}
	if !strings.Contains(r.raw, "event: end") {
		t.Errorf("attach stream did not terminate with an end event: %q", r.raw)
	}
}

// The process runner is the external boundary; approval and run lifetime are
// exercised through the same HTTP API a disconnected CLI leaves behind.
type controlledLaunchApproval struct {
	approved   atomic.Bool
	outcome    atomic.Pointer[string]
	onApproved func(context.Context, LaunchIntent) error
}

func (g *controlledLaunchApproval) Authorize(ctx context.Context, _ model.TenantID, intent LaunchIntent) (LaunchDecision, error) {
	if g.approved.Load() {
		if g.onApproved != nil {
			if err := g.onApproved(ctx, intent); err != nil {
				return LaunchDecision{}, err
			}
		}
		return LaunchDecision{Allowed: true, RecordIO: true, Critical: true, ApprovalRef: "test-approval"}, nil
	}
	return LaunchDecision{DeniedStatus: http.StatusAccepted, Reason: "waiting for human approval", ApprovalRef: "test-approval", Critical: true, RecordIO: true}, nil
}

func TestRuntimeAPIApprovalWaitRestoresAuthenticatedLauncher(t *testing.T) {
	gate := &controlledLaunchApproval{}
	runner := &fakeRunner{initSID: "approved-session"}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred()), WithLaunchGate(gate))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "credential-handoff")
	issuer := auth.NewAuthenticator(h.st, nil)
	m.UseQueuedCredentialCapture(issuer.BindQueuedCredential)
	m.UseQueuedLaunchAuthorization(func(ctx context.Context, _ model.TenantID, credential auth.QueuedCredential, _ string, _ model.ID) (auth.Principal, error) {
		return issuer.RevalidateQueuedCredential(ctx, credential)
	})
	checked := make(chan error, 1)
	gate.onApproved = func(ctx context.Context, intent LaunchIntent) error {
		principal := intent.LauncherPrincipal
		_, bound := principal.Ref()
		var err error
		if !bound || principal.Actor() != intent.Actor {
			err = auth.ErrUnauthenticated
		} else {
			// This is the same exact-reference check the one session credential
			// issuer performs. An unbound reconstruction cannot mint a credential.
			current, currentErr := issuer.Authenticate(ctx, admin)
			err = currentErr
			got, gotBound := principal.Ref()
			want, wantBound := current.Ref()
			if err == nil && (!gotBound || !wantBound || got != want) {
				err = auth.ErrUnauthenticated
			}
		}
		checked <- err
		return err
	}
	response := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{"name": "pending"}, tenantHdr(tenant))
	if response.code != http.StatusAccepted {
		t.Fatalf("create=%d %s", response.code, response.raw)
	}
	gate.approved.Store(true)
	select {
	case err := <-checked:
		if err != nil {
			t.Fatalf("approved launch lost the authenticated launcher: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("approved launch did not reach credential provisioning")
	}
	ref := response.body["run_ref"].(string)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response = h.do("GET", "/v1/m/sessions/runs/"+ref, admin, tenantHdr(tenant))
		if response.body["state"] == stateRunning && launchCount(runner) == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("approved credential handoff did not launch: %s", response.raw)
}

func TestRuntimeAPIImmediateLaunchCarriesAuthenticatedLauncher(t *testing.T) {
	gate := &controlledLaunchApproval{}
	gate.approved.Store(true)
	gate.onApproved = func(ctx context.Context, intent LaunchIntent) error {
		requester, present := api.RequestPrincipal(ctx)
		got, bound := intent.LauncherPrincipal.Ref()
		want, requestBound := requester.Ref()
		if !present || !bound || !requestBound || got != want || intent.LauncherPrincipal.Actor() != intent.Actor {
			return auth.ErrUnauthenticated
		}
		encoded, err := json.Marshal(intent)
		if err != nil {
			return err
		}
		var persisted map[string]any
		if err := json.Unmarshal(encoded, &persisted); err != nil {
			return err
		}
		if _, leaked := persisted["LauncherPrincipal"]; leaked {
			return auth.ErrUnauthenticated
		}
		return nil
	}
	runner := &fakeRunner{initSID: "immediate-session"}
	h := newHarness(t, New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred()), WithLaunchGate(gate)))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "immediate-credential")
	response := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{"name": "immediate"}, tenantHdr(tenant))
	if response.code != http.StatusCreated || response.body["state"] != stateRunning || launchCount(runner) != 1 {
		t.Fatalf("immediate credential handoff did not launch: %d %s", response.code, response.raw)
	}
}
func (g *controlledLaunchApproval) ApprovalStatus(context.Context, model.TenantID, LaunchIntent, string) (string, error) {
	if status := g.outcome.Load(); status != nil {
		return *status, nil
	}
	if g.approved.Load() {
		return "approved", nil
	}
	return "pending", nil
}
func TestRuntimeAPIApprovalWaitOutlivesRequest(t *testing.T) {
	gate := &controlledLaunchApproval{}
	runner := &fakeRunner{initSID: "approved-real-session"}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred()), WithLaunchGate(gate))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "approval-wait")
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	request := httptest.NewRequest("POST", "/v1/m/sessions/runs", bytes.NewBufferString(`{"name":"review this launch"}`)).WithContext(requestCtx)
	request.RemoteAddr = "10.0.0.1:1234"
	request.Header.Set("Authorization", "Bearer "+admin)
	request.Header.Set("X-Olivares-Tenant", tenant.String())
	response := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(response, request)
	cancelRequest()
	result := resp{code: response.Code, raw: response.Body.String()}
	_ = json.Unmarshal(response.Body.Bytes(), &result.body)
	if result.code != http.StatusAccepted || result.body["state"] != "waiting_approval" {
		t.Fatalf("pending launch = %d %s; want 202 with waiting run", result.code, result.raw)
	}
	ref, _ := result.body["run_ref"].(string)
	if ref == "" || result.body["approval_ref"] != "test-approval" {
		t.Fatalf("waiting run references = %s", result.raw)
	}
	if result.body["pid"] != nil || result.body["credential_id"] != nil {
		t.Fatalf("unapproved run has process or credential: %s", result.raw)
	}
	gate.approved.Store(true)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		result = h.do("GET", "/v1/m/sessions/runs/"+ref, admin, tenantHdr(tenant))
		if result.code == http.StatusOK && result.body["state"] == "running" {
			if result.body["record_io"] != true || result.body["approval_ref"] != "test-approval" {
				t.Fatalf("approved governance facts = %s", result.raw)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("approved run did not start automatically: %d %s", result.code, result.raw)
}

func TestRuntimeAPIApprovalTerminalDecisionNeverLaunches(t *testing.T) {
	for _, status := range []string{"rejected", "expired"} {
		t.Run(status, func(t *testing.T) {
			gate := &controlledLaunchApproval{}
			runner := &fakeRunner{initSID: "must-not-start"}
			m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred()), WithLaunchGate(gate))
			h := newHarness(t, m)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "terminal-approval")
			response := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{"name": "pending"}, tenantHdr(tenant))
			if response.code != http.StatusAccepted {
				t.Fatalf("create=%d %s", response.code, response.raw)
			}
			ref := response.body["run_ref"].(string)
			gate.outcome.Store(&status)
			want := stateDeclined
			if status == "expired" {
				want = stateExpired
			}
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				response = h.do("GET", "/v1/m/sessions/runs/"+ref, admin, tenantHdr(tenant))
				if response.body["state"] == want {
					if response.body["reason"] == "" || launchCount(runner) != 0 {
						t.Fatalf("terminal decision=%s, launches=%d", response.raw, launchCount(runner))
					}
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatalf("terminal decision not observed: %s", response.raw)
		})
	}
}

func TestRuntimeAPIApprovalWaitRechecksRevokedCredential(t *testing.T) {
	gate := &controlledLaunchApproval{}
	runner := &fakeRunner{initSID: "must-not-start"}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred()), WithLaunchGate(gate))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "revoked-approval")
	issuer := auth.NewAuthenticator(h.st, nil)
	m.UseQueuedCredentialCapture(issuer.BindQueuedCredential)
	m.UseQueuedLaunchAuthorization(func(ctx context.Context, _ model.TenantID, credential auth.QueuedCredential, _ string, _ model.ID) (auth.Principal, error) {
		return issuer.RevalidateQueuedCredential(ctx, credential)
	})
	response := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{"name": "pending"}, tenantHdr(tenant))
	if response.code != http.StatusAccepted {
		t.Fatalf("create=%d %s", response.code, response.raw)
	}
	ref := response.body["run_ref"].(string)
	if response := h.doJSON("POST", "/v1/auth/logout", admin, nil, nil); response.code != http.StatusNoContent {
		t.Fatalf("logout=%d %s", response.code, response.raw)
	}
	login := h.doJSON("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, nil)
	fresh := login.body["token"].(string)
	gate.approved.Store(true)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response = h.do("GET", "/v1/m/sessions/runs/"+ref, fresh, tenantHdr(tenant))
		if response.body["state"] == stateDeclined {
			if launchCount(runner) != 0 {
				t.Fatal("revoked credential launched")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("revoked credential was not refused: %s", response.raw)
}
