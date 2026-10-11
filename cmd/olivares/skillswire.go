// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"

	"github.com/olivaresai/olivares/core/egress"
	"github.com/olivaresai/olivares/core/model"
)

// Public HTTPS imports use the core's absent-policy public-address rule. The
// importer still checks DNS and pins every actual dial; private/reserved
// addresses cannot be lifted by a source URL or a catalog publisher.
type publicSkillsGitPolicy struct{}

func (publicSkillsGitPolicy) EgressPolicy(context.Context, model.TenantID) (egress.Policy, error) {
	return egress.Policy{}, nil
}
