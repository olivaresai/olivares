// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// Managed credentials for sessions are a Business capability (modules/sessions
// session_credentials.go). The edition's broker is built by thisEdition.sessionCredentialBroker;
// a Community build leaves it nil and a launch that names a credential answers 501.

// credentialSecretPrefix is the tenant vault namespace of managed credentials. It is not
// the session environment namespace (env/): secret_env cannot name a credential, so the
// only way a session reaches one is through the broker's helper.
const credentialSecretPrefix = "credential/"

// sessionCredentialDeps is the one thing the broker may ask the engine for: the sealed
// document of one named credential of the launch's own tenant.
type sessionCredentialDeps struct {
	// DataDir is the engine's absolute data directory, where a run's helpers keep their
	// sockets and files (mode 0700) for the length of the run.
	DataDir string
	// Open returns the credential document sealed as credential/<name> for tenant. The
	// caller holds it for one launch and never persists, logs or returns it.
	Open func(ctx context.Context, tenant model.TenantID, name string) ([]byte, error)
}

// newSessionCredentialDeps binds Open to the tenant vault and to the credential/
// namespace alone: the broker cannot read a session secret, an MCP credential or a
// person's vault item through it.
func newSessionCredentialDeps(dataDir string, vault sessions.ProviderSecretVault) sessionCredentialDeps {
	return sessionCredentialDeps{
		DataDir: dataDir,
		Open: func(ctx context.Context, tenant model.TenantID, name string) ([]byte, error) {
			if vault == nil {
				return nil, fmt.Errorf("no sealed vault is wired on this node")
			}
			if strings.ContainsAny(name, "/\\") || name == "" || name == "." || name == ".." {
				return nil, fmt.Errorf("%q is not a credential name", name)
			}
			return vault.Open(ctx, tenant, credentialSecretPrefix+name)
		},
	}
}
