// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package inferenceproxy

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// evidencefailmode_test.go is the FAIL-MODE MATRIX for evidence: what each surface does when
// the ledger write cannot happen. One row, one test, no row without a decision.
//
// It exists because "mandatory by design" is not one property. There are three distinct
// moments and only one of them can refuse:
//
//	PRE-FORWARD  the intent anchor. The call has NOT happened. Refusing is possible and,
//	             under the mandatory posture, required: no evidence ⇒ no privileged action.
//	POST-FORWARD the finalize. The call HAS happened; there is nothing left to refuse, and
//	             pretending otherwise would be theater. The honest posture is a LOUD GAP.
//	CONFIG       the write that sets the posture. An omitted field must not decide it.
//
// Writing the matrix down is the point: the previous state of this module had a single bool
// defaulting to false, and "what happens when evidence fails" had no stated answer per
// surface — which is how a product ends up with an evidence guarantee it does not have.

// evidenceFailModeRow is one surface and its declared behavior when evidence cannot be
// written.
type evidenceFailModeRow struct {
	surface   string
	moment    string
	mandatory bool
	// origin says WHERE the mandatory posture came from: "configured" when the tenant wrote
	// it, "default" when nobody did. It is a dimension because a default and a choice are not
	// the same claim, and the whole precedence rule turns on telling them apart.
	origin string
	// fault is the evidence fault this row decides for. "" means "any fault".
	fault string
	// behavior is the declared outcome: deny | noisy-gap.
	behavior string
	why      string
}

// evidenceFailModeMatrix is the declared matrix. It is a written-down literal and the count
// below is written down too: a surface added without a decision here is a surface whose
// evidence behavior nobody chose.
func evidenceFailModeMatrix() []evidenceFailModeRow {
	return []evidenceFailModeRow{
		{
			surface: "single message", moment: "pre-forward intent anchor", mandatory: true,
			origin: "configured", fault: "",
			behavior: "deny",
			why:      "the forward has not happened; the anchor binds the effective digest, so refusing costs nothing but the call",
		},
		{
			// THE ROW THE MATRIX WAS MISSING, and the contrast found it by measuring the
			// consequence rather than reading the code: with mandatory defaulted on, an
			// operator who set the spool to `degrade` stopped getting degradation for every
			// tenant that had configured nothing. A default canceled a choice.
			surface: "single message", moment: "pre-forward intent anchor", mandatory: true,
			origin: "default", fault: "spool_degraded",
			behavior: "noisy-gap",
			why:      "the operator declared `degrade` for the spool, which says in as many words: when it is exhausted, drop the evidence and keep serving. A posture this tenant never chose must not cancel one the operator did",
		},
		{
			surface: "single message", moment: "pre-forward intent anchor", mandatory: true,
			origin: "configured", fault: "spool_degraded",
			behavior: "deny",
			why:      "a tenant that wrote record_mandatory=true asked for evidence-or-refuse; its own choice outranks the spool's, so the drop is a refusal",
		},
		{
			surface: "single message", moment: "pre-forward intent anchor", mandatory: true,
			origin: "default", fault: "spool_full",
			behavior: "deny",
			why:      "`block` is the operator declaring deny-closed on exhaustion. Yielding here would invert THAT choice instead of honoring it — the yield is about degrade, not about the spool",
		},
		{
			surface: "single message", moment: "pre-forward intent anchor", mandatory: true,
			origin: "default", fault: "ledger_unavailable",
			behavior: "deny",
			why:      "nobody chose an unreachable ledger. Only a declared policy earns the yield; a fault is not a decision",
		},
		{
			surface: "single message", moment: "pre-forward intent anchor", mandatory: false,
			behavior: "noisy-gap",
			why:      "an operator turned the posture off explicitly; the gap is theirs and it is logged, never silent",
		},
		{
			surface: "batch submit", moment: "pre-forward intent anchor", mandatory: true,
			behavior: "deny",
			why:      "one anchor for the whole submission; a batch that cannot be evidenced is not forwarded in part",
		},
		{
			surface: "batch submit", moment: "pre-forward intent anchor", mandatory: false,
			behavior: "noisy-gap",
			why:      "same as the single message: an explicit opt-out, logged",
		},
		{
			surface: "any", moment: "post-forward finalize", mandatory: true,
			behavior: "noisy-gap",
			why:      "the call already happened. A deny here would refuse a request that has already been served, so the only honest outcome is a loud gap — and the posture must not be described as if it covered this moment",
		},
		{
			surface: "any", moment: "post-forward finalize", mandatory: false,
			behavior: "noisy-gap",
			why:      "unchanged by the posture, for the same reason",
		},
	}
}

// TestEvidenceFailModeMatrixIsTotal keeps the matrix from acquiring a row nobody decided, and
// from losing one silently.
func TestEvidenceFailModeMatrixIsTotal(t *testing.T) {
	rows := evidenceFailModeMatrix()
	if len(rows) != 10 {
		t.Fatalf("the fail-mode matrix carries %d rows and this test expects 10: a surface added without a decision is a surface whose evidence behavior nobody chose, and one removed without updating this is a decision that vanished", len(rows))
	}
	seen := map[string]bool{}
	for _, r := range rows {
		key := r.surface + "|" + r.moment + "|" + map[bool]string{true: "mandatory", false: "best-effort"}[r.mandatory] +
			"|" + r.origin + "|" + r.fault
		if seen[key] {
			t.Errorf("duplicate matrix row %q: two rows for one situation means neither decides it", key)
		}
		seen[key] = true
		switch r.behavior {
		case "deny", "noisy-gap":
		default:
			t.Errorf("row %q declares behavior %q, which is not one of deny|noisy-gap", key, r.behavior)
		}
		if r.why == "" {
			t.Errorf("row %q has no reason; a matrix cell without a reason is a value somebody will change without knowing what it protects", key)
		}
	}
}

// TestOnlyADeclaredDegradeEarnsTheYield pins the precedence rule at its narrowest, because a
// rule stated loosely is a rule somebody widens later: a DEFAULT yields, a CHOICE does not, and
// only the operator-declared degrade counts as the choice it yields to.
func TestOnlyADeclaredDegradeEarnsTheYield(t *testing.T) {
	for _, r := range evidenceFailModeMatrix() {
		if r.behavior != "noisy-gap" || !r.mandatory {
			continue
		}
		if r.moment == "post-forward finalize" {
			continue // that moment is a gap for a different reason: the call already happened
		}
		if r.origin != "default" {
			t.Errorf("%s/%s yields with origin %q: a posture the tenant CHOSE must never be overridden by the spool's",
				r.surface, r.moment, r.origin)
		}
		if r.fault != "spool_degraded" {
			t.Errorf("%s/%s yields on fault %q: only the operator-declared degrade is a decision; every other fault is a failure, and a failure is not a choice",
				r.surface, r.moment, r.fault)
		}
	}
}

// TestOnlyThePreForwardMomentCanRefuse pins the property that makes the matrix honest: the
// posture governs the pre-forward anchor and nothing else. A future edit that claimed
// mandatory recording covered the post-forward path would have to change this test, and
// changing it means stating the claim out loud.
func TestOnlyThePreForwardMomentCanRefuse(t *testing.T) {
	for _, r := range evidenceFailModeMatrix() {
		if r.moment == "post-forward finalize" && r.behavior != "noisy-gap" {
			t.Errorf("%s/%s declares %q: after the forward the call has happened and there is nothing left to refuse",
				r.surface, r.moment, r.behavior)
		}
		// MANDATORY PRE-FORWARD DENIES, WITH EXACTLY ONE EXEMPTION, and this assertion used
		// to have none. It was written before the matrix had an origin/fault dimension, so it
		// said "mandatory always denies" — true of every row that existed then. The exemption
		// is not a softening of the doctrine: it is the doctrine applied one level up, where a
		// posture nobody chose must not cancel one the operator did. TestOnlyADeclaredDegrade
		// EarnsTheYield is what keeps the exemption from widening.
		exempt := r.origin == "default" && r.fault == "spool_degraded"
		if r.moment == "pre-forward intent anchor" && r.mandatory && !exempt && r.behavior != "deny" {
			t.Errorf("%s/%s under the mandatory posture declares %q, want deny: no evidence, no privileged action",
				r.surface, r.moment, r.behavior)
		}
		if r.moment == "pre-forward intent anchor" && !r.mandatory && r.behavior != "noisy-gap" {
			t.Errorf("%s/%s with the posture off declares %q, want noisy-gap", r.surface, r.moment, r.behavior)
		}
	}
}

// TestAnOmittedRecordMandatoryKeepsTheMandatoryPosture is the config row of the matrix, and
// the defect it closes is the one that made the whole doctrine opt-out-by-accident.
//
// record_mandatory was a bare bool on the wire. Go's zero value made "this PUT was about the
// DLP mode and never mentioned evidence" indistinguishable from "the operator turned evidence
// off", and every config write that omitted the field silently opted the tenant out.
func TestAnOmittedRecordMandatoryKeepsTheMandatoryPosture(t *testing.T) {
	t.Run("omitted keeps it on", func(t *testing.T) {
		var dto configDTO // every field at its zero value, exactly like a PUT that never mentioned it
		if got := dto.fields("tester")[colRecordMandatory]; got != true {
			t.Fatalf("an omitted record_mandatory wrote %v; a field nobody mentioned must not turn the evidence guarantee off", got)
		}
	})
	t.Run("an explicit false turns it off, because that is a decision", func(t *testing.T) {
		off := false
		dto := configDTO{RecordMandatory: &off}
		if got := dto.fields("tester")[colRecordMandatory]; got != false {
			t.Fatalf("an explicit false wrote %v; an operator must be able to opt out deliberately", got)
		}
	})
	t.Run("an explicit true is preserved", func(t *testing.T) {
		on := true
		dto := configDTO{RecordMandatory: &on}
		if got := dto.fields("tester")[colRecordMandatory]; got != true {
			t.Fatalf("an explicit true wrote %v", got)
		}
	})
	t.Run("the DTO round-trips the policy", func(t *testing.T) {
		for _, want := range []bool{true, false} {
			dto := policyToDTO(ProxyPolicy{RecordMandatory: want})
			if dto.RecordMandatory == nil {
				t.Fatalf("policyToDTO dropped record_mandatory for %v: a reader would then see the default and not the tenant's actual posture", want)
			}
			if *dto.RecordMandatory != want {
				t.Errorf("round-trip of %v gave %v", want, *dto.RecordMandatory)
			}
		}
	})
}

// TestAPartialPutDoesNotRevokeAnExplicitOptOut is the mirror image of the defect this change
// set out to fix, and the first version of the fix created it.
//
// `nil => true` is right when the row is being CREATED: a deployment that never said anything
// gets the safe posture. Applied to an UPDATE it is the same silent overwrite pointing the
// other way — a PUT about the DLP mode turns an operator's deliberate opt-out back on. The
// repository already issues partial PUTs, so this is a live sequence.
//
// IT DRIVES THE HANDLER, and the first version of this test did not. That one re-implemented
// the merge rule inline and asserted its own arithmetic: mutating the production branch left it
// green, which makes it a test that reads as coverage and is not. Measured, then rewritten.
func TestAPartialPutDoesNotRevokeAnExplicitOptOut(t *testing.T) {
	m, st, tenant := newPolicyHarness(t)
	put := func(body string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/config", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		m.handlePutConfig(rec, req, policyModuleContextRole(st, tenant, auth.RoleAdmin))
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT %s = %d %s", body, rec.Code, rec.Body.String())
		}
	}
	mandatory := func() bool {
		t.Helper()
		pol, err := m.Policy(context.Background(), tenant)
		if err != nil {
			t.Fatalf("read the policy back: %v", err)
		}
		return pol.RecordMandatory
	}

	// The operator opts out DELIBERATELY.
	put(`{"record_mandatory":false}`)
	if mandatory() {
		t.Fatal("an explicit opt-out did not take effect")
	}

	// A later PUT about something else must not undo it.
	put(`{"fail_open":true}`)
	if mandatory() {
		t.Fatal("a PUT that never mentioned record_mandatory revoked the operator's explicit opt-out: the create-time default must not be applied to an update")
	}

	// And an explicit re-enable still works, because the rule is about SILENCE, not about
	// making the field immutable.
	put(`{"record_mandatory":true}`)
	if !mandatory() {
		t.Fatal("an explicit re-enable did not take effect")
	}
}

// TestTheConfigEventSealsWhatWasWritten: the audit event must agree with the row it describes.
//
// It used to seal the REQUEST's value through the create-time default, so a partial PUT sealed
// `record_mandatory: true` while the row correctly kept the operator's `false`. In a product
// whose claim is tamper-evident history, a ledger asserting a choice nobody made is worse than
// no ledger entry at all — and it is the kind of falsehood that reads as evidence.
func TestTheConfigEventSealsWhatWasWritten(t *testing.T) {
	m, st, tenant := newPolicyHarness(t)
	put := func(body string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/config", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		m.handlePutConfig(rec, req, policyModuleContextRole(st, tenant, auth.RoleAdmin))
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT %s = %d %s", body, rec.Code, rec.Body.String())
		}
	}
	put(`{"record_mandatory":false}`)
	put(`{"fail_open":true}`) // the partial PUT that never mentions evidence

	pol, err := m.Policy(context.Background(), tenant)
	if err != nil {
		t.Fatalf("read the policy: %v", err)
	}
	var sealed []string
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			return errors.New("audit log does not expose canonical walk")
		}
		return walker.WalkCanonical(context.Background(), 0, func(ev model.AuditEvent, meta string, _ []byte) error {
			if ev.Action == "inferenceproxy.config.put" {
				sealed = append(sealed, meta)
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("walk the ledger: %v", err)
	}
	if len(sealed) != 2 {
		t.Fatalf("expected 2 config events, got %d: this test measures nothing without both", len(sealed))
	}
	last := sealed[len(sealed)-1]
	want := `"record_mandatory":false`
	if pol.RecordMandatory {
		want = `"record_mandatory":true`
	}
	if !strings.Contains(last, want) {
		t.Fatalf("the row says record_mandatory=%v and the event sealed something else:\n  event: %s\n  want to contain: %s",
			pol.RecordMandatory, last, want)
	}
}

// TestAPartialPutDoesNotUnmakeAnExplicitCHOICE is the sibling of the test above, and it exists
// because the fix for that one carried the very defect it was written to remove.
//
// Two columns record the posture: the VALUE (record_mandatory) and the PROVENANCE
// (record_mandatory_chosen, whose NULL means nobody decided). handlePutConfig preserved the
// value across a partial PUT by capturing it BEFORE the merge loop — the comment there says
// so, and says the first attempt read it after and therefore corrected nothing. The
// provenance was then preserved by a reader placed AFTER that same loop: by then the loop had
// already written `d.RecordMandatory != nil`, which a partial PUT makes false. The
// "preservation" restored false over false and the line that looked like its capture,
// `_, _ = existing[colRecordMandatoryChosen]`, discarded both values.
//
// WHY IT MATTERS, in the module's own terms: an operator who wrote record_mandatory=true asked
// for evidence-or-refuse, and the matrix row `configured/spool_degraded` declares that a DENY.
// Downgrade their provenance to "nobody chose" and defaultMandatoryYieldsTo yields instead —
// so the tenant that most explicitly demanded evidence gets a forwarded call with a gap. The
// console makes this the common path, not the exotic one: it PUTs the whole normalised object
// on every save, so any unrelated change re-issues a partial PUT for this field.
//
// MUTATIONS THAT MUST TURN THIS RED:
//
//  1. Move the prevChosen read back after the `for k, v := range fields` loop. Red in `an
//     unrelated PUT keeps the choice`.
//  2. Drop the whole preservation block. Red in the same subtest.
//  3. Make `fields` set colRecordMandatoryChosen to true unconditionally. Red in `a tenant
//     that never chose is not promoted into having chosen`.
func TestAPartialPutDoesNotUnmakeAnExplicitCHOICE(t *testing.T) {
	m, st, tenant := newPolicyHarness(t)
	put := func(body string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/config", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		m.handlePutConfig(rec, req, policyModuleContextRole(st, tenant, auth.RoleAdmin))
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT %s = %d %s", body, rec.Code, rec.Body.String())
		}
	}
	read := func() ProxyPolicy {
		t.Helper()
		pol, err := m.Policy(context.Background(), tenant)
		if err != nil {
			t.Fatalf("read the policy back: %v", err)
		}
		return pol
	}

	t.Run("an unrelated PUT keeps the choice", func(t *testing.T) {
		put(`{"record_mandatory":true}`)
		if pol := read(); !pol.RecordMandatoryChosen {
			t.Fatal("an explicit record_mandatory=true was not recorded as a choice")
		}
		put(`{"fail_open":true}`) // never mentions evidence
		pol := read()
		if !pol.RecordMandatory {
			t.Error("the partial PUT changed the posture itself")
		}
		if !pol.RecordMandatoryChosen {
			t.Fatal("a PUT that never mentioned record_mandatory erased the operator's CHOICE: " +
				"the tenant now reads as `nobody decided`, so a spool_degraded makes it yield and " +
				"forward with an evidence gap — the exact opposite of the configured/spool_degraded row")
		}
	})

	t.Run("an explicit opt-out is a choice too, and survives", func(t *testing.T) {
		m, st, tenant = newPolicyHarness(t)
		put(`{"record_mandatory":false}`)
		put(`{"response_dlp_mode":"flag"}`)
		pol := read()
		if pol.RecordMandatory {
			t.Error("the partial PUT revoked the explicit opt-out")
		}
		if !pol.RecordMandatoryChosen {
			t.Error("the explicit opt-out stopped counting as a decision after an unrelated PUT")
		}
	})

	t.Run("a tenant that never chose is not promoted into having chosen", func(t *testing.T) {
		m, st, tenant = newPolicyHarness(t)
		put(`{"fail_open":true}`) // creates the row without ever mentioning evidence
		if pol := read(); pol.RecordMandatoryChosen {
			t.Fatal("a config write that never mentioned evidence was recorded as an evidence CHOICE: " +
				"a default that fabricates a decision is the defect the column exists to prevent")
		}
		put(`{"response_dlp_mode":"flag"}`)
		if pol := read(); pol.RecordMandatoryChosen {
			t.Fatal("a second unrelated PUT promoted the default into a choice")
		}
	})
}

// TestALegacyExplicitTrueIsNotReadAsADefault closes the gap the adoption contrast found in
// the fix above: the fix is correct for every row written since the provenance column
// existed, and its universe starts there.
//
// A row written BEFORE the column scans as NULL, and NULL reads as "nobody chose" — right
// for every value but one. The wire field used to be a bare bool (`RecordMandatory bool`,
// no pointer), so encoding/json zeroed it whenever the request omitted it and the handler
// stored that literal. `true` is therefore the one posture the old format could not produce
// by accident: a legacy `true` is a PROVEN explicit choice.
//
// Reading it as a default is not a cosmetic misfiling. defaultMandatoryYieldsTo yields for
// a tenant that never chose, so on a declared spool `degrade` that operator's call is
// forwarded with an evidence gap — the exact outcome they configured evidence-or-refuse to
// prevent, silently switched on by an upgrade they did not ask for.
//
// A legacy `false` stays ambiguous and stays "nobody chose": an omission and a deliberate
// opt-out are indistinguishable in that data. Nothing turns on it, because with the value
// false the mandatory branch is never entered at all.
//
// MUTATIONS THAT MUST TURN THIS RED:
//
//  1. Make recordMandatoryChosen return rec.Bool(colRecordMandatoryChosen) again. Red in
//     `a legacy true is an explicit choice`.
//  2. Make it fall back to `true` for any NULL. Red in `a legacy false stays undecided`.
//  3. Make it ignore the column when it IS present. Red in `a post-column row is believed`.
func TestALegacyExplicitTrueIsNotReadAsADefault(t *testing.T) {
	legacy := func(value any) model.Record {
		r := model.Record{colRecordMandatory: true}
		if value != nil {
			r[colRecordMandatoryChosen] = value
		}
		return r
	}
	cases := []struct {
		name string
		rec  model.Record
		want bool
	}{
		// The two shapes a pre-column row can take: the SQL scanner materializes the
		// key with a nil value, and a backend that omitted the key entirely would be
		// the same statement. Both must reach the same answer.
		{"a legacy true is an explicit choice (NULL column)", legacy(nil), true},
		{"a legacy true is an explicit choice (key absent)", model.Record{colRecordMandatory: true}, true},
		{"a legacy false stays undecided", model.Record{colRecordMandatory: false}, false},
		// Once the column exists it is the only authority: a row created by an
		// omitting PUT carries value=true with chosen=false, and that pair must NOT
		// be promoted the way a legacy row is.
		{"a post-column row is believed over the value (false)", legacy(false), false},
		{"a post-column row is believed over the value (true)", legacy(true), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := recordMandatoryChosen(c.rec); got != c.want {
				t.Fatalf("recordMandatoryChosen = %v, want %v", got, c.want)
			}
		})
	}
}

// TestTheConfigEventSealsTheProvenanceToo: the value is not the whole posture. The
// provenance column — not the value — decides whether a spool `degrade` forwards the call
// or denies it, and the event did not carry it.
//
// The sequence below changes enforcement WITHOUT changing the value, so an event that seals
// only the value renders the two writes identical. An evidence ledger that cannot show when
// enforcement changed is not evidence of enforcement.
func TestTheConfigEventSealsTheProvenanceToo(t *testing.T) {
	m, st, tenant := newPolicyHarness(t)
	put := func(body string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/config", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		m.handlePutConfig(rec, req, policyModuleContextRole(st, tenant, auth.RoleAdmin))
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT %s = %d %s", body, rec.Code, rec.Body.String())
		}
	}
	put(`{}`)                        // creates the row: value true, nobody chose
	put(`{"record_mandatory":true}`) // same value, and NOW somebody chose

	var sealed []string
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			return errors.New("audit log does not expose canonical walk")
		}
		return walker.WalkCanonical(context.Background(), 0, func(ev model.AuditEvent, meta string, _ []byte) error {
			if ev.Action == "inferenceproxy.config.put" {
				sealed = append(sealed, meta)
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("walk the ledger: %v", err)
	}
	if len(sealed) != 2 {
		t.Fatalf("expected 2 config events, got %d: this test measures nothing without both", len(sealed))
	}
	if !strings.Contains(sealed[0], `"record_mandatory_chosen":false`) {
		t.Errorf("the creating PUT sealed no `nobody chose`:\n  %s", sealed[0])
	}
	if !strings.Contains(sealed[1], `"record_mandatory_chosen":true`) {
		t.Errorf("the PUT that made the choice sealed no choice:\n  %s", sealed[1])
	}
	if sealed[0] == sealed[1] {
		t.Fatal("both events sealed the same bytes: the write that changed which faults are " +
			"forwarded is indistinguishable in the ledger from the one that did not")
	}
}
