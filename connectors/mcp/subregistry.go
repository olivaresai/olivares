// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// subregistry.go —: the plane's PRIVATE, tenant-namespaced MCP sub-registry.
//
// Why it exists (verified against primary sources 2026-06-10): the official MCP
// Registry is in PREVIEW and "does not support private servers" — remotes MUST be
// publicly accessible and packages must live in allow-listed PUBLIC package
// registries; its own docs tell private-server owners to "host your own private
// MCP registry". The sanctioned path is NOT forking the official codebase ("not
// designed for self-hosting") but implementing the GENERIC registry OpenAPI
// (modelcontextprotocol/registry docs/reference/api/openapi.yaml, info.version
// 2025-12-01, /v0.1 paths). That same /v0.1 read surface is exactly what GitHub's
// org/enterprise MCP registries require of a bring-your-own registry
// (GET /v0.1/servers, GET /v0.1/servers/{name}/versions/{latest|version}, CORS *),
// so the plane's sub-registry doubles as the org's Copilot allowlist registry.
//
// What it serves: the OPERATOR-APPROVED server set — the internal registry
// (internalregistry.go: the org's source of truth for owned/approved servers)
// elevated from a declared reconciliation list to a SERVED registry. Entries are
// provided per tenant by the composition root (cmd/olivares reads them from
// the gateway provisioning; the store-backed catalog provider is a documented
// seam there). The connector cannot import the AGPL plane (license boundary), so
// this file owns the PROTOCOL only.
//
// Read-only by design: the spec's optional write endpoints (publish/update/
// delete/status) answer 501 with the spec's error shape — entries change through
// the plane's governed approval flow, never through anonymous HTTP writes
// (deny-closed). Reads are anonymous + CORS * (the GitHub client requirement);
// the data served is the operator-CURATED public surface of each entry (names,
// descriptions, install metadata) — no secrets belong in a server.json, and the
// inline-credential guard refuses entries that embed one (docs/SECURITY-HARDENING.md).
//
// Tenant namespacing: /t/{tenant}/v0.1/... serves THAT tenant's entries only;
// the bare /v0.1/... path serves the optional default tenant (single-org
// deployments / a GitHub BYO-registry URL without the prefix). Tenants are
// hard-isolated: an entry exists only inside its tenant's view, and a tenant id
// never rides in a response.

// servedNameRe is the server.json name rule: reverse-DNS namespace, EXACTLY one
// forward slash (the generic-spec pattern, verified against openapi.yaml).
var servedNameRe = regexp.MustCompile(`^[a-zA-Z0-9.-]+/[a-zA-Z0-9._-]+$`)

// Served lifecycle statuses (the generic spec's enum).
const (
	servedStatusActive     = "active"
	servedStatusDeprecated = "deprecated"
	servedStatusDeleted    = "deleted"
)

// ServedServer is one server.json record the sub-registry serves: the ServerDetail
// fields this registry curates (JSON tags are the spec's camelCase, so operator
// provisioning IS a server.json), plus its registry-managed lifecycle (named after
// the official `_meta` keys; stripped from the served record and emitted in
// `_meta`). Repository, Packages, Remotes and Icons are passed through verbatim
// (the operator authors them; the registry serves, never interprets).
type ServedServer struct {
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Version       string          `json:"version"`
	Title         string          `json:"title,omitempty"`
	WebsiteURL    string          `json:"websiteUrl,omitempty"`
	Repository    json.RawMessage `json:"repository,omitempty"`
	Packages      json.RawMessage `json:"packages,omitempty"`
	Remotes       json.RawMessage `json:"remotes,omitempty"`
	Icons         json.RawMessage `json:"icons,omitempty"`
	Status        string          `json:"status,omitempty"`        // active|deprecated|deleted (default active)
	StatusMessage string          `json:"statusMessage,omitempty"` // e.g. a deprecation reason
	PublishedAt   string          `json:"publishedAt,omitempty"`   // RFC3339, optional
	UpdatedAt     string          `json:"updatedAt,omitempty"`     // RFC3339, optional
}

// SubRegistryTenant is one tenant's served view: its entries and (optionally) the
// reverse-DNS namespaces the tenant owns — when declared, every entry must sit
// under one of them, so a tenant cannot serve names under another org's namespace.
type SubRegistryTenant struct {
	OwnedNamespaces []string       `json:"owned_namespaces,omitempty"`
	Servers         []ServedServer `json:"servers"`
}

// SubRegistryConfig is the operator provisioning for the embedded sub-registry.
type SubRegistryConfig struct {
	// DefaultTenant, when set, is served on the un-prefixed /v0.1 paths (it must
	// exist in Tenants). Empty ⇒ only /t/{tenant}/v0.1 paths resolve.
	DefaultTenant string                       `json:"default_tenant,omitempty"`
	Tenants       map[string]SubRegistryTenant `json:"tenants"`
}

// SubRegistry is the embedded read-only MCP registry handler (generic registry
// OpenAPI /v0.1, tenant-namespaced). Build with NewSubRegistry; mount anywhere
// (the composition root strips its mount prefix).
type SubRegistry struct {
	defaultTenant string
	tenants       map[string][]servedRecord
}

// servedRecord is a validated entry with its computed registry-managed metadata.
type servedRecord struct {
	server   ServedServer
	isLatest bool
}

// NewSubRegistry validates the provisioning and builds the handler. Validation is
// deny-closed: ANY invalid entry fails construction (an invalid private registry
// must not mount half-served), duplicates (name, version) are configuration
// errors, and a tenant with owned namespaces declared may only serve names under
// them.
func NewSubRegistry(cfg SubRegistryConfig) (*SubRegistry, error) {
	if len(cfg.Tenants) == 0 {
		return nil, fmt.Errorf("mcp subregistry: no tenants provisioned")
	}
	if cfg.DefaultTenant != "" {
		if _, ok := cfg.Tenants[cfg.DefaultTenant]; !ok {
			return nil, fmt.Errorf("mcp subregistry: default_tenant %q is not a provisioned tenant", cfg.DefaultTenant)
		}
	}
	out := &SubRegistry{defaultTenant: cfg.DefaultTenant, tenants: map[string][]servedRecord{}}
	for tenant, tc := range cfg.Tenants {
		if strings.TrimSpace(tenant) == "" || strings.ContainsAny(tenant, "/ ") {
			return nil, fmt.Errorf("mcp subregistry: invalid tenant id %q", tenant)
		}
		owned := map[string]struct{}{}
		for _, ns := range tc.OwnedNamespaces {
			if ns = strings.ToLower(strings.TrimSpace(ns)); ns != "" {
				owned[ns] = struct{}{}
			}
		}
		records, err := validateServed(tenant, tc.Servers, owned)
		if err != nil {
			return nil, err
		}
		out.tenants[tenant] = records
	}
	return out, nil
}

// validateServed validates one tenant's entries and computes isLatest per name.
func validateServed(tenant string, servers []ServedServer, owned map[string]struct{}) ([]servedRecord, error) {
	seen := map[string]struct{}{}
	records := make([]servedRecord, 0, len(servers))
	for i, s := range servers {
		s.Name = strings.TrimSpace(s.Name)
		s.Description = strings.TrimSpace(s.Description)
		s.Version = strings.TrimSpace(s.Version)
		if !servedNameRe.MatchString(s.Name) || len(s.Name) < 3 || len(s.Name) > 200 {
			return nil, fmt.Errorf("mcp subregistry: tenant %s entry #%d: name %q is not a valid reverse-DNS server name (namespace/name, exactly one slash)", tenant, i, s.Name)
		}
		if s.Description == "" {
			return nil, fmt.Errorf("mcp subregistry: tenant %s entry %s: description is required (server.json requires it)", tenant, s.Name)
		}
		if s.Version == "" || strings.EqualFold(s.Version, "latest") || strings.EqualFold(s.Version, "versions") {
			return nil, fmt.Errorf("mcp subregistry: tenant %s entry %s: version is required and must not be the literal %q (reserved by the /v0.1 URL grammar)", tenant, s.Name, s.Version)
		}
		switch s.Status {
		case "":
			s.Status = servedStatusActive
		case servedStatusActive, servedStatusDeprecated, servedStatusDeleted:
		default:
			return nil, fmt.Errorf("mcp subregistry: tenant %s entry %s: status %q is not active|deprecated|deleted", tenant, s.Name, s.Status)
		}
		if len(owned) > 0 {
			if _, ok := owned[strings.ToLower(namespaceOf(s.Name))]; !ok {
				return nil, fmt.Errorf("mcp subregistry: tenant %s entry %s: namespace %s is not among the tenant's owned namespaces", tenant, s.Name, namespaceOf(s.Name))
			}
		}
		// A served record is install metadata, never a secret carrier: refuse an
		// entry that embeds an inline credential shape anywhere in its raw fields.
		for _, raw := range []json.RawMessage{s.Repository, s.Packages, s.Remotes, s.Icons} {
			if containsSecretShape(string(raw)) {
				return nil, fmt.Errorf("mcp subregistry: tenant %s entry %s: an entry field embeds a credential/secret shape; reference secrets by name, never inline", tenant, s.Name)
			}
		}
		key := s.Name + "@" + s.Version
		if _, dup := seen[key]; dup {
			return nil, fmt.Errorf("mcp subregistry: tenant %s: duplicate entry %s", tenant, key)
		}
		seen[key] = struct{}{}
		records = append(records, servedRecord{server: s})
	}
	// Deterministic order: name asc, then version asc (the official registry's
	// visible cursor order). Then mark the newest NON-deleted version per name.
	sort.Slice(records, func(i, j int) bool {
		if records[i].server.Name != records[j].server.Name {
			return records[i].server.Name < records[j].server.Name
		}
		return versionLess(records[i].server.Version, records[j].server.Version)
	})
	latest := map[string]int{}
	for i, r := range records {
		if r.server.Status == servedStatusDeleted {
			continue
		}
		if li, ok := latest[r.server.Name]; !ok || versionLess(records[li].server.Version, r.server.Version) {
			latest[r.server.Name] = i
		}
	}
	// A name whose EVERY version is deleted still needs a latest, or the sync view
	// (include_deleted, the yank-visibility path) could never resolve
	// /versions/latest for it; the default surface is unaffected because the
	// deleted-status filters fire before any isLatest match.
	for i, r := range records {
		li, ok := latest[r.server.Name]
		if ok && records[li].server.Status != servedStatusDeleted {
			continue // a live latest always wins; this pass only fills all-deleted names
		}
		if !ok || versionLess(records[li].server.Version, r.server.Version) {
			latest[r.server.Name] = i
		}
	}
	for _, i := range latest {
		records[i].isLatest = true
	}
	return records, nil
}

// containsSecretShape is a light inline-credential guard over raw JSON text.
func containsSecretShape(raw string) bool {
	if raw == "" {
		return false
	}
	lower := strings.ToLower(raw)
	for _, marker := range []string{"-----begin", "sk-ant-", "client_secret\":", "\"password\":", "bearer "} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// versionLess orders two version strings as a TOTAL order (sort.Slice and the
// isLatest running-max both require strict-weak ordering — a mixed comparator
// that flips strategies per pair would cycle): non-semver strings sort strictly
// BEFORE semver ones (so a genuine semver always wins "latest" over a
// non-parseable version), semver pairs compare numerically (with "1.2.3-pre" <
// "1.2.3"), non-semver pairs lexicographically — honest best-effort, matching
// the spec's "non-semantic versions may not sort predictably".
func versionLess(a, b string) bool {
	an, apre, aok := splitSemver(a)
	bn, bpre, bok := splitSemver(b)
	if aok != bok {
		return !aok // the non-semver operand sorts first (semver wins latest)
	}
	if aok {
		for i := 0; i < 3; i++ {
			if an[i] != bn[i] {
				return an[i] < bn[i]
			}
		}
		if (apre == "") != (bpre == "") {
			return apre != "" // a prerelease sorts before its release
		}
		return apre < bpre
	}
	return a < b
}

// splitSemver parses "MAJOR.MINOR.PATCH[-pre][+build]" into its numeric triple.
func splitSemver(v string) ([3]int, string, bool) {
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i] // build metadata never orders
	}
	pre := ""
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v, pre = v[:i], v[i+1:]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return [3]int{}, "", false
	}
	var nums [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, "", false
		}
		nums[i] = n
	}
	return nums, pre, true
}

// --- HTTP surface (generic registry OpenAPI /v0.1) ----------------------------

// servedResponse is the spec's ServerResponse: the record under "server" plus the
// REGISTRY-managed metadata under the spec-defined `_meta` key.
type servedResponse struct {
	Server ServedServer   `json:"server"`
	Meta   map[string]any `json:"_meta"`
}

// servedListResponse is the spec's ServerList envelope.
type servedListResponse struct {
	Servers  []servedResponse `json:"servers"`
	Metadata struct {
		NextCursor string `json:"nextCursor,omitempty"`
		Count      int    `json:"count"`
	} `json:"metadata"`
}

// officialMetaKey is the generic spec's registry-managed metadata key (the SAME
// key for every registry implementing the API — subregistry clients, including
// GitHub's, read lifecycle status from it).
const officialMetaKey = "io.modelcontextprotocol.registry/official"

// defaultPageLimit / maxPageLimit bound list pagination.
const (
	defaultPageLimit = 100
	maxPageLimit     = 500
)

// ServeHTTP routes the /v0.1 read surface, tenant-prefixed or default-tenant.
func (s *SubRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS: required by the GitHub BYO-registry contract (IDE clients query the
	// registry cross-origin). Read-only data; preflight answered inline.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	tenant, rest, ok := s.resolveTenant(r.URL.EscapedPath())
	if !ok {
		writeRegistryError(w, http.StatusNotFound, "Server not found")
		return
	}
	records := s.tenants[tenant]

	switch {
	case rest == "/v0.1/servers":
		if !s.requireRead(w, r) {
			return
		}
		s.handleList(w, r, records)
	case strings.HasPrefix(rest, "/v0.1/servers/"):
		name, version, ok := parseServerPath(strings.TrimPrefix(rest, "/v0.1/servers/"))
		if !ok {
			writeRegistryError(w, http.StatusNotFound, "Server not found")
			return
		}
		// The spec's OPTIONAL write endpoints (PUT/DELETE version, PATCH status):
		// this registry is read-only — entries change through the plane's governed
		// approval flow, never anonymous HTTP writes.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeRegistryError(w, http.StatusNotImplemented, "this registry is read-only; entries are managed by the control plane's approval flow")
			return
		}
		if version == "" {
			s.handleVersions(w, r, records, name)
		} else {
			s.handleVersion(w, r, records, name, version)
		}
	case rest == "/v0.1/publish":
		writeRegistryError(w, http.StatusNotImplemented, "this registry is read-only; entries are managed by the control plane's approval flow")
	default:
		writeRegistryError(w, http.StatusNotFound, "Server not found")
	}
}

// requireRead admits GET/HEAD only on the list path.
func (s *SubRegistry) requireRead(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	writeRegistryError(w, http.StatusNotImplemented, "this registry is read-only; entries are managed by the control plane's approval flow")
	return false
}

// resolveTenant maps an escaped request path to (tenant, remaining /v0.1 path).
// /t/{tenant}/v0.1/... selects that tenant; a bare /v0.1/... selects the default
// tenant. An UNKNOWN tenant resolves to an EMPTY view (not a 404): every tenant
// path answers identically (empty list / "Server not found"), so the anonymous
// surface is never an existence oracle for provisioned tenant ids.
func (s *SubRegistry) resolveTenant(escapedPath string) (tenant, rest string, ok bool) {
	if strings.HasPrefix(escapedPath, "/t/") {
		remainder := strings.TrimPrefix(escapedPath, "/t/")
		i := strings.IndexByte(remainder, '/')
		if i <= 0 {
			return "", "", false
		}
		// An unprovisioned tenant id simply has no records: s.tenants[tenant]
		// yields nil downstream, which serves exactly like a provisioned-but-empty
		// tenant.
		return remainder[:i], remainder[i:], true
	}
	if s.defaultTenant == "" {
		return "", "", false
	}
	return s.defaultTenant, escapedPath, true
}

// parseServerPath splits the escaped remainder of /v0.1/servers/ into the server
// name and (optionally) a version. Both the spec's URL-encoded form
// ("com.example%2Fmy-server/versions/1.0.0") and a raw-slash name
// ("com.example/my-server/versions/1.0.0") are accepted; the trailing shapes are
// ".../versions" (list) and ".../versions/{version}" (get; version may be the
// literal "latest", returned verbatim).
func parseServerPath(escaped string) (name, version string, ok bool) {
	segs := strings.Split(strings.TrimSuffix(escaped, "/"), "/")
	// Locate the trailing "versions" marker: last or second-to-last segment.
	vIdx := -1
	if n := len(segs); n >= 2 && segs[n-1] == "versions" {
		vIdx = n - 1
	} else if n >= 3 && segs[n-2] == "versions" {
		vIdx = n - 2
		version = unescapeSegment(segs[n-1])
		if version == "" {
			return "", "", false
		}
	}
	if vIdx < 1 {
		return "", "", false
	}
	nameSegs := make([]string, 0, vIdx)
	for _, seg := range segs[:vIdx] {
		u := unescapeSegment(seg)
		if u == "" {
			return "", "", false
		}
		nameSegs = append(nameSegs, u)
	}
	return strings.Join(nameSegs, "/"), version, true
}

// unescapeSegment percent-decodes one path segment ("" on error).
func unescapeSegment(seg string) string {
	u, err := url.PathUnescape(seg)
	if err != nil {
		return ""
	}
	return u
}

// handleList implements GET /v0.1/servers (cursor, limit, search, version,
// updated_since, include_deleted).
func (s *SubRegistry) handleList(w http.ResponseWriter, r *http.Request, records []servedRecord) {
	q := r.URL.Query()
	includeDeleted := q.Get("include_deleted") == "true"
	updatedSince := strings.TrimSpace(q.Get("updated_since"))
	if updatedSince != "" {
		// The official extension: updated_since implies include_deleted, so a sync
		// client sees deletions too.
		includeDeleted = true
	}
	var sinceT time.Time
	if updatedSince != "" {
		t, err := time.Parse(time.RFC3339, updatedSince)
		if err != nil {
			writeRegistryError(w, http.StatusBadRequest, "updated_since must be an RFC3339 timestamp")
			return
		}
		sinceT = t
	}
	search := strings.ToLower(strings.TrimSpace(q.Get("search")))
	versionFilter := strings.TrimSpace(q.Get("version"))
	limit := defaultPageLimit
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeRegistryError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = min(n, maxPageLimit)
	}
	cursor := q.Get("cursor")

	var page []servedResponse
	started := cursor == ""
	nextCursor := ""
	for _, rec := range records {
		key := rec.server.Name + ":" + rec.server.Version
		if !started {
			if key == cursor {
				started = true // resume strictly AFTER the cursor record
			}
			continue
		}
		if !includeDeleted && rec.server.Status == servedStatusDeleted {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(rec.server.Name), search) {
			continue
		}
		switch versionFilter {
		case "":
		case "latest":
			if !rec.isLatest {
				continue
			}
		default:
			if rec.server.Version != versionFilter {
				continue
			}
		}
		if !sinceT.IsZero() {
			// Entries WITHOUT a parseable updated_at are always included: a sync
			// client must reconcile a superset, never silently miss an entry this
			// registry cannot date.
			if t, err := time.Parse(time.RFC3339, rec.server.UpdatedAt); err == nil && !t.After(sinceT) {
				continue
			}
		}
		if len(page) == limit {
			nextCursor = page[len(page)-1].Server.Name + ":" + page[len(page)-1].Server.Version
			break
		}
		page = append(page, toServedResponse(rec))
	}
	// A non-empty cursor that never matched is stale/garbled (e.g. the registry was
	// re-provisioned mid-walk). Answering an empty 200 would be indistinguishable
	// from a completed walk and silently truncate a sync; refuse it instead so the
	// client's walk degrades loudly.
	if !started {
		writeRegistryError(w, http.StatusBadRequest, "invalid or stale cursor")
		return
	}

	out := servedListResponse{Servers: page}
	if out.Servers == nil {
		out.Servers = []servedResponse{}
	}
	out.Metadata.Count = len(page)
	out.Metadata.NextCursor = nextCursor
	writeRegistryJSON(w, http.StatusOK, out)
}

// handleVersions implements GET /v0.1/servers/{name}/versions (newest first, the
// spec's order; deleted versions included only with include_deleted).
func (s *SubRegistry) handleVersions(w http.ResponseWriter, r *http.Request, records []servedRecord, name string) {
	includeDeleted := r.URL.Query().Get("include_deleted") == "true"
	var versions []servedResponse
	for _, rec := range records {
		if rec.server.Name != name {
			continue
		}
		if !includeDeleted && rec.server.Status == servedStatusDeleted {
			continue
		}
		versions = append(versions, toServedResponse(rec))
	}
	if len(versions) == 0 {
		writeRegistryError(w, http.StatusNotFound, "Server not found")
		return
	}
	// records are version-ascending; the endpoint serves newest first.
	for i, j := 0, len(versions)-1; i < j; i, j = i+1, j-1 {
		versions[i], versions[j] = versions[j], versions[i]
	}
	out := servedListResponse{Servers: versions}
	out.Metadata.Count = len(versions)
	writeRegistryJSON(w, http.StatusOK, out)
}

// handleVersion implements GET /v0.1/servers/{name}/versions/{version|latest}.
func (s *SubRegistry) handleVersion(w http.ResponseWriter, r *http.Request, records []servedRecord, name, version string) {
	includeDeleted := r.URL.Query().Get("include_deleted") == "true"
	for _, rec := range records {
		if rec.server.Name != name {
			continue
		}
		if !includeDeleted && rec.server.Status == servedStatusDeleted {
			continue
		}
		if (version == "latest" && rec.isLatest) || rec.server.Version == version {
			writeRegistryJSON(w, http.StatusOK, toServedResponse(rec))
			return
		}
	}
	writeRegistryError(w, http.StatusNotFound, "Server not found")
}

// toServedResponse renders a record with its registry-managed metadata.
func toServedResponse(rec servedRecord) servedResponse {
	official := map[string]any{
		"status":   rec.server.Status,
		"isLatest": rec.isLatest,
	}
	if rec.server.StatusMessage != "" {
		official["statusMessage"] = rec.server.StatusMessage
	}
	if rec.server.PublishedAt != "" {
		official["publishedAt"] = rec.server.PublishedAt
	}
	if rec.server.UpdatedAt != "" {
		official["updatedAt"] = rec.server.UpdatedAt
	}
	server := rec.server
	// Lifecycle fields ride in _meta (registry-managed), never on the record.
	server.Status, server.StatusMessage, server.PublishedAt, server.UpdatedAt = "", "", "", ""
	return servedResponse{Server: server, Meta: map[string]any{officialMetaKey: official}}
}

func writeRegistryJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeRegistryError(w http.ResponseWriter, status int, msg string) {
	writeRegistryJSON(w, status, map[string]string{"error": msg})
}
