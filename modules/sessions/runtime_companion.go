// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// RuntimeCompanion is the launch authority of an authenticated session child.
// Runner is the agent's runner, including its process confinement. Context ends
// when that exact launch ends; a resumed session cannot inherit its children.
type RuntimeCompanion struct {
	LaunchSpec
	Runner   Runner
	Context  context.Context
	LaunchID model.ID
}

func companionSpec(spec LaunchSpec) LaunchSpec {
	out := spec // preserve runner extensions such as confinement policy
	out.Program, out.Args, out.Env, out.EnvAllow = "", nil, nil, nil
	for _, entry := range spec.Env {
		switch entry.Name {
		case "HOME", "TMPDIR", "TMP", "TEMP":
			out.Env = append(out.Env, entry)
		}
	}
	return out
}

// RuntimeCompanion validates server-authenticated purpose and the exact live
// credential, Claim and launch generation before allowing a companion process.
// It is an in-process seam, never a caller-supplied launch specification.
func (m *Module) RuntimeCompanion(ctx context.Context, tenant model.TenantID, p auth.Principal) (RuntimeCompanion, error) {
	deny := func() (RuntimeCompanion, error) {
		return RuntimeCompanion{}, broken(http.StatusForbidden, "session runtime unavailable")
	}
	legacyPurpose := p.IsWorkSessionCredential() || p.IsOrchestrationSessionCredential()
	_, ordinary := p.Ref()
	_, confined := p.ConfinedWorkspaceIn(tenant)
	if (!legacyPurpose && (ordinary || !confined)) || len(p.Tenants()) != 1 || p.Tenants()[0] != tenant {
		return deny()
	}
	release, err := m.rt.lockRunContext(ctx, liveKey(tenant, p.SessionRunRef))
	if err != nil {
		return RuntimeCompanion{}, err
	}
	defer release()
	live, ok := m.rt.getLive(tenant, p.SessionRunRef)
	if !ok {
		return deny()
	}
	live.mu.Lock()
	valid := !live.finalized && !live.stopRequested && (!legacyPurpose || live.workCredentialID == p.CredID) && live.claim.SID == p.SessionIdentity && live.claim.Fence == p.SessionFence && live.agentRef == p.AgentIdentity
	live.mu.Unlock()
	if !valid || live.context == nil || live.context.Err() != nil {
		return deny()
	}
	if err := m.preflightStop(ctx, tenant, StopDims{RunRef: p.SessionRunRef, AgentRef: live.agentRef}); err != nil {
		return deny()
	}
	if err := m.assertRunAuthority(ctx, live); err != nil {
		return deny()
	}
	return RuntimeCompanion{LaunchSpec: companionSpec(live.companion), Runner: m.rt.runner, Context: live.context, LaunchID: live.launchID}, nil
}
