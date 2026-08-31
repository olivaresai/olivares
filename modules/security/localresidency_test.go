// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// publishResidency emits exactly what connectors/local/local.go residencyPosture
// builds: kind `posture` (NOT `safety_posture`), subject kind `local.residency`, one
// finding per resident model, and the severity taken FROM THE PLACEMENT.
//
// The shape is copied from the connector rather than invented, because the defect this
// file exists to close was a test that asserted against a shape production cannot
// produce: a row was handed straight to the table component while the ingestion path
// silently dropped the real one.
func (h *harness) publishResidency(tenant model.TenantID, subjectRef, detailHash, title string, sev sdkmodel.Severity) {
	h.t.Helper()
	_ = h.bus.Publish(context.Background(), event.FromObservation(tenant.String(), "olivares.local", sdkmodel.FindingReport{
		Kind:        "posture",
		Severity:    sev,
		SubjectKind: "local.residency",
		SubjectRef:  subjectRef,
		Title:       title,
		DetailHash:  detailHash,
		OccurredAt:  time.Now(),
	}))
}

// TestLocalResidencyPersistsAtConnectorSeverity proves the carve-out end to end: a
// Medium residency row survives ingestion (the HIGH+ rule dropped it before), reaches
// GET /findings as the kind and severity THE CONNECTOR SET, dedups on a re-poll, and
// records a fresh row when the placement actually changes.
//
// Without the carve-out every assertion below fails at step 1 — the row never lands.
func TestLocalResidencyPersistsAtConnectorSeverity(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	list := func(query string) []any {
		r := h.do("GET", "/v1/m/security/findings?"+query, admin, nil, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("findings = %d %s", r.code, r.raw)
		}
		items, _ := r.body["items"].([]any)
		return items
	}
	resident := func() []any { return list("subject_kind=local.residency") }
	// GET /findings filters on kind/severity/status/source/subject_kind only — there is
	// no subject_ref filter (dto.go:93), and subject_ref is metadata, not a column. So
	// the per-model count is done HERE, over the rows the API really returns.
	forModel := func(ref string) int {
		n := 0
		for _, it := range resident() {
			row, _ := it.(map[string]any)
			if got, _ := row["subject_ref"].(string); got == ref {
				n++
			}
		}
		return n
	}

	// 1) A model SPLIT between GPU and CPU is Medium. Before the carve-out this fell to
	//    `default: return nil` and no surface could ever show it.
	h.publishResidency(tenant, "llama3:8b", "r1",
		"Ollama model resident on split gpu/cpu: llama3:8b (3221225472 of 8589934592 bytes in VRAM)",
		sdkmodel.SeverityMedium)
	var rows []any
	for i := 0; i < 200; i++ {
		if rows = resident(); len(rows) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(rows) != 1 {
		t.Fatalf("residency rows = %d, want 1 (a Medium posture must survive ingestion)", len(rows))
	}

	// The connector's OWN kind, severity and origin survive. The HIGH+ arm would have
	// rewritten kind to `anomaly` and source to the finding's kind, so this is what
	// proves the carve-out runs BEFORE it and keeps the connector's classification.
	row, _ := rows[0].(map[string]any)
	if got, _ := row["kind"].(string); got != "posture" {
		t.Errorf("kind = %q, want %q (the HIGH+ arm rewrites it to anomaly)", got, "posture")
	}
	if got, _ := row["severity"].(string); got != "medium" {
		t.Errorf("severity = %q, want %q — the connector decides the placement severity, not this module", got, "medium")
	}
	if got, _ := row["source"].(string); got != "olivares.local" {
		t.Errorf("source = %q, want the emitting connector %q", got, "olivares.local")
	}

	// 2) The next poll re-observes the SAME placement: same deterministic hash ⇒ no new
	//    row. The sentinel is a DIFFERENT model, so its arrival proves the duplicate was
	//    actually processed — a fixed sleep would pass vacuously.
	h.publishResidency(tenant, "llama3:8b", "r1",
		"Ollama model resident on split gpu/cpu: llama3:8b (3221225472 of 8589934592 bytes in VRAM)",
		sdkmodel.SeverityMedium)
	h.publishResidency(tenant, "qwen2:7b", "s1",
		"Ollama model resident on gpu: qwen2:7b (4294967296 of 4294967296 bytes in VRAM)",
		sdkmodel.SeverityInfo)
	for i := 0; i < 200; i++ {
		if len(resident()) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := forModel("llama3:8b"); n != 1 {
		t.Fatalf("llama3:8b rows after an unchanged re-poll = %d, want 1 (dedup on the deterministic hash)", n)
	}
	// The Info sentinel is ALSO below HIGH: it proves the carve-out admits the whole
	// severity range the connector uses, not just the Medium one asserted above.
	if n := forModel("qwen2:7b"); n != 1 {
		t.Fatalf("qwen2:7b rows = %d, want 1 (a fully-resident Info row must persist too)", n)
	}

	// 3) The model MOVES from split to fully resident: a real state change, a different
	//    hash, a fresh row. The transition is the event an operator needs.
	h.publishResidency(tenant, "llama3:8b", "r2",
		"Ollama model resident on gpu: llama3:8b (8589934592 of 8589934592 bytes in VRAM)",
		sdkmodel.SeverityInfo)
	for i := 0; i < 200; i++ {
		if forModel("llama3:8b") >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := forModel("llama3:8b"); n != 2 {
		t.Fatalf("llama3:8b rows after a placement CHANGE = %d, want 2 (a new hash must persist)", n)
	}
}

// TestLocalResidencyKeepsItsKindAtHighSeverity is what makes the ORDER of the switch
// arm load-bearing, and without it that ordering is an untested comment.
//
// Every case above rides Info/Medium, which the generic arm ignores anyway — so the
// carve-out could sit AFTER it and all of them would still pass. A HIGH residency row
// is the one input the two arms fight over: the generic arm rewrites kind to `anomaly`
// and source to the finding's own kind, erasing both the connector's classification
// and its origin. The connector does not emit High today, but nothing stops a future
// placement rule from doing so, and the row must not change meaning when it does.
func TestLocalResidencyKeepsItsKindAtHighSeverity(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	resident := func() []any {
		r := h.do("GET", "/v1/m/security/findings?subject_kind=local.residency", admin, nil, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("findings = %d %s", r.code, r.raw)
		}
		items, _ := r.body["items"].([]any)
		return items
	}

	h.publishResidency(tenant, "llama3:70b", "h1",
		"Ollama model resident on cpu: llama3:70b (0 of 42949672960 bytes in VRAM)",
		sdkmodel.SeverityHigh)
	var rows []any
	for i := 0; i < 200; i++ {
		if rows = resident(); len(rows) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(rows) != 1 {
		t.Fatalf("residency rows = %d, want 1", len(rows))
	}
	row, _ := rows[0].(map[string]any)
	if got, _ := row["kind"].(string); got != "posture" {
		t.Errorf("kind = %q, want %q — the carve-out must run BEFORE the generic HIGH+ arm, "+
			"which rewrites kind to anomaly", got, "posture")
	}
	if got, _ := row["source"].(string); got != "olivares.local" {
		t.Errorf("source = %q, want %q — the generic arm replaces source with the finding's kind", got, "olivares.local")
	}
	if got, _ := row["severity"].(string); got != "high" {
		t.Errorf("severity = %q, want %q (painted, never recomputed)", got, "high")
	}
}

// TestOtherPostureFindingsStayDropped is the NON-FIRING direction, and it is the whole
// reason the carve-out is one subject kind rather than the `posture` family: 85 sites
// across the connector tree emit `Kind: "posture"` and 56 are below HIGH. Admitting all
// of them would change what GET /findings contains for every existing deployment.
//
// If someone later widens the case to `f.Kind == "posture"`, this goes red and says so.
func TestOtherPostureFindingsStayDropped(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	list := func(query string) []any {
		r := h.do("GET", "/v1/m/security/findings?"+query, admin, nil, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("findings = %d %s", r.code, r.raw)
		}
		items, _ := r.body["items"].([]any)
		return items
	}

	// A Medium `posture` finding from another connector, differing from the admitted one
	// ONLY in its subject kind.
	_ = h.bus.Publish(context.Background(), event.FromObservation(tenant.String(), "olivares.openrouter", sdkmodel.FindingReport{
		Kind: "posture", Severity: sdkmodel.SeverityMedium,
		SubjectKind: "openrouter.policy", SubjectRef: "org", Title: "some other posture",
		DetailHash: "x1", OccurredAt: time.Now(),
	}))
	// The admitted kind is the SENTINEL: once it lands, the bus has drained past the row
	// above, so its absence is a decision and not a race.
	h.publishResidency(tenant, "llama3:8b", "r1", "Ollama model resident on gpu: llama3:8b", sdkmodel.SeverityInfo)
	for i := 0; i < 200; i++ {
		if len(list("subject_kind=local.residency")) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := len(list("subject_kind=local.residency")); n != 1 {
		t.Fatalf("sentinel rows = %d, want 1 — without it the assertion below is vacuous", n)
	}
	if n := len(list("subject_kind=openrouter.policy")); n != 0 {
		t.Errorf("other posture rows = %d, want 0: the carve-out must admit ONE subject kind, "+
			"not the whole posture family (56 emission sites below HIGH)", n)
	}
}
