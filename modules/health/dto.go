// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package health

import (
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// statusDTO projects a check row as the health status of its subject (GET /status,
// /checks, and the live SSE frame). state is the current health; the *_at/latency
// fields are the last observation. No payload field by construction.
type statusDTO struct {
	ID                      string `json:"id"`
	Name                    string `json:"name,omitempty"`
	SubjectKind             string `json:"subject_kind"`
	SubjectRef              string `json:"subject_ref"`
	State                   string `json:"state"`
	DesiredStatus           string `json:"desired_status"`
	ExpectedIntervalSeconds int64  `json:"expected_interval_seconds"`
	GraceFactor             int64  `json:"grace_factor"`
	SLATargetPPM            int64  `json:"sla_target_ppm"`
	SLABreachOpen           bool   `json:"sla_breach_open"`
	OwnerActor              string `json:"owner_actor,omitempty"`
	LastCheckedAt           string `json:"last_checked_at,omitempty"`
	LastSeenAt              string `json:"last_seen_at,omitempty"`
	LastLatencyMS           int64  `json:"last_latency_ms"`
	LastDetailHash          string `json:"last_detail_hash,omitempty"`
	CreatedAt               string `json:"created_at,omitempty"`
}

// stateOr returns s, or "unknown" when the stored state is empty (a check that has
// never had a signal).
func stateOr(s string) string {
	if s == "" {
		return stateUnknown
	}
	return s
}

// toStatusDTO projects a check record.
func toStatusDTO(rec model.Record) statusDTO {
	return statusDTO{
		ID:                      rec.String(model.ColID),
		Name:                    rec.String(colName),
		SubjectKind:             rec.String(colSubjectKind),
		SubjectRef:              rec.String(colSubjectRef),
		State:                   stateOr(rec.String(colLastState)),
		DesiredStatus:           rec.String(colDesiredStat),
		ExpectedIntervalSeconds: rec.Int(colExpectedIvl),
		GraceFactor:             rec.Int(colGraceFactor),
		SLATargetPPM:            rec.Int(colSLATargetPM),
		SLABreachOpen:           rec.Bool(colSLABreachOpen),
		OwnerActor:              rec.String(colOwnerActor),
		LastCheckedAt:           rec.String(colLastChecked),
		LastSeenAt:              rec.String(colLastSeenAt),
		LastLatencyMS:           rec.Int(colLastLatency),
		LastDetailHash:          rec.String(colLastDetailHash),
		CreatedAt:               rec.String(model.ColCreatedAt),
	}
}

// incidentDTO projects one incident lifecycle row.
type incidentDTO struct {
	ID          string `json:"id"`
	SubjectKind string `json:"subject_kind"`
	SubjectRef  string `json:"subject_ref"`
	CheckRef    string `json:"check_ref,omitempty"`
	Kind        string `json:"kind"`
	Severity    string `json:"severity"`
	State       string `json:"state"`
	OpenedAt    string `json:"opened_at"`
	ResolvedAt  string `json:"resolved_at,omitempty"`
	Summary     string `json:"summary,omitempty"`
}

func toIncidentDTO(rec model.Record) incidentDTO {
	return incidentDTO{
		ID:          rec.String(model.ColID),
		SubjectKind: rec.String(colInSubjectKind),
		SubjectRef:  rec.String(colInSubjectRef),
		CheckRef:    rec.String(colInCheckRef),
		Kind:        rec.String(colInKind),
		Severity:    rec.String(colInSeverity),
		State:       rec.String(colInState),
		OpenedAt:    rec.String(colInOpenedAt),
		ResolvedAt:  rec.String(colInResolvedAt),
		Summary:     rec.String(colInSummary),
	}
}

// eventDTO projects one append-only reliability transition row.
type eventDTO struct {
	ID          string `json:"id"`
	SubjectKind string `json:"subject_kind"`
	SubjectRef  string `json:"subject_ref"`
	CheckRef    string `json:"check_ref,omitempty"`
	State       string `json:"state"`
	PrevState   string `json:"prev_state"`
	Cause       string `json:"cause"`
	LatencyMS   int64  `json:"latency_ms"`
	OccurredAt  string `json:"occurred_at"`
}

func toEventDTO(rec model.Record) eventDTO {
	return eventDTO{
		ID:          rec.String(model.ColID),
		SubjectKind: rec.String(colEvSubjectKind),
		SubjectRef:  rec.String(colEvSubjectRef),
		CheckRef:    rec.String(colEvCheckRef),
		State:       rec.String(colEvState),
		PrevState:   rec.String(colEvPrevState),
		Cause:       rec.String(colEvCause),
		LatencyMS:   rec.Int(colEvLatency),
		OccurredAt:  rec.String(colEvOccurredAt),
	}
}

// dependencyDTO projects one dependency-map edge (React-Flow source/target are the
// from/to refs).
type dependencyDTO struct {
	ID            string `json:"id"`
	Source        string `json:"source"` // from_ref
	Target        string `json:"target"` // to_ref
	FromKind      string `json:"from_kind"`
	ToKind        string `json:"to_kind"`
	Relation      string `json:"relation"`
	ObservedCount int64  `json:"observed_count"`
	FirstSeenAt   string `json:"first_seen_at"`
	LastSeenAt    string `json:"last_seen_at"`
}

func toDependencyDTO(rec model.Record) dependencyDTO {
	return dependencyDTO{
		ID:            rec.String(model.ColID),
		Source:        rec.String(colDepFromRef),
		Target:        rec.String(colDepToRef),
		FromKind:      rec.String(colDepFromKind),
		ToKind:        rec.String(colDepToKind),
		Relation:      rec.String(colDepRelation),
		ObservedCount: rec.Int(colDepObserved),
		FirstSeenAt:   rec.String(colDepFirstAt),
		LastSeenAt:    rec.String(colDepLastAt),
	}
}

// depNodeDTO is one node in the dependency map. Health is the subject's current
// state when a declared check tracks it; "observed" when an edge proves it alive
// but no check is declared (seen alive, health not measured — never fabricated as
// "healthy"); or "unknown" when it is only named with no liveness evidence.
type depNodeDTO struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Ref    string `json:"ref"`
	Health string `json:"health"`
}

// depGraphResponse is the React-Flow data contract for the dependency map.
type depGraphResponse struct {
	Nodes   []depNodeDTO    `json:"nodes"`
	Edges   []dependencyDTO `json:"edges"`
	Cursor  string          `json:"cursor,omitempty"`
	HasMore bool            `json:"has_more"`
}

// slaDTO is the reliability/SLA report for a subject over a trailing window. Uptime
// is computed over observed_seconds (the span actually covered by history), not the
// full window — has_data is false when there is no history, in which case uptime is
// undefined (not 0%) and breaching is always false.
type slaDTO struct {
	SubjectKind     string  `json:"subject_kind"`
	SubjectRef      string  `json:"subject_ref"`
	WindowSeconds   int64   `json:"window_seconds"`
	ObservedSeconds int64   `json:"observed_seconds"`
	HasData         bool    `json:"has_data"`
	UptimePPM       int64   `json:"uptime_ppm"`
	UptimePercent   float64 `json:"uptime_percent"`
	DowntimeSeconds int64   `json:"downtime_seconds"`
	DegradedSeconds int64   `json:"degraded_seconds"`
	CurrentState    string  `json:"current_state"`
	HasCheck        bool    `json:"has_check"`
	SLATargetPPM    int64   `json:"sla_target_ppm"`
	Breaching       bool    `json:"breaching"`
}

// toSLADTO assembles the reliability report, folding in the check's SLA target and
// breach evaluation when a check exists for the subject. Breach is judged only when
// there is observed history.
func toSLADTO(subjectKind, subjectRef string, rel reliability, window time.Duration, check model.Record, hasCheck bool) slaDTO {
	dto := slaDTO{
		SubjectKind:     subjectKind,
		SubjectRef:      subjectRef,
		WindowSeconds:   int64(window.Seconds()),
		ObservedSeconds: int64(rel.observedSeconds),
		HasData:         rel.observedSeconds > 0,
		UptimePPM:       rel.uptimePPM,
		UptimePercent:   float64(rel.uptimePPM) / float64(ppmFull) * 100,
		DowntimeSeconds: int64(rel.downSeconds),
		DegradedSeconds: int64(rel.degradedSeconds),
		CurrentState:    rel.currentState,
		HasCheck:        hasCheck,
	}
	if hasCheck {
		dto.SLATargetPPM = check.Int(colSLATargetPM)
		dto.CurrentState = stateOr(check.String(colLastState))
		if dto.SLATargetPPM > 0 {
			dto.Breaching = dto.HasData && rel.uptimePPM < dto.SLATargetPPM
		}
	}
	return dto
}
