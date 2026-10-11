// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// recordingPreviewGate answers with dec/err and keeps what it was asked.
type recordingPreviewGate struct {
	mu      sync.Mutex
	dec     PreviewDecision
	err     error
	intents []PreviewIntent
}

func (g *recordingPreviewGate) Authorize(_ context.Context, _ model.TenantID, in PreviewIntent) (PreviewDecision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.intents = append(g.intents, in)
	return g.dec, g.err
}

func (g *recordingPreviewGate) set(dec PreviewDecision, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.dec, g.err, g.intents = dec, err, nil
}

// previewFixture is one live run whose process listens on appPort, and an editor
// of its workspace who may write it.
type previewFixture struct {
	*streamConfinementFixture
	tenant    model.TenantID
	workspace model.ID
	runRef    string
	editor    string
	appPort   int
}

func newPreviewFixture(t *testing.T, cfg store.Config) *previewFixture {
	t.Helper()
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	f := newStreamConfinementFixture(t, cfg)
	admin := f.adminLogin()
	tenant := f.createOrg(admin, "preview-gate")
	var ws model.ID
	if err := f.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		id := model.NewID()
		w, err := sc.Workspaces().Create(context.Background(), model.Workspace{Name: id.String(), Slug: id.String(), Status: model.StatusActive})
		ws = w.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	editor := f.member(t, admin, tenant, "preview-gate-editor@test.io", ws)
	runRef := model.NewID().String()
	if err := f.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			colRunRef: runRef, colRunName: "preview", colTransport: string(TransportStreamJSON),
			colPermissionMode: "default", colIsolation: string(IsolationNative),
			colState: stateRunning, colLastEventSeq: int64(0), colRunAuthzWorkspaceID: ws.String(),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	const pid, appPort = 4712, 5173
	old := sessionPorts
	sessionPorts = func(root int) ([]listenPort, error) {
		if root != pid {
			return nil, nil
		}
		return []listenPort{{Port: appPort, Address: "127.0.0.1:5173"}}, nil
	}
	t.Cleanup(func() { sessionPorts = old })
	f.m.rt.putLive(&liveRun{tenant: tenant, runRef: runRef, transport: TransportStreamJSON, proc: &previewTestProc{pid: pid}, ring: newOutputRing(8, 1024)})
	t.Cleanup(func() { f.m.rt.dropLive(tenant, runRef) })
	return &previewFixture{streamConfinementFixture: f, tenant: tenant, workspace: ws, runRef: runRef, editor: editor, appPort: appPort}
}

func (f *previewFixture) open(port int) resp {
	f.t.Helper()
	return f.doJSON("POST", "/v1/m/sessions/runs/"+f.runRef+"/preview", f.editor, map[string]any{"port": port}, tenantHdr(f.tenant))
}

// previewAudits returns the meta of the tenant's audit entries with action.
func (f *previewFixture) previewAudits(action string) []map[string]any {
	f.t.Helper()
	var out []map[string]any
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			return errors.New("canonical audit read unavailable")
		}
		return walker.WalkCanonical(context.Background(), 0, func(e model.AuditEvent, meta string, _ []byte) error {
			if e.Action != action {
				return nil
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(meta), &m); err != nil {
				return err
			}
			out = append(out, m)
			return nil
		})
	}); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *previewFixture) openPreviews() int {
	f.m.previews.mu.Lock()
	defer f.m.previews.mu.Unlock()
	return len(f.m.previews.byToken)
}

// audited counts the entries with action that name the fixture's run and port.
func (f *previewFixture) audited(action string, port int) int {
	n := 0
	for _, meta := range f.previewAudits(action) {
		if meta["run_ref"] == f.runRef && fmt.Sprint(meta["port"]) == strconv.Itoa(port) {
			n++
		}
	}
	return n
}

// A bound PreviewGate decides POST /runs/{ref}/preview before a link is minted:
// a refusal answers 403 with its reason and is audited with the run and the port,
// and a gate that errors opens nothing. Unbound, the route is unchanged. Every
// other refusal (404, 409, 429) is audited with the run and the port too.
func TestRunPreviewGate(t *testing.T) {
	f := newPreviewFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"})
	const opened, refused = "sessions.run.preview", "sessions.run.preview.refused"

	t.Run("unbound, the default gate allows", func(t *testing.T) {
		if r := f.open(f.appPort); r.code != http.StatusCreated {
			t.Fatalf("open = %d %s, want 201", r.code, r.raw)
		}
	})

	gate := &recordingPreviewGate{}
	WithPreviewGate(gate)(f.m)
	before := f.openPreviews()

	t.Run("a refusal answers 403 with its reason, mints nothing and is audited", func(t *testing.T) {
		gate.set(PreviewDecision{Reason: "preview:open is forbidden"}, nil)
		n := f.audited(refused, f.appPort)
		r := f.open(f.appPort)
		if r.code != http.StatusForbidden || !strings.Contains(r.raw, "preview:open is forbidden") || strings.Contains(r.raw, "/session/preview/") {
			t.Fatalf("open = %d %s, want 403 with the gate's reason", r.code, r.raw)
		}
		if got := f.openPreviews(); got != before {
			t.Fatalf("open previews = %d, want %d: a refused preview minted a link", got, before)
		}
		if len(gate.intents) != 1 || gate.intents[0].RunRef != f.runRef || gate.intents[0].WorkspaceID != f.workspace ||
			gate.intents[0].Port != f.appPort || gate.intents[0].Principal.Actor() == "" {
			t.Fatalf("gate intents = %+v, want one for the run, its workspace, the port and the caller", gate.intents)
		}
		if got := f.audited(refused, f.appPort); got != n+1 {
			t.Fatalf("refusals audited with the run and the port = %d, want %d", got, n+1)
		}
	})

	t.Run("an invalid port is answered before the gate is asked", func(t *testing.T) {
		gate.set(PreviewDecision{Reason: "denied"}, nil)
		if r := f.open(0); r.code != http.StatusBadRequest || len(gate.intents) != 0 {
			t.Fatalf("open = %d %s with %d gate calls, want 400 and none", r.code, r.raw, len(gate.intents))
		}
	})

	t.Run("a gate error opens nothing", func(t *testing.T) {
		gate.set(PreviewDecision{Allowed: true}, errors.New("policy store unreachable"))
		if r := f.open(f.appPort); r.code != http.StatusServiceUnavailable || strings.Contains(r.raw, "/session/preview/") {
			t.Fatalf("open = %d %s, want 503", r.code, r.raw)
		}
		if got := f.openPreviews(); got != before {
			t.Fatalf("open previews = %d, want %d", got, before)
		}
	})

	t.Run("an allowed preview opens and is audited", func(t *testing.T) {
		gate.set(PreviewDecision{Allowed: true}, nil)
		n := f.audited(opened, f.appPort)
		if r := f.open(f.appPort); r.code != http.StatusCreated {
			t.Fatalf("open = %d %s, want 201", r.code, r.raw)
		}
		if got := f.audited(opened, f.appPort); got != n+1 {
			t.Fatalf("opened previews audited with the run and the port = %d, want %d", got, n+1)
		}
	})

	t.Run("a port the session does not listen on is refused and audited", func(t *testing.T) {
		gate.set(PreviewDecision{Allowed: true}, nil)
		if r := f.open(f.appPort + 1); r.code != http.StatusNotFound {
			t.Fatalf("open = %d %s, want 404", r.code, r.raw)
		}
		if f.audited(refused, f.appPort+1) != 1 {
			t.Fatal("the 404 refusal is not audited with the run and the port")
		}
	})

	t.Run("a session not running here is refused after the gate and audited", func(t *testing.T) {
		lr, _ := f.m.rt.getLive(f.tenant, f.runRef)
		f.m.rt.dropLive(f.tenant, f.runRef)
		defer f.m.rt.putLive(lr)
		gate.set(PreviewDecision{Reason: "denied"}, nil)
		if r := f.open(f.appPort); r.code != http.StatusForbidden {
			t.Fatalf("open with a refusing gate = %d %s, want 403: the gate answers first", r.code, r.raw)
		}
		gate.set(PreviewDecision{Allowed: true}, nil)
		n := f.audited(refused, f.appPort)
		if r := f.open(f.appPort); r.code != http.StatusConflict {
			t.Fatalf("open = %d %s, want 409", r.code, r.raw)
		}
		if got := f.audited(refused, f.appPort); got != n+1 {
			t.Fatalf("409 refusals audited = %d, want %d", got, n+1)
		}
	})

	t.Run("a node at its preview limit refuses, mints nothing and audits", func(t *testing.T) {
		gate.set(PreviewDecision{Allowed: true}, nil)
		var fill []string
		f.m.previews.mu.Lock()
		for i := 0; len(f.m.previews.byToken) < maxPreviews; i++ {
			ref := "preview-fill-" + strconv.Itoa(i)
			proc := &previewTestProc{pid: 9000 + i}
			f.m.rt.putLive(&liveRun{tenant: f.tenant, runRef: ref, transport: TransportStreamJSON, proc: proc, ring: newOutputRing(8, 1024)})
			fill = append(fill, ref)
			for j := 0; j < maxRunPreviews && len(f.m.previews.byToken) < maxPreviews; j++ {
				f.m.previews.byToken[ref+"-"+strconv.Itoa(j)] = &previewGrant{tenant: f.tenant, runRef: ref, proc: proc, port: 1, expires: time.Now().Add(time.Hour)}
			}
		}
		f.m.previews.mu.Unlock()
		defer func() {
			for _, ref := range fill {
				f.m.rt.dropLive(f.tenant, ref)
			}
		}()
		n := f.audited(refused, f.appPort)
		if r := f.open(f.appPort); r.code != http.StatusTooManyRequests || strings.Contains(r.raw, "/session/preview/") {
			t.Fatalf("open = %d %s, want 429", r.code, r.raw)
		}
		if got := f.audited(refused, f.appPort); got != n+1 {
			t.Fatalf("429 refusals audited = %d, want %d", got, n+1)
		}
	})
}

// droppingAuditData answers every audit append the way the degrade spool policy
// answers one it drops (sqlstore audit.go: an empty event and no error).
type droppingAuditData struct{ api.ScopedData }

func (droppingAuditData) Mutate(_ context.Context, fn func(store.Scope) error) error {
	return fn(droppingAuditScope{})
}

type droppingAuditScope struct{ store.Scope }

func (droppingAuditScope) Audit() store.AuditLog { return droppingAuditLog{} }

type droppingAuditLog struct{ store.AuditLog }

func (droppingAuditLog) Append(context.Context, model.AuditDraft) (model.AuditEvent, error) {
	return model.AuditEvent{}, nil
}

// A preview whose audit entry the ledger dropped is not handed out: the link is a
// credential, and the ledger does not hold it. A refusal it dropped is not answered
// as a refusal either.
func TestRunPreviewDroppedAuditRefusesTheLink(t *testing.T) {
	f := newPreviewFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"})
	open := func(port int) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/runs/"+f.runRef+"/preview", strings.NewReader(`{"port":`+strconv.Itoa(port)+`}`))
		req.Header.Set("Content-Type", "application/json")
		route := chi.NewRouteContext()
		route.URLParams.Add("ref", f.runRef)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		rec := httptest.NewRecorder()
		f.m.handleOpenRunPreview(rec, req, api.ModuleContext{Tenant: f.tenant, Data: droppingAuditData{}})
		return rec
	}
	if rec := open(f.appPort); rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "/session/preview/") {
		t.Fatalf("open = %d %s, want 503 and no link", rec.Code, rec.Body.String())
	}
	if got := f.openPreviews(); got != 0 {
		t.Fatalf("open previews = %d, want 0", got)
	}
	if rec := open(f.appPort + 1); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("refused open = %d %s, want 503: the refusal was not recorded", rec.Code, rec.Body.String())
	}
	WithPreviewGate(&recordingPreviewGate{dec: PreviewDecision{Reason: "denied"}})(f.m)
	if rec := open(f.appPort); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("gate-refused open = %d %s, want 503: the refusal was not recorded", rec.Code, rec.Body.String())
	}
}
