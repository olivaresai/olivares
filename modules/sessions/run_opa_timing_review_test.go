// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package sessions

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

func newOPAConcealmentFixture(t *testing.T, cfg store.Config, pdp auth.PolicyEvaluator) *streamConfinementFixture {
	t.Helper()
	m, gov := New(), governance.New(governance.WithExternalPDP(pdp))
	ctx := context.Background()
	st, err := engine.Open(ctx, cfg, func(reg store.ExtensionRegistry) error {
		if err := m.RegisterSchema(reg); err != nil {
			return err
		}
		return gov.RegisterSchema(reg)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, err := sys.EnsureSystemTenant(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	m.UseData(api.NewModuleData(st))
	bindStoreStanding(m, st)
	gov.UseData(api.NewModuleData(st))
	stopModuleAtCleanup(t, m)
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	setup := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := setup.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	authr := auth.NewAuthenticator(st, nil)
	authz := auth.NewAuthorizer(gov.RequestEvaluator(), auth.WithScopedGrants(gov.ScopedGrants()))
	srv, err := api.New(api.Options{
		Store: st, Authenticator: authr, Authorizer: authz, Signer: signer,
		SetupToken: setup, Version: "test", Modules: []api.Module{m, gov},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	t.Log("mounted Sessions + Governance; real auth/RBAC/scoped grants; no authored policies; recorder not required")
	return &streamConfinementFixture{
		harness: &harness{t: t, m: m, srv: srv, st: st, setupTok: plaintext},
		authr:   authr, authz: authz, server: ts,
	}
}

// Exercise the shipped OPA adapter and full native Sessions/Governance authorizer.
// The local PDP denies every run read, with bounded normal network/evaluation
// latency. No action authority is granted or mocked. Denials must not perform
// an external read-policy round trip only when a hidden run exists.
func TestDeniedRunDoesNotRevealExistenceThroughOPALatency(t *testing.T) {
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	const delay = 75 * time.Millisecond
	var readCalls atomic.Int64
	pdp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Input struct {
				Permission string `json:"permission"`
			} `json:"input"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			t.Error("PDP input decode")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if in.Input.Permission == string(permRunRead) {
			readCalls.Add(1)
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-r.Context().Done():
				return
			}
			_, _ = io.WriteString(w, `{"result":false}`)
		} else {
			_, _ = io.WriteString(w, `{"result":true}`)
		}
	}))
	defer pdp.Close()
	opa, err := governance.NewOPAEvaluator(pdp.URL, "authz.allow", "", pdp.Client())
	if err != nil {
		t.Fatal(err)
	}
	f := newOPAConcealmentFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, opa)
	h := f.harness
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "sr-opa-hidden-run")
	viewer := h.viewerToken(admin, tenant, "sr-opa-viewer@test.io")
	ref := model.NewID().String()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(context.Background(), model.Workspace{Name: "hidden", Slug: "hidden", Status: model.StatusActive})
		if err != nil {
			return err
		}
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{colRunRef: ref, colRunName: "hidden marker", colTransport: string(TransportStreamJSON), colPermissionMode: "default", colIsolation: string(IsolationNative), colState: stateRunning, colLastEventSeq: int64(0), colRunAuthzWorkspaceID: ws.ID.String()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Establish the caller cannot read this run with the actual point-read route.
	if out := h.do(http.MethodGet, "/v1/m/sessions/runs/"+ref, viewer, tenantHdr(tenant)); out.code != 404 {
		t.Fatalf("fixture hidden read status=%d", out.code)
	}
	missing := model.NewID().String()
	for trial := 0; trial < 3; trial++ {
		before := readCalls.Load()
		start := time.Now()
		absent := h.doJSON(http.MethodPost, "/v1/m/sessions/runs/"+missing+"/stop", viewer, nil, tenantHdr(tenant))
		absentElapsed := time.Since(start)
		absentCalls := readCalls.Load() - before
		before = readCalls.Load()
		start = time.Now()
		hidden := h.doJSON(http.MethodPost, "/v1/m/sessions/runs/"+ref+"/stop", viewer, nil, tenantHdr(tenant))
		hiddenElapsed := time.Since(start)
		hiddenCalls := readCalls.Load() - before
		if hidden.code != 404 || absent.code != 404 || hidden.raw != absent.raw {
			t.Fatalf("non-timing concealment changed hidden=%d absent=%d body_equal=%t", hidden.code, absent.code, hidden.raw == absent.raw)
		}
		for _, key := range []string{"Content-Type", "Content-Length", "Cache-Control", "Retry-After", "Vary"} {
			if hidden.header.Get(key) != absent.header.Get(key) {
				t.Fatalf("concealment header differs: %s", key)
			}
		}
		t.Logf("trial=%d identical_404_body=true hidden_read_PDP_calls=%d absent_read_PDP_calls=%d hidden_ms=%.3f absent_ms=%.3f", trial, hiddenCalls, absentCalls, float64(hiddenElapsed)/float64(time.Millisecond), float64(absentElapsed)/float64(time.Millisecond))
		if hiddenCalls != absentCalls && hiddenElapsed-absentElapsed > delay/2 {
			t.Errorf("unreadable existing run distinguishable from absent run through external read-PDP timing")
		}
	}
}

// Catch both the missing-row skip and scoped-denial short circuit: actions and
// concealed reads evaluate read policy once even when confinement rejects read.
// Exercise the real adapter and routes; an equal status alone misses the oracle.
func TestRunActionDisclosureHasEqualOPAWork(t *testing.T) {
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	var readCalls atomic.Int64
	var readMode atomic.Int32 // 0 permits, 1 denies, 2 is unavailable.
	pdp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Input struct {
				Permission string `json:"permission"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if in.Input.Permission == string(permRunRead) {
			readCalls.Add(1)
			switch readMode.Load() {
			case 1:
				_, _ = io.WriteString(w, `{"result":false}`)
				return
			case 2:
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		_, _ = io.WriteString(w, `{"result":true}`)
	}))
	defer pdp.Close()
	opa, err := governance.NewOPAEvaluator(pdp.URL, "authz.allow", "", pdp.Client())
	if err != nil {
		t.Fatal(err)
	}
	f := newOPAConcealmentFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, opa)
	h := f.harness
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "run-opa-disclosure")
	var workspaceA, workspaceB model.ID
	ref := model.NewID().String()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		for i, target := range []*model.ID{&workspaceA, &workspaceB} {
			ws, err := sc.Workspaces().Create(context.Background(), model.Workspace{
				Name: fmt.Sprintf("Workspace %d", i), Slug: fmt.Sprintf("workspace-%d", i), Status: model.StatusActive,
			})
			if err != nil {
				return err
			}
			*target = ws.ID
		}
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			colRunRef: ref, colRunName: "OPA disclosure run", colTransport: string(TransportStreamJSON),
			colPermissionMode: "default", colIsolation: string(IsolationNative), colState: stateRunning,
			colLastEventSeq: int64(0), colRunAuthzWorkspaceID: workspaceB.String(),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	member := func(email string, workspace model.ID) string {
		t.Helper()
		body := map[string]any{"email": email, "password": "run-disclosure-pass1", "tenant": tenant.String(), "role": auth.RoleViewer}
		if !workspace.IsZero() {
			body["workspace_id"] = workspace.String()
		}
		if r := h.doJSON(http.MethodPost, "/v1/users", admin, body, nil); r.code != http.StatusCreated {
			t.Fatalf("member status=%d body=%s", r.code, r.raw)
		}
		r := h.doJSON(http.MethodPost, "/v1/auth/login", "", map[string]any{"email": email, "password": "run-disclosure-pass1"}, nil)
		if r.code != http.StatusOK {
			t.Fatalf("login status=%d body=%s", r.code, r.raw)
		}
		return r.body["token"].(string)
	}
	principals := []struct {
		name, token string
		readable    bool
	}{
		{"unconfined", member("opa-wide@test.io", ""), true},
		{"same workspace", member("opa-same@test.io", workspaceB), true},
		{"foreign workspace", member("opa-foreign@test.io", workspaceA), false},
	}
	missing := model.NewID().String()
	for mode, label := range []string{"read allowed", "read denied", "read unavailable"} {
		readMode.Store(int32(mode))
		t.Run(label, func(t *testing.T) {
			for _, principal := range principals {
				t.Run(principal.name, func(t *testing.T) {
					for _, action := range []struct {
						name, method, suffix string
						body                 any
					}{
						{"input", http.MethodPost, "/input", map[string]any{"text": "must not reach runtime"}},
						{"interrupt", http.MethodPost, "/interrupt", nil},
						{"stop", http.MethodPost, "/stop", nil},
						{"resume", http.MethodPost, "/resume", nil},
						{"cleanup", http.MethodPost, "/cleanup", nil},
						{"delete", http.MethodDelete, "", nil},
						{"detail", http.MethodGet, "", nil},
						{"events", http.MethodGet, "/events", nil},
						{"attach", http.MethodGet, "/attach", nil},
					} {
						// Healthy reads already have the unchanged HTTP confinement
						// matrix (including live attach). These pairs exercise denied
						// reads only and do not depend on inherited event availability.
						if action.method == http.MethodGet && mode == 0 && principal.readable {
							continue
						}
						t.Run(action.name, func(t *testing.T) {
							before := readCalls.Load()
							absent := h.doJSON(action.method, "/v1/m/sessions/runs/"+missing+action.suffix, principal.token, action.body, tenantHdr(tenant))
							absentCalls := readCalls.Load() - before
							before = readCalls.Load()
							present := h.doJSON(action.method, "/v1/m/sessions/runs/"+ref+action.suffix, principal.token, action.body, tenantHdr(tenant))
							presentCalls := readCalls.Load() - before
							want := http.StatusNotFound
							if mode == 0 && principal.readable {
								want = http.StatusForbidden
							}
							if absent.code != http.StatusNotFound || present.code != want {
								t.Errorf("absent=%d present=%d want_present=%d", absent.code, present.code, want)
							}
							if absentCalls != 1 || presentCalls != 1 {
								t.Errorf("existence-dependent read-PDP work: absent=%d present=%d, want 1 each", absentCalls, presentCalls)
							}
							if want == http.StatusNotFound && present.raw != absent.raw {
								t.Errorf("concealed response differs: present=%s absent=%s", present.raw, absent.raw)
							}
							if want == http.StatusNotFound {
								for _, pair := range [][2]http.Header{{present.header, absent.header}, {absent.header, present.header}} {
									for key, values := range pair[0] {
										if key != "Date" && key != "X-Request-Id" && !reflect.DeepEqual(values, pair[1].Values(key)) {
											t.Errorf("concealed header differs: %s", key)
										}
									}
								}
							}
						})
					}
				})
			}
		})
	}
}
