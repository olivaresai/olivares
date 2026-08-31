// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package spiffe

// IDN-07 live SPIRE Workload API client. The offline path (Snapshot from an
// entries export, spiffe.go) and the passive JWT-SVID Verifier (svid.go, which
// verifies a token SOMEONE ELSE presents) are unchanged. This file adds the
// OPT-IN live mode that OBTAINS SVIDs from the local SPIRE agent's Workload API:
// auto-rotating X.509-SVID + JWT-SVID sources, deny-by-default mTLS, trust-domain
// federation, and the JWT-SVID fetch that feeds the Anthropic WIF exchange.
//
// Read-first / no-write-SPIRE (docs/SECURITY-HARDENING.md, §2). The Workload API is the READ-ONLY
// credential-issuance contract spoken over SPIFFE_ENDPOINT_SOCKET; it is NOT the
// SPIRE Server admin/registration API (write-capable), which this connector never
// touches. The client never logs or persists an SVID or a private key (docs/SECURITY-HARDENING.md) —
// it holds them in memory for the lifetime of the source and lets go-spiffe rotate
// them transparently via the streaming Workload API (no manual re-fetch).
//
// We deliberately do NOT re-implement the gRPC Workload API client: go-spiffe/v2
// (Apache-2.0, pure-Go, CGO_ENABLED=0 — preserves the single static binary,
// ARCHITECTURE.md) is the SPIFFE-maintained reference, and its X509Source/JWTSource own
// the rotation stream. We wrap its source interfaces so the hot paths (mTLS, JWT
// fetch, federation) stay rotation-transparent AND unit-testable with injected
// fakes (a real SPIRE agent is not assumed in CI).

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"

	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"

	"github.com/olivaresai/olivares/sdk"
)

// ErrNoWorkloadAPI reports that no SPIRE Workload API endpoint is configured
// (neither socket_addr nor the SPIFFE_ENDPOINT_SOCKET env var). Live mode is then
// OFF: the caller falls back to the offline Snapshot and the passive JWT Verifier,
// neither of which needs the agent. It is a sentinel, not a failure — a control
// plane with no SPIRE agent is a valid deployment.
var ErrNoWorkloadAPI = errors.New("spiffe: no Workload API endpoint (set socket_addr or SPIFFE_ENDPOINT_SOCKET); live mode off")

// WorkloadConfig configures the live Workload API client.
type WorkloadConfig struct {
	// SocketAddr overrides the Workload API address (e.g.
	// "unix:///run/spire/agent/api.sock" or "tcp://127.0.0.1:8081"). Empty uses the
	// SPIFFE_ENDPOINT_SOCKET environment variable (the SPIFFE-standard default); if
	// neither is set, Dial returns ErrNoWorkloadAPI.
	SocketAddr string
	// TrustDomain is the home trust domain (e.g. "corp.example", with or without the
	// spiffe:// scheme). Optional: it supplies the default AuthorizeMemberOf domain
	// and the federation bootstrap anchor. Empty leaves it unset (the caller derives
	// it from the fetched SVID).
	TrustDomain string
}

// Workload is the live Workload API client: auto-rotating X.509-SVID + JWT-SVID
// sources plus the home trust domain. It is built from the real go-spiffe sources
// in production (Dial) and from injected source interfaces in tests (newWorkload),
// so the rotation-transparent hot paths are exercised without a live agent.
type Workload struct {
	x509    x509svid.Source   // the workload's own X.509-SVID (rotates in place)
	bundles x509bundle.Source // trust bundles, incl. federated foreign domains
	jwt     jwtsvid.Source    // mints JWT-SVIDs for a requested audience
	home    spiffeid.TrustDomain
	hasHome bool

	waitUpdated func(context.Context) error // concrete-source rotation barrier; nil in tests
	closeFns    []func() error
}

// Dial connects to the SPIRE Workload API and builds the auto-rotating sources.
// It blocks until the first SVID update arrives (go-spiffe's NewX509Source/
// NewJWTSource contract), so a returned Workload is immediately usable. With no
// endpoint configured it returns ErrNoWorkloadAPI (live mode off) and the caller
// keeps the offline paths. The caller MUST Close the returned Workload on shutdown.
func Dial(ctx context.Context, cfg WorkloadConfig) (*Workload, error) {
	addr := strings.TrimSpace(cfg.SocketAddr)
	if addr == "" {
		if _, ok := workloadapi.GetDefaultAddress(); !ok {
			return nil, ErrNoWorkloadAPI
		}
	}

	var clientOpts []workloadapi.ClientOption
	if addr != "" {
		clientOpts = append(clientOpts, workloadapi.WithAddr(addr))
	}

	// One shared Client (one gRPC connection) backs both sources.
	client, err := workloadapi.New(ctx, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("spiffe: dial workload api: %w", err)
	}

	x509src, err := workloadapi.NewX509Source(ctx, workloadapi.WithClient(client))
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("spiffe: x509 source: %w", err)
	}
	jwtsrc, err := workloadapi.NewJWTSource(ctx, workloadapi.WithClient(client))
	if err != nil {
		_ = x509src.Close()
		_ = client.Close()
		return nil, fmt.Errorf("spiffe: jwt source: %w", err)
	}

	w := &Workload{
		x509:    x509src,
		bundles: x509src, // an X509Source is also an x509bundle.Source
		jwt:     jwtsrc,
		// Close in reverse dependency order: sources first, then the shared client.
		waitUpdated: x509src.WaitUntilUpdated,
		closeFns:    []func() error{jwtsrc.Close, x509src.Close, client.Close},
	}
	if td := normalizeTrustDomain(cfg.TrustDomain); td != "" {
		if parsed, err := spiffeid.TrustDomainFromString(td); err == nil {
			w.home, w.hasHome = parsed, true
		}
	}
	return w, nil
}

// NewWorkloadFromConfig builds the live Workload API client from the same config map
// the connector reads, so the host can wire it alongside the source and the
// passive Verifier. It returns (nil, nil) when no Workload API endpoint is configured
// (live mode off) — exactly like NewVerifierFromConfig returns (nil, nil) for the
// passive path — so a caller treats "no workload" as "offline only", not an error.
func NewWorkloadFromConfig(ctx context.Context, cfg sdk.Config) (*Workload, error) {
	addr := strings.TrimSpace(cfg.Get("socket_addr"))
	if addr == "" {
		if _, ok := workloadapi.GetDefaultAddress(); !ok {
			return nil, nil // live mode not configured
		}
	}
	w, err := Dial(ctx, WorkloadConfig{SocketAddr: addr, TrustDomain: cfg.Get("trust_domain")})
	if errors.Is(err, ErrNoWorkloadAPI) {
		return nil, nil
	}
	return w, err
}

// newWorkload builds a Workload from injected sources (tests), so rotation,
// federation and mTLS config are exercised without a live SPIRE agent.
func newWorkload(x509 x509svid.Source, bundles x509bundle.Source, jwt jwtsvid.Source) *Workload {
	return &Workload{x509: x509, bundles: bundles, jwt: jwt}
}

// Close releases the sources and the underlying Workload API connection. It is
// safe to call once; errors are joined so one failing closer does not mask another.
func (w *Workload) Close() error {
	var errs []error
	for _, c := range w.closeFns {
		if c != nil {
			if err := c(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// WaitUntilUpdated blocks until the X.509 source has received an update from the
// Workload API (or ctx is done). It is a no-op when no concrete source backs the
// Workload (injected-source tests), which already start fully populated.
func (w *Workload) WaitUntilUpdated(ctx context.Context) error {
	if w.waitUpdated == nil {
		return nil
	}
	return w.waitUpdated(ctx)
}

// X509SVID returns the workload's current X.509-SVID. It reads the live source
// every call, so a rotated SVID is reflected without reconstructing anything.
func (w *Workload) X509SVID() (*x509svid.SVID, error) {
	return w.x509.GetX509SVID()
}

// BundleForTrustDomain returns the X.509 trust bundle for td — the workload's own
// domain or, for a -federatesWith workload, a foreign federated domain. The source
// keeps federated bundles fresh, so a retired key is not served from a stale cache.
func (w *Workload) BundleForTrustDomain(td spiffeid.TrustDomain) (*x509bundle.Bundle, error) {
	return w.bundles.GetX509BundleForTrustDomain(td)
}

// FetchJWTSVID mints a JWT-SVID bound to audience (plus any extra audiences). It
// reads the live source, so the token is freshly issued by the agent every call —
// there is no refresh token for the downstream WIF exchange, so a short-lived
// JWT-SVID is fetched per use. The returned token is a bearer credential: use it
// transiently, never log or persist it (docs/SECURITY-HARDENING.md).
func (w *Workload) FetchJWTSVID(ctx context.Context, audience string, extraAudiences ...string) (*jwtsvid.SVID, error) {
	if strings.TrimSpace(audience) == "" {
		return nil, fmt.Errorf("spiffe: FetchJWTSVID: empty audience")
	}
	return w.jwt.FetchJWTSVID(ctx, jwtsvid.Params{Audience: audience, ExtraAudiences: extraAudiences})
}

// MTLSClientConfig returns a *tls.Config for an mTLS client connection that
// presents the workload's X.509-SVID and authorizes the SERVER peer with the given
// authorizer. The authorizer is REQUIRED (deny-by-default): a nil authorizer is an
// error, never an implicit allow-any. Build the authorizer with BuildAuthorizer,
// which never returns AuthorizeAny in production (docs/SECURITY-HARDENING.md, deny-closed).
func (w *Workload) MTLSClientConfig(authorizer tlsconfig.Authorizer) (*tls.Config, error) {
	if authorizer == nil {
		return nil, errDenyByDefault
	}
	return tlsconfig.MTLSClientConfig(w.x509, w.bundles, authorizer), nil
}

// MTLSServerConfig returns a *tls.Config for an mTLS server that presents the
// workload's X.509-SVID and requires+authorizes the CLIENT peer's SVID with the
// given authorizer. As with the client config the authorizer is REQUIRED. The
// underlying go-spiffe verifier rejects any client cert that is not a valid
// X.509-SVID — exactly one URI-SAN, cA=false (per the SPIFFE X509-SVID standard) —
// before the authorizer ever runs, so a multi-SAN or non-SVID cert is denied.
func (w *Workload) MTLSServerConfig(authorizer tlsconfig.Authorizer) (*tls.Config, error) {
	if authorizer == nil {
		return nil, errDenyByDefault
	}
	return tlsconfig.MTLSServerConfig(w.x509, w.bundles, authorizer), nil
}

// errDenyByDefault is returned when an mTLS config is requested without an explicit
// authorizer — the deny-closed posture (never AuthorizeAny by accident).
var errDenyByDefault = errors.New("spiffe: mTLS requires an explicit peer authorizer (deny-by-default; never AuthorizeAny in production)")

// AuthorizerConfig declares which peer SPIFFE identities an mTLS connection trusts.
// EXACTLY ONE rule must be set; an empty config is an error (deny-by-default), and
// AuthorizeAny is intentionally NOT exposed — a control plane that observes/governs
// agent identity must never accept an unauthenticated peer by configuration slip.
type AuthorizerConfig struct {
	// AllowID authorizes a single exact peer SPIFFE ID (AuthorizeID) — a 1:1 pair.
	AllowID string
	// AllowOneOf authorizes any peer whose SPIFFE ID is in this allowlist
	// (AuthorizeOneOf).
	AllowOneOf []string
	// AllowTrustDomain authorizes any peer in this trust domain (AuthorizeMemberOf)
	// — the same-domain case (e.g. all workloads in "corp.example").
	AllowTrustDomain string
}

// BuildAuthorizer constructs the deny-by-default peer authorizer from cfg. Exactly
// one of AllowID / AllowOneOf / AllowTrustDomain must be set; zero or more than one
// is an error. It NEVER returns tlsconfig.AuthorizeAny — the only way to accept a
// peer is to name an id, an allowlist, or a trust domain.
func BuildAuthorizer(cfg AuthorizerConfig) (tlsconfig.Authorizer, error) {
	set := 0
	if strings.TrimSpace(cfg.AllowID) != "" {
		set++
	}
	if len(cfg.AllowOneOf) > 0 {
		set++
	}
	if strings.TrimSpace(cfg.AllowTrustDomain) != "" {
		set++
	}
	switch {
	case set == 0:
		return nil, errors.New("spiffe: deny-by-default: no authorizer rule configured (set allow_id, allow_one_of or allow_trust_domain; AuthorizeAny is never used)")
	case set > 1:
		return nil, errors.New("spiffe: authorizer is ambiguous: set exactly one of allow_id, allow_one_of, allow_trust_domain")
	}

	switch {
	case strings.TrimSpace(cfg.AllowID) != "":
		id, err := spiffeid.FromString(strings.TrimSpace(cfg.AllowID))
		if err != nil {
			return nil, fmt.Errorf("spiffe: allow_id: %w", err)
		}
		return tlsconfig.AuthorizeID(id), nil
	case len(cfg.AllowOneOf) > 0:
		ids := make([]spiffeid.ID, 0, len(cfg.AllowOneOf))
		for _, raw := range cfg.AllowOneOf {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			id, err := spiffeid.FromString(raw)
			if err != nil {
				return nil, fmt.Errorf("spiffe: allow_one_of %q: %w", raw, err)
			}
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			return nil, errors.New("spiffe: allow_one_of had no valid SPIFFE IDs")
		}
		return tlsconfig.AuthorizeOneOf(ids...), nil
	default:
		td, err := spiffeid.TrustDomainFromString(normalizeTrustDomain(cfg.AllowTrustDomain))
		if err != nil {
			return nil, fmt.Errorf("spiffe: allow_trust_domain: %w", err)
		}
		return tlsconfig.AuthorizeMemberOf(td), nil
	}
}
