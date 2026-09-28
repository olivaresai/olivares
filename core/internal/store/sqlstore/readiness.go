// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"fmt"
	"sort"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// CensusDescriptors implements store.CompositionCensus: the closed registry's
// descriptors in registration order, core catalogs first.
func (s *sqlStore) CensusDescriptors() []model.EntityDescriptor { return s.reg.descriptors() }

// CompositionReadiness implements store.CompositionCensus.
func (s *sqlStore) CompositionReadiness() store.Readiness { return s.readiness }

var _ store.CompositionCensus = (*sqlStore)(nil)

// CompositionReaders implements store.CompositionReaders. The registry is closed,
// so the readers cannot change while the store serves.
func (s *sqlStore) CompositionReaders() map[string][]string { return s.reg.readersByModule() }

var _ store.CompositionReaders = (*sqlStore)(nil)

// CompositionContributions implements store.CompositionContributions.
func (s *sqlStore) CompositionContributions() []store.CompositionContribution {
	return s.reg.compositionContributions()
}

var _ store.CompositionContributions = (*sqlStore)(nil)

// computeCompositionReadiness is the census verdict over the closed registry,
// computed once at open. It lists, in registry order and then in the order the
// editions declared their outside stores, every declaration defect of every
// descriptor (an undeclared column, an unclassified or stale leaf, an opaque
// leaf nothing maps, a union kind with no typed and classified variant, a None
// without cited lines), then every counted column that no declared module, or
// more than one, reads, then every declared reader of a column that is not
// counted, then every module an edition expects that declared no reader.
func computeCompositionReadiness(reg *registry) store.Readiness {
	out := store.Readiness{Computed: true}
	counted := make(map[string]bool)
	var order []string
	all := reg.descriptors()
	for _, c := range reg.contributions {
		all = append(all, c.OutsideStores...)
	}
	for _, d := range all {
		for _, defect := range d.PrincipalDefects() {
			out.Causes = append(out.Causes, defect.Error())
		}
		// The member-bound exception is the store's to grant, for the columns it
		// names; declared anywhere else it is refused here, and the seam reads the
		// column as an ordinary counted reference.
		for _, f := range d.Fields {
			if f.Principal != nil && f.Principal.MemberBound && !memberBoundColumn(d, f) {
				out.Causes = append(out.Causes, fmt.Sprintf(
					"%s.%s: member-bound outside the membership-bound delivery columns", d.Kind, f.Name))
			}
		}
		for _, col := range d.CountedColumns() {
			key := string(d.Kind) + "." + col
			counted[key] = true
			order = append(order, key)
		}
	}
	for _, key := range order {
		if readers := reg.readers[key]; len(readers) != 1 {
			out.Causes = append(out.Causes, fmt.Sprintf(
				"%s: counted column read by %d declared modules %v, want exactly one", key, len(readers), readers))
		}
	}
	declared := make([]string, 0, len(reg.readers))
	for key := range reg.readers {
		if !counted[key] {
			declared = append(declared, key)
		}
	}
	sort.Strings(declared)
	for _, key := range declared {
		out.Causes = append(out.Causes, fmt.Sprintf(
			"%s: declared modules %v read a column that is not counted", key, reg.readers[key]))
	}
	byModule := reg.readersByModule()
	for _, c := range reg.contributions {
		for _, m := range c.Modules {
			if len(byModule[m]) == 0 {
				out.Causes = append(out.Causes, fmt.Sprintf(
					"%s: a module the %s edition declares registered no retirement reader", m, c.Edition))
			}
		}
	}
	return out
}
