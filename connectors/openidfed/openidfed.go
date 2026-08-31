// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package openidfed resolves OpenID Federation 1.0 trust chains and tags a
// federated identity with its declared NIST SP 800-63-4 assurance (nice-to-have). OpenID Federation 1.0 is a Final OpenID Foundation
// specification (published 2026-02-17). An entity publishes a self-signed Entity
// Configuration at {entity}/.well-known/openid-federation (a signed JWT, typ
// "entity-statement+jwt"); its authority_hints name its superiors; a superior
// issues a signed Subordinate Statement about it (carrying the entity's attested
// keys) from its federation_fetch_endpoint. A TRUST CHAIN is the path of verified
// statements from a leaf up to a configured TRUST ANCHOR. This package builds and
// VERIFIES that chain: every statement's signature must verify, and the chain must
// terminate at a configured trust anchor — a chain that does not resolve to a
// trust anchor is REJECTED (docs/SECURITY-HARDENING.md: trust is established, never assumed).
//
// Scope (nice-to-have): the resolver verifies the chain's signatures
// and reachability to a trust anchor, and the assurance mapper tags IAL/AAL/FAL.
// metadata_policy merging, trust_marks, constraints, path-based entity identifiers
// and multi-path selection are documented POST-V1 seams — built correct and honest
// at the v1 level rather than broad and unverified.
//
// It reads only entity METADATA and public keys (an Entity Statement's jwks is
// public key material, never a credential). It imports only the SDK and the shared
// Apache helpers — never the engine.
package openidfed

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.openidfed"

// wellKnownPath is the Entity Configuration well-known location appended to an
// entity identifier (OpenID Federation 1.0 §9).
const wellKnownPath = "/.well-known/openid-federation"

// defaultMaxDepth bounds trust-chain length (cycle/runaway guard).
const defaultMaxDepth = 5

// fedAllowedAlgs is the asymmetric signature allow-list for an Entity Statement.
// Symmetric and "none" are rejected by omission (alg-confusion defense).
var fedAllowedAlgs = []jose.SignatureAlgorithm{
	jose.RS256, jose.RS384, jose.RS512,
	jose.ES256, jose.ES384, jose.ES512,
	jose.PS256, jose.PS384, jose.PS512,
}

// entityStatement is the subset of Entity Statement claims the resolver reads
// (OpenID Federation 1.0 §3). iss/sub/iat/exp/jwks are required; authority_hints,
// metadata and metadata_policy are optional.
type entityStatement struct {
	Iss            string             `json:"iss"`
	Sub            string             `json:"sub"`
	Iat            int64              `json:"iat"`
	Exp            int64              `json:"exp"`
	JWKS           jose.JSONWebKeySet `json:"jwks"`
	AuthorityHints []string           `json:"authority_hints"`
	Metadata       json.RawMessage    `json:"metadata"`
	MetadataPolicy json.RawMessage    `json:"metadata_policy"` // post-v1: not merged
	TrustMarks     json.RawMessage    `json:"trust_marks"`     // post-v1: not evaluated
}

// fetchEndpoint extracts metadata.federation_entity.federation_fetch_endpoint.
func (s entityStatement) fetchEndpoint() string {
	if len(s.Metadata) == 0 {
		return ""
	}
	var m struct {
		FederationEntity struct {
			FetchEndpoint string `json:"federation_fetch_endpoint"`
		} `json:"federation_entity"`
	}
	if json.Unmarshal(s.Metadata, &m) != nil {
		return ""
	}
	return strings.TrimSpace(m.FederationEntity.FetchEndpoint)
}

// EntityFetcher fetches Entity Configurations and Subordinate Statements. It is an
// interface so a test drives the resolver with no network.
type EntityFetcher interface {
	// FetchConfig fetches an entity's self-signed Entity Configuration JWT from
	// {entity}/.well-known/openid-federation.
	FetchConfig(ctx context.Context, entityID string) (string, error)
	// FetchStatement fetches the Subordinate Statement that superior `iss` issues
	// about `sub` from its federation_fetch_endpoint.
	FetchStatement(ctx context.Context, fetchEndpoint, iss, sub string) (string, error)
}

// TrustChain is a verified path from a leaf to a trust anchor.
type TrustChain struct {
	Leaf        string
	TrustAnchor string
	// Statements are the verified statements collected along the path (the leaf's
	// configuration first). Canonical chain formatting (OpenID Federation §4) is a
	// post-v1 refinement; this is sufficient to prove a verified path exists.
	Statements []entityStatement
}

// Resolver builds and verifies trust chains against a set of configured trust
// anchors. It is safe for sequential use.
type Resolver struct {
	trustAnchors map[string]bool
	fetcher      EntityFetcher
	maxDepth     int
	now          func() time.Time
}

// NewResolver builds a resolver trusting the given anchor entity ids.
func NewResolver(trustAnchors []string, fetcher EntityFetcher) *Resolver {
	set := map[string]bool{}
	for _, a := range trustAnchors {
		if a = strings.TrimSpace(a); a != "" {
			set[a] = true
		}
	}
	return &Resolver{trustAnchors: set, fetcher: fetcher, maxDepth: defaultMaxDepth}
}

// Resolve builds a verified trust chain from leaf up to a configured trust anchor.
// It returns an error when no authority-hint path reaches a trust anchor with a
// fully-verified signature chain.
func (r *Resolver) Resolve(ctx context.Context, leaf string) (TrustChain, error) {
	if len(r.trustAnchors) == 0 {
		return TrustChain{}, fmt.Errorf("openidfed: no trust anchors configured")
	}
	chain, anchor, err := r.resolveFrom(ctx, leaf, map[string]bool{}, 0)
	if err != nil {
		return TrustChain{}, err
	}
	return TrustChain{Leaf: leaf, TrustAnchor: anchor, Statements: chain}, nil
}

// resolveFrom returns the verified chain from `entity` up to a trust anchor, with
// chain[0] the entity's own (self-verified) configuration.
func (r *Resolver) resolveFrom(ctx context.Context, entity string, visited map[string]bool, depth int) ([]entityStatement, string, error) {
	if depth > r.maxDepth {
		return nil, "", fmt.Errorf("openidfed: trust chain exceeded max depth at %q", entity)
	}
	if visited[entity] {
		return nil, "", fmt.Errorf("openidfed: trust chain cycle at %q", entity)
	}
	visited[entity] = true

	cfgJWT, err := r.fetcher.FetchConfig(ctx, entity)
	if err != nil {
		return nil, "", fmt.Errorf("openidfed: fetch config %q: %w", entity, err)
	}
	cfg, err := r.selfVerify(cfgJWT, entity)
	if err != nil {
		return nil, "", err
	}

	// Terminal case: the entity itself is a configured trust anchor.
	if r.trustAnchors[entity] {
		return []entityStatement{cfg}, entity, nil
	}

	for _, sup := range cfg.AuthorityHints {
		supChain, anchor, err := r.resolveFrom(ctx, sup, copySet(visited), depth+1)
		if err != nil {
			continue // this superior does not reach a trust anchor; try the next hint
		}
		supCfg := supChain[0]
		fetchEP := supCfg.fetchEndpoint()
		if fetchEP == "" {
			continue
		}
		stmtJWT, err := r.fetcher.FetchStatement(ctx, fetchEP, sup, entity)
		if err != nil {
			continue
		}
		// The Subordinate Statement is signed by the SUPERIOR; verify it against the
		// superior's (already self-verified) keys.
		stmt, err := r.verify(stmtJWT, &supCfg.JWKS)
		if err != nil {
			continue
		}
		if stmt.Iss != sup || stmt.Sub != entity {
			continue // statement must bind superior→entity
		}
		chain := append([]entityStatement{cfg, stmt}, supChain...)
		return chain, anchor, nil
	}
	return nil, "", fmt.Errorf("openidfed: %q has no authority-hint path to a configured trust anchor", entity)
}

// selfVerify verifies an Entity Configuration against its OWN embedded jwks (the
// self-signed config). It peeks the unverified payload only to obtain the keys,
// then verifies the signature with them and validates iss==sub==entity and exp.
func (r *Resolver) selfVerify(jwtStr, entity string) (entityStatement, error) {
	peeked, err := peekStatement(jwtStr)
	if err != nil {
		return entityStatement{}, err
	}
	st, err := r.verify(jwtStr, &peeked.JWKS)
	if err != nil {
		return entityStatement{}, fmt.Errorf("openidfed: entity configuration self-signature invalid for %q: %w", entity, err)
	}
	if st.Iss != entity || st.Sub != entity {
		return entityStatement{}, fmt.Errorf("openidfed: entity configuration for %q is not self-issued (iss=%q sub=%q)", entity, st.Iss, st.Sub)
	}
	return st, nil
}

// verify checks an Entity Statement's signature against keys (asymmetric-only) and
// validates its expiry, returning the decoded statement.
func (r *Resolver) verify(jwtStr string, keys *jose.JSONWebKeySet) (entityStatement, error) {
	jws, err := jose.ParseSigned(jwtStr, fedAllowedAlgs)
	if err != nil {
		return entityStatement{}, fmt.Errorf("openidfed: parse statement: %w", err)
	}
	if len(jws.Signatures) == 0 {
		return entityStatement{}, fmt.Errorf("openidfed: statement has no signature")
	}
	kid := jws.Signatures[0].Header.KeyID
	key := lookupKey(keys, kid)
	if key == nil {
		return entityStatement{}, fmt.Errorf("openidfed: no key for kid %q", kid)
	}
	payload, err := jws.Verify(key)
	if err != nil {
		return entityStatement{}, fmt.Errorf("openidfed: statement signature: %w", err)
	}
	var st entityStatement
	if err := json.Unmarshal(payload, &st); err != nil {
		return entityStatement{}, fmt.Errorf("openidfed: decode statement: %w", err)
	}
	if st.Exp > 0 && r.clock().After(time.Unix(st.Exp, 0)) {
		return entityStatement{}, fmt.Errorf("openidfed: statement from %q expired", st.Iss)
	}
	return st, nil
}

func (r *Resolver) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now().UTC()
}

// peekStatement decodes the (unverified) JWS payload to read the embedded jwks of a
// self-signed configuration. The statement is verified with those keys immediately
// after, so this peek never trusts unverified content.
func peekStatement(jwtStr string) (entityStatement, error) {
	parts := strings.Split(jwtStr, ".")
	if len(parts) != 3 {
		return entityStatement{}, fmt.Errorf("openidfed: not a compact JWS")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return entityStatement{}, fmt.Errorf("openidfed: decode payload: %w", err)
	}
	var st entityStatement
	if err := json.Unmarshal(payload, &st); err != nil {
		return entityStatement{}, fmt.Errorf("openidfed: decode payload claims: %w", err)
	}
	return st, nil
}

// lookupKey finds a key by kid, or the sole key when the set has one and no kid.
func lookupKey(ks *jose.JSONWebKeySet, kid string) *jose.JSONWebKey {
	if ks == nil {
		return nil
	}
	if kid != "" {
		if m := ks.Key(kid); len(m) > 0 {
			return &m[0]
		}
		return nil
	}
	if len(ks.Keys) == 1 {
		return &ks.Keys[0]
	}
	return nil
}

func copySet(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// ---------------------------------------------------------------------------
// HTTP fetcher
// ---------------------------------------------------------------------------

// httpFetcher fetches Entity Statements over read-only HTTP GETs. It passes a
// fully-qualified URL as the request path so the shared httpx client uses it
// verbatim (no base-joining that could append a stray trailing slash).
type httpFetcher struct{ doer httpx.Doer }

// FetchConfig GETs {entity}/.well-known/openid-federation.
func (h httpFetcher) FetchConfig(ctx context.Context, entityID string) (string, error) {
	client := httpx.New("", h.doer, nil, nil)
	return getBody(ctx, client, strings.TrimRight(entityID, "/")+wellKnownPath, nil)
}

// FetchStatement GETs the superior's federation_fetch_endpoint with sub (and iss).
func (h httpFetcher) FetchStatement(ctx context.Context, fetchEndpoint, iss, sub string) (string, error) {
	client := httpx.New("", h.doer, nil, nil)
	q := map[string][]string{"sub": {sub}, "iss": {iss}}
	return getBody(ctx, client, fetchEndpoint, q)
}

// ---------------------------------------------------------------------------
// SourceConnector: resolve configured entities, emit a trust-chain finding
// ---------------------------------------------------------------------------

// Source resolves configured leaf entities against configured trust anchors and
// emits a finding per entity (resolved → Info; unresolved → Medium), so the
// control plane observes which federated entities have a verified trust path. The
// per-login assurance tag (MapAssurance) is a library used by the login layer
// (enterprise), not this poll source.
type Source struct {
	entities     []string
	trustAnchors []string
	doer         httpx.Doer
	now          func() time.Time
}

var _ sdk.SourceConnector = (*Source)(nil)

// New returns an openidfed source with default configuration.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "OpenID Federation 1.0 trust resolver",
		Description: "Resolves OpenID Federation 1.0 trust chains for configured entities up to configured trust anchors and reports whether each resolves. Verifies every statement signature.",
		ConfigFields: []sdk.ConfigField{
			{Key: "trust_anchors", Type: sdk.FieldString, Description: "Space/comma-separated trust anchor entity identifiers."},
			{Key: "entities", Type: sdk.FieldString, Description: "Space/comma-separated leaf entity identifiers to resolve. Empty = source does nothing."},
		},
	}
}

// Open reads configuration.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.entities = splitList(cfg.Get("entities"))
	s.trustAnchors = splitList(cfg.Get("trust_anchors"))
	return nil
}

// Gather resolves each configured entity and emits a finding. With no entities or
// no trust anchors it is a no-op.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if len(s.entities) == 0 || len(s.trustAnchors) == 0 {
		return nil
	}
	resolver := NewResolver(s.trustAnchors, httpFetcher{doer: s.doer})
	resolver.now = s.now
	for _, entity := range s.entities {
		chain, err := resolver.Resolve(ctx, entity)
		var f model.FindingReport
		if err != nil {
			f = model.FindingReport{
				Kind: "openid_federation_unresolved", Severity: model.SeverityMedium,
				SubjectKind: "federation", SubjectRef: entity,
				Title:      fmt.Sprintf("OpenID Federation entity %q does NOT resolve to a configured trust anchor — not trusted", entity),
				DetailHash: redact.Hash("openidfed-unresolved|" + entity + "|" + err.Error()),
				OccurredAt: s.clock(),
			}
		} else {
			f = model.FindingReport{
				Kind: "openid_federation_resolved", Severity: model.SeverityInfo,
				SubjectKind: "federation", SubjectRef: entity,
				Title:      fmt.Sprintf("OpenID Federation entity %q resolves to trust anchor %q (chain of %d verified statements)", entity, chain.TrustAnchor, len(chain.Statements)),
				DetailHash: redact.Hash(fmt.Sprintf("openidfed|%s|%s|%d", entity, chain.TrustAnchor, len(chain.Statements))),
				OccurredAt: s.clock(),
			}
		}
		if err := sink.Emit(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

// Close releases resources; the connector holds none.
func (s *Source) Close(context.Context) error { return nil }

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}

// splitList splits a space/comma-separated list, dropping empties.
func splitList(v string) []string {
	fields := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
	var out []string
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// getBody issues a read-only GET and returns the (bounded) body as a string — used
// for the JWT-bodied federation endpoints (content-type application/*+jwt, not JSON).
func getBody(ctx context.Context, client *httpx.Client, path string, query map[string][]string) (string, error) {
	resp, err := client.GetRaw(ctx, path, query)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := readBodyLimited(resp)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// maxFedBytes bounds an Entity Statement body (a signed JWT is small).
const maxFedBytes = 1 << 20

// readBodyLimited reads a bounded response body.
func readBodyLimited(resp *http.Response) ([]byte, error) {
	return io.ReadAll(io.LimitReader(resp.Body, maxFedBytes))
}
