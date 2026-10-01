// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"slices"

	"github.com/olivaresai/olivares/cmd/olivares/internal/agenttoolsapi"
	"github.com/olivaresai/olivares/core/api"
)

// apiModules is the common route composition for serving, reflection and route
// qualification. Reflection supplies an inert host module; boot owns its journal.
func (set moduleSet) apiModules(hostTools *agenttoolsapi.Module) []api.Module {
	return append(slices.Clone(set.all), hostTools)
}
