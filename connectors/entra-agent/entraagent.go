// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package entraagent is the Olivares AI federation connector for Microsoft
// Entra Agent ID (FED-1). It reads the tenant's agent-identity registry
// via Microsoft Graph v1.0 — agentIdentity service principals (the DEDICATED
// per-agent identities, identitysource.KindAgentIdentity), their
// agentIdentityBlueprint applications (collections), the
// agentIdentityBlueprintPrincipal service principals (the credential-holding
// principal SHARED by all of a blueprint's agents — deliberately NOT
// KindAgentIdentity) and agentUser roster rows (the user-shaped account an agent
// acts through, linked back to its parent agentIdentity by identityParentId) —
// and exposes them as an identitysource.Graph to module VI. Gather emits one
// nhi_longlived_credential FindingReport per blueprint that holds static client
// secrets (passwordCredentials), grounded in the Five Eyes joint guidance
// "Careful adoption of agentic AI services" (2026-05-01): "Replace static,
// long-lived secrets with ephemeral credentials".
//
// It is read-only and minimal-data (docs/SECURITY-HARDENING.md §2-3): the OAuth2 client-credentials
// token POST is the only non-GET call, every Graph call is a GET through the
// shared httpx client, and the connector pulls identity METADATA only — object
// ids, display names, account status, blueprint linkage, ownership refs and
// credential COUNTS/expiry presence — never a credential value (it never decodes
// passwordCredential.secretText/hint/customKeyIdentifier or any keyCredential
// key material). The client secret is declared Secret, held in memory, sent only
// to the token endpoint, and never logged, persisted or echoed into an error.
// With any of tenant_id/client_id/client_secret missing the connector runs
// offline: Snapshot returns an empty Graph (Source and CapturedAt set, nil
// error) and Gather returns nil.
//
// Wire-shape provenance (honest labeling). Every shape below was VERIFIED
// against the learn.microsoft.com Microsoft Graph v1.0 reference on 2026-06-11:
//   - servicePrincipals/microsoft.graph.agentIdentity (server-side OData cast;
//     fields id, displayName, accountEnabled, agentIdentityBlueprintId,
//     createdByAppId, createdDateTime, disabledByMicrosoftStatus,
//     servicePrincipalType=="ServiceIdentity", tags; @odata.nextLink paging,
//     pages of 100).
//   - applications/microsoft.graph.agentIdentityBlueprint (cast; appId is the
//     blueprint key agentIdentityBlueprintId points at).
//   - servicePrincipals/microsoft.graph.agentIdentityBlueprintPrincipal (cast).
//   - .../{id}/microsoft.graph.agentIdentity/owners and /sponsors (the sponsors
//     list's documented app-only least-privilege permission is
//     AgentIdentity.ReadWrite.All, so a read-only registration may get 403 —
//     tolerated per identity, never a snapshot failure).
//   - directory/deletedItems/microsoft.graph.servicePrincipal (soft-deleted;
//     requires Application.Read.All in addition, which is why include_deleted
//     is opt-in). The agentIdentity cast under deletedItems is contradictorily
//     documented, so it is deliberately NOT used; the connector client-filters
//     servicePrincipalType=="ServiceIdentity" instead.
//
// GA-era governance surface (verified against learn.microsoft.com on
// 2026-07-04). Snapshot also reads
// /v1.0/users/microsoft.graph.agentUser with User.ReadBasic.All (v1.0/GA).
// Gather can read /beta/identity/conditionalAccess/policies with Policy.Read.All
// for agent Conditional Access coverage, /beta/identityProtection/riskyAgents
// with IdentityRiskyAgent.Read.All for risky agents (ID Protection; Global cloud
// and Agent 365 licensing may gate this), and
// /v1.0/identityGovernance/entitlementManagement/assignmentPolicies with
// EntitlementManagement.Read.All for entitlement-management policy coverage; the
// sponsorless-agent lifecycle check reuses the pinned v1.0 agent
// owners/sponsors relationship reads. Conditional Access agent-user coverage is
// detected via the documented users.includeUsers=="AllAgentIdUsers" sentinel;
// ordinary users.includeUsers=="All" policies deliberately do not count because
// Microsoft documents that all-users policies do not include agent user
// accounts. Gather can also read the beta-only /beta/auditLogs/signIns agent
// attribution surface when explicitly opted in with ingest_signins=true; by
// default it captures Microsoft's documented servicePrincipal-event slice,
// emits observed agent sign-in access edges, and never decodes user telemetry,
// IP/location/device fields or any other user-shaped sign-in data. The connector
// deliberately does NOT read agentUser sponsors/owners or soft-deleted
// agentUsers because those paths were not verified; does NOT read
// lifecycle-workflow agent-sponsor task GUIDs because they were not verified;
// does NOT attempt the agent-identity Conditional Access "all agents" sentinel
// or per-agent coverage math because that sentinel is undocumented; does NOT
// infer Conditional Access templates because the agent templates have no Graph
// read surface; and does NOT inspect per-policy specificAllowedTargets agent
// subjects because that shape is undocumented.
// ID-Protection actuation is declined by design: this connector remains
// read-only toward identity providers; kill/contain for a risky Microsoft
// agent must happen in Entra, and the finding is the signal to do it.
//
// There is NO orphan-list API: permanently-deleted blueprint principals leave
// their child agent identities orphaned (and soft-deleted), so the connector
// computes orphanhood honestly from the inventory it reads — an agent identity
// whose agentIdentityBlueprintId is not among the live blueprints' appIds is
// marked Attributes["orphaned"]="true". Governance consumes that assertion at
// roster-sync time onto the NHI lifecycle record's registry_orphaned column
// (the sweep ORs it into `orphaned` and emits the nhi_orphaned finding on
// the transition); the owner_ref/sponsor_ref attributes land on the same record
// as the accountable humans, and soft-deleted rows surface as Disabled.
//
// It imports only the SDK, the Apache identitysource contract and the shared
// httpx/redact internals — never the engine.
//
// Directory inventory through Snapshot is available in Business. Community keeps
// the observation and credential primitives and reports inventory as unavailable.
package entraagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.entra-agent"

// KindAgentUser is the user-shaped account an Entra agent acts through. It is
// deliberately not identitysource.KindAgentIdentity: the firm per-agent identity
// row remains the parent microsoft.graph.agentIdentity, linked by
// Attributes["identity_parent_id"].
const KindAgentUser = "agent_user"

// Default configuration values.
const (
	// defaultBaseURL is the Graph root WITHOUT the API version: the connector
	// addresses /v1.0/... paths explicitly (matching the reference URLs it was
	// verified against), so an override (sovereign clouds, tests) replaces only
	// the host.
	defaultBaseURL  = "https://graph.microsoft.com"
	defaultMaxPages = 50
	defaultTimeout  = 30 * time.Second

	// defaultSignInFilter is copied literally from Microsoft's beta Agent ID
	// sign-in logs example, verified 2026-07-04. The example's enum casing
	// ('AgentIdentity') is inconsistent with the beta signIn resource page's
	// agent.agentType values ('agenticAppInstance'), but it is the only
	// wire-tested recipe documented for the servicePrincipal-event slice.
	defaultSignInFilter   = "signInEventTypes/any(t: t eq 'servicePrincipal') and agent/agentType eq 'AgentIdentity'"
	defaultSignInLookback = 24 * time.Hour
)

// graphPageSize is the documented default page size of the servicePrincipals /
// applications list endpoints (Graph v1.0, verified 2026-06-11). It bounds the
// ownership N+1 expansion: at most max_pages*graphPageSize identities are
// expanded.
const graphPageSize = 100

// Source is the Entra Agent ID connector. It satisfies sdk.SourceConnector
// (the blueprint static-secret drift findings) and identitysource.GraphProvider
// (the agent-identity roster).
type Source struct {
	tenantID       string
	clientID       string
	clientSecret   string // Secret: sent only to the token endpoint, never logged
	baseURL        string
	oauthTokenURL  string // token endpoint override (tests)
	maxPages       int
	includeDeleted bool
	expandOwners   bool
	caPosture      bool
	riskPosture    bool
	govPosture     bool
	ingestSignIns  bool
	signInFilter   string
	signInLookback time.Duration
	timeout        time.Duration

	doer httpx.Doer       // injected transport (tests); nil => http.Client{Timeout}
	now  func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector          = (*Source)(nil)
	_ identitysource.GraphProvider = (*Source)(nil)
)

// New returns an Entra Agent ID connector with default configuration.
func New() *Source {
	return &Source{
		baseURL:        defaultBaseURL,
		maxPages:       defaultMaxPages,
		expandOwners:   true,
		caPosture:      true,
		riskPosture:    true,
		govPosture:     true,
		signInFilter:   defaultSignInFilter,
		signInLookback: defaultSignInLookback,
		timeout:        defaultTimeout,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.2.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Microsoft Entra Agent ID",
		Description: "Reads agent identities, agent users, agent blueprints and blueprint principals from Microsoft Entra Agent ID (read-only metadata; never credentials), and emits static-secret plus CA, risky-agent and governance posture findings plus opt-in observed agent sign-in access edges.",
		ConfigFields: []sdk.ConfigField{
			{Key: "tenant_id", Type: sdk.FieldString, Description: "Entra tenant id (directory id). Empty = offline (empty graph)."},
			{Key: "client_id", Type: sdk.FieldString, Description: "Entra application (client) id. Empty = offline (empty graph)."},
			{Key: "client_secret", Type: sdk.FieldString, Secret: true, Description: "Entra client secret reference (read-only; never persisted). Empty = offline (empty graph)."},
			{Key: "base_url", Type: sdk.FieldString, Default: defaultBaseURL, Description: "Microsoft Graph root (without /v1.0); override for sovereign clouds."},
			{Key: "oauth_token_url", Type: sdk.FieldString, Description: "OAuth2 token endpoint override (defaults to login.microsoftonline.com/{tenant_id}/oauth2/v2.0/token)."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound per list call (also bounds ownership expansion to max_pages*100 identities)."},
			{Key: "include_deleted", Type: sdk.FieldBool, Default: "false", Description: "Also read soft-deleted agent identities from directory/deletedItems (requires Application.Read.All in addition; opt-in for that reason)."},
			{Key: "expand_ownership", Type: sdk.FieldBool, Default: "true", Description: "Resolve each agent identity's first user owner/sponsor (N+1 GETs; per-identity 403/404 tolerated)."},
			{Key: "ca_posture", Type: sdk.FieldBool, Default: "true", Description: "Emit Conditional Access posture findings (Gather); requires Policy.Read.All; a 403 skips the leg."},
			{Key: "risk_posture", Type: sdk.FieldBool, Default: "true", Description: "Emit risky-agent posture findings (Gather); requires IdentityRiskyAgent.Read.All; a 403 skips the leg."},
			{Key: "governance_posture", Type: sdk.FieldBool, Default: "true", Description: "Emit ID Governance posture findings (Gather); requires EntitlementManagement.Read.All plus agent owner/sponsor read access; a 403 skips the leg."},
			{Key: "ingest_signins", Type: sdk.FieldBool, Default: "false", Description: "Emit observed agent sign-in access edges from beta auditLogs/signIns (Gather); requires AuditLog.Read.All; a 403 skips the leg."},
			{Key: "signin_filter", Type: sdk.FieldString, Default: defaultSignInFilter, Description: "OData $filter for the sign-in slice; the connector appends the lookback window."},
			{Key: "signin_lookback", Type: sdk.FieldDuration, Default: "24h", Description: "How far back each Gather pass reads; overlapping windows are fine because consumers de-duplicate edges on ObservedAt."},
			{Key: "timeout", Type: sdk.FieldDuration, Default: "30s", Description: "Per-request HTTP timeout."},
		},
	}
}

// Open reads configuration. It never fails for a MISSING credential: with any
// of tenant_id/client_id/client_secret absent the connector runs offline
// (Snapshot returns an empty graph, Gather emits nothing). It does not contact
// the network — the token lifetime belongs to Snapshot/Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.tenantID = strings.TrimSpace(cfg.Get("tenant_id"))
	s.clientID = strings.TrimSpace(cfg.Get("client_id"))
	s.clientSecret = cfg.Get("client_secret")
	if v := strings.TrimRight(cfg.Get("base_url"), "/"); v != "" {
		s.baseURL = v
	}
	s.oauthTokenURL = cfg.Get("oauth_token_url")
	s.maxPages = cfg.GetInt("max_pages", s.maxPages)
	if s.maxPages <= 0 {
		s.maxPages = defaultMaxPages
	}
	s.includeDeleted = cfg.GetBool("include_deleted", s.includeDeleted)
	s.expandOwners = cfg.GetBool("expand_ownership", s.expandOwners)
	s.caPosture = cfg.GetBool("ca_posture", s.caPosture)
	s.riskPosture = cfg.GetBool("risk_posture", s.riskPosture)
	s.govPosture = cfg.GetBool("governance_posture", s.govPosture)
	s.ingestSignIns = cfg.GetBool("ingest_signins", s.ingestSignIns)
	s.signInFilter = strings.TrimSpace(cfg.Get("signin_filter"))
	if s.signInFilter == "" {
		s.signInFilter = defaultSignInFilter
	}
	s.signInLookback = cfg.GetDuration("signin_lookback", s.signInLookback)
	if s.signInLookback <= 0 {
		s.signInLookback = defaultSignInLookback
	}
	s.timeout = cfg.GetDuration("timeout", s.timeout)
	return nil
}

// Close releases resources; the connector holds no long-lived connection.
func (s *Source) Close(context.Context) error { return nil }

// offline reports whether the connector lacks a credential (any of the three
// client-credentials inputs missing). Offline is a declared state, not an error.
func (s *Source) offline() bool {
	return s.tenantID == "" || s.clientID == "" || s.clientSecret == ""
}

// clock returns the connector's time source (injectable for tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// transport returns the injected Doer or a per-request-timeout HTTP client. The
// same Doer serves the token POST and every Graph GET, so a test stubs one.
func (s *Source) transport() httpx.Doer {
	if s.doer != nil {
		return s.doer
	}
	return &http.Client{Timeout: s.timeout}
}

// token performs the OAuth2 client-credentials grant (the connector's only
// non-GET call) against the token endpoint using the same injected transport.
// The access token is held only in memory; a non-2xx is an error carrying the
// status and a bounded 2KiB body excerpt — never the client secret (the request
// form, which holds it, is never included in any error).
func (s *Source) token(ctx context.Context) (string, error) {
	tokenURL := s.oauthTokenURL
	if tokenURL == "" {
		tokenURL = "https://login.microsoftonline.com/" + url.PathEscape(s.tenantID) + "/oauth2/v2.0/token"
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {s.clientID},
		"client_secret": {s.clientSecret},
		"scope":         {"https://graph.microsoft.com/.default"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("entraagent: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.transport().Do(req)
	if err != nil {
		return "", fmt.Errorf("entraagent: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt := redact.ReadHTTPError(resp.Body, 2<<10, req, s.clientSecret)
		return "", fmt.Errorf("entraagent: token endpoint status %d: %s", resp.StatusCode, excerpt)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr); err != nil {
		return "", fmt.Errorf("entraagent: decode token response: %w", err)
	}
	return tr.AccessToken, nil
}

// graphClientFromToken builds a read-only Graph client around an already-fetched
// token. The optional headers are static non-secret headers for a specific leg
// (currently only riskyAgents needs Prefer: include-unknown-enum-members).
func (s *Source) graphClientFromToken(tok string, headers map[string]string) (*httpx.Client, error) {
	if tok == "" {
		return nil, fmt.Errorf("entraagent: token endpoint returned an empty access token")
	}
	// transport(), not s.doer: in production the doer is nil and httpx.New would
	// fall back to http.DefaultClient, which has NO timeout — the declared
	// per-request timeout must bound every Graph GET, not only the token POST.
	// Tests are unaffected (transport() returns the injected doer unchanged).
	return httpx.New(s.baseURL, s.transport(), httpx.Bearer(tok), headers), nil
}

// firstUserRef GETs /v1.0/servicePrincipals/{id}/microsoft.graph.agentIdentity/{rel}
// (rel ∈ owners|sponsors) and returns the id of the FIRST member whose
// "@odata.type" is "#microsoft.graph.user", or "" when there is none — following
// @odata.nextLink (bounded by max_pages), since owners/sponsors may include
// service principals/groups before any user member. A 403/404 is tolerated and
// reported as denied=true: the sponsors list's documented app-only
// least-privilege permission is AgentIdentity.ReadWrite.All, so a read-only
// registration may be denied per identity without that being a snapshot fault.
func (s *Source) firstUserRef(ctx context.Context, client *httpx.Client, id, rel string) (ref string, denied bool, err error) {
	path := "/v1.0/servicePrincipals/" + url.PathEscape(id) + "/microsoft.graph.agentIdentity/" + rel
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		var page directoryObjectPage
		if err := client.GetJSON(ctx, path, nil, &page); err != nil {
			var se *httpx.StatusError
			if errors.As(err, &se) && (se.Status == http.StatusForbidden || se.Status == http.StatusNotFound) {
				return "", true, nil // no access / no such relationship: the attribute is simply absent
			}
			return "", false, err
		}
		for _, obj := range page.Value {
			if obj.ODataType == "#microsoft.graph.user" {
				return obj.ID, false, nil
			}
		}
		if page.NextLink == "" {
			return "", false, nil
		}
		path = page.NextLink // absolute and self-contained; httpx passes it verbatim
	}
	return "", false, nil
}
