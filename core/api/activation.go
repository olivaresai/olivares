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
// Applying a preset or a family stages the governed activation manifest. A change
// the edition applies to the running engine needs no restart; one it cannot (an
// add-on wired into the engine's foundations at boot) says so through
// RestartRequired, and the console tells the administrator plainly. The DTOs are
// plain structs owned here, so core/api never imports the commercial catalog — the
// enterprise implementation maps its types onto these.
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

// ActivationFamilyPrefix starts the selection that names one family rather than a
// preset: "family:regulated" is the regulated family alone, while the preset
// "regulated" is that family together with the business one.
const ActivationFamilyPrefix = "family:"

// ActivationApplyRequest is an enable/disable/promote instruction.
type ActivationApplyRequest struct {
	Action string // enable | disable | promote
	Preset string // for enable / disable: a preset name, or ActivationFamilyPrefix + a family key
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
	// Families are the groups the edition sells add-ons in; an administrator turns
	// each on or off as a whole.
	Families []ActivationFamilyDTO `json:"families,omitempty"`

	// Restarting says the engine is restarting itself to apply the change; the
	// console waits for it and reconnects.
	Restarting bool `json:"restarting,omitempty"`
}

// ActivationAddonDTO is one add-on's console row.
type ActivationAddonDTO struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	Env         string `json:"env"`
	Preset      string `json:"preset"`           // the tier that introduces it
	Family      string `json:"family,omitempty"` // the family that sells it (ActivationFamilyDTO.Key)
	State       string `json:"state"`            // active | pending | available | console
	Reason      string `json:"reason,omitempty"`
	NeedsSecret bool   `json:"needs_secret,omitempty"`
	// InBuild and LicenseCovered are independent facts from the edition's
	// catalog and verified licence. Nil means the fact is not known; false is
	// an observed absence. Neither field asserts operational readiness.
	InBuild        *bool `json:"in_build,omitempty"`
	LicenseCovered *bool `json:"license_covered,omitempty"`
}

// ActivationFamilyDTO is one family: its key, its English title, the keys of the
// add-ons it holds and the keys of those that are on now. An add-on that is linked in
// the build but not turned on is not in Active.
type ActivationFamilyDTO struct {
	Key    string   `json:"key"`
	Title  string   `json:"title"`
	Addons []string `json:"addons"`
	Active []string `json:"active"`
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
	// ErrActivationNotRecorded: the edition applied the change to this node's
	// file, but the deployment settings could not be saved, so nothing restarts.
	ErrActivationNotRecorded = errors.New("api: activation not recorded in the deployment settings")
	// ErrActivationRestartUnavailable: the change is saved, but the engine could
	// not restart itself to apply it.
	ErrActivationRestartUnavailable = errors.New("api: the engine cannot restart itself to apply the activation")
)
