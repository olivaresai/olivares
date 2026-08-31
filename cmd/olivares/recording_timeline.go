// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/recording"
	"github.com/olivaresai/olivares/modules/sessions"
)

// sessionsTimelineAdapter bridges the sessions module's TimelineByCredential
// seam into the recording module's TimelineResolver port.
// It is the production form of the inter-module bridge (the evalsScorerAdapter
// mold): neither module imports the other; the composition root owns the glue.
type sessionsTimelineAdapter struct {
	sessions *sessions.Module
}

var _ recording.TimelineResolver = (*sessionsTimelineAdapter)(nil)

func (a *sessionsTimelineAdapter) ResolveTimeline(ctx context.Context, tenant model.TenantID, cred string, limit int, cursor string) (string, []recording.TimelineEntry, string, bool, error) {
	ref, tl, nextCursor, hasMore, err := a.sessions.TimelineByCredential(ctx, tenant, cred, limit, cursor)
	if err != nil || len(tl) == 0 {
		return ref, nil, nextCursor, hasMore, err
	}
	out := make([]recording.TimelineEntry, len(tl))
	for i, ev := range tl {
		out[i] = recording.TimelineEntry{
			At:          ev.At,
			Kind:        ev.Kind,
			ToolRef:     ev.ToolRef,
			ResourceRef: ev.ResourceRef,
			Mode:        ev.Mode,
			Source:      ev.Source,
			Title:       ev.Title,
		}
	}
	return ref, out, nextCursor, hasMore, nil
}
