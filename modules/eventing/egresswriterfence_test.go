// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package eventing

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Unit H, commit 1 — the fence's OWN epoch.
//
// These tests go through `engine.Open` and this module's REAL RegisterSchema, so what they pin is
// the production registration rather than a synthetic pair of controls. That matters here: the
// property is that the destination control and the writer fence are classified INDEPENDENTLY, and
// the only way to be sure the shipped module does that is to open a store the way boot does.
//
// The property is the correction an adversarial review of this unit's design produced. Deriving the
// fence's arming from the destination control's classification is wrong in both directions:
//
//   - a deployment created AFTER the destination control but before the fence is classified
//     `enforced` by the first, while its fleet may not know the fence at all — arming from that
//     answer breaks the rolling update that is replacing it;
//   - a deployment that already COMMITTED the destination control could never arm the fence,
//     because the actuation command returns "nothing to do" once the commitment is set.

// openWithModuleSchema opens a store with this module's real schema, as boot does.
func openWithModuleSchema(t *testing.T, dsn string) store.Store {
	t.Helper()
	m := New()
	st, err := engine.Open(context.Background(),
		store.Config{Engine: store.EngineSQLite, DSN: dsn}, m.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return st
}

// openWithoutTheFence opens a store with the module's tables but WITHOUT the fence control, which
// is what a binary from the era between the destination control and the fence looks like.
//
// It registers the same descriptors the module does and re-declares only the destination control,
// so the witness table exists exactly as a real pre-fence deployment would have left it.
func openWithoutTheFence(t *testing.T, dsn string) store.Store {
	t.Helper()
	m := New()
	st, err := engine.Open(context.Background(),
		store.Config{Engine: store.EngineSQLite, DSN: dsn},
		func(reg store.ExtensionRegistry) error {
			return m.RegisterSchema(preFenceRegistry{reg})
		})
	if err != nil {
		t.Fatalf("open store as a pre-fence binary: %v", err)
	}
	return st
}

// preFenceRegistry models a binary from the era BEFORE the writer fence: it carries the module's
// tables and the destination control, and neither the fence's control nor the fence's migrations.
//
// Withholding the migrations as well as the control is what makes it faithful rather than
// convenient. A real pre-fence binary has neither, and the pair is not separable in production
// either: the migration that installs the trigger and the declaration that classifies the control
// ship in the same module version. The function's "no classification" branch is a corruption
// detector for a database where they were separated by hand, not a state a release can produce.
type preFenceRegistry struct{ inner store.ExtensionRegistry }

func (p preFenceRegistry) Register(d model.EntityDescriptor) error { return p.inner.Register(d) }

// Migrations passes through only the versions that predate the fence, rebuilt as an in-memory FS so
// the older binary is modeled by what it actually shipped.
func (p preFenceRegistry) Migrations(ns string, fsys fs.FS) error {
	old := fstest.MapFS{}
	if err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		base := d.Name()
		if len(base) < 4 || base[:4] >= "0003" {
			return nil // the fence's own migrations did not exist in that era
		}
		b, rerr := fs.ReadFile(fsys, path)
		if rerr != nil {
			return rerr
		}
		old[path] = &fstest.MapFile{Data: b}
		return nil
	}); err != nil {
		return err
	}
	return p.inner.Migrations(ns, old)
}

// SchemaInvariants forwards, and the reason is worth stating because the other two methods here
// deliberately do NOT. This wrapper models a binary from before the fence, so anything the fence
// ships must be withheld — and today the fence declares no schema invariant at all (no module in
// this tree does yet: the interface gained the method before it gained a caller). Withholding
// nothing is therefore the faithful behavior, not a shortcut.
//
// IF THE FENCE EVER DECLARES ONE, this must filter it out the way Migrations filters the fence's
// own migrations. Forwarding it then would hand a pre-fence binary an invariant it never had, and
// the case would start passing for the wrong reason instead of failing.
func (p preFenceRegistry) SchemaInvariants(ns string, byEngine map[store.Engine][]store.SchemaTrigger) error {
	return p.inner.SchemaInvariants(ns, byEngine)
}

func (p preFenceRegistry) WorkspaceInitializer(i store.WorkspaceInitializer) error {
	return p.inner.WorkspaceInitializer(i)
}

func (p preFenceRegistry) RolloutControl(c store.RolloutControl) error {
	if c.Key == EgressWriterFenceControlKey {
		return nil // the era before the fence existed
	}
	return p.inner.RolloutControl(c)
}

// fenceGenerationOf reads the generation a writer must attest against.
//
// It exists for the fixtures that write a governed row STRAIGHT INTO THE STORE. Those model an
// estate whose DESTINATION predates the egress gate, which says nothing about the writer: the writer
// is this binary, it carries the gate, and on a fresh database — where the fence is armed by
// classification, because nothing that predates it ever wrote there — it has to prove exactly that.
// A fixture that refused to prove it would be claiming something its own deployment says is
// impossible.
func fenceGenerationOf(t *testing.T, st store.Store) int64 {
	t.Helper()
	return stateOf(t, st, EgressWriterFenceControlKey).Generation
}

func stateOf(t *testing.T, st store.Store, key string) store.RolloutState {
	t.Helper()
	rs, ok := st.(store.RolloutStater)
	if !ok {
		t.Fatal("store does not expose store.RolloutStater")
	}
	got, err := rs.RolloutState(context.Background(), key)
	if err != nil {
		t.Fatalf("read rollout state %q: %v", key, err)
	}
	return got
}

// TestAFreshDatabaseArmsTheFence: a database that never held the subscription table has no writer
// that predates the fence, so the fence is armed by classification — the same symmetry the
// destination control uses, and for the same reason.
func TestAFreshDatabaseArmsTheFence(t *testing.T) {
	st := openWithModuleSchema(t, filepath.Join(t.TempDir(), "fresh.db"))
	defer st.Close()

	fence := stateOf(t, st, EgressWriterFenceControlKey)
	if fence.ClassifiedMode != store.RolloutEnforced || fence.CurrentMode != store.RolloutEnforced {
		t.Fatalf("fresh database classified the fence %q/%q, want %q — an install with no pre-fence writer must not leave it dormant",
			fence.ClassifiedMode, fence.CurrentMode, store.RolloutEnforced)
	}
	if fence.EnforcementCommitted {
		t.Fatal("a classification is not a decision: the commitment must start clear")
	}
	// And the destination control is classified separately, over the same witness.
	if dest := stateOf(t, st, EgressRolloutControlKey); dest.ClassifiedMode != store.RolloutEnforced {
		t.Fatalf("destination control classified %q", dest.ClassifiedMode)
	}
}

// TestADeploymentFromTheEraBetweenTheTwoControlsLeavesTheFenceDORMANT is the critical case. The
// destination control says `enforced` — the database was new when IT arrived — and the fence must
// still be dormant, because the fleet that ran that era does not know the fence exists.
func TestADeploymentFromTheEraBetweenTheTwoControlsLeavesTheFenceDORMANT(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "between.db")

	// Era 1: a fresh database, opened by a binary that carries the destination control and NOT the
	// fence. The destination control classifies `enforced`.
	st1 := openWithoutTheFence(t, dsn)
	if got := stateOf(t, st1, EgressRolloutControlKey); got.ClassifiedMode != store.RolloutEnforced {
		t.Fatalf("era 1: destination control classified %q, want %q", got.ClassifiedMode, store.RolloutEnforced)
	}
	if rs, ok := st1.(store.RolloutStater); ok {
		if _, err := rs.RolloutState(context.Background(), EgressWriterFenceControlKey); err == nil {
			t.Fatal("era 1 classified a control its binary does not carry")
		}
	}
	_ = st1.Close()

	// Era 2: the fence arrives. The witness table exists, so the fence is DORMANT.
	st2 := openWithModuleSchema(t, dsn)
	defer st2.Close()

	dest := stateOf(t, st2, EgressRolloutControlKey)
	fence := stateOf(t, st2, EgressWriterFenceControlKey)
	if dest.ClassifiedMode != store.RolloutEnforced {
		t.Fatalf("the destination control was reclassified to %q by a later boot", dest.ClassifiedMode)
	}
	if fence.ClassifiedMode != store.RolloutLegacyCompat || fence.CurrentMode != store.RolloutLegacyCompat {
		t.Fatalf("the fence classified %q/%q on a deployment whose fleet predates it, want %q: arming here would fail the authoring of a leader the operator has not replaced yet",
			fence.ClassifiedMode, fence.CurrentMode, store.RolloutLegacyCompat)
	}
}

// TestTheFenceRequirementFollowsItsOwnStateNotTheDestinationControls pins the module-side reading:
// a destination control that is enforced must NOT make the fence report itself armed.
func TestTheFenceRequirementFollowsItsOwnStateNotTheDestinationControls(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "reading.db")
	st1 := openWithoutTheFence(t, dsn)
	_ = st1.Close()
	st := openWithModuleSchema(t, dsn)
	defer st.Close()

	m := New()
	m.UseEgressRollout(rolloutOf(st, EgressRolloutControlKey))
	m.UseEgressWriterFence(fenceOf(st))

	req, err := m.resolveFence(context.Background())
	if err != nil {
		t.Fatalf("resolveFence: %v", err)
	}
	if req.Armed || req.RequiredCapability != 0 {
		t.Fatalf("the fence reports armed=%v required=%d while the destination control is enforced and the fence is not — the two must not be conflated",
			req.Armed, req.RequiredCapability)
	}
	// Decide the fence deliberately, and only then is it armed.
	cur := stateOf(t, st, EgressWriterFenceControlKey)
	if _, err := st.(store.RolloutStater).SetRolloutMode(context.Background(), store.RolloutTransition{
		Key: EgressWriterFenceControlKey, Mode: store.RolloutEnforced,
		Actor: "op", Reason: "CHG-H: fleet converged", ExpectGeneration: cur.Generation,
	}); err != nil {
		t.Fatalf("arm the fence: %v", err)
	}
	req, err = m.resolveFence(context.Background())
	if err != nil {
		t.Fatalf("resolveFence after arming: %v", err)
	}
	if !req.Armed || req.RequiredCapability != EgressWriterCapability {
		t.Fatalf("after arming: armed=%v required=%d, want true/%d", req.Armed, req.RequiredCapability, EgressWriterCapability)
	}
	if req.Generation != cur.Generation+1 {
		t.Fatalf("generation %d, want %d — a writer must attest against the state it read", req.Generation, cur.Generation+1)
	}
}

// TestAnUnreadableFenceIsNotADormantFence: "this plane could not establish whether the fence is
// armed" must never be delivered as "the fence is not armed". It is the failure this campaign keeps
// finding, and the fence is the last place it would be tolerable.
func TestAnUnreadableFenceIsNotADormantFence(t *testing.T) {
	m := New()
	m.UseEgressWriterFence(brokenFence{})
	if _, err := m.resolveFence(context.Background()); err == nil {
		t.Fatal("an unreadable fence state was reported as a usable requirement")
	}
	// And a nil seam IS dormant — the only upgrade-safe reading for an embedder that has not
	// adopted it, which is why the first-party binary makes a store without the capability fatal.
	m2 := New()
	req, err := m2.resolveFence(context.Background())
	if err != nil || req.Armed {
		t.Fatalf("an unwired fence: err=%v armed=%v; want no error and not armed", err, req.Armed)
	}
}

type brokenFence struct{}

func (brokenFence) EgressWriterFence(context.Context) (store.RolloutState, error) {
	return store.RolloutState{}, http.ErrServerClosed
}

// boundedFence is exactly what the composition root wires — a read of the fence's durable state off
// the store — except that it gives the read a deadline of its own.
//
// The deadline is the whole point. This store pins SQLite to ONE connection
// (core/internal/store/sqlstore/store.go:754, "SQLite is single-writer"), so a fence read issued
// from inside an open store transaction waits for the connection that transaction is already
// holding. Without a deadline that is a HANG; with one it is a named failure in two seconds.
type boundedFence struct{ st store.Store }

func (f boundedFence) EgressWriterFence(ctx context.Context) (store.RolloutState, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return f.st.(store.RolloutStater).RolloutState(ctx, EgressWriterFenceControlKey)
}

// TestTheFenceIsReadBeforeTheTransactionNotInsideIt pins the discipline the module already obeys for
// the secret sealer, in these same handlers, for the same reason: "the sealer is never invoked
// inside an open store transaction" (subscription.go).
//
// It was written RED. Before the fix, every governed writer built its proof from INSIDE
// mc.Data.Mutate, so with the fence seam actually wired — which the first-party binary does at boot,
// fatally (cmd/olivares/boot.go) — a create on SQLite waited on the single connection its own
// transaction held. The harness had never noticed because it left the seam nil, which is the one
// configuration where the read costs nothing.
//
// On PostgreSQL it does not deadlock; it quietly takes a SECOND connection per governed write and
// reads OUTSIDE the transaction's snapshot, which is a slower way of being wrong.
func TestTheFenceIsReadBeforeTheTransactionNotInsideIt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := newHarness(t)
	h.mod.UseEgressWriterFence(boundedFence{h.st})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.test", "editor")

	got := h.do("POST", "/v1/m/eventing/subscriptions", editor, map[string]any{
		"name": "siem", "endpoint": srv.URL, "event_types": []string{"finding.reported"},
		"role": "viewer",
	}, tenantHdr(tenant))
	if got.code != http.StatusCreated {
		t.Fatalf("a create with the fence seam wired returned %d %s — a governed writer that reads the fence from inside its own transaction waits on the connection that transaction holds", got.code, got.raw)
	}
}

// rolloutOf and fenceOf adapt a store to the two module seams, the way the composition root does.
type storeRollout struct {
	st  store.Store
	key string
}

func (r storeRollout) EgressRollout(ctx context.Context) (store.RolloutState, error) {
	return r.st.(store.RolloutStater).RolloutState(ctx, r.key)
}

func (r storeRollout) EgressWriterFence(ctx context.Context) (store.RolloutState, error) {
	return r.st.(store.RolloutStater).RolloutState(ctx, r.key)
}

func rolloutOf(st store.Store, key string) EgressRolloutSource { return storeRollout{st: st, key: key} }
func fenceOf(st store.Store) EgressWriterFenceSource {
	return storeRollout{st: st, key: EgressWriterFenceControlKey}
}

// TestTheStatusSurfaceReportsTheFencePostureWithoutClaimingTheFleet: an operator reading
// "destinations are ENFORCED" would reasonably assume every writer is gated. Until the fence is
// armed that assumption is wrong, and the only honest place to say so is next to the claim.
func TestTheStatusSurfaceReportsTheFencePostureWithoutClaimingTheFleet(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.test", "editor")

	got := h.do("GET", "/v1/m/eventing/egress-policy", editor, nil, nil)
	if got.code != http.StatusOK {
		t.Fatalf("status surface: %d", got.code)
	}
	if !strings.Contains(got.raw, `"writer_fence"`) {
		t.Fatalf("the status surface does not report the fence at all: %s", got.raw)
	}
	if !strings.Contains(got.raw, `"binary_capability":1`) {
		t.Fatalf("the status surface does not report what THIS binary declares, so a refusal cannot be diagnosed: %s", got.raw)
	}
	// It must never claim the fleet is proved. The words are load-bearing.
	for _, forbidden := range []string{"proved", "fleet_proved", "all_writers"} {
		if strings.Contains(got.raw, forbidden) {
			t.Fatalf("the status surface claims %q, which the fence cannot prove: %s", forbidden, got.raw)
		}
	}
}

// ---------------------------------------------------------------------------
// Unit H, commit 2 — every governed writer produces a proof, and nothing else does.
// ---------------------------------------------------------------------------

// attestations returns the writer proofs currently in a tenant's table.
func (h *harness) attestations(tenant model.TenantID) []model.Record {
	h.t.Helper()
	var out []model.Record
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(writerAttestKind)
		if err != nil {
			return err
		}
		out, _, err = repo.List(context.Background(), model.Query{Limit: 200})
		return err
	}); err != nil {
		h.t.Fatalf("read attestations: %v", err)
	}
	return out
}

// nonceOf returns the writer nonce stamped on a subscription row.
func (h *harness) nonceOf(tenant model.TenantID, id model.ID) string {
	h.t.Helper()
	var nonce string
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), id)
		if err != nil {
			return err
		}
		nonce = rec.String(colWriterNonce)
		return nil
	}); err != nil {
		h.t.Fatalf("read subscription: %v", err)
	}
	return nonce
}

// auditEvents renders the tenant's whole audit chain as text, so a test can assert what did NOT
// reach it. Rendering the event rather than inspecting fields is deliberate: what must never appear
// is a destination or a credential, and a field-by-field check would miss one that moved.
func (h *harness) auditEvents(tenant model.TenantID) []string {
	h.t.Helper()
	var out []string
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 0, func(ev model.AuditEvent) error {
			meta, _ := json.Marshal(ev.Meta)
			out = append(out, fmt.Sprintf("%s %s %s %s", ev.Action, ev.TargetKind, ev.Actor, string(meta)))
			return nil
		})
	}); err != nil {
		h.t.Fatalf("walk the audit chain: %v", err)
	}
	return out
}

// sinkNonceOf returns the writer nonce stamped on a subscription's sink profile row, and the
// profile's kind with it.
//
// The nonce on the ROW is what a governed sink write can be measured by now that the fence consumes
// the attestation it accepts: counting live proofs measured the world before enforcement, where
// nothing spent them.
func (h *harness) sinkNonceOf(tenant model.TenantID) (kind, nonce string) {
	h.t.Helper()
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionSinkKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Limit: 2})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			h.t.Fatalf("sink rows = %d, want exactly one", len(rows))
		}
		kind, nonce = rows[0].String(colSinkKind), rows[0].String(colWriterNonce)
		return nil
	}); err != nil {
		h.t.Fatalf("read sink: %v", err)
	}
	return kind, nonce
}

// TestEveryGovernedWriterStampsAProof walks the paths that can introduce or move a destination and
// requires each one to leave a proof bound to the row it wrote.
//
// It drives the PUBLIC paths — authoring over HTTP — rather than calling the helper, because this
// campaign has already found three tests that exercised a helper and would have passed with the
// behavior they claimed to pin completely absent.
func TestEveryGovernedWriterStampsAProof(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()

	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.test", "editor")

	// CREATE introduces a destination.
	id, _ := h.createSubscription(editor, tenant, map[string]any{
		"name": "siem", "endpoint": srv.URL, "event_types": []string{"finding.reported"},
		"role": "viewer",
	})
	created := h.nonceOf(tenant, model.ID(id))
	if created == "" {
		t.Fatal("a create left no writer proof on the row: an old binary's create would be indistinguishable from ours")
	}
	// The proof is SPENT by the write it authorized. This assertion read "want 1" while it was
	// written, because at that point nothing consumed anything; with the enforcing triggers in place
	// a proof that OUTLIVED its mutation would be a proof the next writer could use, so zero is the
	// property and one would be the bug.
	if got := len(h.attestations(tenant)); got != 0 {
		t.Fatalf("live proofs after a governed create = %d, want 0: the fence consumes the proof it accepts, and one left behind is one a second writer can spend", got)
	}

	// An UPDATE that MOVES the destination is governed, and gets a FRESH nonce — reusing the
	// stored one is exactly what an old binary does.
	got := h.do("PUT", "/v1/m/eventing/subscriptions/"+id, editor, map[string]any{
		"name": "siem", "endpoint": other.URL, "event_types": []string{"finding.reported"},
		"role": "viewer",
	}, nil)
	if got.code != http.StatusOK {
		t.Fatalf("update: %d %s", got.code, got.raw)
	}
	moved := h.nonceOf(tenant, model.ID(id))
	if moved == "" || moved == created {
		t.Fatalf("an update that moved the destination reused or dropped the nonce (%q → %q)", created, moved)
	}

	// An UPDATE that does NOT move the destination is deliberately NOT governed: unit G preserves
	// that a pre-existing subscription stays editable, including to disable it, and that is what an
	// operator does in an incident.
	got = h.do("PUT", "/v1/m/eventing/subscriptions/"+id, editor, map[string]any{
		"name": "siem renamed", "endpoint": other.URL, "event_types": []string{"finding.reported"},
		"role": "viewer", "enabled": false,
	}, nil)
	if got.code != http.StatusOK {
		t.Fatalf("non-moving update: %d %s", got.code, got.raw)
	}
	disabled := h.nonceOf(tenant, model.ID(id))
	if disabled != moved {
		t.Fatalf("a rename+disable produced a new nonce (%q → %q); the fence is meant to govern the DESTINATION, not every write", moved, disabled)
	}

	// REACTIVATING it IS governed. Turning egress back on makes a dormant destination effective
	// again, so it carries a proof — the rule being: the fence never blocks turning egress off, it
	// governs turning it on. This half was missing until an adversarial review of the
	// implementation found it, and its absence let an old binary resume delivery with no proof.
	got = h.do("PUT", "/v1/m/eventing/subscriptions/"+id, editor, map[string]any{
		"name": "siem renamed", "endpoint": other.URL, "event_types": []string{"finding.reported"},
		"role": "viewer", "enabled": true,
	}, nil)
	if got.code != http.StatusOK {
		t.Fatalf("reactivate: %d %s", got.code, got.raw)
	}
	if reactivated := h.nonceOf(tenant, model.ID(id)); reactivated == disabled {
		t.Fatalf("reactivating a disabled subscription reused the stored nonce (%q): the Go writer and the trigger's WHEN clause must govern the same transition, or an armed deployment refuses our own write", disabled)
	}
}

// TestTheDeliveryRailNeedsNoProof. Fencing it would break every rolling update: an old node keeps
// delivering while it is being replaced, and its delivery rail writes events, deliveries and
// cursors. Only authoring can introduce an ungoverned destination.
func TestTheDeliveryRailNeedsNoProof(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.test", "editor")
	h.createSubscription(editor, tenant, map[string]any{
		"name": "siem", "endpoint": srv.URL, "event_types": []string{"finding.reported"},
		"role": "viewer",
	})
	before := len(h.attestations(tenant))

	h.publishFinding(tenant, "scanner", "drift", "t")
	h.settle(tenant)
	if hits.Load() == 0 {
		t.Fatal("the delivery never went out")
	}
	if after := len(h.attestations(tenant)); after != before {
		t.Fatalf("the delivery rail produced %d new proofs; it must produce none, or an un-upgraded node cannot deliver while it is being replaced", after-before)
	}
}

// TestDroppingASinkProfileIsGoverned is the path the design contrast found. Deleting the profile
// "reverts to a generic webhook" — it MOVES the destination from the rendered URL to the base
// endpoint — and a fence on INSERT and UPDATE never saw it.
func TestDroppingASinkProfileIsGoverned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.test", "editor")
	id, _ := h.createSubscription(editor, tenant, map[string]any{
		"name": "siem", "endpoint": srv.URL, "event_types": []string{"finding.reported"},
		"role": "viewer", "sink_kind": "splunk_hec", "sink_cred": "tok",
	})

	// Drop the profile while the subscription SURVIVES.
	_, before := h.sinkNonceOf(tenant)
	if before == "" {
		t.Fatal("creating a sink profile left no proof on the row")
	}
	got := h.do("PUT", "/v1/m/eventing/subscriptions/"+id, editor, map[string]any{
		"name": "siem", "endpoint": srv.URL, "event_types": []string{"finding.reported"},
		"role": "viewer",
	}, nil)
	if got.code != http.StatusOK {
		t.Fatalf("drop the profile: %d %s", got.code, got.raw)
	}
	// The row survives as the generic-webhook shape rather than being deleted, and delivery still
	// works — the conversion must not change what the subscription DOES.
	sinkKind, nonce := h.sinkNonceOf(tenant)
	if sinkKind != "" {
		t.Fatalf("cleared sink kind = %q, want empty (the generic-webhook shape the delivery path already reads)", sinkKind)
	}
	// A FRESH proof, not the one the create left: reusing the stored nonce is precisely what an old
	// binary does, and the fence spends a proof per governed mutation.
	if nonce == "" || nonce == before {
		t.Fatalf("dropping a live sink profile reused or dropped the nonce (%q → %q): it re-points the destination to the base endpoint, and a fence that never sees it is open on exactly one mutation", before, nonce)
	}
	if live := len(h.attestations(tenant)); live != 0 {
		t.Fatalf("live proofs after the drop = %d, want 0: each governed mutation spends its own", live)
	}
}

// TestAnAgedOrphanProofIsSweptAndNotCountedAsOperatorWork pins the retention half, which nothing
// measured: the implementation contrast pointed out that removing the attestation block from
// pruneBatchOnce would not have turned a single test red.
//
// Both halves matter. An unconsumed proof must actually go — it is the shape that authorizes one old
// write after a repair (TestAProofOrphanedByDriftSurvivesARepairUntilTheGenerationMoves) — and it
// must NOT be counted in the number PruneExpired reports, because that number is an operator-facing
// count of evidence rows removed, and inflating it with internal bookkeeping would misreport
// retention.
//
// What this does NOT establish, and the comment on the descriptor now says so: this is a SWEEP, not
// an expiry. The trigger never checks a proof's age, so a proof stays valid until a sweep runs, and
// the sweep runs from the maintenance pump, which a deployment can disable.
func TestAnAgedOrphanProofIsSweptAndNotCountedAsOperatorWork(t *testing.T) {
	h := newHarness(t, WithRetention(time.Hour))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// A proof stamped and deliberately never spent.
	gen := fenceGenerationOf(t, h.st)
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := (WriterProof{Capability: EgressWriterCapability, Generation: gen}).Stamp(context.Background(), sc)
		return err
	}); err != nil {
		t.Fatalf("stamp an orphan: %v", err)
	}
	if got := len(h.attestations(tenant)); got != 1 {
		t.Fatalf("orphans before the sweep = %d, want 1", got)
	}

	// Younger than its own cutoff: the sweep must leave it alone, or a proof written moments before
	// a maintenance pass would be swept out from under the transaction that is about to use it.
	pruned, err := h.mod.PruneExpired(context.Background(), tenant)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if got := len(h.attestations(tenant)); got != 1 {
		t.Fatalf("a proof younger than the attestation cutoff was swept (%d left)", got)
	}
	if pruned != 0 {
		t.Fatalf("PruneExpired reported %d rows with nothing expired, want 0", pruned)
	}

	// Past the cutoff, it goes — and it is NOT added to the operator-facing count.
	h.clk.advance(2 * time.Hour)
	pruned, err = h.mod.PruneExpired(context.Background(), tenant)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if got := len(h.attestations(tenant)); got != 0 {
		t.Fatalf("an aged orphan proof survived the sweep (%d left): an unconsumed proof is what authorizes one old write after a repair", got)
	}
	if pruned != 0 {
		t.Fatalf("the sweep counted %d rows for an operator who lost no evidence: writer proofs are internal bookkeeping, not retention", pruned)
	}
}

// TestAFailedMutationLeavesNoProof. A proof that outlives the mutation it authorized is the shape
// that authorized an old write on SQLite. Both are written in one transaction, so a failure has to
// take both.
func TestAFailedMutationLeavesNoProof(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.test", "editor")

	// A create that fails INSIDE the transaction: a duplicate name is rejected after the proof is
	// written, so the rollback must take the proof with it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	h.createSubscription(editor, tenant, map[string]any{
		"name": "dup", "endpoint": srv.URL, "event_types": []string{"finding.reported"},
		"role": "viewer",
	})
	before := len(h.attestations(tenant))

	got := h.do("POST", "/v1/m/eventing/subscriptions", editor, map[string]any{
		"name": "dup", "endpoint": srv.URL, "event_types": []string{"finding.reported"},
		"role": "viewer",
	}, nil)
	if got.code < 400 {
		t.Fatalf("the duplicate create was accepted (%d); this test needs a create that fails inside the transaction", got.code)
	}
	if after := len(h.attestations(tenant)); after != before {
		t.Fatalf("a failed create left %d extra proof(s) behind: a proof must commit or roll back WITH the mutation it authorizes", after-before)
	}
}

// TestAProofNeverCarriesTheDestination. The proof travels in the same transaction as a URL and a
// sealed credential; it must carry neither, and neither must reach the audit ledger.
func TestAProofNeverCarriesTheDestination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.test", "editor")
	h.createSubscription(editor, tenant, map[string]any{
		"name": "siem", "endpoint": srv.URL, "event_types": []string{"finding.reported"},
		"role": "viewer", "sink_kind": "splunk_hec", "sink_cred": "super-secret-token",
	})

	// A proof written by a governed write is CONSUMED by it, so reading the table after one is
	// reading an empty list — and a loop over an empty list asserts nothing. This test iterated
	// exactly that and read as though it had checked something; the implementation contrast found it
	// vacuous. So the proof examined here is one that is deliberately NOT spent: stamped in a
	// transaction of its own, which leaves it live and inspectable.
	// The generation is read BEFORE the transaction, like every governed writer — reading it inside
	// would wait on the connection the transaction holds. Writing that line the wrong way here is how
	// this comment came to exist: the harness now wires the real fence, so it hung instead of
	// quietly working.
	gen := fenceGenerationOf(t, h.st)
	var live []model.Record
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := (WriterProof{Capability: EgressWriterCapability, Generation: gen}).Stamp(context.Background(), sc)
		return err
	}); err != nil {
		t.Fatalf("stamp a proof to inspect: %v", err)
	}
	live = h.attestations(tenant)
	if len(live) == 0 {
		t.Fatal("no proof to inspect: this test asserts what a proof CARRIES, so an empty list makes it vacuous — which is exactly what it used to be")
	}
	for _, rec := range live {
		for col, v := range rec {
			s, ok := v.(string)
			if !ok {
				continue
			}
			if strings.Contains(s, "http") || strings.Contains(s, "super-secret-token") {
				t.Fatalf("a writer proof carries %s=%q — it must carry a capability and a nonce, never a destination or a credential", col, s)
			}
		}
	}
	// And the AUDIT LEDGER must not carry them either, which the comment claimed and the test never
	// checked. The subscription's own audit event records ids, names and counts by design.
	for _, ev := range h.auditEvents(tenant) {
		if strings.Contains(ev, "super-secret-token") || strings.Contains(ev, srv.URL) {
			t.Fatalf("the audit ledger carries a destination or a credential: %s", ev)
		}
	}
}

// The probe matrix must cover every governed mutation the MIGRATIONS declare — derived, not
// asserted against a literal.
//
// The ceremony's test pinned `len(FenceProbes()) == 6` under a comment claiming that "adding a
// governed surface without adding its probe fails here". It does not: adding a trigger leaves the
// count at six and the assertion green. Only REMOVING a probe went red — the opposite direction
// from the one that matters, because a fence surface nobody probes is a surface `verify` reports
// green without testing.
//
// This derives the governed surface from the embedded fence migrations, so the day a migration
// declares a new `_writer_fence_<op>` trigger this goes red until a probe exists for it. The
// converse is checked too: a probe naming a (table, operation) no migration declares is a probe
// measuring nothing, which is the same defect pointed the other way.
func TestEveryGovernedTriggerHasAProbe(t *testing.T) {
	declared := map[string]bool{}
	for _, dir := range []string{"migrations/sqlite", "migrations/postgres"} {
		entries, err := migrationsFS.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			body, err := migrationsFS.ReadFile(dir + "/" + e.Name())
			if err != nil {
				t.Fatalf("read %s/%s: %v", dir, e.Name(), err)
			}
			for _, m := range triggerNameRE.FindAllStringSubmatch(string(body), -1) {
				name := m[len(m)-1]
				idx := strings.Index(name, "_writer_fence_")
				if idx < 0 {
					continue
				}
				table, op := name[:idx], name[idx+len("_writer_fence_"):]
				declared[table+"/"+op] = true
			}
		}
	}
	if len(declared) == 0 {
		t.Fatal("no fence triggers found in the embedded migrations: this test would pass vacuously, which is the failure mode it exists to prevent")
	}

	probed := map[string]bool{}
	for _, p := range FenceProbes() {
		if p.Table == "" || p.Op == "" {
			t.Fatalf("probe %q declares no governed (table, operation): coverage cannot be derived from it", p.Name)
		}
		probed[p.Table+"/"+p.Op] = true
	}

	for k := range declared {
		if !probed[k] {
			t.Errorf("the migrations govern %s but no probe exercises it: `fence verify` would report ENFORCING without testing that trigger", k)
		}
	}
	for k := range probed {
		if !declared[k] {
			t.Errorf("a probe exercises %s but no migration declares a fence trigger for it: that probe measures nothing", k)
		}
	}
}

// triggerNameRE captures the trigger identifier from both engines' CREATE TRIGGER syntax.
var triggerNameRE = regexp.MustCompile(`(?i)CREATE\s+TRIGGER\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z0-9_]+)`)

// The sink profile's three write branches, through the real API.
//
// TestEveryGovernedWriterStampsAProof walks the subscription writers and the sink CLEAR. The sink
// profile's own upsert has a branch per outcome and none had a case: CREATE when no profile exists, UPDATE
// when the rendered destination MOVES, and UPDATE when it does not. The third is the one worth
// having: it must go through carrying NO proof, because the fence governs turning egress on and a
// display-hint edit moves nothing — and it is also the branch where the Go comparison and the
// trigger's WHEN clause have to agree, which they did not before COALESCE normalised them.
func TestTheSinkUpsertStampsOnlyWhenTheRenderedDestinationMoves(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.test", "editor")

	// BRANCH 1 — no profile yet: the upsert CREATES one, and that introduces a rendered destination.
	id, _ := h.createSubscription(editor, tenant, map[string]any{
		"name": "siem", "endpoint": srv.URL, "event_types": []string{"finding.reported"},
		"role": "viewer", "sink_kind": "splunk_hec", "sink_cred": "token-1",
	})
	if got := len(h.attestations(tenant)); got != 0 {
		t.Fatalf("live proofs after creating a subscription WITH a sink profile = %d, want 0: both governed rows spend the proof that authorized them", got)
	}
	sink := h.sinkOf(tenant, model.ID(id))
	if sink == nil {
		t.Fatal("no sink profile was written: this case cannot say anything about the branches that follow")
	}
	// The NONCE stays on the row — it is the stamp, and it is what the trigger matched against. What
	// gets CONSUMED is the attestation row, asserted above. (Written the other way round first, from
	// a wrong reading of "consumes the proof": the case failed and was right to.)
	if sink.String(colWriterNonce) == "" {
		t.Fatal("the created sink row carries no writer nonce: a sink profile written by an old binary would be indistinguishable from ours")
	}

	// BRANCH 2 — the rendered destination MOVES. Same endpoint, different sink kind: where the bytes
	// go changes without a character of the endpoint changing, which is the whole reason the sink
	// columns are governed.
	if got := h.do("PUT", "/v1/m/eventing/subscriptions/"+id, editor, map[string]any{
		"name": "siem", "endpoint": srv.URL, "event_types": []string{"finding.reported"},
		"role": "viewer", "sink_kind": "datadog", "sink_cred": "token-1",
	}, nil); got.code != http.StatusOK {
		t.Fatalf("an update that moves the RENDERED destination was refused: %d %s — the writer carries the gate, so it must be allowed to move it", got.code, got.raw)
	}
	if got := len(h.attestations(tenant)); got != 0 {
		t.Fatalf("live proofs after a moving sink update = %d, want 0", got)
	}

	// BRANCH 3 — nothing effective changes. This must go through, and it must NOT have needed a
	// proof: a fence that charged one here would be refusing writes that move no destination, on
	// exactly the path where the Go comparison and the trigger's WHEN clause have to agree.
	before := h.sinkOf(tenant, model.ID(id))
	if got := h.do("PUT", "/v1/m/eventing/subscriptions/"+id, editor, map[string]any{
		"name": "siem-renamed", "endpoint": srv.URL, "event_types": []string{"finding.reported"},
		"role": "viewer", "sink_kind": "datadog", "sink_cred": "token-1",
	}, nil); got.code != http.StatusOK {
		t.Fatalf("an update that moves NOTHING was refused: %d %s — the fence governs turning egress ON, and charging a proof here makes it an obstacle", got.code, got.raw)
	}
	after := h.sinkOf(tenant, model.ID(id))
	if before.String(colSinkKind) != after.String(colSinkKind) || before.String(colSinkCred) != after.String(colSinkCred) {
		t.Fatal("premise broken: this case only means something while the rendered destination really did stay put")
	}
	if got := len(h.attestations(tenant)); got != 0 {
		t.Fatalf("live proofs after a non-moving sink update = %d, want 0: a proof written for a mutation the fence does not govern is a proof left lying for the next writer", got)
	}
}

// sinkOf returns the sink profile row of a subscription, or nil when it has none.
func (h *harness) sinkOf(tenant model.TenantID, subID model.ID) model.Record {
	h.t.Helper()
	var out model.Record
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionSinkKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Limit: 50})
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.String(colSinkSubRef) == subID.String() {
				out = r
				return nil
			}
		}
		return nil
	}); err != nil {
		h.t.Fatalf("read the sink profile: %v", err)
	}
	return out
}
