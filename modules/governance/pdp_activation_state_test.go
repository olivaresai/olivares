// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
func TestPdpPublishReportsLiveActivationStructurally(t *testing.T) {
	h := newHarness(t)
	tenant, token := h.tenantAdmin()
	headers := tenantHdr(tenant)

	published := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
		"engine": "cedar", "source": cedarForbidWrite,
	}, headers)
	if published.code != http.StatusOK {
		t.Fatalf("publish: %d %s", published.code, published.raw)
	}
	if published.body["active"] != true || published.body["live_activation"] != "applied" {
		t.Fatalf("a cedar publish that swapped the engine must report applied: %s", published.raw)
	}

	// A rollback that reaches the engine reports the same way.
	if r := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
		"engine": "cedar", "source": `forbid(principal, action, resource) when { context.permission == "agent:read" };`,
	}, headers); r.code != http.StatusOK {
		t.Fatalf("publish rev2: %d %s", r.code, r.raw)
	}
	rolled := h.do("POST", "/v1/m/governance/pdp/rollback", token, map[string]any{
		"engine": "cedar", "revision": 1,
	}, headers)
	if rolled.code != http.StatusOK || rolled.body["live_activation"] != "applied" {
		t.Fatalf("a rollback that swapped the engine must report applied: %d %s", rolled.code, rolled.raw)
	}
}

// TestPdpOpaSelectsWithoutClaimingEnforcement pins the split that makes OPA coherent:
// `active` is the STORE's selection and moves for both engines, while `live_activation`
// carries the only enforcement claim — and for OPA that claim is always "not from here".
//
// It also pins the agreement that used to be violated: a publish/rollback response and
// /pdp/versions read the SAME activation stream, so they must never disagree about which
// revision is active. OPA publish previously left the stream untouched, so the authored
// Rego history had no head, and a later rollback answered active:false while the list
// showed that same revision active.
func TestPdpOpaSelectsWithoutClaimingEnforcement(t *testing.T) {
	h := newHarness(t)
	tenant, token := h.tenantAdmin()
	headers := tenantHdr(tenant)

	published := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
		"engine": "opa", "source": "package olivares.authz\n\ndefault allow := true\n",
	}, headers)
	if published.code != http.StatusOK {
		t.Fatalf("publish opa: %d %s", published.code, published.raw)
	}
	if published.body["live_activation"] != "not_applicable" {
		t.Fatalf("opa must never claim this process enforces it: %s", published.raw)
	}
	if published.body["active"] != true {
		t.Fatalf("publishing rego selects it as the current authored revision: %s", published.raw)
	}
	assertActiveRevision(t, h, token, headers, "opa", 1)

	// A second revision moves the selection, and rolling back moves it again.
	if r := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
		"engine": "opa", "source": "package olivares.authz\n\ndefault allow := false\n",
	}, headers); r.code != http.StatusOK {
		t.Fatalf("publish opa rev2: %d %s", r.code, r.raw)
	}
	assertActiveRevision(t, h, token, headers, "opa", 2)

	rolled := h.do("POST", "/v1/m/governance/pdp/rollback", token, map[string]any{
		"engine": "opa", "revision": 1,
	}, headers)
	if rolled.code != http.StatusOK || rolled.body["live_activation"] != "not_applicable" {
		t.Fatalf("opa rollback must not claim enforcement: %d %s", rolled.code, rolled.raw)
	}
	if rolled.body["active"] != true {
		t.Fatalf("an opa rollback DID move the store's selection; saying otherwise contradicts /pdp/versions: %s", rolled.raw)
	}
	// The decisive assertion: the response and the list agree.
	assertActiveRevision(t, h, token, headers, "opa", 1)

	// And the selected Rego revision is answerable, which is the point of selecting.
	active := h.do("GET", "/v1/m/governance/pdp/active?engine=opa", token, nil, headers)
	authored, _ := active.body["authored"].(map[string]any)
	if active.code != http.StatusOK || authored == nil || authored["present"] != true ||
		authored["revision"] != float64(1) {
		t.Fatalf("the authored rego history must have a head: %d %s", active.code, active.raw)
	}
}

// TestPdpDeferredActivationIsReportedAndAudited pins the dangerous outcome: the store
// committed and selected the revision, but the live engine kept the PREVIOUS policy. The
// API must say so structurally, and the LEDGER — not just the process log — must record
// it, because the publish event itself is written inside the transaction with active=true
// BEFORE the swap is attempted and therefore cannot distinguish the two outcomes.
func TestPdpDeferredActivationIsReportedAndAudited(t *testing.T) {
	h := newHarness(t)
	tenant, token := h.tenantAdmin()
	headers := tenantHdr(tenant)

	restore := h.gov.SwapReloadGrantsForTest(func(context.Context, model.TenantID) error {
		return errors.New("injected: grant engine swap failed")
	})
	defer restore()

	published := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
		"engine": "cedar", "source": cedarForbidWrite,
	}, headers)
	if published.code != http.StatusOK {
		t.Fatalf("a failed live swap must not fail the publish: %d %s", published.code, published.raw)
	}
	// The revision IS selected in the store — `active` stays true. What changed is that
	// the caller can now see the engine did not take it.
	if published.body["active"] != true {
		t.Fatalf("a deferred publish is still the selected revision: %s", published.raw)
	}
	if published.body["live_activation"] != "deferred" {
		t.Fatalf("a failed live swap must report deferred, not applied: %s", published.raw)
	}
	// The note must not promise a reload the operator cannot trigger: there is no HTTP
	// endpoint that reloads the PDP.
	note, _ := published.body["note"].(string)
	if strings.Contains(note, "next reload/restart") {
		t.Fatalf("deferred note must not promise an on-demand reload that does not exist: %q", note)
	}

	deferredMeta := findAuditMeta(t, h, tenant, "governance.pdp.activation_deferred")
	if deferredMeta == nil {
		t.Fatal("a deferred activation must be recorded in the audit ledger, not only the process log")
	}
	if fmt.Sprint(deferredMeta["revision"]) != "1" ||
		fmt.Sprint(deferredMeta["exact_committed_snapshot_enforcing"]) != "false" ||
		fmt.Sprint(deferredMeta["state"]) != "unavailable" {
		t.Fatalf("deferral audit must name the revision and closed unavailable state without claiming a previous policy: %v", deferredMeta)
	}

	// A rollback whose swap fails reports and audits identically. Asserted on its own
	// ledger event, not just the response: the rollback path writes its own deferral
	// record, and without this the whole rollback branch could be deleted undetected.
	if r := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
		"engine": "cedar", "source": `forbid(principal, action, resource) when { context.permission == "agent:read" };`,
	}, headers); r.code != http.StatusOK {
		t.Fatalf("publish rev2: %d %s", r.code, r.raw)
	}
	rolled := h.do("POST", "/v1/m/governance/pdp/rollback", token, map[string]any{
		"engine": "cedar", "revision": 1,
	}, headers)
	if rolled.code != http.StatusOK || rolled.body["live_activation"] != "deferred" {
		t.Fatalf("a rollback whose swap failed must report deferred: %d %s", rolled.code, rolled.raw)
	}
	// The newest deferral event must be the ROLLBACK's (revision 1), not the leftover
	// publish one — proving the rollback path writes its own.
	rollbackMeta := findAuditMeta(t, h, tenant, "governance.pdp.activation_deferred")
	if rollbackMeta == nil || fmt.Sprint(rollbackMeta["revision"]) != "1" {
		t.Fatalf("the rollback's deferral must be recorded in the ledger: %v", rollbackMeta)
	}
}

// TestPdpGetVersionReturnsStoredContentPerEngine pins two things the diff depends on:
// the stored bytes come back EXACTLY, and the engine disambiguates revision numbers.
// Revision numbers are per-surface, so cedar r1 and opa r1 both exist and are different
// documents — a read that ignored `engine` would confidently return the wrong policy.
func TestPdpGetVersionReturnsStoredContentPerEngine(t *testing.T) {
	h := newHarness(t)
	tenant, token := h.tenantAdmin()
	headers := tenantHdr(tenant)

	const rego = "package olivares.authz\n\ndefault allow := true\n"
	if r := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
		"engine": "cedar", "source": cedarForbidWrite,
	}, headers); r.code != http.StatusOK {
		t.Fatalf("publish cedar: %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
		"engine": "opa", "source": rego,
	}, headers); r.code != http.StatusOK {
		t.Fatalf("publish opa: %d %s", r.code, r.raw)
	}

	cedarRev := h.do("GET", "/v1/m/governance/pdp/versions/1?engine=cedar", token, nil, headers)
	if cedarRev.code != http.StatusOK {
		t.Fatalf("get cedar r1: %d %s", cedarRev.code, cedarRev.raw)
	}
	if cedarRev.body["content"] != cedarForbidWrite {
		t.Fatalf("stored cedar content must round-trip byte-for-byte: %s", cedarRev.raw)
	}
	opaRev := h.do("GET", "/v1/m/governance/pdp/versions/1?engine=opa", token, nil, headers)
	if opaRev.code != http.StatusOK || opaRev.body["content"] != rego {
		t.Fatalf("revision 1 of opa must be opa's own document, not cedar's: %d %s", opaRev.code, opaRev.raw)
	}

	// Absent revision is an honest 404, and a malformed engine a 400.
	if missing := h.do("GET", "/v1/m/governance/pdp/versions/99?engine=cedar", token, nil, headers); missing.code != http.StatusNotFound {
		t.Fatalf("absent revision must 404: %d %s", missing.code, missing.raw)
	}
	if bad := h.do("GET", "/v1/m/governance/pdp/versions/1?engine=nope", token, nil, headers); bad.code != http.StatusBadRequest {
		t.Fatalf("unknown engine must 400: %d %s", bad.code, bad.raw)
	}
}

// TestPdpGetVersionReportsCurrentSelectionNotTheFrozenRow pins that the per-revision read
// agrees with the list. The stored row carries a frozen active flag from the moment it was
// published; the append-only activation stream is the current selection. After a rollback
// those disagree, and a console diffing against "the active revision" would otherwise be
// shown a revision the engine is not running.
func TestPdpGetVersionReportsCurrentSelectionNotTheFrozenRow(t *testing.T) {
	h := newHarness(t)
	tenant, token := h.tenantAdmin()
	headers := tenantHdr(tenant)

	for _, src := range []string{cedarForbidWrite, `forbid(principal, action, resource) when { context.permission == "agent:read" };`} {
		if r := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
			"engine": "cedar", "source": src,
		}, headers); r.code != http.StatusOK {
			t.Fatalf("publish: %d %s", r.code, r.raw)
		}
	}
	if r := h.do("POST", "/v1/m/governance/pdp/rollback", token, map[string]any{
		"engine": "cedar", "revision": 1,
	}, headers); r.code != http.StatusOK {
		t.Fatalf("rollback: %d %s", r.code, r.raw)
	}

	// r2's ROW was written with active=true when it was published; the activation stream
	// now selects r1. The read must reflect the stream.
	rev2 := h.do("GET", "/v1/m/governance/pdp/versions/2?engine=cedar", token, nil, headers)
	if rev2.code != http.StatusOK {
		t.Fatalf("get r2: %d %s", rev2.code, rev2.raw)
	}
	if rev2.body["active"] == true {
		t.Fatalf("a rolled-past revision must not report itself active: %s", rev2.raw)
	}
	rev1 := h.do("GET", "/v1/m/governance/pdp/versions/1?engine=cedar", token, nil, headers)
	if rev1.body["active"] != true {
		t.Fatalf("the selected revision must report active: %s", rev1.raw)
	}
}

// TestPdpRollbackDoesNotGrowOrTruncateHistory pins that rollback is a POINTER MOVE. It
// must not copy the target forward as a new revision, and must not delete the revisions
// after it. The console tells the operator exactly this before they confirm, so if the
// semantics ever changed to a copy-forward the promise in that dialog would be a lie.
func TestPdpRollbackDoesNotGrowOrTruncateHistory(t *testing.T) {
	h := newHarness(t)
	tenant, token := h.tenantAdmin()
	headers := tenantHdr(tenant)

	for _, src := range []string{cedarForbidWrite, `forbid(principal, action, resource) when { context.permission == "agent:read" };`} {
		if r := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
			"engine": "cedar", "source": src,
		}, headers); r.code != http.StatusOK {
			t.Fatalf("publish: %d %s", r.code, r.raw)
		}
	}
	before := cedarRevisionNumbers(t, h, token, headers)

	if r := h.do("POST", "/v1/m/governance/pdp/rollback", token, map[string]any{
		"engine": "cedar", "revision": 1,
	}, headers); r.code != http.StatusOK {
		t.Fatalf("rollback: %d %s", r.code, r.raw)
	}

	after := cedarRevisionNumbers(t, h, token, headers)
	if strings.Join(before, ",") != strings.Join(after, ",") {
		t.Fatalf("rollback must not add or remove revisions: before=%v after=%v", before, after)
	}
	assertActivePdpRevision(t, h, token, headers, 1)
}

// TestPdpActiveDisclosesTheUnionWithoutLeakingTheManagedProjection pins the two halves of
// the diff contract. The authored surface — the ONLY one a publish replaces — comes back
// with its content so a draft can be diffed against it. The other surfaces that are
// unioned into the enforced policy are disclosed as present, so the console cannot imply
// the authored revision is the whole enforced policy; but their SOURCE is withheld,
// because reading the scoped-grant projection is a higher permission than reading policy.
func TestPdpActiveDisclosesTheUnionWithoutLeakingTheManagedProjection(t *testing.T) {
	h := newHarness(t)
	tenant, token := h.tenantAdmin()
	headers := tenantHdr(tenant)

	if r := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
		"engine": "cedar", "source": cedarForbidWrite,
	}, headers); r.code != http.StatusOK {
		t.Fatalf("publish: %d %s", r.code, r.raw)
	}

	active := h.do("GET", "/v1/m/governance/pdp/active?engine=cedar", token, nil, headers)
	if active.code != http.StatusOK {
		t.Fatalf("get active: %d %s", active.code, active.raw)
	}
	authored, _ := active.body["authored"].(map[string]any)
	if authored == nil || authored["present"] != true || authored["content"] != cedarForbidWrite {
		t.Fatalf("the authored surface must carry the content the diff needs: %s", active.raw)
	}
	sum := sha256.Sum256([]byte(cedarForbidWrite))
	if authored["sha256"] != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("authored digest must cover the authored content: %s", active.raw)
	}

	// The other contributing surfaces are always SHAPED in the response so the console can
	// render "none" honestly rather than omitting the fact that they exist at all.
	for _, name := range []string{"managed", "adopted"} {
		surface, ok := active.body[name].(map[string]any)
		if !ok {
			t.Fatalf("%s surface must always be present in the shape: %s", name, active.raw)
		}
		if surface["content"] != nil {
			t.Fatalf("%s source must never be returned at policy-read tier: %s", name, active.raw)
		}
	}

	// OPA has no union and enforces nothing here; the note must not imply otherwise.
	opa := h.do("GET", "/v1/m/governance/pdp/active?engine=opa", token, nil, headers)
	if opa.code != http.StatusOK {
		t.Fatalf("get active opa: %d %s", opa.code, opa.raw)
	}
	if note, _ := opa.body["note"].(string); !strings.Contains(note, "sidecar") {
		t.Fatalf("the opa note must name the sidecar as the enforcer: %s", opa.raw)
	}
}

// TestPdpTestsFollowsTheRequestedRevision pins the divergence a console must design
// around: asked for a specific revision the gate result follows it, but with NO revision
// the route answers for the NEWEST revision, which after a rollback is NOT the active
// one. Both behaviors are pinned deliberately — the console always passes the active
// revision explicitly, and changing this default would silently alter a shipped route.
func TestPdpTestsFollowsTheRequestedRevision(t *testing.T) {
	h := newHarness(t)
	tenant, token := h.tenantAdmin()
	headers := tenantHdr(tenant)

	for _, src := range []string{cedarForbidWrite, `forbid(principal, action, resource) when { context.permission == "agent:read" };`} {
		if r := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
			"engine": "cedar", "source": src,
		}, headers); r.code != http.StatusOK {
			t.Fatalf("publish: %d %s", r.code, r.raw)
		}
	}
	if r := h.do("POST", "/v1/m/governance/pdp/rollback", token, map[string]any{
		"engine": "cedar", "revision": 1,
	}, headers); r.code != http.StatusOK {
		t.Fatalf("rollback: %d %s", r.code, r.raw)
	}

	explicit := h.do("GET", "/v1/m/governance/pdp/tests?engine=cedar&revision=1", token, nil, headers)
	if explicit.code != http.StatusOK || explicit.body["revision"] != float64(1) {
		t.Fatalf("an explicit revision must be answered for that revision: %d %s", explicit.code, explicit.raw)
	}
	defaulted := h.do("GET", "/v1/m/governance/pdp/tests?engine=cedar", token, nil, headers)
	if defaulted.code != http.StatusOK || defaulted.body["revision"] != float64(2) {
		t.Fatalf("with no revision the route answers for the NEWEST, not the active: %d %s", defaulted.code, defaulted.raw)
	}
}

// TestPdpLifecycleReadsAreReadTier pins that seeing the policy lifecycle does not require
// the authority to change it. The console shows history, the active revision and the
// compile/validate gate to any operator who can read policy; only publish and rollback
// need admin. If these reads were admin-gated, a read-only operator's console would show
// an empty screen rather than the truth about what is enforced.
func TestPdpLifecycleReadsAreReadTier(t *testing.T) {
	h := newHarness(t)
	// Built explicitly rather than via tenantAdmin() so the root token stays in hand:
	// adminLogin performs the one-time setup and cannot be called twice.
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, token := h.roleUser(admin, tenant, "boss@acme.io", "admin")
	headers := tenantHdr(tenant)
	if r := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
		"engine": "cedar", "source": cedarForbidWrite,
	}, headers); r.code != http.StatusOK {
		t.Fatalf("publish: %d %s", r.code, r.raw)
	}

	_, editor := h.roleUser(admin, tenant, "policy-reader@acme.io", "editor")

	for _, path := range []string{
		"/v1/m/governance/pdp/versions",
		"/v1/m/governance/pdp/versions/1?engine=cedar",
		"/v1/m/governance/pdp/active?engine=cedar",
		"/v1/m/governance/pdp/tests?engine=cedar&revision=1",
	} {
		if r := h.do("GET", path, editor, nil, headers); r.code != http.StatusOK {
			t.Fatalf("read-tier principal must be able to GET %s: %d %s", path, r.code, r.raw)
		}
	}

	// ...but not to change what is enforced.
	if r := h.do("POST", "/v1/m/governance/pdp/publish", editor, map[string]any{
		"engine": "cedar", "source": cedarForbidWrite,
	}, headers); r.code != http.StatusForbidden {
		t.Fatalf("publish must remain admin-tier: %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/governance/pdp/rollback", editor, map[string]any{
		"engine": "cedar", "revision": 1,
	}, headers); r.code != http.StatusForbidden {
		t.Fatalf("rollback must remain admin-tier: %d %s", r.code, r.raw)
	}
}

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
func TestPdpActiveReportsLiveActivationMeasuredNotDeclared(t *testing.T) {
	h := newHarness(t)
	tenant, token := h.tenantAdmin()
	headers := tenantHdr(tenant)

	readActive := func(engine string) resp {
		r := h.do("GET", "/v1/m/governance/pdp/active?engine="+engine, token, nil, headers)
		if r.code != http.StatusOK {
			t.Fatalf("GET /pdp/active?engine=%s: %d %s", engine, r.code, r.raw)
		}
		return r
	}

	// THE POSITIVE. A publish that reached the engine must still read back as
	// applied on a LATER, independent GET — that is what "survives a reload" means.
	if r := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
		"engine": "cedar", "source": cedarForbidWrite,
	}, headers); r.code != http.StatusOK || r.body["live_activation"] != "applied" {
		t.Fatalf("publish should have applied: %d %s", r.code, r.raw)
	}
	applied := readActive("cedar")
	if applied.body["live_activation"] != "applied" {
		t.Fatalf("a policy this process compiled must read back as applied, not only in the publish response: %s", applied.raw)
	}

	// THE NEGATIVE, and it is the dangerous one. The store commits and selects the
	// revision while the swap fails, so the PREVIOUS policy keeps deciding. Both
	// facts have to be readable AT THE SAME TIME from a plain GET: `authored`
	// reports the newly selected revision, `live_activation` reports that it is not
	// the one in force here.
	restore := h.gov.SwapReloadGrantsForTest(func(context.Context, model.TenantID) error {
		return errors.New("injected: grant engine swap failed")
	})
	if r := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
		"engine": "cedar", "source": `forbid(principal, action, resource) when { context.permission == "agent:read" };`,
	}, headers); r.code != http.StatusOK || r.body["live_activation"] != "deferred" {
		t.Fatalf("publish should have deferred: %d %s", r.code, r.raw)
	}
	restore()

	deferredRead := readActive("cedar")
	if deferredRead.body["live_activation"] != "deferred" {
		t.Fatalf("after a deferred publish the GET must say the selected revision is NOT in force here: %s", deferredRead.raw)
	}
	authored, _ := deferredRead.body["authored"].(map[string]any)
	if authored == nil || authored["present"] != true || authored["revision"] != float64(2) {
		t.Fatalf("the store still selects r2 — the two facts must be readable together: %s", deferredRead.raw)
	}

	// OPA never claims this process enforces anything, on the READ path too.
	if r := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
		"engine": "opa", "source": "package olivares.authz\n\ndefault allow := true\n",
	}, headers); r.code != http.StatusOK {
		t.Fatalf("publish opa: %d %s", r.code, r.raw)
	}
	if opa := readActive("opa"); opa.body["live_activation"] != "not_applicable" {
		t.Fatalf("opa must report not_applicable on the read path: %s", opa.raw)
	}

	// NON-FIRING DIRECTION. A control that always warns passes every "it warns"
	// assertion, so the field must go back to applied once the engine really has
	// the union again — here, the next successful publish.
	if r := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
		"engine": "cedar", "source": cedarForbidWrite,
	}, headers); r.code != http.StatusOK {
		t.Fatalf("recovery publish: %d %s", r.code, r.raw)
	}
	if recovered := readActive("cedar"); recovered.body["live_activation"] != "applied" {
		t.Fatalf("a re-established policy must stop reporting deferred, or the field only ever warns: %s", recovered.raw)
	}

	// The field is ALWAYS present. An engine that omitted it would be
	// indistinguishable from one that does not report it at all, and the console
	// must be able to tell those apart to render the third answer honestly.
	if _, ok := applied.body["live_activation"]; !ok {
		t.Fatalf("live_activation must always be emitted, never omitted: %s", applied.raw)
	}
}

// TestPdpValidateAgreesWithThePublishGate pins that `ok` answers the question the
// route exists to answer — "would this be accepted?" — and not the strictly
// narrower "did the pre-check say nothing at all".
//
// validateRego ALWAYS appends a "structural pre-check only" WARNING, so with
// OK: len(diags)==0 every Rego document on earth came back ok:false while
// handlePdpPublish accepted the very same bytes (it applies hasError, which
// ignores warnings). The honest caveat was served as a rejection: the third
// answer rendered as the second, on the two routes that must agree.
func TestPdpValidateAgreesWithThePublishGate(t *testing.T) {
	h := newHarness(t)
	tenant, token := h.tenantAdmin()
	headers := tenantHdr(tenant)

	const goodRego = "package olivares.authz\n\ndefault allow := true\n"
	// Unbalanced braces AND no package declaration: two structural errors.
	const badRego = "allow if { input.x == 1"

	validate := func(engine, source string) resp {
		r := h.do("POST", "/v1/m/governance/pdp/validate", token, map[string]any{
			"engine": engine, "source": source,
		}, headers)
		if r.code != http.StatusOK {
			t.Fatalf("validate %s: %d %s", engine, r.code, r.raw)
		}
		return r
	}

	good := validate("opa", goodRego)
	if good.body["ok"] != true {
		t.Fatalf("publish accepts this document, so validate must not call it invalid: %s", good.raw)
	}
	// ...and the warning is STILL reported. Making ok:true by dropping the caveat
	// would trade one dishonesty for another.
	diags, _ := good.body["diagnostics"].([]any)
	if len(diags) == 0 {
		t.Fatalf("the structural-pre-check caveat must survive: %s", good.raw)
	}
	// The agreement is asserted against the real gate, not against a belief about it.
	if r := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
		"engine": "opa", "source": goodRego,
	}, headers); r.code != http.StatusOK {
		t.Fatalf("validate said ok, so publish must accept it: %d %s", r.code, r.raw)
	}

	// NON-FIRING DIRECTION: a validator that answers ok:true for everything is as
	// useless as one that answers ok:false for everything.
	bad := validate("opa", badRego)
	if bad.body["ok"] != false {
		t.Fatalf("a rego source with structural errors must not validate: %s", bad.raw)
	}
	if r := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
		"engine": "opa", "source": badRego,
	}, headers); r.code != http.StatusBadRequest {
		t.Fatalf("validate said not-ok, so publish must reject it: %d %s", r.code, r.raw)
	}

	// Cedar is unchanged by the fix: it emits no warnings, so both predicates agree
	// there. Pinned so a future "simplification" cannot quietly regress it.
	if c := validate("cedar", cedarForbidWrite); c.body["ok"] != true {
		t.Fatalf("a compiling cedar policy validates: %s", c.raw)
	}
	if c := validate("cedar", "this is not cedar"); c.body["ok"] != false {
		t.Fatalf("a cedar source that does not compile must not validate: %s", c.raw)
	}
}

// TestPdpOpaDryRunDoesNotClaimAnEvaluation pins that the OPA dry-run stops being a
// probe that answers the same for every input WITHOUT saying so.
//
// The route returns allow:true for any Rego whatsoever — the authored policy is not
// deployed to the sidecar, so nothing can be evaluated in-process — and the console
// painted that as a green "Allowed". allow:true is defensible on its own terms ("the
// PDP layer imposes no restriction; RBAC still governs"), but nothing in the payload
// separated it from a measured grant. `evaluated` is that separation.
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

// TestPdpActiveIdentifiesTheActivationNotTheText pins the two states the FIRST
// version of live_activation got wrong, both reproduced by an adversarial
// contrast before this fix. They share one root cause: it compared
// sha256(loaded source) against the store's union digest, and TEXT IS NOT
// IDENTITY.
func TestPdpActiveIdentifiesTheActivationNotTheText(t *testing.T) {
	h := newHarness(t)
	tenant, token := h.tenantAdmin()
	headers := tenantHdr(tenant)
	get := func() resp {
		r := h.do("GET", "/v1/m/governance/pdp/active?engine=cedar", token, nil, headers)
		if r.code != http.StatusOK {
			t.Fatalf("GET /pdp/active: %d %s", r.code, r.raw)
		}
		return r
	}

	// (1) A TENANT THAT HAS PUBLISHED NOTHING. `contentDigest("")` equals the empty
	// union's digest, so "never loaded" and "loaded with nothing" were the same
	// value and every brand-new tenant — the commonest state there is — reported
	// applied. An adjective needs something to apply to.
	fresh := get()
	if fresh.body["live_activation"] != "no_policy" {
		t.Fatalf("a tenant with no selected revision has no activation to report: %s", fresh.raw)
	}
	authored, _ := fresh.body["authored"].(map[string]any)
	if authored == nil || authored["present"] != false {
		t.Fatalf("...and it must still report the surfaces as measured absences: %s", fresh.raw)
	}

	// (2) IDENTICAL BYTES ACROSS TWO REVISIONS. appendRevision never deduplicates
	// content, so r2 can carry exactly r1's source. If r2's swap fails, the loaded
	// TEXT still matches the store's union — the POST said deferred and the GET
	// said applied, about the same publish, on the same screen.
	if r := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
		"engine": "cedar", "source": cedarForbidWrite,
	}, headers); r.code != http.StatusOK || r.body["live_activation"] != "applied" {
		t.Fatalf("publish r1: %d %s", r.code, r.raw)
	}
	restore := h.gov.SwapReloadGrantsForTest(func(context.Context, model.TenantID) error {
		return errors.New("injected: grant engine swap failed")
	})
	deferredPost := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
		"engine": "cedar", "source": cedarForbidWrite, // BYTE-IDENTICAL to r1
	}, headers)
	restore()
	if deferredPost.code != http.StatusOK || deferredPost.body["live_activation"] != "deferred" {
		t.Fatalf("publish r2 with a failed swap is deferred: %d %s", deferredPost.code, deferredPost.raw)
	}
	after := get()
	if after.body["live_activation"] != "deferred" {
		t.Fatalf("the GET must agree with the POST about the SAME publish, identical bytes or not: %s", after.raw)
	}
	authored, _ = after.body["authored"].(map[string]any)
	if authored == nil || authored["revision"] != float64(2) {
		t.Fatalf("the store still selects r2: %s", after.raw)
	}

	// NON-FIRING DIRECTION: recovering really does clear it, so this is not a flag
	// that latches on and warns forever.
	if r := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
		"engine": "cedar", "source": cedarForbidWrite,
	}, headers); r.code != http.StatusOK {
		t.Fatalf("recovery publish: %d %s", r.code, r.raw)
	}
	if r := get(); r.body["live_activation"] != "applied" {
		t.Fatalf("a successful swap of identical bytes IS applied: %s", r.raw)
	}

	// The freshness axis is reported separately and is false on a connected node.
	if _, ok := get().body["grants_expired"]; !ok {
		t.Fatalf("grants_expired must always be emitted: %s", get().raw)
	}
}
