// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package capabilities_test

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/capabilities"
)

// D-09: approve/unpin of a tool-pin alters an authorization baseline (docs/SECURITY-HARDENING.md),
// so it MUST leave durable, operator-attributed evidence and the effect MUST NOT stand
// unless that evidence anchored (evidence-or-refuse, sdk/evidence.go). The historical bug:
// RecordPin/Unpin mutated the pin FIRST and auditToolPin discarded the ledger error, so a
// dropped/failed audit still returned 200 with the baseline changed and no trace. The fix
// anchors evidence-intent BEFORE the CAS-guarded, idempotent durable apply, never discards
// the ledger error, and never returns 200-applied (202-pending; the durable apply/settle is
// authoritative). The applier/verifier/settle live in the ABSENT enterprise overlay; the
// tests here exercise the community-side actuating contract against a synchronous fake.

// degradeSpoolHarness first provisions the lifecycle state with a healthy spool, then
// closes and reopens the same SQLite file with a 1-byte DEGRADE budget. This isolates the
// tool-pin evidence-or-refuse path: setup, organization creation, and membership are
// governed writes themselves and must not be starved merely to arrange the fixture.
func degradeSpoolHarness(
	t *testing.T,
	admin mcpc.ToolPinAdmin,
) (*harness, model.TenantID, string) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "toolpins-evidence.db")
	security := newHarnessSecurity(t)
	bootstrap := openHarness(t, store.Config{
		Engine: store.EngineSQLite, DSN: dsn, Debug: true,
	}, security, false, capabilities.WithToolPinAdmin(admin))
	root := bootstrap.adminLogin()
	tenant := bootstrap.createOrg(root, "acme")
	editor := bootstrap.roleToken(root, tenant, "e@x.io", "editor")
	if err := bootstrap.st.Close(); err != nil {
		t.Fatalf("close healthy spool fixture: %v", err)
	}

	h := openHarness(t, store.Config{
		Engine: store.EngineSQLite, DSN: dsn, Debug: true,
		AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade,
	}, security, true, capabilities.WithToolPinAdmin(admin))
	return h, tenant, editor
}

func pendingDrops(t *testing.T, h *harness) int64 {
	t.Helper()
	status, _, err := h.st.(store.AuditSpoolStatuser).AuditSpoolStatus(context.Background())
	if err != nil {
		t.Fatalf("spool status: %v", err)
	}
	return status.PendingDrops
}

// Test 1: approve with an audit that cannot anchor (degrade drop) must REFUSE (503) and leave
// the pin baseline UNCHANGED and the durable applier UNCALLED — never 202/200 with a
// silently-changed pin.
func TestApproveToolPinRefusesWhenEvidenceDropped(t *testing.T) {
	fake := newFakePinAdmin()
	h, tenant, editor := degradeSpoolHarness(t, fake)

	now := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	key := tenant.String() + "|search"
	fake.pins[key] = mcpc.PinSnapshot{
		Server: tenant.String(), Tool: "search", Fingerprint: "fp-old",
		PinnedAt: now, UpdatedAt: now, PinCount: 1,
	}

	before := pendingDrops(t, h)
	body := map[string]any{"tool": "search", "fingerprint": "fp-new", "expected_version": 0}
	r := h.do("POST", "/v1/m/capabilities/toolpins/approve", editor, body, tenantIdem(tenant, "k1"))

	if r.code == http.StatusOK || r.code == http.StatusAccepted {
		t.Fatalf("approve returned %d despite un-anchored evidence (fail-open): %s", r.code, r.raw)
	}
	if r.code != http.StatusServiceUnavailable {
		t.Fatalf("approve = %d, want 503 (evidence deny-closed); body=%s", r.code, r.raw)
	}
	if got := fake.pins[key].Fingerprint; got != "fp-old" {
		t.Fatalf("baseline changed without durable evidence: pin fingerprint = %q, want unchanged %q", got, "fp-old")
	}
	if len(fake.ops) != 0 {
		t.Fatalf("durable applier was invoked before evidence anchored: ops=%v", fake.ops)
	}
	// F9 discipline: the degrade drop's loss accounting must be COMMITTED (advanced), not
	// rolled back by returning the sentinel from inside the transaction (sdk/evidence.go).
	if after := pendingDrops(t, h); after <= before {
		t.Fatalf("declared evidence gap was rolled back: PendingDrops before=%d after=%d, want after>before", before, after)
	}
}

// Test 2: unpin with an un-anchorable audit must REFUSE (503) and RETAIN the pin — an unpin
// that slips through returns the tool to TOFU with no operator-attributed trace.
func TestUnpinToolPinRefusesWhenEvidenceDropped(t *testing.T) {
	fake := newFakePinAdmin()
	h, tenant, editor := degradeSpoolHarness(t, fake)

	now := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	key := tenant.String() + "|search"
	fake.pins[key] = mcpc.PinSnapshot{
		Server: tenant.String(), Tool: "search", Fingerprint: "fp-1",
		PinnedAt: now, UpdatedAt: now, PinCount: 1,
	}

	body := map[string]any{"tool": "search", "expected_version": 0}
	r := h.do("POST", "/v1/m/capabilities/toolpins/unpin", editor, body, tenantIdem(tenant, "k2"))

	if r.code == http.StatusOK || r.code == http.StatusAccepted {
		t.Fatalf("unpin returned %d despite un-anchored evidence (fail-open): %s", r.code, r.raw)
	}
	if r.code != http.StatusServiceUnavailable {
		t.Fatalf("unpin = %d, want 503 (evidence deny-closed); body=%s", r.code, r.raw)
	}
	if _, ok := fake.pins[key]; !ok {
		t.Fatal("pin was revoked without durable evidence: the rug-pull tripwire was removed with no trace")
	}
}

// Test 3: from_drift approve where the tool's DURABLE drift no longer equals the fingerprint
// the operator reviewed (a rug-pull between GET and POST) must REJECT (409) via the durable
// CAS — never pin the stale reviewed fingerprint. This is the D-09 TOCTOU, now closed by
// comparing the client-supplied expected_drift_fingerprint against the durable row (not an
// in-handler Pins() re-read).
func TestApproveFromDriftRejectsStaleFingerprint(t *testing.T) {
	now := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	fake := newFakePinAdmin()
	// Healthy store so evidence anchors — this isolates the TOCTOU, not the audit path.
	h := newHarness(t, capabilities.WithToolPinAdmin(fake))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	key := tenant.String() + "|search"
	// The operator's GET showed drift "fp-A"; by POST time the tool rug-pulled to "fp-B",
	// which is what the durable row now records.
	fake.pins[key] = mcpc.PinSnapshot{
		Server: tenant.String(), Tool: "search", Fingerprint: "fp-pinned",
		PinnedAt: now, UpdatedAt: now, PinCount: 1,
		DriftFingerprint: "fp-B", DriftAt: now.Add(time.Hour),
	}

	body := map[string]any{
		"tool": "search", "from_drift": true,
		"expected_version": 0, "expected_drift_fingerprint": "fp-A",
	}
	r := h.do("POST", "/v1/m/capabilities/toolpins/approve", editor, body, tenantIdem(tenant, "k3"))

	if r.code == http.StatusOK || r.code == http.StatusAccepted {
		t.Fatalf("approve-from-drift returned %d and pinned a stale fingerprint (TOCTOU): %s", r.code, r.raw)
	}
	if r.code != http.StatusConflict {
		t.Fatalf("approve-from-drift with changed drift = %d, want 409; body=%s", r.code, r.raw)
	}
	if code := errorCode(r); code != "pin_drift_changed" {
		t.Fatalf("drift 409 carries code %q, want pin_drift_changed; body=%s", code, r.raw)
	}
	if got := fake.pins[key].Fingerprint; got == "fp-A" {
		t.Fatalf("pinned the stale reviewed fingerprint %q; the durable drift had already moved to fp-B", got)
	}
	if got := fake.pins[key].Fingerprint; got != "fp-pinned" {
		t.Fatalf("pin fingerprint = %q, want the untouched original %q (no write on CAS miss)", got, "fp-pinned")
	}
}

// Test 4: the D-09 API preconditions are mandatory — a missing Idempotency-Key, a missing
// expected_version, or a from_drift approve without expected_drift_fingerprint is a 400
// (never a best-effort write). This is what forces the operator to act on a specific,
// reviewed durable state.
func TestToolPinActuatorRequiresPreconditions(t *testing.T) {
	fake := newFakePinAdmin()
	h := newHarness(t, capabilities.WithToolPinAdmin(fake))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	// No Idempotency-Key header.
	if r := h.do("POST", "/v1/m/capabilities/toolpins/approve", editor,
		map[string]any{"tool": "t", "fingerprint": "fp", "expected_version": 0}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("approve without Idempotency-Key = %d, want 400", r.code)
	}
	// Missing expected_version.
	if r := h.do("POST", "/v1/m/capabilities/toolpins/approve", editor,
		map[string]any{"tool": "t", "fingerprint": "fp"}, tenantIdem(tenant, "k4a")); r.code != http.StatusBadRequest {
		t.Fatalf("approve without expected_version = %d, want 400", r.code)
	}
	// from_drift without expected_drift_fingerprint.
	if r := h.do("POST", "/v1/m/capabilities/toolpins/approve", editor,
		map[string]any{"tool": "t", "from_drift": true, "expected_version": 0}, tenantIdem(tenant, "k4b")); r.code != http.StatusBadRequest {
		t.Fatalf("from_drift without expected_drift_fingerprint = %d, want 400", r.code)
	}
	// Unpin without expected_version.
	if r := h.do("POST", "/v1/m/capabilities/toolpins/unpin", editor,
		map[string]any{"tool": "t"}, tenantIdem(tenant, "k4c")); r.code != http.StatusBadRequest {
		t.Fatalf("unpin without expected_version = %d, want 400", r.code)
	}
}

// Test 5: reusing an Idempotency-Key for a DIFFERENT change (different EffectDigest under the
// same server-minted OperationID) is a replay and must be refused (409), never silently
// applied as a second effect (sdk/evidence.go replay law).
func TestToolPinActuatorRejectsIdempotencyKeyReplay(t *testing.T) {
	fake := newFakePinAdmin()
	h := newHarness(t, capabilities.WithToolPinAdmin(fake))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	key := tenant.String() + "|search"
	fake.pins[key] = mcpc.PinSnapshot{Server: tenant.String(), Tool: "search", Fingerprint: "fp-0", PinCount: 1}

	first := map[string]any{"tool": "search", "fingerprint": "fp-A", "expected_version": 0}
	if r := h.do("POST", "/v1/m/capabilities/toolpins/approve", editor, first, tenantIdem(tenant, "same-key")); r.code != http.StatusAccepted {
		t.Fatalf("first approve = %d %s, want 202", r.code, r.raw)
	}
	// Same key, different fingerprint → different EffectDigest under the same OperationID.
	second := map[string]any{"tool": "search", "fingerprint": "fp-B", "expected_version": 1}
	r := h.do("POST", "/v1/m/capabilities/toolpins/approve", editor, second, tenantIdem(tenant, "same-key"))
	if r.code != http.StatusConflict {
		t.Fatalf("replayed Idempotency-Key with a different change = %d, want 409; body=%s", r.code, r.raw)
	}
	// The STATUS is not the answer: the actuator also returns 409 when the row moved or
	// the tool drifted again, and those two are ordinary concurrency a console resolves by
	// refetching. A rebound idempotency key is a replay or a client bug and must stay
	// distinguishable, or the console reassures the operator about the one 409 that
	// deserves attention (the model contrast).
	if code := errorCode(r); code != "idempotency_key_reused" {
		t.Fatalf("replay 409 carries code %q, want idempotency_key_reused; body=%s", code, r.raw)
	}
	if got := fake.pins[key].Fingerprint; got != "fp-A" {
		t.Fatalf("replay applied a second effect: fingerprint = %q, want the original %q", got, "fp-A")
	}
}
