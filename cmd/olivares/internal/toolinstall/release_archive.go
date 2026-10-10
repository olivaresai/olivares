// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package toolinstall

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// ReleaseArchiveOptions allows the composition root to supply its HTTP client.
// Origins, repository identity, checksum policy and layout cannot be overridden.
type ReleaseArchiveOptions struct{ Client *http.Client }

// ReleaseArchive installs a checksum-pinned archive from one official GitHub
// repository. The checksum the release states (its own checksum file, else GitHub's
// metadata) is an origin assertion, not a detached publisher signature. The plan and
// receipt preserve that distinction.
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
// releases, pinned to the SHA-256 the release states for the platform archive.
// The chatgpt.com/codex origin the Codex package installer used answers 404, so
// this is the installer the engine wires for Codex.
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
	Tag        string               `json:"tag_name"`
	Draft      bool                 `json:"draft"`
	Prerelease bool                 `json:"prerelease"`
	Assets     []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
	URL    string `json:"browser_download_url"`
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
	return p.parseMetadata(body)
}

// parseMetadata reads official release metadata and refuses anything but a stable
// release of this driver's tag line.
func (p *ReleaseArchive) parseMetadata(body []byte) (githubReleaseMetadata, []byte, error) {
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
	if requested == ChannelLatest || requested == ChannelStable {
		channel = requested
	} else if !ValidVersion(requested) {
		return nil, nil, refuse(KindInvalidRequest, "select latest, stable or an exact semantic version")
	}
	// GitHub's API answers a network without a token 60 times an hour, so a release
	// that attaches its own checksum file is planned from github.com alone: latest and
	// stable from the releases/latest redirect, which never names a pre-release or a
	// draft. Only the API metadata says whether another exact version is a pre-release,
	// so an exact version reads it first; when the API limit is used up, the checksum
	// file admits it only when releases/latest names that very version.
	var f releaseFacts
	ok := false
	if channel != ChannelExact {
		f, ok = p.fromChecksumFile(ctx, channel, requested, vendor)
	}
	if !ok {
		f, err = p.fromMetadata(ctx, channel, requested, vendor)
		if channel == ChannelExact && errors.Is(err, errGitHubLimitUsedUp) {
			if latest, latestOK := p.fromChecksumFile(ctx, ChannelLatest, requested, vendor); latestOK && latest.version == requested {
				latest.pointer, latest.pointerBody = latest.checksums, latest.checksumBody
				f, err = latest, nil
			}
		}
		if err != nil {
			return nil, nil, err
		}
	}
	if f.qualified {
		channel, requested = ChannelExact, f.version
	}
	if f.size <= 0 || f.size > f.layout.Limits.MaxCompressedBytes {
		return nil, nil, refuse(KindManifestInvalid, "official release must name exactly one bounded checksummed archive for this platform")
	}
	selection := SelectionV2{Driver: p.driver, Channel: channel, RequestedVersion: requested, Version: f.version, Platform: platform, VendorPlatform: vendor,
		Source:        SourcePolicyV2{Kind: SourceOfficial, Pointer: URLRef{State: URLStatePresent, URL: f.pointer}, Checksums: URLRef{State: URLStatePresent, URL: f.checksums}, Package: URLRef{State: URLStatePresent, URL: p.download(f.version, f.asset)}, Proofs: []ProofLocator{}, AllowedOrigins: []string{"https://api.github.com", "https://github.com", "https://release-assets.githubusercontent.com"}, Redirects: RedirectPolicyV2{MaxHops: 3, AllowedOrigins: []string{"https://release-assets.githubusercontent.com"}}},
		FetchedObject: FetchedObjectExpectation{DigestState: DigestStateExact, SHA256: f.digest, SizeState: SizeStateExact, Size: f.size, MaxSize: f.layout.Limits.MaxCompressedBytes}, Layout: f.layout, RequiredSubjects: []SubjectExpectation{}, PackagePolicyID: PackagePolicyReleaseArchiveV1, Verification: VerificationProfileV2{Kind: VerificationGitHubReleaseSHA256}, Destination: destinationFor(req.DestRoot, p.driver, f.version, vendor, filepath.Base(f.layout.EntryPoint))}
	plan := &PlanV2{Schema: PlanSchemaV2, Selection: selection}
	if err := plan.validate(); err != nil {
		return nil, nil, err
	}
	plan.Digest = ComputeDigestV2(plan)
	return plan, &ResolvedMaterialV2{Pointer: resolvedFromURL(selection.Source.Pointer, f.pointerBody), Checksums: resolvedFromURL(selection.Source.Checksums, f.checksumBody), Proofs: []ResolvedProofV2{}}, nil
}

// releaseFacts is what a plan binds about one release, from the official document
// that stated it.
type releaseFacts struct {
	version, pointer, checksums, asset, digest string
	pointerBody, checksumBody                  []byte
	layout                                     ExpectedLayout
	size                                       int64
	qualified                                  bool // the recorded metadata of the qualified release, installed exactly
}

// fromChecksumFile reads the release from github.com: releases/latest redirects to
// its tag, the release's own checksum file states the archive's SHA-256, and the
// archive's download answers HEAD with its size. ok is false when the repository
// attaches no checksum file, or github.com does not give one of these answers (an
// older release without the file, a route that changed); the API metadata decides.
func (p *ReleaseArchive) fromChecksumFile(ctx context.Context, channel, requested, vendor string) (releaseFacts, bool) {
	file := releaseChecksumFile(p.driver)
	if file == "" {
		return releaseFacts{}, false
	}
	repo, prefix := releaseRepository(p.driver), releaseTagPrefix(p.driver)
	f := releaseFacts{version: requested, layout: releaseArchiveLayout(p.driver), asset: releaseAssetName(p.driver, vendor)}
	if channel != ChannelExact {
		f.pointer = "https://github.com/" + repo + "/releases/latest"
		loc, status, err := p.fetch.redirect(ctx, f.pointer)
		tag, found := strings.CutPrefix(loc, "https://github.com/"+repo+"/releases/tag/")
		if err != nil || status != http.StatusFound || !found || !strings.HasPrefix(tag, prefix) || !ValidVersion(strings.TrimPrefix(tag, prefix)) || strings.Contains(strings.TrimPrefix(tag, prefix), "-") {
			return f, false
		}
		f.version, f.pointerBody = strings.TrimPrefix(tag, prefix), []byte(loc)
	}
	assets := p.fetch.confine(RedirectPolicyV2{MaxHops: 3, AllowedOrigins: []string{"https://release-assets.githubusercontent.com"}}, []string{"https://github.com"})
	f.checksums = p.download(f.version, file)
	body, status, err := assets.small(ctx, f.checksums, 1<<20)
	if err != nil || status != http.StatusOK {
		return f, false
	}
	f.checksumBody = body
	if channel == ChannelExact {
		f.pointer, f.pointerBody = f.checksums, body
	}
	if f.digest = checksumFor(body, f.asset); f.digest == "" {
		return f, false
	}
	if f.size, err = assets.size(ctx, p.download(f.version, f.asset)); err != nil {
		return f, false
	}
	return f, true
}

// checksumFor is the SHA-256 a sha256sum-format file states for name ("./name" and
// "*name" are name), or "" unless exactly one line states one.
func checksumFor(body []byte, name string) string {
	var found []string
	for _, line := range strings.Split(string(body), "\n") {
		if f := strings.Fields(line); len(f) == 2 && strings.TrimPrefix(strings.TrimPrefix(f[1], "*"), "./") == name && isHex64(f[0]) {
			found = append(found, strings.ToLower(f[0]))
		}
	}
	if len(found) != 1 {
		return ""
	}
	return found[0]
}

// fromMetadata reads the release from GitHub's API metadata: the latest pointer or
// the tag, and the tag's assets with their SHA-256 and size.
func (p *ReleaseArchive) fromMetadata(ctx context.Context, channel, requested, vendor string) (releaseFacts, error) {
	endpoint := "https://api.github.com/repos/" + releaseRepository(p.driver) + "/releases/"
	prefix := releaseTagPrefix(p.driver)
	f := releaseFacts{pointer: endpoint + "tags/" + prefix + requested}
	if channel != ChannelExact {
		f.pointer = endpoint + "latest"
	}
	meta, err := p.readMetadata(ctx, &f, channel, requested, endpoint, prefix)
	if errors.Is(err, errGitHubLimitUsedUp) {
		// With GitHub's API limit used up at either read, the release this build was
		// qualified with, when it is what was asked for, is installed exactly from its
		// recorded metadata.
		if body, ok := qualifiedRelease(p.driver); ok {
			if q, qbody, qerr := p.parseMetadata(body); qerr == nil && (channel != ChannelExact || q.Tag == prefix+requested) {
				meta, err = q, nil
				f.version, f.qualified = strings.TrimPrefix(q.Tag, prefix), true
				f.pointer, f.pointerBody = endpoint+"tags/"+q.Tag, qbody
				f.checksums, f.checksumBody = f.pointer, qbody
			}
		}
	}
	if err != nil {
		return f, err
	}
	f.layout, f.asset = releaseArchiveLayout(p.driver), releaseAssetName(p.driver, vendor)
	if p.driver == DriverCodex && !slices.ContainsFunc(meta.Assets, func(a githubReleaseAsset) bool { return a.Name == f.asset }) {
		// A release without the package (older Codex) keeps the single-binary archive.
		f.layout, f.asset = codexArchiveV1(vendor)
	}
	matches := 0
	for _, asset := range meta.Assets {
		if asset.Name != f.asset {
			continue
		}
		matches++
		f.digest = strings.TrimPrefix(asset.Digest, "sha256:")
		f.size = asset.Size
		if asset.Digest != "sha256:"+f.digest || !isHex64(f.digest) || asset.URL != p.download(f.version, f.asset) {
			return f, refuse(KindManifestInvalid, "official release asset lacks an exact SHA-256 or expected archive URL")
		}
	}
	if matches != 1 {
		return f, refuse(KindManifestInvalid, "official release must name exactly one bounded checksummed archive for this platform")
	}
	return f, nil
}

// readMetadata reads the pointer and, when the pointer is the latest release, that
// release's own tag metadata, which the plan records as its checksums.
func (p *ReleaseArchive) readMetadata(ctx context.Context, f *releaseFacts, channel, requested, endpoint, prefix string) (githubReleaseMetadata, error) {
	meta, pointerBody, err := p.metadata(ctx, f.pointer)
	if err != nil {
		return meta, err
	}
	f.version, f.pointerBody = strings.TrimPrefix(meta.Tag, prefix), pointerBody
	if channel == ChannelExact && f.version != requested {
		return meta, refuse(KindVersionUnknown, "official release tag differs from the requested version")
	}
	f.checksums, f.checksumBody = endpoint+"tags/"+prefix+f.version, pointerBody
	if f.pointer == f.checksums {
		return meta, nil
	}
	if meta, f.checksumBody, err = p.metadata(ctx, f.checksums); err != nil {
		return meta, err
	}
	if meta.Tag != prefix+f.version {
		return meta, refuse(KindPlanChanged, "official release tag changed while resolving")
	}
	return meta, nil
}

// download is the github.com address of file attached to version's release.
func (p *ReleaseArchive) download(version, file string) string {
	return "https://github.com/" + releaseRepository(p.driver) + "/releases/download/" + releaseTagPrefix(p.driver) + version + "/" + file
}

// routePointer is the pointer ResolveV2 records for sel's release selected
// through channel: for an exact version the checksum document itself, else the
// latest pointer of that document's origin.
func (p *ReleaseArchive) routePointer(sel SelectionV2, channel string) string {
	if channel == ChannelExact {
		return sel.Source.Checksums.URL
	}
	if strings.HasPrefix(sel.Source.Checksums.URL, "https://api.github.com/") {
		return "https://api.github.com/repos/" + releaseRepository(p.driver) + "/releases/latest"
	}
	return "https://github.com/" + releaseRepository(p.driver) + "/releases/latest"
}

// otherChecksums is the release's other official checksum document: an install
// planned from the API metadata is the same release when it is now planned from
// the release's own checksum file, and the reverse.
func (p *ReleaseArchive) otherChecksums(sel SelectionV2) []URLRef {
	file := releaseChecksumFile(p.driver)
	if file == "" {
		return nil
	}
	other := "https://api.github.com/repos/" + releaseRepository(p.driver) + "/releases/tags/" + releaseTagPrefix(p.driver) + sel.Version
	if sel.Source.Checksums.URL == other {
		other = p.download(sel.Version, file)
	}
	return []URLRef{{State: URLStatePresent, URL: other}}
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
	program := filepath.Base(releaseArchiveLayout(p.driver).EntryPoint)
	paths := []string{"/usr/bin/" + program, "/usr/local/bin/" + program}
	if filepath.IsAbs(home) {
		paths = append(paths, filepath.Join(home, ".local/bin", program))
		if p.driver == DriverOpenCode {
			paths = append(paths, filepath.Join(home, ".opencode/bin/opencode"))
		}
	}
	return paths
}

var _ PackageProviderV2 = (*ReleaseArchive)(nil)
