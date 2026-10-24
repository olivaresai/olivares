// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package codexmanagedconfig

import (
	"time"

	"github.com/olivaresai/olivares/connectors/internal/filedrift"
	"github.com/olivaresai/olivares/sdk/model"
)

// filedrift.go compares a published Codex file with the host's key by key. VerifyDriftTOML
// (authoring.go) compares the typed policy the connector models, with a severity per
// constraint; a key the model does not hold (a granular approval_policy, a new Codex key,
// or a file that sets only such keys) never reaches it, so a host that dropped or changed
// it would read as clean. This comparison has no model to fall behind: every published key
// must be set to the same value on the host.

var allowlistArrays = []string{
	"allowed_approval_policies", "allowed_sandbox_modes", "allowed_web_search_modes", "allowed_approvals_reviewers",
}

// VerifyRequirementsFileTOML compares a published requirements.toml with the host's. Its
// [mcp_servers] table is the allowlist, so a server the host adds is drift too.
func VerifyRequirementsFileTOML(scope string, authored, observed []byte, at time.Time) ([]model.FindingReport, error) {
	return filedrift.TOML(filedrift.Spec{Origin: originManagedConfig, Tool: "Codex",
		ExactTables: []string{"mcp_servers"}, SetArrays: allowlistArrays}, scope, authored, observed, at)
}

// VerifyManagedConfigFileTOML compares a published managed_config.toml with the host's. The
// file holds defaults, so keys the host adds are its own.
func VerifyManagedConfigFileTOML(scope string, authored, observed []byte, at time.Time) ([]model.FindingReport, error) {
	return filedrift.TOML(filedrift.Spec{Origin: originManagedConfig, Tool: "Codex"}, scope, authored, observed, at)
}
