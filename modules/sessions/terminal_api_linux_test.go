// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// terminalAPI is a harness with confinement wired, one registered folder and a real
// HTTP server in front of the engine, because the stream is a long response.
type terminalAPI struct {
	h      *harness
	srv    *httptest.Server
	tenant model.TenantID
	admin  string
	ref    string
}

func newTerminalAPI(t *testing.T) *terminalAPI {
	t.Helper()
	h := newHarness(t, New(WithConfinement([]string{t.TempDir()}, false)))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "terminals")
	folder := h.doJSON(http.MethodPost, "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": t.TempDir()}, tenantHdr(tenant))
	if folder.code != http.StatusCreated {
		t.Fatalf("register folder = %d %s", folder.code, folder.raw)
	}
	srv := httptest.NewServer(h.srv.Handler())
	t.Cleanup(srv.Close)
	return &terminalAPI{h: h, srv: srv, tenant: tenant, admin: admin, ref: folder.body["workspace_ref"].(string)}
}

func (a *terminalAPI) path(suffix string) string {
	return "/v1/m/sessions/workspaces/" + a.ref + "/terminals" + suffix
}

func (a *terminalAPI) start(token string) resp {
	return a.h.doJSON(http.MethodPost, a.path(""), token, map[string]any{"rows": 24, "cols": 80}, tenantHdr(a.tenant))
}

// stream opens the output of a terminal and returns the decoded bytes as they arrive.
func (a *terminalAPI) stream(t *testing.T, token, id string, from uint64) (<-chan string, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.srv.URL+a.path("/"+id+"/stream?from="+strconv.FormatUint(from, 10)), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Olivares-Tenant", a.tenant.String())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		cancel()
		t.Fatalf("stream = %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	out := make(chan string, 64)
	go func() {
		defer close(out)
		defer res.Body.Close()
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(nil, 1<<20)
		event := ""
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				var d struct {
					Data string `json:"d"`
				}
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d)
				switch event {
				case "out":
					b, _ := base64.StdEncoding.DecodeString(d.Data)
					out <- string(b)
				case "exit":
					out <- "\x00exit"
				}
			}
		}
	}()
	return out, cancel
}

func collect(t *testing.T, out <-chan string, want string) string {
	t.Helper()
	var seen bytes.Buffer
	timeout := time.After(10 * time.Second)
	for !strings.Contains(seen.String(), want) {
		select {
		case s, ok := <-out:
			if !ok {
				t.Fatalf("the stream ended before %q, saw %q", want, seen.String())
			}
			seen.WriteString(s)
		case <-timeout:
			t.Fatalf("waiting for %q, saw %q", want, seen.String())
		}
	}
	return seen.String()
}

func TestTerminalAPIStartTypeAndClose(t *testing.T) {
	a := newTerminalAPI(t)
	started := a.start(a.admin)
	if started.code != http.StatusCreated {
		t.Fatalf("start = %d %s", started.code, started.raw)
	}
	id, _ := started.body["terminal"].(string)
	out, cancel := a.stream(t, a.admin, id, 0)
	defer cancel()
	in := a.h.doJSON(http.MethodPost, a.path("/"+id+"/input"), a.admin, map[string]any{"data": "echo ans-$((6*7))\r"}, tenantHdr(a.tenant))
	if in.code != http.StatusOK {
		t.Fatalf("input = %d %s", in.code, in.raw)
	}
	collect(t, out, "ans-42")
	size := a.h.doJSON(http.MethodPost, a.path("/"+id+"/size"), a.admin, map[string]any{"rows": 30, "cols": 100}, tenantHdr(a.tenant))
	if size.code != http.StatusOK {
		t.Fatalf("size = %d %s", size.code, size.raw)
	}
	closed := a.h.do(http.MethodDelete, a.path("/"+id), a.admin, tenantHdr(a.tenant))
	if closed.code != http.StatusOK {
		t.Fatalf("close = %d %s", closed.code, closed.raw)
	}
	collect(t, out, "\x00exit")
	again := a.h.doJSON(http.MethodPost, a.path("/"+id+"/input"), a.admin, map[string]any{"data": "x"}, tenantHdr(a.tenant))
	if again.code != http.StatusNotFound {
		t.Fatalf("input after close = %d, want 404", again.code)
	}
}

func TestTerminalAPIRefusesAPersonWithoutTheRight(t *testing.T) {
	a := newTerminalAPI(t)
	viewer := a.h.viewerToken(a.admin, a.tenant, "v@terminals.test")
	r := a.start(viewer)
	if r.code != http.StatusForbidden {
		t.Fatalf("a viewer's start = %d %s, want a refusal", r.code, r.raw)
	}
	if n := len(a.h.m.terminals.byID); n != 0 {
		t.Fatalf("a refused start left %d shells", n)
	}
}

func TestTerminalAPIRefusesAnAPITokenWithStepUp(t *testing.T) {
	a := newTerminalAPI(t)
	issued := a.h.doJSON("POST", "/v1/tokens", a.admin, map[string]any{"name": "ci", "tenant": a.tenant.String(), "role": auth.RoleEditor}, nil)
	token, _ := issued.body["token"].(string)
	if issued.code != http.StatusCreated || token == "" {
		t.Fatalf("token = %d %s", issued.code, issued.raw)
	}
	r := a.start(token)
	if r.code != http.StatusForbidden || !strings.Contains(r.raw, "step_up_required") {
		t.Fatalf("a token's start = %d %s, want 403 step_up_required", r.code, r.raw)
	}
	if n := len(a.h.m.terminals.byID); n != 0 {
		t.Fatalf("a refused start left %d shells", n)
	}
}

func TestTerminalAPIHidesATerminalFromAnotherPerson(t *testing.T) {
	a := newTerminalAPI(t)
	started := a.start(a.admin)
	id, _ := started.body["terminal"].(string)
	editor := a.h.userToken(a.admin, a.tenant, "e@terminals.test", auth.RoleEditor)
	for _, req := range []struct{ method, suffix string }{{http.MethodPost, "/input"}, {http.MethodPost, "/size"}, {http.MethodGet, "/stream"}, {http.MethodDelete, ""}} {
		r := a.h.doJSON(req.method, a.path("/"+id+req.suffix), editor, map[string]any{"data": "x", "rows": 10, "cols": 10}, tenantHdr(a.tenant))
		if r.code != http.StatusNotFound {
			t.Errorf("%s %s by another person = %d, want 404", req.method, req.suffix, r.code)
		}
	}
	if a.h.m.terminals.get(id) == nil {
		t.Fatal("another person ended the terminal")
	}
}

func TestTerminalAPIRevokedFolderRightEndsShell(t *testing.T) {
	for _, right := range []auth.TrusteeRight{auth.TrusteeWrite, auth.TrusteeCreate, auth.TrusteeErase} {
		for _, endpoint := range []string{"input", "size", "stream"} {
			t.Run(fmt.Sprint(right)+"/"+endpoint, func(t *testing.T) {
				m, tenant, _, folder := terminalHarness(t)
				principal := auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID(), AAL: 3}
				revoked := false
				mc := api.ModuleContext{Tenant: tenant, Principal: principal, Resource: auth.ResourceAttrs{Kind: "resource", ID: folder.resourceID.String()}, Admission: func(_ context.Context, q auth.Request) bool {
					canonical, _ := right.Question(principal, tenant, q.Resource)
					return !revoked || q.Route.CedarAction != string(canonical.Permission)
				}}
				request := func(body string) *http.Request {
					req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
					rc := chi.NewRouteContext()
					rc.URLParams.Add("ref", folder.WorkspaceRef)
					req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
					return req
				}
				rec := httptest.NewRecorder()
				m.handleStartTerminal(rec, request(`{}`), mc)
				if rec.Code != http.StatusCreated {
					t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
				}
				var started struct {
					Terminal string `json:"terminal"`
				}
				_ = json.Unmarshal(rec.Body.Bytes(), &started)
				term := m.terminals.get(started.Terminal)
				revoked = true
				body := `{"data":"touch unsealed\r"}`
				if endpoint == "size" {
					body = `{"rows":24,"cols":80}`
				}
				req := request(body)
				chi.RouteContext(req.Context()).URLParams.Add("tid", started.Terminal)
				rec = httptest.NewRecorder()
				switch endpoint {
				case "input":
					m.handleTerminalInput(rec, req, mc)
				case "size":
					m.handleTerminalSize(rec, req, mc)
				case "stream":
					// Bound a broken variant that incorrectly accepts the stream.
					ctx, cancel := context.WithTimeout(req.Context(), time.Second)
					defer cancel()
					m.handleTerminalStream(rec, req.WithContext(ctx), mc)
				}
				if rec.Code != http.StatusForbidden {
					t.Errorf("revoked %v: %d %s", right, rec.Code, rec.Body.String())
				}
				select {
				case <-term.done():
				case <-time.After(3 * time.Second):
					t.Fatal("revoked terminal stayed alive")
				}
				if _, err := m.terminalOf(tenant, folder.WorkspaceRef, term.id, terminalWhoOf(mc)); err == nil {
					t.Fatal("revoked terminal remains accessible")
				}
			})
		}
	}
}
