// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// slowAudit holds each audit append until the test lets it through, as a busy
// SQLite or a slow disk under the ledger would; stop lets every audit through.
type slowAudit struct {
	api.ScopedData
	entered, release, stop chan struct{}
}

func (d slowAudit) Mutate(ctx context.Context, fn func(store.Scope) error) error {
	select {
	case d.entered <- struct{}{}:
		select {
		case <-d.release:
		case <-d.stop:
		}
	case <-d.stop:
	}
	return d.ScopedData.Mutate(ctx, fn)
}

func withID(r *http.Request, id model.ID) *http.Request {
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", id.String())
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
}

// A sign-in read answers while an install request, a job poll or an install
// outcome waits on its audit: the sign-in link and code take the same module
// lock, so no audit, journal fsync or catalog read may run under it.
func TestSignInIsNotHeldBehindToolJobAudits(t *testing.T) {
	m, err := New(t.Context(), toolinstall.NewEngine(toolinstall.NewCatalog(), toolinstall.EngineOptions{}), filepath.Join(t.TempDir(), "tools"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	st := auditStore(t)
	admin := auth.Principal{Kind: auth.KindUser, Superadmin: true, AAL: auth.AAL1}
	plain := api.ModuleContext{Tenant: model.SystemTenantID, Principal: admin, Data: api.NewScopedData(st, model.SystemTenantID)}
	slow := slowAudit{plain.Data, make(chan struct{}), make(chan struct{}), make(chan struct{})}
	t.Cleanup(func() { close(slow.stop) }) // a failed check never leaves the module locked
	held := plain
	held.Data = slow

	signInAnswers := func(while string) {
		t.Helper()
		done := make(chan int, 1)
		go func() {
			w := httptest.NewRecorder()
			m.handleSignInGet(w, withID(httptest.NewRequest(http.MethodGet, "/", nil), model.NewID()), plain)
			done <- w.Code
		}()
		select {
		case code := <-done:
			if code != http.StatusNotFound {
				t.Fatalf("sign-in read while %s = %d, want 404 for an unknown sign-in", while, code)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("sign-in read waited while %s was audited", while)
		}
	}
	waitAudit := func(what string) {
		t.Helper()
		select {
		case <-slow.entered:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s never reached its audit", what)
		}
	}
	pollJob := func(id model.ID) Job {
		t.Helper()
		w := httptest.NewRecorder()
		m.job(w, withID(httptest.NewRequest(http.MethodGet, "/", nil), id), plain)
		var j Job
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &j) != nil {
			t.Fatalf("job poll = %d %s", w.Code, w.Body.String())
		}
		return j
	}

	// An install request: its audit, then its journal record.
	id := model.NewID()
	m.plans[model.SystemTenantID.String()+":approved"] = &Plan{Digest: "approved", Driver: "absent", Version: "1.0", v1: &toolinstall.Plan{}, expires: time.Now().Add(time.Hour)}
	installed := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		m.install(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"plan_digest":"approved","request_id":"`+id.String()+`"}`)), held)
		installed <- w
	}()
	waitAudit("the install request")
	signInAnswers("the install request")
	slow.release <- struct{}{}
	if w := <-installed; w.Code != http.StatusAccepted {
		t.Fatalf("install = %d %s", w.Code, w.Body.String())
	}

	// The install outcome: the engine refuses the absent driver at once, and the
	// outcome waits on its audit. The job reads running until it is retained.
	waitAudit("the install outcome")
	signInAnswers("the install outcome")
	if j := pollJob(id); j.State != "running" {
		t.Fatalf("job before its outcome was retained = %+v", j)
	}
	slow.release <- struct{}{}
	deadline := time.Now().Add(10 * time.Second)
	j := pollJob(id)
	for j.State == "running" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		j = pollJob(id)
	}
	if j.State != "failed" || j.AuditError != "" || j.Error == "" {
		t.Fatalf("job after its outcome = %+v", j)
	}

	// A job poll: each read is audited.
	polled := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		m.job(w, withID(httptest.NewRequest(http.MethodGet, "/", nil), id), held)
		polled <- w.Code
	}()
	waitAudit("the job poll")
	signInAnswers("the job poll")
	slow.release <- struct{}{}
	if code := <-polled; code != http.StatusOK {
		t.Fatalf("held job poll = %d", code)
	}
}
