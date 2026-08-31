// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// the two engine defects the the model contrast found while auditing the
// console wiring of the regulatory-operations plane. Both are the same shape as the
// defect that session came to fix: a request that SUCCEEDS and does something other
// than what was asked, or fails as if the engine broke when the caller could see the
// request was unsatisfiable.

package compliance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// capturingDepthPackager records the CCMSnapshotInput it was handed. The shared
// stubDepthPackager discards it, which is exactly why the widening below was
// invisible: every existing cell asserts on the RESPONSE, and the response of a
// silently-widened snapshot looks identical to a correct one.
type capturingDepthPackager struct {
	stubDepthPackager
	gotInput      CCMSnapshotInput
	gotAssessment map[string]FrameworkAssessment
	calls         int
}

func (c *capturingDepthPackager) RunCCMSnapshot(
	_ context.Context,
	in CCMSnapshotInput,
	assessments map[string]FrameworkAssessment,
) (*CCMSnapshot, error) {
	c.calls++
	c.gotInput = in
	c.gotAssessment = assessments
	return c.ccmSnapshot, c.err
}

// doUnknownLength sends a body whose LENGTH IS UNKNOWN (ContentLength == -1), which is
// what Go reports for a chunked or streamed request.
//
// httptest.NewRequest sets ContentLength from the reader only for the concrete types it
// recognizes (*bytes.Reader, *bytes.Buffer, *strings.Reader); wrapping in io.NopCloser
// hides the type and leaves it at -1. That is not a contrivance: it is the shape of
// every chunked upload, and of any client that streams a body it has not buffered.
func doUnknownLength(
	h *harness, method, path, token, body string, tenant model.TenantID,
) resp {
	h.t.Helper()
	req := httptest.NewRequest(
		method, path, io.NopCloser(strings.NewReader(body)),
	)
	if req.ContentLength != -1 {
		h.t.Fatalf(
			"CONTROL FAILED: this request was supposed to have an unknown "+
				"length, so it cannot exercise the defect; got ContentLength=%d",
			req.ContentLength,
		)
	}
	req.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	if tenant != "" {
		req.Header.Set("X-Olivares-Tenant", tenant.String())
	}
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	out := resp{code: rec.Code, raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

// TestCCMSnapshotHonoursAnUnknownLengthBody is the regression cell for the widening.
//
// THE DEFECT. handleTriggerCCMSnapshot gated its decode on `r.ContentLength > 0`, and
// Go reports an unknown length as -1, not 0. So a request carrying a NARROWED framework
// list skipped decoding entirely and fell through to the empty-input branch — which on
// this route means EVERY catalog framework. The operator narrowed the governed scope,
// the engine widened it, and the answer was 201. Nothing anywhere said so.
//
// THE MUTANT THAT KILLS THIS CELL: restore `if r.ContentLength > 0 {` around the decode
// in depthhandlers.go. The first sub-test then sees the packager receive zero frameworks
// and an empty note.
func TestCCMSnapshotHonoursAnUnknownLengthBody(t *testing.T) {
	pack := &capturingDepthPackager{
		stubDepthPackager: stubDepthPackager{ccmSnapshot: sampleCCMSnapshot()},
	}
	h := newHarness(t, WithComplianceDepth(pack))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(admin, tenant, "o@x.io", "owner")

	t.Run("a narrowed selection survives an unknown length", func(t *testing.T) {
		r := doUnknownLength(h, "POST",
			"/v1/m/compliance/depth/ccm/snapshot", owner,
			`{"frameworks":["eu_ai_act"],"scope_note":"EU only"}`, tenant)
		if r.code != http.StatusCreated {
			t.Fatalf("want 201; got %d %s", r.code, r.raw)
		}
		if got := pack.gotInput.Frameworks; len(got) != 1 || got[0] != "eu_ai_act" {
			t.Fatalf(
				"the packager must receive the frameworks the operator chose; got %v",
				got,
			)
		}
		if pack.gotInput.ScopeNote != "EU only" {
			t.Fatalf("scope note lost; got %q", pack.gotInput.ScopeNote)
		}
		// The scope the snapshot was actually taken over — the fact the DTO cannot
		// show, and the one that makes this a widening rather than a cosmetic loss.
		if len(pack.gotAssessment) != 1 {
			t.Fatalf(
				"snapshot must cover ONLY the chosen framework; covered %d",
				len(pack.gotAssessment),
			)
		}
	})

	// THE NO-DISPARO DIRECTION. A guard that decoded nothing would pass the cell above
	// only if it also broke this one, and a guard that rejected everything would fail
	// here: absence of a body must still mean "the whole catalog", which is the
	// engine's documented behavior and a genuinely different action.
	t.Run("no body at all still means every catalog framework", func(t *testing.T) {
		before := pack.calls
		r := h.do("POST", "/v1/m/compliance/depth/ccm/snapshot",
			owner, nil, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("want 201; got %d %s", r.code, r.raw)
		}
		if pack.calls != before+1 {
			t.Fatalf("the packager was not called")
		}
		// The engine EXPANDS an empty selection to the whole catalog before the
		// packager sees it (depthhandlers.go:821-826), so "all" arrives as the
		// full list rather than as an empty one. Asserting emptiness here would
		// have been asserting my own expectation instead of the engine's rule —
		// the first version of this cell did exactly that and was wrong.
		if len(pack.gotInput.Frameworks) != len(frameworkByID) {
			t.Fatalf(
				"an absent body means every catalog framework (%d); got %d",
				len(frameworkByID), len(pack.gotInput.Frameworks),
			)
		}
		if len(pack.gotAssessment) != len(frameworkByID) {
			t.Fatalf(
				"the snapshot must cover the whole catalog (%d); covered %d",
				len(frameworkByID), len(pack.gotAssessment),
			)
		}
		// And the two answers must be genuinely DIFFERENT, or this cell and the
		// one above could both pass on a handler that ignores the body entirely.
		if len(frameworkByID) < 2 {
			t.Fatalf("catalog too small for this test to distinguish anything")
		}
	})

	// The unknown-field rule is not relaxed by decoding optionally. decodeOptionalJSON
	// differs from decodeJSON in ONE thing — io.EOF means "no body" — and this is the
	// cell that keeps the difference from widening into "anything goes".
	t.Run("an unknown field is still a 400, not an ignored key", func(t *testing.T) {
		r := doUnknownLength(h, "POST",
			"/v1/m/compliance/depth/ccm/snapshot", owner,
			`{"frameworks":["eu_ai_act"],"nope":1}`, tenant)
		if r.code != http.StatusBadRequest {
			t.Fatalf("want 400 for an unknown field; got %d %s", r.code, r.raw)
		}
	})

	t.Run("two documents in one body are still a 400", func(t *testing.T) {
		r := doUnknownLength(h, "POST",
			"/v1/m/compliance/depth/ccm/snapshot", owner,
			`{"frameworks":["eu_ai_act"]}{"frameworks":["soc2_tsc"]}`, tenant)
		if r.code != http.StatusBadRequest {
			t.Fatalf("want 400 for a concatenated body; got %d %s", r.code, r.raw)
		}
	})

	t.Run("an unknown length with an EMPTY body is not an error", func(t *testing.T) {
		// The case `!= 0` still gets wrong, and the reason decodeOptionalJSON reads
		// rather than trusting the header: there is no document, so there is nothing
		// malformed about it.
		r := doUnknownLength(h, "POST",
			"/v1/m/compliance/depth/ccm/snapshot", owner, "", tenant)
		if r.code != http.StatusCreated {
			t.Fatalf("want 201 for an empty optional body; got %d %s", r.code, r.raw)
		}
	})
}

// TestCCMDriftUnsatisfiableRequestIsAClientError pins the OTHER half.
//
// THE DEFECT. Both "there are fewer than two snapshots" and "this snapshot has no
// predecessor" returned a bare error, which writeDepthError passes to writeStoreError
// and which therefore surfaced as 500. Nothing failed in either case: the caller asked
// for a comparison the data cannot support. A console cannot tell an operator "retry
// later, the server broke" apart from "this needs two snapshots" when both are 500 —
// and the console was offering the impossible pin, because nothing told it not to.
//
// THE MUTANT THAT KILLS THIS CELL: unwrap either errDepthRejected in depthhandlers.go
// and the corresponding sub-test sees 500.
func TestCCMDriftUnsatisfiableRequestIsAClientError(t *testing.T) {
	pack := &capturingDepthPackager{
		stubDepthPackager: stubDepthPackager{ccmSnapshot: sampleCCMSnapshot()},
	}
	h := newHarness(t, WithComplianceDepth(pack))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(admin, tenant, "o@x.io", "owner")

	t.Run("fewer than two snapshots is 422, not 500", func(t *testing.T) {
		r := h.do("POST", "/v1/m/compliance/depth/ccm/drift",
			owner, nil, tenantHdr(tenant))
		if r.code != http.StatusUnprocessableEntity {
			t.Fatalf(
				"an estate with no snapshots cannot be compared, and that is the "+
					"caller's news, not a fault; want 422, got %d %s",
				r.code, r.raw,
			)
		}
	})

	// Take ONE snapshot, then pin it: the oldest has no predecessor by construction
	// (findPredecessor returns nil for i == 0), so this is the console's F3 case.
	rSnap := h.do("POST", "/v1/m/compliance/depth/ccm/snapshot",
		owner, nil, tenantHdr(tenant))
	if rSnap.code != http.StatusCreated {
		t.Fatalf("setup: snapshot must be created; got %d %s", rSnap.code, rSnap.raw)
	}
	id, _ := rSnap.body["id"].(string)
	if id == "" {
		t.Fatalf("setup: no snapshot id in %s", rSnap.raw)
	}

	t.Run("pinning the oldest snapshot is 422, not 500", func(t *testing.T) {
		r := h.do("POST",
			"/v1/m/compliance/depth/ccm/drift?snapshot_id="+id,
			owner, nil, tenantHdr(tenant))
		if r.code != http.StatusUnprocessableEntity {
			t.Fatalf(
				"the first snapshot has no predecessor; want 422, got %d %s",
				r.code, r.raw,
			)
		}
	})

	// NO-DISPARO: 422 must not become the answer to everything. An id the store cannot
	// resolve is a different refusal and keeps its own status.
	t.Run("an unknown snapshot id keeps its own status", func(t *testing.T) {
		r := h.do("POST",
			"/v1/m/compliance/depth/ccm/drift?snapshot_id=00000000-0000-0000-0000-000000000000",
			owner, nil, tenantHdr(tenant))
		if r.code == http.StatusUnprocessableEntity {
			t.Fatalf(
				"an unresolvable id is not the same news as an unsatisfiable "+
					"comparison; got %d %s", r.code, r.raw,
			)
		}
		if r.code < 400 {
			t.Fatalf("an unknown id must not succeed; got %d %s", r.code, r.raw)
		}
	})
}
