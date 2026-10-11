// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// KillSwitchSnapshot describes posture under the caller's transaction barrier.
// It grants no authority and does not prove a successful commit. Discard it if
// the enclosing transaction rolls back or its commit outcome is unknown.
type KillSwitchSnapshot struct {
	tenant     model.TenantID
	generation int64
	state      StopState
}

func (s KillSwitchSnapshot) Tenant() model.TenantID { return s.tenant }
func (s KillSwitchSnapshot) Generation() int64      { return s.generation }

// State returns a copy; changing it cannot change this snapshot.
func (s KillSwitchSnapshot) State() StopState {
	st := s.state
	st.AgentRefs = make(map[string]model.ID, len(s.state.AgentRefs))
	for ref, id := range s.state.AgentRefs {
		st.AgentRefs[ref] = id
	}
	return st
}

// LockKillSwitchState fences every supported stop transition in this exact
// Mutate scope. Directory/user authority locks precede this barrier; acquire it
// before stop, action, approval, signal or consumer target row locks. A View or
// a scope lacking TransactionLocker is refused. The first call establishes a
// generation domain, not historical evidence: actual stop rows are always read.
func (m *Module) LockKillSwitchState(ctx context.Context, sc store.Scope) (KillSwitchSnapshot, error) {
	rec, err := lockKillSwitchGeneration(ctx, sc)
	if err != nil {
		return KillSwitchSnapshot{}, err
	}
	state, err := readKillSwitchState(ctx, sc)
	if err != nil {
		return KillSwitchSnapshot{}, err
	}
	return KillSwitchSnapshot{tenant: sc.Tenant(), generation: rec.Int(colKSGeneration), state: state}, nil
}

func lockKillSwitchTransaction(ctx context.Context, sc store.Scope) error {
	if sc == nil {
		return errors.New("kill-switch fence requires a scope")
	}
	tenant := sc.Tenant()
	parsed, err := model.ParseTenantID(tenant.String())
	if err != nil || parsed != tenant || tenant.IsZero() || tenant.IsSystem() {
		return errors.New("kill-switch fence requires a canonical tenant")
	}
	locker, ok := sc.(store.TransactionLocker)
	if !ok {
		return errors.New("kill-switch transaction fence unavailable")
	}
	if err := locker.LockTransaction(ctx, "governance.killswitch:"+tenant.String()); err != nil {
		return fmt.Errorf("lock kill-switch state: %w", err)
	}
	return nil
}

// No supported writer deletes or resets this singleton. Creation starts a new
// domain on an old store; it never implies the tenant has no active stops.
func lockKillSwitchGeneration(ctx context.Context, sc store.Scope) (model.Record, error) {
	if err := lockKillSwitchTransaction(ctx, sc); err != nil {
		return nil, err
	}
	repo, err := sc.Ext(killSwitchGenerationKind)
	if err != nil {
		return nil, err
	}
	recs, _, err := repo.List(ctx, model.Query{Limit: 2})
	if err != nil {
		return nil, err
	}
	var rec model.Record
	switch len(recs) {
	case 0:
		rec, err = repo.Create(ctx, model.Record{colKSGeneration: int64(1)})
		if err != nil {
			return nil, err
		}
	case 1:
		rec = recs[0]
	default:
		return nil, errors.New("duplicate kill-switch generation")
	}
	id, err := model.ParseID(rec.String(model.ColID))
	if err != nil || id.IsZero() || id.String() != rec.String(model.ColID) ||
		rec.String(model.ColTenantID) != sc.Tenant().String() ||
		rec.Int(colKSGeneration) <= 0 || rec.Int(model.ColVersion) <= 0 {
		return nil, errors.New("invalid kill-switch generation")
	}
	return rec, nil
}

// Advance only once for a new engage or an active-to-reenabled transition.
// The caller propagates errors so its generation and effect commit together.
func advanceKillSwitchGeneration(ctx context.Context, sc store.Scope, rec model.Record) error {
	if rec.Int(colKSGeneration) == math.MaxInt64 || rec.Int(model.ColVersion) == math.MaxInt64 {
		return errors.New("kill-switch generation exhausted")
	}
	repo, err := sc.Ext(killSwitchGenerationKind)
	if err != nil {
		return err
	}
	rec[colKSGeneration] = rec.Int(colKSGeneration) + 1
	_, err = repo.Update(ctx, rec)
	return err
}

func readKillSwitchState(ctx context.Context, sc store.Scope) (StopState, error) {
	st := StopState{AgentRefs: map[string]model.ID{}}
	repo, err := sc.Ext(killSwitchKind)
	if err != nil {
		return st, err
	}
	recs, err := listAll(ctx, repo, eq(colKSStatus, ksStatusActive))
	if err != nil {
		return st, err
	}
	return stopStateFromRows(recs), nil
}

// stopStateFromRows is shared by live-state and credential-epoch reads, including
// their estate precedence and attribution when several stops match one agent.
func stopStateFromRows(recs []model.Record) StopState {
	st := StopState{AgentRefs: map[string]model.ID{}}
	for _, rec := range recs {
		if rec.String(colKSStatus) != ksStatusActive {
			continue
		}
		id := model.ID(rec.String(model.ColID))
		switch rec.String(colKSScopeKind) {
		case ksScopeEstate:
			st.EstateStopped, st.EstateStopID = true, id
		case ksScopeAgent:
			for _, ref := range []string{rec.String(colKSScopeRef), rec.String(colKSAgentID), rec.String(colKSAgentExternal)} {
				if ref != "" {
					st.AgentRefs[ref] = id
				}
			}
		}
	}
	return st
}
