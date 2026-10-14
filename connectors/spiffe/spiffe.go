// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package spiffe is the Olivares AI identity connector for SPIFFE/SPIRE workload
// identities. It discovers the registered workload SPIFFE IDs and the agent/node
// each is parented to, and exposes them as an identitysource.Graph to module VI
// (identity/permissions/governance) in addition to sdk.SourceConnector.
//
// Integration choice (honest deviation, docs/SECURITY-HARDENING.md). SPIRE does NOT expose a
// universal, read-only HTTP "list registration entries" API: the entries live
// behind the SPIRE Server's admin/local API (a Unix socket or the Server API,
// both privileged and write-capable). To stay read-only and minimal-data the
// connector deliberately does NOT talk to the SPIRE Server. Instead it consumes a
// registration-entries JSON EXPORT the operator wires — the exact shape of
// `spire-server entry show -output json` — from either a local file (entries_file)
// or an HTTP URL (entries_url) the operator serves read-only. This mirrors the
// Gemini connector, which consumes an operator-wired usage export for the same
// reason (no first-party read-only API exists). It is a conscious, documented
// choice, not a limitation hidden from the operator.
//
// Read-only and minimal-data (docs/SECURITY-HARDENING.md §2-3). A SPIRE registration entry carries
// NO secret: it is the workload's SPIFFE ID, its parent agent's SPIFFE ID, its
// selectors and a few flags (admin, ttl). The connector never sees an SVID
// (the issued X.509/JWT credential), a private key, or a CA — only identity
// metadata. With no file and no URL it returns an empty Graph (offline), no error.
//
// It emits no observations: a workload roster is reference data, and a
// workload→agent parentage is identity→identity (a membership), not an
// identity→resource access edge, so Gather is a no-op and the roster travels the
// typed Snapshot (the pattern). It imports only the SDK, the Apache
// identitysource contract and the shared read-only httpx client — never the engine.
//
// Directory inventory through Snapshot is available in Business. Community keeps
// the observation and credential primitives and reports inventory as unavailable.
package spiffe

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.spiffe"

// scheme is the SPIFFE URI scheme. A SPIFFE ID is scheme + trust domain + path.
const scheme = "spiffe://"

// Source is the SPIFFE/SPIRE identity connector. It satisfies sdk.SourceConnector
// (a no-op Gather) and identitysource.GraphProvider (the workload roster).
type Source struct {
	entriesFile string
	entriesURL  string
	trustDomain string // optional filter (trust-domain name, with or without scheme)

	doer httpx.Doer       // optional injected transport (tests); nil => default
	now  func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector          = (*Source)(nil)
	_ identitysource.GraphProvider = (*Source)(nil)
)

// New returns a SPIFFE connector with default (empty/offline) configuration.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "SPIFFE / SPIRE",
		Description: "Reads SPIRE workload registration entries from an operator-wired JSON export (file or URL); read-only identity metadata, never SVIDs or keys; emits no observation stream, roster travels via identity Snapshot.",
		ConfigFields: []sdk.ConfigField{
			{Key: "entries_file", Type: sdk.FieldString, Description: "Path to a local 'spire-server entry show -output json' export. Empty = use entries_url."},
			{Key: "entries_url", Type: sdk.FieldString, Description: "URL serving the registration-entries JSON export (read-only GET). Empty = use entries_file."},
			{Key: "trust_domain", Type: sdk.FieldString, Description: "Optional trust-domain filter (e.g. corp.example): include only entries in this trust domain. Also constrains the live JWT-SVID verifier's accepted subjects."},
			// IDN-07 live mode (opt-in): a JWT-SVID verifier the host wires for the WIF
			// exchange. Configuring a key source enables it; it does NOT change the
			// offline Snapshot path. See NewVerifierFromConfig.
			{Key: "svid_jwks", Type: sdk.FieldString, Description: "IDN-07 live mode: inline JWKS JSON (the trust bundle public keys) to verify presented JWT-SVIDs. Mutually exclusive with svid_jwks_url. Empty = live mode off."},
			{Key: "svid_jwks_url", Type: sdk.FieldString, Description: "IDN-07 live mode: read-only JWKS endpoint (the SPIRE OIDC Discovery Provider keys URL) to verify JWT-SVIDs. Never the SPIRE write-capable admin API."},
			{Key: "svid_audience", Type: sdk.FieldString, Description: "IDN-07 live mode: the audience a presented JWT-SVID must carry (the WIF relying-party audience). Empty = audience not checked."},
			// Multi-trust-domain federation: verify JWT-SVIDs minted in FEDERATED
			// foreign trust domains, each against its own SPIFFE trust bundle (keyed by
			// trust domain), not just the single static JWKS above.
			{Key: "svid_federation", Type: sdk.FieldString, Description: "IDN-07 multi-trust-domain: JSON array of foreign trust domains to federate with via their SPIFFE bundle endpoints, e.g. [{\"trust_domain\":\"partner.example\",\"bundle_endpoint_url\":\"https://spire.partner.example/bundle\",\"profile\":\"https_web\"},{\"trust_domain\":\"gov.example\",\"bundle_endpoint_url\":\"https://spire.gov.example/bundle\",\"profile\":\"https_spiffe\",\"endpoint_spiffe_id\":\"spiffe://gov.example/bundle-endpoint\"}]. profile is https_web (Web PKI, default) or https_spiffe (X509-SVID validated against endpoint_spiffe_id, deny-closed; bootstrapped from svid_bundles). Re-fetched on key rotation and past spiffe_refresh_hint. Read-only, never the SPIRE admin API."},
			{Key: "svid_bundles", Type: sdk.FieldString, Description: "IDN-07 multi-trust-domain: JSON object mapping a trust-domain name to an inline SPIFFE trust-bundle JSON document, e.g. {\"partner.example\":{...}}. For air-gapped/statically pinned federation, and the bootstrap trust bundle whose X.509 authorities authenticate an https_spiffe endpoint of that domain. A malformed bundle fails at startup."},
			// IDN-07 live Workload API client (opt-in): OBTAINS auto-rotating SVIDs from
			// the local SPIRE agent. Distinct from the passive JWKS Verifier above (which
			// verifies a token someone ELSE presents). Off when neither socket_addr nor
			// SPIFFE_ENDPOINT_SOCKET is set. See NewWorkloadFromConfig.
			{Key: "socket_addr", Type: sdk.FieldString, Description: "IDN-07 live mode: SPIRE Workload API address (e.g. unix:///run/spire/agent/api.sock). Empty uses the SPIFFE_ENDPOINT_SOCKET env var; if neither is set, the live client is off (offline Snapshot + passive Verifier still work). Read-only Workload API only, never the write-capable SPIRE Server admin API."},
		},
	}
}

// NewVerifierFromConfig builds the live JWT-SVID verifier (IDN-07) from the same
// config map the connector reads, so the host can wire it alongside the source.
// It returns (nil, nil) when no key source is configured (live mode off), so a caller
// can treat "no verifier" as "offline only" without an error.
func NewVerifierFromConfig(cfg sdk.Config, doer httpx.Doer) (*Verifier, error) {
	jwks := strings.TrimSpace(cfg.Get("svid_jwks"))
	jwksURL := strings.TrimSpace(cfg.Get("svid_jwks_url"))
	federations, err := parseFederations(cfg.Get("svid_federation"))
	if err != nil {
		return nil, err
	}
	bundles, err := parseInlineBundles(cfg.Get("svid_bundles"))
	if err != nil {
		return nil, err
	}
	if jwks == "" && jwksURL == "" && len(federations) == 0 && len(bundles) == 0 {
		return nil, nil // live mode not configured
	}
	return NewVerifier(VerifierConfig{
		TrustDomain:   cfg.Get("trust_domain"),
		Audience:      cfg.Get("svid_audience"),
		JWKS:          jwks,
		JWKSURL:       jwksURL,
		Federations:   federations,
		InlineBundles: bundles,
	}, doer)
}

// parseFederations decodes the svid_federation config: a JSON array of foreign
// trust domains to federate with via their SPIFFE bundle endpoints. An empty value
// yields nil (the feature is off); malformed JSON is an error (deny-closed).
func parseFederations(raw string) ([]FederationEndpoint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var wire []struct {
		TrustDomain      string `json:"trust_domain"`
		BundleEndpoint   string `json:"bundle_endpoint_url"`
		Profile          string `json:"profile"`
		EndpointSpiffeID string `json:"endpoint_spiffe_id"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return nil, fmt.Errorf("spiffe: parse svid_federation: %w", err)
	}
	out := make([]FederationEndpoint, 0, len(wire))
	for _, w := range wire {
		out = append(out, FederationEndpoint{
			TrustDomain:       w.TrustDomain,
			BundleEndpointURL: w.BundleEndpoint,
			Profile:           w.Profile,
			EndpointSpiffeID:  w.EndpointSpiffeID,
		})
	}
	return out, nil
}

// parseInlineBundles decodes the svid_bundles config: a JSON object of trust-domain
// name -> inline SPIFFE trust-bundle JSON document. Each bundle's raw JSON is kept
// verbatim (re-marshaled) so spiffebundle.Parse sees the original document.
func parseInlineBundles(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("spiffe: parse svid_bundles: %w", err)
	}
	out := make(map[string]string, len(m))
	for td, doc := range m {
		out[td] = string(doc)
	}
	return out, nil
}

// Open reads configuration. It performs no I/O (the export read belongs to a
// Snapshot call), so it never fails for an unreachable URL or a missing file —
// that surfaces on Snapshot. With neither file nor URL the connector is offline.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.entriesFile = strings.TrimSpace(cfg.Get("entries_file"))
	s.entriesURL = strings.TrimSpace(cfg.Get("entries_url"))
	s.trustDomain = normalizeTrustDomain(cfg.Get("trust_domain"))
	return nil
}

// Gather emits no observations: a SPIRE workload roster is reference data exposed
// through Snapshot, and workload→agent parentage is a membership, not an
// identity→resource access edge. It returns nil immediately (a batch source with
// nothing to stream).
func (s *Source) Gather(context.Context, sdk.Sink) error { return nil }

// Close releases resources; the connector holds none.
func (s *Source) Close(context.Context) error { return nil }

// normalizeTrustDomain reduces a configured filter to a bare trust-domain name: it
// strips a leading "spiffe://" scheme and any path so an operator can write either
// "corp.example" or "spiffe://corp.example".
func normalizeTrustDomain(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, scheme)
	if i := strings.IndexByte(v, '/'); i >= 0 {
		v = v[:i]
	}
	return v
}

// clock returns the connector's time source (injectable for tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}
