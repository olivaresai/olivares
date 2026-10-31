// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/modules/sessions"
)

// The part of the deployment's sealed secret store that governed connections may name is
// sessions.ConnectionSecretPrefix: `olivares secrets put --name connection/<name>`,
// referenced as store:connection/<name>. As with git-host/ (gitpublishcustody.go), only a
// superadmin writes it and no tenant route reaches it.

// connectionSecretReader is the port the sessions module opens a connection's credential
// through. A module holds no secret store (modules/sessions/provider_secret.go), so it is
// given this one closed door: it opens connection/<name> entries of the deployment scope
// and answers "not found" for every other name without asking the store. A secret under
// another namespace (the MCP servers', the git hosts', a personal vault item) is therefore
// unreachable through a connection, whatever a connection record says.
func connectionSecretReader(secrets gitpublishSecretReader) func(context.Context, string) ([]byte, error) {
	return func(ctx context.Context, name string) ([]byte, error) {
		rest, ok := strings.CutPrefix(name, sessions.ConnectionSecretPrefix)
		if !ok || secrets == nil || name != strings.TrimSpace(name) || auth.ValidateSecretName(name) != "" {
			return nil, auth.ErrSecretNotFound
		}
		for _, segment := range strings.Split(rest, "/") {
			if segment == "" || segment == "." || segment == ".." {
				return nil, auth.ErrSecretNotFound
			}
		}
		return secrets.Resolve(ctx, auth.GlobalSecretScope, name)
	}
}
