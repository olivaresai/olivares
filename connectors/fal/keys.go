// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// keys.go implements the fal key-lifecycle CONTROL POINT (G5): the API-key
// inventory and the rotation-posture findings that drive key hygiene. Issuing/rotating
// a key is a MUTATION and is intentionally OUT OF SCOPE for this read-first source — it
// is HITL-gated, like every other destructive provider action in the directory. What
// the connector does is surface the POSTURE: which keys exist (metadata only) and which
// are past their rotation age. It also emits the honest sales-gated/UNVERIFIED caveat so
// the governance view never implies coverage fal does not expose publicly.
package fal

import (
	"context"
	"fmt"
	"net/url"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Finding subjects for the fal governance posture findings.
const (
	subjectAPIKey     = "fal.api_key"
	subjectGovernance = "fal.governance"
	subjectKeySurface = "fal.key_management"
)

// gatherKeyPosture lists the API keys and emits a rotation-posture finding for any key
// older than key_max_age (the control point). On an unavailable key-management surface
// (UNVERIFIED-OFFLINE / sales-gated) it degrades to a single posture finding and
// returns nil — never a hard failure, never a fabricated empty inventory.
func (s *Source) gatherKeyPosture(ctx context.Context, sink sdk.Sink) error {
	keys, err := s.listKeys(ctx)
	if err != nil {
		if isUnavailable(err) {
			return sink.Emit(ctx, s.keyMgmtUnavailableFinding())
		}
		return err
	}
	now := s.clock().UTC()
	for _, k := range keys {
		created := parseTime(k.CreatedAt)
		if created.IsZero() {
			continue // cannot assess age without a creation time; do not guess
		}
		age := now.Sub(created)
		if age <= s.keyMaxAge {
			continue
		}
		days := int(age.Hours() / 24)
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "governance",
			Severity:    model.SeverityMedium,
			SubjectKind: subjectAPIKey,
			SubjectRef:  k.ID,
			Title:       fmt.Sprintf("fal API key past rotation age (%d days old)", days),
			DetailHash:  redact.Hash(fmt.Sprintf("fal key %s alias=%q scope=%q age_days=%d threshold_days=%d; rotate (key-lifecycle control point)", k.ID, k.Alias, k.Scope, days, int(s.keyMaxAge.Hours()/24))),
			OccurredAt:  now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// listKeys paginates the key-management list endpoint. Cursor pagination via last_id +
// has_more, bounded by max_pages. It returns the raw key metadata; both the posture
// gather and the Snapshot inventory map from it.
func (s *Source) listKeys(ctx context.Context) ([]falKey, error) {
	var out []falKey
	after := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp keysListResponse
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("after_id", after)
		}
		if err := s.keysClient.GetJSON(ctx, s.keysPath, q, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Data...)
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	return out, nil
}

// keyInventory maps the live key list to KeyRef inventory (metadata only — the masked
// partial, never the secret). On an unavailable key-management surface it returns an
// empty inventory with no error, so Snapshot degrades honestly (the offline posture).
func (s *Source) keyInventory(ctx context.Context) ([]modelprovider.KeyRef, error) {
	keys, err := s.listKeys(ctx)
	if err != nil {
		if isUnavailable(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]modelprovider.KeyRef, 0, len(keys))
	for _, k := range keys {
		out = append(out, modelprovider.KeyRef{
			ID: k.ID, Name: k.Alias, Status: k.Status,
			Hint: k.Masked, CreatedAt: parseTime(k.CreatedAt),
		})
	}
	return out, nil
}

// salesGatedCaveat is the honest UNVERIFIED governance caveat: fal exposes
// no public usage/audit API and its deep governance (SOC2/SSO/private endpoints) is
// sales-gated, so the control plane meters cost around the queue and governs key
// lifecycle — but does NOT claim audit/compliance coverage it cannot verify.
func (s *Source) salesGatedCaveat() model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectGovernance,
		SubjectRef:  "fal",
		Title:       "fal deep governance is sales-gated; no public usage/audit API (UNVERIFIED)",
		DetailHash:  redact.Hash("fal.ai: API-key-only, pay-per-output; SOC2/SSO/private-endpoints sales-gated; no public usage/audit API — cost is metered around the queue and governance is key-lifecycle posture only"),
		OccurredAt:  s.clock().UTC(),
	}
}

// keyMgmtUnavailableFinding is the honest degrade when the key-management REST surface
// returns 403/404 (UNVERIFIED-OFFLINE / not entitled / path differs on this tenant).
func (s *Source) keyMgmtUnavailableFinding() model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectKeySurface,
		SubjectRef:  "fal",
		Title:       "fal key-management API unavailable (sales-gated/UNVERIFIED on this tenant)",
		DetailHash:  redact.Hash("fal key-management path=" + s.keysPath + " base=" + s.keysBaseURL + " returned 403/404; key issuance/rotation is primarily dashboard-driven and the REST shape could not be verified offline"),
		OccurredAt:  s.clock().UTC(),
	}
}
