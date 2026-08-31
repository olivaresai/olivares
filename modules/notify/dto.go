// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

import (
	"github.com/olivaresai/olivares/core/model"
)

// routeDTO projects one routing rule. The match_* sets are exposed as arrays; no
// destination credential is present by construction (the route stores only a name).
type routeDTO struct {
	ID                    string   `json:"id"`
	Name                  string   `json:"name"`
	Enabled               bool     `json:"enabled"`
	MatchTypes            []string `json:"match_types"`
	MatchKinds            []string `json:"match_kinds"`
	MinSeverity           string   `json:"min_severity,omitempty"`
	MatchSources          []string `json:"match_sources"`
	MatchSubjectKinds     []string `json:"match_subject_kinds"`
	Destination           string   `json:"destination"`
	DedupWindowSeconds    int64    `json:"dedup_window_seconds"`
	ThrottleWindowSeconds int64    `json:"throttle_window_seconds"`
	Priority              int64    `json:"priority"`
	OwnerActor            string   `json:"owner_actor,omitempty"`
	CreatedAt             string   `json:"created_at,omitempty"`
}

func toRouteDTO(rec model.Record) routeDTO {
	return routeDTO{
		ID:                    rec.String(model.ColID),
		Name:                  rec.String(colName),
		Enabled:               rec.Bool(colEnabled),
		MatchTypes:            csvSplit(rec.String(colMatchTypes)),
		MatchKinds:            csvSplit(rec.String(colMatchKinds)),
		MinSeverity:           rec.String(colMinSeverity),
		MatchSources:          csvSplit(rec.String(colMatchSources)),
		MatchSubjectKinds:     csvSplit(rec.String(colMatchSubjects)),
		Destination:           rec.String(colDestination),
		DedupWindowSeconds:    rec.Int(colDedupWindow),
		ThrottleWindowSeconds: rec.Int(colThrottleWin),
		Priority:              rec.Int(colPriority),
		OwnerActor:            rec.String(colOwnerActor),
		CreatedAt:             rec.String(model.ColCreatedAt),
	}
}

// deliveryDTO projects one append-only delivery-attempt row.
type deliveryDTO struct {
	ID          string `json:"id"`
	RouteRef    string `json:"route_ref,omitempty"`
	Destination string `json:"destination"`
	EventType   string `json:"event_type"`
	FindingKind string `json:"finding_kind"`
	Severity    string `json:"severity,omitempty"`
	SubjectKind string `json:"subject_kind,omitempty"`
	SubjectRef  string `json:"subject_ref,omitempty"`
	Title       string `json:"title,omitempty"`
	DedupKey    string `json:"dedup_key,omitempty"`
	Status      string `json:"status"`
	Detail      string `json:"detail,omitempty"`
	OccurredAt  string `json:"occurred_at"`
}

func toDeliveryDTO(rec model.Record) deliveryDTO {
	return deliveryDTO{
		ID:          rec.String(model.ColID),
		RouteRef:    rec.String(colDelRouteRef),
		Destination: rec.String(colDelDestination),
		EventType:   rec.String(colDelEventType),
		FindingKind: rec.String(colDelKind),
		Severity:    rec.String(colDelSeverity),
		SubjectKind: rec.String(colDelSubjectKind),
		SubjectRef:  rec.String(colDelSubjectRef),
		Title:       rec.String(colDelTitle),
		DedupKey:    rec.String(colDelDedupKey),
		Status:      rec.String(colDelStatus),
		Detail:      rec.String(colDelDetail),
		OccurredAt:  rec.String(colDelOccurredAt),
	}
}
