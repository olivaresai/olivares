// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package consoleviews

import (
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A user's console FAVORITES: the pages they starred, kept on the server so they follow
// the user to another browser, another address of the same console, or a new sign-in.
// Before 26.10.1 they lived only in the browser's localStorage, keyed by origin, and were
// "lost at once" whenever the console was opened another way.
//
// They are one private saved-view row per user and tenant under a reserved feature slug,
// so no new table: (tenant, feature, owner, name) is already the unique key. The routes
// read and replace only the caller's own row. They are gated by the module's READ
// permission on purpose: favorites are the caller's own interface state, and every
// signed-in role (viewers too) keeps its own. Nothing here is shared or seen by others,
// and a star toggle is not an auditable act on the tenant, so it is not ledgered.
const (
	favoritesFeature = "favorites"
	favoritesName    = "favorites"
	// maxFavorites is above the number of pages the console offers to star, so every page
	// a person can star is saved (the console's MAX_FAVORITES is the same number).
	maxFavorites = 256
)

// favoriteIDPattern keeps entries to console-registry-shaped ids (the console resolves
// them against its registry and ignores what it does not know).
var favoriteIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,63}$`)

type favoriteLink struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type favoritesBody struct {
	Favorites []favoriteLink `json:"favorites"`
}

type favoritesDTO struct {
	Favorites []favoriteLink `json:"favorites"`
	// Stored is false when the caller has no favorites row yet (the console may then move
	// what it kept locally to the server once).
	Stored    bool   `json:"stored"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

func (b *favoritesBody) validate() string {
	if len(b.Favorites) > maxFavorites {
		return "too many favorites (max 256)"
	}
	seen := make(map[string]bool, len(b.Favorites))
	for _, f := range b.Favorites {
		if f.Kind != "feature" && f.Kind != "utility" {
			return `each favorite needs kind "feature" or "utility"`
		}
		if !favoriteIDPattern.MatchString(f.ID) {
			return "each favorite needs a console page id"
		}
		if seen[f.ID] {
			return "a page is listed twice"
		}
		seen[f.ID] = true
	}
	return ""
}

// ownFavorites finds the caller's favorites row, or nil.
func ownFavorites(r *http.Request, sc store.Scope, caller string) (*model.Record, error) {
	recs, err := drain(r.Context(), sc, eq(colOwner, caller), eq(colFeature, favoritesFeature))
	if err != nil {
		return nil, err
	}
	for i := range recs {
		if recs[i].String(colName) == favoritesName {
			return &recs[i], nil
		}
	}
	return nil, nil
}

func favoritesOf(rec *model.Record) favoritesDTO {
	if rec == nil {
		return favoritesDTO{Favorites: []favoriteLink{}}
	}
	var body favoritesBody
	if err := json.Unmarshal([]byte(rec.String(colParams)), &body); err != nil || body.validate() != "" {
		// A row that no longer validates is read as empty rather than failing the shell.
		body.Favorites = nil
	}
	if body.Favorites == nil {
		body.Favorites = []favoriteLink{}
	}
	return favoritesDTO{Favorites: body.Favorites, Stored: true, UpdatedAt: rec.String(model.ColUpdatedAt)}
}

// handleFavoritesGet returns the caller's own favorites; stored is false when none were
// saved yet.
func (m *Module) handleFavoritesGet(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	caller := mc.Principal.Actor()
	var out favoritesDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		rec, err := ownFavorites(r, sc, caller)
		if err != nil {
			return err
		}
		out = favoritesOf(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleFavoritesPut replaces the caller's favorites with the given ordered list.
func (m *Module) handleFavoritesPut(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in favoritesBody
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Favorites == nil {
		in.Favorites = []favoriteLink{}
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	params, err := json.Marshal(in)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("could not encode favorites"))
		return
	}
	caller := mc.Principal.Actor()
	var out favoritesDTO
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(SavedViewKind)
		if err != nil {
			return err
		}
		rec, err := ownFavorites(r, sc, caller)
		if err != nil {
			return err
		}
		if rec == nil {
			created, err := repo.Create(r.Context(), model.Record{
				colFeature: favoritesFeature, colName: favoritesName, colDesc: "",
				colParams: string(params), colOwner: caller, colShared: false,
			})
			if err != nil {
				return err
			}
			out = favoritesOf(&created)
			return nil
		}
		rec.Set(colParams, string(params))
		updated, err := repo.Update(r.Context(), *rec)
		if err != nil {
			return err
		}
		out = favoritesOf(&updated)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
