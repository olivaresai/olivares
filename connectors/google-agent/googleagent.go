// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package googleagent is the Olivares AI identity connector for Google Agent
// Identity (FED-1): the SPIFFE-based identities Google issues to Gemini
// Enterprise Agent Platform (formerly Vertex AI) Agent Engine reasoning engines
// (GA per the Google IAM launch blog, 2026-05-07). It lists each configured
// location's reasoningEngines collection and exposes the per-agent identities as
// an identitysource.Graph to module VI (governance), converging with the
// connectors/spiffe roster.
//
// Read-only and minimal-data (docs/SECURITY-HARDENING.md-3). Every roster call is a GET through
// the shared httpx client (GET-only by construction). The single POST is the
// standard Google service-account jwt-bearer token exchange against token_url: it
// sends a short-lived RS256-signed assertion, never the private key itself. The
// operator credential (a service-account key JSON) is declared Secret, held only
// in memory, and never logged, persisted or emitted; error messages carry bounded
// provider excerpts and never key material. With no credentials configured the
// connector runs offline: Snapshot returns an empty Graph with Source and
// CapturedAt set (nil error) and Gather emits nothing.
//
// Reasoning-engine wire facts — VERIFIED-RAW 2026-06-11 against the live
// aiplatform v1 discovery document and the Google IAM agent-identity
// documentation; re-verified 2026-07-05 against aiplatform v1 revision 20260627,
// now titled "Agent Platform API". The reasoningEngines collection is unchanged:
//
//   - GET https://{location}-aiplatform.googleapis.com/v1/projects/{project}/locations/{location}/reasoningEngines
//     paged via pageSize/pageToken; the reply is {reasoningEngines, nextPageToken}.
//   - ReasoningEngine carries name (resource path), displayName, createTime and
//     spec.identityType (enum IDENTITY_TYPE_UNSPECIFIED | SERVICE_ACCOUNT |
//     AGENT_IDENTITY), spec.effectiveIdentity (output-only), spec.serviceAccount,
//     spec.agentFramework ("google-adk", "langchain", "langgraph", "ag2",
//     "llama-index", "custom"), and spec.deploymentSpec.agentGatewayConfig's
//     two gateway resource-name links.
//   - For identityType AGENT_IDENTITY, effectiveIdentity is a SCHEME-LESS SPIFFE
//     ID of the form
//     agents.global.org-{ORG_NUMBER}.system.id.goog/resources/aiplatform/projects/{PROJECT_NUMBER}/locations/{LOCATION}/reasoningEngines/{ENGINE}
//     (org-less projects use agents.global.project-{PROJECT_NUMBER}.system.id.goog
//     as the trust domain). The connector prepends "spiffe://" so the Identity.Ref
//     equals the connectors/spiffe roster Ref convention and the two sources
//     converge on one row by external_id.
//   - The documented IAM aggregate binding for all of a project's agents is
//     principalSet://{TRUST_DOMAIN}/attribute.platformContainer/aiplatform/projects/{PROJECT_NUMBER}
//     (VERIFIED against the IAM docs); the connector emits it as one group
//     Collection plus a Membership per agent-identity row.
//   - Auth is the standard service-account jwt-bearer flow (Google OAuth2 docs):
//     an RS256 assertion {iss: client_email, scope:
//     https://www.googleapis.com/auth/cloud-platform, aud: token_url, iat, exp}
//     form-POSTed as grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer to
//     https://oauth2.googleapis.com/token (or the key's token_uri). The existing
//     cloud-platform scope covers the registry and networkservices reads; Agent
//     Registry also documents a narrower agentregistry.read-only scope, but this
//     connector keeps the single established service-account flow.
//
// Registry/gateway facts — VERIFIED-RAW 2026-07-05 against live discovery
// documents: agentregistry.googleapis.com v1 revision 20260623 (GA 2026-06-18)
// exposes read-only list/get/search for Agent Registry agents and MCP servers.
// Agent has no owner field and no approval-state field; the "approved assets"
// posture is curation-by-registration plus IAM, never an invented enum. Agent
// resources carry a full A2A card plus description and skills[] text, and MCP
// tools carry description text; those fields are deliberately not decoded.
// networkservices.googleapis.com v1 revision 20260626 exposes Agent Gateway
// list/get (GA 2026-06-18). The gateway's Model Armor/policy attachment is not
// readable at GA; it exists only as v1alpha1 extensionBindings, so Gather reports
// model_armor_binding=unreadable_at_ga rather than reading the alpha surface.
//
// Classification: the roster still travels through Snapshot, but Gather is
// no longer a no-op. When credentialed and enabled it emits registry/gateway
// posture FindingReports, including shadow-agent detection by comparing
// reasoningEngines to Agent Registry RuntimeReference links. With no credentials
// configured the offline contract remains unchanged: Open succeeds, Snapshot
// returns an empty graph, and Gather emits nothing.
//
// Honest seams. (1) The engines' permitted IAM bindings are IAM policy, out of
// scope here — the permitted side belongs to an IAM-policy source, not this
// roster. (2) A reasoning engine whose effectiveIdentity is empty (still
// provisioning, or not yet migrated) is SKIPPED by Snapshot — no identity is
// invented — and simply absent from the roster; Gather may still report it as a
// shadow engine by resource name when a readable registry exists. (3) Out of
// scope as of the 2026-07-05 pin: agentidentity.googleapis.com (Preview
// 2026-06-18, discovery not publicly readable, gcloud-beta-only reads);
// semanticGovernancePolicies (aiplatform v1, Preview 2026-06-29); the
// aiplatform projects.locations.agents collection for Managed Agents /
// Antigravity harness (Preview, different product); Gemini Enterprise
// assistants/agents in discoveryengine.googleapis.com (separate v1alpha agent
// CRUD surface); agentAnomalyDetectionScopes (v1beta1 scaffolding, no documented
// findings collection); and Agent Platform Threat Detection findings in SCC
// Premium/Enterprise Preview (different ingest path, deferred).
//
// It imports only the SDK, the Apache identitysource contract, the shared
// read-only httpx/redact internals and go-jose (already a module dependency) —
// never the engine.
package googleagent

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.google-agent"

// scheme is the SPIFFE URI scheme the connector prepends to the scheme-less
// effectiveIdentity the API returns, so agent-identity Refs equal the
// connectors/spiffe roster convention.
const scheme = "spiffe://"

// kindServiceAccountAgent is the Identity.Kind for an engine whose identity is a
// service-account email. An SA is possibly SHARED across engines, so this kind is
// a governed NHI but never a firm per-agent attribution signal (unlike
// identitysource.KindAgentIdentity).
const kindServiceAccountAgent = "service_account_agent"

// Default configuration values.
const (
	defaultBasePattern = "https://{location}-aiplatform.googleapis.com"
	defaultLocation    = "us-central1"
	defaultRegistryLoc = "global"
	defaultPageSize    = 100
	defaultMaxPages    = 50
	defaultTimeout     = 30 * time.Second
)

const (
	findingRegistryAgentUnattributed = "google_registry_agent_unattributed"
	findingRegistryEmpty             = "google_registry_empty_with_active_agents"
	findingRegistryUnreadable        = "google_registry_unreadable"
	findingAgentOutsideRegistry      = "google_agent_outside_registry"
	findingRegistryToolDestructive   = "google_registry_tool_destructive"
	findingGatewayPosture            = "google_gateway_posture"
	findingGatewayNoRegistry         = "google_gateway_no_registry"
	findingRegistryPartialCoverage   = "google_registry_partial_coverage"
	findingGatewayPartialCoverage    = "google_gateway_partial_coverage"
)

// Source is the Google Agent Identity connector. It satisfies sdk.SourceConnector
// (registry/gateway posture findings) and identitysource.GraphProvider (the
// roster).
type Source struct {
	key                     *saKey // parsed service-account key; nil => offline
	project                 string
	locations               []string
	tokenURL                string
	baseURL                 string // base pattern; "{location}" is substituted per location
	readRegistry            bool
	registryEndpoint        string
	registryLocations       []string
	readGateways            bool
	networkServicesEndpoint string
	gatewayLocations        []string
	pageSize                int
	maxPages                int
	timeout                 time.Duration

	doer httpx.Doer       // injected transport (tests); nil => http.Client{Timeout}
	now  func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector          = (*Source)(nil)
	_ identitysource.GraphProvider = (*Source)(nil)
)

// New returns a Google Agent Identity connector with default configuration.
func New() *Source {
	return &Source{
		baseURL:                 defaultBasePattern,
		readRegistry:            true,
		registryEndpoint:        defaultRegistryEndpoint,
		registryLocations:       []string{defaultRegistryLoc},
		readGateways:            true,
		networkServicesEndpoint: defaultNetworkServicesEndpoint,
		pageSize:                defaultPageSize,
		maxPages:                defaultMaxPages,
		timeout:                 defaultTimeout,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.2.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Google Agent Identity",
		Description: "Reads Gemini Enterprise Agent Platform reasoning engines and SPIFFE-based identities for the roster, and emits read-only Agent Registry / Agent Gateway posture findings including shadow-agent detection. Metadata only; never credentials or agent-authored card/skill/description text.",
		ConfigFields: []sdk.ConfigField{
			{Key: "credentials_json", Type: sdk.FieldString, Secret: true, Description: "Service-account key JSON ({client_email, private_key, token_uri}; read-only roster scope; held in memory, never persisted). Empty = use credentials_file, or offline (empty graph)."},
			{Key: "credentials_file", Type: sdk.FieldString, Description: "Path to the service-account key JSON file. Used when credentials_json is empty. Empty = offline (empty graph)."},
			{Key: "project", Type: sdk.FieldString, Required: true, Description: "Project id or number, as used in resource names (projects/{project}/locations/...)."},
			{Key: "locations", Type: sdk.FieldString, Default: defaultLocation, Description: "Comma-separated Vertex AI locations to list reasoning engines in."},
			{Key: "token_url", Type: sdk.FieldString, Description: "OAuth2 token endpoint override (tests). Defaults to the key's token_uri or https://oauth2.googleapis.com/token."},
			{Key: "base_url", Type: sdk.FieldString, Description: "API base override (tests). \"{location}\" is substituted; defaults to https://{location}-aiplatform.googleapis.com."},
			{Key: "read_registry", Type: sdk.FieldBool, Default: "true", Description: "List Agent Registry agents and MCP servers during Gather. 403/404 per location is emitted as posture instead of failing the pass."},
			{Key: "registry_endpoint", Type: sdk.FieldString, Default: defaultRegistryEndpoint, Description: "Agent Registry API root override (tests)."},
			{Key: "registry_locations", Type: sdk.FieldString, Default: defaultRegistryLoc, Description: "Comma-separated Agent Registry locations to list, in order. Google examples use global; other locations are unverified."},
			{Key: "read_gateways", Type: sdk.FieldBool, Default: "true", Description: "List Agent Gateways during Gather."},
			{Key: "networkservices_endpoint", Type: sdk.FieldString, Default: defaultNetworkServicesEndpoint, Description: "Network Services API root override for Agent Gateway reads (tests)."},
			{Key: "gateway_locations", Type: sdk.FieldString, Description: "Comma-separated Agent Gateway locations. Empty falls back to locations because gateways are regional and typically co-located with reasoning engines."},
			{Key: "page_size", Type: sdk.FieldInt, Default: strconv.Itoa(defaultPageSize), Description: "reasoningEngines.list pageSize."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound per location."},
			{Key: "timeout", Type: sdk.FieldDuration, Default: "30s", Description: "Per-request HTTP timeout."},
		},
	}
}

// Open reads configuration. It NEVER fails for a missing credential (no
// credentials_json and no credentials_file = offline), only for malformed
// configuration: unparseable key JSON, a key missing client_email/private_key, a
// non-RSA or non-PEM private key, a configured-but-unreadable credentials_file,
// or credentials WITHOUT a project (the project is what the resource names are
// built from). It does not contact the network.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.project = strings.TrimSpace(cfg.Get("project"))
	s.locations = splitLocations(cfg.Get("locations"))
	if base := strings.TrimSpace(cfg.Get("base_url")); base != "" {
		s.baseURL = strings.TrimRight(base, "/")
	}
	s.readRegistry = cfg.GetBool("read_registry", true)
	registryEndpoint := strings.TrimSpace(cfg.Get("registry_endpoint"))
	if registryEndpoint == "" {
		registryEndpoint = defaultRegistryEndpoint
	}
	s.registryEndpoint = strings.TrimRight(registryEndpoint, "/")
	s.registryLocations = splitCSVDefault(cfg.Get("registry_locations"), defaultRegistryLoc)
	s.readGateways = cfg.GetBool("read_gateways", true)
	networkServicesEndpoint := strings.TrimSpace(cfg.Get("networkservices_endpoint"))
	if networkServicesEndpoint == "" {
		networkServicesEndpoint = defaultNetworkServicesEndpoint
	}
	s.networkServicesEndpoint = strings.TrimRight(networkServicesEndpoint, "/")
	s.gatewayLocations = splitCSV(cfg.Get("gateway_locations"))
	if len(s.gatewayLocations) == 0 {
		s.gatewayLocations = append([]string(nil), s.locations...)
	}
	s.pageSize = cfg.GetInt("page_size", s.pageSize)
	if s.pageSize <= 0 {
		s.pageSize = defaultPageSize
	}
	s.maxPages = cfg.GetInt("max_pages", s.maxPages)
	if s.maxPages <= 0 {
		s.maxPages = defaultMaxPages
	}
	s.timeout = cfg.GetDuration("timeout", s.timeout)

	key, err := loadSAKey(cfg.Get("credentials_json"), cfg.Get("credentials_file"))
	if err != nil {
		return err
	}
	s.key = key
	if s.key == nil {
		return nil // offline: nothing else to validate
	}
	if s.project == "" {
		return fmt.Errorf("googleagent: project is required when credentials are configured")
	}
	s.tokenURL = strings.TrimSpace(cfg.Get("token_url"))
	if s.tokenURL == "" {
		s.tokenURL = s.key.TokenURI
	}
	if s.tokenURL == "" {
		s.tokenURL = defaultTokenURL
	}
	return nil
}

// Gather emits Agent Registry / Agent Gateway posture findings. It performs
// fresh reads for each pass and does not cache Snapshot state. With no
// credential it preserves the historical offline contract: nil, no emissions,
// and no HTTP calls.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.key == nil || (!s.readRegistry && !s.readGateways) {
		return nil
	}
	token, err := s.accessToken(ctx)
	if err != nil {
		return err
	}
	at := s.clock().UTC()

	var registry registryInventory
	if s.readRegistry {
		client := httpx.New(s.registryEndpoint, s.transport(), httpx.Bearer(token), nil)
		registry, err = s.readRegistryInventory(ctx, client)
		if err != nil {
			return err
		}
	}

	var engines []reasoningEngine
	registryReadable := len(registry.ReadableLocations) > 0
	if s.readRegistry && registryReadable {
		engines, err = s.gatherReasoningEngines(ctx, token)
		if err != nil {
			return err
		}
	}

	var gateways gatewayInventory
	if s.readGateways {
		client := httpx.New(s.networkServicesEndpoint, s.transport(), httpx.Bearer(token), nil)
		gateways, err = s.readGatewayInventory(ctx, client)
		if err != nil {
			return err
		}
	}

	if s.readRegistry && len(registry.UnreadableLocations) == len(s.registryLocations) {
		if err := sink.Emit(ctx, registryUnreadableFinding(s.project, registry.UnreadableLocations, at)); err != nil {
			return err
		}
	}
	if registryReadable {
		for _, a := range registry.Agents {
			if a.Name == "" || a.runtimePrincipal() != "" {
				continue
			}
			if err := sink.Emit(ctx, registryAgentUnattributedFinding(a, at)); err != nil {
				return err
			}
		}
		if len(registry.Agents) == 0 && len(engines) > 0 {
			if err := sink.Emit(ctx, registryEmptyFinding(s.project, len(engines), at)); err != nil {
				return err
			}
		}

		registeredTails := map[string]bool{}
		for _, a := range registry.Agents {
			if tail := reasoningEngineTail(a.runtimeReferenceURI()); tail != "" {
				registeredTails[tail] = true
			}
		}
		for _, e := range engines {
			// Match on the trailing locations/{l}/reasoningEngines/{id} segment:
			// Agent Registry RuntimeReference URIs carry the PROJECT NUMBER, while
			// this connector's project config may be a project id. Full-path compare
			// would false-positive registered engines as shadow agents.
			if tail := reasoningEngineTail(e.Name); tail != "" && registeredTails[tail] {
				continue
			}
			if err := sink.Emit(ctx, agentOutsideRegistryFinding(e, at)); err != nil {
				return err
			}
		}
		for _, srv := range registry.MCPServers {
			f, ok := registryDestructiveToolsFinding(srv, at)
			if !ok {
				continue
			}
			if err := sink.Emit(ctx, f); err != nil {
				return err
			}
		}
	}

	for _, gw := range gateways.Gateways {
		if len(gw.Registries) == 0 {
			continue
		}
		if err := sink.Emit(ctx, gatewayPostureFinding(gw, gateways.UnreadableLocations, at)); err != nil {
			return err
		}
	}
	for _, gw := range gateways.Gateways {
		if len(gw.Registries) != 0 {
			continue
		}
		if err := sink.Emit(ctx, gatewayNoRegistryFinding(gw, gateways.UnreadableLocations, at)); err != nil {
			return err
		}
	}

	for _, p := range registry.Partials {
		if err := sink.Emit(ctx, registryPartialFinding(p, at)); err != nil {
			return err
		}
	}
	for _, p := range gateways.Partials {
		if err := sink.Emit(ctx, gatewayPartialFinding(p, at)); err != nil {
			return err
		}
	}
	for _, u := range gateways.Unreachable {
		if err := sink.Emit(ctx, gatewayUnreachableFinding(u, at)); err != nil {
			return err
		}
	}
	if registryReadable && len(registry.UnreadableLocations) > 0 {
		if err := sink.Emit(ctx, registryLocationsPartialFinding(s.project, registry.UnreadableLocations, at)); err != nil {
			return err
		}
	}
	return nil
}

// Close releases resources; the connector holds no long-lived connection.
func (s *Source) Close(context.Context) error { return nil }

// Snapshot lists each configured location's reasoning engines read-only and
// assembles the identity graph. With no credentials it returns the offline
// (empty) graph, nil error. It never returns credential material.
func (s *Source) Snapshot(ctx context.Context) (identitysource.Graph, error) {
	g := identitysource.Graph{Source: identitysource.SourceGoogleAgent, CapturedAt: s.clock().UTC()}
	if s.key == nil {
		return g, nil // offline
	}

	token, err := s.accessToken(ctx)
	if err != nil {
		return identitysource.Graph{}, err
	}

	// agentRow keeps what the principalSet derivation needs per AGENT_IDENTITY row.
	type agentRow struct {
		ref           string // the spiffe:// identity Ref
		trustDomain   string
		projectNumber string
	}
	var agentRows []agentRow

	for _, loc := range s.locations {
		// The bearer token lives only on this call's stack and the client's auth
		// closure; it is never logged or persisted. transport(), not s.doer: in
		// production the doer is nil and httpx would fall back to
		// http.DefaultClient, which has NO timeout (review fix).
		client := httpx.New(s.locationBase(loc), s.transport(), httpx.Bearer(token), nil)
		engines, err := s.listEngines(ctx, client, loc)
		if err != nil {
			return identitysource.Graph{}, err
		}
		for _, e := range engines {
			eff := e.Spec.EffectiveIdentity
			if eff == "" {
				// No invented identity, no finding: the engine is still provisioning
				// or was never granted one — it is simply absent from the roster
				// (Snapshot stays stateless; the engine re-polls review fix).
				continue
			}
			display := e.DisplayName
			if display == "" {
				display = lastSegment(e.Name)
			}
			switch e.Spec.IdentityType {
			case identityTypeAgent:
				// Dedicated per-agent SPIFFE identity: the FIRM attribution kind.
				td := trustDomainOf(eff)
				ref := scheme + eff
				g.Identities = append(g.Identities, identitysource.Identity{
					Ref:         ref,
					Type:        identitysource.PrincipalNHI,
					Kind:        identitysource.KindAgentIdentity,
					DisplayName: display,
					Source:      identitysource.SourceGoogleAgent,
					Attributes: pruneAttrs(map[string]string{
						"trust_domain":              td, // survives governance's attribute allowlist (deliberate)
						"framework":                 e.Spec.AgentFramework,
						"resource":                  e.Name,
						"location":                  loc,
						"gateway_client_to_agent":   e.Spec.DeploymentSpec.AgentGatewayConfig.ClientToAgentConfig.AgentGateway,
						"gateway_agent_to_anywhere": e.Spec.DeploymentSpec.AgentGatewayConfig.AgentToAnywhereConfig.AgentGateway,
					}),
				})
				agentRows = append(agentRows, agentRow{ref: ref, trustDomain: td, projectNumber: projectNumberOf(eff)})
			case identityTypeSA, identityTypeUnspecified, "":
				// The identity is an SA email, possibly shared across engines — a
				// governed NHI, never a firm per-agent signal.
				g.Identities = append(g.Identities, identitysource.Identity{
					Ref:         eff,
					Type:        identitysource.PrincipalNHI,
					Kind:        kindServiceAccountAgent,
					DisplayName: display,
					Source:      identitysource.SourceGoogleAgent,
					Attributes: pruneAttrs(map[string]string{
						"framework":                 e.Spec.AgentFramework,
						"resource":                  e.Name,
						"location":                  loc,
						"gateway_client_to_agent":   e.Spec.DeploymentSpec.AgentGatewayConfig.ClientToAgentConfig.AgentGateway,
						"gateway_agent_to_anywhere": e.Spec.DeploymentSpec.AgentGatewayConfig.AgentToAnywhereConfig.AgentGateway,
					}),
				})
			default:
				// Forward-compat: an enum value this connector does not know is treated
				// like SERVICE_ACCOUNT (approximate NHI) — AGENT_IDENTITY firmness is
				// never guessed.
				g.Identities = append(g.Identities, identitysource.Identity{
					Ref:         eff,
					Type:        identitysource.PrincipalNHI,
					Kind:        kindServiceAccountAgent,
					DisplayName: display,
					Source:      identitysource.SourceGoogleAgent,
					Attributes: pruneAttrs(map[string]string{
						"framework":                 e.Spec.AgentFramework,
						"resource":                  e.Name,
						"location":                  loc,
						"gateway_client_to_agent":   e.Spec.DeploymentSpec.AgentGatewayConfig.ClientToAgentConfig.AgentGateway,
						"gateway_agent_to_anywhere": e.Spec.DeploymentSpec.AgentGatewayConfig.AgentToAnywhereConfig.AgentGateway,
					}),
				})
			}
		}
	}

	// The documented IAM aggregate binding for a project's agents:
	// principalSet://{TRUST_DOMAIN}/attribute.platformContainer/aiplatform/projects/{PROJECT_NUMBER}.
	// One Collection per distinct (trust domain, project number) — a single one in
	// practice — plus a membership per agent-identity row.
	seen := map[string]bool{}
	for _, r := range agentRows {
		if r.projectNumber == "" {
			continue // cannot derive the principal set; the identity row stands alone
		}
		ref := "principalSet://" + r.trustDomain + "/attribute.platformContainer/aiplatform/projects/" + r.projectNumber
		if !seen[ref] {
			seen[ref] = true
			g.Collections = append(g.Collections, identitysource.Collection{
				Ref:         ref,
				Kind:        identitysource.KindGroup,
				DisplayName: "aiplatform agents " + s.project,
				Source:      identitysource.SourceGoogleAgent,
				Attributes:  map[string]string{"object": "iam_principal_set"},
			})
		}
		g.Memberships = append(g.Memberships, identitysource.Membership{
			MemberRef:     r.ref,
			MemberKind:    identitysource.MemberIdentity,
			CollectionRef: ref,
			Source:        identitysource.SourceGoogleAgent,
		})
	}
	return g, nil
}

func (s *Source) gatherReasoningEngines(ctx context.Context, token string) ([]reasoningEngine, error) {
	var out []reasoningEngine
	for _, loc := range s.locations {
		client := httpx.New(s.locationBase(loc), s.transport(), httpx.Bearer(token), nil)
		engines, err := s.listEngines(ctx, client, loc)
		if err != nil {
			return nil, err
		}
		out = append(out, engines...)
	}
	return out, nil
}

func registryAgentUnattributedFinding(a registryAgent, at time.Time) model.FindingReport {
	subject := a.Name
	return model.FindingReport{
		Kind:        findingRegistryAgentUnattributed,
		Severity:    model.SeverityLow,
		SubjectKind: "identity",
		SubjectRef:  subject,
		Title:       "registry agent has no runtime identity link: " + registryResourceLabel(a.Name, a.DisplayName),
		DetailHash:  redact.Hash(findingRegistryAgentUnattributed + "|google-agent|" + subject),
		OccurredAt:  at,
	}
}

func registryEmptyFinding(project string, engineCount int, at time.Time) model.FindingReport {
	detail := fmt.Sprintf("%s|google-agent|projects/%s|engines=%d", findingRegistryEmpty, project, engineCount)
	return model.FindingReport{
		Kind:        findingRegistryEmpty,
		Severity:    model.SeverityMedium,
		SubjectKind: "project",
		SubjectRef:  "projects/" + project,
		Title:       "Agent Registry is empty while reasoning engines are active",
		DetailHash:  redact.Hash(detail),
		OccurredAt:  at,
	}
}

func registryUnreadableFinding(project string, locations []string, at time.Time) model.FindingReport {
	detail := fmt.Sprintf("%s|google-agent|projects/%s|locations=%s", findingRegistryUnreadable, project, formatList(locations))
	return model.FindingReport{
		Kind:        findingRegistryUnreadable,
		Severity:    model.SeverityMedium,
		SubjectKind: "project",
		SubjectRef:  "projects/" + project,
		Title:       "Agent Registry unreadable in every configured location (service disabled or roles/agentregistry.viewer missing)",
		DetailHash:  redact.Hash(detail),
		OccurredAt:  at,
	}
}

func agentOutsideRegistryFinding(e reasoningEngine, at time.Time) model.FindingReport {
	subject := engineRosterRef(e)
	return model.FindingReport{
		Kind:        findingAgentOutsideRegistry,
		Severity:    model.SeverityMedium,
		SubjectKind: "identity",
		SubjectRef:  subject,
		Title:       "reasoning engine is outside the readable Agent Registry: " + registryResourceLabel(e.Name, e.DisplayName),
		DetailHash:  redact.Hash(findingAgentOutsideRegistry + "|google-agent|" + e.Name),
		OccurredAt:  at,
	}
}

func registryDestructiveToolsFinding(srv registryMCPServer, at time.Time) (model.FindingReport, bool) {
	var names []string
	destructive := 0
	openWorld := 0
	for _, tool := range srv.Tools {
		flagged := false
		if tool.Annotations.DestructiveHint {
			destructive++
			flagged = true
		}
		if tool.Annotations.OpenWorldHint {
			openWorld++
			flagged = true
		}
		if flagged {
			names = append(names, tool.Name)
		}
	}
	if len(names) == 0 {
		return model.FindingReport{}, false
	}
	sort.Strings(names)
	detail := fmt.Sprintf("%s|google-agent|%s|destructive=%d|open_world=%d|tools=%s",
		findingRegistryToolDestructive, srv.Name, destructive, openWorld, strings.Join(names, ","))
	return model.FindingReport{
		Kind:        findingRegistryToolDestructive,
		Severity:    model.SeverityLow,
		SubjectKind: "mcp_server",
		SubjectRef:  srv.Name,
		Title:       fmt.Sprintf("registry MCP server exposes destructive/open-world tool hints: %s", strings.Join(names, ", ")),
		DetailHash:  redact.Hash(detail),
		OccurredAt:  at,
	}, true
}

func gatewayPostureFinding(gw agentGateway, unreadable []string, at time.Time) model.FindingReport {
	detail := gatewayPostureDetail(gw, unreadable)
	return model.FindingReport{
		Kind:        findingGatewayPosture,
		Severity:    model.SeverityInfo,
		SubjectKind: "agent_gateway",
		SubjectRef:  gw.Name,
		Title:       "Agent Gateway posture: " + lastSegment(gw.Name),
		DetailHash:  redact.Hash(detail),
		OccurredAt:  at,
	}
}

func gatewayNoRegistryFinding(gw agentGateway, unreadable []string, at time.Time) model.FindingReport {
	detail := gatewayPostureDetail(gw, unreadable)
	return model.FindingReport{
		Kind:        findingGatewayNoRegistry,
		Severity:    model.SeverityMedium,
		SubjectKind: "agent_gateway",
		SubjectRef:  gw.Name,
		Title:       "Agent Gateway has no linked Agent Registry: " + lastSegment(gw.Name),
		DetailHash:  redact.Hash(strings.Replace(detail, findingGatewayPosture, findingGatewayNoRegistry, 1)),
		OccurredAt:  at,
	}
}

func gatewayPostureDetail(gw agentGateway, unreadable []string) string {
	selfManaged := gw.SelfManaged.ResourceURI != ""
	accessPath := gw.GoogleManaged.GovernedAccessPath
	detail := fmt.Sprintf("%s|google-agent|%s|access_path=%s self_managed=%t registries=%d model_armor_binding=unreadable_at_ga",
		findingGatewayPosture, gw.Name, accessPath, selfManaged, len(gw.Registries))
	if len(unreadable) > 0 {
		detail += " locations_unreadable=" + formatList(unreadable)
	}
	return detail
}

func registryPartialFinding(p registryPartial, at time.Time) model.FindingReport {
	subject := "agentregistry/" + p.Location + "/" + p.Resource
	detail := fmt.Sprintf("%s|google-agent|%s|reason=%s", findingRegistryPartialCoverage, subject, p.Reason)
	return model.FindingReport{
		Kind:        findingRegistryPartialCoverage,
		Severity:    model.SeverityLow,
		SubjectKind: "coverage",
		SubjectRef:  subject,
		Title:       "Agent Registry partial coverage: " + p.Resource + " in " + p.Location,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  at,
	}
}

func gatewayPartialFinding(p gatewayPartial, at time.Time) model.FindingReport {
	subject := "agentgateway/" + p.Location
	detail := fmt.Sprintf("%s|google-agent|%s|reason=%s", findingGatewayPartialCoverage, subject, p.Reason)
	return model.FindingReport{
		Kind:        findingGatewayPartialCoverage,
		Severity:    model.SeverityLow,
		SubjectKind: "coverage",
		SubjectRef:  subject,
		Title:       "Agent Gateway partial coverage: " + p.Location,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  at,
	}
}

func gatewayUnreachableFinding(u gatewayUnreachable, at time.Time) model.FindingReport {
	subject := "agentgateway/" + u.Location + "/unreachable"
	detail := fmt.Sprintf("%s|google-agent|%s|unreachable=%s", findingGatewayPartialCoverage, subject, formatList(u.Unreachable))
	return model.FindingReport{
		Kind:        findingGatewayPartialCoverage,
		Severity:    model.SeverityLow,
		SubjectKind: "coverage",
		SubjectRef:  subject,
		Title:       "Agent Gateway returned unreachable locations: " + u.Location,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  at,
	}
}

func registryLocationsPartialFinding(project string, locations []string, at time.Time) model.FindingReport {
	subject := "agentregistry/projects/" + project + "/locations"
	detail := fmt.Sprintf("%s|google-agent|%s|locations_unreadable=%s", findingRegistryPartialCoverage, subject, formatList(locations))
	return model.FindingReport{
		Kind:        findingRegistryPartialCoverage,
		Severity:    model.SeverityLow,
		SubjectKind: "coverage",
		SubjectRef:  subject,
		Title:       "Agent Registry partially unreadable",
		DetailHash:  redact.Hash(detail),
		OccurredAt:  at,
	}
}

func engineRosterRef(e reasoningEngine) string {
	eff := strings.TrimSpace(e.Spec.EffectiveIdentity)
	if e.Spec.IdentityType == identityTypeAgent {
		if eff != "" {
			return scheme + eff
		}
		return e.Name
	}
	if eff != "" {
		return eff
	}
	if sa := strings.TrimSpace(e.Spec.ServiceAccount); sa != "" {
		return sa
	}
	return e.Name
}

func reasoningEngineTail(resource string) string {
	const marker = "locations/"
	i := strings.Index(resource, marker)
	if i < 0 {
		return ""
	}
	tail := resource[i:]
	if !strings.Contains(tail, "/reasoningEngines/") {
		return ""
	}
	return tail
}

func formatList(values []string) string {
	return "[" + strings.Join(values, ",") + "]"
}

// listEngines pages one location's reasoningEngines collection (pageSize +
// pageToken/nextPageToken), bounded by max_pages.
func (s *Source) listEngines(ctx context.Context, client *httpx.Client, loc string) ([]reasoningEngine, error) {
	var out []reasoningEngine
	path := "/v1/projects/" + url.PathEscape(s.project) + "/locations/" + url.PathEscape(loc) + "/reasoningEngines"
	pageToken := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		query := url.Values{"pageSize": {strconv.Itoa(s.pageSize)}}
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		var resp reasoningEnginesResponse
		if err := client.GetJSON(ctx, path, query, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.ReasoningEngines...)
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	return out, nil
}

// locationBase substitutes the location into the base pattern
// (https://{location}-aiplatform.googleapis.com by default; an override without
// the placeholder is used verbatim).
func (s *Source) locationBase(loc string) string {
	return strings.ReplaceAll(s.baseURL, "{location}", loc)
}

// splitLocations parses the comma-separated locations setting, trimming spaces
// and dropping empties; the default is us-central1.
func splitLocations(v string) []string {
	out := splitCSV(v)
	if len(out) == 0 {
		out = []string{defaultLocation}
	}
	return out
}

func splitCSVDefault(v, def string) []string {
	out := splitCSV(v)
	if len(out) == 0 {
		return []string{def}
	}
	return out
}

func splitCSV(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// pruneAttrs drops empty values so the attribute map carries only present
// metadata, and returns nil when nothing remains (keeping Snapshots diff-stable).
func pruneAttrs(m map[string]string) map[string]string {
	for k, v := range m {
		if v == "" {
			delete(m, k)
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// transport returns the injected Doer or a per-request-timeout HTTP client.
// The same Doer serves the token POST and every aiplatform GET, so one stub
// drives every call and the declared timeout actually bounds production
// requests (http.DefaultClient has none — Review fix).
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
