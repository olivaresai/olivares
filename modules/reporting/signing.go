// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"context"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// SigningState is product state, not key material. A nil state leaves legacy
// startup configuration in effect; an explicit disabled state overrides it.
type SigningState struct {
	Enabled   bool   `json:"enabled"`
	KeyID     string `json:"key_id"`
	SecretRef string `json:"secret_ref"`
}

// SigningStatus contains only public information about the engine's active key.
type SigningStatus struct {
	Enabled   bool   `json:"enabled"`
	Ready     bool   `json:"ready"`
	Reason    string `json:"reason"`
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
	Source    string `json:"source"`
}

type SigningSettingsStore interface {
	ReportingSigning(context.Context) (*SigningState, error)
	SaveReportingSigning(context.Context, auth.Principal, SigningState) error
}

// SigningSecrets is satisfied by the existing encrypted auth.SecretStore.
type SigningSecrets interface {
	Put(context.Context, auth.Principal, model.TenantID, string, string, string) (auth.SecretView, error)
	Resolve(context.Context, model.TenantID, string) ([]byte, error)
}

// SigningManager is an optional capability of the bound report source. Its
// management calls are exposed only through the native system-admin route gate.
type SigningManager interface {
	ReportingSigning(context.Context) (SigningStatus, error)
	SetReportingSigning(context.Context, auth.Principal, bool) (SigningStatus, error)
}

// BindBundleSigning forwards the engine's existing late-bound settings and
// encrypted secret store to the report source; it creates no second key store.
func (m *Module) BindBundleSigning(ctx context.Context, settings SigningSettingsStore, secrets SigningSecrets) error {
	if source, ok := m.enterprise.(interface {
		UseSigningStore(context.Context, SigningSettingsStore, SigningSecrets) error
	}); ok {
		return source.UseSigningStore(ctx, settings, secrets)
	}
	return nil
}
