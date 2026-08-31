// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package client is a small, self-contained REST client for the Olivares AI
// control plane. It is intentionally decoupled from the engine's Go SDK/core:
// a Terraform provider talks to the running engine purely over HTTP, so the
// request/response shapes are duplicated here against the frozen REST contract
// rather than imported.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrNotFound is returned by GetAgent when the engine responds 404, signaling
// that the resource no longer exists and should be removed from Terraform state.
var ErrNotFound = errors.New("olivares: resource not found")

// defaultTimeout bounds every request. 30s is generous without hanging
// Terraform indefinitely if the engine is unreachable.
const defaultTimeout = 30 * time.Second

// Agent is the wire representation of an agent, matching AgentDTO from the REST
// contract. Maps are decoded as generic objects; Terraform does not model these
// today but they are preserved for forward compatibility and read-back.
type Agent struct {
	ID         string         `json:"id"`
	TenantID   string         `json:"tenant_id"`
	Name       string         `json:"name"`
	Kind       string         `json:"kind"`
	ExternalID string         `json:"external_id"`
	Status     string         `json:"status"`
	Labels     map[string]any `json:"labels"`
	Metadata   map[string]any `json:"metadata"`
	CreatedAt  string         `json:"created_at"`
	UpdatedAt  string         `json:"updated_at"`
	Version    int64          `json:"version"`
}

// agentRequest is the create/update body. It carries only the writable fields
// defined by the contract: name, kind, external_id, status.
type agentRequest struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	ExternalID string `json:"external_id"`
	Status     string `json:"status"`
}

// apiError is the non-2xx error envelope: {"error":{"code":..,"message":..}}.
type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Client is a thin HTTP wrapper around the control plane REST API.
type Client struct {
	endpoint   string
	apiToken   string
	tenant     string
	userAgent  string
	httpClient *http.Client
	// deprecations is the transport-level RFC 9745/8594 recorder installed by
	// New; kept on the Client so DeprecationNotices can expose what was seen.
	deprecations *deprecationRecorder
}

// Options configures New. Endpoint and APIToken are required for a functional
// client; everything else is optional.
type Options struct {
	// Endpoint is the base URL of the control plane API (a trailing slash is
	// trimmed so path concatenation cannot produce a double slash).
	Endpoint string
	// APIToken is sent as the Bearer token on every request.
	APIToken string
	// Tenant, when non-empty, is sent as X-Olivares-Tenant unless a per-call
	// override replaces it.
	Tenant string
	// Version is the provider build version embedded in the User-Agent
	// ("terraform-provider-olivares/<version>") so the control plane can
	// attribute traffic — and, during a deprecation window, target outreach —
	// per provider release. Empty falls back to "dev", mirroring main.go's
	// un-ldflagged default.
	Version string
	// InsecureSkipVerify makes the underlying transport skip TLS verification,
	// which is required to talk to the self-signed dev cert.
	InsecureSkipVerify bool
	// OnDeprecation, when set, is invoked at most once per unique method+path
	// for the lifetime of the client whenever a response carries an RFC 9745
	// Deprecation header. The context is the originating request's, so
	// context-aware loggers (tflog) work from inside the hook.
	OnDeprecation func(ctx context.Context, notice Notice)
}

// New builds a Client from opts. The base transport (default, or the dev-only
// TLS-skipping one) is always wrapped by the deprecation recorder, so every
// response — from present and future call sites alike — passes one choke
// point that watches for the API stability policy's deprecation headers.
func New(opts Options) *Client {
	// http.DefaultTransport is declared as http.RoundTripper, so inference keeps
	// base an interface and the conditional reassignment below still type-checks.
	base := http.DefaultTransport
	if opts.InsecureSkipVerify {
		base = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- opt-in dev-only self-signed cert support (provider "insecure" attribute)
		}
	}
	recorder := &deprecationRecorder{
		next:          base,
		onDeprecation: opts.OnDeprecation,
		seen:          make(map[string]struct{}),
	}

	version := opts.Version
	if version == "" {
		// Defensive: a bare "terraform-provider-olivares/" UA would be worse
		// than an honest "dev" (the same default main.go ships unlinked).
		version = "dev"
	}

	return &Client{
		endpoint:     strings.TrimRight(opts.Endpoint, "/"),
		apiToken:     opts.APIToken,
		tenant:       opts.Tenant,
		userAgent:    "terraform-provider-olivares/" + version,
		httpClient:   &http.Client{Timeout: defaultTimeout, Transport: recorder},
		deprecations: recorder,
	}
}

// DeprecationNotices returns a copy of the deprecation notices this client has
// observed so far (one per unique method+path, in first-seen order). Safe to
// call concurrently with in-flight requests.
func (c *Client) DeprecationNotices() []Notice {
	return c.deprecations.snapshot()
}

// CreateAgent performs POST /v1/agents and returns the created AgentDTO.
// tenantOverride, when non-empty, replaces the client-level tenant for this call.
func (c *Client) CreateAgent(ctx context.Context, tenantOverride string, a Agent) (*Agent, error) {
	return c.writeAgent(ctx, http.MethodPost, c.endpoint+"/v1/agents", tenantOverride, a)
}

// UpdateAgent performs PATCH /v1/agents/{id} and returns the updated AgentDTO.
func (c *Client) UpdateAgent(ctx context.Context, tenantOverride, id string, a Agent) (*Agent, error) {
	return c.writeAgent(ctx, http.MethodPatch, c.endpoint+"/v1/agents/"+id, tenantOverride, a)
}

// GetAgent performs GET /v1/agents/{id}. A 404 returns ErrNotFound so callers
// can drop the resource from state.
func (c *Client) GetAgent(ctx context.Context, tenantOverride, id string) (*Agent, error) {
	req, err := c.newRequest(ctx, http.MethodGet, c.endpoint+"/v1/agents/"+id, tenantOverride, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("olivares: get agent: %w", err)
	}
	defer drainClose(resp.Body)

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	return decodeAgent(resp)
}

// DeleteAgent performs DELETE /v1/agents/{id}, expecting 204. A 404 is treated
// as already-deleted and returns nil.
func (c *Client) DeleteAgent(ctx context.Context, tenantOverride, id string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, c.endpoint+"/v1/agents/"+id, tenantOverride, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("olivares: delete agent: %w", err)
	}
	defer drainClose(resp.Body)

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return errorFromResponse(resp)
}

// writeAgent serializes an agentRequest and issues a create/update call.
func (c *Client) writeAgent(ctx context.Context, method, url, tenantOverride string, agent Agent) (*Agent, error) {
	body, err := json.Marshal(agentRequest{
		Name:       agent.Name,
		Kind:       agent.Kind,
		ExternalID: agent.ExternalID,
		Status:     agent.Status,
	})
	if err != nil {
		return nil, fmt.Errorf("olivares: encode agent: %w", err)
	}
	req, err := c.newRequest(ctx, method, url, tenantOverride, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("olivares: %s agent: %w", strings.ToLower(method), err)
	}
	defer drainClose(resp.Body)

	return decodeAgent(resp)
}

// newRequest builds a request with the auth header and, when a tenant is in
// effect, the X-Olivares-Tenant header. A per-call tenant overrides the
// client-level tenant; an empty per-call tenant falls back to the client's.
func (c *Client) newRequest(ctx context.Context, method, url, tenantOverride string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("olivares: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Accept", "application/json")
	// Identify the exact provider release on every call: the API stability
	// policy keys deprecation telemetry and sunset outreach on client UAs.
	req.Header.Set("User-Agent", c.userAgent)

	tenant := c.tenant
	if tenantOverride != "" {
		tenant = tenantOverride
	}
	if tenant != "" {
		req.Header.Set("X-Olivares-Tenant", tenant)
	}
	return req, nil
}

// decodeAgent reads a 2xx AgentDTO body or maps a non-2xx error envelope.
func decodeAgent(resp *http.Response) (*Agent, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errorFromResponse(resp)
	}
	var agent Agent
	if err := json.NewDecoder(resp.Body).Decode(&agent); err != nil {
		return nil, fmt.Errorf("olivares: decode agent (status %d): %w", resp.StatusCode, err)
	}
	return &agent, nil
}

// errorFromResponse turns a non-2xx response into an error, surfacing the
// envelope's code+message when present, otherwise the raw status/body.
func errorFromResponse(resp *http.Response) error {
	raw, _ := io.ReadAll(resp.Body)
	var env apiError
	if json.Unmarshal(raw, &env) == nil && env.Error.Code != "" {
		return fmt.Errorf("olivares: API error (status %d): %s: %s", resp.StatusCode, env.Error.Code, env.Error.Message)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return fmt.Errorf("olivares: unexpected status %d", resp.StatusCode)
	}
	return fmt.Errorf("olivares: unexpected status %d: %s", resp.StatusCode, trimmed)
}

// drainClose drains and closes a body so the connection can be reused.
func drainClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
}
