// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
)

// ActivationService is the LIVE enterprise activation surface: the console
// reads per-add-on activation state and enables/disables a preset or promotes a
// staged add-on, mirroring the CLI `olivares enterprise` command. It is
// ENTERPRISE-ONLY — the community build wires nil and every /v1/console/activation
// route answers 501 (the honest not-wired seam, like the DR / log-broker surfaces).
//
// Applying a preset stages the governed activation manifest; because add-ons are
// wired at boot (the module graph is built once, unlike a license which hot-applies
// for seats), the change takes effect on the next RESTART. RestartRequired is
// therefore always true, and the console says so plainly rather than implying a
// live flip. The DTOs are plain structs owned here, so core/api never imports the
// commercial catalog — the enterprise implementation maps its types onto these.
type ActivationService interface {
	// ActivationStatus reports the build edition, the enabled preset, and every
	// add-on's state (active / pending / available / console) for the table.
	ActivationStatus(ctx context.Context) (ActivationStatusDTO, error)
	// ActivationPreview computes the change plan (diff) for a preset WITHOUT writing
	// anything — the console shows it before the operator confirms.
	ActivationPreview(ctx context.Context, preset string) (ActivationPlanDTO, error)
	// ActivationApply enables/disables a preset or promotes a staged add-on. It
	// writes the manifest and audits the change; the caller supplies the actor.
	ActivationApply(ctx context.Context, req ActivationApplyRequest) (ActivationStatusDTO, error)
}

// ActivationApplyRequest is an enable/disable/promote instruction.
type ActivationApplyRequest struct {
	Action string // enable | disable | promote
	Preset string // for enable / disable
	Addon  string // for promote
	Actor  string // resolved from the authenticated principal by the handler
}

// ActivationStatusDTO is the rendered activation view for the console.
type ActivationStatusDTO struct {
	Edition         string                `json:"edition"`
	Preset          string                `json:"preset,omitempty"`
	RestartRequired bool                  `json:"restart_required"`
	Addons          []ActivationAddonDTO  `json:"addons"`
	Presets         []ActivationPresetDTO `json:"presets"`
}

// ActivationAddonDTO is one add-on's console row.
type ActivationAddonDTO struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	Env         string `json:"env"`
	Preset      string `json:"preset"` // the tier that introduces it
	State       string `json:"state"`  // active | pending | available | console
	Reason      string `json:"reason,omitempty"`
	NeedsSecret bool   `json:"needs_secret,omitempty"`
}

// ActivationPresetDTO lists a preset's add-on keys for the "enable" chooser.
type ActivationPresetDTO struct {
	Name   string   `json:"name"`
	Addons []string `json:"addons"`
}

// ActivationPlanDTO is the diff a preview returns.
type ActivationPlanDTO struct {
	Preset  string                   `json:"preset"`
	Changes bool                     `json:"changes"`
	Entries []ActivationPlanEntryDTO `json:"entries"`
}

// ActivationPlanEntryDTO is one add-on's line in a preview diff.
type ActivationPlanEntryDTO struct {
	Addon  string `json:"addon"`
	Action string `json:"action"` // activate | stage | unchanged | console
	State  string `json:"state,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Activation service errors (mapped to honest HTTP codes in statusFor).
var (
	// ErrActivationUnavailable: no activation service wired (community build / an
	// embedder that did not opt in). 501, like the other console seams.
	ErrActivationUnavailable = errors.New("api: activation service unavailable")
	// ErrActivationInvalidRequest: a malformed enable/disable/promote (bad preset or
	// add-on). 400.
	ErrActivationInvalidRequest = errors.New("api: activation request invalid")
)
