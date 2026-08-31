// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"context"
	"os"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.claude-wif"

// Default configuration values.
const (
	defaultBaseURL          = "https://api.anthropic.com"
	defaultAnthropicVersion = "2023-06-01"
	defaultMaxPages         = 20

	// Admin API list endpoints (read-only; require an sk-ant-admin… key).
	pathUsers      = "/v1/organizations/users"
	pathAPIKeys    = "/v1/organizations/api_keys"
	pathWorkspaces = "/v1/organizations/workspaces"
	pathInvites    = "/v1/organizations/invites"
	// pathWorkspaceMembers is formatted with a workspace id.
	pathWorkspaceMembersFmt = "/v1/organizations/workspaces/%s/members"
)

// Source is the Anthropic identity connector. It satisfies sdk.SourceConnector
// (PERMITTED grant edges + the WIF footgun finding) and
// identitysource.GraphProvider (the NHI roster). A single instance serves both:
// Snapshot returns the roster; Gather streams the permitted-grant edges and finding.
type Source struct {
	client   *modelprovider.Client
	adminKey string
	baseURL  string
	version  string
	orgID    string
	wsFilter string
	maxPages int

	// federation holds the operator-declared WIF issuers/rules/service accounts — the
	// governed baseline the reconciliation diffs the live config against.
	federation []FederationRule

	// wifClient lists the org's LIVE federation config (service accounts, issuers, rules)
	// for declared-vs-actual reconciliation. The WIF Admin API endpoints require an
	// org:admin OAuth BEARER token (modelprovider.AuthBearer) and explicitly REJECT the
	// sk-ant-admin Admin API key the roster reads use, so this is a DISTINCT client from
	// s.client (built from org_admin_oauth_token). nil when no org:admin token is
	// configured → reconciliation is simply skipped (honest offline, never a fabricated
	// live roster). It is GET-only by construction (modelprovider.Client), so read-first
	// holds even though an org:admin token could write elsewhere.
	wifClient     *modelprovider.Client
	orgAdminToken string

	doer    modelprovider.Doer          // optional injected transport (tests); nil => default
	now     func() time.Time            // injectable clock (tests); nil => time.Now
	lookEnv func(string) (string, bool) // injectable env lookup (tests); nil => os.LookupEnv
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector          = (*Source)(nil)
	_ identitysource.GraphProvider = (*Source)(nil)
)

// New returns an Anthropic identity connector with default configuration.
func New() *Source {
	return &Source{
		baseURL:  defaultBaseURL,
		version:  defaultAnthropicVersion,
		maxPages: defaultMaxPages,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Claude identity (NHI & WIF)",
		Description: "Models Claude API keys, workspaces, service accounts, org members and WIF issuers as governed NHI (read-only Admin API); emits PERMITTED scope edges and the WIF static-key footgun finding.",
		ConfigFields: []sdk.ConfigField{
			{Key: "admin_key", Type: sdk.FieldString, Secret: true, Description: "Anthropic Admin API key reference (sk-ant-admin…; read-only, never persisted). Empty = roster from declared federation config only."},
			{Key: "base_url", Type: sdk.FieldString, Default: defaultBaseURL, Description: "Anthropic API base URL."},
			{Key: "anthropic_version", Type: sdk.FieldString, Default: defaultAnthropicVersion, Description: "anthropic-version header value."},
			{Key: "organization_id", Type: sdk.FieldString, Description: "Anthropic organization UUID (labels the org collection and the WIF exchange; never invented)."},
			{Key: "workspace_id", Type: sdk.FieldString, Description: "Optional workspace filter: enumerate members/keys for this workspace only (bounds Admin API calls)."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: "20", Description: "Pagination safety bound per list."},
			{Key: "federation", Type: sdk.FieldString, Default: "", Description: "IDN-01/CLA-12: JSON array of operator-declared WIF rules, each modeling an issuer (fdis_)/rule (fdrl_)/service account (svac_) as governed NHI with its oauth_scope as a PERMITTED grant — e.g. [{\"issuer_id\":\"fdis_…\",\"issuer_url\":\"https://oidc.spire.example\",\"rule_id\":\"fdrl_…\",\"service_account_id\":\"svac_…\",\"service_account_name\":\"ci-deployer\",\"oauth_scope\":\"workspace:developer\",\"workspace_id\":\"wrkspc_…\"}]. This is the GOVERNED BASELINE the live reconciliation diffs against (see org_admin_oauth_token). Empty = no federation declared."},
			{Key: "org_admin_oauth_token", Type: sdk.FieldString, Secret: true, Description: "Anthropic org:admin OAuth bearer token reference (read-only, never persisted). Enables declared-vs-actual reconciliation: lists the org's LIVE WIF service accounts/issuers/rules to diff against `federation` and surface drift (undeclared/over-broad/orphan). The WIF Admin API REJECTS the sk-ant-admin admin_key — this is a distinct credential. Empty = no live reconciliation (declared-only graph)."},
		},
	}
}

// Open reads configuration and builds the read-only API clients: the sk-ant-admin Admin
// API client (roster reads) and, when an org:admin OAuth token is configured, the
// AuthBearer WIF client (live federation reconciliation). It never fails for a missing
// credential: with no admin_key the connector still models the declared federation
// NHI/edges and the footgun finding (config-driven, like claude-api's mcp_toolsets), and
// with no org:admin token it simply skips live reconciliation — so identity governance
// flows even offline. A malformed federation declaration fails Open — never silently
// ungoverned.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if v := cfg.Get("base_url"); v != "" {
		s.baseURL = v
	}
	if v := cfg.Get("anthropic_version"); v != "" {
		s.version = v
	}
	s.orgID = cfg.Get("organization_id")
	s.wsFilter = cfg.Get("workspace_id")
	s.maxPages = cfg.GetInt("max_pages", s.maxPages)
	if s.maxPages <= 0 {
		s.maxPages = defaultMaxPages
	}
	s.adminKey = cfg.Get("admin_key")
	s.orgAdminToken = cfg.Get("org_admin_oauth_token")

	rules, err := parseFederation(cfg.Get("federation"))
	if err != nil {
		return err
	}
	s.federation = rules

	headers := map[string]string{"anthropic-version": s.version}
	s.client = modelprovider.NewClient(s.baseURL, s.doer, modelprovider.AuthAnthropicKey, s.adminKey, headers)
	// The WIF Admin API (live federation list) needs an org:admin OAuth bearer, NOT the
	// sk-ant-admin key — a distinct AuthBearer client, built only when the token is set so
	// reconciliation is opt-in and honestly absent otherwise.
	if s.orgAdminToken != "" {
		s.wifClient = modelprovider.NewClient(s.baseURL, s.doer, modelprovider.AuthBearer, s.orgAdminToken, headers)
	}
	return nil
}

// Close releases resources; this connector holds none.
func (s *Source) Close(context.Context) error { return nil }

// clock returns the connector's time source (injectable for tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// envLookup returns the connector's environment accessor (injectable for tests),
// used by the WIF footgun detection to see whether a static key is set.
func (s *Source) envLookup(key string) (string, bool) {
	if s.lookEnv != nil {
		return s.lookEnv(key)
	}
	return os.LookupEnv(key)
}
