// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package clauderoutines

import (
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	kindRoutine       = "anthropic.routine"
	originWorkspace   = "workspace"
	findingGovernance = "governance"
	findingPosture    = "posture"
	findingSelfAudit  = "self_audit"
	connectorSubject  = "connector.claude_routines"
)

// routineEdge maps a trigger to an inventory EdgeObservation: the organization
// (workspace) owns the routine (trigger). Mode is read — the connector
// observes, it does not act.
func routineEdge(t trigger, orgID string, at time.Time) model.EdgeObservation {
	wsRef := orgID
	if wsRef == "" {
		wsRef = "organization"
	}
	created := parseTime(t.CreatedAt)
	if created.IsZero() {
		created = at
	}
	return model.EdgeObservation{
		OriginKind:   originWorkspace,
		OriginRef:    wsRef,
		ResourceKind: kindRoutine,
		ResourceRef:  redact.Clean(t.ID),
		Mode:         model.ModeRead,
		Source:       model.SignalCMA,
		Confidence:   model.ConfidenceAttributed,
		ToolRef:      routineLabel(t),
		ObservedAt:   created,
	}
}

// cadenceFinding checks whether a cron routine fires more often than the
// operator's policy floor (maxCadenceSeconds). Returns false when no finding
// applies.
func cadenceFinding(t trigger, maxCadenceSeconds int, at time.Time) (model.FindingReport, bool) {
	if !t.Enabled || t.CronExpression == "" {
		return model.FindingReport{}, false
	}
	intervalSec := estimateCronInterval(t.CronExpression)
	if intervalSec <= 0 || intervalSec >= maxCadenceSeconds {
		return model.FindingReport{}, false
	}
	return model.FindingReport{
		Kind:        findingGovernance,
		Severity:    model.SeverityHigh,
		SubjectKind: kindRoutine,
		SubjectRef:  t.ID,
		Title:       fmt.Sprintf("Routine %s fires every %ds, below %ds policy floor", routineLabel(t), intervalSec, maxCadenceSeconds),
		DetailHash:  redact.Hash(fmt.Sprintf("cadence trigger=%s cron=%s interval=%d floor=%d", t.ID, t.CronExpression, intervalSec, maxCadenceSeconds)),
		OccurredAt:  at,
	}, true
}

// reviewFinding checks whether a routine is older than the review window
// (maxDays) and still active. Only enabled routines that have not ended raise
// the finding.
func reviewFinding(t trigger, maxDays int, at time.Time) (model.FindingReport, bool) {
	if !t.Enabled || t.EndedReason != "" {
		return model.FindingReport{}, false
	}
	created := parseTime(t.CreatedAt)
	if created.IsZero() {
		return model.FindingReport{}, false
	}
	age := at.Sub(created)
	threshold := time.Duration(maxDays) * 24 * time.Hour
	if age < threshold {
		return model.FindingReport{}, false
	}
	days := int(age.Hours() / 24)
	return model.FindingReport{
		Kind:        findingGovernance,
		Severity:    model.SeverityMedium,
		SubjectKind: kindRoutine,
		SubjectRef:  t.ID,
		Title:       fmt.Sprintf("Routine %s is %d days old without recorded review (threshold: %d days)", routineLabel(t), days, maxDays),
		DetailHash:  redact.Hash(fmt.Sprintf("review trigger=%s created=%s age_days=%d threshold=%d", t.ID, t.CreatedAt, days, maxDays)),
		OccurredAt:  at,
	}, true
}

// nameFinding reports routines with no human-readable name as a LOW posture
// signal (anonymous automations are harder to audit).
func nameFinding(t trigger, at time.Time) (model.FindingReport, bool) {
	if strings.TrimSpace(t.Name) != "" {
		return model.FindingReport{}, false
	}
	return model.FindingReport{
		Kind:        findingPosture,
		Severity:    model.SeverityLow,
		SubjectKind: kindRoutine,
		SubjectRef:  t.ID,
		Title:       fmt.Sprintf("Routine %s has no human-readable name", t.ID),
		DetailHash:  redact.Hash(fmt.Sprintf("unnamed trigger=%s", t.ID)),
		OccurredAt:  at,
	}, true
}

// routineLabel returns a display-safe reference for the trigger: its name if
// set, otherwise its id. The name is scrubbed (redact.Clean) defensively.
func routineLabel(t trigger) string {
	if n := strings.TrimSpace(redact.Clean(t.Name)); n != "" {
		return n
	}
	return t.ID
}

// parseTime parses an RFC3339 / RFC3339Nano timestamp, returning the zero time
// on failure.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// estimateCronInterval returns a rough estimate of the firing interval in
// seconds for a standard 5-field cron expression. It handles the common
// patterns without bringing a full cron parser dependency — a conservative
// heuristic that returns 0 (no finding) when the pattern is unrecognized.
func estimateCronInterval(expr string) int {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return 0
	}
	minute, hour := fields[0], fields[1]

	// "*/N * * * *" — every N minutes
	if strings.HasPrefix(minute, "*/") {
		n := atoi(minute[2:])
		if n > 0 && hour == "*" {
			return n * 60
		}
	}
	// "0 */N * * *" — every N hours (or a fixed minute with hour divisor)
	if strings.HasPrefix(hour, "*/") {
		n := atoi(hour[2:])
		if n > 0 {
			return n * 3600
		}
	}
	// A fixed minute + "*" hour — every hour
	if hour == "*" && !strings.Contains(minute, "*") && !strings.Contains(minute, "/") && !strings.Contains(minute, ",") {
		return 3600
	}
	return 0
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
