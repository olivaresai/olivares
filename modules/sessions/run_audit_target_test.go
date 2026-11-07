// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The audit list filters tool: and folder: reach a sessions.run entry through its
// target_id, so every entry about a run must name the sessions.run row there, not
// only in meta.run_ref.

// runAuditTargets returns every entry with action on the tenant's ledger.
func runAuditTargets(t *testing.T, st store.Store, tenant model.TenantID, action string) []model.AuditEvent {
	t.Helper()
	var out []model.AuditEvent
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 0, func(ev model.AuditEvent) error {
			if ev.Action == action {
				out = append(out, ev)
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// requireRunTarget fails unless exactly want entries with action exist and each
// targets the run kind and the stored row id of runRef.
func requireRunTarget(t *testing.T, st store.Store, tenant model.TenantID, runRef, action string, want int) {
	t.Helper()
	id := model.ID(storedRunIDForTest(t, st, tenant, runRef))
	got := runAuditTargets(t, st, tenant, action)
	if len(got) != want {
		t.Fatalf("%s entries = %d, want %d", action, len(got), want)
	}
	for _, ev := range got {
		if ev.TargetKind != runKind || ev.TargetID != id {
			t.Errorf("%s target = %q/%q, want %q/%q (the run row)", action, ev.TargetKind, ev.TargetID, runKind, id)
		}
	}
}

// requireToolAndFolderFilters asks the real GET /v1/audit?tool= and ?folder= for
// the entries of runRef: each action must come back, and a tool the run did not
// use must not return them.
func requireToolAndFolderFilters(t *testing.T, h *harness, admin string, tenant model.TenantID, runRef string, actions ...string) {
	t.Helper()
	var tool, folder string
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		runs, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(context.Background(), runs, runRef)
		tool, folder = rec.String(colRunProfileDriver), rec.String(colRunWorkspacePath)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if tool == "" || folder == "" {
		t.Fatalf("fixture run has tool %q and folder %q, want both set", tool, folder)
	}
	listed := func(query string) map[string]bool {
		r := h.do(http.MethodGet, "/v1/audit?limit=500&"+query, admin, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("GET /v1/audit?%s = %d %s", query, r.code, r.raw)
		}
		seen := map[string]bool{}
		items, _ := r.body["items"].([]any)
		for _, item := range items {
			seen[item.(map[string]any)["action"].(string)] = true
		}
		return seen
	}
	for _, query := range []string{"tool=" + url.QueryEscape(tool), "folder=" + url.QueryEscape(folder)} {
		seen := listed(query)
		for _, action := range actions {
			if !seen[action] {
				t.Errorf("GET /v1/audit?%s lacks %s", query, action)
			}
		}
	}
	other := listed("tool=" + url.QueryEscape(tool+"-other"))
	for _, action := range actions {
		if other[action] {
			t.Errorf("GET /v1/audit?tool=%s-other returns %s of a %s run", tool, action, tool)
		}
	}
}

func TestRunAuditTarget(t *testing.T) {
	t.Run("attach", func(t *testing.T) {
		runner := &fakeRunner{initSID: "audit-target-attach"}
		m, st, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()))
		dto, err := createProfiledTestRun(t, m, context.Background(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:operator", ActorKind: model.ActorUser})
		if err != nil {
			t.Fatal(err)
		}
		identity, _ := dto.Completion.Identity()
		live, _ := m.rt.getLive(tenant, dto.RunRef)
		seq := live.ring.append("stdout", []byte("bounded output"), time.Now())
		stop := errors.New("followed one frame")
		err = m.AttachRuntimeOutput(context.Background(), identity, "user:operator", model.ActorUser, "inspect output", seq, func(ev RuntimeOutputEvent) error {
			if ev.Type == "output" {
				return stop
			}
			return nil
		})
		if !errors.Is(err, stop) {
			t.Fatalf("attach error = %v, want the stop", err)
		}
		requireRunTarget(t, st, tenant, dto.RunRef, "sessions.run.attach", 1)
	})
	t.Run("reads", func(t *testing.T) {
		f := newMoveFixture(t)
		if _, r := f.activity(t); r.code != http.StatusOK {
			t.Fatalf("activity = %d %s", r.code, r.raw)
		}
		requireRunTarget(t, f.h.st, f.tenant, f.runRef, "sessions.run.activity", 1)
		if _, r := f.messages(t, ""); r.code != http.StatusOK {
			t.Fatalf("messages = %d %s", r.code, r.raw)
		}
		requireRunTarget(t, f.h.st, f.tenant, f.runRef, "sessions.run.messages", 1)
		if r := f.h.do(http.MethodGet, "/v1/m/sessions/runs/"+f.runRef+"/handoff", f.admin, f.hdr); r.code != http.StatusOK {
			t.Fatalf("handoff preview = %d %s", r.code, r.raw)
		}
		requireRunTarget(t, f.h.st, f.tenant, f.runRef, "sessions.run.handoff_preview", 1)
		// The route the Ledger filters resolve a run through: target_id.
		id := storedRunIDForTest(t, f.h.st, f.tenant, f.runRef)
		list := f.h.do("GET", "/v1/audit?limit=200&target_id="+id, f.admin, tenantHdr(f.tenant))
		if list.code != http.StatusOK {
			t.Fatalf("audit list = %d %s", list.code, list.raw)
		}
		seen := map[string]bool{}
		items, _ := list.body["items"].([]any)
		for _, item := range items {
			seen[item.(map[string]any)["action"].(string)] = true
		}
		for _, action := range []string{"sessions.run.activity", "sessions.run.messages", "sessions.run.handoff_preview"} {
			if !seen[action] {
				t.Errorf("GET /v1/audit?target_id=<run> lacks %s", action)
			}
		}
		requireToolAndFolderFilters(t, f.h, f.admin, f.tenant, f.runRef, "sessions.run.activity", "sessions.run.messages", "sessions.run.handoff_preview")
	})
	t.Run("preview", func(t *testing.T) {
		f := newPreviewFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"})
		gate := &recordingPreviewGate{}
		WithPreviewGate(gate)(f.m)
		gate.set(PreviewDecision{Allowed: true}, nil)
		if r := f.open(f.appPort); r.code != http.StatusCreated {
			t.Fatalf("open = %d %s", r.code, r.raw)
		}
		gate.set(PreviewDecision{Reason: "no"}, nil)
		if r := f.open(f.appPort); r.code != http.StatusForbidden {
			t.Fatalf("refused open = %d %s", r.code, r.raw)
		}
		requireRunTarget(t, f.st, f.tenant, f.runRef, "sessions.run.preview", 1)
		requireRunTarget(t, f.st, f.tenant, f.runRef, "sessions.run.preview.refused", 1)
	})
	t.Run("denial budget", func(t *testing.T) {
		f := newDenialFixture(t, true)
		f.deny(3)
		f.waitBreaches(1)
		requireRunTarget(t, f.h.st, f.tenant, f.run, denialBudgetAction, 1)
		requireToolAndFolderFilters(t, f.h, f.admin, f.tenant, f.run, denialBudgetAction)
	})
	t.Run("review comment", func(t *testing.T) {
		m, h, admin, tenant, prof := codexHTTPHarness(t, "review-comment-target")
		setCodexFixture(t, prof, codexFixture{ThreadID: "thread-review-target", Account: "apikey"})
		runRef, _ := codexHTTPRun(t, m, h, admin, tenant, prof)
		place := map[string]any{"path": "core/a.go", "line": 12, "side": "modified"}
		sent := h.doJSON(http.MethodPost, "/v1/m/sessions/runs/"+runRef+"/input", admin,
			map[string]any{"text": "this loop never ends", "review_comment": place}, tenantHdr(tenant))
		if sent.code != http.StatusAccepted {
			t.Fatalf("review comment = %d %s", sent.code, sent.raw)
		}
		requireRunTarget(t, h.st, tenant, runRef, "sessions.run.review_comment", 1)
		requireToolAndFolderFilters(t, h, admin, tenant, runRef, "sessions.run.review_comment")
	})
}
