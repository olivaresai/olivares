// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"path/filepath"
	"slices"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Project the final launch terms, never the requested permission label or an
// account's presumed provider. No credential or environment value is retained.
func (m *Module) publishLaunchReach(ctx context.Context, lr *liveRun, spec LaunchSpec, tool string, ws *resolvedWorkspace) {
	if m.publishObservation == nil {
		return
	}
	folder := spec.Dir
	if spec.Workspace != nil {
		folder = spec.Workspace.HostPath
	}
	publish := func(kind, ref string, mode sdkmodel.AccessMode) {
		if ref == "" {
			return
		}
		edge := sdkmodel.EdgeObservation{OriginKind: "session", OriginRef: lr.launchID.String(),
			ResourceKind: kind, ResourceRef: ref, ToolRef: tool, Mode: mode,
			Source: sdkmodel.SignalSessionLaunch, Confidence: sdkmodel.ConfidenceAttributed, ObservedAt: m.now()}
		if kind == "resource" {
			edge.Labels = map[string]string{sdkmodel.LabelProjectFolder: filepath.Base(folder)}
		}
		e := event.FromObservation(lr.tenant.String(), Name, edge)
		e.SessionProjection = true
		if err := m.publishObservation(ctx, e); err != nil {
			m.warnf("session reach publication failed", "err", redactErr(err))
		}
	}
	// The final policy, including sealed/no-delete grants, determines the mode.
	if spec.Dir != "" {
		mode := sdkmodel.AccessMode("")
		if spec.Workspace != nil {
			mode = sdkmodel.ModeReadWrite
			if spec.Workspace.ReadOnly {
				mode = sdkmodel.ModeRead
			}
		} else if spec.Confinement != nil {
			switch {
			case slices.Contains(spec.Confinement.Sealed, spec.Dir):
				mode = sdkmodel.ModeRead
			case slices.Contains(spec.Confinement.ReadWrite, spec.Dir), slices.Contains(spec.ConfinementNoDelete, spec.Dir):
				mode = sdkmodel.ModeReadWrite
			case slices.Contains(spec.Confinement.ReadOnly, spec.Dir):
				mode = sdkmodel.ModeRead
			}
		}
		if mode != "" && ws != nil {
			relative, err := filepath.Rel(ws.rootReal, folder)
			if err != nil {
				m.warnf("session folder reach unavailable", "err", redactErr(err))
			} else {
				ref := "workspace:" + ws.ref
				if relative != "." {
					ref += "/" + filepath.ToSlash(relative)
				}
				publish("resource", ref, mode)
			}
		}
	}
	// Confinement states a folder's mode; it says nothing about what a server's
	// tools or the provider's endpoint do, so those edges stay unknown.
	for _, server := range spec.SessionMCPReach {
		publish("mcp_server", server, sdkmodel.ModeUnknown)
	}
	publish("provider", spec.BoundProvider.Endpoint, sdkmodel.ModeUnknown)
}

func (m *Module) publishEndedReach(ctx context.Context, tenant model.TenantID, rec model.Record) {
	if m.publishObservation == nil || rec.String(colRunCoreSessionID) == "" {
		return
	}
	var launch string
	err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
		session, err := sc.Sessions().Get(ctx, model.ID(rec.String(colRunCoreSessionID)))
		if err == nil {
			launch = session.ExternalID
		}
		return err
	})
	if err == nil && launch != "" {
		err = m.publishObservation(ctx, event.Event{Type: event.TypeSessionReachExpired, Tenant: tenant.String(), Source: Name,
			SessionProjection: true, Payload: sdkmodel.EdgeObservation{OriginKind: "session", OriginRef: launch, Source: sdkmodel.SignalSessionLaunch}})
	}
	if err != nil {
		m.warnf("session reach expiry failed", "err", redactErr(err))
	}
}
