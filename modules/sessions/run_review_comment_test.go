// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// reviewCommentRecords counts the ledger records of one review comment sent to a run.
func reviewCommentRecords(t *testing.T, m *Module, tenant model.TenantID) int {
	t.Helper()
	ctx := context.Background()
	n := 0
	if err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(ctx, 0, func(ev model.AuditEvent) error {
			if ev.Action == "sessions.run.review_comment" {
				n++
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// A line comment on a session's diff travels through the run's input route as an
// ordinary message; the optional review_comment names where it was made, and the ledger
// records that place once the input was accepted. A malformed place is refused before
// anything is sent or recorded, and a plain input leaves no such record.
func TestRunInputReviewCommentIsRecordedOnceAcceptedAndRefusedWhenMalformed(t *testing.T) {
	m, h, admin, tenant, prof := codexHTTPHarness(t, "review-comment-audit")
	setCodexFixture(t, prof, codexFixture{ThreadID: "thread-review-comment", Account: "apikey"})
	runRef, _ := codexHTTPRun(t, m, h, admin, tenant, prof)
	input := "/v1/m/sessions/runs/" + runRef + "/input"
	place := func(path string, line int, side string) map[string]any {
		return map[string]any{"path": path, "line": line, "side": side}
	}

	plain := h.doJSON(http.MethodPost, input, admin, map[string]any{"text": "keep going"}, tenantHdr(tenant))
	if plain.code != http.StatusAccepted {
		t.Fatalf("plain input = %d %s", plain.code, plain.raw)
	}
	if n := reviewCommentRecords(t, m, tenant); n != 0 {
		t.Fatalf("a plain input left %d review-comment record(s)", n)
	}

	for name, bad := range map[string]map[string]any{
		"line zero":       place("core/a.go", 0, "modified"),
		"unknown side":    place("core/a.go", 4, "left"),
		"empty path":      place("", 4, "modified"),
		"path above tree": place("../a.go", 4, "modified"),
		"absolute path":   place("/etc/passwd", 4, "modified"),
	} {
		got := h.doJSON(http.MethodPost, input, admin,
			map[string]any{"text": "bad place " + name, "review_comment": bad}, tenantHdr(tenant))
		if got.code != http.StatusBadRequest {
			t.Errorf("%s: input = %d %s, want 400", name, got.code, got.raw)
		}
	}
	withImage := h.doJSON(http.MethodPost, input, admin,
		map[string]any{"text": "with an image", "attachments": []string{"any"}, "review_comment": place("core/a.go", 4, "modified")}, tenantHdr(tenant))
	if withImage.code != http.StatusBadRequest {
		t.Errorf("a review comment with attachments = %d %s, want 400", withImage.code, withImage.raw)
	}
	if n := reviewCommentRecords(t, m, tenant); n != 0 {
		t.Fatalf("refused review comments left %d record(s)", n)
	}

	sent := h.doJSON(http.MethodPost, input, admin,
		map[string]any{"text": "this loop never ends", "review_comment": place("core/a.go", 12, "modified")}, tenantHdr(tenant))
	if sent.code != http.StatusAccepted {
		t.Fatalf("review comment = %d %s, want 202", sent.code, sent.raw)
	}
	if n := reviewCommentRecords(t, m, tenant); n != 1 {
		t.Fatalf("an accepted review comment left %d record(s), want 1", n)
	}
}
