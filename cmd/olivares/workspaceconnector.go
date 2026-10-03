// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sourcescope"
)

// Boot binds the vault before serving. The module calls this adapter outside
// its tenant transaction, since the vault owns separate auth transactions.
type workspaceConnectorSecrets struct {
	vault *auth.SecretStore
}

var _ sourcescope.WorkspaceConnectorSealer = (*workspaceConnectorSecrets)(nil)

func workspaceSecretPrefix(workspace, connector string) string {
	// Independent hashes prevent slashes, spaces and Unicode names from aliasing
	// a sibling namespace or exceeding the vault's secret-name limit.
	return fmt.Sprintf("ws/%x/%x/", sha256.Sum256([]byte(workspace)), sha256.Sum256([]byte(connector)))
}

func (s *workspaceConnectorSecrets) SealWorkspaceSecret(ctx context.Context, tenant model.TenantID, workspace, connector, field, value string, actor auth.Principal) (string, error) {
	if s.vault == nil || !s.vault.SealerWired() {
		return "", auth.ErrNoSecretSealer
	}
	if tenant.IsZero() || tenant == model.SystemTenantID {
		return "", store.ErrNoTenant
	}
	name := fmt.Sprintf("%s%x/%s", workspaceSecretPrefix(workspace, connector), sha256.Sum256([]byte(field)), model.NewID())
	if _, err := s.vault.Put(ctx, actor, tenant, name, value, "Workspace connector secret"); err != nil {
		// Put can fail on its metadata read after committing. This fresh name
		// has not been published to a connector, so compensating is safe even
		// when the vault commit acknowledgement was lost.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		derr := s.vault.Delete(cleanup, actor, tenant, name)
		if errors.Is(derr, auth.ErrSecretNotFound) {
			derr = nil
		}
		return "", errors.Join(err, derr)
	}
	return "store:" + name, nil
}

func (s *workspaceConnectorSecrets) DeleteWorkspaceSecrets(ctx context.Context, tenant model.TenantID, workspace, connector string, refs map[string]string, actor auth.Principal) error {
	if s.vault == nil {
		return auth.ErrNoSecretSealer
	}
	if tenant.IsZero() || tenant == model.SystemTenantID {
		return store.ErrNoTenant
	}
	prefix := workspaceSecretPrefix(workspace, connector)
	for _, value := range refs {
		ref, ok := secret.ParseReference(value)
		if !ok || ref.Scheme != secret.SchemeStore || !strings.HasPrefix(ref.Locator, prefix) {
			continue
		}
		if err := s.vault.Delete(ctx, actor, tenant, ref.Locator); err != nil && !errors.Is(err, auth.ErrSecretNotFound) {
			return err
		}
	}
	return nil
}
