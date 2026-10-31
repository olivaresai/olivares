// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// callerAsks are the two permission questions about the authenticated caller that
// only some launches need: may they run "full" (run administration) and may they
// give vault secrets (tenant administration). The authorizer records every question
// it answers, so each is asked only once the launch's effective terms make its
// answer decide something (askCallerQuestions). A nil ask leaves the answer as the
// params set it, a no for an in-process launch, which has no caller to ask.
type callerAsks struct {
	runUnrestricted, secretEnv func() bool
	// profileAccess asks whether the caller may use a tool and one profile of it
	// (provider_profile_access.go). A launch with no caller to ask, a recovery, leaves it nil.
	profileAccess func(profileRef, driver string) error
	// standing asks the departments of the caller (department_standards.go). A launch with no
	// caller to ask, or a build with no assignments wired, leaves it nil: no standards.
	standing func() (launchStanding, error)
	// workspaceRights asks whether the caller holds, on the registered folder a launch or
	// resume names, the rights its session will exercise there (workspace_api.go). A launch with
	// no caller to ask leaves it nil.
	workspaceRights func(ref string) error
}

// refuseWorkspace is the folder question for the workspace a launch or resume names.
func (a callerAsks) refuseWorkspace(ref string) error {
	if a.workspaceRights == nil || ref == "" {
		return nil
	}
	return a.workspaceRights(ref)
}

// askCallerQuestions settles MayRunUnrestricted and MayUseSecretEnv for the terms
// as they are now, after the template and the profile policy have been applied:
// "full" and "auto" ask the first (refuseUnrestrictedFor), secret_env, git_read and credentials
// ask the second (refuseSecretEnvFor, refuseGitReadFor, refuseCredentialsFor). Any
// other launch asks neither.
func (p *CreateRunParams) askCallerQuestions() {
	if p.asks.runUnrestricted != nil && needsRunAdministration(p.PermissionMode) {
		p.MayRunUnrestricted = p.asks.runUnrestricted()
	}
	if p.asks.secretEnv != nil && (len(p.SecretEnv) > 0 || p.GitRead != "" || len(p.Credentials) > 0) {
		p.MayUseSecretEnv = p.asks.secretEnv()
	}
}

// prepareLaunchTerms applies the current template before the profile's session
// policy on create, work launches, queued launches and resume. Template terms are
// approval-bound; a profile mode only defaults a launch that names neither a mode
// nor a template. Both sources must be applied before the governance gates.
//
// Resume supplies the policy from its revalidated stored profile. A new launch
// resolves its selected profile here, before any claim, credential or persistence.
func (m *Module) prepareLaunchTerms(ctx context.Context, tenant model.TenantID, p *CreateRunParams, resumePolicy sessionPolicy) (templateDTO, []mergeConflict, error) {
	// The department's default template governs a launch that names none, so it is named
	// before the template is applied. Its standards are not read when nobody asks.
	var standing *launchStanding
	if !p.resuming {
		var err error
		if standing, err = p.standingOfLaunch(); err != nil {
			return templateDTO{}, nil, m.errStandardsUnreadable(err)
		}
	}
	defaulted, err := m.applyDepartmentDefaultTemplate(ctx, tenant, p, standing)
	if err != nil {
		return templateDTO{}, nil, err
	}
	tpl, conflicts, err := m.applyLaunchTemplate(ctx, tenant, p)
	if err != nil {
		if defaulted {
			err = m.errDefaultTemplate(standing, err)
		}
		return templateDTO{}, nil, err
	}
	if p.resuming {
		if p.ProviderHome != nil {
			// A resume runs under the profile it was launched under: a withdrawn assignment
			// stops it too. (A new launch is asked in resolveLaunchProfileInto.)
			if p.asks.profileAccess != nil {
				if err := p.asks.profileAccess(p.ProviderHome.ProfileID, p.ProviderHome.Driver); err != nil {
					return templateDTO{}, nil, err
				}
			}
			if err := applySessionPolicy(p, p.ProviderHome.Driver, resumePolicy); err != nil {
				return templateDTO{}, nil, err
			}
		}
	} else {
		// Default only after the template merge: an empty mode is distinguishable
		// from a chosen mode, and must not produce a spurious merge conflict.
		if err := validateCreate(p); err != nil {
			return templateDTO{}, nil, err
		}
		if err := m.refuseUnsupportedIsolation(ctx, p.Isolation); err != nil {
			return templateDTO{}, nil, err
		}
		if err := m.resolveLaunchProfileInto(ctx, tenant, p); err != nil {
			return templateDTO{}, nil, m.attributedToDefault(defaulted, standing, err)
		}
	}
	if err := m.refuseRemoteInputs(*p); err != nil {
		return templateDTO{}, nil, err
	}
	if err := m.validateLaunchSpeed(ctx, tenant, p); err != nil {
		return templateDTO{}, nil, err
	}
	// The department's instructions go ahead of the launch's own, once the profile has said
	// which tool this is. A resume and a queued launch read them again, as they do the template.
	if err := m.applyDepartmentInstructions(ctx, tenant, p, standing); err != nil {
		return templateDTO{}, nil, err
	}
	// Every launch, a resume and a queued one too, hands Claude Code the exact id.
	resolveClaudeModelAlias(p)
	if p.TimeoutSeconds > 0 && p.runDeadline.IsZero() {
		p.runDeadline = m.now().Add(time.Duration(p.TimeoutSeconds * float64(time.Second)))
	}
	if !p.runDeadline.IsZero() {
		remaining := p.runDeadline.Sub(m.now())
		if remaining <= 0 {
			return templateDTO{}, nil, conflictErr("run timeout reached")
		}
		if p.MaxDuration <= 0 || remaining < p.MaxDuration {
			p.MaxDuration = remaining
		}
	}
	if p.CostCapMicroUSD > 0 && p.costSpent >= p.CostCapMicroUSD {
		return templateDTO{}, nil, conflictErr("run cost cap reached")
	}
	return tpl, conflicts, nil
}
