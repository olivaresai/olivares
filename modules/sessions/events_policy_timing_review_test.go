// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// A workspace reader whose full read policy refuses must not learn whether a
// run exists from the presence or absence of the external read-policy work.
func TestEventsDeniedReadHasEqualPolicyWorkHTTP(t *testing.T) {
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	const delay = 75 * time.Millisecond
	var calls atomic.Int64
	var mode atomic.Int32
	pdp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Input struct {
				Permission string `json:"permission"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if in.Input.Permission == string(permRunRead) {
			calls.Add(1)
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-r.Context().Done():
				return
			}
			if mode.Load() == 1 {
				w.WriteHeader(503)
				return
			}
			_, _ = io.WriteString(w, `{"result":false}`)
			return
		}
		_, _ = io.WriteString(w, `{"result":true}`)
	}))
	t.Cleanup(pdp.Close)
	opa, err := governance.NewOPAEvaluator(pdp.URL, "authz.allow", "", pdp.Client())
	if err != nil {
		t.Fatal(err)
	}
	f := newOPAConcealmentFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, opa)
	h := f.harness
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "sr-event-disclosure")
	var workspace model.ID
	ref := model.NewID().String()
	foreignRef := model.NewID().String()
	if err = h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(context.Background(), model.Workspace{Name: "own", Slug: "own", Status: model.StatusActive})
		if err != nil {
			return err
		}
		workspace = ws.ID
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{colRunRef: ref, colRunName: "read-denied", colTransport: string(TransportStreamJSON), colPermissionMode: "default", colIsolation: string(IsolationNative), colState: stateRunning, colLastEventSeq: int64(0), colRunAuthzWorkspaceID: workspace.String()})
		if err != nil {
			return err
		}
		other, err := sc.Workspaces().Create(context.Background(), model.Workspace{Name: "foreign", Slug: "foreign", Status: model.StatusActive})
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{colRunRef: foreignRef, colRunName: "foreign-hidden", colTransport: string(TransportStreamJSON), colPermissionMode: "default", colIsolation: string(IsolationNative), colState: stateRunning, colLastEventSeq: int64(0), colRunAuthzWorkspaceID: other.ID.String()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	created := h.doJSON("POST", "/v1/users", admin, map[string]any{"email": "sr-events-viewer@test.io", "password": "synthetic-events-pass1", "tenant": tenant.String(), "role": auth.RoleViewer, "workspace_id": workspace.String()}, nil)
	if created.code != 201 {
		t.Fatalf("create confined viewer status=%d", created.code)
	}
	login := h.doJSON("POST", "/v1/auth/login", "", map[string]any{"email": "sr-events-viewer@test.io", "password": "synthetic-events-pass1"}, nil)
	if login.code != 200 {
		t.Fatalf("login status=%d", login.code)
	}
	token := login.body["token"].(string)
	principal, err := f.authr.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := principal.ConfinedWorkspaceIn(tenant); !ok || got != workspace {
		t.Fatal("fixture is not workspace confined")
	}
	get := func(target string) (int, http.Header, []byte, time.Duration, int64) {
		t.Helper()
		request, err := http.NewRequest("GET", f.server.URL+"/v1/m/sessions/runs/"+target+"/events", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("X-Olivares-Tenant", tenant.String())
		before := calls.Load()
		start := time.Now()
		response, err := f.server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, response.Header, data, time.Since(start), calls.Load() - before
	}
	for index, name := range []string{"PDP-deny", "PDP-unavailable"} {
		mode.Store(int32(index))
		t.Run(name, func(t *testing.T) {
			for _, target := range []struct{ name, ref string }{{"policy-hidden", ref}, {"foreign-workspace", foreignRef}} {
				t.Run(target.name, func(t *testing.T) {
					for trial := 0; trial < 2; trial++ {
						absent, ah, ab, at, ac := get(model.NewID().String())
						existing, eh, eb, et, ec := get(target.ref)
						if absent != 404 || existing != 404 || !reflect.DeepEqual(ab, eb) {
							t.Errorf("concealment status/body differs absent=%d existing=%d", absent, existing)
						}
						for _, pair := range [][2]http.Header{{ah, eh}, {eh, ah}} {
							for key, values := range pair[0] {
								if key == "Date" || key == "X-Request-Id" {
									continue
								}
								if !reflect.DeepEqual(values, pair[1].Values(key)) {
									t.Errorf("concealment header differs: %s", key)
								}
							}
						}
						t.Logf("trial=%d existing_calls=%d absent_calls=%d existing_ms=%.3f absent_ms=%.3f", trial, ec, ac, float64(et)/float64(time.Millisecond), float64(at)/float64(time.Millisecond))
						if ec != ac && math.Abs(float64(et-at)) > float64(delay/2) {
							t.Error("read-denied run existence exposed through remote policy timing")
						}
					}
				})
			}
		})
	}
}
