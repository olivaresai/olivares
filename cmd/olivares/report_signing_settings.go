// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/modules/reporting"
)

func (p *productSettings) ReportingSigning(ctx context.Context) (*reporting.SigningState, error) {
	doc, _, err := p.load(ctx)
	return doc.ReportingSigning, err
}
func (p *productSettings) SaveReportingSigning(ctx context.Context, actor auth.Principal, state reporting.SigningState) error {
	if (state.KeyID == "") != (state.SecretRef == "") || (state.Enabled && state.SecretRef == "") {
		return errors.New("report signing needs a stored key reference")
	}
	// The native system-admin route admits the actor. The common document update
	// keeps activation and module settings and records this change on the audit chain.
	return p.update(ctx, actor, "reporting.signing", map[string]any{"enabled": state.Enabled, "key_id": state.KeyID}, func(doc *productSettingsDoc) { doc.ReportingSigning = &state })
}
