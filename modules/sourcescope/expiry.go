// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// BindingExpiryReport counts one idempotent binding-expiry pass.
type BindingExpiryReport struct {
	Scanned   int `json:"scanned"`
	Expired   int `json:"expired"`
	Reminders int `json:"reminders"`
}

type bindingExpiryFinding struct {
	kind  string
	id    string
	title string
}

// SweepExpiredBindings records lapsed bindings in the ledger and sends the one-shot
// reminders. Authorization already reads expires_at on every resolve, so a slow
// sweep can never keep a grant alive or turn its last allow into a global source.
// Reminders run in their own transaction: a notify outage retries them and never
// holds back the expiry audit.
func (m *Module) SweepExpiredBindings(ctx context.Context, tenant model.TenantID) (BindingExpiryReport, error) {
	report, expireErr := m.sweepBindings(ctx, tenant, true)
	reminded, remindErr := m.sweepBindings(ctx, tenant, false)
	report.Reminders = reminded.Reminders
	return report, errors.Join(expireErr, remindErr)
}

// sweepBindings runs one half of the sweep: lapsed bindings (expire) or the
// reminders of bindings still in force.
func (m *Module) sweepBindings(ctx context.Context, tenant model.TenantID, expire bool) (BindingExpiryReport, error) {
	var report BindingExpiryReport
	var findings []bindingExpiryFinding
	data := m.moduleData()
	if data == nil {
		return report, errors.New("sourcescope: expiry requires module data")
	}
	err := data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(bindingKind)
		if err != nil {
			return err
		}
		recs, err := allExt(ctx, sc, bindingKind)
		if err != nil {
			return err
		}
		now := m.clock.Now()
		for _, rec := range recs {
			report.Scanned++
			raw := rec.String(colExpiresAt)
			if raw == "" {
				continue
			}
			expires, err := model.ParseTimestamp(raw)
			if err != nil {
				return fmt.Errorf("sourcescope: binding %s has an invalid expiry: %w", rec.String(model.ColID), err)
			}
			if !expire {
				if !now.Before(expires) || !rec.Bool(colEnabled) {
					continue
				}
				changed := false
				for _, reminder := range []struct {
					kind   string
					column string
					window time.Duration
					title  string
				}{
					{kind: "sourcescope_binding_expiring_14", column: colRemind14At, window: 14 * 24 * time.Hour, title: "Access binding expires in 14 days"},
					{kind: "sourcescope_binding_expiring_1", column: colRemind1At, window: 24 * time.Hour, title: "Access binding expires in 1 day"},
				} {
					if rec.String(reminder.column) != "" || expires.Time().After(now.Time().Add(reminder.window)) {
						continue
					}
					if reminder.column == colRemind14At && !expires.Time().After(now.Time().Add(24*time.Hour)) {
						continue
					}
					rec[reminder.column] = now.String()
					changed = true
					report.Reminders++
					findings = append(findings, bindingExpiryFinding{kind: reminder.kind, id: rec.String(model.ColID), title: reminder.title})
				}
				if !changed {
					continue
				}
				if _, err := repo.Update(ctx, rec); err != nil {
					return err
				}
				continue
			}
			if now.Before(expires) || rec.String(colExpiredAt) != "" {
				continue
			}
			rec[colExpiredAt] = now.String()
			report.Expired++
			findings = append(findings, bindingExpiryFinding{kind: "sourcescope_binding_expired", id: rec.String(model.ColID), title: "Access binding expired"})
			updated, err := repo.Update(ctx, rec)
			if err != nil {
				return err
			}
			if err := appendBindingExpiryAudit(ctx, sc, toBindingDTO(updated)); err != nil {
				return err
			}
		}
		if expire {
			return nil
		}
		// Reminders publish inside this transaction on purpose: a failed publish rolls
		// the markers back, so the next sweep sends them again.
		for _, finding := range findings {
			if err := m.emitBindingExpiryFinding(ctx, tenant, finding); err != nil {
				return fmt.Errorf("sourcescope: publish binding reminder: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return report, err
	}
	if !expire {
		return report, nil
	}
	var publishErrs []error
	for _, finding := range findings {
		if err := m.emitBindingExpiryFinding(ctx, tenant, finding); err != nil {
			publishErrs = append(publishErrs, fmt.Errorf("sourcescope: publish binding expiry: %w", err))
		}
	}
	return report, errors.Join(publishErrs...)
}

func appendBindingExpiryAudit(ctx context.Context, sc store.Scope, binding bindingDTO) error {
	event, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor: "system:access-expiry", ActorKind: model.ActorSystem,
		Action: "sourcescope.binding.expire", TargetKind: bindingKind, TargetID: model.ID(binding.ID),
		Meta: map[string]any{
			"source_type": binding.SourceType, "source_ref": binding.SourceRef,
			"scope_tree": binding.ScopeTree, "scope_ref": binding.ScopeRef, "expires_at": binding.ExpiresAt,
		},
	})
	if err != nil {
		return err
	}
	if event.Seq == 0 {
		return fmt.Errorf("sourcescope: expiry audit was not sealed for binding %s", binding.ID)
	}
	return nil
}

func (m *Module) emitBindingExpiryFinding(ctx context.Context, tenant model.TenantID, finding bindingExpiryFinding) error {
	host := m.host0()
	if host == nil {
		return nil
	}
	sum := sha256.Sum256([]byte(finding.kind + "|" + finding.id))
	report := sdkmodel.FindingReport{
		Kind: finding.kind, Severity: sdkmodel.SeverityMedium, SubjectKind: "source_binding", SubjectRef: finding.id,
		Title: finding.title, DetailHash: hex.EncodeToString(sum[:]), OccurredAt: m.clock.Now().Time(),
	}
	if err := host.Publish(ctx, event.FromObservation(tenant.String(), Name, report)); err != nil {
		if m.log != nil {
			m.log.Debug("sourcescope: emit expiry finding failed", "err", err)
		}
		return err
	}
	return nil
}

// handleSweepBindings emits due reminders and records lapsed bindings in the
// audit ledger. Repeated sweeps are idempotent.
func (m *Module) handleSweepBindings(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	report, err := m.SweepExpiredBindings(r.Context(), mc.Tenant)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// resolveBindingExpiry applies the tenant grant_expiry policy to an allow. A
// forbid is a restriction: a policy default would make it lift itself and a
// maximum would refuse it, so a forbid keeps only the expiry its writer sent.
func (m *Module) resolveBindingExpiry(ctx context.Context, sc store.Scope, b *bindingDTO) (string, error) {
	now := m.clock.Now().Time()
	if normalizeEffect(b.Effect) == effectForbid {
		return auth.ResolveGrantExpiry(b.ExpiresAt, now, auth.GrantExpiryPolicy{})
	}
	return auth.ResolveGrantExpiryForTenant(ctx, sc, b.ExpiresAt, now)
}

// expiryFailure answers a client error for a refused expiry and passes a failure to read
// the tenant policy (a store or configuration fault) through as itself.
func expiryFailure(err error) error {
	var refused *auth.GrantExpiryRequestError
	if errors.As(err, &refused) {
		return validationError(err.Error())
	}
	return err
}
