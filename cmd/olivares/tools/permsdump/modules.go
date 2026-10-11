// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/modules/registry"
)

// allModules reads the same spec as boot. Routes and permissions come from the
// module's default constructor; configured adapters do not change the vocabulary.
func allModules() []api.Module {
	modules, err := registry.Build(nil, nil)
	if err != nil {
		panic(err)
	} // A broken built-in table must not emit a partial inventory.
	return modules
}
