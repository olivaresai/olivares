// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package claudecompliance is the Olivares AI read-only evidence connector for the
// Anthropic Compliance API Activity Feed (CLA-06 / FIN-05). It paginates
// GET /v1/compliance/activities and emits one minimal-data FindingReport per activity
// record, which the engine appends to the tamper-evident audit ledger and the SIEM
// output connector exports — turning the Claude platform's own activity log into
// auditable, eDiscovery-grade evidence inside the control plane.
//
// READ-ONLY BY CONSTRUCTION (docs/SECURITY-HARDENING.md-3). Every call is a GET via the shared
// GET-only modelprovider client, so this connector CANNOT perform the destructive
// content-DELETE operations the Compliance API also exposes — that is deliberate. The
// hard-delete endpoints (eDiscovery / GDPR erasure of chats/files/projects) are
// permanent with no recovery window, so they must route through a human-in-the-loop
// governance gate (module VI) and never run automatically; they are intentionally OUT
// OF SCOPE for this connector. Bringing them in would require the full Compliance
// Access Key and an explicit, opt-in, HITL-gated action surface — not a source poll.
//
// Two-key model (verified against Anthropic primary docs, platform.claude.com):
//   - An ADMIN API key (sk-ant-admin01-) carrying ONLY the read:compliance_activities
//     scope reaches the Activity Feed and nothing else — the least-privilege key this
//     connector is designed for.
//   - A full COMPLIANCE ACCESS KEY (sk-ant-api01-) can additionally read user content
//     and DELETE it (read/delete:compliance_user_data). This connector neither needs
//     nor uses those scopes; use the Activity-Feed-only Admin key.
//
// Minimal data (docs/SECURITY-HARDENING.md): a finding carries a reference (the activity id) + a
// non-sensitive title (the activity type) + a one-way DetailHash of the structural
// fields. Actor PII (ip, user-agent, email) is folded into the HASH only and never
// transmitted or persisted in the clear; chat/message CONTENT is never read at all.
// Topology + limits: a Claude Enterprise tenant has ONE parent org (the feed returns
// the parent + all linked orgs); all /v1/compliance/* endpoints share a single rate
// limit of 600 requests/min per parent org — retry/rate-limiting is the engine's
// concern (SourceConnector contract), so this connector adds no throttle, only a
// max_pages safety bound. It imports only the SDK and Apache connector contracts,
// never the engine.
package claudecompliance

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.claude-compliance"

// findingKindActivity is the Kind every emitted activity-evidence finding carries. It
// is the same string the compliance module's external_activity capability counts —
// module XIII keys its evidence on this Kind, so keep them in lockstep.
const findingKindActivity = "external_activity"

// findingKindCoverage is the Kind of the once-per-Gather posture finding that records
// the feed's documented coverage gaps. It is DISTINCT from findingKindActivity so it
// never pollutes the external_activity evidence count module XIII keys on.
const findingKindCoverage = "posture"

// Default configuration values.
const (
	defaultBaseURL          = "https://api.anthropic.com"
	defaultAnthropicVersion = "2023-06-01"
	defaultOrgRef           = "anthropic-parent-org"
	defaultMaxPages         = 50
	defaultPageLimit        = "100"

	activitiesPath = "/v1/compliance/activities"
)

// Source is the Compliance evidence connector. It satisfies sdk.SourceConnector:
// Gather paginates the Activity Feed (api_key) AND, when the higher-privilege
// Compliance Access Key is configured, ingests the org DIRECTORY (orgs/users/roles/
// groups) as governance evidence — including the SCIM-provisioning signal the Admin
// API cannot see (a group's source_type). Both streams are read-only; content
// retrieve and the irreversible RTBF DELETE are the separately-governed surfaces in
// content.go (never auto-polled here).
type Source struct {
	apiKey              string
	complianceAccessKey string // DISTINCT higher-privilege key (read:compliance_org_data/user_data) for directory
	baseURL             string
	version             string
	orgRef              string
	maxPages            int

	client    *modelprovider.Client // Activity Feed client (api_key)
	cakClient *modelprovider.Client // directory client (Compliance Access Key)
	doer      modelprovider.Doer    // injected transport (tests); nil => default
	now       func() time.Time      // injectable clock (tests); nil => time.Now
}

// Compile-time proof Source is a read-only source connector.
var _ sdk.SourceConnector = (*Source)(nil)

// SetTestTransport injects a custom HTTP transport and clock for integration
// tests that construct Source via New() from outside the package. Production code
// should never call this; Open builds the real client from config.
func (s *Source) SetTestTransport(doer modelprovider.Doer) {
	s.doer = doer
	s.now = func() time.Time { return time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC) }
}

// New returns a Compliance Activity Feed connector with default configuration.
func New() *Source {
	return &Source{baseURL: defaultBaseURL, version: defaultAnthropicVersion, orgRef: defaultOrgRef, maxPages: defaultMaxPages}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Claude Compliance Activity Feed (audit evidence)",
		Description: "Read-only: appends Anthropic Compliance API activity records (event-log stream, hundreds of activity types classified into non-sensitive categories) to the tamper-evident ledger as audit/eDiscovery evidence, and documents the feed's coverage gaps (ZDR/Cowork/EU-routing) honestly. Also attests each linked org's EFFECTIVE enforced controls (data retention, content redaction, IP allowlist, SSO/SCIM provisioning mode, code-execution network egress) as sealed posture evidence — a missing control row is reported not-introspectable, never as 'off'. TWO-KEY MODEL with honest degradation: an Admin API key (sk-ant-admin01-) reaches only the Activity Feed; a DISTINCT Compliance Access Key (sk-ant-api01-) additionally enables org directory, effective-settings, and content enumeration — absence of either key is DECLARED, never silent. Enterprise-gated: offline without an Enterprise plan (no fabricated events). LIMITS: Anthropic retains activity records for 6 years — VERIFIED 2026-07-03; earlier capture (jun-2026) said 180 days; docs were restructured; all /v1/compliance/* endpoints share 600 requests/min per parent org (throttling is the engine's concern; the connector bounds pagination via max_pages). Content retrieve / eDiscovery DELETE are intentionally out of scope (higher-privilege, HITL-gated).",
		ConfigFields: []sdk.ConfigField{
			{Key: "api_key", Type: sdk.FieldString, Secret: true, Description: "Anthropic Activity-Feed key reference (Admin API key sk-ant-admin01- with read:compliance_activities; read-only; never persisted). Empty = offline (no activity evidence emitted). Anthropic retains activity records for 6 years — VERIFIED 2026-07-03; earlier capture (jun-2026) said 180 days; docs were restructured. Poll frequently for freshness and near-real-time audit value."},
			{Key: "compliance_access_key", Type: sdk.FieldString, Secret: true, Description: "DISTINCT Compliance Access Key reference (sk-ant-api01- with read:compliance_org_data and/or read:compliance_user_data; read-only; never persisted). Enables the org DIRECTORY ingest (org metadata/roles/groups under read:compliance_org_data; user listings, group membership, and content enumeration under read:compliance_user_data) and, under read:compliance_org_data, the EFFECTIVE-SETTINGS attestation (the enforced retention/redaction/IP-allowlist/SSO-mode/code-exec-egress controls; a missing control row is reported not-introspectable, never 'off'; best-effort — a 403/404 degrades to one honest note). The dedicated read:compliance_org_settings scope was RETIRED (~2026-06-30); effective settings now ride under read:compliance_org_data (VERIFIED 2026-07-03). Empty = directory off (deny-closed). The delete:compliance_user_data scope used by the RTBF eraser (content.go) is provisioned separately and is dual-control gated — never used by this read connector."},
			{Key: "base_url", Type: sdk.FieldString, Default: defaultBaseURL, Description: "Anthropic API base URL."},
			{Key: "anthropic_version", Type: sdk.FieldString, Default: defaultAnthropicVersion, Description: "anthropic-version header value."},
			{Key: "org_ref", Type: sdk.FieldString, Default: defaultOrgRef, Description: "Stable reference for the governed Claude parent org (the evidence subject)."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound. All /v1/compliance/* endpoints share 600 requests/min per parent org (throttling is the engine's concern — the connector does not self-throttle; this bound only caps the page count per Gather cycle)."},
		},
	}
}

// Open reads configuration and, when an API key is present, builds the read-only
// (GET-only) Activity Feed client. It contacts no network.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.apiKey = cfg.Get("api_key")
	if b := strings.TrimRight(cfg.Get("base_url"), "/"); b != "" {
		s.baseURL = b
	}
	if v := cfg.Get("anthropic_version"); v != "" {
		s.version = v
	}
	if o := strings.TrimSpace(cfg.Get("org_ref")); o != "" {
		s.orgRef = o
	}
	s.maxPages = cfg.GetInt("max_pages", s.maxPages)
	if s.maxPages <= 0 {
		s.maxPages = defaultMaxPages
	}
	s.complianceAccessKey = cfg.Get("compliance_access_key")
	if s.apiKey != "" {
		s.client = modelprovider.NewClient(s.baseURL, s.doer, modelprovider.AuthAnthropicKey, s.apiKey,
			map[string]string{"anthropic-version": s.version})
	}
	if s.complianceAccessKey != "" {
		s.cakClient = modelprovider.NewClient(s.baseURL, s.doer, modelprovider.AuthAnthropicKey, s.complianceAccessKey,
			map[string]string{"anthropic-version": s.version})
	}
	return nil
}

// Gather ingests the two read streams the connector is configured for, each gated on
// its OWN key (deny-closed, honest absence when a key is absent): (1) the Activity Feed
// (api_key) — one minimal-data FindingReport per activity record; (2) the org DIRECTORY
// (Compliance Access Key) — orgs/users/roles/groups governance evidence including the
// SCIM-provisioning signal, PLUS each linked org's EFFECTIVE enforced settings when the
// key also carries read:compliance_org_data for effective settings (settings.go; the
// dedicated read:compliance_org_settings scope was RETIRED ~2026-06-30, VERIFIED
// 2026-07-03). Content retrieve and the irreversible RTBF DELETE are NOT
// here (they are the separately-governed surfaces in content.go). A sink/transport
// error stops the run and is returned (the engine retries).
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.client != nil {
		if err := s.gatherActivity(ctx, sink); err != nil {
			return err
		}
	}
	if s.cakClient != nil {
		if err := s.gatherDirectory(ctx, sink); err != nil {
			return err
		}
	}
	// Honest degradation: when the Activity Feed key is present but the Compliance
	// Access Key is absent, the connector DECLARES that directory, content enumeration,
	// effective-settings attestation, and the RTBF erase surface are unavailable — so an
	// auditor sees a concrete posture gap rather than silent omission. With no keys at
	// all, the connector stays fully silent (nothing to declare against).
	if s.client != nil && s.cakClient == nil {
		if err := sink.Emit(ctx, s.cakAbsentFinding(s.clock().UTC())); err != nil {
			return err
		}
	}
	return nil
}

// gatherActivity paginates the Activity Feed (after_id cursor) and emits one minimal-
// data FindingReport per activity record. It first documents the feed's coverage gaps
// ONCE per online Gather (honest by construction: the buyer sees what the event-log
// stream does NOT cover before reading the evidence). Pagination is bounded by max_pages.
func (s *Source) gatherActivity(ctx context.Context, sink sdk.Sink) error {
	if err := sink.Emit(ctx, s.coverageFinding(s.clock().UTC())); err != nil {
		return err
	}
	after := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var resp activitiesResponse
		if err := s.getPage(ctx, after, &resp); err != nil {
			return err
		}
		for _, a := range resp.Data {
			if a.ID == "" {
				continue
			}
			if err := sink.Emit(ctx, s.activityFinding(a)); err != nil {
				return err
			}
		}
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	return nil
}

// cakAbsentFinding emits the honest degradation posture note when the connector runs in
// feed-only mode (Admin API key present, Compliance Access Key absent). It names every
// surface that is OFF and why, so an auditor sees a concrete gap — not silence.
func (s *Source) cakAbsentFinding(at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingKindCoverage,
		Severity:    model.SeverityLow,
		SubjectKind: "claude_compliance",
		SubjectRef:  s.orgRef,
		Title: "Compliance Access Key not configured: org directory (orgs/users/roles/groups/SCIM signal), " +
			"effective-settings attestation (retention/redaction/IP-allowlist/SSO/code-exec-egress), " +
			"content enumeration (chats/projects), and RTBF erase are UNAVAILABLE — " +
			"only the Activity Feed (Admin API key) is active. " +
			"Grant a Compliance Access Key (sk-ant-api01-) with read:compliance_org_data " +
			"(org metadata, roles, groups, effective settings) and read:compliance_user_data " +
			"(user listings, group membership, content enumeration) to enable these surfaces. " +
			"The dedicated read:compliance_org_settings scope was RETIRED (~2026-06-30); " +
			"effective settings now ride under read:compliance_org_data (VERIFIED 2026-07-03).",
		DetailHash: redact.Hash(s.orgRef + "|cak-absent|" +
			"directory=off;settings=off;content=off;rtbf=off;feed=on"),
		OccurredAt: at,
	}
}

// Close releases resources; the connector holds no long-lived connection.
func (s *Source) Close(context.Context) error { return nil }

// activityFinding maps one Activity record to a minimal-data evidence FindingReport.
// The SubjectRef is the non-sensitive activity id (the auditor's handle back to the
// Compliance API); the Title is the activity type; every other field — including the
// actor's ip/user-agent/email — is folded into the one-way DetailHash and NEVER
// surfaced in the clear (docs/SECURITY-HARDENING.md). Severity is Info: this is evidence, not an alert.
func (s *Source) activityFinding(a activity) model.FindingReport {
	// The hash preimage: structural identity + actor SHAPE + any PII, so the hash is a
	// stable, tamper-evident fingerprint an auditor can re-derive — but the values
	// themselves never leave the connector. Anthropic documents forward-compatible
	// handling: "pass through unrecognized type and actor.type values" (VERIFIED
	// 2026-07-03).
	detail := strings.Join([]string{
		a.ID, a.Type, a.CreatedAt, a.OrganizationID,
		a.Actor.Type, a.Actor.IPAddress, a.Actor.UserAgent, a.Actor.EmailAddress, a.Actor.APIKeyID, a.Actor.UserID,
		a.ClaudeChatID, a.ClaudeProjectID,
		// New actor fields are appended only. Reordering the preimage would break
		// historical DetailHash re-derivation.
		a.Actor.AdminAPIKeyID, a.Actor.UnauthenticatedEmailAddress, a.Actor.WorkOSEventID,
		a.Actor.DirectoryID, a.Actor.IDPConnectionType,
	}, "|")
	// Classify the event type into a non-sensitive category (chat/authn/membership/…)
	// so the ledger/SIEM can group and prioritize hundreds of activity types instead of
	// one opaque blob. The category rides in the Title (the model carries no label map);
	// the security-relevance flag is available to consumers via the exported
	// ClassifyActivity (the connector emits Info evidence, not alerts — its stated posture).
	class := ClassifyActivity(a.Type)
	return model.FindingReport{
		Kind:        findingKindActivity,
		Severity:    model.SeverityInfo,
		SubjectKind: "claude_activity",
		SubjectRef:  a.ID,
		Title:       "Claude compliance activity [" + string(class.Category) + "]: " + nonEmpty(a.Type, "activity"),
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.activityTime(a.CreatedAt),
	}
}

// getPage issues one cursor-paginated GET (limit + after_id) against the Activity Feed.
func (s *Source) getPage(ctx context.Context, after string, out any) error {
	q := url.Values{"limit": {defaultPageLimit}}
	if after != "" {
		q.Set("after_id", after)
	}
	return s.client.GetJSON(ctx, activitiesPath, q, out)
}

// activityTime parses the RFC3339 created_at, falling back to the connector clock so a
// record with an unparseable timestamp is still ledgered (with the ingestion time).
func (s *Source) activityTime(created string) time.Time {
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(created)); err == nil {
		return t.UTC()
	}
	return s.clock().UTC()
}

// clock returns the connector's time source (injectable for tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// nonEmpty returns a if non-empty, else b.
func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
