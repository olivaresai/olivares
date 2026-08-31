// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// keys.go implements the xAI API-key + ACL governance (the key-lifecycle control point,
// like connectors/fal): inventory the team's keys (metadata + masked hint only — never the
// secret) and emit governance posture — a rotation finding for any active key past
// key_max_age, and a least-privilege finding for any active key holding a wildcard ACL
// (api-key:endpoint:* / api-key:model:*). Issuing / rotating / disabling a key is a
// MUTATION and is out of scope for this read-first source (HITL-gated). The masked hint
// and ACL strings are non-sensitive; actor ids are folded into the one-way DetailHash.
package xai

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Finding subjects for the xAI governance/FinOps posture findings.
const (
	subjectTeam     = "xai.team"
	subjectAPIKey   = "xai.api_key"
	subjectACL      = "xai.api_key_acl"
	subjectBalance  = "xai.credit_balance"
	subjectSpendCap = "xai.spending_limit"
)

// wildcardACLs are the broad endpoint/model ACL grants whose presence on a key is a
// least-privilege posture signal (the key can reach every endpoint / every model).
var wildcardACLs = map[string]bool{
	"api-key:endpoint:*": true,
	"api-key:model:*":    true,
}

// gatherKeyPosture lists the team's API keys and emits rotation + broad-ACL posture for the
// active ones. The full key inventory (metadata) flows separately through Snapshot; here we
// emit only the governance signals so the ledger is not flooded with one finding per key.
func (s *Source) gatherKeyPosture(ctx context.Context, sink sdk.Sink, team string) error {
	keys, err := s.listKeys(ctx, team)
	if err != nil {
		if isUnavailable(err) {
			return sink.Emit(ctx, s.keysUnavailableFinding(team))
		}
		return err
	}
	now := s.clock().UTC()
	for _, k := range keys {
		if bool(k.Disabled) {
			continue // a disabled key is not an active credential; nothing to govern
		}
		if f, ok := s.rotationFinding(k, now); ok {
			if err := sink.Emit(ctx, f); err != nil {
				return err
			}
		}
		if f, ok := s.broadACLFinding(k, now); ok {
			if err := sink.Emit(ctx, f); err != nil {
				return err
			}
		}
	}
	return nil
}

// rotationFinding emits a Medium rotation-posture finding for a key older than key_max_age.
// ok is false for a key with no/sane-future creation time (age cannot be assessed) or one
// within the threshold.
func (s *Source) rotationFinding(k xaiAPIKey, now time.Time) (model.FindingReport, bool) {
	created := parseTime(k.CreateTime)
	if created.IsZero() || created.After(now) {
		return model.FindingReport{}, false
	}
	age := now.Sub(created)
	if age <= s.keyMaxAge {
		return model.FindingReport{}, false
	}
	days := int(age.Hours() / 24)
	return model.FindingReport{
		Kind:        "governance",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectAPIKey,
		SubjectRef:  k.APIKeyID,
		Title:       fmt.Sprintf("xAI API key past rotation age (%d days old)", days),
		DetailHash:  redact.Hash(fmt.Sprintf("xai key id=%s name=%q user=%s team=%s age_days=%d threshold_days=%d %s; rotate (key-lifecycle control point)", k.APIKeyID, k.Name, k.UserID, k.TeamID, days, int(s.keyMaxAge.Hours()/24), k.quotaDetail())),
		OccurredAt:  now,
	}, true
}

// broadACLFinding emits a Low least-privilege posture finding for a key holding a wildcard
// ACL (every endpoint / every model). ok is false when the key has no wildcard ACL.
func (s *Source) broadACLFinding(k xaiAPIKey, now time.Time) (model.FindingReport, bool) {
	var broad []string
	for _, a := range k.acls() {
		if wildcardACLs[a] {
			broad = append(broad, a)
		}
	}
	if len(broad) == 0 {
		return model.FindingReport{}, false
	}
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityLow,
		SubjectKind: subjectACL,
		SubjectRef:  k.APIKeyID,
		Title:       "xAI API key holds a wildcard ACL (broad access — least-privilege review)",
		DetailHash:  redact.Hash(fmt.Sprintf("xai key id=%s name=%q acls=%v %s; holds wildcard grant(s) %v — scope to specific endpoints/models", k.APIKeyID, k.Name, k.acls(), k.quotaDetail(), broad)),
		OccurredAt:  now,
	}, true
}

func (k xaiAPIKey) quotaDetail() string {
	return fmt.Sprintf("tpm=%d qps=%d qpm=%d", int(k.TPM), int(k.QPS), int(k.QPM))
}

// keysUnavailableFinding is the honest degrade when the key-list surface returns 403/404
// (the management key is not entitled to list the team's keys, or the team is wrong).
func (s *Source) keysUnavailableFinding(team string) model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectAPIKey,
		SubjectRef:  "xai",
		Title:       "xAI API-key inventory unavailable (management key not entitled / wrong team)",
		DetailHash:  redact.Hash("xai api-keys list for team=" + team + " base=" + s.managementBaseURL + " returned 403/404; the management key may lack scope or the team_id may be wrong"),
		OccurredAt:  s.clock().UTC(),
	}
}

// listKeys paginates GET /auth/teams/{teamId}/api-keys. Cursor pagination via
// paginationToken (absent/empty => done), bounded by max_pages. activeOnly is NOT set so
// the inventory reflects disabled keys too (the rotation/ACL posture filters them out).
func (s *Source) listKeys(ctx context.Context, team string) ([]xaiAPIKey, error) {
	var out []xaiAPIKey
	token := ""
	path := "/auth/teams/" + url.PathEscape(team) + "/api-keys"
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp apiKeysResponse
		q := url.Values{"pageSize": {"100"}}
		if token != "" {
			q.Set("paginationToken", token)
		}
		if err := s.mgmtClient.GetJSON(ctx, path, q, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.APIKeys...)
		if resp.PaginationToken == "" {
			break
		}
		token = resp.PaginationToken
	}
	return out, nil
}

// keyInventory maps the listed keys to KeyRef inventory (the masked hint, never the secret)
// for the catalog. Status reflects the disabled flag; ExpiresAt and CreatedBy come from the
// key's metadata. ACLs are NOT a KeyRef field — they surface as governance posture findings.
func keyInventory(keys []xaiAPIKey) []modelprovider.KeyRef {
	out := make([]modelprovider.KeyRef, 0, len(keys))
	for _, k := range keys {
		status := "active"
		if bool(k.Disabled) {
			status = "disabled"
		}
		out = append(out, modelprovider.KeyRef{
			ID: k.APIKeyID, Name: k.Name, WorkspaceRef: k.TeamID,
			Status: status, Hint: k.RedactedKey,
			CreatedAt: parseTime(k.CreateTime), ExpiresAt: parseTime(k.ExpireTime),
			CreatedBy: k.UserID,
		})
	}
	return out
}
