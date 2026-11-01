// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package consoleviews

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SessionNotificationAllowed reads the launcher's current event choices in the
// caller's tenant scope. Older preferences without event choices keep all events.
func SessionNotificationAllowed(ctx context.Context, sc store.Scope, owner, kind string) (bool, error) {
	event := map[string]string{"approval": "needsYou", "finished": "finished", "failed": "failed"}[kind]
	if event == "" {
		return false, nil
	}
	recs, err := drain(ctx, sc, eq(colOwner, "user:"+owner), eq(colFeature, uiStateFeature), eq(colName, uiStateName))
	if err != nil {
		return false, err
	}
	if len(recs) == 0 {
		return true, nil
	}
	var body uiStateBody
	if err := json.Unmarshal([]byte(recs[0].String(colParams)), &body); err != nil {
		return false, err
	}
	raw := body.Preferences["olivares.settings"]
	if raw == nil {
		return true, nil
	}
	var settings struct {
		State struct {
			Events map[string]bool `json:"notificationEvents"`
			Owner  json.RawMessage `json:"sessionNotificationsFor"`
		} `json:"state"`
	}
	if err := json.Unmarshal([]byte(*raw), &settings); err != nil {
		return false, err
	}
	if len(settings.State.Owner) > 0 {
		var selected *string
		if err := json.Unmarshal(settings.State.Owner, &selected); err != nil {
			return false, err
		}
		expected, _ := json.Marshal([]string{owner, sc.Tenant().String()})
		if selected == nil || *selected != string(expected) {
			return false, nil
		}
	}
	allowed, set := settings.State.Events[event]
	return !set || allowed, nil
}

// A user's console interface state: sidebar width, edition badge visibility and panel widths,
// kept on the server so it follows the user to another browser, as favorites do.
// One private saved-view row per user and tenant under its own reserved feature slug, beside
// the favorites row, so no new table. Gated by the module's READ permission like favorites:
// it is the caller's own interface state, every signed-in role keeps its own, nothing is
// shared, and a sidebar width is not an auditable act on the tenant.
const (
	uiStateFeature = "ui-state"
	uiStateName    = "ui-state"
)

// Panel widths: pixels by pane id. A save names only the panes it changes and a width of 0
// puts a pane back to its default (the console then keeps no entry for it).
const (
	maxPanes     = 16
	maxPaneWidth = 4000
)

var errTooManyPanes = errors.New("too many panes")

var paneIDRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

type uiStateBody struct {
	Preferences      map[string]*string `json:"preferences,omitempty"`
	Sidebar          string             `json:"sidebar,omitempty"`
	ShowEditionBadge *bool              `json:"show_edition_badge,omitempty"`
	PaneWidths       map[string]int     `json:"pane_widths,omitempty"`
}

type uiStateDTO struct {
	Preferences      map[string]*string `json:"preferences,omitempty"`
	Sidebar          string             `json:"sidebar,omitempty"`
	ShowEditionBadge bool               `json:"show_edition_badge"`
	PaneWidths       map[string]int     `json:"pane_widths,omitempty"`
	// Stored is false when the caller has no row yet (the console may then move what it
	// kept locally to the server once).
	Stored    bool   `json:"stored"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

var preferenceNames = map[string]bool{
	"olivares.prefs": true, "olivares.settings": true, "olivares.workspace": true, "olivares.tenant": true,
	"olivares:api-playground": true, "olivares.lang": true, "olivares.sessions.model-favorites": true,
	"olivares.notifications.lastSeen": true, "olivares.onboarding.dismissed": true,
	"olivares.compliance.opened": true, "olivares.sessions.sidePane": true,
}

func (b uiStateBody) validate() string {
	for name, value := range b.Preferences {
		if !preferenceNames[name] {
			return "unknown preference key"
		}
		if value != nil && len(*value) > 16384 {
			return "preference exceeds 16384 bytes"
		}
	}

	if b.Sidebar == "" && b.ShowEditionBadge == nil && len(b.PaneWidths) == 0 && len(b.Preferences) == 0 {
		return "at least one interface setting is required"
	}
	if b.Sidebar != "" && b.Sidebar != "full" && b.Sidebar != "rail" {
		return `sidebar must be "full" or "rail"`
	}
	if len(b.PaneWidths) > maxPanes {
		return errTooManyPanes.Error()
	}
	for id, px := range b.PaneWidths {
		if !paneIDRE.MatchString(id) {
			return "pane_widths: a pane id is lowercase letters, digits and dashes, up to 32 characters"
		}
		if px < 0 || px > maxPaneWidth {
			return "pane_widths: a width is 0 (the default) to 4000 pixels"
		}
	}
	return ""
}

// ownUIState finds the caller's interface-state row, or nil.
func ownUIState(r *http.Request, sc store.Scope, caller string) (*model.Record, error) {
	recs, err := drain(r.Context(), sc, eq(colOwner, caller), eq(colFeature, uiStateFeature))
	if err != nil {
		return nil, err
	}
	for i := range recs {
		if recs[i].String(colName) == uiStateName {
			return &recs[i], nil
		}
	}
	return nil, nil
}

func uiStateOf(rec *model.Record) uiStateDTO {
	if rec == nil {
		return uiStateDTO{ShowEditionBadge: true}
	}
	var body uiStateBody
	if err := json.Unmarshal([]byte(rec.String(colParams)), &body); err != nil || body.validate() != "" {
		// A row that no longer validates is read as nothing stored rather than failing the shell.
		return uiStateDTO{ShowEditionBadge: true}
	}
	return uiStateDTO{Preferences: body.Preferences, Sidebar: body.Sidebar, ShowEditionBadge: body.ShowEditionBadge == nil || *body.ShowEditionBadge, PaneWidths: body.PaneWidths, Stored: true, UpdatedAt: rec.String(model.ColUpdatedAt)}
}

// handleUIStateGet returns the caller's own console interface state; stored is false when
// none was saved yet.
func (m *Module) handleUIStateGet(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	caller := mc.Principal.Actor()
	var out uiStateDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		rec, err := ownUIState(r, sc, caller)
		if err != nil {
			return err
		}
		out = uiStateOf(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUIStatePut replaces the caller's console interface state.
func (m *Module) handleUIStatePut(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in uiStateBody
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	caller := mc.Principal.Actor()
	var out uiStateDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(SavedViewKind)
		if err != nil {
			return err
		}
		rec, err := ownUIState(r, sc, caller)
		if err != nil {
			return err
		}
		// Old sidebar-only clients must not erase a badge choice. Merge inside the
		// same transaction that writes the caller's private row.
		previous := uiStateOf(rec)
		state := in
		state.Preferences = make(map[string]*string)
		for key, value := range previous.Preferences {
			state.Preferences[key] = value
		}
		for key, value := range in.Preferences {
			if value == nil {
				delete(state.Preferences, key)
			} else {
				state.Preferences[key] = value
			}
		}
		if state.Sidebar == "" {
			state.Sidebar = previous.Sidebar
			if state.Sidebar == "" {
				state.Sidebar = "full"
			}
		}
		if state.ShowEditionBadge == nil {
			state.ShowEditionBadge = &previous.ShowEditionBadge
		}
		// Panes merge: the ones the request names replace theirs (0 drops one), the rest stay.
		widths := map[string]int{}
		for id, px := range previous.PaneWidths {
			widths[id] = px
		}
		for id, px := range in.PaneWidths {
			if px == 0 {
				delete(widths, id)
			} else {
				widths[id] = px
			}
		}
		if len(widths) > maxPanes {
			return errTooManyPanes
		}
		state.PaneWidths = nil
		if len(widths) > 0 {
			state.PaneWidths = widths
		}
		params, err := json.Marshal(state)
		if err != nil {
			return err
		}
		if rec == nil {
			created, err := repo.Create(r.Context(), model.Record{
				colFeature: uiStateFeature, colName: uiStateName, colDesc: "",
				colParams: string(params), colOwner: caller, colShared: false,
			})
			if err != nil {
				return err
			}
			out = uiStateOf(&created)
			return nil
		}
		rec.Set(colParams, string(params))
		updated, err := repo.Update(r.Context(), *rec)
		if err != nil {
			return err
		}
		out = uiStateOf(&updated)
		return nil
	})
	if errors.Is(err, errTooManyPanes) {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
