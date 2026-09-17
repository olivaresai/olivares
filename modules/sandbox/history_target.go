// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandbox

import (
	"context"
	"errors"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

// ScopedHistorySource is additive: older adapters serve explicitly legacy IDs;
// they cannot accidentally interpret a live_ref in that namespace.
type ScopedHistorySource interface {
	TimelineByLiveRef(context.Context, model.TenantID, string) ([]ReplayStep, error)
}

func historyTarget(sessionRef, liveRef string) (string, string, error) {
	sessionRef, liveRef = strings.TrimSpace(sessionRef), strings.TrimSpace(liveRef)
	if (sessionRef == "") == (liveRef == "") {
		return "", "", errors.New("select exactly one session_ref or live_ref")
	}
	if liveRef != "" {
		id, err := model.ParseID(liveRef)
		if err != nil || id.IsZero() {
			return "", "", errors.New("invalid live_ref")
		}
		return "", id.String(), nil
	}
	if len(sessionRef) > maxRefLen {
		return "", "", errors.New("session_ref is too long")
	}
	return sessionRef, "", nil
}

func (m *Module) historyTimeline(ctx context.Context, tenant model.TenantID, sessionRef, liveRef string) ([]ReplayStep, error) {
	if liveRef == "" {
		return m.history.Timeline(ctx, tenant, sessionRef)
	}
	source, ok := m.history.(ScopedHistorySource)
	if !ok {
		return nil, errors.New("scoped session history is unavailable")
	}
	return source.TimelineByLiveRef(ctx, tenant, liveRef)
}
