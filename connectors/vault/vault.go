// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package vault is the Olivares AI identity connector for HashiCorp Vault. It
// reads the Identity secrets engine (entities and groups) and the ACL policy
// store, and exposes them as an identitysource.Graph to module VI (governance):
// entities are non-human identities, groups are collections (with nested-group
// support), and policies are policy collections an entity/group is bound to.
//
// Vault is the ONE identity source that ALSO emits observations. The other
// sources carry a roster only (membership is identity→collection, reference
// data), but a Vault ACL policy expresses identity→secret-path GRANTS — the
// PERMITTED side of the permitted-vs-observed diff (ARCHITECTURE.md). So Gather
// expands each entity's bound policies into model.EdgeObservation with
// Source=model.SignalPolicy: "this entity is permitted to read/write this
// path". The engine diffs these declared grants against what was actually
// observed (eBPF/OTEL/CloudTrail) to surface over-provisioned and shadow access.
//
// It is read-only and minimal-data (docs/SECURITY-HARDENING.md-3): every call is a GET, it reads
// identity METADATA and policy GRANTS only, and it NEVER reads a secret value
// (it never GETs the secret paths a policy mentions — only the policy document).
// The X-Vault-Token operator credential is held in memory, applied per request
// via httpx, and never logged or persisted. With no token the connector runs
// offline: Snapshot returns an empty graph, Gather emits nothing. It imports
// only the SDK, the Apache identitysource/httpx contracts, and the standard
// library — never the engine.
package vault

import (
	"context"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.vault"

// Default configuration values.
const (
	defaultBaseURL = "https://127.0.0.1:8200"
	defaultTimeout = 30 * time.Second
)

// headerToken is the Vault authentication header. The token is the request
// credential, not a Bearer scheme.
const headerToken = "X-Vault-Token"

// headerNamespace selects a Vault Enterprise namespace; it is non-secret routing
// metadata, sent as a static header.
const headerNamespace = "X-Vault-Namespace"

// Source is the Vault identity connector. It satisfies sdk.SourceConnector (the
// permitted-grant edges) and identitysource.GraphProvider (the identity roster).
// A single instance serves both: Snapshot returns the roster; Gather streams the
// identity→path grants derived from ACL policies.
type Source struct {
	client    *httpx.Client
	baseURL   string
	token     string
	namespace string
	timeout   time.Duration

	doer httpx.Doer       // optional injected transport (tests); nil => default
	now  func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector          = (*Source)(nil)
	_ identitysource.GraphProvider = (*Source)(nil)
)

// New returns a Vault connector with default configuration.
func New() *Source {
	return &Source{
		baseURL: defaultBaseURL,
		timeout: defaultTimeout,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "HashiCorp Vault",
		Description: "Reads Vault entities, groups and ACL policies (read-only metadata) and emits identity→secret-path permitted grants. Never reads secret values.",
		ConfigFields: []sdk.ConfigField{
			{Key: "base_url", Type: sdk.FieldString, Default: defaultBaseURL, Description: "Vault API base URL (e.g. https://vault.example:8200)."},
			{Key: "token", Type: sdk.FieldString, Secret: true, Description: "Vault token reference (X-Vault-Token; read-only; never persisted). Empty = offline (empty graph)."},
			{Key: "namespace", Type: sdk.FieldString, Description: "Vault Enterprise namespace (X-Vault-Namespace header). Optional."},
			{Key: "timeout", Type: sdk.FieldDuration, Default: "30s", Description: "Per-request timeout (advisory)."},
		},
	}
}

// Open reads configuration and builds the read-only client. It never fails for a
// missing credential: with no token the connector runs offline (Snapshot empty,
// Gather emits nothing). The namespace, if set, is a static non-secret header.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if v := cfg.Get("base_url"); v != "" {
		s.baseURL = v
	}
	s.token = cfg.Get("token")
	s.namespace = cfg.Get("namespace")
	s.timeout = cfg.GetDuration("timeout", s.timeout)

	var headers map[string]string
	if s.namespace != "" {
		headers = map[string]string{headerNamespace: s.namespace}
	}
	s.client = httpx.New(s.baseURL, s.doer, httpx.Header(headerToken, s.token, s.token), headers)
	return nil
}

// Close releases resources; this connector holds none.
func (s *Source) Close(context.Context) error { return nil }

// listResponse is the shape of Vault's LIST (?list=true) replies.
type listResponse struct {
	Data struct {
		Keys []string `json:"keys"`
	} `json:"data"`
}

// entityResponse is the relevant slice of GET /v1/identity/entity/id/{id}.
type entityResponse struct {
	Data struct {
		ID       string   `json:"id"`
		Name     string   `json:"name"`
		Disabled bool     `json:"disabled"`
		Policies []string `json:"policies"`
		GroupIDs []string `json:"group_ids"`
	} `json:"data"`
}

// groupResponse is the relevant slice of GET /v1/identity/group/id/{id}.
type groupResponse struct {
	Data struct {
		ID              string   `json:"id"`
		Name            string   `json:"name"`
		Policies        []string `json:"policies"`
		MemberEntityIDs []string `json:"member_entity_ids"`
		MemberGroupIDs  []string `json:"member_group_ids"`
	} `json:"data"`
}

// policyResponse is the relevant slice of GET /v1/sys/policies/acl/{name}.
type policyResponse struct {
	Data struct {
		Name   string `json:"name"`
		Policy string `json:"policy"` // raw HCL document
	} `json:"data"`
}

// entityRec holds the fields the connector keeps about an entity after resolving
// it, reused by both Snapshot (roster) and Gather (grant expansion).
type entityRec struct {
	id       string
	name     string
	disabled bool
	policies []string
}

// Snapshot connects read-only and assembles the identity graph: entities (NHI),
// groups (collections, with nested-group memberships), and policies
// (collections). It maps entity→policy and group→policy as policy memberships.
// With no token it returns an empty graph (offline). It never returns credential
// material and never reads a secret value.
func (s *Source) Snapshot(ctx context.Context) (identitysource.Graph, error) {
	g := identitysource.Graph{Source: identitysource.SourceVault, CapturedAt: s.clock().UTC()}
	if s.token == "" || s.client == nil {
		return g, nil // offline
	}

	entities, idToName, err := s.fetchEntities(ctx)
	if err != nil {
		return identitysource.Graph{}, err
	}

	groups, groupIDToName, err := s.fetchGroups(ctx)
	if err != nil {
		return identitysource.Graph{}, err
	}

	policyNames, err := s.listPolicies(ctx)
	if err != nil {
		return identitysource.Graph{}, err
	}

	// Entities → identities (NHI) + entity→policy memberships.
	for _, e := range entities {
		ref := "entity:" + e.name
		g.Identities = append(g.Identities, identitysource.Identity{
			Ref:         ref,
			Type:        identitysource.PrincipalNHI,
			Kind:        "vault_entity",
			DisplayName: e.name,
			Source:      identitysource.SourceVault,
			Disabled:    e.disabled,
		})
		for _, p := range e.policies {
			g.Memberships = append(g.Memberships, identitysource.Membership{
				MemberRef:     ref,
				MemberKind:    identitysource.MemberIdentity,
				CollectionRef: "policy:" + p,
				Source:        identitysource.SourceVault,
			})
		}
	}

	// Groups → collections + member (entity/group) memberships + group→policy.
	for _, gr := range groups {
		gref := "group:" + gr.Data.Name
		g.Collections = append(g.Collections, identitysource.Collection{
			Ref:         gref,
			Kind:        identitysource.KindGroup,
			DisplayName: gr.Data.Name,
			Source:      identitysource.SourceVault,
		})
		for _, mid := range gr.Data.MemberEntityIDs {
			name, ok := idToName[mid]
			if !ok {
				name = mid // resolve id→name; fall back to id when unknown
			}
			g.Memberships = append(g.Memberships, identitysource.Membership{
				MemberRef:     "entity:" + name,
				MemberKind:    identitysource.MemberIdentity,
				CollectionRef: gref,
				Source:        identitysource.SourceVault,
			})
		}
		for _, mgid := range gr.Data.MemberGroupIDs {
			name, ok := groupIDToName[mgid]
			if !ok {
				name = mgid
			}
			g.Memberships = append(g.Memberships, identitysource.Membership{
				MemberRef:     "group:" + name,
				MemberKind:    identitysource.MemberCollection,
				CollectionRef: gref,
				Source:        identitysource.SourceVault,
			})
		}
		for _, p := range gr.Data.Policies {
			g.Memberships = append(g.Memberships, identitysource.Membership{
				MemberRef:     gref,
				MemberKind:    identitysource.MemberIdentity,
				CollectionRef: "policy:" + p,
				Source:        identitysource.SourceVault,
			})
		}
	}

	// Policies → collections.
	for _, name := range policyNames {
		g.Collections = append(g.Collections, identitysource.Collection{
			Ref:         "policy:" + name,
			Kind:        identitysource.KindPolicy,
			DisplayName: name,
			Source:      identitysource.SourceVault,
		})
	}

	return g, nil
}

// Gather expands each ACL policy's path grants onto the entities bound to it,
// emitting one EdgeObservation per (entity, path) permitted grant with
// Source=SignalPolicy. This is the PERMITTED side of the permitted-vs-observed
// diff. It NEVER reads a secret value: it GETs the policy document only, parses
// the HCL path/capabilities blocks, and never touches the paths themselves. With
// no token it returns nil immediately.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.token == "" || s.client == nil {
		return nil // offline
	}
	now := s.clock().UTC()

	entities, _, err := s.fetchEntities(ctx)
	if err != nil {
		return err
	}
	// policy name -> entities (by name) that bind it.
	entitiesByPolicy := map[string][]string{}
	for _, e := range entities {
		for _, p := range e.policies {
			entitiesByPolicy[p] = append(entitiesByPolicy[p], e.name)
		}
	}

	policyNames, err := s.listPolicies(ctx)
	if err != nil {
		return err
	}

	for _, pname := range policyNames {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		bound := entitiesByPolicy[pname]
		if len(bound) == 0 {
			continue // a policy no entity binds grants no entity→path edge
		}
		var resp policyResponse
		if err := s.client.GetJSON(ctx, "/v1/sys/policies/acl/"+url.PathEscape(pname), nil, &resp); err != nil {
			return err
		}
		grants := parsePolicyPaths(resp.Data.Policy)
		for _, gr := range grants {
			for _, ename := range bound {
				if err := sink.Emit(ctx, model.EdgeObservation{
					OriginKind:   "identity",
					OriginRef:    "entity:" + ename,
					ResourceKind: "vault.path",
					ResourceRef:  gr.path,
					Mode:         gr.mode,
					Source:       model.SignalPolicy,
					Confidence:   model.ConfidenceAttributed,
					ObservedAt:   now,
				}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// fetchEntities lists entity ids and resolves each to its detail. It returns the
// entity records and an id→name resolution map used to label group memberships.
func (s *Source) fetchEntities(ctx context.Context) ([]entityRec, map[string]string, error) {
	ids, err := s.listIDs(ctx, "/v1/identity/entity/id")
	if err != nil {
		return nil, nil, err
	}
	out := make([]entityRec, 0, len(ids))
	idToName := make(map[string]string, len(ids))
	for _, id := range ids {
		var resp entityResponse
		if err := s.client.GetJSON(ctx, "/v1/identity/entity/id/"+url.PathEscape(id), nil, &resp); err != nil {
			return nil, nil, err
		}
		name := resp.Data.Name
		if name == "" {
			name = id // fall back to id when the entity has no name
		}
		idToName[id] = name
		out = append(out, entityRec{
			id:       id,
			name:     name,
			disabled: resp.Data.Disabled,
			policies: resp.Data.Policies,
		})
	}
	return out, idToName, nil
}

// fetchGroups lists group ids and resolves each to its detail. It returns the
// group details and an id→name resolution map for nested-group memberships.
func (s *Source) fetchGroups(ctx context.Context) ([]groupResponse, map[string]string, error) {
	ids, err := s.listIDs(ctx, "/v1/identity/group/id")
	if err != nil {
		return nil, nil, err
	}
	out := make([]groupResponse, 0, len(ids))
	idToName := make(map[string]string, len(ids))
	for _, id := range ids {
		var resp groupResponse
		if err := s.client.GetJSON(ctx, "/v1/identity/group/id/"+url.PathEscape(id), nil, &resp); err != nil {
			return nil, nil, err
		}
		name := resp.Data.Name
		if name == "" {
			name = id
		}
		idToName[id] = name
		out = append(out, resp)
	}
	return out, idToName, nil
}

// listPolicies lists ACL policy names. Vault includes the always-present "root"
// and "default" policies; they are returned verbatim (the consumer decides).
func (s *Source) listPolicies(ctx context.Context) ([]string, error) {
	var resp listResponse
	if err := s.client.GetJSON(ctx, "/v1/sys/policies/acl", url.Values{"list": {"true"}}, &resp); err != nil {
		return nil, err
	}
	return resp.Data.Keys, nil
}

// listIDs runs a Vault LIST (?list=true) and returns data.keys.
func (s *Source) listIDs(ctx context.Context, path string) ([]string, error) {
	var resp listResponse
	if err := s.client.GetJSON(ctx, path, url.Values{"list": {"true"}}, &resp); err != nil {
		return nil, err
	}
	return resp.Data.Keys, nil
}

// pathGrant is one parsed ACL path block: a path and its resolved access mode.
type pathGrant struct {
	path string
	mode model.AccessMode
}

// pathBlockRe matches a single Vault HCL ACL path block:
//
//	path "secret/data/foo" { ... capabilities = ["read", "list"] ... }
//
// Group 1 is the (quoted) path; group 2 is the bracketed capabilities list. It is
// a focused regexp, not a full HCL parser (no new dependency): it tolerates
// comments and other directives inside the block by anchoring on the path token,
// the opening brace, the capabilities assignment and its closing bracket. The
// path value never contains an unescaped double quote in practice (Vault path
// strings are simple), so a non-greedy [^"]* is sufficient and avoids spanning
// into a following block.
var pathBlockRe = regexp.MustCompile(`(?s)path\s+"([^"]*)"\s*\{.*?capabilities\s*=\s*\[([^\]]*)\]`)

// capRe extracts each quoted capability token from a capabilities list body.
var capRe = regexp.MustCompile(`"([^"]*)"`)

// parsePolicyPaths parses a Vault ACL HCL document into deduplicated path grants.
// It strips line comments (#, //) so a commented-out block does not match, maps
// the capability set of each block to an AccessMode, and skips deny-only blocks
// (a deny grants no access). The result is sorted by path for stable output.
func parsePolicyPaths(hcl string) []pathGrant {
	hcl = stripComments(hcl)

	// A path may legally appear in more than one block; the union of modes wins.
	modeByPath := map[string]model.AccessMode{}
	for _, m := range pathBlockRe.FindAllStringSubmatch(hcl, -1) {
		path := m[1]
		if path == "" {
			continue
		}
		mode, ok := capsToMode(capRe.FindAllStringSubmatch(m[2], -1))
		if !ok {
			continue // deny-only / no real capability => not a grant
		}
		modeByPath[path] = mergeMode(modeByPath[path], mode)
	}

	out := make([]pathGrant, 0, len(modeByPath))
	for p, mode := range modeByPath {
		out = append(out, pathGrant{path: p, mode: mode})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// readCaps and writeCaps classify the Vault capabilities. "deny" is neither (a
// deny removes access); "list" and "read" are reads; the mutating verbs are
// writes. "sudo" is a privileged write capability.
var (
	readCaps  = map[string]bool{"read": true, "list": true}
	writeCaps = map[string]bool{"create": true, "update": true, "delete": true, "patch": true, "sudo": true}
)

// capsToMode maps a block's capability tokens to an AccessMode. It returns ok=false
// when the block grants nothing (empty, or "deny" only): both kinds present =>
// ReadWrite; reads only => Read; writes only => Write.
func capsToMode(caps [][]string) (model.AccessMode, bool) {
	var hasRead, hasWrite bool
	for _, c := range caps {
		switch tok := strings.ToLower(strings.TrimSpace(c[1])); {
		case readCaps[tok]:
			hasRead = true
		case writeCaps[tok]:
			hasWrite = true
		default:
			// "deny" and any unknown/forward-compat token grant nothing here.
		}
	}
	switch {
	case hasRead && hasWrite:
		return model.ModeReadWrite, true
	case hasWrite:
		return model.ModeWrite, true
	case hasRead:
		return model.ModeRead, true
	default:
		return model.ModeUnknown, false
	}
}

// mergeMode unions two access modes (used when the same path appears twice).
func mergeMode(a, b model.AccessMode) model.AccessMode {
	if a == "" {
		return b
	}
	if a == b {
		return a
	}
	return model.ModeReadWrite
}

// stripComments removes HCL line comments (# … and // …) so a commented-out path
// block is not parsed as a live grant. It does not need to handle block comments:
// Vault ACL policies use line comments. Quoted strings in a real ACL path never
// contain a '#' or '//', so this conservative strip is safe.
func stripComments(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if idx := strings.Index(ln, "#"); idx >= 0 {
			ln = ln[:idx]
		}
		if idx := strings.Index(ln, "//"); idx >= 0 {
			ln = ln[:idx]
		}
		lines[i] = ln
	}
	return strings.Join(lines, "\n")
}

// clock returns the connector's time source (injectable for tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}
