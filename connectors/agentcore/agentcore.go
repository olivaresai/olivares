// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package agentcore is the Olivares AI connector for AWS Bedrock AgentCore
//. It reads the per-account agent directory of the
// bedrock-agentcore-control plane across four service surfaces:
//
//   - Identity: workload identities, OAuth2/API-key credential providers.
//   - Policy: Cedar policy engines and their policies.
//   - Registry: agent registries and MCP/A2A/CUSTOM/AGENT_SKILLS records.
//   - Gateway: enforcement points and their policy engine attachments.
//
// It exposes the identity graph as an identitysource.Graph with
// Source=SourceAgentCore to module VI (governance), and emits three
// classes of observations via Gather:
//
//   - NHI drift findings: static API-key credential providers flagged per
//     the Five Eyes joint guidance (2026-05-01).
//   - Registry inventory: EdgeObservation per APPROVED registry record
//     (SignalAgentCore) + posture findings for non-healthy records.
//   - Cedar policy posture: engine status, gateway enforcement mode, Cedar
//     coverage, ungoverned gateway warnings.
//   - Export drift and apply-failure posture for Olivares-managed policies.
//   - Evaluations health and guardrail-coverage posture.
//
// Read-only and minimal-data (docs/SECURITY-HARDENING.md-3). Every operation this connector
// invokes is a read (List*/Get*) even where the HTTP verb is POST: the identity
// half of bedrock-agentcore-control is an RPC-style protocol (POST
// /identities/<OperationName> with a JSON body), the policy/registry/gateway
// half is REST GET. The connector pulls METADATA only — names, ARNs, vendor
// labels, timestamps, policy status, Cedar source fingerprints — and NEVER
// touches the token vault's secret material. Cedar policy source text is read
// for hash-based fingerprinting only; it is never emitted or stored verbatim.
//
// Offline behavior: when the region or the access-key pair is missing (after the
// AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY / AWS_SESSION_TOKEN env fallbacks),
// Snapshot returns an EMPTY graph and Gather returns nil — no network call is
// ever attempted.
//
// Cedar bridge: the IMPORT direction reads AgentCore Cedar policies and
// emits posture findings with content fingerprints. The EXPORT direction
// (Olivares Cedar → AgentCore) is defined as type stubs in export.go for a
// future session — the namespace translation (Olivares:: ↔ AgentCore::) is
// non-trivial because AgentCore's entity types (OAuthUser, IamEntity, Action,
// Gateway) do not map 1:1 to Olivares entities (Workspace, AgentGroup, User,
// Role, Group, Resource).
//
// Wire facts — VERIFIED 2026-06-11:
//   - SigV4 signingName is "bedrock-agentcore" (NOT the endpoint prefix).
//   - ListWorkloadIdentities items carry ONLY {name, workloadIdentityArn}.
//   - maxResults caps: 20 for identities/oauth2, 100 for api-key/policies.
//   - Credential-provider ARNs use the "acps" ARN namespace.
//   - Default endpoint: https://bedrock-agentcore-control.<region>.amazonaws.com
//
// It imports only the SDK, the Apache identitysource contract and the shared
// connectors/internal helpers (awssig, httpx.Doer, redact) — never the engine.
package agentcore

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/awssig"
	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.agentcore"

// version is the connector's own semantic version.
const version = "0.1.0"

// Configuration keys (declared in the Descriptor, read in Open).
const (
	cfgRegion              = "region"
	cfgAccessKeyID         = "access_key_id"
	cfgSecretAccessKey     = "secret_access_key"
	cfgSessionToken        = "session_token"
	cfgEndpoint            = "endpoint"
	cfgMaxPages            = "max_pages"
	cfgDetail              = "detail"
	cfgIncludePolicies     = "include_policies"
	cfgTimeout             = "timeout"
	cfgEnableRegistry      = "enable_registry"
	cfgEnablePolicyPosture = "enable_policy_posture"
	cfgEnableExportDrift   = "enable_export_drift"
	cfgEnableEvalPosture   = "enable_eval_posture"
	cfgMaxRegistries       = "max_registries"
	cfgMaxRecords          = "max_records"
	cfgAccountID           = "account_id"
)

// Environment-variable fallbacks for credentials, used when the corresponding
// config field is absent. They mirror the conventional AWS SDK variable names
// (the connectors/aws pattern).
const (
	envAccessKeyID     = "AWS_ACCESS_KEY_ID"
	envSecretAccessKey = "AWS_SECRET_ACCESS_KEY"
	envSessionToken    = "AWS_SESSION_TOKEN"
)

// Defaults.
const (
	defaultMaxPages      = 50
	defaultTimeout       = 30 * time.Second
	defaultMaxRegistries = 50
	defaultMaxRecords    = 200
)

// Source is the AgentCore identity connector. It satisfies sdk.SourceConnector
// (Gather emits long-lived-credential drift findings + registry inventory +
// Cedar policy posture findings) and identitysource.GraphProvider (the
// agent-identity roster).
type Source struct {
	region          string
	accountID       string
	creds           awssig.Creds // in memory only; never logged or emitted
	endpoint        string
	maxPages        int
	detail          bool
	includePolicies bool
	timeout         time.Duration

	enableRegistry      bool
	enablePolicyPosture bool
	enableExportDrift   bool
	enableEvalPosture   bool
	maxRegistries       int
	maxRecords          int

	doer httpx.Doer       // injected transport (tests); nil => http.DefaultClient
	now  func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector          = (*Source)(nil)
	_ identitysource.GraphProvider = (*Source)(nil)
)

// New returns an agentcore connector with default configuration.
func New() *Source {
	return &Source{
		maxPages:            defaultMaxPages,
		includePolicies:     true,
		enableRegistry:      true,
		enablePolicyPosture: true,
		enableExportDrift:   true,
		enableEvalPosture:   true,
		maxRegistries:       defaultMaxRegistries,
		maxRecords:          defaultMaxRecords,
		timeout:             defaultTimeout,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "AWS Bedrock AgentCore Identity + Policy + Registry",
		Description: "Reads AgentCore workload identities, token-vault credential providers, Cedar policy engines, agent registries and gateways (read-only metadata; never credential material). Emits identity roster, long-lived-credential drift findings, registry inventory edges, Cedar policy posture findings and gateway enforcement-mode coverage.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgRegion, Type: sdk.FieldString, Description: "AWS region of the AgentCore control plane. Empty = offline (empty graph)."},
			{Key: cfgAccessKeyID, Type: sdk.FieldString, Secret: true, Description: "AWS access key id (falls back to AWS_ACCESS_KEY_ID). Empty = offline."},
			{Key: cfgSecretAccessKey, Type: sdk.FieldString, Secret: true, Description: "AWS secret access key (falls back to AWS_SECRET_ACCESS_KEY). Empty = offline."},
			{Key: cfgSessionToken, Type: sdk.FieldString, Secret: true, Description: "Optional STS session token (falls back to AWS_SESSION_TOKEN)."},
			{Key: cfgAccountID, Type: sdk.FieldString, Description: "Optional AWS account id, used in posture/health subject refs."},
			{Key: cfgEndpoint, Type: sdk.FieldString, Description: "Endpoint override for tests (default https://bedrock-agentcore-control.<region>.amazonaws.com, constructed by pattern)."},
			{Key: cfgMaxPages, Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound per list call."},
			{Key: cfgDetail, Type: sdk.FieldBool, Default: "false", Description: "Fetch per-identity GetWorkloadIdentity for created/updated timestamps (one extra call per identity; a per-identity 404/403 degrades to the row without timestamps)."},
			{Key: cfgIncludePolicies, Type: sdk.FieldBool, Default: "true", Description: "Include AgentCore Policy engines and policies as collections in the identity roster."},
			{Key: cfgEnableRegistry, Type: sdk.FieldBool, Default: "true", Description: "Emit EdgeObservation for approved AgentCore Registry records and posture findings for non-healthy records."},
			{Key: cfgEnablePolicyPosture, Type: sdk.FieldBool, Default: "true", Description: "Emit Cedar policy posture findings: engine status, gateway enforcement mode, Cedar coverage."},
			{Key: cfgEnableExportDrift, Type: sdk.FieldBool, Default: "true", Description: "Emit export-drift and export apply-failure findings for Olivares-managed AgentCore policies."},
			{Key: cfgEnableEvalPosture, Type: sdk.FieldBool, Default: "true", Description: "Emit evaluations health and guardrail-coverage posture findings."},
			{Key: cfgMaxRegistries, Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxRegistries), Description: "Max registries to scan per pass (safety bound)."},
			{Key: cfgMaxRecords, Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxRecords), Description: "Max registry records to read per registry per pass (safety bound)."},
			{Key: cfgTimeout, Type: sdk.FieldDuration, Default: defaultTimeout.String(), Description: "Per-request HTTP timeout."},
		},
	}
}

// Open reads configuration, applies the AWS env fallbacks, derives the endpoint
// and validates it. It never fails for a MISSING credential or region (that is
// the offline mode); the only error is malformed configuration — an endpoint
// override that is not an absolute http(s) URL. It does not contact the network.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.region = strings.TrimSpace(cfg.Get(cfgRegion))
	s.accountID = strings.TrimSpace(cfg.Get(cfgAccountID))
	s.creds = awssig.Creds{
		AKID:   firstNonEmpty(strings.TrimSpace(cfg.Get(cfgAccessKeyID)), strings.TrimSpace(os.Getenv(envAccessKeyID))),
		Secret: firstNonEmpty(cfg.Get(cfgSecretAccessKey), os.Getenv(envSecretAccessKey)),
		Token:  firstNonEmpty(cfg.Get(cfgSessionToken), os.Getenv(envSessionToken)),
	}
	s.endpoint = strings.TrimRight(strings.TrimSpace(cfg.Get(cfgEndpoint)), "/")
	s.maxPages = cfg.GetInt(cfgMaxPages, defaultMaxPages)
	if s.maxPages <= 0 {
		s.maxPages = defaultMaxPages
	}
	s.detail = cfg.GetBool(cfgDetail, false)
	s.includePolicies = cfg.GetBool(cfgIncludePolicies, true)
	s.enableRegistry = cfg.GetBool(cfgEnableRegistry, true)
	s.enablePolicyPosture = cfg.GetBool(cfgEnablePolicyPosture, true)
	s.enableExportDrift = cfg.GetBool(cfgEnableExportDrift, true)
	s.enableEvalPosture = cfg.GetBool(cfgEnableEvalPosture, true)
	s.maxRegistries = cfg.GetInt(cfgMaxRegistries, defaultMaxRegistries)
	if s.maxRegistries <= 0 {
		s.maxRegistries = defaultMaxRegistries
	}
	s.maxRecords = cfg.GetInt(cfgMaxRecords, defaultMaxRecords)
	if s.maxRecords <= 0 {
		s.maxRecords = defaultMaxRecords
	}
	s.timeout = cfg.GetDuration(cfgTimeout, defaultTimeout)
	if s.timeout <= 0 {
		s.timeout = defaultTimeout
	}

	if s.endpoint != "" {
		u, err := url.Parse(s.endpoint)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("agentcore: endpoint must be an absolute http(s) URL, got %q", s.endpoint)
		}
	} else if s.region != "" {
		s.endpoint = "https://bedrock-agentcore-control." + s.region + ".amazonaws.com"
	}
	return nil
}

// accountScope returns the non-sensitive subject reference for account-scoped
// posture findings. The region is part of the identity because AgentCore
// resources are regional.
func (s *Source) accountScope() string {
	acct := s.accountID
	if acct == "" {
		acct = "aws"
	}
	return acct + "/" + s.region
}

// Close releases resources; the connector holds no long-lived connection.
func (s *Source) Close(context.Context) error { return nil }

// offline reports whether the connector lacks what online mode needs: a region
// (signing scope + endpoint pattern) and the access-key pair (env counts via the
// Open fallbacks). A missing session token does NOT make the connector offline —
// long-lived keys have none.
func (s *Source) offline() bool {
	return s.region == "" || s.creds.AKID == "" || s.creds.Secret == ""
}

// Snapshot reads the AgentCore control plane read-only and assembles the
// identity graph: workload identities (the dedicated per-agent rows), the token
// vault's OAuth2/API-key credential providers (outbound-credential custodians),
// and — when include_policies — the Cedar policy engines and policies as
// collections with policy→engine memberships. Offline it returns the empty
// graph with Source and CapturedAt set, nil error. It never returns credential
// material.
func (s *Source) Snapshot(ctx context.Context) (identitysource.Graph, error) {
	g := identitysource.Graph{Source: identitysource.SourceAgentCore, CapturedAt: s.clock().UTC()}
	if s.offline() {
		return g, nil // offline: no credential/region configured
	}
	c := s.newClient()

	// A) Workload identities — the directory's DEDICATED per-agent primitive,
	// stamped KindWorkloadIdentity so the access-map attribution axis
	// treats them as a FIRM per-agent signal.
	wids, err := s.listWorkloadIdentities(ctx, c)
	if err != nil {
		return identitysource.Graph{}, err
	}
	for _, wi := range wids {
		attrs := map[string]string{"region": s.region}
		if s.detail {
			// Opt-in N+1: the list items carry no timestamps on the wire (the
			// devguide's list columns are stale doc), so detail mode resolves each
			// identity via GetWorkloadIdentity. A per-identity 404 (deleted between
			// list and get — the list/get TOCTOU) or 403 (IAM grants List but not
			// Get) degrades to the listed row WITHOUT timestamps, never a snapshot
			// failure (the entra-agent ownership-expansion precedent); any other
			// status still fails the snapshot.
			det, err := s.getWorkloadIdentity(ctx, c, wi.Name)
			switch {
			case err == nil:
				attrs["created_at"] = epochToRFC3339(det.CreatedTime)
				attrs["updated_at"] = epochToRFC3339(det.LastUpdatedTime)
			case isStatus(err, http.StatusNotFound, http.StatusForbidden):
				// degrade per identity: the row existed at list time
			default:
				return identitysource.Graph{}, err
			}
		}
		g.Identities = append(g.Identities, identitysource.Identity{
			Ref:         wi.WorkloadIdentityArn,
			Type:        identitysource.PrincipalNHI,
			Kind:        identitysource.KindWorkloadIdentity,
			DisplayName: wi.Name,
			Source:      identitysource.SourceAgentCore,
			Attributes:  pruneAttrs(attrs),
		})
	}

	// B) OAuth2 credential providers — custodians of OUTBOUND credentials,
	// deliberately NOT KindWorkloadIdentity (never a firm per-agent signal).
	// NOTE (VERIFIED 2026-06-11): credentialProviderArn lives in the "acps" ARN
	// namespace (arn:aws:acps:<region>:<acct>:token-vault/...), not
	// bedrock-agentcore — never assume the service prefix when parsing the ref.
	oauthProviders, err := s.listOauth2Providers(ctx, c)
	if err != nil {
		return identitysource.Graph{}, err
	}
	for _, p := range oauthProviders {
		g.Identities = append(g.Identities, identitysource.Identity{
			Ref:         p.CredentialProviderArn,
			Type:        identitysource.PrincipalNHI,
			Kind:        "credential_provider",
			DisplayName: p.Name,
			Source:      identitysource.SourceAgentCore,
			Attributes: pruneAttrs(map[string]string{
				"vendor":     p.CredentialProviderVendor,
				"created_at": epochToRFC3339(p.CreatedTime),
			}),
		})
	}

	// C) API-key credential providers — same custodian class; these additionally
	// drive the long-lived-credential drift finding in Gather.
	apiKeyProviders, err := s.listAPIKeyProviders(ctx, c)
	if err != nil {
		return identitysource.Graph{}, err
	}
	for _, p := range apiKeyProviders {
		g.Identities = append(g.Identities, identitysource.Identity{
			Ref:         p.CredentialProviderArn,
			Type:        identitysource.PrincipalNHI,
			Kind:        "apikey_credential_provider",
			DisplayName: p.Name,
			Source:      identitysource.SourceAgentCore,
			Attributes: pruneAttrs(map[string]string{
				"created_at": epochToRFC3339(p.CreatedTime),
			}),
		})
	}

	// D) Registry records as NHI identities — an agent registered in the account's
	// agent directory is a non-human identity with its registry ARN as the ref.
	// Only APPROVED records are roster entries; non-healthy records are posture
	// findings emitted by Gather, not roster identities.
	if s.enableRegistry {
		registries, err := s.listRegistries(ctx, c)
		if err != nil {
			return identitysource.Graph{}, err
		}
		for _, reg := range registries {
			records, err := s.listRegistryRecords(ctx, c, reg.RegistryID)
			if err != nil {
				return identitysource.Graph{}, err
			}
			for _, rec := range records {
				if strings.EqualFold(rec.Status, "APPROVED") {
					g.Identities = append(g.Identities, identitysource.Identity{
						Ref:         rec.RecordArn,
						Type:        identitysource.PrincipalNHI,
						Kind:        identitysource.KindWorkloadIdentity,
						DisplayName: rec.Name,
						Source:      identitysource.SourceAgentCore,
						Attributes: pruneAttrs(map[string]string{
							"registry":        reg.Name,
							"descriptor_type": rec.DescriptorType,
							"version":         rec.RecordVersion,
							"region":          s.region,
						}),
					})
				}
			}
		}
	}

	// E) Policy engines + policies as collections. NO identity→resource permitted
	// edges are derived from Cedar statements: AgentCore Cedar principals are
	// OAuth claims (username/role/scope), not roster identities — fabricating an
	// identity ref would violate the no-invented-identity rule (package doc seam).
	if s.includePolicies {
		engines, err := s.listPolicyEngines(ctx, c)
		if err != nil {
			return identitysource.Graph{}, err
		}
		for _, e := range engines {
			g.Collections = append(g.Collections, identitysource.Collection{
				Ref:         e.PolicyEngineArn,
				Kind:        identitysource.KindGroup,
				DisplayName: e.Name,
				Source:      identitysource.SourceAgentCore,
				Attributes: pruneAttrs(map[string]string{
					"object": "policy_engine",
					"status": e.Status,
				}),
			})
			policies, err := s.listEnginePolicies(ctx, c, e.PolicyEngineID)
			if err != nil {
				return identitysource.Graph{}, err
			}
			for _, p := range policies {
				g.Collections = append(g.Collections, identitysource.Collection{
					Ref:         p.PolicyArn,
					Kind:        identitysource.KindPolicy,
					DisplayName: p.Name,
					Source:      identitysource.SourceAgentCore,
					Attributes: pruneAttrs(map[string]string{
						"status":     p.Status,
						"engine":     e.PolicyEngineID,
						"definition": p.Definition.kind(), // discriminator only, never the statement text
					}),
				})
				g.Memberships = append(g.Memberships, identitysource.Membership{
					MemberRef:     p.PolicyArn,
					MemberKind:    identitysource.MemberCollection,
					CollectionRef: e.PolicyEngineArn,
					Source:        identitysource.SourceAgentCore,
				})
			}
		}
	}
	return g, nil
}

// clock returns the connector's time source (injectable for tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// firstNonEmpty returns the first non-empty argument, or "" if all are empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
