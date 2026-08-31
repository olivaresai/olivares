// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

// - provider-entitlement attestation for restricted access tiers. Anthropic's
// model overview describes Claude Mythos 5 as "not generally available",
// "invitation-only", with "no self-serve sign-up"; customers must contact their
// account team for access. Also verified that Project Glasswing access was
// suspended and restored at least once, with no tenant-admin toggle. This table is
// therefore an operator-attested revocation ledger: it can only narrow routing for
// a tier a policy already enrolled, never grant provider access by itself.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const accessTierEntitlementKind model.Kind = "models.access_tier_entitlement"

const accessTierEntitlementTable = "models_access_tier_entitlement"

const (
	colATETier      = "tier"
	colATEState     = "state"
	colATENote      = "note"
	colATEAsOf      = "as_of"
	colATEUpdatedBy = "updated_by"
)

const (
	entitlementStateGranted   = "granted"
	entitlementStateSuspended = "suspended"
)

var accessTierEntitlementStates = set(entitlementStateGranted, entitlementStateSuspended)

// registerAccessTierEntitlementSchema registers one operator-attested entitlement
// state per restricted access tier and tenant. The unique index leads with tenant_id
// so a suspension in one tenant cannot affect another.
func registerAccessTierEntitlementSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  accessTierEntitlementKind,
		Table: accessTierEntitlementTable,
		Fields: []model.FieldSpec{
			{Name: colATETier, Kind: model.KindText, Indexed: true},
			{Name: colATEState, Kind: model.KindText, Indexed: true},
			{Name: colATENote, Kind: model.KindText, Nullable: true},
			{Name: colATEAsOf, Kind: model.KindText, Nullable: true},
			{Name: colATEUpdatedBy, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			Name: "models_access_tier_entitlement_uniq", Columns: []string{model.ColTenantID, colATETier}, Unique: true,
		}},
	})
}

// accessTierEntitlementDTO is the operator-attested provider entitlement state for
// one restricted tier. It records provider-side state, not policy enrollment:
// policies still need access_tiers, and a suspended attestation only narrows them.
type accessTierEntitlementDTO struct {
	ID        string `json:"id,omitempty"`
	Tier      string `json:"tier"`
	State     string `json:"state"`
	Note      string `json:"note,omitempty"`
	AsOf      string `json:"as_of,omitempty"`
	UpdatedBy string `json:"updated_by,omitempty"`
}

func (d *accessTierEntitlementDTO) validate() string {
	d.Tier = strings.ToLower(trimClamp(d.Tier))
	if d.Tier == "" {
		return "tier is required"
	}
	d.State = strings.ToLower(strings.TrimSpace(d.State))
	if !accessTierEntitlementStates[d.State] {
		return "state must be granted or suspended"
	}
	d.Note = trimClamp(d.Note)
	d.AsOf = strings.TrimSpace(d.AsOf)
	return ""
}

func (d accessTierEntitlementDTO) toRecord(actor, at string) model.Record {
	asOf := d.AsOf
	if asOf == "" {
		asOf = at
	}
	return model.Record{
		colATETier:      d.Tier,
		colATEState:     d.State,
		colATENote:      d.Note,
		colATEAsOf:      asOf,
		colATEUpdatedBy: actor,
	}
}

func toAccessTierEntitlementDTO(rec model.Record) accessTierEntitlementDTO {
	return accessTierEntitlementDTO{
		ID:        rec.String(model.ColID),
		Tier:      rec.String(colATETier),
		State:     rec.String(colATEState),
		Note:      rec.String(colATENote),
		AsOf:      rec.String(colATEAsOf),
		UpdatedBy: rec.String(colATEUpdatedBy),
	}
}

// handleListAccessTierEntitlements lists operator-attested entitlement states,
// optionally filtered by tier or state. Absence of a row means the existing routing
// behavior applies unchanged.
func (m *Module) handleListAccessTierEntitlements(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tier"))); v != "" {
		q.Filters = append(q.Filters, eq(colATETier, v))
	}
	if v := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("state"))); v != "" {
		q.Filters = append(q.Filters, eq(colATEState, v))
	}
	out := listResponse[accessTierEntitlementDTO]{Items: []accessTierEntitlementDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(accessTierEntitlementKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toAccessTierEntitlementDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUpsertAccessTierEntitlement records the current provider-side entitlement
// state for one restricted tier. It is an upsert keyed by tier and is audited as a
// governance write attributed to the real principal.
func (m *Module) handleUpsertAccessTierEntitlement(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in accessTierEntitlementDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out accessTierEntitlementDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(accessTierEntitlementKind)
		if err != nil {
			return err
		}
		actor := mc.Principal.Actor()
		at := model.NewTimestamp(time.Now()).String()
		existing, _, err := repo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colATETier, in.Tier)}, Limit: 1})
		if err != nil {
			return err
		}
		var rec model.Record
		if len(existing) > 0 {
			rec = existing[0]
			for k, v := range in.toRecord(actor, at) {
				rec[k] = v
			}
			rec, err = repo.Update(r.Context(), rec)
		} else {
			rec, err = repo.Create(r.Context(), in.toRecord(actor, at))
		}
		if err != nil {
			return err
		}
		out = toAccessTierEntitlementDTO(rec)
		return auditOwned(r.Context(), sc, mc, accessTierEntitlementKind, "attest", model.ID(rec.String(model.ColID)))
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// suspendedEntitlementTiers returns the restricted tiers the operator has attested
// as provider-suspended. A granted row and an absent row both return no suspension,
// preserving the existing routing behavior unless the stored state narrows it.
func suspendedEntitlementTiers(ctx context.Context, sc store.Scope) ([]string, error) {
	repo, err := sc.Ext(accessTierEntitlementKind)
	if err != nil {
		return nil, err
	}
	recs, err := listAllExt(ctx, repo, eq(colATEState, entitlementStateSuspended))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(recs))
	for _, rec := range recs {
		tier := strings.TrimSpace(rec.String(colATETier))
		if tier != "" {
			out = append(out, tier)
		}
	}
	return cleanStrings(out), nil
}
