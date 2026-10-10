// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

// Emergency consumption is an edition-neutral deny-capable engine port.
type consumeBreakGlassRequest struct {
	Action      string `json:"action"`
	SubjectKind string `json:"subject_kind,omitempty"`
	SubjectRef  string `json:"subject_ref,omitempty"`
}

type EmergencyConsumption struct {
	Granted   bool   `json:"granted"`
	Grant     string `json:"grant,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}
