// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package agentcore

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"time"
)

// Per-operation maxResults caps — VERIFIED 2026-06-11 from the botocore service
// model: ListWorkloadIdentities and ListOauth2CredentialProviders cap
// maxResults at 20, while ListApiKeyCredentialProviders caps at 100. The
// asymmetry is deliberate upstream; never "harmonize" it (a 100 on the
// 20-capped ops is a ValidationException).
const (
	pageSizeIdentities      = 20
	pageSizeAPIKeyProviders = 100
	pageSizePolicyEngines   = 100
)

// identityListRequest is the RPC body shared by the three /identities list
// operations: a bounded page size and the opaque continuation token.
type identityListRequest struct {
	MaxResults int    `json:"maxResults"`
	NextToken  string `json:"nextToken,omitempty"`
}

// workloadIdentityItem is one ListWorkloadIdentities item. VERIFIED 2026-06-11
// (API reference + botocore model): the item carries ONLY name and
// workloadIdentityArn — the developer guide's createdBy/createdAt list columns
// are STALE doc and DO NOT EXIST on the wire. Timestamps require the
// per-identity GetWorkloadIdentity call (detail mode).
type workloadIdentityItem struct {
	Name                string `json:"name"`
	WorkloadIdentityArn string `json:"workloadIdentityArn"`
}

type listWorkloadIdentitiesResponse struct {
	WorkloadIdentities []workloadIdentityItem `json:"workloadIdentities"`
	NextToken          string                 `json:"nextToken"`
}

// getWorkloadIdentityRequest selects one workload identity by name.
type getWorkloadIdentityRequest struct {
	Name string `json:"name"`
}

// getWorkloadIdentityResponse is the per-identity detail. createdTime and
// lastUpdatedTime are EPOCH SECONDS on the wire (VERIFIED 2026-06-11); the
// connector renders them RFC3339 UTC. The response's
// allowedResourceOauth2ReturnUrls list is deliberately not read (not
// governance metadata).
type getWorkloadIdentityResponse struct {
	Name                string  `json:"name"`
	WorkloadIdentityArn string  `json:"workloadIdentityArn"`
	CreatedTime         float64 `json:"createdTime"`
	LastUpdatedTime     float64 `json:"lastUpdatedTime"`
}

// oauth2ProviderItem is one ListOauth2CredentialProviders item. The ARN lives
// in the "acps" namespace (arn:aws:acps:...:token-vault/...), not
// bedrock-agentcore. The item never carries the client secret — only the vault
// location metadata, which this connector does not read either.
type oauth2ProviderItem struct {
	Name                     string  `json:"name"`
	CredentialProviderArn    string  `json:"credentialProviderArn"`
	CredentialProviderVendor string  `json:"credentialProviderVendor"`
	CreatedTime              float64 `json:"createdTime"`
}

type listOauth2ProvidersResponse struct {
	CredentialProviders []oauth2ProviderItem `json:"credentialProviders"`
	NextToken           string               `json:"nextToken"`
}

// apiKeyProviderItem is one ListApiKeyCredentialProviders item (same acps ARN
// namespace; never the key value).
type apiKeyProviderItem struct {
	Name                  string  `json:"name"`
	CredentialProviderArn string  `json:"credentialProviderArn"`
	CreatedTime           float64 `json:"createdTime"`
}

type listAPIKeyProvidersResponse struct {
	CredentialProviders []apiKeyProviderItem `json:"credentialProviders"`
	NextToken           string               `json:"nextToken"`
}

// policyEngineItem is one GET /policy-engines item (the REST half of the API).
type policyEngineItem struct {
	PolicyEngineArn string `json:"policyEngineArn"`
	PolicyEngineID  string `json:"policyEngineId"`
	Name            string `json:"name"`
	Status          string `json:"status"`
}

type listPolicyEnginesResponse struct {
	PolicyEngines []policyEngineItem `json:"policyEngines"`
	NextToken     string             `json:"nextToken"`
}

// policyItem is one GET /policy-engines/{id}/policies item.
type policyItem struct {
	PolicyArn string `json:"policyArn"`
	PolicyID  string `json:"policyId"`
	Name      string `json:"name"`
	// NOTE: Description and EnforcementMode are passive decode fields for
	// the export planner's ownership/diff checks; Snapshot still emits only the
	// discriminator metadata below.
	Description     string           `json:"description"`
	Status          string           `json:"status"`
	StatusReasons   []string         `json:"statusReasons"`
	EnforcementMode string           `json:"enforcementMode"`
	Definition      policyDefinition `json:"definition"`
}

type listPoliciesResponse struct {
	Policies  []policyItem `json:"policies"`
	NextToken string       `json:"nextToken"`
}

// policyDefinition is the definition tagged union — wire members "cedar" →
// CedarPolicy, "policyGeneration" → PolicyGenerationDetails, and the June GA
// "policy" statement arm (VERIFIED 2026-07-04). Only the DISCRIMINATOR is
// kept in roster output: the
// Cedar statement text is policy content the roster does not carry
// (minimal-data), and its principals are OAuth claims, not roster identities
// (package-doc seam).
type policyDefinition struct {
	Cedar     json.RawMessage `json:"cedar"`
	Generated json.RawMessage `json:"policyGeneration"`
	Policy    json.RawMessage `json:"policy"`
}

// kind returns "cedar", "generated", "policy", or "" when the union arm is absent.
func (d policyDefinition) kind() string {
	switch {
	case len(d.Cedar) > 0 && string(d.Cedar) != "null":
		return "cedar"
	case len(d.Generated) > 0 && string(d.Generated) != "null":
		return "generated"
	case len(d.Policy) > 0 && string(d.Policy) != "null":
		return "policy"
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// Paginated list helpers (every loop is bounded by max_pages and honors ctx).
// ---------------------------------------------------------------------------

// listWorkloadIdentities pages POST /identities/ListWorkloadIdentities via the
// nextToken in the JSON body (cap 20 per page).
func (s *Source) listWorkloadIdentities(ctx context.Context, c *client) ([]workloadIdentityItem, error) {
	var out []workloadIdentityItem
	token := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp listWorkloadIdentitiesResponse
		if err := c.postJSON(ctx, "ListWorkloadIdentities",
			identityListRequest{MaxResults: pageSizeIdentities, NextToken: token}, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.WorkloadIdentities...)
		if resp.NextToken == "" {
			break
		}
		token = resp.NextToken
	}
	return out, nil
}

// getWorkloadIdentity resolves one identity by name (detail mode only).
func (s *Source) getWorkloadIdentity(ctx context.Context, c *client, name string) (getWorkloadIdentityResponse, error) {
	var resp getWorkloadIdentityResponse
	err := c.postJSON(ctx, "GetWorkloadIdentity", getWorkloadIdentityRequest{Name: name}, &resp)
	return resp, err
}

// listOauth2Providers pages POST /identities/ListOauth2CredentialProviders
// (cap 20 per page).
func (s *Source) listOauth2Providers(ctx context.Context, c *client) ([]oauth2ProviderItem, error) {
	var out []oauth2ProviderItem
	token := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp listOauth2ProvidersResponse
		if err := c.postJSON(ctx, "ListOauth2CredentialProviders",
			identityListRequest{MaxResults: pageSizeIdentities, NextToken: token}, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.CredentialProviders...)
		if resp.NextToken == "" {
			break
		}
		token = resp.NextToken
	}
	return out, nil
}

// listAPIKeyProviders pages POST /identities/ListApiKeyCredentialProviders
// (cap 100 per page — deliberately different from the 20 above).
func (s *Source) listAPIKeyProviders(ctx context.Context, c *client) ([]apiKeyProviderItem, error) {
	var out []apiKeyProviderItem
	token := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp listAPIKeyProvidersResponse
		if err := c.postJSON(ctx, "ListApiKeyCredentialProviders",
			identityListRequest{MaxResults: pageSizeAPIKeyProviders, NextToken: token}, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.CredentialProviders...)
		if resp.NextToken == "" {
			break
		}
		token = resp.NextToken
	}
	return out, nil
}

// listPolicyEngines pages GET /policy-engines?maxResults=100[&nextToken=] —
// the REST style: the continuation token travels as a QUERY parameter, not a
// body field.
func (s *Source) listPolicyEngines(ctx context.Context, c *client) ([]policyEngineItem, error) {
	var out []policyEngineItem
	token := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q := url.Values{"maxResults": {strconv.Itoa(pageSizePolicyEngines)}}
		if token != "" {
			q.Set("nextToken", token)
		}
		var resp listPolicyEnginesResponse
		if err := c.getJSON(ctx, "/policy-engines", q, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.PolicyEngines...)
		if resp.NextToken == "" {
			break
		}
		token = resp.NextToken
	}
	return out, nil
}

// listEnginePolicies pages GET /policy-engines/{policyEngineId}/policies. No
// maxResults is sent (the server default applies); pagination follows the
// nextToken query parameter, bounded by max_pages.
func (s *Source) listEnginePolicies(ctx context.Context, c *client, engineID string) ([]policyItem, error) {
	return listEnginePolicies(ctx, c, engineID, s.maxPages)
}

func listEnginePolicies(ctx context.Context, c *client, engineID string, maxPages int) ([]policyItem, error) {
	var out []policyItem
	path := "/policy-engines/" + url.PathEscape(engineID) + "/policies"
	token := ""
	for i := 0; i < maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var q url.Values
		if token != "" {
			q = url.Values{"nextToken": {token}}
		}
		var resp listPoliciesResponse
		if err := c.getJSON(ctx, path, q, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Policies...)
		if resp.NextToken == "" {
			break
		}
		token = resp.NextToken
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Registry wire types (AgentCore Registry interop)
// ---------------------------------------------------------------------------

// Per-operation defaults for Registry pagination. ListRegistries caps at 100
// per page; ListRegistryRecords and SearchRegistryRecords cap at 100 per page.
const (
	pageSizeRegistries = 100
	pageSizeRecords    = 100
	pageSizeGateways   = 100
)

// registryItem is one ListRegistries item (REST GET /registries).
type registryItem struct {
	RegistryID  string  `json:"registryId"`
	RegistryArn string  `json:"registryArn"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Status      string  `json:"status"`
	CreatedAt   float64 `json:"createdAt"`
	UpdatedAt   float64 `json:"updatedAt"`
}

type listRegistriesResponse struct {
	Registries []registryItem `json:"registries"`
	NextToken  string         `json:"nextToken"`
}

// registryRecordItem is one ListRegistryRecords item (REST GET
// /registries/{registryId}/records). The descriptorType discriminates the
// kind of agent (MCP, A2A, CUSTOM, AGENT_SKILLS). Only metadata fields are
// mapped — never the descriptor payload (minimal-data).
type registryRecordItem struct {
	RecordID       string  `json:"recordId"`
	RecordArn      string  `json:"recordArn"`
	Name           string  `json:"name"`
	Description    string  `json:"description"`
	DescriptorType string  `json:"descriptorType"`
	RecordVersion  string  `json:"recordVersion"`
	Status         string  `json:"status"`
	StatusReason   string  `json:"statusReason"`
	CreatedAt      float64 `json:"createdAt"`
	UpdatedAt      float64 `json:"updatedAt"`
}

type listRegistryRecordsResponse struct {
	Records   []registryRecordItem `json:"records"`
	NextToken string               `json:"nextToken"`
}

// gatewayItem is one ListGateways item (REST GET /gateways). Gateways are
// the enforcement points where AgentCore Policy evaluates Cedar policies;
// the policy→gateway attachment is the enforcement-posture signal emits.
type gatewayItem struct {
	GatewayID   string `json:"gatewayId"`
	GatewayArn  string `json:"gatewayArn"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	AuthType    string `json:"authorizerType"`
	Description string `json:"description"`
}

type listGatewaysResponse struct {
	Gateways  []gatewayItem `json:"gateways"`
	NextToken string        `json:"nextToken"`
}

// gatewayPolicyConfig represents the policy engine attached to a gateway.
// Read from GET /gateways/{gatewayId} detail (a sub-field of the full
// gateway response). Only the ARN and enforcement mode are mapped.
type gatewayPolicyConfig struct {
	PolicyEngineArn string `json:"policyEngineArn"`
	EnforcementMode string `json:"enforcementMode"`
}

type getGatewayResponse struct {
	GatewayID    string               `json:"gatewayId"`
	GatewayArn   string               `json:"gatewayArn"`
	Name         string               `json:"name"`
	Status       string               `json:"status"`
	PolicyConfig *gatewayPolicyConfig `json:"policyConfiguration"`
}

// cedarPolicyBody is the Cedar-arm content of the policyDefinition union
// when the definition IS Cedar. The wire shape carries the Cedar source
// text under "statement" (VERIFIED from fixture data). Only the source
// text is mapped; the schema and other metadata are not read here.
type cedarPolicyBody struct {
	Statement string `json:"statement"`
}

// ---------------------------------------------------------------------------
// Registry + Gateway API methods
// ---------------------------------------------------------------------------

// listRegistries pages GET /registries?maxResults=100[&nextToken=].
func (s *Source) listRegistries(ctx context.Context, c *client) ([]registryItem, error) {
	var out []registryItem
	token := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q := url.Values{"maxResults": {strconv.Itoa(pageSizeRegistries)}}
		if token != "" {
			q.Set("nextToken", token)
		}
		var resp listRegistriesResponse
		if err := c.getJSON(ctx, "/registries", q, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Registries...)
		if resp.NextToken == "" {
			break
		}
		token = resp.NextToken
	}
	return out, nil
}

// listRegistryRecords pages GET /registries/{id}/records?maxResults=100.
func (s *Source) listRegistryRecords(ctx context.Context, c *client, registryID string) ([]registryRecordItem, error) {
	var out []registryRecordItem
	path := "/registries/" + url.PathEscape(registryID) + "/records"
	token := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q := url.Values{"maxResults": {strconv.Itoa(pageSizeRecords)}}
		if token != "" {
			q.Set("nextToken", token)
		}
		var resp listRegistryRecordsResponse
		if err := c.getJSON(ctx, path, q, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Records...)
		if resp.NextToken == "" {
			break
		}
		token = resp.NextToken
	}
	return out, nil
}

// listGateways pages GET /gateways?maxResults=100[&nextToken=].
func (s *Source) listGateways(ctx context.Context, c *client) ([]gatewayItem, error) {
	var out []gatewayItem
	token := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q := url.Values{"maxResults": {strconv.Itoa(pageSizeGateways)}}
		if token != "" {
			q.Set("nextToken", token)
		}
		var resp listGatewaysResponse
		if err := c.getJSON(ctx, "/gateways", q, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Gateways...)
		if resp.NextToken == "" {
			break
		}
		token = resp.NextToken
	}
	return out, nil
}

// getGateway reads one gateway's detail (GET /gateways/{id}), which includes
// the policy configuration attachment.
func (s *Source) getGateway(ctx context.Context, c *client, gatewayID string) (getGatewayResponse, error) {
	var resp getGatewayResponse
	err := c.getJSON(ctx, "/gateways/"+url.PathEscape(gatewayID), nil, &resp)
	return resp, err
}

// getCedarPolicyContent reads one Cedar policy's source text. The existing
// listEnginePolicies captures metadata only (name, status, definition kind);
// this method resolves the definition body when it is Cedar.
func (s *Source) getCedarPolicyContent(p policyItem) string {
	if p.Definition.kind() != "cedar" {
		return ""
	}
	var body cedarPolicyBody
	if err := json.Unmarshal(p.Definition.Cedar, &body); err != nil {
		return ""
	}
	return body.Statement
}

// ---------------------------------------------------------------------------
// Mapping helpers
// ---------------------------------------------------------------------------

// epochToRFC3339 renders an epoch-seconds wire timestamp as RFC3339 UTC, or ""
// for the zero value so pruneAttrs drops it.
func epochToRFC3339(sec float64) string {
	if sec <= 0 {
		return ""
	}
	return time.Unix(int64(sec), 0).UTC().Format(time.RFC3339)
}

// pruneAttrs drops empty values so the attribute map carries only present
// metadata, and returns nil when nothing remains (diff-stable snapshots — the
// claude-wif convention, copied locally per the connector-boundary rule).
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
