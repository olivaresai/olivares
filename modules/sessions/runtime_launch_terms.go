// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"strings"

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
}

// askCallerQuestions settles MayRunUnrestricted and MayUseSecretEnv for the terms
// as they are now, after the template and the profile policy have been applied:
// "full" asks the first (refuseUnrestrictedFor), secret_env and git_read ask the
// second (refuseSecretEnvFor, refuseGitReadFor). Any other launch asks neither.
func (p *CreateRunParams) askCallerQuestions() {
	if p.asks.runUnrestricted != nil && strings.TrimSpace(p.PermissionMode) == permModeBypass {
		p.MayRunUnrestricted = p.asks.runUnrestricted()
	}
	if p.asks.secretEnv != nil && (len(p.SecretEnv) > 0 || p.GitRead != "") {
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
	tpl, conflicts, err := m.applyLaunchTemplate(ctx, tenant, p)
	if err != nil {
		return templateDTO{}, nil, err
	}
	if p.resuming {
		if p.ProviderHome != nil {
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
			return templateDTO{}, nil, err
		}
	}
	return tpl, conflicts, nil
}
