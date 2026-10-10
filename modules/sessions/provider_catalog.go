// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// ReadProviderModels reuses the sealed provider probe without changing the
// provider record or its operator-requested test receipt. The models module owns
// automatic availability observations. A result belongs to the exact credential
// and endpoint tested; a concurrent rotation/revocation invalidates it.
func (m *Module) ReadProviderModels(ctx context.Context, tenant model.TenantID, expected ProviderRecord) ([]string, error) {
	if m.rt.ProviderProbe == nil {
		return nil, ErrNoProviderProbe
	}
	rec, err := m.GetProviderRecord(ctx, tenant, expected.Ref)
	if err != nil {
		return nil, err
	}
	if rec.State != ProviderRecordActive {
		return nil, ErrProviderRecordRevoked
	}
	if rec.SecretRef != expected.SecretRef || rec.BaseURL != expected.BaseURL {
		return nil, ErrProviderRecordChanged
	}
	result, err := m.probeProviderRecord(ctx, tenant, rec)
	if err != nil {
		return nil, err
	}
	after, err := m.GetProviderRecord(ctx, tenant, expected.Ref)
	if err != nil {
		return nil, err
	}
	if after.State != ProviderRecordActive || after.SecretRef != rec.SecretRef || after.BaseURL != rec.BaseURL {
		return nil, ErrProviderRecordChanged
	}
	return result.Models, nil
}

// OwnToolLoginHomes returns the product's tenant-specific login homes, without
// creating them, opening a credential file or consulting the engine user's home.
func (m *Module) OwnToolLoginHomes(tenant model.TenantID, driver string) (string, string, bool) {
	if m.toolLoginsRoot == "" {
		return "", "", false
	}
	return ToolLoginHome(m.toolLoginsRoot, tenant, driver)
}

// ModelProfileHomes validates the local account-home identity before a discovery
// adapter inspects even its login/configuration file metadata.
func (m *Module) ModelProfileHomes(tenant model.TenantID, profile ProviderProfile) (ProviderHomeSnapshot, error) {
	return m.snapshotForLaunch(tenant, profile, m.ExecutionEnvironmentRef())
}
