// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package registry binds the module spec to the existing implementations.
package registry

import (
	"fmt"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/modulespec"
	"github.com/olivaresai/olivares/sdk"
)

// Build uses configured instances where supplied and default constructors for
// the remaining rows. Each instance supplies its own APIRoutes, Permissions and
// RegisterSchema (including file migrations); none of those facts are copied.
// Edition instances replace the shared placeholder or append new namespaces.
// A nil edition list uses the default placeholders (inventory tools); a non-nil
// list, including an empty one, is the edition's authoritative registration set.
// Defaults and registration order come exclusively from the spec.
func Build(configured, edition []api.Module) ([]api.Module, error) {
	built := make(map[string]api.Module, len(configured)+len(edition))
	for _, group := range [][]api.Module{configured, edition} {
		for _, m := range group {
			if m == nil {
				return nil, fmt.Errorf("module composition contains a nil instance")
			}
			ns := m.APINamespace()
			if _, duplicate := built[ns]; duplicate {
				return nil, fmt.Errorf("module composition repeats namespace %q", ns)
			}
			built[ns] = m
		}
	}
	var all []api.Module
	seen := make(map[string]bool)
	for _, spec := range modulespec.All() {
		m, ok := built[spec.Namespace]
		if spec.EditionSlot && edition != nil && !ok {
			continue
		}
		if !ok {
			var err error
			m, err = newDefault(spec.Namespace, built)
			if err != nil {
				return nil, err
			}
			lifecycle, ok := m.(sdk.Module)
			if !ok || lifecycle.Descriptor().Name != spec.Name {
				return nil, fmt.Errorf("module spec %q does not implement lifecycle %q", spec.Namespace, spec.Name)
			}
		}
		if m.APINamespace() != spec.Namespace {
			return nil, fmt.Errorf("module spec %q constructed namespace %q", spec.Namespace, m.APINamespace())
		}
		built[spec.Namespace] = m
		seen[spec.Namespace] = true
		all = append(all, m)
	}
	for _, m := range configured {
		if !seen[m.APINamespace()] {
			return nil, fmt.Errorf("configured module %q has no spec row", m.APINamespace())
		}
	}
	for _, m := range edition {
		if !seen[m.APINamespace()] {
			all = append(all, m)
		}
	}
	return all, nil
}
