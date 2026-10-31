// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/driverfacts"
)

// toolDTO is one row of the engine's tool table as a client reads it: the console names
// and offers tools from it, so a new row needs no console change.
type toolDTO struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	// NameStem is the word an account of the tool is named from (driverfacts NameStem).
	NameStem string `json:"name_stem"`
	Session  bool   `json:"session"`
	// Bindings are the provider kinds a session of the tool runs on, each with the
	// endpoint rule CanBind applies (configured-endpoint, vendor-default-only or
	// local-endpoint-only).
	Bindings []toolBindingDTO `json:"bindings"`
}

type toolBindingDTO struct {
	Kind   string `json:"kind"`
	Egress string `json:"egress"`
}

func toolList() []toolDTO {
	all := driverfacts.All()
	out := make([]toolDTO, 0, len(all))
	for _, f := range all {
		bindings := make([]toolBindingDTO, 0, len(f.Bindings))
		for _, b := range f.Bindings {
			bindings = append(bindings, toolBindingDTO{Kind: b.Kind, Egress: b.Egress})
		}
		out = append(out, toolDTO{Key: f.Key, Name: f.Name, NameStem: f.NameStem(), Session: f.Session, Bindings: bindings})
	}
	return out
}

// handleListTools lists the tools this engine knows, in its own order: each tool's key,
// product name, the word its account names start from, whether a session runs on it, and
// the provider kinds it runs on with the endpoint rule for each. It reads no host state.
func (m *Module) handleListTools(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusOK, map[string]any{"tools": toolList()})
}
