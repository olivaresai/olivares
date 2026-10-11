// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const cedarForbidWrite = `forbid(principal, action, resource) when { context.permission == "agent:write" };`

// TestPdpPublishReportsLiveActivationStructurally pins that the difference between
// "enforcing now" and "stored but NOT enforcing" is a FIELD, not prose. `active` says
// which revision the store selects; `live_activation` says whether the running evaluator
// took it. Before this, both outcomes returned active:true and differed only inside the
// human-readable `note`, so no client could route on it — for an authorization policy
// that is the difference between a restriction being in force and not.

// TestPdpOpaSelectsWithoutClaimingEnforcement pins the split that makes OPA coherent:
// `active` is the STORE's selection and moves for both engines, while `live_activation`
// carries the only enforcement claim — and for OPA that claim is always "not from here".
//
// It also pins the agreement that used to be violated: a publish/rollback response and
// /pdp/versions read the SAME activation stream, so they must never disagree about which
// revision is active. OPA publish previously left the stream untouched, so the authored
// Rego history had no head, and a later rollback answered active:false while the list
// showed that same revision active.

// TestPdpDeferredActivationIsReportedAndAudited pins the dangerous outcome: the store
// committed and selected the revision, but the live engine kept the PREVIOUS policy. The
// API must say so structurally, and the LEDGER — not just the process log — must record
// it, because the publish event itself is written inside the transaction with active=true
// BEFORE the swap is attempted and therefore cannot distinguish the two outcomes.

// TestPdpGetVersionReturnsStoredContentPerEngine pins two things the diff depends on:
// the stored bytes come back EXACTLY, and the engine disambiguates revision numbers.
// Revision numbers are per-surface, so cedar r1 and opa r1 both exist and are different
// documents — a read that ignored `engine` would confidently return the wrong policy.

// TestPdpGetVersionReportsCurrentSelectionNotTheFrozenRow pins that the per-revision read
// agrees with the list. The stored row carries a frozen active flag from the moment it was
// published; the append-only activation stream is the current selection. After a rollback
// those disagree, and a console diffing against "the active revision" would otherwise be
// shown a revision the engine is not running.

// TestPdpRollbackDoesNotGrowOrTruncateHistory pins that rollback is a POINTER MOVE. It
// must not copy the target forward as a new revision, and must not delete the revisions
// after it. The console tells the operator exactly this before they confirm, so if the
// semantics ever changed to a copy-forward the promise in that dialog would be a lie.

// TestPdpActiveDisclosesTheUnionWithoutLeakingTheManagedProjection pins the two halves of
// the diff contract. The authored surface — the ONLY one a publish replaces — comes back
// with its content so a draft can be diffed against it. The other surfaces that are
// unioned into the enforced policy are disclosed as present, so the console cannot imply
// the authored revision is the whole enforced policy; but their SOURCE is withheld,
// because reading the scoped-grant projection is a higher permission than reading policy.

// TestPdpTestsFollowsTheRequestedRevision pins the divergence a console must design
// around: asked for a specific revision the gate result follows it, but with NO revision
// the route answers for the NEWEST revision, which after a rollback is NOT the active
// one. Both behaviors are pinned deliberately — the console always passes the active
// revision explicitly, and changing this default would silently alter a shipped route.

// TestPdpLifecycleReadsAreReadTier pins that seeing the policy lifecycle does not require
// the authority to change it. The console shows history, the active revision and the
// compile/validate gate to any operator who can read policy; only publish and rollback
// need admin. If these reads were admin-gated, a read-only operator's console would show
// an empty screen rather than the truth about what is enforced.

// assertActiveRevision fails unless /pdp/versions reports exactly one active revision
// for the surface, and it is want.
func assertActiveRevision(t *testing.T, h *harness, token string, headers map[string]string, surface string, want int64) {
	t.Helper()
	versions := h.do("GET", "/v1/m/governance/pdp/versions", token, nil, headers)
	if versions.code != http.StatusOK {
		t.Fatalf("list versions: %d %s", versions.code, versions.raw)
	}
	got, count := int64(0), 0
	for _, raw := range items(versions) {
		version, _ := raw.(map[string]any)
		if version["surface"] == surface && version["active"] == true {
			got = int64(version["revision"].(float64))
			count++
		}
	}
	if count != 1 || got != want {
		t.Fatalf("active %s revision = %d (count %d), want %d: %s", surface, got, count, want, versions.raw)
	}
}

// cedarRevisionNumbers returns the cedar revision numbers currently in the history.
func cedarRevisionNumbers(t *testing.T, h *harness, token string, headers map[string]string) []string {
	t.Helper()
	versions := h.do("GET", "/v1/m/governance/pdp/versions", token, nil, headers)
	if versions.code != http.StatusOK {
		t.Fatalf("list versions: %d %s", versions.code, versions.raw)
	}
	out := []string{}
	for _, raw := range items(versions) {
		version, _ := raw.(map[string]any)
		if version["surface"] == "cedar" {
			out = append(out, fmt.Sprint(version["revision"]))
		}
	}
	return out
}

// findAuditMeta returns the canonical metadata of the newest audit event with action, or
// nil when no such event was appended.
func findAuditMeta(t *testing.T, h *harness, tenant model.TenantID, action string) map[string]any {
	t.Helper()
	var meta map[string]any
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			return fmt.Errorf("audit log does not expose canonical metadata")
		}
		return walker.WalkCanonical(context.Background(), 1, func(event model.AuditEvent, canonical string, _ []byte) error {
			if event.Action != action {
				return nil
			}
			decoder := json.NewDecoder(strings.NewReader(canonical))
			decoder.UseNumber()
			return decoder.Decode(&meta)
		})
	}); err != nil {
		t.Fatalf("walk audit: %v", err)
	}
	return meta
}

// TestPdpActiveReportsLiveActivationMeasuredNotDeclared is the test the audit of
// 2026-08-11 found missing, and it pins the fact that used to exist only in the
// response to the POST that caused it.
//
// Before this, `live_activation` was returned by publish/rollback and by NOTHING
// else: a console could only remember it in browser memory, so a reload, a second
// operator or another replica lost the one fact that says whether the revision on
// screen is the one deciding requests. The store's `active` flag survived — and it
// answers a DIFFERENT question.
//
// Both directions are asserted, and that is the point: the engine measures the
// answer by comparing the union the store selects against the source THIS PROCESS
// compiled, so a constant in either direction fails here. A `liveActivationFor`
// hard-wired to "applied" fails the deferred case; one hard-wired to "deferred"
// fails the applied case.

// TestPdpValidateAgreesWithThePublishGate pins that `ok` answers the question the
// route exists to answer — "would this be accepted?" — and not the strictly
// narrower "did the pre-check say nothing at all".
//
// validateRego ALWAYS appends a "structural pre-check only" WARNING, so with
// OK: len(diags)==0 every Rego document on earth came back ok:false while
// handlePdpPublish accepted the very same bytes (it applies hasError, which
// ignores warnings). The honest caveat was served as a rejection: the third
// answer rendered as the second, on the two routes that must agree.

// TestPdpOpaDryRunDoesNotClaimAnEvaluation pins that the OPA dry-run stops being a
// probe that answers the same for every input WITHOUT saying so.
//
// The route returns allow:true for any Rego whatsoever — the authored policy is not
// deployed to the sidecar, so nothing can be evaluated in-process — and the console
// painted that as a green "Allowed". allow:true is defensible on its own terms ("the
// PDP layer imposes no restriction; RBAC still governs"), but nothing in the payload
// separated it from a measured grant. `evaluated` is that separation.

// TestPdpActiveIdentifiesTheActivationNotTheText pins the two states the FIRST
// version of live_activation got wrong, both reproduced by an adversarial
// contrast before this fix. They share one root cause: it compared
// sha256(loaded source) against the store's union digest, and TEXT IS NOT
// IDENTITY.

func TestPdpOpaDryRunDoesNotClaimAnEvaluation(t *testing.T) {
	h := newHarness(t)
	tenant, token := h.tenantAdmin()
	headers := tenantHdr(tenant)

	exampleReq := map[string]any{
		"principal":  map[string]any{"kind": "user", "id": "u1"},
		"permission": "agent:write",
		"resource":   map[string]any{"kind": "agent", "id": "a1"},
	}
	dryRun := func(engine, source string) resp {
		r := h.do("POST", "/v1/m/governance/pdp/dry-run", token, map[string]any{
			"engine": engine, "source": source, "request": exampleReq,
		}, headers)
		if r.code != http.StatusOK {
			t.Fatalf("dry-run %s: %d %s", engine, r.code, r.raw)
		}
		return r
	}

	// Two Rego documents with OPPOSITE intent get the identical answer. That is the
	// defect stated as a test: the route cannot distinguish them, so it must not
	// report either as an evaluation.
	permissive := dryRun("opa", "package olivares.authz\n\ndefault allow := true\n")
	restrictive := dryRun("opa", "package olivares.authz\n\ndefault allow := false\n")
	if permissive.body["allow"] != restrictive.body["allow"] {
		t.Fatalf("the opa dry-run is a constant; if that ever changes this test is the wrong shape: %s vs %s",
			permissive.raw, restrictive.raw)
	}
	if permissive.body["evaluated"] != false || restrictive.body["evaluated"] != false {
		t.Fatalf("nothing was evaluated for opa, and the payload must say so: %s / %s",
			permissive.raw, restrictive.raw)
	}
	if _, ok := permissive.body["evaluated"]; !ok {
		t.Fatalf("evaluated must always be emitted: an absent field is exactly the ambiguity it removes: %s", permissive.raw)
	}

	// NON-FIRING DIRECTION: cedar really is evaluated, so a constant false would be
	// the mirror-image lie.
	cedar := dryRun("cedar", cedarForbidWrite)
	if cedar.body["evaluated"] != true {
		t.Fatalf("a cedar dry-run IS an evaluation: %s", cedar.raw)
	}
	// And it discriminates: the same request against a policy that forbids it, and
	// one that does not, must differ.
	if cedar.body["allow"] != false {
		t.Fatalf("the forbid rule matches this request: %s", cedar.raw)
	}
	if other := dryRun("cedar", `forbid(principal, action, resource) when { context.permission == "agent:read" };`); other.body["allow"] != true {
		t.Fatalf("a policy that does not match this request must not deny it: %s", other.raw)
	}
}
