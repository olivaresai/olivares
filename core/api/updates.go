// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/core/auth"
)

// UpdateService is the console's Updates page: the installed version, the newest
// signed release in this instance's channel with its notes, and the one action that
// installs it. The composition root implements it by running `olivares upgrade`, so
// the console and the CLI verify, install and refuse in exactly the same way; this
// package owns only the route and its authorization.
type UpdateService interface {
	// UpdateStatus checks the channel now and reports what it found. A channel that
	// cannot be reached is a CheckError in the reply, not a failure of the call: the
	// page still names the installed version.
	UpdateStatus(ctx context.Context) (UpdateStatusDTO, error)
	// ApplyUpdate installs the newest release (verified and atomic, as `olivares
	// upgrade --yes`) and restarts the engine on it. actor is the administrator who asked.
	ApplyUpdate(ctx context.Context, actor auth.Principal) (UpdateStatusDTO, error)
}

// UpdateStatusDTO is the Updates page.
type UpdateStatusDTO struct {
	// Edition is the build edition: community or enterprise.
	Edition string `json:"edition"`
	// Version is the version this engine runs now. After an apply it is still the old
	// one until the engine has restarted.
	Version string `json:"version"`
	Channel string `json:"channel"`
	// Available is the newest release in the channel, when the check reached it.
	Available       string `json:"available,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	ReleasedAt      string `json:"released_at,omitempty"`
	Notes           string `json:"notes,omitempty"`
	// Security marks a release that fixes a published advisory.
	Security   bool     `json:"security,omitempty"`
	Advisories []string `json:"advisories,omitempty"`
	// CheckError says why the channel could not be checked or the update could not
	// be planned, in the words `olivares upgrade` uses.
	CheckError string `json:"check_error,omitempty"`
	// RunningSessions is how many sessions this engine runs now. The restart that
	// applies an update stops every one of them (each can be resumed).
	RunningSessions int `json:"running_sessions"`
	// RestartRequired says a newer binary than the one running is already installed
	// (an apply whose restart did not happen); the engine runs the old version until
	// its service manager restarts it.
	RestartRequired bool `json:"restart_required,omitempty"`
	// Restarting says the engine is restarting itself on the new version; the
	// console waits for it and reconnects.
	Restarting bool `json:"restarting,omitempty"`
	// EditionSwitch is present on a Community build that holds a live Business or Enterprise
	// license: the move to the licensed build, which the plan above then describes.
	EditionSwitch *EditionSwitchDTO `json:"edition_switch,omitempty"`
}

// EditionSwitchDTO is the one next step from a Community build to the licensed build, for the
// way this installation was installed.
type EditionSwitchDTO struct {
	// To is the edition the license entitles: business or enterprise (one licensed build).
	To string `json:"to"`
	// Install is how this engine was installed: binary, container, package or kubernetes.
	Install string `json:"install"`
	// Command is the exact command for this installation; empty where the manifest does it.
	Command string `json:"command,omitempty"`
	// Then says what follows the command.
	Then string `json:"then"`
	// ConsoleAction says the console's apply takes the step itself, so it shows a button.
	ConsoleAction bool `json:"console_action"`
}

var (
	// ErrUpdatesUnavailable: no update service is wired. 501.
	ErrUpdatesUnavailable = errors.New("api: updates are not available on this engine")
	// ErrUpdateRefused: `olivares upgrade` refused or failed; nothing was installed
	// (or the previous binary was restored). The wrapped text is its own reason. 409.
	ErrUpdateRefused = errors.New("update refused")
	// ErrUpdateRestartUnavailable: the new version is installed, but the engine could
	// not restart itself on it. 503.
	ErrUpdateRestartUnavailable = errors.New("api: the engine cannot restart itself on the new version")
)

func (s *Server) updateSvc(w http.ResponseWriter, r *http.Request) (UpdateService, bool) {
	if s.updates == nil {
		s.writeError(w, r, ErrUpdatesUnavailable)
		return nil, false
	}
	return s.updates, true
}

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	svc, ok := s.updateSvc(w, r)
	if !ok {
		return
	}
	dto, err := svc.UpdateStatus(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// applyUpdateWriteWindow is how long the reply to an apply may take: the install
// downloads and verifies a release, which outlasts the server's 60-second write timeout
// (and `olivares upgrade` bounds one run at 10 minutes).
const applyUpdateWriteWindow = 12 * time.Minute

// handleApplyUpdate replaces the engine binary, so it needs the administrative
// step-up like the other privilege-shaped writes (license, modules, activation).
func (s *Server) handleApplyUpdate(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	if !s.requireStepUp(w, r, p) {
		return
	}
	svc, ok := s.updateSvc(w, r)
	if !ok {
		return
	}
	// A writer that cannot move its deadline keeps the server's; the install still finishes.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(applyUpdateWriteWindow))
	dto, err := svc.ApplyUpdate(r.Context(), p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}
