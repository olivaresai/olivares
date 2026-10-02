// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import "github.com/olivaresai/olivares/core/model"

// CompositionCensus is an OPTIONAL Store capability: the descriptors of the
// closed registry the store opened with, and the readiness verdict computed once
// over them at open.
//
// A composition is ready for an absence proof only when every text, JSON, bytes
// and UUID column of every registered descriptor is declared, every union
// variant is typed and classified, and nothing the composition declares is
// missing. An unready composition keeps serving; every absence proof it is asked
// for answers incomplete, with the first cause.
type CompositionCensus interface {
	// CensusDescriptors returns every descriptor of the closed registry, core
	// catalogs first, in registration order.
	CensusDescriptors() []model.EntityDescriptor
	// CompositionReadiness returns the verdict computed at open.
	CompositionReadiness() Readiness
}

// Readiness is a composition's census verdict.
type Readiness struct {
	// Computed is false when no verdict was computed.
	Computed bool
	// Causes lists every defect found, in registry order; the composition is
	// ready when it is computed and empty.
	Causes []string
}

// Ready reports whether the verdict was computed and found nothing missing.
func (r Readiness) Ready() bool { return r.Computed && len(r.Causes) == 0 }

// Cause returns the reason an absence proof is incomplete, or "" when ready.
func (r Readiness) Cause() string {
	switch {
	case !r.Computed:
		return "census_incomplete:not_computed"
	case len(r.Causes) > 0:
		return "census_incomplete:" + r.Causes[0]
	}
	return ""
}

// CompositionReaderRegistry is the OPTIONAL registry capability a composition
// declares its retirement readers through: each declared module names the
// counted or content columns (kind.column) its retirement step reads. Readiness requires
// every counted or content column of the closed registry to be read by exactly one declared
// module, so a registry without the capability, or a composition that declares
// nothing, opens unready: the capability fails closed.
type CompositionReaderRegistry interface {
	// DeclareCompositionReader records that module's retirement step reads
	// columns. It must be called before the registry closes.
	DeclareCompositionReader(module string, columns []string) error
}

// AuthPartitionReader is the name under which the store itself declares the
// reader of its auth partition's counted columns: the offboard that removes an
// account from a tenant removes what the account holds there, so no composition
// declares, or copies, those columns.
const AuthPartitionReader = "auth"

// CompositionReaders is an OPTIONAL Store capability: the counted or content columns
// (kind.column) each declared reader covers, as the closed registry recorded
// them, including the store's own AuthPartitionReader.
type CompositionReaders interface {
	// CompositionReaders returns a copy of the declared readers, by module.
	CompositionReaders() map[string][]string
}

// CompositionContribution is what one edition of the product declares it
// contributes to the census, whether or not the build at hand links it: the
// declared modules whose retirement readers it expects, and the stores it keeps
// outside the registry, with their column declarations. A module a contribution
// names that declares no reader, or links no retirement step, stays missing: it
// never counts as a module with nothing to read.
type CompositionContribution struct {
	// Edition names the contributor, for readiness causes.
	Edition string
	// Modules are the declared modules the edition expects. Each must declare a
	// retirement reader, and the retirement must run its step.
	Modules []string
	// OutsideStores are the descriptors of the tables the edition keeps outside
	// the registry. Every column must be declared, and every counted or content column read
	// by exactly one declared module.
	OutsideStores []model.EntityDescriptor
}

// CompositionContributionRegistry is the OPTIONAL registry capability a
// composition declares each edition's contribution through, before the registry
// closes. The readiness verdict computed at open then requires every expected
// module to have declared its reader and every outside store to be declared and
// read.
type CompositionContributionRegistry interface {
	// DeclareCompositionContribution records c. It must be called before the
	// registry closes.
	DeclareCompositionContribution(c CompositionContribution) error
}

// CompositionContributions is an OPTIONAL Store capability: the contributions
// the closed registry recorded, in declaration order.
type CompositionContributions interface {
	// CompositionContributions returns a copy of the recorded contributions.
	CompositionContributions() []CompositionContribution
}
