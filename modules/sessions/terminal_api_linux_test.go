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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
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
				case "replay":
					out <- "\x00replay:" + strings.TrimPrefix(line, "data: ")
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
	// The console names the shell in the drawer's title row.
	if shell, _ := started.body["shell"].(string); shell != filepath.Base(terminalProgram()) {
		t.Fatalf("start names the shell %q, want %q", shell, filepath.Base(terminalProgram()))
	}
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
	// The list answers 200 only to someone holding the folder's rights: empty, and not refused.
	listed := a.h.do(http.MethodGet, a.path(""), editor, tenantHdr(a.tenant))
	if items, ok := listed.body["items"].([]any); listed.code != http.StatusOK || !ok || len(items) != 0 {
		t.Fatalf("another person's list = %d %s, want 200 and no terminals", listed.code, listed.raw)
	}
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

// A stream that will replay history says first where that history ends, so the console can
// tell old output from new; a stream with nothing to replay gets no such frame.
func TestTerminalAPIMarksWhereReplayEnds(t *testing.T) {
	a := newTerminalAPI(t)
	id, _ := a.start(a.admin).body["terminal"].(string)
	if in := a.h.doJSON(http.MethodPost, a.path("/"+id+"/input"), a.admin, map[string]any{"data": "echo hist-$((6*7))\r"}, tenantHdr(a.tenant)); in.code != http.StatusOK {
		t.Fatalf("input = %d %s", in.code, in.raw)
	}
	term := a.h.m.terminals.get(id)
	readUntil(t, term, "hist-42")

	out, cancel := a.stream(t, a.admin, id, 0)
	defer cancel()
	var first string
	select {
	case first = <-out:
	case <-time.After(10 * time.Second):
		t.Fatal("no frame from the stream")
	}
	raw, ok := strings.CutPrefix(first, "\x00replay:")
	if !ok {
		t.Fatalf("first frame = %q, want the replay marker", first)
	}
	var marker struct {
		End uint64 `json:"end"`
	}
	if err := json.Unmarshal([]byte(raw), &marker); err != nil {
		t.Fatalf("replay data %q: %v", raw, err)
	}
	term.mu.Lock()
	histEnd := term.base + uint64(bytes.Index(term.buf, []byte("hist-42"))+len("hist-42"))
	outputEnd := term.base + uint64(len(term.buf))
	term.mu.Unlock()
	if marker.End < histEnd || marker.End > outputEnd {
		t.Fatalf("replay end = %d, want the history (%d) covered and at most the output end (%d)", marker.End, histEnd, outputEnd)
	}
	collect(t, out, "hist-42")

	live, stop := a.stream(t, a.admin, id, 1<<40) // past the end: nothing to replay
	defer stop()
	if closed := a.h.do(http.MethodDelete, a.path("/"+id), a.admin, tenantHdr(a.tenant)); closed.code != http.StatusOK {
		t.Fatalf("close = %d %s", closed.code, closed.raw)
	}
	if seen := collect(t, live, "\x00exit"); strings.Contains(seen, "\x00replay") {
		t.Fatalf("a stream with nothing to replay got a marker: %q", seen)
	}
}

func TestTerminalAPIRevokedFolderRightEndsShell(t *testing.T) {
	for _, right := range []auth.TrusteeRight{auth.TrusteeWrite, auth.TrusteeCreate, auth.TrusteeErase} {
		for _, endpoint := range []string{"input", "size", "stream"} {
			t.Run(fmt.Sprint(right)+"/"+endpoint, func(t *testing.T) {
				m, tenant, _, folder := terminalHarness(t)
				principal := auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID(), AAL: 3}
				revoked := false
				mc := api.ModuleContext{Tenant: tenant, Principal: principal, Resource: auth.ResourceAttrs{Kind: "resource", ID: folder.ResourceID.String()}, Admission: func(_ context.Context, q auth.Request) bool {
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

// A terminal id lives only in the browser that started it. A person who lost it (another
// browser, cleared site data, a private window) still holds the slot, so the API must say
// which terminals are theirs and let them close one.
func TestTerminalAPIListsMyTerminalsWhenTheIDsAreLost(t *testing.T) {
	a := newTerminalAPI(t)
	started := map[string]bool{}
	for i := 0; i < terminalMaxPerPerson; i++ {
		r := a.start(a.admin)
		id, _ := r.body["terminal"].(string)
		if r.code != http.StatusCreated || id == "" {
			t.Fatalf("start %d = %d %s", i, r.code, r.raw)
		}
		started[id] = true
	}
	if r := a.start(a.admin); r.code != http.StatusTooManyRequests {
		t.Fatalf("start over the limit = %d %s, want 429", r.code, r.raw)
	}
	listed := a.h.do(http.MethodGet, a.path(""), a.admin, tenantHdr(a.tenant))
	items, _ := listed.body["items"].([]any)
	if listed.code != http.StatusOK || len(items) != terminalMaxPerPerson {
		t.Fatalf("list = %d %s, want the %d terminals", listed.code, listed.raw, terminalMaxPerPerson)
	}
	var first string
	for _, it := range items {
		id, _ := it.(map[string]any)["terminal"].(string)
		if !started[id] {
			t.Fatalf("listed %q, which this person never started", id)
		}
		first = id
	}
	if closed := a.h.do(http.MethodDelete, a.path("/"+first), a.admin, tenantHdr(a.tenant)); closed.code != http.StatusOK {
		t.Fatalf("close a listed terminal = %d %s", closed.code, closed.raw)
	}
	if r := a.start(a.admin); r.code != http.StatusCreated {
		t.Fatalf("start after freeing a slot = %d %s", r.code, r.raw)
	}
	// Another person's list holds none of them.
	viewer := a.h.viewerToken(a.admin, a.tenant, "list-v@terminals.test")
	if r := a.h.do(http.MethodGet, a.path(""), viewer, tenantHdr(a.tenant)); r.code == http.StatusOK {
		t.Fatalf("a viewer without the rights listed terminals: %s", r.raw)
	}
}

// A step-up that went stale is an answer to that request, not the end of a shell the person
// may have a build running in: only a proven loss of the right closes it.
func TestTerminalAPIStaleStepUpAnswersAndKeepsTheShell(t *testing.T) {
	m, tenant, _, folder := terminalHarness(t)
	fresh := auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID(), AAL: 3}
	admit := func(_ context.Context, _ auth.Request) bool { return true }
	node := auth.ResourceAttrs{Kind: "resource", ID: folder.ResourceID.String()}
	mcOf := func(p auth.Principal) api.ModuleContext {
		return api.ModuleContext{Tenant: tenant, Principal: p, Resource: node, Admission: admit}
	}
	request := func(body string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		rc := chi.NewRouteContext()
		rc.URLParams.Add("ref", folder.WorkspaceRef)
		return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
	}
	rec := httptest.NewRecorder()
	m.handleStartTerminal(rec, request(`{}`), mcOf(fresh))
	var started struct {
		Terminal string `json:"terminal"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &started)
	term := m.terminals.get(started.Terminal)
	if rec.Code != http.StatusCreated || term == nil {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	stale := fresh
	stale.AAL = 0 // the same person, a request whose step-up is no longer satisfied
	req := request(`{"data":"echo kept\r"}`)
	chi.RouteContext(req.Context()).URLParams.Add("tid", started.Terminal)
	rec = httptest.NewRecorder()
	m.handleTerminalInput(rec, req, mcOf(stale))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "step_up_required") {
		t.Fatalf("stale step-up = %d %s, want 403 step_up_required", rec.Code, rec.Body.String())
	}
	select {
	case <-term.done():
		t.Fatal("a stale step-up closed the shell")
	case <-time.After(500 * time.Millisecond):
	}
}

// A shell nobody reads from must still end when its person loses standing: signed out, or no
// longer a member. No request reaches it, so the module asks again on its own, as it does for
// an open SFTP session, and a failure to ask never ends it.
func TestTerminalDetachedShellEndsWhenItsPersonLosesStanding(t *testing.T) {
	old := sftpRecheckInterval
	sftpRecheckInterval = 100 * time.Millisecond
	oldGrace := terminalUnverifiedGrace
	terminalUnverifiedGrace = 500 * time.Millisecond
	t.Cleanup(func() { sftpRecheckInterval, terminalUnverifiedGrace = old, oldGrace })
	f := newStreamConfinementFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	if err := f.m.UsePersonalFilesRoot(filepath.Join(t.TempDir(), "files")); err != nil {
		t.Fatal(err)
	}
	WithConfinement([]string{t.TempDir()}, false)(f.m)
	f.m.CommunicationAuthority = NewCommunicationRequestAuthority(f.authr, f.authz)
	admin := f.adminLogin()
	tenant := f.createOrg(admin, "terminal-standing")
	ctx := context.Background()
	for i, tc := range []struct {
		name, reason string
		lose         func(t *testing.T, token string)
	}{
		{"signed out", "access_revoked", func(t *testing.T, token string) {
			if got := f.do("POST", "/v1/auth/logout", token, nil); got.code != http.StatusNoContent {
				t.Fatalf("logout = %d %s", got.code, got.raw)
			}
		}},
		// The core answers "evidence unavailable" for a removed member: the shell ends when that persists.
		{"membership removed", "access_unverified", func(t *testing.T, token string) {
			p, err := f.authr.Authenticate(ctx, token)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
				all, _, err := as.Memberships().List(ctx, model.Query{Limit: 100})
				if err != nil {
					return err
				}
				for _, ms := range all {
					if ms.UserID == p.UserID && ms.TargetTenantID == tenant {
						return as.Memberships().Delete(ctx, ms.ID)
					}
				}
				return fmt.Errorf("no membership of %s in the tenant", p.UserID)
			}); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := f.member(t, admin, tenant, fmt.Sprintf("term-person-%d@test.io", i), model.ID(""))
			listed := f.do("GET", "/v1/m/sessions/workspaces", token, tenantHdr(tenant))
			items, _ := listed.body["items"].([]any)
			if listed.code != http.StatusOK || len(items) != 1 {
				t.Fatalf("list folders: %d %s; want the person's own folder", listed.code, listed.raw)
			}
			ref := items[0].(map[string]any)["workspace_ref"].(string)
			started := f.doJSON("POST", "/v1/m/sessions/workspaces/"+ref+"/terminals", token, map[string]any{}, tenantHdr(tenant))
			id, _ := started.body["terminal"].(string)
			term := f.m.terminals.get(id)
			if started.code != http.StatusCreated || term == nil {
				t.Fatalf("start = %d %s", started.code, started.raw)
			}
			select {
			case <-term.done():
				t.Fatalf("the shell ended before the person lost anything (%s)", term.closeReason())
			case <-time.After(500 * time.Millisecond):
			}
			tc.lose(t, token)
			select {
			case <-term.done():
			case <-time.After(5 * time.Second):
				term.close("test")
				t.Fatalf("%s: the detached shell still runs", tc.name)
			}
			if got := term.closeReason(); got != tc.reason {
				t.Errorf("close reason = %q, want %s", got, tc.reason)
			}
		})
	}
}

// Only a proven loss ends a shell at once; a check that could not be answered does not.
func TestTerminalAccessLostIsOnlyAProvenLoss(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		lost bool
	}{
		{"credential gone", auth.ErrUnauthenticated, true},
		{"tenant admission withdrawn", fmt.Errorf("%w: %w", auth.ErrPrincipalEvidenceUnavailable, auth.ErrPrincipalScopeAdmissionRequired), true},
		{"right refused", forbiddenErr("no"), true},
		{"folder gone", &runErr{http.StatusNotFound, "not found"}, true},
		{"store unavailable", errors.Join(&runErr{http.StatusServiceUnavailable, "folder resource is unavailable"}, forbiddenErr("inner")), false},
		{"evidence unavailable", auth.ErrPrincipalEvidenceUnavailable, false},
		{"deadline", context.DeadlineExceeded, false},
		{"cannot ask", errSFTPRefused, false},
		{"nothing wrong", nil, false},
	} {
		if got := accessLost(tc.err); got != tc.lost {
			t.Errorf("%s: accessLost = %v, want %v", tc.name, got, tc.lost)
		}
	}
}
