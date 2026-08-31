// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"errors"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// subjectMCPServer is the SubjectKind/ResourceKind for an MCP server, aligned
// with the claude connector's mcp.server resource so the two connectors describe
// the same entity.
const subjectMCPServer = "mcp.server"

// Finding kinds this connector emits (documented in the contract). The first
// two predate (mcp_auth in mcp.go, health for introspection failures); the
// rest are catalog-metadata/provenance signals. All carry only non-sensitive
// titles + a hashed detail (docs/SECURITY-HARDENING.md); none carries a payload.
const (
	findingRevision   = "mcp_revision"   // AIP-01: per-server negotiated protocol revision
	findingSurface    = "mcp_surface"    // AIP-04: advertised feature surface (UNTRUSTED catalog metadata)
	findingProvenance = "mcp_provenance" // AIP-03: registry namespace + ownership provenance
	findingShadow     = "mcp_shadow"     // AIP-03: shadow-server candidate (absent from verified namespace) → MCP09
	findingPosture    = "mcp_posture"    // AIP-10: posture issue + per-server score; OWASP MCP id rides in the title (multi-id), so it is NOT in MCPTop10ForFindingKind
)

// Source is the MCP introspection SourceConnector. It is a batch source: each
// Gather introspects every configured server once and returns; the engine owns
// re-scheduling (the connector holds no ticker, per the SDK contract).
type Source struct {
	cfg config
	// reg is the optional read-only MCP Registry client (AIP-03), non-nil only when
	// registry enrichment is configured. nil = no registry lookups.
	reg *registryClient
	// internal is the operator-declared registry of org-owned namespaces + approved
	// servers (AIP-10). It is local config (no network) and reconciles a server
	// before the public registry, so a vetted internal server is never a shadow.
	internal internalRegistry
	// trace is the AIP-09 deny-closed correlation seam for W3C Trace Context found
	// in a server's `_meta`. It defaults to a no-op wires the real correlator.
	trace traceCorrelator
}

// Compile-time proof that Source satisfies the contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns an MCP connector; servers are supplied in Open.
func New() *Source { return &Source{trace: nopTraceCorrelator{}} }

// Descriptor returns the connector's stable self-description.
func (s *Source) Descriptor() sdk.Descriptor { return descriptor() }

// Open resolves and validates the server list and timeout. A configuration error
// (no servers, unparsable config) surfaces here, before Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c, err := loadConfig(cfg)
	if err != nil {
		return err
	}
	s.cfg = c
	if c.registryEnabled {
		s.reg = newRegistryClient(c.registryURL, c.timeout)
	}
	ir, err := newInternalRegistry(c.ownedNamespaces, c.internalServers)
	if err != nil {
		return err
	}
	s.internal = ir
	return nil
}

// Gather introspects each configured server once, emitting one capability edge
// per discovered tool/resource/template/prompt, and a health finding for any
// server that cannot be introspected (a gap is a signal, not silence; docs/SECURITY-HARDENING.md
// §6). It returns nil when the pass completes and ctx.Err() if canceled mid-pass.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	at := time.Now().UTC()
	// AIP-10: registry-sync discovery — enumerate owned namespaces once per
	// pass and reconcile published servers (yank/unmanaged detection). Opt-in; a no-op
	// unless registry_sync + owned_namespaces are configured.
	if err := emitEach(ctx, sink, s.discoverFindings(ctx, at)); err != nil {
		return err
	}
	// deprecated-features registry drift detection (opt-in; the compiled
	// deprecation rules never depend on the feed).
	if err := emitEach(ctx, sink, s.deprecationFeedFindings(ctx, at)); err != nil {
		return err
	}
	// federation snapshot — federated /v0.1 registries + the Docker MCP
	// Catalog, fetched ONCE per pass. Per-server checks below consult the snapshot
	// only; an unreachable catalog degrades here, never into per-server findings.
	fed, fedFindings := s.federationSnapshot(ctx, at)
	if err := emitEach(ctx, sink, fedFindings); err != nil {
		return err
	}
	for _, spec := range s.cfg.servers {
		if err := ctx.Err(); err != nil {
			return err
		}
		cat, err := s.introspectOne(ctx, spec)
		if err != nil {
			if emitErr := sink.Emit(ctx, introspectFinding(spec.Name, err, at)); emitErr != nil {
				return emitErr
			}
			continue
		}
		// IDN-03: record the token-binding-verified dimension when the server was
		// introspected with an OAuth token bound to it (resource indicator).
		if cat.authBound {
			if emitErr := sink.Emit(ctx, tokenBindingVerifiedFinding(spec.Name, at)); emitErr != nil {
				return emitErr
			}
		}
		// AIP-01: surface the negotiated protocol revision per server, flagging a
		// server stuck on an older (or unknown) revision — fleet hygiene, not fatal.
		if emitErr := sink.Emit(ctx, revisionFinding(spec.Name, cat.server.ProtocolVersion, at)); emitErr != nil {
			return emitErr
		}
		if cat.negotiatedDown {
			if emitErr := sink.Emit(ctx, revisionNegotiatedDownFinding(spec.Name, cat.server.ProtocolVersion, at)); emitErr != nil {
				return emitErr
			}
		}
		// AIP-04: surface the advertised, governance-relevant feature surface
		// (elicitation/sampling/icons/tasks/structured-output) as UNTRUSTED catalog
		// metadata — elicitation/sampling are user/model-input vectors (OWASP MCP10).
		for _, f := range surfaceFindings(spec.Name, cat, at) {
			if emitErr := sink.Emit(ctx, f); emitErr != nil {
				return emitErr
			}
		}
		// (SEP-1865): MCP Apps as a governed resource class — the observed
		// ui:// template inventory vs the operator's pre-declared list, mimeType/
		// meta conformance, app-only tools, CSP/permission posture. Inventory
		// conformance like the surface findings: no network, always on.
		if emitErr := emitEach(ctx, sink, appsFindings(spec, cat, at)); emitErr != nil {
			return emitErr
		}
		// federation membership/provenance signals for this server (allowlist
		// presence, Docker catalog pin checks). The SCORED pin/attestation issues
		// fold into the posture grade below; the provenance findings emit as-is.
		fedIssues, fedServerFindings := fed.serverSignals(spec, at)
		if emitErr := emitEach(ctx, sink, fedServerFindings); emitErr != nil {
			return emitErr
		}
		// AIP-10: posture scan of the introspected catalog metadata
		// (tool-poisoning, injection-in-description, homoglyph/zero-width, over-broad
		// scopes) + a per-server posture grade, now also deprecation-aware and fed by
		// the federation supply-chain checks. UNTRUSTED catalog metadata,
		// minimal-data.
		if s.cfg.postureScan {
			if emitErr := emitEach(ctx, sink, postureFindings(spec, cat, fedIssues, at)); emitErr != nil {
				return emitErr
			}
		} else {
			// posture_scan gates the text scan, the deprecation rules and the grade —
			// NOT the federation verdicts: with docker_catalog on, a pin-drift/
			// unattested-entry verdict must surface even without the scanner, or the
			// pin-match Info channel would keep emitting while the failure channel is
			// silently gated off by an unrelated flag (deny-closed).
			for _, is := range fedIssues {
				if emitErr := sink.Emit(ctx, postureIssueFinding(spec.Name, is, at)); emitErr != nil {
					return emitErr
				}
			}
		}
		// AIP-10: internal-registry reconciliation FIRST (org-owned namespace /
		// approved entry + version-drift). A recognized server is cleared, so it skips
		// the public-registry shadow logic below.
		intFindings, handled := s.internalReconcile(spec, cat, at)
		if emitErr := emitEach(ctx, sink, intFindings); emitErr != nil {
			return emitErr
		}
		// AIP-03: public-registry provenance (reverse-DNS namespace + ownership) and the
		// shadow-server candidate signal (OWASP MCP09) — only when the internal registry
		// did not already recognize the server. Optional enrichment: a no-op unless a
		// registry is configured.
		if !handled {
			if emitErr := emitEach(ctx, sink, s.registryFindings(ctx, spec, at)); emitErr != nil {
				return emitErr
			}
		}
		// AIP-09: hand any W3C Trace Context the server carried in `_meta` to the
		// (deny-closed) correlation seam — inert until wires the OTLP correlator.
		if s.trace != nil && cat.trace.present() {
			s.trace.Correlate(spec.Name, cat.trace)
		}
		for _, edge := range capabilityEdges(spec.Name, cat, at) {
			if emitErr := sink.Emit(ctx, edge); emitErr != nil {
				return emitErr
			}
		}
	}
	return nil
}

// Close releases the connector's resources; it holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// emitEach emits every finding in order, stopping (and returning the error) on the
// first sink failure — the shared shape for the finding batches.
func emitEach(ctx context.Context, sink sdk.Sink, findings []model.FindingReport) error {
	for _, f := range findings {
		if err := sink.Emit(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

// introspectOne runs introspection for one server under a per-server timeout.
// The connector-level next_revision_preview flag auto-negotiates
// the 2026-07-28 frozen-RC stateless mode by default; a per-server
// next_revision=true forces RC-only, and false forces legacy-only.
func (s *Source) introspectOne(ctx context.Context, spec serverSpec) (catalog, error) {
	sctx, cancel := context.WithTimeout(ctx, s.cfg.timeout)
	defer cancel()
	return introspectWithPreview(sctx, spec, s.cfg.nextRevisionPreview)
}

// introspectFinding classifies an introspection failure: an OAuth-protected server
// (IDN-03 Phase 1) gets the auth finding carrying the token-binding-verified=false
// dimension; any other failure gets the generic health finding.
func introspectFinding(server string, err error, at time.Time) model.FindingReport {
	var oauthErr *oauthRequiredError
	if errors.As(err, &oauthErr) {
		return oauthProtectedFinding(server, oauthErr, at)
	}
	return failureFinding(server, err, at)
}

// failureFinding reports a server that could not be introspected. The error
// detail is hashed, not embedded, so a connection string in an error message is
// never persisted (docs/SECURITY-HARDENING.md).
func failureFinding(server string, err error, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        "health",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectMCPServer,
		SubjectRef:  server,
		Title:       "MCP server introspection failed",
		DetailHash:  redact.Hash(err.Error()),
		OccurredAt:  at,
	}
}

// oauthProtectedFinding is the IDN-03 Phase-1 signal: the server answered 401 with an
// OAuth challenge and was NOT introspected, so the MCP edge would be UNTRUSTED with
// token-binding-verified=false. The PRM URL is hashed into the detail, never stored
// in the clear. attempted=true means a token binding was configured but still failed.
func oauthProtectedFinding(server string, e *oauthRequiredError, at time.Time) model.FindingReport {
	title := "MCP server is OAuth-protected and was not introspected (token-binding-verified=false)"
	if e.attempted {
		title = "MCP server OAuth token binding failed; not introspected (token-binding-verified=false)"
	}
	// Auth-posture (objective 3 detective side): an OAuth-protected server whose
	// 401 advertises NO RFC 9728 resource_metadata is non-conformant with the MCP
	// authorization spec — a client cannot discover its authorization server. Surfaced
	// as an MCP07 upgrade risk (the mcp_auth kind already maps to MCP07, owasp_mcp.go).
	if e.resourceMetadata == "" {
		title = "[MCP07] " + title + "; advertises no RFC 9728 resource_metadata (auth-discovery non-conformant)"
	}
	return model.FindingReport{
		Kind:        "mcp_auth",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectMCPServer,
		SubjectRef:  server,
		Title:       title,
		DetailHash:  redact.Hash("oauth-protected resource_metadata=" + e.resourceMetadata),
		OccurredAt:  at,
	}
}

// tokenBindingVerifiedFinding is the IDN-03 positive signal: the server was
// introspected with an OAuth token bound to it (resource indicator), so its
// capability edges carry token-binding-verified=true.
func tokenBindingVerifiedFinding(server string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        "mcp_auth",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectMCPServer,
		SubjectRef:  server,
		Title:       "MCP server introspected with verified token binding (token-binding-verified=true)",
		DetailHash:  redact.Hash("token-binding-verified:" + server),
		OccurredAt:  at,
	}
}

// revisionFinding surfaces the protocol revision a server negotiated, classified
// against the current baseline (revision.go). A current revision is Info; a
// known-but-older revision is Low (fleet hygiene — the server presents as an older
// MCP peer); an unknown/absent revision is Info (it may be a server already
// speaking a newer-than-advertised revision, which is surfaced, never trusted as
// a version claim). The mismatch is tolerated-but-observed: the connector still
// introspected the server.
func revisionFinding(server, negotiated string, at time.Time) model.FindingReport {
	status := classifyRevision(negotiated)
	sev := model.SeverityInfo
	var title string
	switch status {
	case revisionStale:
		sev = model.SeverityLow
		title = "MCP server speaks stale protocol revision " + negotiated + " (current is " + currentRevision + ")"
	case revisionUnknown:
		if negotiated == "" {
			title = "MCP server reported no protocol revision (connector advertises " + currentRevision + ")"
		} else {
			title = "MCP server reported unrecognized protocol revision " + negotiated + " (connector advertises " + currentRevision + ")"
		}
	default:
		title = "MCP server speaks current protocol revision " + currentRevision
	}
	return model.FindingReport{
		Kind:        findingRevision,
		Severity:    sev,
		SubjectKind: subjectMCPServer,
		SubjectRef:  server,
		Title:       title,
		DetailHash:  redact.Hash("mcp-revision negotiated=" + negotiated + " status=" + string(status) + " server=" + server),
		OccurredAt:  at,
	}
}

func revisionNegotiatedDownFinding(server, negotiated string, at time.Time) model.FindingReport {
	title := "MCP server auto-negotiated down to protocol revision " + negotiated + " after 2026-07-28 frozen-RC probe"
	return model.FindingReport{
		Kind:        findingRevision,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectMCPServer,
		SubjectRef:  server,
		Title:       title,
		DetailHash:  redact.Hash("mcp-revision negotiated-down negotiated=" + negotiated + " server=" + server),
		OccurredAt:  at,
	}
}
