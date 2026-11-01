// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// ErrRemoteNodeUnavailable identifies a lost remote process without accepting a node's error text.
var ErrRemoteNodeUnavailable = errors.New("sessions: remote node unavailable")

// ErrRunNodeUnavailable is a placement verdict, distinct from an unavailable backing store.
var ErrRunNodeUnavailable = errors.New("sessions: selected node is not available")

// checkRunPlacement is the edition and live-node gate shared by create and resume.
// It runs before any local workspace or credential side effect. In particular, an
// unconfigured Community engine never treats a remote request as a local launch.
func (m *Module) checkRunPlacement(ctx context.Context, tenant model.TenantID, requested string, principal auth.Principal) (string, error) {
	if requested == "" {
		return "", nil
	}
	node := strings.TrimSpace(requested)
	if node == "" || len(node) > 128 || strings.ContainsAny(node, "\x00\r\n") {
		return "", &runErr{http.StatusBadRequest, "run_on must name one enrolled node"}
	}
	if m.rt.RunPlacement == nil {
		return "", &codedRunErr{&runErr{http.StatusNotImplemented, "Running a session on a remote node requires Olivares Business."}, "business_required"}
	}
	if err := m.rt.RunPlacement.Check(ctx, tenant, node, principal); err != nil {
		if errors.Is(err, ErrRunNodeUnavailable) {
			return "", &codedRunErr{conflictErr("The selected node is not enrolled and online."), "node_unavailable"}
		}
		return "", &runErr{http.StatusServiceUnavailable, "The selected node could not be checked; retry the launch."}
	}
	return node, nil
}

// refuseRemoteInputs runs before launch side effects, on the effective template/profile terms.
func (m *Module) refuseRemoteInputs(p CreateRunParams) error {
	if p.RunOn == "" {
		return nil
	}
	if p.Isolation != "" && p.Isolation != IsolationNative || p.Transport != "" && p.Transport != TransportStreamJSON || p.ProviderHome != nil && p.ProviderHome.Driver != "" && p.ProviderHome.Driver != providerDriverClaude {
		return conflictErr("remote sessions currently require native Claude stream-json; run locally for another tool or execution mode")
	}
	if p.ProviderHome != nil && p.ProviderHome.AuthSource == AuthSourceAccountHome {
		return conflictErr("remote sessions cannot use a login saved in this engine's account home; select a managed provider key or run locally")
	}
	// Vault secrets stay on the central engine: a remote run would carry their values to the node.
	if p.GitRead != "" || len(p.Credentials) != 0 || len(p.SecretEnv) != 0 || p.Worktree || p.WorktreeBranch != "" || p.WorktreeFrom != "" || p.WorkspaceRef != "" || p.RepositoryURL != "" || p.WorkspaceDir != "" && !p.WorkspaceDirOwned || len(p.profileRegistries) != 0 || m.rt.SessionDecoys != nil {
		return conflictErr("remote sessions do not support central workspace, repository, registry, managed credential, vault secret or decoy inputs")
	}
	return nil
}

func remoteLaunchSpecRefusal(spec LaunchSpec) error {
	if spec.RunOn != "" && (spec.ScratchHome != nil || spec.OnStart != nil || len(spec.ConfinementFiles) != 0) {
		return conflictErr("remote sessions cannot use central-only launch handles")
	}
	if spec.RunOn != "" && spec.NetworkPolicy == nil {
		controlled := spec.SessionMCPURL != "" || spec.HelperPath != ""
		for _, arg := range spec.Args {
			controlled = controlled || arg == "--mcp-config" || strings.HasPrefix(arg, "mcp_servers.olivares.url=")
		}
		for _, env := range spec.Env {
			controlled = controlled || env.Value != "" && slices.Contains(hookControlEnv, env.Name)
		}
		if controlled {
			return conflictErr("remote sessions with engine approvals or MCP need a relayed network policy")
		}
	}
	return nil
}
