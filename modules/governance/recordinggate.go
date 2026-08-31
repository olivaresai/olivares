// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// RecordingGate is the seam through which break-glass demands its
// MANDATORY session recording (SEC-G5): an emergency window whose
// console actions are not being recorded must never open. Expressed in this
// module's own terms (a module never imports a sibling); the composition root
// wires the recording module, which satisfies it structurally. The default is
// DENY-CLOSED: with no recorder wired, break-glass activation refuses.
type RecordingGate interface {
	// EnsureActive returns the principal's ACTIVE recording session. By the time
	// the activation handler runs, the engine's recording wrapper has already
	// gated (and so opened) the session — a miss means recording is not actually
	// capturing, and activation must refuse.
	EnsureActive(ctx context.Context, tenant model.TenantID, p auth.Principal) (model.ID, error)
	// BindGrant stamps the activated grant onto its recording session (the
	// first-class linkage the replay console joins on). It remains the standalone
	// compatibility surface; the break-glass HTTP path requires
	// AtomicRecordingGate so grant creation and binding share one transaction.
	BindGrant(ctx context.Context, tenant model.TenantID, session, grant model.ID, p auth.Principal) error
}

// AtomicRecordingGate is the additive transaction-scoped capability required by
// break-glass. Keeping it separate preserves RecordingGate's established
// EnsureActive and BindGrant functions while refusing to fall back to their
// separate transactions on a safety-critical path.
type AtomicRecordingGate interface {
	// BindGrantInScope validates and binds the exact session ID returned by the
	// engine Gate inside the caller's transaction.
	BindGrantInScope(ctx context.Context, sc store.Scope, session, grant model.ID, p auth.Principal) error
	// SealGrantInScope seals the one active recording bound to grant inside the
	// caller's review transaction.
	SealGrantInScope(ctx context.Context, sc store.Scope, grant model.ID, reviewer auth.Principal) error
}

// denyRecordingGate is the deny-closed default: break-glass without recording
// is exactly the silent emergency power SEC-G5 forbids, so an unwired gate
// refuses activation rather than degrading.
type denyRecordingGate struct{}

func (denyRecordingGate) EnsureActive(context.Context, model.TenantID, auth.Principal) (model.ID, error) {
	return "", errors.New("session recording is not wired; break-glass requires an actively recorded session")
}

func (denyRecordingGate) BindGrant(context.Context, model.TenantID, model.ID, model.ID, auth.Principal) error {
	return errors.New("session recording is not wired")
}

func (denyRecordingGate) BindGrantInScope(context.Context, store.Scope, model.ID, model.ID, auth.Principal) error {
	return api.ErrRecordingSessionPrecondition
}

func (denyRecordingGate) SealGrantInScope(context.Context, store.Scope, model.ID, auth.Principal) error {
	return api.ErrRecordingSessionPrecondition
}

// UseRecordingGate wires the recorder (additive module-level injection,
// parallel to UseLifecycleGate). Safe to call before Start; nil restores the
// deny-closed default.
func (m *Module) UseRecordingGate(g RecordingGate) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g == nil {
		g = denyRecordingGate{}
	}
	m.recordingGate = g
}

// recordingGateNow returns the wired gate (or the deny-closed default).
func (m *Module) recordingGateNow() RecordingGate {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recordingGate == nil {
		return denyRecordingGate{}
	}
	return m.recordingGate
}
