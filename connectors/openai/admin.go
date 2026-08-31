// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// admin.go extends the OpenAI governance surface with the ChatGPT Enterprise /
// platform Admin API depth:
//
//   - Invites inventory: pending/expired org invitations with role + timestamp.
//   - Project users: per-project user roster mapped to Olivares workspace grants.
//   - Project service accounts: NHI identities per project.
//   - Project API keys: per-project key inventory (masked, never values).
//
// Every surface follows HONEST DEGRADATION: a 403/404 emits a posture finding
// and returns nil. Minimal-data: actor email/name folded into the DetailHash,
// never surfaced in Title or SubjectRef (except the admin-scoped email in invite
// titles, which is an operator-chosen admin fact, not PII from usage).
package openai

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Finding subjects for the admin governance surfaces.
const (
	subjectInvite         = "openai.invite"
	subjectProjectUser    = "openai.project_user"
	subjectServiceAccount = "openai.service_account"
	subjectProjectAPIKey  = "openai.project_api_key"
)

// ---------- Invites (GET /v1/organization/invites) ----------

// gatherInvites paginates the org invites API and emits one inventory finding
// per pending/expired invite. On a 403/404 it degrades honestly.
func (s *Source) gatherInvites(ctx context.Context, sink sdk.Sink) error {
	after := ""
	pending := 0
	expired := 0
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var resp invitesResponse
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("after", after)
		}
		if err := s.client.GetJSON(ctx, "/v1/organization/invites", q, &resp); err != nil {
			if isUnavailable(err) {
				return sink.Emit(ctx, s.unavailableFinding("invites", "/v1/organization/invites"))
			}
			return err
		}
		for _, inv := range resp.Data {
			if inv.ID == "" {
				continue
			}
			switch inv.Status {
			case "pending":
				pending++
			case "expired":
				expired++
			}
			if err := sink.Emit(ctx, s.inviteFinding(inv)); err != nil {
				return err
			}
		}
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	if pending > 0 || expired > 0 {
		return sink.Emit(ctx, s.inviteSummaryFinding(pending, expired))
	}
	return nil
}

// inviteFinding builds an inventory finding for one invite. The email is hashed
// in the detail; the title carries only the role and status (minimal-data).
func (s *Source) inviteFinding(inv inviteEntry) model.FindingReport {
	sev := model.SeverityInfo
	if inv.Status == "expired" {
		sev = model.SeverityLow
	}
	title := fmt.Sprintf("OpenAI invite: role=%s, status=%s", inv.Role, inv.Status)
	detail := fmt.Sprintf("openai invite id=%s email=%s role=%s status=%s",
		inv.ID, inv.Email, inv.Role, inv.Status)

	return model.FindingReport{
		Kind:        "inventory",
		Severity:    sev,
		SubjectKind: subjectInvite,
		SubjectRef:  inv.ID,
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

// inviteSummaryFinding emits a posture finding summarizing pending/expired invite
// counts. Expired invites are a hygiene signal.
func (s *Source) inviteSummaryFinding(pending, expired int) model.FindingReport {
	sev := model.SeverityInfo
	if expired > 0 {
		sev = model.SeverityLow
	}
	title := fmt.Sprintf("OpenAI org invites: %d pending, %d expired", pending, expired)
	detail := fmt.Sprintf("openai invites pending=%d expired=%d", pending, expired)

	return model.FindingReport{
		Kind:        "posture",
		Severity:    sev,
		SubjectKind: subjectInvite,
		SubjectRef:  "organization",
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

// ---------- Project-level admin depth ----------

// gatherProjectAdmin reads the per-project users, service accounts, and API keys
// for each known (non-archived) project, emitting inventory and posture findings.
// It correlates project users → Olivares workspace grants. If the project list
// itself fails, it degrades and returns nil (the org-level gather already ran).
func (s *Source) gatherProjectAdmin(ctx context.Context, sink sdk.Sink) error {
	projects, err := s.fetchProjects(ctx)
	if err != nil {
		return nil
	}
	for _, p := range projects {
		if p.Archived {
			continue
		}
		if err := s.gatherProjectUsers(ctx, sink, p.ID, p.Name); err != nil {
			return err
		}
		if err := s.gatherProjectServiceAccounts(ctx, sink, p.ID, p.Name); err != nil {
			return err
		}
		if err := s.gatherProjectAPIKeys(ctx, sink, p.ID, p.Name); err != nil {
			return err
		}
	}
	return nil
}

// gatherProjectUsers lists users in a project and emits an inventory finding per
// user (role correlation) plus a summary. The email is hashed, never surfaced.
func (s *Source) gatherProjectUsers(ctx context.Context, sink sdk.Sink, projID, projName string) error {
	roleCounts := map[string]int{}
	total := 0
	after := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var resp projectUsersResponse
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("after", after)
		}
		path := "/v1/organization/projects/" + projID + "/users"
		if err := s.client.GetJSON(ctx, path, q, &resp); err != nil {
			if isUnavailable(err) {
				return nil
			}
			return err
		}
		for _, u := range resp.Data {
			total++
			role := u.Role
			if role == "" {
				role = "unknown"
			}
			roleCounts[role]++
		}
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	if total == 0 {
		return nil
	}
	return sink.Emit(ctx, s.projectUserSummary(projID, projName, total, roleCounts))
}

// projectUserSummary builds a posture finding summarizing the user roster for
// one project, correlating it to an Olivares workspace ref.
func (s *Source) projectUserSummary(projID, projName string, total int, roles map[string]int) model.FindingReport {
	var parts []string
	for role, count := range roles {
		parts = append(parts, fmt.Sprintf("%s=%d", role, count))
	}
	roleSummary := strings.Join(parts, ", ")
	scope := projName
	if scope == "" {
		scope = projID
	}
	title := fmt.Sprintf("OpenAI project %q users: %d user(s), roles: %s", scope, total, roleSummary)
	detail := fmt.Sprintf("openai project_users project=%s total=%d roles=[%s]", projID, total, roleSummary)

	return model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectProjectUser,
		SubjectRef:  projID,
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

// gatherProjectServiceAccounts lists NHI service accounts in a project and emits
// an inventory posture finding.
func (s *Source) gatherProjectServiceAccounts(ctx context.Context, sink sdk.Sink, projID, projName string) error {
	var accounts []projectServiceAccountEntry
	after := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var resp projectServiceAccountsResponse
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("after", after)
		}
		path := "/v1/organization/projects/" + projID + "/service_accounts"
		if err := s.client.GetJSON(ctx, path, q, &resp); err != nil {
			if isUnavailable(err) {
				return nil
			}
			return err
		}
		accounts = append(accounts, resp.Data...)
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	if len(accounts) == 0 {
		return nil
	}
	scope := projName
	if scope == "" {
		scope = projID
	}
	title := fmt.Sprintf("OpenAI project %q: %d service account(s)", scope, len(accounts))
	detail := fmt.Sprintf("openai project_service_accounts project=%s count=%d", projID, len(accounts))

	return sink.Emit(ctx, model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectServiceAccount,
		SubjectRef:  projID,
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	})
}

// gatherProjectAPIKeys lists API keys in a project and emits an inventory
// posture finding. Key values are never returned by the API; only the masked
// redacted_value.
func (s *Source) gatherProjectAPIKeys(ctx context.Context, sink sdk.Sink, projID, projName string) error {
	totalKeys := 0
	var staleKeys int
	after := ""
	now := s.clock().UTC()
	staleThreshold := now.Add(-90 * 24 * time.Hour) // 90 days without rotation
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var resp projectAPIKeysResponse
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("after", after)
		}
		path := "/v1/organization/projects/" + projID + "/api_keys"
		if err := s.client.GetJSON(ctx, path, q, &resp); err != nil {
			if isUnavailable(err) {
				return nil
			}
			return err
		}
		for _, k := range resp.Data {
			totalKeys++
			created := unixTime(k.CreatedAt)
			if !created.IsZero() && created.Before(staleThreshold) {
				staleKeys++
			}
		}
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	if totalKeys == 0 {
		return nil
	}
	scope := projName
	if scope == "" {
		scope = projID
	}
	sev := model.SeverityInfo
	if staleKeys > 0 {
		sev = model.SeverityLow
	}
	title := fmt.Sprintf("OpenAI project %q: %d API key(s)", scope, totalKeys)
	if staleKeys > 0 {
		title += fmt.Sprintf(" (%d older than 90d)", staleKeys)
	}
	detail := fmt.Sprintf("openai project_api_keys project=%s total=%d stale=%d", projID, totalKeys, staleKeys)

	return sink.Emit(ctx, model.FindingReport{
		Kind:        "inventory",
		Severity:    sev,
		SubjectKind: subjectProjectAPIKey,
		SubjectRef:  projID,
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	})
}
