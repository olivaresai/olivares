// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeprojects

import (
	"context"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// ArtifactState is the lifecycle state of a tracked artifact.
type ArtifactState string

const (
	ArtifactCreated  ArtifactState = "created"
	ArtifactShared   ArtifactState = "shared"
	ArtifactArchived ArtifactState = "archived"
)

// ArtifactEvent is one lifecycle event for an artifact, derived from Compliance API
// activity records. The connector does not fabricate lifecycle events — it derives
// them from the activity types the Compliance API actually reports.
type ArtifactEvent struct {
	ArtifactRef string
	ProjectRef  string
	EventType   string
	State       ArtifactState
	OccurredAt  time.Time
}

// artifactActivityPrefixes maps Compliance API activity type prefixes to artifact
// lifecycle states. The Compliance API does not have dedicated artifact endpoints;
// artifact lifecycle is inferred from project-related activity types.
var artifactActivityPrefixes = map[string]ArtifactState{
	"artifact_created":  ArtifactCreated,
	"artifact_shared":   ArtifactShared,
	"artifact_archived": ArtifactArchived,
	"artifact_updated":  ArtifactCreated,
	"file_uploaded":     ArtifactCreated,
	"file_shared":       ArtifactShared,
	"file_deleted":      ArtifactArchived,
}

// ClassifyArtifactEvent maps a Compliance API activity type to an artifact lifecycle
// state. Returns empty string if the activity is not artifact-related.
func ClassifyArtifactEvent(activityType string) ArtifactState {
	t := strings.ToLower(strings.TrimSpace(activityType))
	for prefix, state := range artifactActivityPrefixes {
		if strings.Contains(t, prefix) {
			return state
		}
	}
	return ""
}

// ArtifactRetentionPolicy configures the retention rules for artifacts.
type ArtifactRetentionPolicy struct {
	MaxRetentionDays      int  `json:"max_retention_days,omitempty"`
	AutoArchiveDays       int  `json:"auto_archive_days,omitempty"`
	RequireArchiveOnShare bool `json:"require_archive_on_share,omitempty"`
}

// EmitArtifactLifecycleFinding emits a governance finding for an artifact lifecycle
// event. It is called by the composition root when it correlates a Compliance API
// activity event with an artifact (the connector itself does not poll the Compliance
// API — that is claude-compliance's job; this provides the governance evaluation).
func EmitArtifactLifecycleFinding(ctx context.Context, sink sdk.Sink, ev ArtifactEvent, retention *ArtifactRetentionPolicy) error {
	now := ev.OccurredAt
	if now.IsZero() {
		now = time.Now().UTC()
	}

	if err := sink.Emit(ctx, model.FindingReport{
		Kind:        "artifact_lifecycle",
		Severity:    model.SeverityInfo,
		SubjectKind: resArtifact,
		SubjectRef:  ev.ArtifactRef,
		Title:       "Artifact lifecycle event: " + string(ev.State) + " (project " + sanitizeName(ev.ProjectRef) + ")",
		DetailHash:  redact.Hash(ev.ArtifactRef + "|" + string(ev.State) + "|" + ev.ProjectRef + "|" + ev.EventType),
		OccurredAt:  now,
	}); err != nil {
		return err
	}

	if err := sink.Emit(ctx, model.EdgeObservation{
		OriginKind:   resProject,
		OriginRef:    ev.ProjectRef,
		ResourceKind: resArtifact,
		ResourceRef:  ev.ArtifactRef,
		Mode:         model.ModeReadWrite,
		Source:       model.SignalPolicy,
		Confidence:   model.ConfidenceAttributed,
		ToolRef:      string(ev.State),
		ObservedAt:   now,
	}); err != nil {
		return err
	}

	if retention != nil {
		return evaluateArtifactRetention(ctx, sink, ev, retention, now)
	}
	return nil
}

func evaluateArtifactRetention(ctx context.Context, sink sdk.Sink, ev ArtifactEvent, ret *ArtifactRetentionPolicy, now time.Time) error {
	if ret.RequireArchiveOnShare && ev.State == ArtifactShared {
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "policy_violation",
			Severity:    model.SeverityMedium,
			SubjectKind: resArtifact,
			SubjectRef:  ev.ArtifactRef,
			Title:       "Artifact shared without prior archival (policy: require_archive_on_share)",
			DetailHash:  redact.Hash(ev.ArtifactRef + "|shared-without-archive|" + ev.ProjectRef),
			OccurredAt:  now,
		}); err != nil {
			return err
		}
	}
	return nil
}
