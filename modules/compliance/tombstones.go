// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A restore brings back everything its backup holds, including a person erased
// after the backup was taken. Each completed erasure therefore leaves a tombstone
// outside the store, which no restore overwrites, and a restore applies again every
// erasure the restored store does not hold applied. The backup stays as it
// is until its retention expires: the replay keeps it beyond use (docs/DR-RUNBOOK.md).

// ErasureTombstone is the minimum a replay needs. The subject's identifiers are
// sealed under the tombstone key (the subject-payload AEAD, bound to the tenant, the
// key-ring id and the kind); every other field is an id, a class id or an instant.
type ErasureTombstone struct {
	Tenant   string    `json:"tenant"`
	Erasure  string    `json:"erasure"`
	KeyID    string    `json:"key_id"`
	Kind     string    `json:"kind"`
	Classes  []string  `json:"classes"`
	Subject  string    `json:"subject"`
	ErasedAt time.Time `json:"erased_at"`
}

// TombstoneLog keeps tombstones where a restore cannot reach them. Append must be
// durable when it returns: the shred commits only after it.
type TombstoneLog interface {
	Key() ([]byte, error)
	Append(ErasureTombstone) error
	All() ([]ErasureTombstone, error)
}

// WithTombstoneLog wires the tombstone log. Without it an erasure leaves no
// tombstone, and a restore of an older backup brings the subject back.
func WithTombstoneLog(l TombstoneLog) Option {
	return func(m *Module) { m.tombstones = l }
}

// replayActor is who the ledger names for a replayed erasure.
const replayActor = "system:dr-restore"

// replayEventAction is the event a replay appends in the transaction of its
// shred: the store's own record that the erasure is applied.
const replayEventAction = "compliance.erasure.replay"

// maxReplayPasses bounds the batch loop of one replayed erasure (each pass
// deletes up to the batch cap of every target).
const maxReplayPasses = 1000

// recordTombstone appends the erasure's tombstone inside the shred transaction, so
// a key never dies without one.
func (m *Module) recordTombstone(tenant model.TenantID, erasure model.ID, key subjectKey, classes []string) error {
	if m.tombstones == nil {
		return nil
	}
	tk, err := m.tombstones.Key()
	if err != nil {
		return fmt.Errorf("compliance: tombstone key: %w", err)
	}
	sealed, err := sealSubjectPayload(tenant, subjectKey{ID: key.ID, Kind: key.Kind, Ref: key.Ref, Aliases: key.Aliases, dek: tk})
	if err != nil {
		return err
	}
	return m.tombstones.Append(ErasureTombstone{
		Tenant: tenant.String(), Erasure: erasure.String(), KeyID: key.ID.String(), Kind: key.Kind,
		Classes: classes, Subject: sealed, ErasedAt: m.clock.Now().Time().UTC(),
	})
}

// ErasureReplay is what ReplayErasures did.
type ErasureReplay struct {
	// Reapplied counts erasures the restored store predated and that ran again.
	Reapplied int
	// Present counts erasures the restored store already holds applied: their
	// receipt, or the event of an earlier replay.
	Present int
	// Absent counts erasures of a tenant the restored store does not hold.
	Absent int
	// Held lists the erasures a legal hold in the restored store stopped.
	Held []string
}

// ReplayErasures applies again every tombstoned erasure the store does not hold
// applied (no receipt and no earlier replay event): the physical erasure over the
// target registry, the account leg and the crypto-shred, with a ledger event per
// erasure. A tenant the store does not hold has nothing to erase. The provider leg
// is not replayed: the provider's copy is not in our backup. A legal hold in the
// restored store wins, as it does for a live erasure, and is reported in Held.
func (m *Module) ReplayErasures(ctx context.Context) (ErasureReplay, error) {
	var out ErasureReplay
	if m.tombstones == nil {
		return out, nil
	}
	all, err := m.tombstones.All()
	if err != nil {
		return out, fmt.Errorf("compliance: read erasure tombstones: %w", err)
	}
	if len(all) == 0 {
		return out, nil
	}
	if m.data == nil {
		return out, errors.New("compliance: erasure replay has no data handle")
	}
	tk, err := m.tombstones.Key()
	if err != nil {
		return out, fmt.Errorf("compliance: tombstone key: %w", err)
	}
	// An execute whose commit failed after its tombstone was written, and that ran
	// again, leaves two lines for one erasure.
	seen := map[string]bool{}
	for _, t := range all {
		if seen[t.Erasure] {
			continue
		}
		seen[t.Erasure] = true
		state, err := m.replayErasure(ctx, tk, t)
		switch {
		case errors.Is(err, errErasureHeld):
			out.Held = append(out.Held, t.Erasure)
		case err != nil:
			return out, fmt.Errorf("compliance: replay erasure %s: %w", t.Erasure, err)
		case state == replayDue:
			out.Reapplied++
		case state == replayApplied:
			out.Present++
		default:
			out.Absent++
		}
	}
	return out, nil
}

func (m *Module) replayErasure(ctx context.Context, tk []byte, t ErasureTombstone) (replayOutcome, error) {
	tenant, err := model.ParseTenantID(t.Tenant)
	if err != nil {
		return replayDue, err
	}
	state, err := m.replayState(ctx, tenant, t.Erasure)
	if err != nil || state != replayDue {
		return state, err
	}
	p, err := openSubjectPayload(tenant, subjectKey{ID: model.ID(t.KeyID), Kind: t.Kind, dek: tk}, t.Subject)
	if err != nil {
		return replayDue, err
	}
	key := subjectKey{ID: model.ID(t.KeyID), Kind: t.Kind, Ref: p.Ref, Aliases: p.Aliases}
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		return erasureHeld(ctx, sc, key, t.Classes)
	}); err != nil {
		return replayDue, err
	}
	for pass := 0; ; pass++ {
		outcomes, err := m.runErasureTargets(ctx, tenant, replayActor, model.ActorSystem, key, t.Classes)
		if err != nil {
			return replayDue, err
		}
		truncated := false
		for _, o := range outcomes {
			truncated = truncated || o.Truncated
		}
		if !truncated {
			break
		}
		if pass == maxReplayPasses {
			return replayDue, fmt.Errorf("still truncated after %d passes", maxReplayPasses)
		}
	}
	if key.Kind == erasureSubjectUser {
		if _, err := m.accounts.EraseAccount(ctx, tenant, key.identifiers(), replayActor, model.ActorSystem); err != nil {
			return replayDue, err
		}
	}
	return replayDue, m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		if err := erasureHeld(ctx, sc, key, t.Classes); err != nil {
			return err
		}
		if _, err := getSubjectKey(ctx, sc, key.ID); err == nil {
			if err := shredSubjectKey(ctx, sc, key.ID); err != nil {
				return err
			}
		} else if !errors.Is(err, ErrKeyShredded) {
			return err
		}
		_, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: replayActor, ActorKind: model.ActorSystem, Action: replayEventAction,
			TargetKind: erasureRequestKind, TargetID: model.ID(t.Erasure),
			Meta: map[string]any{"erased_at": t.ErasedAt.Format(time.RFC3339), "subject_kind": t.Kind},
		})
		return err
	})
}

type replayOutcome int

const (
	replayDue     replayOutcome = iota // the store predates the erasure
	replayApplied                      // the receipt or an earlier replay is in the store
	replayAbsent                       // the store does not hold the tenant
)

// replayState reads what the restored store holds of one erasure. A replay writes
// no receipt, so its event is what tells a later restore (of a backup taken after
// it) that the erasure is applied; without it the replay would run again and
// erase a person who came back under the same identifier.
func (m *Module) replayState(ctx context.Context, tenant model.TenantID, erasure string) (replayOutcome, error) {
	state := replayDue
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		if _, err := sc.Org(ctx); errors.Is(err, store.ErrNotFound) {
			state = replayAbsent
			return nil
		} else if err != nil {
			return err
		}
		repo, err := sc.Ext(erasureReceiptKind)
		if err != nil {
			return err
		}
		_, present, err := findOne(ctx, repo, eq(colRCErasureID, erasure))
		if err != nil {
			return err
		}
		if present {
			state = replayApplied
			return nil
		}
		replayed, err := replayRecorded(ctx, sc.Audit(), erasure)
		if replayed {
			state = replayApplied
		}
		return err
	})
	return state, err
}

// errReplayFound stops the chain walk at the first replay event.
var errReplayFound = errors.New("replay event found")

func replayRecorded(ctx context.Context, log store.AuditLog, erasure string) (bool, error) {
	match := func(ev model.AuditEvent) error {
		if ev.Action == replayEventAction && ev.TargetKind == erasureRequestKind && ev.TargetID.String() == erasure {
			return errReplayFound
		}
		return nil
	}
	var err error
	if fw, ok := log.(store.FilteredWalker); ok {
		err = fw.WalkFiltered(ctx, 0, store.AuditFilter{
			ActionPrefix: replayEventAction, TargetKind: string(erasureRequestKind), TargetID: erasure,
		}, match)
	} else {
		err = log.Walk(ctx, 0, match)
	}
	if errors.Is(err, errReplayFound) {
		return true, nil
	}
	return false, err
}
