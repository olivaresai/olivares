// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package agent365 is the Olivares AI identity connector for the Microsoft
// Agent 365 registry (GA 2026-05-01). The registry is the package-level
// inventory of every agent an organization runs, including agents without an
// Entra agent identity, so it is complementary to the entra-agent connector
// (the identity-level view of the Entra directory), never a duplicate: this
// connector answers "what agent packages exist", entra-agent answers "which
// per-agent identities exist". Each registry package becomes one
// identitysource.Identity (PrincipalNHI, Kind "registered_agent") in the Graph
// module VI (governance) consumes.
//
// Primary-source facts, verified 2026-07-04 against learn.microsoft.com under
// /microsoft-365/copilot/extensibility/api/admin-settings/package/: the registry
// read surface is Microsoft Graph v1.0 GET
// /v1.0/copilot/admin/catalog/packages and GET
// /v1.0/copilot/admin/catalog/packages/{id}, with application and delegated
// CopilotPackages.Read.All supported for reads. Access requires a Microsoft
// Agent 365 license (standalone per-user add-on, included in Microsoft 365 E7);
// delegated access also needs the AI administrator or Global administrator role.
// The API is available in the global cloud only. The write actions
// (block/unblock/reassign) remain beta/preview delegated-only surfaces and are
// deliberately not implemented here. Pagination is still undocumented on the
// list endpoint; the connector follows @odata.nextLink when present and
// terminates cleanly when absent.
//
// Legacy statement, corrected 2026-07-04: the beta Entra agentRegistry and
// agentInstance Graph APIs are still live with a future-tense deprecation notice
// ("Starting May 2026 ... will be replaced by the Agent Registry APIs powered by
// Microsoft Agent 365") and no hard retirement date; only the Entra admin center
// registry blades were retired on 2026-05-01. Those legacy APIs are deliberately
// not used by this package.
//
// It is read-only and minimal-data (docs/SECURITY-HARDENING.md §2-3): the OAuth2
// client-credentials token POST is the only non-GET call, every Graph call is a
// GET through the shared httpx client, and only package metadata is read: ids,
// display names, package type/status, blocked flag, publisher, version,
// manifest/app/asset ids, descriptions, element-type names, last-modified,
// optional detail categories/sensitivity and access-list counts. It never
// decodes package zipFile, embedded element definition JSON, credentials or
// owner data. Owner, agent instances, risk signals (agents-at-risk/shadow
// agents), usage/run-time metrics, registry sync state and Agent Map are M365
// admin-center portal experiences with no public read API as of 2026-07-04, so
// the connector does not pretend to introspect them.
//
// With no delegated token and incomplete client-credentials settings the
// connector runs offline: Snapshot returns an empty Graph (Source and CapturedAt
// set, nil error) and Gather emits nothing. The statement remains true
// for observations: the registry roster travels via Snapshot, not access-edge
// observations. Gather is no longer a no-op; it emits registry-hygiene
// FindingReports for blocked packages still deployed and external/shared
// packages deployed to all users. It imports only the SDK, the Apache
// identitysource contract and the shared httpx/redact internals, never the
// engine.
//
// Directory inventory through Snapshot is available in Business. Community keeps
// the observation and credential primitives and reports inventory as unavailable.
package agent365

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.agent365"

// Default configuration values.
const (
	defaultBaseURL  = "https://graph.microsoft.com"
	defaultMaxPages = 50
	defaultTimeout  = 30 * time.Second
)

// packagesPath is the Agent 365 package inventory list surface (Graph v1.0;
// verified 2026-07-04 under the Package Management API docs).
const packagesPath = "/v1.0/copilot/admin/catalog/packages"

// Source is the Agent 365 registry connector. It satisfies sdk.SourceConnector
// (registry-hygiene findings) and identitysource.GraphProvider (the registry
// roster).
type Source struct {
	tenantID      string
	clientID      string
	clientSecret  string // Secret: sent only to the token endpoint, never logged
	accessToken   string // delegated bearer token (Secret); takes precedence
	baseURL       string
	oauthTokenURL string // token endpoint override (tests/sovereign)
	maxPages      int
	expandDetails bool
	timeout       time.Duration

	doer httpx.Doer       // injected transport (tests); nil => http.Client{Timeout}
	now  func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector          = (*Source)(nil)
	_ identitysource.GraphProvider = (*Source)(nil)
)

// New returns an Agent 365 registry connector with default configuration.
func New() *Source {
	return &Source{
		baseURL:  defaultBaseURL,
		maxPages: defaultMaxPages,
		timeout:  defaultTimeout,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.2.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Microsoft Agent 365 registry",
		Description: "Reads the Agent 365 registry package inventory via Graph v1.0 using application client-credentials or an operator-supplied delegated token, optionally enriches package details, and emits registry-hygiene posture findings. It reads metadata only (no zipFile, element definitions, credentials, usage, owner or risk/Agent Map portal-only data).",
		ConfigFields: []sdk.ConfigField{
			{Key: "tenant_id", Type: sdk.FieldString, Description: "Entra tenant id (directory id) for application client-credentials. Empty with no access_token = offline (empty graph)."},
			{Key: "client_id", Type: sdk.FieldString, Description: "Entra application (client) id for CopilotPackages.Read.All application permission. Empty with no access_token = offline (empty graph)."},
			{Key: "client_secret", Type: sdk.FieldString, Secret: true, Description: "Entra client secret reference (read-only; never persisted). Empty with no access_token = offline (empty graph)."},
			{Key: "access_token", Type: sdk.FieldString, Secret: true, Description: "Operator-supplied delegated bearer token; takes PRECEDENCE over client-credentials when set."},
			{Key: "base_url", Type: sdk.FieldString, Default: defaultBaseURL, Description: "Microsoft Graph root (global cloud only for this API; override for tests/sovereign clouds)."},
			{Key: "oauth_token_url", Type: sdk.FieldString, Description: "OAuth2 token endpoint override (defaults to login.microsoftonline.com/{tenant_id}/oauth2/v2.0/token)."},
			{Key: "expand_details", Type: sdk.FieldBool, Default: "false", Description: "Opt in to one detail GET per listed package for categories/sensitivity/access-list counts/element-type enrichment (N+1 reads)."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound (@odata.nextLink is undocumented on this endpoint; handled present-or-absent)."},
			{Key: "timeout", Type: sdk.FieldDuration, Default: "30s", Description: "Per-request HTTP timeout."},
		},
	}
}

// Open reads configuration. It never fails for a missing credential: with no
// access_token and incomplete client-credentials settings the connector runs
// offline (Snapshot empty, Gather emits nothing). It does not contact the
// network; token minting belongs to Snapshot/Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.tenantID = strings.TrimSpace(cfg.Get("tenant_id"))
	s.clientID = strings.TrimSpace(cfg.Get("client_id"))
	s.clientSecret = cfg.Get("client_secret")
	s.accessToken = strings.TrimSpace(cfg.Get("access_token"))
	if v := strings.TrimRight(strings.TrimSpace(cfg.Get("base_url")), "/"); v != "" {
		s.baseURL = v
	}
	s.oauthTokenURL = cfg.Get("oauth_token_url")
	s.maxPages = cfg.GetInt("max_pages", s.maxPages)
	if s.maxPages <= 0 {
		s.maxPages = defaultMaxPages
	}
	s.expandDetails = cfg.GetBool("expand_details", s.expandDetails)
	s.timeout = cfg.GetDuration("timeout", s.timeout)
	return nil
}

// Close releases resources; the connector holds no long-lived connection.
func (s *Source) Close(context.Context) error { return nil }

// catalogPackagesResponse is the Graph v1.0 list envelope. @odata.nextLink is
// undocumented on this endpoint, so its presence is handled but never assumed.
type catalogPackagesResponse struct {
	NextLink string           `json:"@odata.nextLink"`
	Value    []copilotPackage `json:"value"`
}

// copilotPackage is the slice of the Graph v1.0 copilotPackage resource the
// connector reads (only the fields it maps; never zipFile, credentials or
// embedded element definitions). Verified 2026-07-04 under the Package
// Management API docs.
type copilotPackage struct {
	ID                   string   `json:"id"`
	DisplayName          string   `json:"displayName"`
	ShortDescription     string   `json:"shortDescription"`
	AssetID              string   `json:"assetId"`
	Type                 string   `json:"type"` // evolvable enum, string-open
	IsBlocked            bool     `json:"isBlocked"`
	Publisher            string   `json:"publisher"`
	Version              string   `json:"version"`
	ManifestID           string   `json:"manifestId"`
	AppID                string   `json:"appId"`
	AvailableTo          string   `json:"availableTo"`  // packageStatus, string-open
	DeployedTo           string   `json:"deployedTo"`   // packageStatus, string-open
	ElementTypes         []string `json:"elementTypes"` // "Bots","DeclarativeAgent",…
	LastModifiedDateTime string   `json:"lastModifiedDateTime"`
}

func (s *Source) listPackages(ctx context.Context, client *httpx.Client) ([]copilotPackage, error) {
	var out []copilotPackage
	path := packagesPath
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp catalogPackagesResponse
		if err := client.GetJSON(ctx, path, nil, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Value...)
		// Pagination is undocumented on this v1.0 endpoint: follow a nextLink
		// when the service sends one, terminate cleanly when it does not.
		if resp.NextLink == "" {
			break
		}
		path = resp.NextLink // absolute and self-contained (httpx uses it verbatim)
	}
	return out, nil
}

// offline reports whether the connector lacks effective auth. A delegated token
// wins over client-credentials when both are configured.
func (s *Source) offline() bool {
	if s.accessToken != "" {
		return false
	}
	return s.tenantID == "" || s.clientID == "" || s.clientSecret == ""
}

// transport returns the injected Doer or a per-request-timeout HTTP client,
// so the declared timeout actually bounds production requests (review fix).
func (s *Source) transport() httpx.Doer {
	if s.doer != nil {
		return s.doer
	}
	return &http.Client{Timeout: s.timeout}
}

// clock returns the connector's time source (injectable for tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}
