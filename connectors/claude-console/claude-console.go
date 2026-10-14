// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package claudeconsole is the Olivares AI governance connector for the Anthropic
// Claude Console / Claude.ai organization IAM (CLA-13 / IDN-02). It reconciles the
// org identity surface the public Admin API DOES expose — members, invites,
// workspaces and workspace membership — and, crucially, emits an HONEST governance
// finding for what it does NOT: whether org SSO is enforced and whether a departed
// member was SCIM-deprovisioned are not externally observable through the Admin
// API, so the connector flags that blind spot rather than fabricating a posture.
//
// It is read-only and minimal-data (docs/SECURITY-HARDENING.md §0-3): every call is a GET via the
// shared GET-only modelprovider client, it carries identity METADATA only (ids,
// emails, roles, workspace names) and never a credential value (the Admin API
// returns no key secrets), and the admin key is held in memory, never logged or
// persisted. With no key it runs offline (empty roster) but STILL emits the
// blind-spot finding — that finding is a structural truth about the API, not a
// runtime observation. It imports only the SDK and the Apache connector contracts,
// never the engine.
//
// What it deliberately does NOT do (verified against Anthropic primary docs):
//   - It does NOT query api.anthropic.com/scim/v2 — that is not a customer-readable
//     endpoint; SCIM is one-directional IdP→Anthropic push via a WorkOS-hosted base
//     URL, so provisioning STATE is not introspectable from the consumer side.
//   - It does NOT assert "SSO enforced" or synthesize a SCIM roster.
//   - It does NOT gate SSO checks on "Enterprise-only": SSO/JIT are Team+; only
//     SCIM, audit-log export and the full Compliance API are Enterprise-gated.
//
// Directory inventory through Snapshot is available in Business. Community keeps
// the observation and credential primitives and reports inventory as unavailable.
package claudeconsole

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.claude-console"

// SourceClaudeConsole is the identitysource provenance for this connector's roster.
const SourceClaudeConsole = identitysource.SourceKind("claude-console")

// Default configuration values.
const (
	defaultBaseURL          = "https://api.anthropic.com"
	defaultAnthropicVersion = "2023-06-01"
	defaultOrgRef           = "anthropic-console"
	defaultMaxPages         = 50
	defaultPageLimit        = "100"

	usersPath      = "/v1/organizations/users"
	invitesPath    = "/v1/organizations/invites"
	workspacesPath = "/v1/organizations/workspaces"

	// RBAC groups + custom roles are the Claude Enterprise user-management beta
	// surface (VERIFIED 2026-07-20 against platform.claude.com). They ride the same
	// Admin API but require ceUserMgmtBeta and use opaque-cursor pagination.
	rbacGroupsPath = "/v1/organizations/rbac_groups"
	rbacRolesPath  = "/v1/organizations/rbac_roles"

	// ceUserMgmtBeta is the anthropic-beta header value that gates the RBAC
	// groups/custom-roles endpoints (Claude Enterprise). Requests without it 404
	// (VERIFIED 2026-07-20; release note dated 2026-07-14).
	ceUserMgmtBeta = "ce-user-management-2026-07-13"
)

// Source is the Claude Console governance connector. It satisfies
// sdk.SourceConnector (Gather emits the IAM-posture finding) and
// identitysource.GraphProvider (Snapshot returns the member/workspace roster).
type Source struct {
	adminKey string
	baseURL  string
	version  string
	orgRef   string
	maxPages int

	client *modelprovider.Client
	// betaClient is the SAME Admin credential + base, but carries the
	// ce-user-management-2026-07-13 beta header for the RBAC groups/custom-roles
	// reads (which 404 without it). Kept separate so the member/invite/workspace
	// reads never send a beta header they do not need.
	betaClient *modelprovider.Client
	doer       modelprovider.Doer // injected transport (tests); nil => default
	now        func() time.Time   // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector          = (*Source)(nil)
	_ identitysource.GraphProvider = (*Source)(nil)
)

// New returns a Claude Console connector with default configuration.
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
		Title:       "Claude Console (org IAM governance)",
		Description: "Reconciles Claude org members/invites/workspaces via the Admin API and flags the SSO/SCIM observability blind spot (read-only).",
		ConfigFields: []sdk.ConfigField{
			{Key: "admin_key", Type: sdk.FieldString, Secret: true, Description: "Anthropic Admin API key reference (sk-ant-admin01-; read-only; never persisted). Empty = offline (empty roster; the blind-spot finding still emits)."},
			{Key: "base_url", Type: sdk.FieldString, Default: defaultBaseURL, Description: "Anthropic API base URL."},
			{Key: "anthropic_version", Type: sdk.FieldString, Default: defaultAnthropicVersion, Description: "anthropic-version header value."},
			{Key: "org_ref", Type: sdk.FieldString, Default: defaultOrgRef, Description: "Stable reference for the governed Claude org (the finding/roster subject)."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound per list call."},
		},
	}
}

// Open reads configuration and, when an admin key is present, builds the read-only
// Admin client. It contacts no network (the roster lifetime belongs to Snapshot).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.adminKey = cfg.Get("admin_key")
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
	if s.adminKey != "" {
		s.client = modelprovider.NewClient(s.baseURL, s.doer, modelprovider.AuthAnthropicKey, s.adminKey,
			map[string]string{"anthropic-version": s.version})
		// Second client for the CE RBAC beta surface: same key/base, plus the beta
		// header the groups/custom-roles endpoints require (they 404 without it).
		s.betaClient = modelprovider.NewClient(s.baseURL, s.doer, modelprovider.AuthAnthropicKey, s.adminKey,
			map[string]string{"anthropic-version": s.version, "anthropic-beta": ceUserMgmtBeta})
	}
	return nil
}

// Gather emits the two structural IAM-posture findings. Both run UNCONDITIONALLY —
// even offline — because they are structural properties of the Admin API surface, not
// runtime observations: (1) the SSO-enforcement blind spot (recalibrated 2026-07-20 —
// groups + custom roles ARE now listable via the ce-user-management beta, but
// SSO-enforcement state still is not), and (2) the model-access gap (model
// entitlements are Console-only and unreadable via any API, so the provider cannot
// serve as a second enforcement layer for model access). The roster itself travels
// the typed Snapshot, not the bus.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if err := sink.Emit(ctx, s.postureFinding()); err != nil {
		return err
	}
	return sink.Emit(ctx, s.modelAccessGapFinding())
}

// Close releases resources; the connector holds no long-lived connection.
func (s *Source) Close(context.Context) error { return nil }

// postureFinding is the honest governance finding the connector always emits. The
// human-readable detail (hashed, not transmitted) records what the Admin API DOES and
// does NOT expose about org identity. RECALIBRATED 2026-07-20 for the
// ce-user-management-2026-07-13 beta (VERIFIED against platform.claude.com): on
// Claude Enterprise the Admin API now lists org GROUPS (rbac_groups) and CUSTOM ROLES
// (rbac_roles), and a group's source_type EXPOSES scim-vs-direct provisioning AT GROUP
// GRANULARITY — narrowing the old blind spot. What remains unknown-by-api is the org
// SSO-ENFORCEMENT flag (whether SSO is required) and the per-user SCIM-deprovisioning
// EVENT: no endpoint exposes either. Members ARE listable; SSO/JIT are Team+; the
// RBAC beta is Enterprise-only + scope-gated (read:rbac_groups); continuous audit is
// the Compliance Activity Feed, not the Owner-only 180-day Console CSV.
func (s *Source) postureFinding() model.FindingReport {
	detail := s.orgRef + "|admin-api-blind-spot|" +
		"sso_enforcement=unknown-by-api;scim_deprovision_event=unknown-by-api;" +
		"members=listable;groups=listable(ce-user-management-2026-07-13);" +
		"custom_roles=listable(ce-user-management-2026-07-13);" +
		"group_source_type=scim|direct(group-granularity);" +
		"sso_jit=team+;rbac_beta=enterprise+scope(read:rbac_groups);" +
		"audit=compliance-activity-feed(180d-csv=owner-only-console-download)"
	return model.FindingReport{
		Kind:        "iam_posture",
		Severity:    model.SeverityMedium,
		SubjectKind: "claude_org",
		SubjectRef:  s.orgRef,
		Title:       "Claude Admin API now lists org groups + custom roles (ce-user-management beta; group source_type reveals SCIM at group granularity), but org SSO-enforcement state and per-user SCIM-deprovisioning events remain not externally verifiable (members/groups/roles ARE listable; SSO/JIT are Team+; the RBAC beta is Enterprise + scope-gated; continuous audit is the Compliance Activity Feed, not a pollable API).",
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

// modelAccessGapFinding records the honest E2 posture: Anthropic model access /
// model "entitlements" is a CONSOLE-ONLY control with NO readable API (VERIFIED
// 2026-07-20 against support.claude.com/articles/15694740 + the CE user-management
// docs). The two nearest programmatic reads deliberately omit it — the read-only
// custom-role permissions endpoint (GET /v1/organizations/rbac_roles/{id}/permissions)
// states its grant "covers neither model access nor the permission_-prefixed
// admin-panel permissions", and the Compliance effective-settings endpoint returns no
// model-access row. So the provider CANNOT be used as a second enforcement layer for
// which models an org/role/user may call: our own per-seat model-access grant remains
// the sole enforcement point (there is nothing to reconcile a drift against — a gap,
// not a drift). Emitted unconditionally: this is a structural property of the API.
func (s *Source) modelAccessGapFinding() model.FindingReport {
	detail := s.orgRef + "|model-access-gap|" +
		"model_entitlements_api=none(console-only);" +
		"rbac_roles_permissions=readable-but-excludes-model-access;" +
		"compliance_settings=no-model-access-row;" +
		"second_layer_enforcement=not-available;own_per_seat_grant=sole-enforcement"
	return model.FindingReport{
		Kind:        "iam_posture",
		Severity:    model.SeverityInfo,
		SubjectKind: "claude_org",
		SubjectRef:  s.orgRef,
		Title:       "Anthropic model access (entitlements) is Console-only with no readable API — the custom-role permissions read explicitly excludes model access and Compliance effective-settings carries no model-access row, so the provider cannot serve as a second enforcement layer; the org's own per-seat model-access grant is the sole enforcement point.",
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

// clock returns the connector's time source (injectable for tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}
