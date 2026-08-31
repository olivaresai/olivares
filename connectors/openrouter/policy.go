// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openrouter

// policy.go is the OpenRouter APPROVED-MODEL policy: an operator allow/deny list
// over model ids, evaluated both as a BATCH against the live catalog (a denied
// model that is reachable, an approved model that has gone missing) and at the
// POINT OF USE via Meter (evaluate returns the verdict for a single call). It
// inventories and SIGNALS; enforcement is the caller's (a gateway/PEP).

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// PolicyVerdict is the approval outcome for a model under the operator policy.
type PolicyVerdict string

const (
	// VerdictApproved: on the allowlist, or no allowlist configured and not denied.
	VerdictApproved PolicyVerdict = "approved"
	// VerdictDenied: explicitly on the denylist (forbidden).
	VerdictDenied PolicyVerdict = "denied"
	// VerdictUnapproved: an allowlist IS configured and the model is not on it.
	VerdictUnapproved PolicyVerdict = "unapproved"
)

type modelPolicy struct {
	approved map[string]struct{}
	denied   map[string]struct{}
}

func newModelPolicy(approvedCSV, deniedCSV string) modelPolicy {
	return modelPolicy{approved: parseModelSet(approvedCSV), denied: parseModelSet(deniedCSV)}
}

func parseModelSet(csv string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, m := range strings.Split(csv, ",") {
		if m = strings.ToLower(strings.TrimSpace(m)); m != "" {
			out[m] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (p modelPolicy) configured() bool { return p.approved != nil || p.denied != nil }

// evaluate returns the verdict for one model id. Deny wins over allow.
func (p modelPolicy) evaluate(modelID string) PolicyVerdict {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if p.denied != nil {
		if _, ok := p.denied[id]; ok {
			return VerdictDenied
		}
	}
	if p.approved != nil {
		if _, ok := p.approved[id]; !ok {
			return VerdictUnapproved
		}
	}
	return VerdictApproved
}

// gatherPolicy evaluates the configured policy against the live catalog and emits
// the drift findings. No policy configured => nothing emitted (inventory only).
func (s *Source) gatherPolicy(ctx context.Context, sink sdk.Sink) error {
	if !s.policy.configured() {
		return nil
	}
	cat, err := s.Snapshot(ctx)
	if err != nil {
		return err
	}
	at := s.clock().UTC()

	catalog := make(map[string]struct{}, len(cat.Models))
	var deniedReachable []string
	for _, m := range cat.Models {
		id := strings.ToLower(m.Ref)
		catalog[id] = struct{}{}
		if s.policy.denied != nil {
			if _, ok := s.policy.denied[id]; ok {
				deniedReachable = append(deniedReachable, id)
			}
		}
	}
	sort.Strings(deniedReachable)
	for _, id := range deniedReachable {
		safe := redact.Clean(id)
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "posture",
			Severity:    model.SeverityHigh,
			SubjectKind: subjectPolicy,
			SubjectRef:  "denied/" + safe,
			Title:       "OpenRouter denied model is reachable via this key: " + safe,
			DetailHash:  redact.Hash("openrouter denied-model-reachable model=" + id),
			OccurredAt:  at,
		}); err != nil {
			return err
		}
	}

	var approvedMissing []string
	for id := range s.policy.approved {
		if _, ok := catalog[id]; !ok {
			approvedMissing = append(approvedMissing, id)
		}
	}
	sort.Strings(approvedMissing)
	for _, id := range approvedMissing {
		safe := redact.Clean(id)
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "posture",
			Severity:    model.SeverityLow,
			SubjectKind: subjectPolicy,
			SubjectRef:  "approved-missing/" + safe,
			Title:       "OpenRouter approved model is not in the live catalog (stale allowlist): " + safe,
			DetailHash:  redact.Hash("openrouter approved-missing model=" + id),
			OccurredAt:  at,
		}); err != nil {
			return err
		}
	}

	return sink.Emit(ctx, model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectPolicy,
		SubjectRef:  "summary",
		Title: "OpenRouter model policy: " + strconv.Itoa(len(s.policy.approved)) + " approved, " +
			strconv.Itoa(len(s.policy.denied)) + " denied, " + strconv.Itoa(len(deniedReachable)) +
			" denied-reachable, " + strconv.Itoa(len(approvedMissing)) + " approved-missing",
		DetailHash: redact.Hash("openrouter model-policy summary catalog=" + strconv.Itoa(len(cat.Models))),
		OccurredAt: at,
	})
}
