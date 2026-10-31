// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const limitResumeSettingsKind model.Kind = "sessions.limit_resume_settings"
const colLimitResumeOwner = "owner_id"
const colLimitResumeAutomatic = "automatic"

type limitResumeSettingsDTO struct {
	Automatic bool `json:"automatic"`
}

func (m *Module) registerLimitResumeSettingsSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{Kind: limitResumeSettingsKind, Table: "sessions_limit_resume_settings", Fields: []model.FieldSpec{
		{Name: colLimitResumeOwner, Kind: model.KindUUID, Principal: model.Ref(model.EncodeUserID, model.ClassAuthority)},
		{Name: colLimitResumeAutomatic, Kind: model.KindBool},
	}, Indexes: []model.IndexSpec{{Name: "sessions_limit_resume_settings_owner", Columns: []string{model.ColTenantID, colLimitResumeOwner}, Unique: true}}})
}
func limitResumeSettingsRow(ctx context.Context, sc store.Scope, user model.ID) (model.Record, error) {
	repo, err := sc.Ext(limitResumeSettingsKind)
	if err != nil {
		return nil, err
	}
	rows, _, err := repo.List(ctx, model.Query{Limit: 1, Filters: []model.Filter{eq(colLimitResumeOwner, user.String())}})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}
func (m *Module) automaticLimitResume(ctx context.Context, tenant model.TenantID, user model.ID) (bool, error) {
	var enabled bool
	err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
		row, err := limitResumeSettingsRow(ctx, sc, user)
		if err == nil {
			enabled = row.Bool(colLimitResumeAutomatic)
		}
		return err
	})
	return enabled, err
}

// handleLimitResumeSettings reads or changes the signed-in person's default-off
// preference for continuing sessions automatically after a usage limit resets.
func (m *Module) handleLimitResumeSettings(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	user := mc.Principal.UserID
	if user.IsZero() || mc.Principal.AgentIdentity != "" {
		writeJSON(w, http.StatusForbidden, errorBody("a person's sign-in is required"))
		return
	}
	if r.Method == http.MethodGet {
		enabled, err := m.automaticLimitResume(r.Context(), mc.Tenant, user)
		if err != nil {
			writeRunErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, limitResumeSettingsDTO{enabled})
		return
	}
	var body struct {
		Automatic *bool `json:"automatic"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if body.Automatic == nil {
		writeJSON(w, http.StatusBadRequest, errorBody("automatic is required"))
		return
	}
	err := auth.FencedWrite(r.Context(), mc.Standing, mc.Tenant, []model.ID{user}, auth.FenceDirectory, func(write func(store.Scope) error) error { return mc.Data.Mutate(r.Context(), write) }, func(sc store.Scope, _ bool) error {
		row, err := limitResumeSettingsRow(r.Context(), sc, user)
		if err != nil {
			return err
		}
		repo, err := sc.Ext(limitResumeSettingsKind)
		if err != nil {
			return err
		}
		switch {
		case !*body.Automatic:
			if row != nil {
				err = repo.Delete(r.Context(), model.ID(row.String(model.ColID)))
			}
		case row == nil:
			_, err = repo.Create(r.Context(), model.Record{colLimitResumeOwner: user.String(), colLimitResumeAutomatic: true})
		}

		if err != nil {
			return err
		}
		_, err = sc.Audit().Append(r.Context(), model.AuditDraft{Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(), Action: "sessions.limit_resume_settings.changed", TargetKind: limitResumeSettingsKind, Meta: map[string]any{"automatic": *body.Automatic}})
		return err
	})
	if err != nil {
		m.warnf("sessions: could not save automatic resume preference", "err", redactErr(err))
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, limitResumeSettingsDTO{*body.Automatic})
}
