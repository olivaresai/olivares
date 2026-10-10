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
	"github.com/olivaresai/olivares/modules/governance/testsupport"
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
	sameWorkspace := member("same-workspace-run@test.io", auth.RoleViewer, workspaceB)
	principal, err := auth.NewAuthenticator(h.st, nil).Authenticate(context.Background(), confined)
	if err != nil {
		t.Fatal(err)
	}
	if workspace, ok := principal.ConfinedWorkspaceIn(tenant); !ok || workspace != workspaceA || principal.Superadmin {
		t.Fatalf("fixture did not establish workspace-A confinement: %s %t", workspace, ok)
	}
	reads := &runRouteReadCounter{ModuleData: h.m.Data}
	h.m.UseData(reads)
	const marker = "FOREIGN-WORKSPACE-LIVE-OUTPUT"
	const unrelatedMarker = "ANOTHER-RUN-OR-ORPHAN-EVENT"
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
		if err != nil {
			return err
		}
		rowID = model.ID(rec.String(model.ColID))
		events, err := sc.Ext(runEventKind)
		if err != nil {
			return err
		}
		_, err = events.Create(context.Background(), model.Record{
			colEvRunRef: runRef, colEvSeq: int64(1), colEvAt: time.Now(),
			colEvEvent: "created", colEvDetail: marker, colEvPayloadHash: "test-hash", colEvAuditSeq: int64(0),
		})
		if err != nil {
			return err
		}
		// Unrelated historic rows and an orphan with a row-ID-shaped key must
		// stay out of this run's ledger even after a confined read is admitted.
		for _, otherRef := range []string{model.NewID().String(), rowID.String()} {
			if _, err := events.Create(context.Background(), model.Record{
				colEvRunRef: otherRef, colEvSeq: int64(1), colEvAt: time.Now(),
				colEvEvent: "created", colEvDetail: unrelatedMarker, colEvPayloadHash: "other-hash", colEvAuditSeq: int64(0),
			}); err != nil {
				return err
			}
		}
		return nil
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
		for _, tc := range []struct {
			name, token, ref string
			want             int
		}{
			{"foreign content", confined, runRef, http.StatusNotFound},
			{"missing content", unconfined, model.NewID().String(), http.StatusNotFound},
			{"confined missing content", sameWorkspace, model.NewID().String(), http.StatusNotFound},
			{"primary ID is not content reference", sameWorkspace, rowID.String(), http.StatusNotFound},
			{"same workspace read", sameWorkspace, runRef, http.StatusOK},
			{"tenant wide read", unconfined, runRef, http.StatusOK},
		} {
			t.Run(tc.name+suffix, func(t *testing.T) {
				before := reads.views.Load()
				r := h.do("GET", "/v1/m/sessions/runs/"+tc.ref+suffix, tc.token, tenantHdr(tenant))
				if r.code != tc.want || (tc.want == 404 && strings.Contains(r.raw, marker)) {
					t.Fatalf("content=%d %s, want %d", r.code, r.raw, tc.want)
				}
				if tc.want == 200 && !strings.Contains(r.raw, marker) {
					t.Fatal("authorized read did not return the stored run or its event")
				}
				if strings.Contains(r.raw, unrelatedMarker) {
					t.Fatal("run read included an unrelated event")
				}
				if tc.want == 404 {
					if reads.views.Load() != before {
						t.Fatal("concealed content reached the module reader")
					}
					errorObject, _ := r.body["error"].(map[string]any)
					if errorObject["code"] != "not_found" {
						t.Fatalf("concealed read must have the same not_found envelope: %s", r.raw)
					}
				}
			})
		}
	}
	t.Run("event query cannot replace the authorized parent", func(t *testing.T) {
		r := h.do("GET", "/v1/m/sessions/runs/"+runRef+"/events?run_ref="+rowID.String()+"&workspace_id="+workspaceA.String(), sameWorkspace, tenantHdr(tenant))
		if r.code != http.StatusOK || !strings.Contains(r.raw, marker) || strings.Contains(r.raw, unrelatedMarker) {
			t.Fatalf("caller selected the event parent: %d %s", r.code, r.raw)
		}
	})
	for _, action := range []string{"input", "interrupt", "stop", "resume", "peers", "cleanup", "delete"} {
		t.Run("foreign control/"+action, func(t *testing.T) {
			beforeRead, beforeWrite := reads.views.Load(), reads.mutations.Load()
			method, suffix := "POST", "/"+action
			if action == "delete" {
				method, suffix = "DELETE", ""
			} else if action == "peers" {
				method = "PUT"
			}
			var body any
			if action == "input" {
				body = map[string]any{"text": "must not reach the foreign process"}
			} else if action == "peers" {
				body = map[string]any{"peers": []string{}}
			}
			r := h.doJSON(method, "/v1/m/sessions/runs/"+runRef+suffix, confined, body, tenantHdr(tenant))
			if r.code != 404 || strings.Contains(r.raw, marker) {
				t.Fatalf("foreign control=%d %s", r.code, r.raw)
			}
			errorObject, _ := r.body["error"].(map[string]any)
			if errorObject["code"] != "not_found" || reads.views.Load() != beforeRead || reads.mutations.Load() != beforeWrite {
				t.Fatalf("foreign control must be concealed before runtime access: %s", r.raw)
			}
		})
		for _, tc := range []struct {
			name, token, ref string
			want             int
		}{
			{"same workspace read but not act", sameWorkspace, runRef, http.StatusForbidden},
			{"tenant wide read but not act", unconfined, runRef, http.StatusForbidden},
			{"missing control", unconfined, model.NewID().String(), http.StatusNotFound},
		} {
			t.Run(tc.name+"/"+action, func(t *testing.T) {
				beforeRead, beforeWrite := reads.views.Load(), reads.mutations.Load()
				method, suffix := "POST", "/"+action
				if action == "delete" {
					method, suffix = "DELETE", ""
				} else if action == "peers" {
					method = "PUT"
				}
				var body any
				if action == "input" {
					body = map[string]any{"text": "must not reach the process"}
				} else if action == "peers" {
					body = map[string]any{"peers": []string{}}
				}
				r := h.doJSON(method, "/v1/m/sessions/runs/"+tc.ref+suffix, tc.token, body, tenantHdr(tenant))
				if r.code != tc.want || strings.Contains(r.raw, marker) {
					t.Fatalf("control=%d %s, want %d", r.code, r.raw, tc.want)
				}
				errorObject, _ := r.body["error"].(map[string]any)
				wantCode := "forbidden"
				if tc.want == 404 {
					wantCode = "not_found"
				}
				if errorObject["code"] != wantCode {
					t.Fatalf("control code=%v, want %s", errorObject["code"], wantCode)
				}
				if reads.views.Load() != beforeRead || reads.mutations.Load() != beforeWrite {
					t.Fatal("denied control entered runtime data access")
				}
			})
		}
	}
	t.Run("readable admin policy-denied controls retain 403", func(t *testing.T) {
		testsupport.SeedCedar(t, h.st, tenant, `forbid(principal, action, resource) when { context.permission == "sessions:run:write" || context.permission == "sessions:run:admin" };`, f.gov)

		if r := h.do("GET", "/v1/m/sessions/runs/"+runRef, admin, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("admin read = %d %s", r.code, r.raw)
		}
		for _, action := range []string{"input", "interrupt", "stop", "resume", "peers", "cleanup", "delete"} {
			t.Run(action, func(t *testing.T) {
				beforeRead, beforeWrite := reads.views.Load(), reads.mutations.Load()
				method, suffix := "POST", "/"+action
				if action == "delete" {
					method, suffix = "DELETE", ""
				} else if action == "peers" {
					method = "PUT"
				}
				r := h.doJSON(method, "/v1/m/sessions/runs/"+runRef+suffix, admin, map[string]any{}, tenantHdr(tenant))
				errorObject, _ := r.body["error"].(map[string]any)
				if r.code != http.StatusForbidden || errorObject["code"] != "forbidden" || reads.views.Load() != beforeRead || reads.mutations.Load() != beforeWrite {
					t.Fatalf("policy-denied readable control = %d %s; want 403 before runtime access", r.code, r.raw)
				}
			})
		}
	})
	t.Run("tenant-wide reader still receives live output", func(t *testing.T) { attach(t, unconfined, runRef, 200) })
	t.Run("same-workspace reader receives live output", func(t *testing.T) { attach(t, sameWorkspace, runRef, 200) })
}

// Preserve the real store's confinement behavior while observing whether the
// route gate refused before entering a runtime reader. No authority is replaced.
type runRouteReadCounter struct {
	api.ModuleData
	views     atomic.Int64
	mutations atomic.Int64
}

func (d *runRouteReadCounter) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.views.Add(1)
	return d.ModuleData.View(ctx, tenant, fn)
}

func (d *runRouteReadCounter) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.mutations.Add(1)
	return d.ModuleData.Mutate(ctx, tenant, fn)
}
