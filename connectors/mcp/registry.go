// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// AIP-03 — read-only MCP Registry awareness. The connector optionally consults the
// official MCP Registry to attach PROVENANCE to an introspected server (its
// reverse-DNS namespace + the fact that a publisher verified ownership of that
// namespace) and to flag SHADOW candidates — observed servers it cannot tie to any
// verified namespace (OWASP MCP09, feeding module III drift).
//
// Verified facts (primary source, re-verified 2026-06-10):
//   - The registry is in PREVIEW with "minimal-to-no moderation"; it does NOT vet
//     servers, does NO security scanning, and does NOT remove vulnerable/buggy
//     servers (modelcontextprotocol.io/registry/moderation-policy). Inclusion proves
//     PUBLISHER IDENTITY only (ownership of the namespace via GitHub/DNS/HTTP), NEVER
//     safety. So the connector labels every result "PREVIEW; self-verify" and never
//     treats registry presence as an endorsement (docs/SECURITY-HARDENING.md anti-evasion).
//   - It does NOT accept private servers ("The MCP Registry does not support private
//     servers"; remotes MUST be publicly accessible) — the embedded sub-registry
//     (subregistry.go) exists to serve exactly what the official one rejects.
//   - Names are reverse-DNS (e.g. io.github.user/server, com.example/server).
//   - API: the STABLE, frozen read path is /v0.1 (API freeze since 2025-10-24; /v0 is
//     the un-frozen development alias and /v1 does not exist yet). PINS /v0.1
//     (registryAPIPath) — the same surface GitHub's org/enterprise MCP registries
//     require of a bring-your-own registry. The response NESTS the server record:
//     {"servers":[{"server":{...},"_meta":{"io.modelcontextprotocol.registry/official":
//     {"status":...}}}],"metadata":{...}} (the flat pre-freeze shape parsed no
//     longer exists on the wire — a PREVIEW breaking change absorbed here).
//   - Removed servers are status="deleted" in the registry-managed _meta and excluded
//     by default (we never pass include_deleted on the provenance path).
//
// Read-only and minimal-data: the client performs only GETs, parses metadata, and
// emits findings whose detail is hashed (docs/SECURITY-HARDENING.md). It is gated (opt-in) and
// bounded by a timeout; any failure — including a response whose shape no longer
// parses (the registry is PREVIEW and may break/reset again) — degrades to a single
// Info finding, never a fabricated provenance or a false shadow flag.

// maxRegistryBody caps a registry response body so a hostile/runaway registry
// cannot exhaust memory.
const maxRegistryBody = 8 << 20 // 8 MiB

// registryAPIPath is the PINNED registry API version path. /v0.1 is the
// frozen stable read surface of the official registry AND the version GitHub's
// BYO-registry clients require; it is also what the embedded sub-registry serves.
// Bump deliberately when upstream publishes a stable successor — never silently.
const registryAPIPath = "/v0.1"

// registryClient is a read-only client for an MCP-registry-API read surface (the
// official registry, or any federated subregistry implementing the same OpenAPI).
type registryClient struct {
	client  *http.Client
	baseURL string
}

// newRegistryClient builds a read-only registry client bounded by timeout.
func newRegistryClient(baseURL string, timeout time.Duration) *registryClient {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &registryClient{
		client:  &http.Client{Timeout: timeout},
		baseURL: strings.TrimRight(baseURL, "/"),
	}
}

// registryServer is the subset of a server.json record this client reads (nested
// under "server" in every /v0.1 response).
type registryServer struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
}

// registryRecord is one ServerResponse of the /v0.1 API: the server.json record
// plus the REGISTRY-managed metadata (lifecycle status, isLatest) that lives in the
// response-level `_meta`, not on the record itself.
type registryRecord struct {
	Server registryServer `json:"server"`
	Meta   struct {
		Official struct {
			Status   string `json:"status"`
			IsLatest bool   `json:"isLatest"`
		} `json:"io.modelcontextprotocol.registry/official"`
	} `json:"_meta"`
}

// status returns the record's effective lifecycle status (registry-managed _meta),
// defaulting to "active" when absent.
func (r registryRecord) status() string {
	if s := r.Meta.Official.Status; s != "" {
		return s
	}
	return "active"
}

// listResult is the GET /v0.1/servers envelope (servers + pagination metadata).
type listResult struct {
	Servers  []registryRecord `json:"servers"`
	Metadata struct {
		NextCursor string `json:"nextCursor"`
		Count      int    `json:"count"`
	} `json:"metadata"`
}

// registryProvenance is the resolved provenance for one server.
type registryProvenance struct {
	found     bool
	name      string // registry reverse-DNS name (e.g. io.github.acme/widgets)
	namespace string // reverse-DNS namespace (the part before "/")
}

// searchServers fetches one page of GET /v0.1/servers?search=<term> (deleted servers
// excluded by default — we never request include_deleted on the provenance path).
func (c *registryClient) searchServers(ctx context.Context, term string) ([]registryRecord, error) {
	res, err := c.fetchPage(ctx, term, "", false)
	if err != nil {
		return nil, err
	}
	return res.Servers, nil
}

// fetchPage performs one GET /v0.1/servers request (search term, cursor, limit, and —
// only for the deliberate yank-detection sync path — include_deleted) and returns the
// decoded envelope (servers + nextCursor). It is the single HTTP+decode chokepoint for
// every registry read, bounded by maxRegistryBody.
//
// Shape drift guard (the registry is PREVIEW — breaking changes/resets are announced
// policy): a 2xx page whose records ALL decode without a server name is a response in
// a shape this client no longer understands. That is surfaced as an ERROR so callers
// degrade to their honest "unavailable" finding — silently accepting it would turn
// every configured server into a fabricated shadow flag.
func (c *registryClient) fetchPage(ctx context.Context, term, cursor string, includeDeleted bool) (listResult, error) {
	q := url.Values{}
	if term != "" {
		q.Set("search", term)
	}
	q.Set("limit", "100")
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if includeDeleted {
		q.Set("include_deleted", "true")
	}
	endpoint := c.baseURL + registryAPIPath + "/servers?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return listResult{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return listResult{}, fmt.Errorf("mcp registry: get servers: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxRegistryBody))
		return listResult{}, fmt.Errorf("mcp registry: http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRegistryBody))
	if err != nil {
		return listResult{}, fmt.Errorf("mcp registry: read body: %w", err)
	}
	var res listResult
	if err := json.Unmarshal(body, &res); err != nil {
		return listResult{}, fmt.Errorf("mcp registry: decode: %w", err)
	}
	if len(res.Servers) > 0 && allNamesEmpty(res.Servers) {
		return listResult{}, fmt.Errorf("mcp registry: response shape not recognized (no record carries a server name — registry API drift?)")
	}
	return res, nil
}

// allNamesEmpty reports whether NO record in a non-empty page decoded a server name —
// the signature of an API-shape drift this client must not misread as data.
func allNamesEmpty(records []registryRecord) bool {
	for _, r := range records {
		if strings.TrimSpace(r.Server.Name) != "" {
			return false
		}
	}
	return true
}

// lookup resolves provenance for a server spec. It searches by the operator's
// asserted reverse-DNS RegistryName when present (and claims a match only on EXACT
// name equality, never a fuzzy search hit — a fuzzy match would be fabricated
// provenance); otherwise it searches by the local Name and matches only on exact
// equality, which for reverse-DNS names essentially never happens, so an
// un-asserted server is honestly reported as unresolved (a shadow candidate). A
// deleted entry is treated as not found.
func (c *registryClient) lookup(ctx context.Context, spec serverSpec) (registryProvenance, error) {
	term := spec.RegistryName
	if term == "" {
		term = spec.Name
	}
	records, err := c.searchServers(ctx, term)
	if err != nil {
		return registryProvenance{}, err
	}
	want := spec.RegistryName
	if want == "" {
		want = spec.Name
	}
	for _, r := range records {
		if r.status() != "active" {
			continue // deleted/other lifecycle states are not a verified namespace
		}
		if r.Server.Name == want {
			return registryProvenance{found: true, name: r.Server.Name, namespace: namespaceOf(r.Server.Name)}, nil
		}
	}
	return registryProvenance{found: false}, nil
}

// namespaceOf extracts the reverse-DNS namespace from a registry name (the part
// before the first "/"); returns the whole string when there is no "/".
func namespaceOf(name string) string {
	if i := strings.IndexByte(name, '/'); i >= 0 {
		return name[:i]
	}
	return name
}

// registryFindings resolves provenance for one server and returns the resulting
// catalog/drift findings: a provenance finding when the server resolves to a
// verified namespace, a shadow-candidate finding (OWASP MCP09) when it does not,
// and an Info finding when the registry could not be reached (a gap is a signal,
// not a silent pass — and never a false shadow flag). It is a no-op when registry
// enrichment is disabled.
func (s *Source) registryFindings(ctx context.Context, spec serverSpec, at time.Time) []model.FindingReport {
	if s.reg == nil {
		return nil
	}
	prov, err := s.reg.lookup(ctx, spec)
	if err != nil {
		return []model.FindingReport{registryUnavailableFinding(spec.Name, err, at)}
	}
	if prov.found {
		return []model.FindingReport{provenanceFinding(spec.Name, prov, at)}
	}
	return []model.FindingReport{shadowFinding(spec.Name, at)}
}

// provenanceFinding records the resolved registry provenance, explicitly labeled
// PREVIEW/self-verify: the registry verifies publisher IDENTITY (namespace
// ownership), never server safety, so this is provenance metadata, not an
// endorsement.
func provenanceFinding(server string, p registryProvenance, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingProvenance,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectMCPServer,
		SubjectRef:  server,
		Title:       "MCP server resolved to registry namespace " + p.namespace + " (publisher identity verified; registry PREVIEW — self-verify, not an endorsement)",
		DetailHash:  redact.Hash("mcp-provenance server=" + server + " name=" + p.name + " namespace=" + p.namespace),
		OccurredAt:  at,
	}
}

// shadowFinding flags a server that could not be tied to a verified registry
// namespace as a shadow-MCP-server candidate (OWASP MCP09). It is a CANDIDATE
// (Low) — many legitimate servers (local stdio, private) are simply unpublished;
// the operator clears a known-good server by asserting its registry_name. The
// signal feeds module III drift, exactly the MCP09 value proposition.
func shadowFinding(server string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingShadow,
		Severity:    model.SeverityLow,
		SubjectKind: subjectMCPServer,
		SubjectRef:  server,
		Title:       "[MCP09] Shadow MCP server candidate: not resolvable to a verified registry namespace",
		DetailHash:  redact.Hash("mcp-shadow server=" + server),
		OccurredAt:  at,
	}
}

// registryUnavailableFinding reports that the registry could not be consulted, so
// provenance is unknown for this pass — surfaced (not silent) but NOT treated as a
// shadow flag (absence of data is not evidence of a shadow server).
func registryUnavailableFinding(server string, err error, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingProvenance,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectMCPServer,
		SubjectRef:  server,
		Title:       "MCP Registry provenance unavailable this pass (lookup failed)",
		DetailHash:  redact.Hash("mcp-registry-unavailable server=" + server + " err=" + err.Error()),
		OccurredAt:  at,
	}
}
