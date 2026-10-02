// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package toolinstall

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ReleaseArchiveOptions allows the composition root to supply its HTTP client.
// Origins, repository identity, checksum policy and layout cannot be overridden.
type ReleaseArchiveOptions struct{ Client *http.Client }

// ReleaseArchive installs a checksum-pinned archive from one official GitHub
// repository. GitHub's metadata checksum is an origin assertion, not a detached
// publisher signature. The plan and receipt preserve that distinction.
type ReleaseArchive struct {
	driver string
	fetch  fetcher
}

func NewOpenCode(options ReleaseArchiveOptions) *ReleaseArchive {
	return newReleaseArchive(DriverOpenCode, options)
}
func NewOllama(options ReleaseArchiveOptions) *ReleaseArchive {
	return newReleaseArchive(DriverOllama, options)
}

// NewCodexRelease installs the official Codex CLI from github.com/openai/codex
// releases, pinned to the SHA-256 the release metadata states for the platform
// archive. The chatgpt.com/codex origin the Codex package installer used answers
// 404, so this is the installer the engine wires for Codex.
func NewCodexRelease(options ReleaseArchiveOptions) *ReleaseArchive {
	return newReleaseArchive(DriverCodex, options)
}
func newReleaseArchive(driver string, options ReleaseArchiveOptions) *ReleaseArchive {
	client := options.Client
	if client == nil {
		client = NewHTTPClient()
	}
	return &ReleaseArchive{driver: driver, fetch: fetcher{client: client}}
}
func (p *ReleaseArchive) Key() string               { return p.driver }
func (p *ReleaseArchive) VerificationLevel() string { return VerificationGitHubReleaseSHA256 }

type githubReleaseMetadata struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name   string `json:"name"`
		Size   int64  `json:"size"`
		Digest string `json:"digest"`
		URL    string `json:"browser_download_url"`
	} `json:"assets"`
}

func (p *ReleaseArchive) metadata(ctx context.Context, u string) (githubReleaseMetadata, []byte, error) {
	// Metadata never follows a redirect away from the pinned official API.
	fetch := p.fetch.confine(RedirectPolicyV2{MaxHops: 0}, []string{"https://api.github.com"})
	body, status, err := fetch.small(ctx, u, 2<<20)
	if err != nil {
		return githubReleaseMetadata{}, nil, err
	}
	if err := statusOrTransport(status, u, "release metadata"); err != nil {
		return githubReleaseMetadata{}, nil, err
	}
	var meta githubReleaseMetadata
	if err := json.Unmarshal(body, &meta); err != nil {
		return meta, nil, refuse(KindManifestInvalid, "official release metadata is invalid")
	}
	prefix := releaseTagPrefix(p.driver)
	if meta.Draft || meta.Prerelease || !strings.HasPrefix(meta.Tag, prefix) || !ValidVersion(strings.TrimPrefix(meta.Tag, prefix)) {
		return meta, nil, refuse(KindVersionUnknown, "official release metadata does not name a supported stable semantic version")
	}
	return meta, body, nil
}
func (p *ReleaseArchive) ResolveV2(ctx context.Context, req RequestV2) (*PlanV2, *ResolvedMaterialV2, error) {
	if req.Driver != p.driver || req.Source != "" {
		return nil, nil, refuse(KindUnsupportedSource, "this archive adapter accepts only its pinned official repository")
	}
	platform, vendor, err := PlatformV2For(p.driver, Platform{OS: req.Platform.OS, Arch: req.Platform.Arch})
	if err != nil {
		return nil, nil, err
	}
	if req.Platform.Libc != "" && req.Platform.Libc != platform.Libc {
		return nil, nil, refuse(KindUnsupportedPlatform, "release archive libc does not match its fixed platform")
	}
	requested := strings.TrimSpace(req.Version)
	channel := ChannelExact
	endpoint := "https://api.github.com/repos/" + releaseRepository(p.driver) + "/releases/"
	prefix := releaseTagPrefix(p.driver)
	pointer := endpoint + "tags/" + prefix + requested
	if requested == ChannelLatest || requested == ChannelStable {
		channel = requested
		pointer = endpoint + "latest"
	} else if !ValidVersion(requested) {
		return nil, nil, refuse(KindInvalidRequest, "select latest, stable or an exact semantic version")
	}
	meta, pointerBody, err := p.metadata(ctx, pointer)
	if err != nil {
		return nil, nil, err
	}
	version := strings.TrimPrefix(meta.Tag, prefix)
	if channel == ChannelExact && version != requested {
		return nil, nil, refuse(KindVersionUnknown, "official release tag differs from the requested version")
	}
	checksumURL := endpoint + "tags/" + prefix + version
	checksumBody := pointerBody
	if pointer != checksumURL {
		meta, checksumBody, err = p.metadata(ctx, checksumURL)
		if err != nil {
			return nil, nil, err
		}
		if meta.Tag != prefix+version {
			return nil, nil, refuse(KindPlanChanged, "official release tag changed while resolving")
		}
	}
	layout := releaseArchiveLayout(p.driver)
	assetName := releaseAssetName(p.driver, vendor)
	pkgURL := "https://github.com/" + releaseRepository(p.driver) + "/releases/download/" + prefix + version + "/" + assetName
	var digest string
	var size int64
	matches := 0
	for _, asset := range meta.Assets {
		if asset.Name != assetName {
			continue
		}
		matches++
		digest = strings.TrimPrefix(asset.Digest, "sha256:")
		size = asset.Size
		if asset.Digest != "sha256:"+digest || !isHex64(digest) || asset.URL != pkgURL {
			return nil, nil, refuse(KindManifestInvalid, "official release asset lacks an exact SHA-256 or expected archive URL")
		}
	}
	if matches != 1 || size <= 0 || size > layout.Limits.MaxCompressedBytes {
		return nil, nil, refuse(KindManifestInvalid, "official release must name exactly one bounded checksummed archive for this platform")
	}
	selection := SelectionV2{Driver: p.driver, Channel: channel, RequestedVersion: requested, Version: version, Platform: platform, VendorPlatform: vendor,
		Source:        SourcePolicyV2{Kind: SourceOfficial, Pointer: URLRef{State: URLStatePresent, URL: pointer}, Checksums: URLRef{State: URLStatePresent, URL: checksumURL}, Package: URLRef{State: URLStatePresent, URL: pkgURL}, Proofs: []ProofLocator{}, AllowedOrigins: []string{"https://api.github.com", "https://github.com", "https://release-assets.githubusercontent.com"}, Redirects: RedirectPolicyV2{MaxHops: 3, AllowedOrigins: []string{"https://release-assets.githubusercontent.com"}}},
		FetchedObject: FetchedObjectExpectation{DigestState: DigestStateExact, SHA256: digest, SizeState: SizeStateExact, Size: size, MaxSize: layout.Limits.MaxCompressedBytes}, Layout: layout, RequiredSubjects: []SubjectExpectation{}, PackagePolicyID: PackagePolicyReleaseArchiveV1, Verification: VerificationProfileV2{Kind: VerificationGitHubReleaseSHA256}, Destination: destinationFor(req.DestRoot, p.driver, version, vendor, p.driver)}
	plan := &PlanV2{Schema: PlanSchemaV2, Selection: selection}
	if err := plan.validate(); err != nil {
		return nil, nil, err
	}
	plan.Digest = ComputeDigestV2(plan)
	return plan, &ResolvedMaterialV2{Pointer: resolvedFromURL(selection.Source.Pointer, pointerBody), Checksums: resolvedFromURL(selection.Source.Checksums, checksumBody), Proofs: []ResolvedProofV2{}}, nil
}

// routePointer is the pointer ResolveV2 records for sel's release selected
// through channel: the tag metadata for its exact version, else the latest pointer.
func (p *ReleaseArchive) routePointer(sel SelectionV2, channel string) string {
	endpoint := "https://api.github.com/repos/" + releaseRepository(p.driver) + "/releases/"
	if channel == ChannelExact {
		return endpoint + "tags/" + releaseTagPrefix(p.driver) + sel.Version
	}
	return endpoint + "latest"
}
func (p *ReleaseArchive) FetchV2(ctx context.Context, plan *PlanV2, w io.Writer) (FetchedObjectObserved, error) {
	if err := plan.validate(); err != nil {
		return FetchedObjectObserved{}, err
	}
	expected := plan.Selection.FetchedObject
	return p.fetch.confine(plan.Selection.Source.Redirects, plan.Selection.Source.AllowedOrigins).exactSizeHash(ctx, plan.Selection.Source.Package.URL, expected.SHA256, expected.Size, expected.MaxSize, w)
}
func (p *ReleaseArchive) PlaceV2(ctx context.Context, plan *PlanV2, root *os.Root, staging, fetched string) (*ObservedPayloadInventory, error) {
	if err := plan.validate(); err != nil {
		return nil, err
	}
	// Defense at the placement boundary: no payload member is written until the
	// complete fetched object matches the selected official metadata checksum.
	digest, size, err := fileSHA256Root(root, fetched)
	if err != nil {
		return nil, err
	}
	if digest != plan.Selection.FetchedObject.SHA256 || size != plan.Selection.FetchedObject.Size {
		return nil, refuse(KindDigestMismatch, "archive does not match the approved official release checksum")
	}
	return extractReleaseArchive(ctx, p.driver, root, staging, fetched, plan.Selection.Layout)
}
func (p *ReleaseArchive) VerifyPayload(ctx context.Context, access PayloadAccess, inv *ObservedPayloadInventory, policy PackagePolicyV2) error {
	if policy.ID != PackagePolicyReleaseArchiveV1 || policy.Verification.Kind != VerificationGitHubReleaseSHA256 || !releaseArchiveLayoutID(policy.Layout.ID) || policy.Layout.Variant != p.driver || len(policy.RequiredSubjects) != 0 {
		return refuse(KindInvalidRequest, "release archive verification policy mismatch")
	}
	return verifyReleaseArchiveInventory(ctx, access, inv, p.driver, policy.Layout.Limits)
}
func (p *ReleaseArchive) Probe(ctx context.Context, exe, scratch, want string) (ProbeReport, error) {
	home := filepath.Join(scratch, "home")
	tmp := filepath.Join(scratch, "tmp")
	for _, dir := range []string{home, tmp} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return ProbeReport{}, err
		}
	}
	report, err := runProbe(ctx, probeSpec{Exe: exe, Args: []string{"--version"}, Dir: home, Budget: 20 * time.Second, Grace: 3 * time.Second, Env: []string{"HOME=" + home, "TMPDIR=" + tmp, "PATH=/usr/bin:/bin", "TERM=dumb", "LANG=C.UTF-8"}})
	if err != nil {
		return report, err
	}
	output := strings.TrimSpace(report.Output)
	if p.driver == DriverOllama && !strings.Contains(strings.ToLower(output), "ollama") && !strings.Contains(strings.ToLower(output), "client version") {
		return report, refuse(KindProbeMismatch, "version probe does not identify Ollama")
	}
	got := probeVersion(p.driver, output)
	if got == "" || (want != "" && got != want) {
		return report, refuse(KindProbeMismatch, "version probe does not match the selected release")
	}
	return report, nil
}
func (p *ReleaseArchive) DefaultPaths(home string) []string {
	paths := []string{"/usr/bin/" + p.driver, "/usr/local/bin/" + p.driver}
	if filepath.IsAbs(home) {
		paths = append(paths, filepath.Join(home, ".local/bin", p.driver))
		if p.driver == DriverOpenCode {
			paths = append(paths, filepath.Join(home, ".opencode/bin/opencode"))
		}
	}
	return paths
}

var _ PackageProviderV2 = (*ReleaseArchive)(nil)
