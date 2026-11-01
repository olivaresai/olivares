// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// GrantExpiryReport counts one idempotent grant-expiry pass.
type GrantExpiryReport struct {
	Expired   int `json:"grants_expired"`
	Reminders int `json:"grant_reminders"`
}

type grantExpiryFinding struct {
	kind string
	id   string
}

func grantExpiryReloadRequired(state scopedTenantState, loaded, projectionChanged bool) bool {
	return projectionChanged || !loaded || !state.available
}

// SweepGrantExpiry removes lapsed grants from the managed Cedar projection and
// sends one-shot 14-day and 1-day reminders through the existing finding rail.
// Reminders run in their own transaction: a notify outage retries them and never
// holds back a revocation.
func (m *Module) SweepGrantExpiry(ctx context.Context, tenant model.TenantID) (GrantExpiryReport, error) {
	report, expireErr := m.expireGrants(ctx, tenant)
	reminders, remindErr := m.remindGrants(ctx, tenant)
	report.Reminders = reminders
	return report, errors.Join(expireErr, remindErr)
}

func (m *Module) expireGrants(ctx context.Context, tenant model.TenantID) (GrantExpiryReport, error) {
	var report GrantExpiryReport
	var expiryFindings []grantExpiryFinding
	projectionChanged := false
	if m.data == nil {
		return report, errors.New("governance: grant expiry requires module data")
	}
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		now := m.clock.Now().Time()
		if err := lockPolicyAuthorizationEpoch(ctx, sc); err != nil {
			return err
		}
		grants, err := loadAllScopedGrants(ctx, sc)
		if err != nil {
			return err
		}
		roles, err := loadCustomRoles(ctx, sc)
		if err != nil {
			return err
		}
		groups, err := loadPermGroups(ctx, sc)
		if err != nil {
			return err
		}
		state := managedProjectionState{roles: roles, groups: groups, grants: make([]scopedGrant, 0, len(grants))}
		for _, grant := range grants {
			if grantCurrent(grant, now) {
				state.grants = append(state.grants, grant)
			}
		}
		plan, err := planManagedProjection(ctx, sc, state)
		if err != nil {
			return err
		}
		projectionChanged = plan.changed
		if plan.changed {
			if err := advancePolicyAuthorizationEpoch(ctx, sc); err != nil {
				return err
			}
		}
		repo, err := sc.Ext(scopedGrantKind)
		if err != nil {
			return err
		}
		for _, grant := range grants {
			if grantCurrent(grant, now) {
				continue
			}
			if err := repo.Delete(ctx, grant.ID); err != nil {
				return err
			}
			if err := appendGrantExpiryAudit(ctx, sc, grant); err != nil {
				return err
			}
			report.Expired++
			expiryFindings = append(expiryFindings, grantExpiryFinding{kind: "governance_scoped_grant_expired", id: grant.ID.String()})
		}
		if err := appendManagedProjection(ctx, sc, plan, "system:access-expiry"); err != nil {
			return err
		}
		return persistManagedProjectionFreshness(ctx, sc, plan)
	})
	if err != nil {
		return report, err
	}
	// The rows are gone and audited: a failed reload or publish below is reported,
	// but never hides the other, so the findings still go out when the reload fails.
	var errs []error
	if m.grants != nil {
		state, loaded := m.grants.tenantState(tenant)
		if grantExpiryReloadRequired(state, loaded, projectionChanged) {
			if err := m.reloadTenantGrants(ctx, tenant); err != nil {
				errs = append(errs, fmt.Errorf("governance: reload expired grants: %w", err))
			}
		}
	}
	for _, finding := range expiryFindings {
		if err := m.emitGrantExpiryFinding(ctx, tenant, finding); err != nil {
			errs = append(errs, fmt.Errorf("governance: publish grant expiry: %w", err))
		}
	}
	return report, errors.Join(errs...)
}

// remindGrants sends the one-shot reminders of grants still in force. The publish
// stays inside the transaction on purpose: a failed publish rolls the markers
// back, so the next sweep sends the reminders again.
func (m *Module) remindGrants(ctx context.Context, tenant model.TenantID) (int, error) {
	reminders := 0
	if m.data == nil {
		return 0, errors.New("governance: grant reminders require module data")
	}
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		now := m.clock.Now().Time()
		grants, err := loadAllScopedGrants(ctx, sc)
		if err != nil {
			return err
		}
		repo, err := sc.Ext(scopedGrantKind)
		if err != nil {
			return err
		}
		for _, grant := range grants {
			if grant.ExpiresAt == "" || !grantCurrent(grant, now) {
				continue
			}
			expires, perr := time.Parse(time.RFC3339Nano, grant.ExpiresAt)
			if perr != nil {
				return fmt.Errorf("governance: grant %s has an invalid expiry: %w", grant.ID, perr)
			}
			rec, err := repo.Get(ctx, grant.ID)
			if err != nil {
				return err
			}
			var findings []grantExpiryFinding
			for _, reminder := range []struct {
				kind   string
				column string
				marker string
				window time.Duration
			}{
				{kind: "governance_scoped_grant_expiring_14", column: colSGRemind14At, marker: grant.Remind14At, window: 14 * 24 * time.Hour},
				{kind: "governance_scoped_grant_expiring_1", column: colSGRemind1At, marker: grant.Remind1At, window: 24 * time.Hour},
			} {
				if reminder.marker != "" || expires.After(now.Add(reminder.window)) {
					continue
				}
				if reminder.column == colSGRemind14At && !expires.After(now.Add(24*time.Hour)) {
					continue
				}
				rec[reminder.column] = m.clock.Now().String()
				findings = append(findings, grantExpiryFinding{kind: reminder.kind, id: grant.ID.String()})
			}
			if len(findings) == 0 {
				continue
			}
			if _, err := repo.Update(ctx, rec); err != nil {
				return err
			}
			for _, finding := range findings {
				if err := m.emitGrantExpiryFinding(ctx, tenant, finding); err != nil {
					return fmt.Errorf("governance: publish grant reminder: %w", err)
				}
				reminders++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return reminders, nil
}

func appendGrantExpiryAudit(ctx context.Context, sc store.Scope, grant scopedGrant) error {
	event, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor: "system:access-expiry", ActorKind: model.ActorSystem,
		Action: "governance.scoped_grant.expire", TargetKind: scopedGrantKind, TargetID: grant.ID,
		Meta: map[string]any{
			"subject_kind": grant.SubjectKind, "subject_ref": grant.SubjectRef, "role": grant.Role,
			"scope_tree": grant.Scope.Tree, "scope_ref": grant.Scope.Ref, "expires_at": grant.ExpiresAt,
		},
	})
	if err != nil {
		return err
	}
	if event.Seq == 0 {
		return fmt.Errorf("governance: expiry audit was not sealed for grant %s", grant.ID)
	}
	return nil
}

func (m *Module) emitGrantExpiryFinding(ctx context.Context, tenant model.TenantID, finding grantExpiryFinding) error {
	if m.host == nil {
		return nil
	}
	sum := sha256.Sum256([]byte(finding.kind + "|" + finding.id))
	report := sdkmodel.FindingReport{
		Kind: finding.kind, Severity: sdkmodel.SeverityMedium, SubjectKind: "scoped_grant", SubjectRef: finding.id,
		Title: "Access grant expiry", DetailHash: hex.EncodeToString(sum[:]), OccurredAt: m.clock.Now().Time(),
	}
	if err := m.host.Publish(ctx, event.FromObservation(tenant.String(), Name, report)); err != nil {
		m.debugf("governance: emit grant expiry finding failed", "err", err)
		return err
	}
	return nil
}
