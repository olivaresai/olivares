// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap

import (
	"context"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Expire only the launch grant, preserving independent observations and policy.
func (m *Module) expireSessionReach(ctx context.Context, tenantRef, launch string) error {
	tenant, ok := tenantOf(tenantRef)
	if !ok || launch == "" {
		return nil
	}
	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		session, found, ambiguous, err := sessionByExternalID(ctx, sc, launch)
		if err != nil || !found || ambiguous || session.State == model.SessionRunning {
			return err
		}
		// Deletion changes pagination, so collect this session's edges before writing.
		var edges []model.AccessEdge
		q := model.Query{Filters: []model.Filter{eq("session_id", session.ID.String())}, Limit: 100}
		for {
			batch, page, err := sc.AccessEdges().List(ctx, q)
			if err != nil {
				return err
			}
			edges = append(edges, batch...)
			if !page.HasMore {
				break
			}
			q.Cursor = page.Cursor
		}
		for _, edge := range edges {
			sources := sourceSet(edge.Metadata)
			sources[string(edge.SignalSource)] = struct{}{}
			if _, ok := sources[string(sdkmodel.SignalSessionLaunch)]; !ok {
				continue
			}
			delete(sources, string(sdkmodel.SignalSessionLaunch))
			if len(sources) == 0 {
				if err := sc.AccessEdges().Delete(ctx, edge.ID); err != nil {
					return err
				}
				continue
			}
			edge.Metadata["signal_sources"] = joinSet(sources)
			edge.Observed, edge.Permitted = false, false
			for _, source := range strings.Split(joinSet(sources), ",") {
				observed, permitted := deriveObservedPermitted(sdkmodel.SignalSource(source))
				edge.Observed = edge.Observed || observed
				edge.Permitted = edge.Permitted || permitted
				if edge.SignalSource == sdkmodel.SignalSessionLaunch {
					edge.SignalSource = sdkmodel.SignalSource(source)
				}
			}
			if edge.OccurrenceCount > 1 {
				edge.OccurrenceCount--
			}
			if _, err := sc.AccessEdges().Update(ctx, edge); err != nil {
				return err
			}
		}
		return nil
	})
}
