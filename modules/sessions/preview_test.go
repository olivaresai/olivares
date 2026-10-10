// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// previewTestProc is a live session process whose app is an in-test server.
type previewTestProc struct {
	Process
	pid int
}

func (p *previewTestProc) PID() int { return p.pid }
func (p *previewTestProc) DialPreview(ctx context.Context, address string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", address)
}

// The preview reaches a session's port only through a URL its run's writer
// opened: another user, workspace or tenant cannot open one, a guessed token
// reaches nothing, and neither side's cookies cross the proxy.
func TestRunPreviewAuthorizationAndIsolation(t *testing.T) {
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	var m *Module
	f := newStreamConfinementFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, func(o *api.Options) {
		m = o.Modules[0].(*Module)
		o.SessionPreview = http.HandlerFunc(m.ServePreviewHTTP)
	})
	h := f.harness

	var hits atomic.Int32
	var appCookie atomic.Value
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		appCookie.Store(r.Header.Get("Cookie"))
		http.SetCookie(w, &http.Cookie{Name: "__Host-olivares-session", Value: "tossed", Path: "/", Secure: true})
		w.Header().Set("Clear-Site-Data", `"cookies"`)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Origin", "https://evil.example")
		w.Header().Set("NEL", `{"report_to":"x","max_age":86400}`)
		w.Header().Set("Referrer-Policy", "unsafe-url")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "APP-BODY "+r.URL.Path)
	}))
	t.Cleanup(app.Close)
	_, portText, _ := net.SplitHostPort(app.Listener.Addr().String())
	appPort, _ := strconv.Atoi(portText)
	const pid = 4711
	var closed, unreadable atomic.Bool
	old := sessionPorts
	sessionPorts = func(root int) ([]listenPort, error) {
		if unreadable.Load() {
			return nil, errors.New("no such process")
		}
		if root != pid || closed.Load() {
			return nil, nil
		}
		return []listenPort{{Port: appPort, Address: app.Listener.Addr().String()}}, nil
	}
	t.Cleanup(func() { sessionPorts = old })

	admin := h.adminLogin()
	tenant := h.createOrg(admin, "preview")
	other := h.createOrg(admin, "preview-other")
	var wsRun, wsOther model.ID
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		for _, target := range []*model.ID{&wsRun, &wsOther} {
			id := model.NewID()
			ws, err := sc.Workspaces().Create(context.Background(), model.Workspace{Name: id.String(), Slug: id.String(), Status: model.StatusActive})
			if err != nil {
				return err
			}
			*target = ws.ID
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	member := func(email, role string, org model.TenantID, ws model.ID) string {
		t.Helper()
		body := map[string]any{"email": email, "password": "preview-member-pass1", "tenant": org.String(), "role": role}
		if !ws.IsZero() {
			body["workspace_id"] = ws.String()
		}
		if r := h.doJSON("POST", "/v1/users", admin, body, nil); r.code != http.StatusCreated {
			t.Fatalf("member = %d %s", r.code, r.raw)
		}
		r := h.doJSON("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "preview-member-pass1"}, nil)
		if r.code != http.StatusOK {
			t.Fatalf("login = %d %s", r.code, r.raw)
		}
		return r.body["token"].(string)
	}
	writer := member("preview-writer@test.io", auth.RoleAdmin, tenant, wsRun)
	reader := member("preview-reader@test.io", auth.RoleViewer, tenant, "")
	foreignWorkspace := member("preview-foreign-ws@test.io", auth.RoleAdmin, tenant, wsOther)
	foreignTenant := member("preview-foreign-tenant@test.io", auth.RoleAdmin, other, "")

	runRef := model.NewID().String()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			colRunRef: runRef, colRunName: "preview", colTransport: string(TransportStreamJSON),
			colPermissionMode: "default", colIsolation: string(IsolationNative),
			colState: stateRunning, colLastEventSeq: int64(0), colRunAuthzWorkspaceID: wsRun.String(),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	proc := &previewTestProc{pid: pid}
	h.m.rt.putLive(&liveRun{tenant: tenant, runRef: runRef, transport: TransportStreamJSON, proc: proc, ring: newOutputRing(8, 1024)})
	t.Cleanup(func() { h.m.rt.dropLive(tenant, runRef) })

	path := "/v1/m/sessions/runs/" + runRef + "/preview"
	open := func(token string, org model.TenantID, port int) resp {
		t.Helper()
		return h.doJSON("POST", path, token, map[string]any{"port": port}, tenantHdr(org))
	}

	t.Run("ports are read with run read", func(t *testing.T) {
		r := h.do("GET", path, reader, tenantHdr(tenant))
		if r.code != http.StatusOK || !strings.Contains(r.raw, portText) {
			t.Fatalf("ports = %d %s", r.code, r.raw)
		}
	})
	for _, tc := range []struct {
		name  string
		token string
		org   model.TenantID
		want  int
	}{
		{"reader cannot open a preview", reader, tenant, http.StatusForbidden},
		{"another workspace's user cannot open or see it", foreignWorkspace, tenant, http.StatusNotFound},
		{"another tenant's user cannot open or see it", foreignTenant, tenant, http.StatusNotFound},
		{"another tenant's user cannot reach it from its own tenant", foreignTenant, other, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if r := open(tc.token, tc.org, appPort); r.code != tc.want || strings.Contains(r.raw, "/session/preview/") {
				t.Fatalf("open = %d %s, want %d", r.code, r.raw, tc.want)
			}
			if tc.want == http.StatusNotFound {
				if r := h.do("GET", path, tc.token, tenantHdr(tc.org)); r.code != http.StatusNotFound || strings.Contains(r.raw, portText) {
					t.Fatalf("ports = %d %s, want 404", r.code, r.raw)
				}
			}
		})
	}
	t.Run("a port the session does not listen on is refused", func(t *testing.T) {
		if r := open(writer, tenant, appPort+1); r.code != http.StatusNotFound {
			t.Fatalf("open = %d %s", r.code, r.raw)
		}
	})
	if hits.Load() != 0 {
		t.Fatalf("the app was reached %d times before any preview was opened", hits.Load())
	}

	r := open(writer, tenant, appPort)
	if r.code != http.StatusCreated {
		t.Fatalf("open = %d %s", r.code, r.raw)
	}
	url, _ := r.body["url"].(string)
	if !strings.HasPrefix(url, api.SessionPreviewPathPrefix) || !strings.HasSuffix(url, "/") {
		t.Fatalf("url = %q", url)
	}
	get := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", target, nil)
		req.Header.Set("Cookie", "__Host-olivares-session=ENGINE-SECRET; theme=dark")
		rec := httptest.NewRecorder()
		h.srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	t.Run("the writer's URL serves the app, sandboxed and without cookies", func(t *testing.T) {
		rec := get(url + "page?q=1")
		if rec.Code != http.StatusOK || rec.Body.String() != "APP-BODY /page" {
			t.Fatalf("preview = %d %q", rec.Code, rec.Body.String())
		}
		if c, _ := appCookie.Load().(string); c != "" {
			t.Fatalf("the app received the browser's cookies: %q", c)
		}
		hd := rec.Header()
		if hd.Get("Set-Cookie") != "" || hd.Get("Clear-Site-Data") != "" || hd.Get("Access-Control-Allow-Credentials") != "" ||
			hd.Get("Nel") != "" || slices.Contains(hd.Values("Referrer-Policy"), "unsafe-url") {
			t.Fatalf("the app's response acts on the engine origin: %v", hd)
		}
		if !strings.HasPrefix(hd.Get("Content-Security-Policy"), "sandbox ") || strings.Contains(hd.Get("Content-Security-Policy"), "allow-same-origin") {
			t.Fatalf("preview is not sandboxed: %q", hd.Get("Content-Security-Policy"))
		}
		if hd.Get("X-Frame-Options") != "" {
			t.Fatalf("X-Frame-Options = %q", hd.Get("X-Frame-Options"))
		}
		// The page's own origin is opaque: its requests are cross-origin and
		// credential-less, so the engine answers them for any origin, not the app.
		if got := hd.Values("Access-Control-Allow-Origin"); len(got) != 1 || got[0] != "*" {
			t.Fatalf("Access-Control-Allow-Origin = %q", got)
		}
		if hd.Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Fatalf("the app's own Content-Type was lost: %q", hd.Get("Content-Type"))
		}
	})
	t.Run("a preflight is answered by the engine, not the app", func(t *testing.T) {
		before := hits.Load()
		req := httptest.NewRequest("OPTIONS", url+"api", nil)
		req.Header.Set("Origin", "null")
		req.Header.Set("Access-Control-Request-Method", "PUT")
		rec := httptest.NewRecorder()
		h.srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Methods") != "PUT" || hits.Load() != before {
			t.Fatalf("preflight = %d %v, app hits %d", rec.Code, rec.Header(), hits.Load()-before)
		}
	})
	t.Run("a port the session closed stops answering", func(t *testing.T) {
		closed.Store(true)
		h.m.clock = &testClock{now: time.Now().Add(previewRecheck + time.Second)}
		t.Cleanup(func() { closed.Store(false); h.m.clock = model.SystemClock{} })
		before := hits.Load()
		if rec := get(url); rec.Code != http.StatusBadGateway || hits.Load() != before {
			t.Fatalf("closed port = %d %q", rec.Code, rec.Body.String())
		}
		// Ports that cannot be read are not the last ones read: the port may be
		// someone else's by now.
		closed.Store(false)
		h.m.clock = &testClock{now: time.Now().Add(3*previewRecheck + time.Second)}
		if rec := get(url); rec.Code != http.StatusOK {
			t.Fatalf("reopened port = %d %q", rec.Code, rec.Body.String())
		}
		unreadable.Store(true)
		t.Cleanup(func() { unreadable.Store(false) })
		h.m.clock = &testClock{now: time.Now().Add(5*previewRecheck + time.Second)}
		before = hits.Load()
		if rec := get(url); rec.Code != http.StatusBadGateway || hits.Load() != before {
			t.Fatalf("unreadable ports = %d %q", rec.Code, rec.Body.String())
		}
	})
	t.Run("a guessed token reaches nothing", func(t *testing.T) {
		before := hits.Load()
		for _, target := range []string{api.SessionPreviewPathPrefix + "guess/", api.SessionPreviewPathPrefix + strings.Repeat("A", 43) + "/", api.SessionPreviewPathPrefix} {
			if rec := get(target); rec.Code != http.StatusNotFound {
				t.Fatalf("%s = %d %q", target, rec.Code, rec.Body.String())
			}
		}
		if hits.Load() != before {
			t.Fatal("a guessed token reached the app")
		}
	})
	t.Run("the URL ends with the session", func(t *testing.T) {
		h.m.rt.dropLive(tenant, runRef)
		before := hits.Load()
		if rec := get(url); rec.Code != http.StatusNotFound || hits.Load() != before {
			t.Fatalf("after stop = %d %q", rec.Code, rec.Body.String())
		}
	})
}

// Reloading one session's preview never fills the node: each run keeps its
// newest previews, so another session can still open one.
func TestPreviewGrantsEvictTheRunsOldest(t *testing.T) {
	var g previewGrants
	now := time.Now()
	live := func(*previewGrant) bool { return true }
	grant := func(run string) *previewGrant {
		return &previewGrant{tenant: "t", runRef: run, expires: now.Add(previewTTL)}
	}
	for i := range maxPreviews + 10 {
		if !g.put("a"+strconv.Itoa(i), grant("run-a"), now, live) {
			t.Fatalf("open %d of one run was refused", i)
		}
	}
	if !g.put("b", grant("run-b"), now, live) {
		t.Fatal("one run's reloads kept another run from opening a preview")
	}
	if g.get("a0", now) != nil {
		t.Fatal("the run's oldest preview outlived its newer ones")
	}
	if g.get("a"+strconv.Itoa(maxPreviews+9), now) == nil || g.get("b", now) == nil {
		t.Fatal("a newest preview was evicted")
	}
	// A run that ended frees every preview it held, also unrequested ones.
	ended := func(p *previewGrant) bool { return p.runRef != "run-a" }
	if !g.put("c", grant("run-c"), now, ended) {
		t.Fatal("open refused")
	}
	if g.get("a"+strconv.Itoa(maxPreviews+9), now) != nil || g.get("b", now) == nil {
		t.Fatal("an ended run kept its previews, or a live one lost its own")
	}
	if g.get("b", now.Add(previewTTL)) != nil {
		t.Fatal("a preview outlived its hour")
	}
}
