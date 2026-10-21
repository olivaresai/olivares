// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

func (p *productSettings) SessionIdleSettings(ctx context.Context) (api.SessionIdleSettings, error) {
	doc, _, err := p.load(ctx)
	if err != nil {
		return api.SessionIdleSettings{}, sessionIdleSettingsError{err}
	}
	if doc.SessionIdle == nil {
		return api.SessionIdleSettings{}, nil
	}
	if err := doc.SessionIdle.Validate(); err != nil {
		return api.SessionIdleSettings{}, sessionIdleSettingsError{err}
	}
	return *doc.SessionIdle, nil
}

func (p *productSettings) SaveSessionIdleSettings(ctx context.Context, actor auth.Principal, choice api.SessionIdleSettings) (api.SessionIdleSettings, error) {
	if err := choice.Validate(); err != nil {
		return api.SessionIdleSettings{}, err
	}
	err := p.updateWithAudit(ctx, actor, "deployment.settings.session_idle", map[string]any{"idle_stop_minutes": choice.IdleStopMinutes}, func(doc *productSettingsDoc) { doc.SessionIdle = &choice }, true)
	if err != nil {
		return api.SessionIdleSettings{}, sessionIdleSettingsError{err}
	}
	return choice, nil
}

func (p *productSettings) sessionIdleWindow(ctx context.Context) (time.Duration, error) {
	choice, err := p.SessionIdleSettings(ctx)
	return time.Duration(choice.IdleStopMinutes) * time.Minute, err
}

// Retain classification without exposing storage or audit error text.
type sessionIdleSettingsError struct{ cause error }

func (e sessionIdleSettingsError) Error() string {
	return api.ErrSessionIdleSettingsUnavailable.Error()
}
func (e sessionIdleSettingsError) Unwrap() []error {
	return []error{api.ErrSessionIdleSettingsUnavailable, e.cause}
}
