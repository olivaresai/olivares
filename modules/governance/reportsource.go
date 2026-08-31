// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// reportsource.go exposes a minimal-data, read-only aggregate of the NHI
// lifecycle plane (governance.nhi_lifecycle) for the enterprise reporting
// engine's credential-rotation section. It is additive and read-only — the open
// binary behaves identically whether or not it is called — and keeps the schema
// single-owner: the composition root's reporting adapter consumes this typed
// view instead of the module's private kind strings and staleness materialization.

// ReportCredentialRotation is the aggregate rotation posture of the tenant's
// non-human identities, derived from the sweep-materialized staleness columns.
type ReportCredentialRotation struct {
	// TotalCredentials is the number of governed NHIs in the tenant.
	TotalCredentials int `json:"total_credentials"`
	// RotatedLast30d counts NHIs with a known rotation within the last 30 days.
	RotatedLast30d int `json:"rotated_last_30d"`
	// Stale counts NHIs the sweep marked stale (past their rotation window).
	Stale int `json:"stale"`
	// ExpiringIn7d counts NHIs whose escalation deadline (alert → block) falls
	// within the next 7 days — the actionable "rotate soon" set.
	ExpiringIn7d int `json:"expiring_in_7d"`
	// Stale90d counts NHIs that have been continuously stale for over 90 days —
	// the chronic-neglect set.
	Stale90d int `json:"stale_90d"`
	// UnknownRotation counts NHIs whose last rotation is unknown (a coverage gap,
	// never silently counted as fresh).
	UnknownRotation int `json:"unknown_rotation"`
}

// ReportCredentialRotation aggregates the tenant's NHI lifecycle rows into a
// rotation-health summary. Read-only and additive; the sweep is the authority
// for the staleness_status column this reads.
func (m *Module) ReportCredentialRotation(ctx context.Context, tenant model.TenantID) (ReportCredentialRotation, error) {
	if m.data == nil {
		return ReportCredentialRotation{}, errors.New("governance: no data handle; cannot read NHI lifecycle")
	}
	now := m.clock.Now().Time()
	var out ReportCredentialRotation
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(nhiLifecycleKind)
		if err != nil {
			if errors.Is(err, store.ErrUnknownEntity) {
				return nil
			}
			return err
		}
		recs, err := listAll(ctx, repo)
		if err != nil {
			return err
		}
		out.TotalCredentials = len(recs)
		for _, rec := range recs {
			switch rec.String(colNHIStaleStatus) {
			case staleStale:
				out.Stale++
			case staleUnknown, "":
				out.UnknownRotation++
			}
			if rotated, ok := tsValue(rec, colNHIRotatedAt); ok {
				if now.Sub(rotated.Time()).Hours() <= 30*24 {
					out.RotatedLast30d++
				}
			}
			if block, ok := tsValue(rec, colNHIBlockAfter); ok {
				d := block.Time().Sub(now).Hours()
				if d >= 0 && d <= 7*24 {
					out.ExpiringIn7d++
				}
			}
			if since, ok := tsValue(rec, colNHIStaleSince); ok {
				if now.Sub(since.Time()).Hours() > 90*24 {
					out.Stale90d++
				}
			}
		}
		return nil
	})
	return out, err
}
