// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// memTombstones is a TombstoneLog that keeps the encoded lines, so a test reads
// exactly the bytes a file would hold.
type memTombstones struct {
	mu    sync.Mutex
	key   []byte
	lines [][]byte
}

func newMemTombstones() *memTombstones {
	return &memTombstones{key: bytes.Repeat([]byte{7}, 32)}
}

func (l *memTombstones) Key() ([]byte, error) { return l.key, nil }

func (l *memTombstones) Append(t ErasureTombstone) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, b)
	return nil
}

func (l *memTombstones) All() ([]ErasureTombstone, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []ErasureTombstone
	for _, b := range l.lines {
		var t ErasureTombstone
		if err := json.Unmarshal(b, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// A completed erasure leaves one tombstone, and the subject's identifiers are in
// it only sealed. The store that holds the receipt has nothing to replay.
func TestErasureTombstoneHoldsNoIdentifierInClear(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-ok", "alice", "bob")
	acc := &stubAccountEraser{outcome: AccountEraseOutcome{Attempted: true, Erased: 1, Detail: "ok"}}
	prov := &stubProviderEraser{outcome: ProviderEraseOutcome{Wired: true}}
	log := newMemTombstones()
	h := newHarness(t, WithApprovalGate(gate), WithAccountEraser(acc), WithProviderEraser(prov), WithTombstoneLog(log))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "tombstone")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	hdr := tenantHdr(tenant)

	const email, alias = "maria@example.com", "user:11111111-2222-3333-4444-555555555555"
	r := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "user", "subject_ref": email, "aliases": []string{alias}, "case_ref": "DSR-TOMB",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	if r := h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, map[string]any{}, hdr); r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}

	if len(log.lines) != 1 {
		t.Fatalf("tombstones = %d, want 1", len(log.lines))
	}
	line := log.lines[0]
	for _, clear := range []string{email, "maria", "11111111-2222-3333-4444-555555555555"} {
		if bytes.Contains(line, []byte(clear)) {
			t.Fatalf("the tombstone holds %q in clear: %s", clear, line)
		}
	}
	tombs, _ := log.All()
	tomb := tombs[0]
	if tomb.Tenant != tenant.String() || tomb.Erasure != id || tomb.Kind != "user" || tomb.KeyID == "" || tomb.ErasedAt.IsZero() {
		t.Fatalf("tombstone = %+v", tomb)
	}
	p, err := openSubjectPayload(tenant, subjectKey{ID: model.ID(tomb.KeyID), Kind: tomb.Kind, dek: log.key}, tomb.Subject)
	if err != nil || p.Ref != email || !slices.Equal(p.Aliases, []string{alias}) {
		t.Fatalf("sealed subject = %+v err=%v, want the ref and its alias", p, err)
	}

	rep, err := h.mod.ReplayErasures(context.Background())
	if err != nil || rep.Reapplied != 0 || rep.Present != 1 {
		t.Fatalf("replay over the store that holds the receipt = %+v err=%v, want 1 present", rep, err)
	}
	if len(acc.calls) != 1 {
		t.Fatalf("account leg calls = %d, want only the live erasure's", len(acc.calls))
	}
}

// A tombstone the store has no receipt for runs again and shreds the subject's
// key, with a replay event on a chain that still verifies. A legal hold in the
// store stops it, as it stops a live erasure.
func TestErasureReplayReappliesUnlessHeld(t *testing.T) {
	acc := &stubAccountEraser{outcome: AccountEraseOutcome{Attempted: true}}
	log := newMemTombstones()
	h := newHarness(t, WithAccountEraser(acc), WithTombstoneLog(log))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "replay")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	ctx := context.Background()

	// The restored store: the subject keys exist and no receipt names the erasures.
	keys := map[string]subjectKey{}
	h.mutate(tenant, func(sc store.Scope) error {
		for _, ref := range []string{"maria@example.com", "held@example.com"} {
			k, err := mintSubjectKey(ctx, sc, "user", ref, nil, "test")
			if err != nil {
				return err
			}
			keys[ref] = k
		}
		return nil
	})
	// maria's erasure failed once after its tombstone was written, so it has two.
	for _, ref := range []string{"maria@example.com", "maria@example.com", "held@example.com"} {
		if err := h.mod.recordTombstone(tenant, model.ID("e-"+ref), keys[ref], affectedClasses("user")); err != nil {
			t.Fatal(err)
		}
	}
	if r := h.do("POST", "/v1/m/compliance/holds", owner, map[string]any{
		"matter_ref": "M-9", "scope_kind": "subject", "subject_kind": "user", "subject_ref": "held@example.com",
		"reason": "litigation",
	}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("hold = %d %s", r.code, r.raw)
	}

	rep, err := h.mod.ReplayErasures(ctx)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if rep.Reapplied != 1 || rep.Present != 0 || !slices.Equal(rep.Held, []string{"e-held@example.com"}) {
		t.Fatalf("replay = %+v, want 1 reapplied and the held erasure reported", rep)
	}
	if len(acc.calls) != 1 || acc.calls[0][0] != "maria@example.com" {
		t.Fatalf("account leg calls = %+v, want the replayed subject only", acc.calls)
	}
	h.mutate(tenant, func(sc store.Scope) error {
		if _, err := getSubjectKey(ctx, sc, keys["maria@example.com"].ID); !errors.Is(err, ErrKeyShredded) {
			t.Errorf("replayed subject key err = %v, want shredded", err)
		}
		if _, err := getSubjectKey(ctx, sc, keys["held@example.com"].ID); err != nil {
			t.Errorf("held subject key err = %v, want alive", err)
		}
		v, err := sc.Audit().Verify(ctx, 0)
		if err != nil || !v.OK {
			t.Errorf("chain after the replay = %+v err=%v", v, err)
		}
		return nil
	})
	if !slices.Contains(h.auditActions(tenant), "compliance.erasure.replay") {
		t.Fatalf("audit actions = %v, want compliance.erasure.replay", h.auditActions(tenant))
	}
}

// A replay leaves the store holding it applied, so a later restore of a backup
// taken after the replay, or a second run over the same store, does not erase
// again: the person may have come back legitimately under the same email.
func TestErasureReplayRunsOncePerStore(t *testing.T) {
	acc := &stubAccountEraser{outcome: AccountEraseOutcome{Attempted: true}}
	log := newMemTombstones()
	h := newHarness(t, WithAccountEraser(acc), WithTombstoneLog(log))
	tenant := h.createOrg(h.adminLogin(), "once")
	ctx := context.Background()
	var key subjectKey
	h.mutate(tenant, func(sc store.Scope) (err error) {
		key, err = mintSubjectKey(ctx, sc, "user", "maria@example.com", nil, "test")
		return err
	})
	if err := h.mod.recordTombstone(tenant, model.ID("e-once"), key, affectedClasses("user")); err != nil {
		t.Fatal(err)
	}

	if rep, err := h.mod.ReplayErasures(ctx); err != nil || rep.Reapplied != 1 {
		t.Fatalf("first replay = %+v err=%v, want 1 reapplied", rep, err)
	}
	rep, err := h.mod.ReplayErasures(ctx)
	if err != nil || rep.Reapplied != 0 || rep.Present != 1 {
		t.Fatalf("second replay = %+v err=%v, want 1 present", rep, err)
	}
	if len(acc.calls) != 1 {
		t.Fatalf("account leg calls = %v, want only the first replay's", acc.calls)
	}
}

// A tombstone of a tenant the restored store does not hold has nothing to erase:
// no account leg runs and no chain is written for that tenant.
func TestErasureReplaySkipsATenantTheStoreDoesNotHold(t *testing.T) {
	acc := &stubAccountEraser{outcome: AccountEraseOutcome{Attempted: true}}
	log := newMemTombstones()
	h := newHarness(t, WithAccountEraser(acc), WithTombstoneLog(log))
	h.createOrg(h.adminLogin(), "present")
	absent := model.NewTenantID()
	key := subjectKey{ID: model.NewID(), Kind: erasureSubjectUser, Ref: "maria@example.com"}
	if err := h.mod.recordTombstone(absent, model.ID("e-absent"), key, affectedClasses("user")); err != nil {
		t.Fatal(err)
	}

	rep, err := h.mod.ReplayErasures(context.Background())
	if err != nil || rep.Reapplied != 0 || rep.Present != 0 || rep.Absent != 1 {
		t.Fatalf("replay for an absent tenant = %+v err=%v, want 1 absent", rep, err)
	}
	if len(acc.calls) != 0 {
		t.Fatalf("account leg calls = %v, want none", acc.calls)
	}
}

// failingVerifyCoordinator fails the shred transaction at its coordinator
// verification, a step after the shred itself.
type failingVerifyCoordinator struct{ *stubCryptoShredCoordinator }

func (failingVerifyCoordinator) VerifyShredCompleteness(context.Context, string, []string, CryptoShredProbes) (CryptoShredVerification, error) {
	return CryptoShredVerification{}, errors.New("coordinator down")
}

// An execute whose shred transaction rolls back leaves no tombstone, so no
// restore completes an erasure that never completed live.
func TestErasureRolledBackLeavesNoTombstone(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-ok", "alice", "bob")
	acc := &stubAccountEraser{outcome: AccountEraseOutcome{Attempted: true, Erased: 1, Detail: "ok"}}
	prov := &stubProviderEraser{outcome: ProviderEraseOutcome{Wired: true}}
	log := newMemTombstones()
	h := newHarness(t, WithApprovalGate(gate), WithAccountEraser(acc), WithProviderEraser(prov), WithTombstoneLog(log),
		WithCryptoShredCoordinator(failingVerifyCoordinator{&stubCryptoShredCoordinator{}}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "rollback")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	hdr := tenantHdr(tenant)
	r := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "user", "subject_ref": "maria@example.com", "case_ref": "DSR-RB",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/compliance/erasure/"+r.body["id"].(string)+"/execute", owner, map[string]any{}, hdr); r.code == http.StatusOK {
		t.Fatalf("execute = %d %s, want the coordinator failure", r.code, r.raw)
	}
	if len(log.lines) != 0 {
		t.Fatalf("tombstones after a rolled-back execute = %d, want 0", len(log.lines))
	}
}
