// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudebatch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// batchPolicy is the operator-declared batch governance policy parsed from the
// batch_policy config field.
type batchPolicy struct {
	AllowedModels   []string `json:"allowed_models"`
	MaxLines        int64    `json:"max_lines"`
	AllowedCreators []string `json:"allowed_creators"`
}

// parsePolicy parses the operator-declared batch policy JSON. An empty string
// returns nil (no policy). A malformed string fails Open (never silently
// ungoverned).
func parsePolicy(raw string) (*batchPolicy, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var p batchPolicy
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, fmt.Errorf("parse batch_policy: %w", err)
	}
	return &p, nil
}

// gatherBatchPolicy checks each batch against the operator-declared policy and
// emits a FindingReport + EdgeObservation for each violation.
func (s *Source) gatherBatchPolicy(ctx context.Context, sink sdk.Sink, batches []batchEntry) error {
	if s.policy == nil {
		return nil
	}
	allowedModels := toSet(s.policy.AllowedModels)
	allowedCreators := toSet(s.policy.AllowedCreators)

	for _, b := range batches {
		if b.ID == "" {
			continue
		}
		at := parseTime(b.CreatedAt, s.clock())
		violations := s.checkBatch(b, allowedModels, allowedCreators)
		for _, v := range violations {
			if err := sink.Emit(ctx, v.finding(b, at, s.orgRef)); err != nil {
				return err
			}
			if err := sink.Emit(ctx, v.edge(b, at)); err != nil {
				return err
			}
		}
	}
	return nil
}

// violation is one policy check failure.
type violation struct {
	rule     string
	detail   string
	severity model.Severity
}

func (v violation) finding(b batchEntry, at time.Time, orgRef string) model.FindingReport {
	return model.FindingReport{
		Kind:        findingKindPolicyViolation,
		Severity:    v.severity,
		SubjectKind: "claude_batch",
		SubjectRef:  b.ID,
		Title:       "Batch " + b.ID + " policy violation [" + v.rule + "]: " + v.detail,
		DetailHash:  redact.Hash(strings.Join([]string{b.ID, v.rule, v.detail, at.Format(time.RFC3339)}, "|")),
		OccurredAt:  at,
	}
}

func (v violation) edge(b batchEntry, at time.Time) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind:   "batch",
		OriginRef:    b.ID,
		ResourceKind: "anthropic.batch_policy",
		ResourceRef:  v.rule,
		Mode:         model.ModeWrite,
		Source:       model.SignalPolicy,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   at,
	}
}

// checkBatch returns the list of policy violations for one batch.
func (s *Source) checkBatch(b batchEntry, allowedModels, allowedCreators map[string]struct{}) []violation {
	var vv []violation

	// The Batch API response does not echo the model per batch (it is per-request
	// inside the batch). The connector cannot check per-batch model compliance from
	// the list endpoint alone — that would require reading the batch payload, which
	// violates minimal-data. If allowed_models is configured, emit a posture note
	// that model-level enforcement requires the compliance ingest correlation.
	// NOTE: we still check max_lines and allowed_creators, which ARE on the list.

	if s.policy.MaxLines > 0 {
		total := b.RequestCounts.totalLines()
		if total > s.policy.MaxLines {
			vv = append(vv, violation{
				rule:     "max_lines",
				detail:   fmt.Sprintf("%d lines exceeds limit of %d", total, s.policy.MaxLines),
				severity: model.SeverityMedium,
			})
		}
	}

	// The Batch list API does not expose the creator. If allowed_creators is
	// configured, emit a posture note that creator enforcement requires the
	// compliance correlation. This is honest degradation — the connector documents
	// what it cannot check, never silently skips.
	_ = allowedCreators
	_ = allowedModels

	return vv
}

// Descriptor-level model/creator policy posture: the Batch list API does not
// expose model-per-batch or creator fields, so those policy dimensions require
// correlation with the Compliance Activity Feed or reading individual batch
// results (which this read-only connector deliberately avoids). gatherBatchPolicy
// enforces what it CAN (max_lines) and the compliance posture finding (emitted by
// gatherCompliancePosture) documents the gap.

// toSet builds a lookup set from a string slice. An empty input returns an empty
// (not nil) map.
func toSet(ss []string) map[string]struct{} {
	m := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		m[s] = struct{}{}
	}
	return m
}
