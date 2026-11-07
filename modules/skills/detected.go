// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills

import (
	"context"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
)

// DetectedSkill describes existing native instructions without importing them.
type DetectedSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Path        string `json:"path"`
	Hash        string `json:"sha256"`
	Driver      string `json:"driver"`
	ProfileRef  string `json:"profile_ref,omitempty"`
}

// ParseDetectedSkill uses the same front matter and digest as catalog imports.
func ParseDetectedSkill(dir string, body []byte) (DetectedSkill, error) {
	member, err := parseSkill(dir, body)
	return DetectedSkill{Name: member.Name, Description: member.Description, Hash: member.SkillDigest}, err
}

// UseDetection binds the provider-home owner before requests are served.
func (m *Module) UseDetection(scan func(context.Context, api.ModuleContext) ([]DetectedSkill, error)) {
	m.opts.Detect = scan
}

// listDetected lists the existing native skills visible to the caller without importing or executing them.
func (m *Module) listDetected(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.opts.Detect == nil {
		respondError(w, refuse("source_unavailable", "Skill detection is unavailable"))
		return
	}
	items, err := m.opts.Detect(r.Context(), mc)
	if err != nil {
		respondError(w, refuse("source_unavailable", "Skills could not be detected"))
		return
	}
	respond(w, http.StatusOK, api.ListResponse[DetectedSkill]{Items: items})
}
