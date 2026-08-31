// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file ingests the Compliance API DIRECTORY (the orgs/users/roles/groups
// arm of the multi-resource Compliance surface) as minimal-data governance evidence.
// It is the read the Activity Feed cannot give: the PARENT-org topology (parent + all
// linked orgs), the RBAC role inventory, and — the governance prize — each group's
// source_type, which reveals whether a group is SCIM-provisioned. The Admin API
// CANNOT observe SCIM provisioning state (the claude-console connector flags that as an
// honest blind spot); this directory read RESOLVES that blind spot for orgs that grant
// the Compliance Access Key, turning "SCIM state unknown" into a concrete signal.
//
// It uses the DISTINCT higher-privilege Compliance Access Key (read:compliance_org_data
// for org metadata, roles, groups, and effective settings; read:compliance_user_data for
// user listings and group membership), kept in its own slot — never the Activity-Feed key
// (the conflation warned against). It
// is read-only and minimal-data (docs/SECURITY-HARDENING.md): it emits aggregate COUNTS + non-sensitive
// RBAC labels + the SCIM signal — never a user email, a group's membership roster, or
// any content. Per-org deep enumeration is bounded (defaultMaxOrgs) and the connector
// SAYS so when it truncates (honest, never a silent cap).
//
// Authority (jun-2026): platform.claude.com/docs/en/api/compliance/organizations +
// /groups. 600 req/min shared per parent org (throttling is the engine's concern).
package claudecompliance

import (
	"context"
	"net/url"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Directory endpoint paths + bounds.
const (
	orgsPath   = "/v1/compliance/organizations"
	groupsPath = "/v1/compliance/groups"

	// defaultMaxOrgs bounds how many orgs the directory deep-dives (roles + user count)
	// per Gather, so a 1000-org parent does not fan out unboundedly. When more orgs
	// exist than this, the connector emits a truncation note (honest, never silent).
	defaultMaxOrgs = 100

	// findingKindDirectory is the Kind of the directory inventory evidence (distinct
	// from external_activity so it never pollutes the activity evidence count).
	findingKindDirectory = "directory"

	// groupSourceSCIM is the source_type a SCIM-provisioned group carries.
	groupSourceSCIM = "scim"
)

// organizationsResponse is GET /v1/compliance/organizations (no pagination; up to 1000
// linked orgs under the parent). Only the org reference + name are read.
type organizationsResponse struct {
	Data []complianceOrg `json:"data"`
}

// complianceOrg is one org under the parent (uuid is the canonical id for joins).
type complianceOrg struct {
	UUID      string `json:"uuid"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

// rolesResponse is the per-org RBAC roles page (page/next_page token pagination).
type rolesResponse struct {
	Data     []complianceRole `json:"data"`
	HasMore  bool             `json:"has_more"`
	NextPage string           `json:"next_page"`
}

// complianceRole is one RBAC role (id + name are non-sensitive governance labels).
type complianceRole struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// usersListResponse is the per-org users page. Only the COUNT is used (the email/role
// PII is never surfaced — the roster lives in claude-console's identitysource graph).
type usersListResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
	HasMore  bool   `json:"has_more"`
	NextPage string `json:"next_page"`
}

// groupsListResponse is the parent-wide groups page. source_type is the SCIM signal.
type groupsListResponse struct {
	Data     []complianceGroup `json:"data"`
	HasMore  bool              `json:"has_more"`
	NextPage string            `json:"next_page"`
}

// complianceGroup is one RBAC/SCIM group. source_type ∈ {direct, scim} — "scim" is the
// signal that an external IdP provisions this group (the Admin-API blind spot resolved).
type complianceGroup struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	SourceType string `json:"source_type"`
}

// gatherDirectory ingests the directory as governance evidence: the org topology, the
// per-org role inventory + user count, and the parent-wide group inventory with the
// SCIM-provisioning signal. It uses the Compliance Access Key client (cakClient); the
// caller (Gather) only invokes it when that key is configured. Errors propagate (the
// engine retries); the per-org user count is best-effort (it needs the extra
// user_data scope, so a 403 there must not lose the org_data evidence).
func (s *Source) gatherDirectory(ctx context.Context, sink sdk.Sink) error {
	at := s.clock().UTC()

	var orgs organizationsResponse
	if err := s.cakClient.GetJSON(ctx, orgsPath, url.Values{}, &orgs); err != nil {
		return err
	}
	// Topology evidence: parent + linked orgs (the multi-org reach the Activity Feed
	// returns but the Admin API never enumerates).
	if err := sink.Emit(ctx, s.directoryFinding(
		"claude_compliance_directory", s.orgRef,
		"Compliance directory: "+strconv.Itoa(len(orgs.Data))+" organization(s) under parent",
		"orgs="+strconv.Itoa(len(orgs.Data)),
		model.SeverityInfo, at)); err != nil {
		return err
	}

	// Parent-wide groups + the SCIM-provisioning signal.
	if err := s.gatherGroups(ctx, sink, at); err != nil {
		return err
	}

	// Per-org roles + user count, bounded.
	deepDive := orgs.Data
	truncated := false
	if len(deepDive) > defaultMaxOrgs {
		deepDive = deepDive[:defaultMaxOrgs]
		truncated = true
	}
	for _, o := range deepDive {
		if err := ctx.Err(); err != nil {
			return err
		}
		if o.UUID == "" {
			continue
		}
		if err := s.gatherOrgDetail(ctx, sink, o, at); err != nil {
			return err
		}
	}
	if truncated {
		// Honest cap: say what was NOT deep-dived rather than imply full coverage.
		if err := sink.Emit(ctx, s.directoryFinding(
			"claude_compliance_directory", s.orgRef,
			"Compliance directory deep-dive (roles/users/settings) truncated at "+strconv.Itoa(defaultMaxOrgs)+" of "+strconv.Itoa(len(orgs.Data))+" orgs",
			"deep_dive_cap="+strconv.Itoa(defaultMaxOrgs)+";total_orgs="+strconv.Itoa(len(orgs.Data)),
			model.SeverityInfo, at)); err != nil {
			return err
		}
	}

	// Effective-settings attestation (settings.go): turn the org-level controls Anthropic
	// ENFORCES (retention / content redaction / IP allowlist / SSO mode / code-exec egress)
	// into sealed posture evidence — resolving the claude-console sso_enforcement=
	// unknown-by-api blind spot. Best-effort on read:compliance_org_data; the dedicated
	// read:compliance_org_settings scope was RETIRED (~2026-06-30), and effective
	// settings now ride under read:compliance_org_data (VERIFIED 2026-07-03). It reuses
	// this same (bounded) linked-org list and Compliance Access Key, so it needs no extra
	// org listing and inherits the deep-dive truncation bound above.
	if err := s.gatherSettings(ctx, sink, deepDive, at); err != nil {
		return err
	}

	// Content inventory (best-effort): when the key additionally carries
	// read:compliance_user_data, enumerate content REFERENCES (ids + metadata only,
	// never bodies) and emit a governance summary per kind (chat/project) — so the
	// audit ledger and retention policies have a sealed record of WHAT provider-side
	// content exists, without the connector ever downloading or persisting customer
	// content. A 403 (scope absent) degrades to a single honest note; a transport
	// error propagates for retry.
	if err := s.gatherContentInventory(ctx, sink, at); err != nil {
		return err
	}
	return nil
}

// gatherGroups pages the parent-wide group list and emits the group inventory + the
// SCIM-provisioning signal: how many groups are SCIM-sourced vs direct. The SCIM count
// is the governance prize — it resolves the SSO/SCIM blind spot the Admin API leaves.
func (s *Source) gatherGroups(ctx context.Context, sink sdk.Sink, at time.Time) error {
	var total, scim int
	page := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var resp groupsListResponse
		q := url.Values{"limit": {defaultPageLimit}}
		if page != "" {
			q.Set("page", page)
		}
		if err := s.cakClient.GetJSON(ctx, groupsPath, q, &resp); err != nil {
			return err
		}
		for _, g := range resp.Data {
			total++
			if g.SourceType == groupSourceSCIM {
				scim++
			}
		}
		if !resp.HasMore || resp.NextPage == "" {
			break
		}
		page = resp.NextPage
	}
	sev := model.SeverityInfo
	title := "Compliance directory groups: " + strconv.Itoa(total) + " total, " +
		strconv.Itoa(scim) + " SCIM-provisioned, " + strconv.Itoa(total-scim) + " direct"
	if scim > 0 {
		// A positive SCIM count is the concrete provisioning signal claude-console's
		// Admin-API blind-spot finding could only mark "unknown-by-api".
		title = "SCIM provisioning ACTIVE: " + strconv.Itoa(scim) + " of " + strconv.Itoa(total) +
			" directory group(s) SCIM-sourced (resolves the Admin-API SCIM blind spot)"
	}
	return sink.Emit(ctx, s.directoryFinding(
		"claude_compliance_groups", s.orgRef, title,
		"groups_total="+strconv.Itoa(total)+";scim="+strconv.Itoa(scim),
		sev, at))
}

// gatherOrgDetail emits one org's role count and user count (counts only — no PII). The
// user count is best-effort: it needs read:compliance_user_data, so a failure there is
// surfaced as a scope note and does not lose the role (org_data) evidence.
func (s *Source) gatherOrgDetail(ctx context.Context, sink sdk.Sink, o complianceOrg, at time.Time) error {
	roleCount, err := s.countRoles(ctx, o.UUID)
	if err != nil {
		return err
	}
	userCount, userErr := s.countUsers(ctx, o.UUID)
	detail := "org=" + o.UUID + ";roles=" + strconv.Itoa(roleCount)
	title := "Compliance directory org: " + strconv.Itoa(roleCount) + " role(s)"
	if userErr == nil {
		detail += ";users=" + strconv.Itoa(userCount)
		title += ", " + strconv.Itoa(userCount) + " user(s)"
	} else {
		// Honest: the user count needs the extra user_data scope; say so, do not fake 0.
		detail += ";users=unavailable(no read:compliance_user_data)"
		title += ", user count unavailable (needs read:compliance_user_data)"
	}
	return sink.Emit(ctx, s.directoryFinding("claude_compliance_org", o.UUID, title, detail, model.SeverityInfo, at))
}

// countRoles pages the org's RBAC roles and returns the count (labels not surfaced).
func (s *Source) countRoles(ctx context.Context, orgUUID string) (int, error) {
	n := 0
	page := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		var resp rolesResponse
		q := url.Values{"limit": {defaultPageLimit}}
		if page != "" {
			q.Set("page", page)
		}
		if err := s.cakClient.GetJSON(ctx, orgsPath+"/"+url.PathEscape(orgUUID)+"/roles", q, &resp); err != nil {
			return 0, err
		}
		n += len(resp.Data)
		if !resp.HasMore || resp.NextPage == "" {
			break
		}
		page = resp.NextPage
	}
	return n, nil
}

// countUsers pages the org's users and returns the count. It surfaces only the COUNT —
// the email/role PII is never read into a finding (minimal-data); the full roster is
// claude-console's identitysource graph, not this evidence stream.
func (s *Source) countUsers(ctx context.Context, orgUUID string) (int, error) {
	n := 0
	page := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		var resp usersListResponse
		q := url.Values{"limit": {defaultPageLimit}}
		if page != "" {
			q.Set("page", page)
		}
		if err := s.cakClient.GetJSON(ctx, orgsPath+"/"+url.PathEscape(orgUUID)+"/users", q, &resp); err != nil {
			return 0, err
		}
		n += len(resp.Data)
		if !resp.HasMore || resp.NextPage == "" {
			break
		}
		page = resp.NextPage
	}
	return n, nil
}

// gatherContentInventory enumerates provider-side content REFERENCES (chat/project ids
// + structural metadata — never names, message bodies, file bytes, or PII) and emits a
// governance inventory finding per kind. This makes the content footprint visible to the
// audit ledger and the compliance module's retention/hold framework without the connector
// ever downloading or persisting customer content (docs/SECURITY-HARDENING.md).
//
// It requires read:compliance_user_data on the Compliance Access Key (the same scope the
// RTBF enumerator uses); a 403 (scope absent) degrades to a single honest note and does
// NOT lose the directory/settings evidence already emitted. An empty inventory (0 chats,
// 0 projects) is explicitly attested — no silent omission.
//
// The inventory is STRUCTURAL (counts + age distribution per kind + deletion-state
// counts), never PII. An auditor reads "42 chats, 3 soft-deleted" and can correlate with
// the retention window and the hold-gate without seeing a single chat title.
func (s *Source) gatherContentInventory(ctx context.Context, sink sdk.Sink, at time.Time) error {
	// List chats and projects WITHOUT user-scoping (empty userIDs) — the API requires
	// user_ids for chats but not for projects. For the inventory we page projects only
	// (the API supports un-scoped listing) and record chats as "scope-gated" when
	// user_ids are not supplied.
	var projects []ContentRef
	var projErr error
	projects, projErr = s.EnumerateProjects(ctx, nil)
	if projErr != nil {
		if isStatus(projErr, 403) {
			return sink.Emit(ctx, s.directoryFinding(
				"claude_compliance_content", s.orgRef,
				"Content inventory unavailable: the Compliance Access Key is missing read:compliance_user_data (content enumeration requires this scope)",
				"content_inventory=unavailable;reason=scope_missing", model.SeverityInfo, at))
		}
		return projErr
	}

	var active, deleted int
	for _, p := range projects {
		if p.DeletedAt != "" {
			deleted++
		} else {
			active++
		}
	}
	title := "Provider content inventory: " + strconv.Itoa(len(projects)) + " project(s) enumerated (" +
		strconv.Itoa(active) + " active, " + strconv.Itoa(deleted) + " soft-deleted); " +
		"chat inventory requires per-user enumeration (user_ids — available during RTBF execution, not bulk inventory)"
	return sink.Emit(ctx, s.directoryFinding(
		"claude_compliance_content", s.orgRef, title,
		"projects_total="+strconv.Itoa(len(projects))+";projects_active="+strconv.Itoa(active)+
			";projects_deleted="+strconv.Itoa(deleted)+";chats=per_user_only",
		model.SeverityInfo, at))
}

// directoryFinding builds one minimal-data directory evidence finding (Info — inventory,
// not an alert). The structural detail is folded into the one-way DetailHash; the Title
// carries the non-sensitive counts/signal a governance view reads.
func (s *Source) directoryFinding(subjectKind, subjectRef, title, detail string, sev model.Severity, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingKindDirectory,
		Severity:    sev,
		SubjectKind: subjectKind,
		SubjectRef:  subjectRef,
		Title:       title,
		DetailHash:  redact.Hash(s.orgRef + "|" + detail),
		OccurredAt:  at,
	}
}
