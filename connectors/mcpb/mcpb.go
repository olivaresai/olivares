// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package mcpb governs `.mcpb` DESKTOP EXTENSIONS (MCP Bundles, formerly .dxt) —
// the Claude Desktop supply-chain surface (CUR-7). It mirrors the Claude
// Desktop ENTERPRISE allowlist as the PERMITTED side and inventories what is
// actually installed/distributed as the OBSERVED side, emitting drift on every
// divergence (the managed-settings driftFindings pattern).
//
// The Enterprise control it mirrors (VERIFIED 2026-06-10 against
// support.claude.com art. 12592343 + claude.com/docs/cowork/3p/extensions):
//
//   - the org-console Desktop allowlist is ALLOW-ONLY and keyed by the manifest
//     `name` field (not hash, not publisher); once enabled, non-allowlisted
//     installs are force-removed from clients and direct .mcpb installs are
//     blocked. There is NO blocklist and NO per-extension MDM key (the machine
//     keys are coarse toggles: isDesktopExtensionEnabled,
//     isDesktopExtensionSignatureRequired, …) — this connector does not invent
//     one.
//   - signing is OPTIONAL: a detached PKCS#7 block appended AFTER the zip
//     content between `MCPB_SIG_V1` and `MCPB_SIG_END` markers
//     (modelcontextprotocol/mcpb CLI.md). With the
//     isDesktopExtensionSignatureRequired policy, unsigned bundles are
//     rejected by the client. This connector checks signature PRESENCE only
//     (the marker block) and says so honestly — full PKCS#7 chain verification
//     is a documented seam, never faked.
//
// What it observes:
//
//   - an EXTENSIONS DIRECTORY of unpacked installs (one subdirectory with a
//     manifest.json per extension — the Claude Desktop "Claude Extensions"
//     layout), and/or
//   - a BUNDLES DIRECTORY of .mcpb/.dxt files (an org distribution share),
//     whose manifests are read from inside the zip (tolerating the appended
//     signature block).
//
// Each observed extension is inventoried as a declared-capability edge, its
// manifest text surface (description, tool/prompt descriptions — the same
// poisoning surface as an MCP catalog) is posture-scanned via the shared
// textscan primitives, and the PERMITTED-vs-OBSERVED diff yields mcpb_drift
// findings. MINIMAL-DATA (docs/SECURITY-HARDENING.md): sanitized names + hashed details; the
// manifest content and any embedded config values are never emitted.
package mcpb

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.mcpb"

const version = "0.1.0"

// Configuration keys.
const (
	cfgExtensionsDir = "extensions_dir" // unpacked installs (…/Claude Extensions)
	cfgBundlesDir    = "bundles_dir"    // .mcpb/.dxt bundle files (a distribution share)
	cfgScope         = "scope"          // attribution scope (defaults to the OS hostname)
	cfgExpected      = "expected_policy"
	cfgTrustedRoots  = "trusted_roots" // PEM CA bundle anchoring .mcpb signatures
)

// Finding vocabulary.
const (
	// findingDrift marks a PERMITTED-allowlist vs OBSERVED-extension divergence.
	findingDrift = "mcpb_drift"
	// findingPosture marks a content/posture issue in an extension manifest.
	findingPosture = "mcpb_posture"
	// subjectExtension is the SubjectKind/ResourceKind for a desktop extension.
	subjectExtension = "claude.desktop_extension"
	// originManagedPolicy attributes the PERMITTED allowlist edges.
	originManagedPolicy = "managed_policy"
	// originWorkspace attributes the OBSERVED installed/distributed extensions.
	originWorkspace = "workspace"
)

// Policy is the governance-authored intent this connector verifies against —
// the control-plane mirror of the org-console Desktop allowlist plus the
// signature-required machine policy. Only fields the policy ASSERTS are
// checked (an unset expectation is not drift — the rule).
type Policy struct {
	// AllowlistEnabled mirrors the org console's Desktop allowlist being ON:
	// deny-by-default — an observed extension whose manifest name is not in
	// Allowed is drift (the org console would have force-removed it).
	AllowlistEnabled bool `json:"allowlist_enabled"`
	// Allowed are the sanctioned extensions, keyed by manifest NAME (the
	// verified allowlist identity). An optional Version pins the sanctioned
	// version; an observed different version is drift.
	Allowed []AllowedExtension `json:"allowed"`
	// RequireSigned mirrors isDesktopExtensionSignatureRequired=true: a bundle
	// without the MCPB signature block is drift; unpacked installs (where the
	// bundle signature is no longer attached) are honestly reported as
	// not-verifiable, never assumed signed.
	RequireSigned bool `json:"require_signed"`
}

// AllowedExtension is one sanctioned extension entry.
type AllowedExtension struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// config is the resolved connector configuration.
type config struct {
	extensionsDir string
	bundlesDir    string
	scope         string
	// expected is the governance-authored intent. nil = observe-only (inventory
	// + posture; no drift to compute).
	expected *Policy
	// roots are the operator-configured trusted CA certificates that anchor a
	// bundle's PKCS#7 signature. nil = no cryptographic verification (a present
	// signature is reported PRESENT-BUT-UNVERIFIED, never valid — deny-closed).
	roots *x509.CertPool
}

// Source is the .mcpb governance SourceConnector (batch; the engine re-polls).
type Source struct {
	cfg config
}

// Compile-time proof that Source satisfies the contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a .mcpb connector with default configuration.
func New() *Source { return &Source{} }

// Descriptor returns the connector's stable self-description.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Claude Desktop .mcpb extensions (allowlist verify)",
		Description: "Inventories installed/distributed .mcpb desktop extensions, posture-scans their manifests (injection/secret/hidden-Unicode surface) and reports drift against the governance-authored Enterprise allowlist (PERMITTED vs OBSERVED), cryptographically verifying each bundle's PKCS#7 signature (chain-anchored to operator-configured trusted roots) under a require-signed policy.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgExtensionsDir, Type: sdk.FieldString, Description: `directory of UNPACKED installed extensions (one subdirectory with manifest.json per extension — Claude Desktop's "Claude Extensions" layout)`},
			{Key: cfgBundlesDir, Type: sdk.FieldString, Description: "directory of .mcpb/.dxt bundle FILES (an org distribution share); manifests are read from inside the zip, tolerating the appended signature block"},
			{Key: cfgScope, Type: sdk.FieldString, Description: "attribution scope ref (a host id / distribution-share name); defaults to the OS hostname"},
			{Key: cfgExpected, Type: sdk.FieldString, Description: `OPTIONAL governance-authored intent as JSON: {"allowlist_enabled":bool,"allowed":[{"name":…,"version":…}],"require_signed":bool}. When set, the connector reports PERMITTED-vs-OBSERVED drift; when empty it is observe-only`},
			{Key: cfgTrustedRoots, Type: sdk.FieldString, Description: "OPTIONAL PEM bundle of trusted CA certificates. When set, each signed .mcpb's PKCS#7 SignedData is cryptographically verified and its signer chained to these roots; when empty a present signature is reported PRESENT-BUT-UNVERIFIED, never valid (deny-closed)"},
		},
	}
}

// Open resolves and validates configuration. A malformed expected_policy fails
// LOUD (never a silent downgrade to observe-only); at least one observation
// directory is required (a verifier with nothing to observe is a config error).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c := config{
		extensionsDir: strings.TrimSpace(cfg.Get(cfgExtensionsDir)),
		bundlesDir:    strings.TrimSpace(cfg.Get(cfgBundlesDir)),
		scope:         strings.TrimSpace(cfg.Get(cfgScope)),
	}
	if c.extensionsDir == "" && c.bundlesDir == "" {
		return fmt.Errorf("mcpb: at least one of %q or %q is required", cfgExtensionsDir, cfgBundlesDir)
	}
	if raw := strings.TrimSpace(cfg.Get(cfgExpected)); raw != "" {
		var p Policy
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			return fmt.Errorf("mcpb: invalid %s: %w", cfgExpected, err)
		}
		for i, a := range p.Allowed {
			if strings.TrimSpace(a.Name) == "" {
				return fmt.Errorf("mcpb: %s.allowed[%d].name is required (the allowlist identity is the manifest name)", cfgExpected, i)
			}
		}
		c.expected = &p
	}
	if raw := strings.TrimSpace(cfg.Get(cfgTrustedRoots)); raw != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(raw)) {
			return fmt.Errorf("mcpb: %s contains no valid PEM CA certificate", cfgTrustedRoots)
		}
		c.roots = pool
	}
	if c.scope == "" {
		if h, err := os.Hostname(); err == nil && h != "" {
			c.scope = h
		} else {
			c.scope = "desktop"
		}
	}
	s.cfg = c
	return nil
}

// Close releases resources (none held).
func (s *Source) Close(context.Context) error { return nil }

// Gather inventories the observed extensions (unpacked installs + bundles),
// emits the PERMITTED allowlist edges, the per-extension posture findings and
// the PERMITTED-vs-OBSERVED drift. A missing observation directory means
// "nothing installed/distributed there" (benign); only a genuine read fault
// (permissions/IO) aborts the pass for the engine to retry.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	at := time.Now().UTC()

	// PERMITTED side: the authored allowlist (deterministic order).
	if s.cfg.expected != nil {
		allowed := append([]AllowedExtension(nil), s.cfg.expected.Allowed...)
		sort.Slice(allowed, func(i, j int) bool { return allowed[i].Name < allowed[j].Name })
		for _, a := range allowed {
			if err := sink.Emit(ctx, model.EdgeObservation{
				OriginKind:   originManagedPolicy,
				OriginRef:    s.cfg.scope,
				ResourceKind: subjectExtension,
				ResourceRef:  a.Name,
				Mode:         model.ModeUnknown,
				Source:       model.SignalPolicy,
				Confidence:   model.ConfidenceAttributed,
				ObservedAt:   at,
			}); err != nil {
				return err
			}
		}
	}

	observed, err := s.observe()
	if err != nil {
		return err
	}
	for _, ext := range observed {
		if err := s.emitExtension(ctx, sink, ext, at); err != nil {
			return err
		}
	}
	return nil
}

// observe collects the observed extensions from both configured surfaces, in
// deterministic order.
func (s *Source) observe() ([]observedExtension, error) {
	var out []observedExtension
	if s.cfg.extensionsDir != "" {
		exts, err := observeInstalled(s.cfg.extensionsDir)
		if err != nil {
			return nil, err
		}
		out = append(out, exts...)
	}
	if s.cfg.bundlesDir != "" {
		exts, err := observeBundles(s.cfg.bundlesDir, s.cfg.roots)
		if err != nil {
			return nil, err
		}
		out = append(out, exts...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ref() < out[j].ref() })
	return out, nil
}

// emitExtension emits the observed edge, the manifest posture findings and the
// drift verdicts for one observed extension.
func (s *Source) emitExtension(ctx context.Context, sink sdk.Sink, ext observedExtension, at time.Time) error {
	if name := ext.manifest.name(); name != "" {
		if err := sink.Emit(ctx, model.EdgeObservation{
			OriginKind:   originWorkspace,
			OriginRef:    s.cfg.scope,
			ResourceKind: subjectExtension,
			ResourceRef:  name,
			Mode:         model.ModeUnknown,
			Source:       model.SignalConfig,
			Confidence:   model.ConfidenceAttributed,
			ObservedAt:   at,
		}); err != nil {
			return err
		}
	}
	for _, f := range manifestPostureFindings(s.cfg.scope, ext, at) {
		if err := sink.Emit(ctx, f); err != nil {
			return err
		}
	}
	for _, f := range signatureFindings(s.cfg.scope, s.cfg.expected, ext, at) {
		if err := sink.Emit(ctx, f); err != nil {
			return err
		}
	}
	for _, f := range driftFindings(s.cfg.scope, s.cfg.expected, ext, at) {
		if err := sink.Emit(ctx, f); err != nil {
			return err
		}
	}
	return nil
}
