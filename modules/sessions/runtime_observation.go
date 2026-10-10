// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// RecordSessionObservation records a resolved session's tool, cost or finding
// through the existing owning bridge, then publishes the canonical session fact.
// The current registered process, launch and Claim fence must all agree. An old
// generation never falls back to a cooperative row. Observations must already be
// safe to retain; this port neither opens secrets nor changes policy inputs.
func (m *Module) RecordSessionObservation(ctx context.Context, principal auth.Principal, obs sdkmodel.Observation) error {
	tenant := principal.SessionScope()
	if tenant.IsZero() || tenant.IsSystem() || !principal.IsMember(tenant) || principal.SessionIdentity == "" || principal.SessionRunRef == "" || principal.SessionFence < 1 {
		return auth.ErrUnauthenticated
	}
	if m.publishObservation == nil || m.Data == nil {
		return errors.New("sessions: observation publication is unavailable")
	}
	lr, ok := m.rt.getLive(tenant, principal.SessionRunRef)
	if !ok || lr.claim.SID != principal.SessionIdentity || lr.claim.Fence != principal.SessionFence {
		return auth.ErrUnauthenticated
	}
	// Normalize pointers without retaining the caller's DTO. Nil and non-session
	// observations refuse rather than creating an unowned projection.
	switch v := obs.(type) {
	case *sdkmodel.EdgeObservation:
		if v == nil {
			return auth.ErrUnauthenticated
		}
		obs = *v
	case *sdkmodel.CostSample:
		if v == nil {
			return auth.ErrUnauthenticated
		}
		obs = *v
	case *sdkmodel.FindingReport:
		if v == nil {
			return auth.ErrUnauthenticated
		}
		obs = *v
	}
	sid := principal.SessionIdentity
	belongs := func(ref string) bool { return ref == sid || ref == principal.SessionRunRef }
	switch v := obs.(type) {
	case sdkmodel.EdgeObservation:
		if v.OriginKind != "session" || !belongs(v.OriginRef) {
			return auth.ErrUnauthenticated
		}
		v.OriginRef = sid
		obs = v
	case sdkmodel.CostSample:
		if v.SessionRef != "" && !belongs(v.SessionRef) {
			return auth.ErrUnauthenticated
		}
		v.SessionRef = sid
		obs = v
	case sdkmodel.FindingReport:
		if v.SubjectKind != "session" || !belongs(v.SubjectRef) {
			return auth.ErrUnauthenticated
		}
		v.SubjectRef = sid
		obs = v
	default:
		return errors.New("sessions: observation is not a session tool, cost or finding")
	}
	e := event.FromObservation(tenant.String(), Name, obs)
	at := nonZeroTime(e.Time, m.clock)
	return m.withRegisteredProfiledRun(lr, func() error {
		lr.mu.Lock()
		terminal := lr.finalized || lr.stopRequested || lr.launchFailed
		lr.mu.Unlock()
		if terminal {
			return auth.ErrUnauthenticated
		}
		var snap liveSnapshot
		err := m.mutateProfiledRun(ctx, lr, func(sc store.Scope, run model.Record) error {
			if run.String(colRunAuthzWorkspaceID) != principal.SessionWorkspaceID.String() {
				return auth.ErrUnauthenticated
			}
			live, found, err := findManagedLive(ctx, sc, sid)
			if err != nil {
				return err
			}
			if !found {
				external := run.String(colClaudeSessionID)
				if external == "" {
					external = sid
				}
				live, err = m.upsertManagedLive(ctx, sc, managedAliasInput{profileID: lr.profile.ProfileID, provider: lr.profile.Driver, externalID: external, sid: sid, runRef: lr.runRef, launchID: lr.launchID, claimFence: lr.claim.Fence}, lr.profile.EnvironmentRef, at)
				if err != nil {
					return err
				}
			}
			scope := managedScope(lr.profile.ProfileID, lr.profile.Driver, lr.profile.EnvironmentRef, sid, lr.runRef)
			if live.String(colObservationScope) != scope.scope || live.String(colLiveRunRef) != lr.runRef || live.String(colLiveProfileID) != scope.profileID || live.String(colLiveEnvRef) != scope.envRef {
				return auth.ErrUnauthenticated
			}
			var kind, tool, resource, mode, source, title string
			switch v := e.Payload.(type) {
			case sdkmodel.EdgeObservation:
				applyLiveEdge(live, scope, v, at)
				kind, tool, resource, mode, source, title = tlTool, v.ToolRef, v.ResourceRef, string(v.Mode), string(v.Source), edgeTitle(v)
				if v.ResourceKind == "mcp.server" {
					kind = tlMCP
				}
			case sdkmodel.CostSample:
				v.Labels = maps.Clone(v.Labels)
				if v.Labels == nil {
					v.Labels = make(map[string]string)
				}
				v.Labels[event.SessionCoreIDLabel] = run.String(colRunCoreSessionID)
				e.Payload = v
				applyLiveCost(live, v, at)
				kind, source, title = tlCost, "cost", fmt.Sprintf("%d in / %d out tokens", v.InputTokens, v.OutputTokens)
			case sdkmodel.FindingReport:
				applyLiveFinding(live, v, at)
				kind, source, title = tlFinding, v.Kind, v.Title
			}
			repo, err := sc.Ext(liveKind)
			if err != nil {
				return err
			}
			live, err = repo.Update(ctx, live)
			if err != nil {
				return err
			}
			if err := m.appendTimelineScoped(ctx, sc, live.String(colSessionRef), live, scope, at, kind, tool, resource, mode, source, title); err != nil {
				return err
			}
			runs, err := sc.Ext(runKind)
			if err != nil {
				return err
			}
			before := run.String(colLastActivityAt)
			run[colLastActivityAt] = model.NewTimestamp(at).String()
			conservaElSelloMasNuevo(run, before)
			run[colRunLiveRef] = live.String(model.ColID)
			if _, err := runs.Update(ctx, run); err != nil {
				return err
			}
			snap = m.snapshot(live, tenant)
			return nil
		})
		if err != nil {
			return err
		}
		m.broker.publish(snap)
		// Only the successful bridge commit grants this marker. The registered
		// host validates its publisher; remote/collector ingestion cannot stamp it.
		e.SessionProjection = true
		return m.publishObservation(ctx, e)
	})
}
