// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"errors"
	"sort"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// reportsource.go exposes minimal-data, read-only accessors over the compliance
// module's OWN ext entities (NIS2 incidents, RTBF erasure requests + receipts,
// retention policies + the governor) for the enterprise reporting
// engine. Like ActiveLegalHolds these are additive and read-only — the open
// binary behaves identically whether or not they are called — and they keep the
// schema single-owner: the composition root's reporting adapters consume THESE
// typed views instead of reaching into the module's private kind strings.

// ReportIncident is a minimal-data view of one persisted NIS 2 significant-
// incident record (compliance.nis2_incident). It carries no incident content —
// only the structural reference, the reporting phase and the classification
// verdict — safe for a governance report.
type ReportIncident struct {
	ID           string `json:"id"`
	Reference    string `json:"reference,omitempty"`
	Phase        string `json:"phase"` // early_warning | notification | intermediate | final
	Significant  bool   `json:"significant"`
	ClassifiedAt string `json:"classified_at,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}

// ReportIncidents lists the tenant's persisted NIS 2 significant-incident records
// (minimal data). Read-only and additive.
func (m *Module) ReportIncidents(ctx context.Context, tenant model.TenantID) ([]ReportIncident, error) {
	if m.data == nil {
		return nil, errors.New("compliance: no data handle; cannot read incidents")
	}
	var out []ReportIncident
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(nis2IncidentKind)
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
		for _, rec := range recs {
			out = append(out, ReportIncident{
				ID:           rec.String(model.ColID),
				Reference:    rec.String(colNIReference),
				Phase:        rec.String(colNIPhase),
				Significant:  rec.Bool(colNISignificant),
				ClassifiedAt: rec.String(colNIClassifiedAt),
				CreatedAt:    rec.String(model.ColCreatedAt),
			})
		}
		return nil
	})
	return out, err
}

// ReportRTBFRequest is a minimal-data view of one in-flight RTBF erasure request
// (not yet completed). No subject identifier — only the kind, the case reference
// and the lifecycle status.
type ReportRTBFRequest struct {
	ID          string `json:"id"`
	SubjectKind string `json:"subject_kind"`
	CaseRef     string `json:"case_ref,omitempty"`
	Status      string `json:"status"`
	RequestedAt string `json:"requested_at,omitempty"`
}

// ReportShred is a minimal-data view of one completed crypto-shred receipt.
type ReportShred struct {
	ID          string `json:"id"`
	SubjectKind string `json:"subject_kind"`
	ShreddedAt  string `json:"shredded_at,omitempty"`
	Verified    bool   `json:"verified"`
}

// ReportRTBF returns the tenant's in-flight erasure requests and completed
// crypto-shred receipts (minimal data) for the reporting RTBF section.
// "In-flight" is any request whose status is not a terminal completed/denied
// state; a receipt row means the key was shredded (verify_ok records whether the
// post-shred verification passed).
func (m *Module) ReportRTBF(ctx context.Context, tenant model.TenantID) ([]ReportRTBFRequest, []ReportShred, error) {
	if m.data == nil {
		return nil, nil, errors.New("compliance: no data handle; cannot read RTBF state")
	}
	var pending []ReportRTBFRequest
	var shreds []ReportShred
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		reqRepo, err := sc.Ext(erasureRequestKind)
		if err != nil {
			if !errors.Is(err, store.ErrUnknownEntity) {
				return err
			}
		} else {
			recs, err := listAll(ctx, reqRepo)
			if err != nil {
				return err
			}
			for _, rec := range recs {
				status := rec.String(colERStatus)
				if isTerminalErasureStatus(status) {
					continue
				}
				pending = append(pending, ReportRTBFRequest{
					ID:          rec.String(model.ColID),
					SubjectKind: rec.String(colERSubjectKind),
					CaseRef:     rec.String(colCaseRef),
					Status:      status,
					RequestedAt: rec.String(model.ColCreatedAt),
				})
			}
		}

		rcptRepo, err := sc.Ext(erasureReceiptKind)
		if err != nil {
			if errors.Is(err, store.ErrUnknownEntity) {
				return nil
			}
			return err
		}
		recs, err := listAll(ctx, rcptRepo)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			if !rec.Bool(colRCShredded) {
				continue
			}
			shreds = append(shreds, ReportShred{
				ID:          rec.String(model.ColID),
				SubjectKind: rec.String(colRCSubject),
				ShreddedAt:  rec.String(model.ColCreatedAt),
				Verified:    rec.Bool(colRCVerifyOK),
			})
		}
		return nil
	})
	return pending, shreds, err
}

// isTerminalErasureStatus reports whether an erasure request has reached a state
// where it is no longer in flight (completed, completed-with-gaps, or denied).
func isTerminalErasureStatus(status string) bool {
	switch status {
	case erasureStatusCompleted, erasureStatusGaps, erasureStatusDenied:
		return true
	default:
		return false
	}
}

// ReportRetentionStatus is a minimal-data aggregate of the tenant's retention
// posture for the reporting retention section.
type ReportRetentionStatus struct {
	// TotalClasses is the number of configured retention policies.
	TotalClasses int `json:"total_classes"`
	// BelowFloor counts policies whose retention_days is shorter than the
	// regulatory floor in force for their class (0 when no governor is wired).
	BelowFloor int `json:"below_floor"`
	// PendingPurge counts enabled policies with a purge disposition.
	PendingPurge int `json:"pending_purge"`
	// ComplianceLocked reports that an regulatory floor governor is wired
	// (retention floors are enforced and cannot be freely relaxed).
	ComplianceLocked bool `json:"compliance_locked"`
	// LastSweepAt is the created_at of the most recent retention run (empty when
	// no sweep has run).
	LastSweepAt string `json:"last_sweep_at,omitempty"`
}

// ReportRetention aggregates the tenant's retention policies against the
// governor floor (when wired) and the most recent sweep, for the reporting
// retention section. Read-only and additive.
func (m *Module) ReportRetention(ctx context.Context, tenant model.TenantID) (ReportRetentionStatus, error) {
	if m.data == nil {
		return ReportRetentionStatus{}, errors.New("compliance: no data handle; cannot read retention")
	}
	out := ReportRetentionStatus{ComplianceLocked: m.governor != nil}
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		polRepo, err := sc.Ext(retentionPolicyKind)
		if err != nil {
			if !errors.Is(err, store.ErrUnknownEntity) {
				return err
			}
		} else {
			recs, err := listAll(ctx, polRepo)
			if err != nil {
				return err
			}
			out.TotalClasses = len(recs)
			for _, rec := range recs {
				if rec.Bool(colRPEnabled) && rec.String(colRPDisposition) == dispositionPurge {
					out.PendingPurge++
				}
				if floor, ok := m.floorFor248(ctx, tenant, rec.String(colDataClass)); ok {
					if rec.Int(colRPDays) < int64(floor.MinDays) {
						out.BelowFloor++
					}
				}
			}
		}

		runRepo, err := sc.Ext(retentionRunKind)
		if err != nil {
			if errors.Is(err, store.ErrUnknownEntity) {
				return nil
			}
			return err
		}
		runs, err := listAll(ctx, runRepo)
		if err != nil {
			return err
		}
		var latest string
		for _, rec := range runs {
			if at := rec.String(model.ColCreatedAt); at > latest {
				latest = at
			}
		}
		out.LastSweepAt = latest
		return nil
	})
	return out, err
}

// sortReportIncidents orders incidents newest-first by created_at (stable).
// Exposed for the reporting adapter's deterministic output.
func sortReportIncidents(in []ReportIncident) {
	sort.SliceStable(in, func(i, j int) bool { return in[i].CreatedAt > in[j].CreatedAt })
}
