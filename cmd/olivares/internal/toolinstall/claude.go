// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ClaudeOfficialBaseURL is Anthropic's release bucket: <ver>/manifest.json with a
// detached manifest.json.sig, <ver>/<platform>/claude, and the pointers latest
// and stable.
const ClaudeOfficialBaseURL = "https://downloads.claude.ai/claude-code-releases"

const (
	claudeDriver         = "claude"
	claudePointerCap     = 256
	claudeManifestCap    = 1 << 20
	claudeSignatureCap   = 64 << 10
	claudeArtifactCap    = 1 << 30
	claudeProbeBudget    = 20 * time.Second
	claudeProbeGrace     = 3 * time.Second
	claudeProbeFixedPath = "/usr/bin:/bin"
)

// SignatureVerifier is what a provider needs from a verifier; GPGVerifier is the
// one implementation in this release.
type SignatureVerifier interface {
	Verify(ctx context.Context, key []byte, wantFingerprint string, signature, data []byte) (SignatureReport, error)
	Describe() string
}

// ClaudeOptions configures the adapter. Zero values mean the official base URL,
// a default HTTP client and the default probe budget.
type ClaudeOptions struct {
	BaseURL     string
	Client      *http.Client
	Verifier    SignatureVerifier
	ProbeBudget time.Duration
	ProbeGrace  time.Duration
}

// Claude installs Claude Code from the signed release bucket.
type Claude struct {
	base        string
	fetch       fetcher
	verifier    SignatureVerifier
	key         []byte
	fingerprint string
	probeBudget time.Duration
	probeGrace  time.Duration
}

// NewClaude is the production constructor: it trusts exactly the embedded
// release key and its pinned fingerprint. No option can change either.
func NewClaude(opts ClaudeOptions) *Claude {
	return newClaude(opts, []byte(claudeReleaseKey), ClaudeReleaseKeyFingerprint)
}

// NewClaudeWithTrust builds an adapter that trusts key/fingerprint instead of
// the embedded pin. It exists for fixtures with a throwaway signing key; no
// production wiring or flag calls it (cmd_agent_tool_test pins that by grep).
func NewClaudeWithTrust(opts ClaudeOptions, key []byte, fingerprint string) *Claude {
	return newClaude(opts, key, fingerprint)
}

func newClaude(opts ClaudeOptions, key []byte, fingerprint string) *Claude {
	c := &Claude{
		base: ClaudeOfficialBaseURL, verifier: opts.Verifier, key: key, fingerprint: normalizeFingerprint(fingerprint),
		probeBudget: opts.ProbeBudget, probeGrace: opts.ProbeGrace,
	}
	if opts.BaseURL != "" {
		c.base = strings.TrimRight(opts.BaseURL, "/")
	}
	client := opts.Client
	if client == nil {
		client = NewHTTPClient()
	}
	c.fetch = fetcher{client: client}
	if c.probeBudget <= 0 {
		c.probeBudget = claudeProbeBudget
	}
	if c.probeGrace <= 0 {
		c.probeGrace = claudeProbeGrace
	}
	return c
}

func (c *Claude) Key() string { return claudeDriver }

// claudePlatformKey maps a platform to the vendor's manifest key. macOS and
// Windows are listed by the vendor manifest but need their own platform proof
// (code signature verification, install and execution checks) before this
// installer offers them; they are release work ahead, not a removed scope.
func claudePlatformKey(p Platform) (string, error) {
	var arch string
	switch p.Arch {
	case "amd64":
		arch = "x64"
	case "arm64":
		arch = "arm64"
	default:
		return "", refuse(KindUnsupportedPlatform, "architecture %q has no Claude Code release", p.Arch)
	}
	switch p.OS {
	case "linux":
		key := "linux-" + arch
		switch p.Libc {
		case "glibc", "":
		case "musl":
			key += "-musl"
		default:
			return "", refuse(KindUnsupportedPlatform, "libc %q is not glibc or musl", p.Libc)
		}
		return key, nil
	case "darwin", "windows":
		return "", refuse(KindUnsupportedPlatform, "%s is published by the vendor but this installer verifies and installs Linux x64/arm64 (glibc and musl) in this release; macOS and Windows need their own signature and execution proof and remain release work", p.OS)
	default:
		return "", refuse(KindUnsupportedPlatform, "operating system %q has no Claude Code release", p.OS)
	}
}

type claudeManifest struct {
	Version   string `json:"version"`
	Platforms map[string]struct {
		Binary   string `json:"binary"`
		Checksum string `json:"checksum"`
		Size     int64  `json:"size"`
	} `json:"platforms"`
}

// VerifiedManifest is a manifest whose signature verified under the pinned key.
type VerifiedManifest struct {
	Version    string
	Artifacts  map[string]Artifact
	Provenance Provenance
}

func (c *Claude) verifyManifest(ctx context.Context, manifest, signature []byte) (*VerifiedManifest, error) {
	if c.verifier == nil {
		return nil, refuse(KindVerificationUnavailable, "no signature verifier is configured; nothing is downloaded or executed without manifest verification")
	}
	rep, err := c.verifier.Verify(ctx, c.key, c.fingerprint, signature, manifest)
	if err != nil {
		return nil, err
	}
	var m claudeManifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		return nil, refuse(KindManifestInvalid, "the signed manifest is not the JSON document this installer understands: %v", err)
	}
	if !ValidVersion(m.Version) {
		return nil, refuse(KindManifestInvalid, "the signed manifest declares version %q, which is not a valid version string", m.Version)
	}
	vm := &VerifiedManifest{Version: m.Version, Artifacts: map[string]Artifact{}}
	for key, e := range m.Platforms {
		if !vendorKeyRe.MatchString(key) {
			return nil, refuse(KindManifestInvalid, "the signed manifest names platform %q, which is not a valid platform key", key)
		}
		if e.Binary == "" || e.Binary != filepath.Base(e.Binary) || strings.ContainsAny(e.Binary, `/\`) || strings.HasPrefix(e.Binary, ".") {
			return nil, refuse(KindManifestInvalid, "the signed manifest names binary %q for %s, which is not a plain file name", e.Binary, key)
		}
		if !isHex64(e.Checksum) {
			return nil, refuse(KindManifestInvalid, "the signed manifest checksum for %s is not a lowercase 64-hex SHA-256", key)
		}
		if e.Size <= 0 || e.Size > claudeArtifactCap {
			return nil, refuse(KindManifestInvalid, "the signed manifest size %d for %s is outside (0, %d]", e.Size, key, claudeArtifactCap)
		}
		vm.Artifacts[key] = Artifact{Name: e.Binary, SHA256: e.Checksum, Size: e.Size}
	}
	vm.Provenance = Provenance{
		Class: ProvenancePublisherSigned, Verifier: rep.Verifier,
		KeyFingerprint: rep.PrimaryFingerprint, SigningKeyFingerprint: rep.SigningFingerprint,
		SignatureCreated: rep.Created, ManifestSHA256: sha256Hex(manifest), SignatureSHA256: sha256Hex(signature),
		KeySHA256: sha256Hex(c.key),
	}
	return vm, nil
}

// Resolve fetches the pointer (for latest/stable), the manifest and its
// signature, verifies the signature, and binds the platform's artifact. It
// performs metadata GETs only.
func (c *Claude) Resolve(ctx context.Context, req Request) (*Plan, *Material, error) {
	vendorPlatform, err := claudePlatformKey(req.Platform)
	if err != nil {
		return nil, nil, err
	}
	base, kind := c.base, SourceOfficial
	if req.Source != "" {
		if base, err = baseURL(req.Source); err != nil {
			return nil, nil, err
		}
		if base != c.base {
			kind = SourceMirror
		}
	}
	src := Source{Kind: kind, BaseURL: base}
	channel, version := ChannelExact, strings.TrimSpace(req.Version)
	switch version {
	case ChannelLatest, ChannelStable:
		channel = version
		src.PointerURL = base + "/" + version
		body, status, err := c.fetch.small(ctx, src.PointerURL, claudePointerCap)
		if err != nil {
			return nil, nil, err
		}
		if status != http.StatusOK {
			return nil, nil, refuse(KindVersionUnknown, "the %s pointer at %s answered HTTP %d, so no version can be selected", version, src.PointerURL, status)
		}
		version = strings.TrimSpace(string(body))
		if !ValidVersion(version) {
			return nil, nil, refuse(KindVersionUnknown, "the %s pointer at %s returned %q, which is not a version", channel, src.PointerURL, version)
		}
	default:
		if !ValidVersion(version) {
			return nil, nil, refuse(KindInvalidRequest, "version %q is neither an exact version (X.Y.Z) nor one of latest, stable", req.Version)
		}
	}
	src.ManifestURL = base + "/" + version + "/manifest.json"
	src.SignatureURL = src.ManifestURL + ".sig"
	manifest, status, err := c.fetch.small(ctx, src.ManifestURL, claudeManifestCap)
	if err != nil {
		return nil, nil, err
	}
	switch status {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusForbidden:
		return nil, nil, refuse(KindVersionUnknown, "%s has no manifest for version %s (HTTP %d)", base, version, status)
	default:
		return nil, nil, refuse(KindTransport, "GET %s: HTTP %d", src.ManifestURL, status)
	}
	signature, status, err := c.fetch.small(ctx, src.SignatureURL, claudeSignatureCap)
	if err != nil {
		return nil, nil, err
	}
	switch status {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusForbidden:
		return nil, nil, refuse(KindUnsupportedSource, "%s publishes a manifest for %s but no detached signature (%s answered HTTP %d); an unsigned manifest is never used", base, version, src.SignatureURL, status)
	default:
		return nil, nil, refuse(KindTransport, "GET %s: HTTP %d", src.SignatureURL, status)
	}
	vm, err := c.verifyManifest(ctx, manifest, signature)
	if err != nil {
		return nil, nil, err
	}
	if vm.Version != version {
		return nil, nil, refuse(KindManifestInvalid, "the signed manifest at %s declares version %s, not the requested %s", src.ManifestURL, vm.Version, version)
	}
	art, ok := vm.Artifacts[vendorPlatform]
	if !ok {
		return nil, nil, refuse(KindUnsupportedPlatform, "the signed manifest for %s lists no %s artifact", version, vendorPlatform)
	}
	if req.Platform.OS == "linux" && art.Name != claudeDriver {
		return nil, nil, refuse(KindManifestInvalid, "the signed manifest names the Linux binary %q; this installer expects %q", art.Name, claudeDriver)
	}
	src.ArtifactURL = base + "/" + version + "/" + vendorPlatform + "/" + art.Name
	plan := &Plan{
		Schema: PlanSchema, Driver: claudeDriver, Channel: channel, RequestedVersion: req.Version, Version: version,
		Platform: req.Platform, VendorPlatform: vendorPlatform, Source: src, Artifact: art, Provenance: vm.Provenance,
		Destination: destinationFor(req.DestRoot, claudeDriver, version, vendorPlatform, art.Name),
		Verified:    []string{"manifest_signature", "manifest_version", "artifact_selection"},
	}
	return plan, &Material{Manifest: manifest, Signature: signature, Key: c.key}, nil
}

// VerifyMaterial re-verifies retained metadata under the pinned key. The
// retained copy of the key is not consulted: trust comes from the pin.
func (c *Claude) VerifyMaterial(ctx context.Context, m *Material) (*VerifiedManifest, error) {
	if m == nil || len(m.Manifest) == 0 || len(m.Signature) == 0 {
		return nil, refuse(KindSignatureInvalid, "retained manifest or signature is empty")
	}
	return c.verifyManifest(ctx, m.Manifest, m.Signature)
}

func (c *Claude) FetchArtifact(ctx context.Context, plan *Plan, w io.Writer) (Artifact, error) {
	if plan.Source.ArtifactURL == "" || plan.Artifact.Size <= 0 || !isHex64(plan.Artifact.SHA256) {
		return Artifact{}, refuse(KindInvalidRequest, "plan carries no complete artifact selection")
	}
	return c.fetch.exact(ctx, plan.Source.ArtifactURL, plan.Artifact, w)
}

// Probe runs "claude --version" from an empty home. The environment is fixed and
// complete: nothing of the operator's environment, PATH or credentials reaches
// the tool, and auto-update and telemetry are disabled by the vendor's own
// documented variables so the probe changes nothing and phones nowhere.
func (c *Claude) Probe(ctx context.Context, exe, scratch, wantVersion string) (ProbeReport, error) {
	home := filepath.Join(scratch, "home")
	config := filepath.Join(scratch, "config")
	tmp := filepath.Join(scratch, "tmp")
	for _, d := range []string{home, config, tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return ProbeReport{}, refuse(KindProbeFailed, "prepare probe home: %v", err)
		}
	}
	spec := probeSpec{
		Exe: exe, Args: []string{"--version"}, Dir: home, Budget: c.probeBudget, Grace: c.probeGrace,
		Env: []string{
			"HOME=" + home,
			"CLAUDE_CONFIG_DIR=" + config,
			"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
			"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
			"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
			"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
			"TMPDIR=" + tmp,
			"PATH=" + claudeProbeFixedPath,
			"DISABLE_AUTOUPDATER=1",
			"DISABLE_TELEMETRY=1",
			"DISABLE_ERROR_REPORTING=1",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
			"TERM=dumb",
			"LANG=C.UTF-8",
		},
	}
	rep, err := runProbe(ctx, spec)
	if err != nil {
		return rep, err
	}
	line := firstLine(rep.Output)
	fields := strings.Fields(line)
	if len(fields) == 0 || !strings.Contains(line, "Claude Code") {
		return rep, refuse(KindProbeMismatch, "%s --version printed %q, which does not identify Claude Code", exe, line)
	}
	if wantVersion != "" && fields[0] != wantVersion {
		return rep, refuse(KindProbeMismatch, "%s --version reports %q; the verified manifest says %s", exe, fields[0], wantVersion)
	}
	return rep, nil
}

// DefaultPaths are the vendor's native install layout and the package-repository
// path. None of them is under the vendor's configuration directory.
func (c *Claude) DefaultPaths(home string) []string {
	paths := []string{"/usr/bin/claude", "/usr/local/bin/claude"}
	if filepath.IsAbs(home) {
		paths = append(paths, filepath.Join(home, ".local", "bin", "claude"))
		if versions, err := filepath.Glob(filepath.Join(home, ".local", "share", "claude", "versions", "*")); err == nil {
			paths = append(paths, versions...)
		}
	}
	return paths
}

func destinationFor(root, driver, version, vendorPlatform, binary string) Destination {
	dir := filepath.Join(root, driver, version+"-"+vendorPlatform)
	return Destination{Root: root, ReleaseDir: dir, Executable: filepath.Join(dir, "bin", binary)}
}

// String describes the adapter for help and receipts.
func (c *Claude) String() string {
	return fmt.Sprintf("claude (%s, key %s)", c.base, c.fingerprint)
}
