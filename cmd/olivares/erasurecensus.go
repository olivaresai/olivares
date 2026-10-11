// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/modulespec"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/knowledge"
)

// declaredModule is one module this composition declares to the census: the
// step that retires an account from a tenant in that module, and the counted
// columns the step accounts for. A declared module with no step is unavailable,
// never empty: it declares no reader, so the composition opens unready, and a
// retirement waits for it.
type declaredModule struct {
	name   string
	step   auth.RetirementStep
	covers []string
}

// retiringModule is a module that declares a retirement step and the counted
// columns it reads.
type retiringModule interface {
	RetirementStep() auth.RetirementStep
	RetirementCovers() []string
}

// holdGatedRetiringModule lets a content eraser use the declared compliance
// instance, including when the module profile leaves compliance dormant.
type holdGatedRetiringModule interface {
	RetirementStepWithHoldGate(knowledge.HoldGate) auth.RetirementStep
}

// authPartitionModule is the declared name of the auth partition, whose reader
// the store declares itself.
const authPartitionModule = store.AuthPartitionReader

// authPartitionStep is the auth partition's step. The offboard itself removes,
// in the transaction that records the exclusion, what lets the account act in
// the tenant: its membership, its rows in the tenant's groups, the tokens bound
// to the tenant (by owner or by act-as, with their exchanged children) and the
// sessions scoped to it (core/auth/tenant_authority.go). It leaves the counted
// rows that cannot act for the account there: a pending invitation redeems only
// for an account that is still a member of the invitation's tenant and joins it
// to nothing (core/auth/onboarding.go), an account offer has no writer yet, and
// a delegation handle is refused while the tenant excludes its account or source
// session (core/auth/delegation.go). Its passkeys and account-scope sessions are
// the account's own, not the tenant's. So the step finds nothing that acts in
// the tenant, and reports clean.
type authPartitionStep struct{}

// Module implements auth.RetirementStep.
func (authPartitionStep) Module() string { return authPartitionModule }

// RetireUser implements auth.RetirementStep.
func (authPartitionStep) RetireUser(context.Context, auth.RetirementRequest) (auth.RetirementOutcome, error) {
	return auth.RetirementOutcome{}, nil
}

// communityCensusModules are the modules the Community composition declares to
// the census, in the order their steps run: the modules whose rows grant
// authority, then those that hold obligations. The auth partition runs first; the
// store declares it. The list is the composition's own, not read from what a
// build links: a declared module a build lacks, or that gives no retirement
// step, stays declared and is reported missing.
var communityCensusModules = modulespec.RetirementModules()

// communityCensusContribution is the Community composition's contribution to the
// census: its declared modules, and no table outside the registry.
func communityCensusContribution() store.CompositionContribution {
	return store.CompositionContribution{Edition: "community", Modules: append([]string(nil), communityCensusModules...)}
}

// censusContributions returns every contribution this build declares: the
// Community composition's, then this edition's.
func censusContributions() []store.CompositionContribution {
	return append([]store.CompositionContribution{communityCensusContribution()}, thisEdition.censusContributions.get()...)
}

// declaredModules returns the modules the contributions declare, each with the
// retirement step and covered columns the built module set gives it, in the
// contributions' order, and then any other linked module that declares a step.
// A declared module the set lacks, or whose step is nil, is still returned, with
// no step: it is missing, never empty. The auth partition is added once the
// store has opened (withAuthPartition), because the store declares its columns.
func declaredModules(set moduleSet, contributions []store.CompositionContribution) []declaredModule {
	byName := map[string]declaredModule{}
	var linked []string
	for _, m := range set.all {
		rm, ok := m.(retiringModule)
		if !ok {
			continue
		}
		var step auth.RetirementStep
		if gated, ok := m.(holdGatedRetiringModule); ok {
			step = gated.RetirementStepWithHoldGate(complianceHoldGate{m: set.compliance})
		} else {
			step = rm.RetirementStep()
		}
		if step == nil {
			continue
		}
		name := step.Module()
		if _, dup := byName[name]; dup {
			continue
		}
		byName[name] = declaredModule{name: name, step: step, covers: rm.RetirementCovers()}
		linked = append(linked, name)
	}
	var out []declaredModule
	seen := map[string]bool{}
	for _, c := range contributions {
		for _, name := range c.Modules {
			if seen[name] {
				continue
			}
			seen[name] = true
			if d, ok := byName[name]; ok {
				out = append(out, d)
			} else {
				out = append(out, declaredModule{name: name})
			}
		}
	}
	for _, name := range linked {
		if !seen[name] {
			seen[name] = true
			out = append(out, byName[name])
		}
	}
	return out
}

// withAuthPartition puts the auth partition first among declared, covering the
// columns the opened store declared for it. A store that reports no readers
// declares no auth partition, and its census stays unready.
func withAuthPartition(st store.Store, declared []declaredModule) []declaredModule {
	readers, ok := st.(store.CompositionReaders)
	if !ok {
		return declared
	}
	covers := readers.CompositionReaders()[authPartitionModule]
	if len(covers) == 0 {
		return declared
	}
	partition := declaredModule{name: authPartitionModule, step: authPartitionStep{}, covers: covers}
	return append([]declaredModule{partition}, declared...)
}

// declareCompositionReaders declares every contribution, then every module's
// counted columns, to the registry before it closes. A registry without the
// capabilities declares nothing, and the store opens unready: the census fails
// closed.
func declareCompositionReaders(reg store.ExtensionRegistry, declared []declaredModule, contributions []store.CompositionContribution) error {
	if expected, ok := reg.(store.CompositionContributionRegistry); ok {
		for _, c := range contributions {
			if err := expected.DeclareCompositionContribution(c); err != nil {
				return fmt.Errorf("declare the %s census contribution: %w", c.Edition, err)
			}
		}
	}
	readers, ok := reg.(store.CompositionReaderRegistry)
	if !ok {
		return nil
	}
	for _, d := range declared {
		if len(d.covers) == 0 || d.name == authPartitionModule {
			continue
		}
		if err := readers.DeclareCompositionReader(d.name, d.covers); err != nil {
			return fmt.Errorf("declare the %s retirement reader: %w", d.name, err)
		}
	}
	return nil
}

// retirementSteps returns the declared modules' steps, in order.
func retirementSteps(declared []declaredModule) []auth.RetirementStep {
	out := make([]auth.RetirementStep, 0, len(declared))
	for _, d := range declared {
		if d.step != nil {
			out = append(out, d.step)
		}
	}
	return out
}
