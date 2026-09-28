// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// editionCensusContributions is what this edition contributes to the retirement
// census beyond the Community composition (communityCensusContribution). The
// community artifact links no edition module and keeps no table outside the
// registry, so it contributes nothing. The enterprise build declares its own in
// its overlay: the declared modules it expects and the tables it keeps outside
// the registry, with their column declarations. There is no default, so a build
// that omits it does not compile, and a contribution naming a module the build
// does not link leaves the composition unready.
func editionCensusContributions() []store.CompositionContribution { return nil }

// circuitBreakerDeclarations declares the breaker rule table's text columns for
// the community artifact. The table is registered in every edition, but this
// artifact links neither the engine that writes rules nor any reader of their
// configuration: the gate reads only the breaker state. The enterprise build,
// whose engine writes and reads rules, declares these columns from that engine
// in its overlay, so the public tree never declares them on its behalf.
func circuitBreakerDeclarations() circuitBreakerRuleDecls {
	return circuitBreakerRuleDecls{
		Config: model.None("breaker configuration no reader in this edition reads; the gate reads only the breaker state: cmd/olivares/circuitbreakergate.go:23, cmd/olivares/circuitbreaker.go:21"),
		Tier:   model.None("an agent risk tier from the tier vocabulary: modules/governance/agentrisk.go:526"),
		// No writer of this column is in this edition, so its spelling of the
		// author is not fixed here: it is matched against every alias.
		Author: model.Scan(model.ClassEvidence),
	}
}
