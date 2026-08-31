// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// THE CONSOLE'S OWN BYTES AGAINST THIS ENGINE'S OWN CONTRACT.
//
// Every other test in this package builds its request from a map[string]any the test
// itself writes, so it asserts that the engine agrees with the TEST, not that the engine
// agrees with the CONSOLE. That gap is not hypothetical: it is exactly how `tool-pinning`
// shipped a console whose two writes could never succeed (capabilities.test.tsx:189-192
// was green while toolpins.go answered 400, because the double accepted what production
// rejects — an internal design note (not shipped) §5.8).
//
// So the payloads below are VERBATIM the bytes web/src/features/compliance/api.ts puts on
// the wire for the retention plane, and they go through the real router, the real auth
// and the real handler.
//
// The direction of non-firing is the second half and the load-bearing one: an "accepts"
// assertion passes just as well against a handler that accepts EVERYTHING, so each accept
// is paired with the near-miss payload that must be refused.
//
// ⛔ WHAT THIS FILE DOES NOT CATCH, MEASURED RATHER THAN ASSUMED. These bytes are a HAND
// COPY of the console's payload, not a value derived from it, so a change on the CONSOLE
// side leaves this file green. Measured 2026-08-11 by adding `data_class: entry.id` to
// the request built in retention-view.tsx: this file stayed `ok`, and the cell that went
// red was retention.test.tsx "sends EXACTLY the four keys the engine decodes", which
// asserts the key set of the object actually handed to the client.
//
// ⇒ THE CONTRACT IS PINNED BY THE PAIR, AND EACH HALF ONLY SEES ITS OWN SIDE: this file
// fails if the ENGINE stops accepting these bytes, that cell fails if the CONSOLE stops
// sending them. Neither alone is the guard, and citing this file as if it covered both is
// the mistake the tool-pinning green test made in the other direction.

// consolePutPolicyBody is the body web/src/features/compliance/api.ts sends for
// PUT /retention/policies/{class} — built from RetentionPolicyInput (types.ts), whose
// four keys are the four fields of putPolicyRequest (retention.go:225-230).
//
// `basis` is omitted by the console when the operator left it blank
// (retention-view.tsx spreads it in only when non-empty), so both shapes are exercised.
const (
	consolePutRetainBody = `{"retention_days":365,"disposition":"retain","basis":"SOX 7y schedule","enabled":true}`
	consolePutNoBasis    = `{"retention_days":30,"disposition":"retain","enabled":true}`
	consolePutPurgeBody  = `{"retention_days":30,"disposition":"purge","basis":"ops retention schedule","enabled":true}`
	consolePutDisabled   = `{"retention_days":30,"disposition":"purge","basis":"drafted, not armed","enabled":false}`

	// The two remaining combinations of the same form, named by the sol-max contrast as
	// generable and unrepresented (§P2-2), plus the one value shape a free-text field
	// actually produces: JSON.stringify escapes quotes and newlines, and `basis` is a
	// textarea with no character restriction. Escaped content in a documented legal basis
	// is not exotic — it is a citation.
	consolePutPurgeNoBasis   = `{"retention_days":90,"disposition":"purge","enabled":true}`
	consolePutRetainDisabled = `{"retention_days":36500,"disposition":"retain","enabled":false}`
	consolePutEscapedBasis   = `{"retention_days":30,"disposition":"purge","basis":"policy says \"keep\"\nsecond line","enabled":true}`
)

// doRaw sends VERBATIM bytes, unlike harness.do which marshals a Go value. The point of
// this file is the bytes, so re-encoding them through a map would destroy the evidence:
// a map cannot express "the console also sent a data_class key", which is the mistake
// this contract is most exposed to.
func (h *harness) doRaw(method, path, token string, body string, tenant model.TenantID) resp {
	h.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
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

// TestConsoleRetentionPayloadIsAcceptedVerbatim is the acceptance criterion: the
// console's real request bytes reach the real handler and are accepted.
//
// THE MUTANT THAT KILLS IT (measured): delete dec.DisallowUnknownFields() from
// helpers.go:99 and "one extra key is refused" goes red on two of its three cases. That
// guard is what makes ONE stray key — `data_class`, duplicated from the path, the natural
// mistake for a path-parameterised route — a flat 400 on an otherwise perfect request,
// which is invisible to any console-side test with a mocked mutationFn.
func TestConsoleRetentionPayloadIsAcceptedVerbatim(t *testing.T) {
	gate := &stubApprovalGate{status: GateStatusApproved, ref: "ap-s696", approvers: []string{"user:approver"}}
	h := newHarness(t, WithApprovalGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adm := h.roleToken(admin, tenant, "a@x.io", "admin")

	base := s138Base + "/retention/policies/"

	t.Run("retain with a basis", func(t *testing.T) {
		r := h.doRaw("PUT", base+"audit.ledger", adm, consolePutRetainBody, tenant)
		if r.code != http.StatusOK {
			t.Fatalf("the console's own retain payload was refused: %d %s", r.code, r.raw)
		}
		// The console reads `enabled` and `disposition` OFF THIS DTO rather than assuming
		// what it asked for (retention-view.tsx, the allowlist in onSuccess), so the two
		// keys it branches on have to be here and have to be right.
		if r.body["enabled"] != true || r.body["disposition"] != "retain" {
			t.Fatalf("the DTO the console branches on is wrong: %s", r.raw)
		}
		if r.body["data_class"] != "audit.ledger" {
			t.Fatalf("data_class is the console's confirmation the write landed: %s", r.raw)
		}
	})

	t.Run("retain with basis omitted", func(t *testing.T) {
		// The console drops `basis` entirely when it is blank. A `string` field (not a
		// pointer) accepts the omission, and omitting it must CLEAR the stored basis
		// rather than preserve the old one — otherwise erasing the documented basis in
		// the UI would silently leave the previous justification attached.
		if r := h.doRaw("PUT", base+"voice.session", adm, consolePutRetainBody, tenant); r.code != http.StatusOK {
			t.Fatalf("seed = %d %s", r.code, r.raw)
		}
		r := h.doRaw("PUT", base+"voice.session", adm, consolePutNoBasis, tenant)
		if r.code != http.StatusOK {
			t.Fatalf("payload without basis was refused: %d %s", r.code, r.raw)
		}
		if b, ok := r.body["basis"]; ok && b != "" {
			t.Fatalf("omitting basis must clear it, got %q: %s", b, r.raw)
		}
	})

	t.Run("purge, enabled — the gated act", func(t *testing.T) {
		r := h.doRaw("PUT", base+"agent.memory", adm, consolePutPurgeBody, tenant)
		if r.code != http.StatusOK {
			t.Fatalf("the console's own purge payload was refused: %d %s", r.code, r.raw)
		}
		if r.body["enabled"] != true || r.body["approval_ref"] != "ap-s696" {
			t.Fatalf("an approved arm must come back enabled with its ref: %s", r.raw)
		}
	})

	t.Run("purge, disabled — enabled:false is honored, not defaulted", func(t *testing.T) {
		// `enabled` is a *bool server-side: absent means true (retention.go:270). The
		// console always sends it explicitly, and false has to survive — a schedule the
		// operator deliberately left off must not come back armed.
		r := h.doRaw("PUT", base+"session.timeline", adm, consolePutDisabled, tenant)
		if r.code != http.StatusOK {
			t.Fatalf("disabled purge payload = %d %s", r.code, r.raw)
		}
		if r.body["enabled"] != false {
			t.Fatalf("enabled:false must not be defaulted to true: %s", r.raw)
		}
		// And it must not have consulted the approval gate at all: nothing is armed.
		for _, req := range gate.requests() {
			if req.SubjectRef == "session.timeline" {
				t.Fatalf("a disabled purge opened an approval: %+v", req)
			}
		}
	})

	t.Run("the remaining form combinations and an escaped basis", func(t *testing.T) {
		// Named by the contrast rather than found by me: four samples are four samples,
		// and the form generates more. The escaped one is the interesting case — the
		// engine must store what the operator wrote, quotes and newline included, since
		// this string is the documented legal basis a schedule rests on.
		for name, tc := range map[string]struct {
			class string
			body  string
			basis string
		}{
			"purge with no basis":                  {class: "voice.session", body: consolePutPurgeNoBasis},
			"retain, disabled, at the upper bound": {class: "audit.ledger", body: consolePutRetainDisabled},
			"a basis carrying quotes and a newline": {
				class: "agent.memory", body: consolePutEscapedBasis,
				basis: "policy says \"keep\"\nsecond line",
			},
		} {
			r := h.doRaw("PUT", base+tc.class, adm, tc.body, tenant)
			if r.code != http.StatusOK {
				t.Errorf("%s: %d %s", name, r.code, r.raw)
				continue
			}
			if tc.basis != "" && r.body["basis"] != tc.basis {
				t.Errorf("%s: basis came back as %q, want %q — a documented basis is "+
					"evidence and must survive the round trip verbatim", name, r.body["basis"], tc.basis)
			}
		}
	})

	// --- the direction of non-firing ------------------------------------------------
	//
	// Without these, every assertion above would still pass against a handler that
	// accepted any JSON object at all, and the whole file would be measuring nothing.

	t.Run("one extra key is refused", func(t *testing.T) {
		// Two of these three are the oracle for DisallowUnknownFields. The typo is NOT,
		// and saying so matters: with the guard removed it still answers 400, because a
		// misspelled `disposition` decodes to "" and the vocabulary check rejects it
		// (dataclass.go:190). It is kept because it is the mistake an operator's client
		// actually makes, not because it measures the guard.
		for name, body := range map[string]string{
			"class duplicated from the path": `{"data_class":"audit.ledger","retention_days":365,"disposition":"retain","enabled":true}`,
			"a plausible typo":               `{"retention_days":365,"dispositon":"retain","enabled":true}`,
			"a field from another route":     `{"retention_days":365,"disposition":"retain","enabled":true,"reason":"cleanup"}`,
		} {
			r := h.doRaw("PUT", base+"audit.ledger", adm, body, tenant)
			if r.code != http.StatusBadRequest {
				t.Errorf("%s: want 400, got %d %s", name, r.code, r.raw)
			}
		}
	})

	t.Run("two documents in one body are refused", func(t *testing.T) {
		// helpers.go:104-114. A concatenation error must not perform a durable mutation
		// while answering about the first document only.
		r := h.doRaw("PUT", base+"audit.ledger", adm,
			consolePutRetainBody+consolePutRetainBody, tenant)
		if r.code != http.StatusBadRequest {
			t.Fatalf("want 400 for two documents, got %d %s", r.code, r.raw)
		}
	})
}

// TestConsoleReadsThePendingApprovalContract pins the 202 the retention console branches
// on. This is the answer that looks most like success and is the furthest from it: the
// schedule IS persisted, the purge is NOT in force, and nothing will be destroyed.
//
// THE MUTANT THAT KILLS IT: drop the `res.status === 202` branch from
// retention-view.tsx's onSuccess and the console reports "schedule saved and the purge is
// in force" over this exact body — the DTO assertions here are what say the 202 body is
// NOT a policy DTO, so the success branch would be reading fields that do not exist.
func TestConsoleReadsThePendingApprovalContract(t *testing.T) {
	gate := &stubApprovalGate{status: GateStatusPending, ref: "ap-pending-696"}
	h := newHarness(t, WithApprovalGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adm := h.roleToken(admin, tenant, "a@x.io", "admin")

	r := h.doRaw("PUT", s138Base+"/retention/policies/agent.memory", adm, consolePutPurgeBody, tenant)
	if r.code != http.StatusAccepted {
		t.Fatalf("a pending gate must answer 202, got %d %s", r.code, r.raw)
	}
	// The two keys the console reads off the 202 (retention-view.tsx setPending).
	if r.body["status"] != "pending_approval" {
		t.Fatalf("the 202 body must name itself pending_approval: %s", r.raw)
	}
	if r.body["approval_ref"] != "ap-pending-696" {
		t.Fatalf("the 202 must carry the approval ref the operator chases: %s", r.raw)
	}
	// It is NOT a policy DTO — the console must not fall through to the success branch
	// and read `enabled` off it, because the answer to "is the purge on" would be
	// `undefined`, which is falsy for the wrong reason.
	if _, ok := r.body["enabled"]; ok {
		t.Fatalf("the 202 body must not look like a policy DTO: %s", r.raw)
	}

	// AND THE POLICY IS PERSISTED, DISABLED. "Nothing was saved" and "saved but not
	// armed" are different answers and the console tells the operator the second one.
	lst := h.do("GET", s138Base+"/retention/policies", adm, nil, tenantHdr(tenant))
	items := itemsOf(t, lst)
	if len(items) != 1 {
		t.Fatalf("want the schedule persisted, got %v", items)
	}
	if items[0]["enabled"] != false || items[0]["disposition"] != "purge" {
		t.Fatalf("a pending purge must persist DISABLED: %v", items[0])
	}
	if items[0]["approval_ref"] != nil && items[0]["approval_ref"] != "" {
		t.Fatalf("an unapproved schedule must carry no approval ref: %v", items[0])
	}

	// A sweep now finds NOTHING armed — the console's own worklist rule (enabled &&
	// disposition==purge) is the engine's, so the sweep-scope dialog cannot promise a
	// deletion this pass would not perform.
	sw := h.do("POST", s138Base+"/retention/sweep", adm, nil, tenantHdr(tenant))
	if sw.code != http.StatusOK {
		t.Fatalf("sweep = %d %s", sw.code, sw.raw)
	}
	if intOf(sw.body["examined"]) != 0 || intOf(sw.body["purged"]) != 0 {
		t.Fatalf("a pending schedule must arm nothing: %s", sw.raw)
	}
}

// TestSweepSummaryClassesIsNullWhenNothingIsArmed pins a shape, not a behavior, and it
// is here because the console has to survive it.
//
// RetentionSummary.Classes is a Go slice with no omitempty (retention.go:500), so an
// un-armed tenant gets `"classes": null` — NOT `[]`. The shared list-envelope guard in
// the web client only covers the `items` key (client.ts:216-243), so this one reaches the
// view raw, and `summary.classes.map(...)` would crash the whole tab on the most ordinary
// state this screen has: a tenant that has scheduled nothing yet.
//
// THE MUTANT THAT KILLS IT: drop the `?? []` from SweepResultCard.
func TestSweepSummaryClassesIsNullWhenNothingIsArmed(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adm := h.roleToken(admin, tenant, "a@x.io", "admin")

	// The console posts NO body here (api.ts runRetentionSweep passes no body at all).
	r := h.doRaw("POST", s138Base+"/retention/sweep", adm, "", tenant)
	if r.code != http.StatusOK {
		t.Fatalf("a bodyless sweep must be accepted: %d %s", r.code, r.raw)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(r.raw), &raw); err != nil {
		t.Fatalf("sweep body is not an object: %s", r.raw)
	}
	if got := string(raw["classes"]); got != "null" {
		t.Fatalf("this test exists to pin `classes: null`; got %q. If the engine now "+
			"emits [], the `?? []` in SweepResultCard is merely redundant and this "+
			"test should be updated, not deleted: %s", got, r.raw)
	}
	if v, ok := r.body["truncated"]; !ok || v != false {
		t.Fatalf("truncated must be present and false on a clean pass: %s", r.raw)
	}
}

// TestDeletingAScheduleAnswers204WithNoBody pins what the console types as `void`.
//
// The handler calls writeJSON(w, 204, nil) (retention.go:441), which sets a JSON
// content-type and writes NO body. The web client special-cases 204 before reading
// (client.ts:156), so a body here would be silently discarded — and a 200-with-body would
// flow into `data` as a shape nothing declares. Pinning the status is what keeps the
// console's "removed" toast honest.
func TestDeletingAScheduleAnswers204WithNoBody(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adm := h.roleToken(admin, tenant, "a@x.io", "admin")

	if r := h.doRaw("PUT", s138Base+"/retention/policies/finops.cost_sample", adm,
		consolePutRetainBody, tenant); r.code != http.StatusOK {
		t.Fatalf("seed = %d %s", r.code, r.raw)
	}
	r := h.do("DELETE", s138Base+"/retention/policies/finops.cost_sample", adm, nil, tenantHdr(tenant))
	if r.code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", r.code, r.raw)
	}
	if r.raw != "" {
		t.Fatalf("204 must carry no body, got %q", r.raw)
	}
	// Direction of non-firing: a handler that answered 204 to everything would pass the
	// line above. A second delete has nothing to remove and says so.
	if r := h.do("DELETE", s138Base+"/retention/policies/finops.cost_sample", adm, nil, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("deleting a missing schedule = %d %s", r.code, r.raw)
	}
}
