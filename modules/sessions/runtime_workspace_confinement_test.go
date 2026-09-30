// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Exercise the mounted route, real authentication/RBAC and an open live output
// ring. The public run reference deliberately differs from its primary row ID.
func TestRunHTTPWorkspaceConfinement(t *testing.T) {
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	f := newStreamConfinementFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	h := f.harness
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "run-confinement")
	var workspaceA, workspaceB model.ID
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		for _, target := range []*model.ID{&workspaceA, &workspaceB} {
			id := model.NewID()
			ws, err := sc.Workspaces().Create(context.Background(), model.Workspace{
				Name: id.String(), Slug: id.String(), Status: model.StatusActive,
			})
			if err != nil {
				return err
			}
			*target = ws.ID
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	member := func(email, role string, workspace model.ID) string {
		t.Helper()
		body := map[string]any{"email": email, "password": "run-member-pass1", "tenant": tenant.String(), "role": role}
		if !workspace.IsZero() {
			body["workspace_id"] = workspace.String()
		}
		r := h.doJSON("POST", "/v1/users", admin, body, nil)
		if r.code != http.StatusCreated {
			t.Fatalf("member = %d %s", r.code, r.raw)
		}
		r = h.doJSON("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "run-member-pass1"}, nil)
		if r.code != http.StatusOK {
			t.Fatalf("login = %d %s", r.code, r.raw)
		}
		return r.body["token"].(string)
	}
	confined := member("confined-run@test.io", auth.RoleAdmin, workspaceA)
	unconfined := member("tenant-reader@test.io", auth.RoleViewer, "")
	principal, err := auth.NewAuthenticator(h.st, nil).Authenticate(context.Background(), confined)
	if err != nil {
		t.Fatal(err)
	}
	if workspace, ok := principal.ConfinedWorkspaceIn(tenant); !ok || workspace != workspaceA || principal.Superadmin {
		t.Fatalf("fixture did not establish workspace-A confinement: %s %t", workspace, ok)
	}
	reads := &runRouteReadCounter{ModuleData: h.m.data}
	h.m.UseData(reads)
	const marker = "FOREIGN-WORKSPACE-LIVE-OUTPUT"
	runRef := model.NewID().String()
	var rowID model.ID
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(context.Background(), model.Record{
			colRunRef: runRef, colRunName: marker, colTransport: string(TransportStreamJSON),
			colPermissionMode: "default", colIsolation: string(IsolationNative),
			colState: stateRunning, colLastEventSeq: int64(0), colRunAuthzWorkspaceID: workspaceB.String(),
		})
		if err == nil {
			rowID = model.ID(rec.String(model.ColID))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if rowID.String() == runRef {
		t.Fatal("test must distinguish the public run_ref from its primary row ID")
	}
	ring := newOutputRing(8, 1024)
	ring.append(streamStdout, []byte(marker), time.Now())
	h.m.rt.putLive(&liveRun{tenant: tenant, runRef: runRef, runID: rowID, transport: TransportStreamJSON, ring: ring})
	t.Cleanup(func() { h.m.rt.dropLive(tenant, runRef) })
	ts := httptest.NewServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	attach := func(t *testing.T, token, ref string, want int) {
		t.Helper()
		before := reads.views.Load()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/v1/m/sessions/runs/"+ref+"/attach?workspace_id="+workspaceA.String(), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Olivares-Tenant", tenant.String())
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			buf := make([]byte, 512)
			n, _ := res.Body.Read(buf)
			t.Fatalf("attach status=%d want=%d first_bytes=%q", res.StatusCode, want, buf[:n])
		}
		if want == http.StatusNotFound {
			body, err := io.ReadAll(res.Body)
			if err != nil || res.Header.Get("Content-Type") == "text/event-stream" || strings.Contains(string(body), marker) || strings.Contains(string(body), "event:") || strings.Contains(string(body), ": connected") {
				t.Fatalf("denied attach exposed stream bytes: headers=%v body=%q err=%v", res.Header, body, err)
			}
			if reads.views.Load() != before {
				t.Fatal("denied attach reached the runtime reader instead of failing at the stored-row route gate")
			}
		} else {
			if res.Header.Get("Content-Type") != "text/event-stream" {
				t.Fatalf("allowed attach not SSE: %v", res.Header)
			}
			events := collectSSETimeout(t, res.Body, time.Second, func(events []sseEvt) bool {
				for _, event := range events {
					if event.Event == "output" && strings.Contains(event.Data, marker) {
						return true
					}
				}
				return false
			})
			if len(events) == 0 {
				t.Fatal("allowed attach returned no output")
			}
		}
	}
	t.Run("foreign workspace attach has no live bytes", func(t *testing.T) { attach(t, confined, runRef, 404) })
	t.Run("missing reference is concealed", func(t *testing.T) { attach(t, confined, model.NewID().String(), 404) })
	t.Run("primary ID is not a public reference alias", func(t *testing.T) { attach(t, unconfined, rowID.String(), 404) })
	for _, suffix := range []string{"", "/events"} {
		t.Run("foreign content"+suffix, func(t *testing.T) {
			r := h.do("GET", "/v1/m/sessions/runs/"+runRef+suffix, confined, tenantHdr(tenant))
			if r.code != 404 || strings.Contains(r.raw, marker) {
				t.Fatalf("foreign content=%d %s", r.code, r.raw)
			}
		})
	}
	for _, action := range []string{"input", "interrupt", "stop", "resume", "cleanup", "delete"} {
		t.Run("foreign control/"+action, func(t *testing.T) {
			method, suffix := "POST", "/"+action
			if action == "delete" {
				method, suffix = "DELETE", ""
			}
			var body any
			if action == "input" {
				body = map[string]any{"text": "must not reach the foreign process"}
			}
			r := h.doJSON(method, "/v1/m/sessions/runs/"+runRef+suffix, confined, body, tenantHdr(tenant))
			if r.code != 404 || strings.Contains(r.raw, marker) {
				t.Fatalf("foreign control=%d %s", r.code, r.raw)
			}
		})
	}
	t.Run("tenant-wide reader still receives live output", func(t *testing.T) { attach(t, unconfined, runRef, 200) })
	sameWorkspace := member("same-workspace-run@test.io", auth.RoleViewer, workspaceB)
	t.Run("same-workspace reader receives live output", func(t *testing.T) { attach(t, sameWorkspace, runRef, 200) })
}

// Preserve the real store's confinement behavior while observing whether the
// route gate refused before entering a runtime reader. No authority is replaced.
type runRouteReadCounter struct {
	api.ModuleData
	views atomic.Int64
}

func (d *runRouteReadCounter) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.views.Add(1)
	return d.ModuleData.View(ctx, tenant, fn)
}
