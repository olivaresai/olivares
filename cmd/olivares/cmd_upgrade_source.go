// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/olivaresai/olivares/core/release"
)

// cmd_upgrade_source.go abstracts WHERE an update manifest + artifact come from,
// so the one upgrade flow (verify → plan → swap) serves three transports without
// branching:
//
//   - communitySource : plain public HTTPS, no token and no license — the community
//                       edition. It serves BOTH public layouts, because WHERE the
//                       objects live is resolved once for every reader of a channel by
//                       release.ResolveChannel: the GitHub Releases of the public
//                       repository (the carrier signed on 2026-08-21, FIRMA B) and
//                       a static mirror base (the fallback the same signature keeps).
//   - gatedSource     : the licensed download worker (gate.ts contract) — a
//                       token authorizes the enterprise artifact; reuses downloadGated.
//   - bundleSource    : a local air-gapped bundle directory — no network at all. The
//                       ROUTE is license-gated in buildUpdateSource (C02-20); this
//                       transport itself only reads files, which is why the gate lives
//                       there and not here.
//
// Every source yields the SAME two things: the signed manifest (+ its detached
// signature) and, later, one artifact's bytes. The trust boundary is identical for
// all three — the manifest signature and the artifact SHA-256 are verified by the
// caller AFTER fetch, so an untrusted transport (plain HTTP, a hostile mirror, a
// tampered bundle) can never get unsigned bytes executed.

// updateSource is the transport contract for the upgrade flow.
type updateSource interface {
	// fetchManifest returns the raw manifest bytes and its detached signature.
	fetchManifest(ctx context.Context) (manifest, sig []byte, err error)
	// fetchArtifact returns the bytes of one manifest artifact. It receives the
	// VERIFIED manifest as well as the artifact entry because a transport may need a
	// signed field to locate the bytes — the release-asset layout derives its tag from
	// m.Version rather than reading a mutable "latest" pointer a second time.
	// Transports that do not need it ignore it; passing the manifest is what keeps
	// that derivation on data whose signature has already been checked.
	fetchArtifact(ctx context.Context, m release.Manifest, a release.Artifact) ([]byte, error)
	// describe is a short human label for logs (never carries a secret).
	describe() string
}

// --- community: one public HTTP transport, two layouts ------------------------
//
// WHERE the manifest, its signature and an artifact live is NOT decided here: it is
// decided once, for every reader of a public channel, by release.ResolveChannel. See
// core/release/channelurl.go for the two layouts FIRMA B keeps alive (the GitHub release
// assets that carry the community channel, and the static mirror kept as fallback) and
// for why the decision is made from the endpoint's SHAPE rather than by sniffing.
//
// This type is only the TRANSPORT: it does the GETs and nothing else. The trust anchor is
// unchanged and is the caller's — the offline Ed25519 signature over the manifest bytes
// and the artifact's signed SHA-256 — so an untrusted transport (plain HTTP, a hostile
// mirror) can never get unsigned bytes executed.
type communitySource struct {
	layout release.ChannelLayout
	client *http.Client
}

func (s communitySource) fetchManifest(ctx context.Context) ([]byte, []byte, error) {
	murl := s.layout.ManifestURL()
	m, err := httpGet(ctx, s.client, murl)
	if err != nil {
		// A MISSING CHANNEL MANIFEST IS NOT A BROKEN ENDPOINT, and telling them apart is
		// the difference between "retry" and "this channel does not exist yet". Only
		// `stable` is produced unconditionally; `security` is produced by release.yml ONLY
		// for a tag that declares an advisory (release/advisories/<version>.txt), and `lts`
		// is not produced at all until its policy exists. So a 404 for a non-stable channel
		// is an EXPECTED answer, and the operator must be told which of the two they are
		// looking at instead of being left to read a bare 404.
		return nil, nil, fmt.Errorf("fetch manifest %s: %w%s", displayEndpoint(murl), err, s.missingChannelHint())
	}
	sigURL := s.layout.SignatureURL()
	sig, err := httpGet(ctx, s.client, sigURL)
	if err != nil {
		// ⛔ WRAPPED SO THE CALLER CAN TELL WHICH GET FAILED, and that is not tidiness. Both
		// fetches return through this one error value, and a caller that only asks "was this a
		// 404?" cannot tell "the channel publishes nothing" from "the manifest is there and its
		// signature is missing". Those are opposite facts: the first is a legitimate first
		// publication, the second is a SPLIT PAIR that every conforming client refuses. The
		// external contrast found verify-channel-advance taking the first reading for the
		// second and answering 0 with a live manifest in front of it.
		return nil, nil, fmt.Errorf("%w: %s: %w", errManifestSignature, displayEndpoint(sigURL), err)
	}
	return m, sig, nil
}

// missingChannelHint appends, for a non-stable channel, the reason a release may
// legitimately not carry it. It says nothing about `stable`: a missing stable manifest
// means the channel is not published, which is a plain fault and needs no excuse.
func (s communitySource) missingChannelHint() string {
	ch := s.layout.Channel()
	if ch == release.ChannelStable {
		return ""
	}
	which := "the latest release"
	if t := s.layout.Tag(); t != "" {
		which = displayReleaseTag(t)
	}
	if !s.layout.ReleaseAssets() {
		which = "this endpoint"
	}
	return fmt.Sprintf(
		"\nNOTE: %s publishes a %q channel manifest only when it declares one; the %q channel"+
			"\n      is NOT a fallback to stable (asking for it and being served stable is refused"+
			"\n      deliberately). Use --channel stable, or pin the release that carries the %q"+
			"\n      manifest with --endpoint <repo-url>/releases/tag/<tag>.",
		which, ch, ch, ch)
}

func (s communitySource) fetchArtifact(ctx context.Context, m release.Manifest, a release.Artifact) ([]byte, error) {
	u, err := s.layout.ArtifactURL(m.Version, a.Filename)
	if err != nil {
		return nil, err
	}
	return httpGet(ctx, s.client, u)
}

func (s communitySource) describe() string {
	// Display-only: the layout still addresses the configured URL (userinfo
	// included) and the exact requested tag on the wire. The operator label is
	// scheme://host plus a bounded tag. Non-ASCII hosts, including IDN, render
	// as unavailable-endpoint.
	loc := displayEndpoint(s.layout.ManifestURL())
	if !s.layout.ReleaseAssets() {
		return "public channel " + s.layout.Channel() + " (" + loc + ")"
	}
	where := "latest release"
	if t := s.layout.Tag(); t != "" {
		where = "release " + displayReleaseTag(t)
	}
	return "public channel " + s.layout.Channel() + " (release assets of " + loc + ", " + where + ")"
}

// buildCommunitySource resolves an endpoint to the one public transport. The layout
// decision, including its deny-closed refusals, lives in release.ResolveChannel.
// Resolution and the live layout URLs are unchanged; a constructor refusal is
// wrapped so operator text never quotes the raw endpoint (userinfo, query, fragment).
func buildCommunitySource(endpoint, channel string, client *http.Client) (updateSource, error) {
	layout, err := release.ResolveChannel(endpoint, channel)
	if err != nil {
		return nil, wrapCommunitySourceResolve(endpoint, channel, err)
	}
	if schemeErr := communitySourceNonHTTPError(endpoint); schemeErr != nil {
		return nil, wrapCommunitySourceResolve(endpoint, channel, schemeErr)
	}
	return communitySource{layout: layout, client: client}, nil
}

// errCommunityNotHTTP is the cause when ResolveChannel accepted a non-HTTP(S)
// endpoint (ftp, htp, …) that this constructor refuses before any transport.
var errCommunityNotHTTP = errors.New("update endpoint is not HTTP(S)")

func communitySourceNonHTTPError(endpoint string) error {
	trimmed := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if trimmed == "" {
		return nil
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return err
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return errCommunityNotHTTP
	}
	return nil
}

// communitySourceResolveError is a display-safe constructor refusal. Unwrap keeps
// the original release.ResolveChannel error (and any nested parse cause). Error()
// never interpolates that text: the resolver quotes the raw endpoint.
type communitySourceResolveError struct {
	endpoint string // display-only scheme://host, or empty when unprintable
	reason   string // owned constant; never err.Error()
	err      error
}

func (e *communitySourceResolveError) Error() string {
	if e.endpoint == "" || e.endpoint == displayEndpointUnavailable {
		return "refusing update endpoint: " + e.reason
	}
	return "refusing update endpoint " + e.endpoint + ": " + e.reason
}

func (e *communitySourceResolveError) Unwrap() error { return e.err }

const (
	communityResolveEmpty     = "empty update endpoint"
	communityResolveChannel   = "channel is not one of the documented public channels"
	communityResolveAbsolute  = "want an absolute HTTP(S) URL"
	communityResolveQueryFrag = "an update endpoint is a base path, so it carries no query string and no fragment"
	communityResolveGitHub    = "on github.com a channel is served from a repository's RELEASES"
	communityResolveLayout    = "the endpoint is not a usable public channel layout"
)

func wrapCommunitySourceResolve(endpoint, channel string, err error) error {
	if err == nil {
		return nil
	}
	return &communitySourceResolveError{
		endpoint: displayEndpoint(endpoint),
		reason:   communitySourceResolveReason(endpoint, channel),
		err:      err,
	}
}

// communitySourceResolveReason classifies the operator argument, not the resolver
// error string. The argument is ours; the resolver message quotes it verbatim.
func communitySourceResolveReason(endpoint, channel string) string {
	ch := strings.TrimSpace(channel)
	if ch == "" {
		ch = release.ChannelStable
	}
	if !release.ValidChannel(ch) {
		return communityResolveChannel
	}
	trimmed := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if trimmed == "" {
		return communityResolveEmpty
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return communityResolveAbsolute
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return communityResolveAbsolute
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return communityResolveQueryFrag
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "github.com" || host == "www.github.com" {
		return communityResolveGitHub
	}
	return communityResolveLayout
}

// --- enterprise: the licensed download worker (gate contract) -----------

// gatedSource is a POINTER receiver because the release-v1 flow stashes the resolved tuple
// between fetchManifest and fetchArtifact: the manifest resolution answers {version,set,digests}
// once, and the artifact request corroborates the SAME tuple, so three requests cannot mix two
// releases. The legacy flow keeps no state.
type gatedSource struct {
	o      *upgradeOptions
	client *http.Client
	// pub is the OTA verification anchor runUpgrade resolved. The source needs it for exactly one
	// thing: after a same-version conflict at the artifact step, the fresh resolution's manifest
	// is re-verified against the SAME key before it is allowed to stand in for the one the run
	// planned against. nil fails closed (VerifyManifest → ErrNoKey).
	pub      ed25519.PublicKey
	tuple    v1Tuple
	resolved bool
	// reresolved is the ONE automatic fresh resolution addendum §4 permits for a same-version
	// metadata/set conflict, and it is a budget for the WHOLE run, not per step: a second such
	// conflict anywhere terminates with the way out. A version conflict never spends it.
	reresolved bool
	// refreshed is the Manifest VerifyManifest accepted during an artifact-step retry.
	// runUpgrade takes it and re-applies policy/plan before any installation; a nil
	// value means this fetch did not replace the run's verified manifest.
	refreshed *release.Manifest
}

// verifiedCorroborator is implemented by a source whose resolution declared a version ahead of the
// signature check. runUpgrade calls it with the manifest it has just VERIFIED, and the two
// versions must agree byte for byte before any decision is made on either.
type verifiedCorroborator interface {
	corroborateVerified(m release.Manifest) error
}

// refreshedVerifiedManifest is implemented by a source that may replace the verified
// manifest during the one run-wide same-version retry. fetchArtifact does not plan or
// install: it stores the newly verified bytes' Manifest and returns
// errFreshVerifiedManifest so runUpgrade re-applies the SAME policy/plan authority
// it used on the first resolution. A field-by-field allow list here would silently
// omit the next Manifest policy field.
type refreshedVerifiedManifest interface {
	takeRefreshedVerifiedManifest() (release.Manifest, bool)
}

// errFreshVerifiedManifest is the artifact-step signal that the run spent its one
// same-version retry, verified a new manifest against the original Ed25519
// anchor, and stored it. It is not an operator-facing refusal: runUpgrade must
// re-evaluate that manifest before requesting artifact bytes again.
var errFreshVerifiedManifest = errors.New("release-v1 same-version retry produced a freshly verified manifest")

func (s *gatedSource) legacy() bool { return s.o.downloadProtocol == downloadProtocolLegacy }

func (s *gatedSource) fetchManifest(ctx context.Context) ([]byte, []byte, error) {
	if s.legacy() {
		m, err := downloadGated(ctx, s.client, s.o, "manifest", "")
		if err != nil {
			return nil, nil, fmt.Errorf("fetch manifest: %w", err)
		}
		sig, err := downloadGated(ctx, s.client, s.o, "manifest.sig", "")
		if err != nil {
			return nil, nil, fmt.Errorf("fetch manifest signature: %w", err)
		}
		return m, sig, nil
	}
	// release-v1: ONE complete resolution — manifest, then signature under the answered tuple —
	// with the single bounded retry the contract permits: a same-version conflict on the
	// signature step (the pair or the set moved between the two requests) restarts the WHOLE
	// resolution once, from the manifest request, under fresh authorisation. A second conflict
	// terminates; a version conflict is never retried (R8 of the 2026-09-05 review).
	for {
		m, sig, err := s.resolveV1(ctx)
		if err == nil {
			return m, sig, nil
		}
		if sameVersionConflict(err) {
			if s.spendReresolution() {
				continue
			}
			return nil, nil, fmt.Errorf("%w — the publication or the entitlement changed AGAIN after one fresh resolution; not retried further, re-run when the release has settled", err)
		}
		return nil, nil, err
	}
}

// spendReresolution takes the run's single automatic fresh resolution; false once it is spent.
func (s *gatedSource) spendReresolution() bool {
	if s.reresolved {
		return false
	}
	s.reresolved = true
	return true
}

// resolveV1 performs one complete release-v1 resolution: the manifest request (fresh
// authorisation by the worker, the tuple answered in headers and validated field by field), then
// the detached signature under that SAME tuple, each response's bytes checked against the digest
// it was announced with and each success corroborated against the recorded tuple. The digests are
// consistency identifiers; VerifyManifest (Ed25519) is still the trust anchor the caller applies.
func (s *gatedSource) resolveV1(ctx context.Context) ([]byte, []byte, error) {
	m, hdr, err := downloadReleaseV1(ctx, s.client, s.o, "manifest", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve release-v1 manifest: %w", err)
	}
	t, err := parseV1Tuple(hdr)
	if err != nil {
		return nil, nil, fmt.Errorf("release-v1 resolution: %w", err)
	}
	if got := sha256hex(m); got != t.manifestSHA256 {
		return nil, nil, fmt.Errorf("release-v1 manifest digest header %s does not match the served bytes (%s)", t.manifestSHA256, got)
	}
	sig, shdr, err := downloadReleaseV1(ctx, s.client, s.o, "manifest.sig", &t)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch release-v1 signature: %w", err)
	}
	if err := corroborateV1Headers(shdr, t, "signature"); err != nil {
		return nil, nil, err
	}
	if got := sha256hex(sig); got != t.signatureSHA256 {
		return nil, nil, fmt.Errorf("release-v1 signature digest header %s does not match the served bytes (%s)", t.signatureSHA256, got)
	}
	s.tuple = t
	s.resolved = true
	return m, sig, nil
}

func (s *gatedSource) corroborateVerified(m release.Manifest) error {
	if s.legacy() {
		return nil
	}
	if !s.resolved {
		return fmt.Errorf("release-v1 manifest verified before it was resolved")
	}
	if m.Version != s.tuple.version {
		return fmt.Errorf("the release-v1 resolution declared version %q but the verified signed manifest is version %q — the tuple does not describe the release it delivered; refusing to plan against either", s.tuple.version, m.Version)
	}
	return nil
}

func (s *gatedSource) fetchArtifact(ctx context.Context, m release.Manifest, a release.Artifact) ([]byte, error) {
	if s.legacy() {
		// The gate serves THE enterprise binary for the token's os/arch/channel; the version is
		// the one the caller just verified, and the bytes are bound to the signed digest next.
		return downloadGated(ctx, s.client, s.o, "", m.Version)
	}
	if !s.resolved {
		return nil, fmt.Errorf("release-v1 artifact requested before the manifest was resolved")
	}
	if m.Version != s.tuple.version {
		return nil, fmt.Errorf("release-v1 artifact requested for version %q while the resolved tuple names %q", m.Version, s.tuple.version)
	}
	for {
		body, hdr, err := downloadReleaseV1(ctx, s.client, s.o, "", &s.tuple)
		if err == nil {
			if cerr := corroborateV1Headers(hdr, s.tuple, "artifact"); cerr != nil {
				return nil, cerr
			}
			// The worker echoes the signed descriptor's digest when the manifest carries one; it
			// must be the digest this run verified. (VerifyArtifactSHA256 over the bytes follows
			// in the caller regardless — this catches a served-for-another-descriptor answer
			// before 500 MiB are hashed, it does not replace the hash.)
			if declared := hdr.Get(hdrArtifactSHA256); declared != "" && declared != a.SHA256 {
				return nil, fmt.Errorf("release-v1 artifact response declares a digest that does not match the verified descriptor for %s/%s; refusing bytes described by another manifest", a.OS, a.Arch)
			}
			return body, nil
		}
		if sameVersionConflict(err) {
			if !s.spendReresolution() {
				return nil, fmt.Errorf("%w — the publication or the entitlement changed AGAIN after one fresh resolution; not retried further, re-run when the release has settled", err)
			}
			if rerr := s.reresolveForArtifact(ctx, m, a); rerr != nil {
				return nil, rerr
			}
			// Do not fetch bytes against the new tuple here. The caller planned against
			// the previous verified manifest; runUpgrade must re-apply that same
			// policy/plan authority to `s.refreshed` before any installation.
			return nil, errFreshVerifiedManifest
		}
		return nil, err
	}
}

func (s *gatedSource) takeRefreshedVerifiedManifest() (release.Manifest, bool) {
	if s.refreshed == nil {
		return release.Manifest{}, false
	}
	m := *s.refreshed
	s.refreshed = nil
	return m, true
}

// reresolveForArtifact restarts the WHOLE coherent tuple after a same-version conflict at the
// artifact step, once: a fresh manifest resolution and its signature under the new tuple (fresh
// authorisation on both) and the Ed25519 verification the caller performed on the first manifest
// (against the same key). The retry is still the SAME release: the same signed version and the
// same signed artifact descriptor for this platform. A same-version publication that changed its
// artifact is not something to retry blindly (addendum §4): it terminates with the way out. A
// different version is a new release and needs a new authorization, never a retry from this
// bearer.
//
// The descriptor is the Artifact value ParseManifest produced for this platform — the same
// selection PlanUpgrade used — compared in full. Artifact is comparable, so inequality is
// the whole struct after that normalization (SHA-256 lowercased and trimmed). Comparing
// Filename and SHA256 alone, or the raw JSON, would accept a Size/Cosign/Variant change
// or refuse an encoding-only republish. Policy, rollout, CRL, freshness, channel and
// every other Manifest field are still NOT decided here: this method stores the verified
// Manifest and returns; runUpgrade re-applies the productive bind/plan/guard path before
// requesting bytes or swapping.
func (s *gatedSource) reresolveForArtifact(ctx context.Context, prev release.Manifest, a release.Artifact) error {
	mb, sig, err := s.resolveV1(ctx)
	if err != nil {
		return fmt.Errorf("fresh release-v1 resolution after a same-version conflict: %w", err)
	}
	m, err := release.VerifyManifest(mb, sig, s.pub)
	if err != nil {
		return fmt.Errorf("REFUSING: the re-resolved manifest does not verify: %w", err)
	}
	if m.Version != prev.Version {
		return fmt.Errorf("REFUSING: the fresh resolution publishes version %s, not the verified %s — a different version needs a new authorization, not a retry", m.Version, prev.Version)
	}
	if m.Version != s.tuple.version {
		return fmt.Errorf("REFUSING: the fresh resolution declared version %q but its verified manifest is %q", s.tuple.version, m.Version)
	}
	cur, ok := m.ArtifactFor(a.OS, a.Arch)
	if !ok || cur != a {
		return fmt.Errorf("REFUSING: after the fresh resolution the signed artifact descriptor for %s/%s differs from the one this run planned against (version %s unchanged); not retrying a potentially different artifact — re-run to plan against the current publication", a.OS, a.Arch, prev.Version)
	}
	stored := m
	s.refreshed = &stored
	return nil
}

func (s *gatedSource) describe() string {
	if s.legacy() {
		return "licensed worker channel " + s.o.channel + " (legacy download)"
	}
	return "licensed worker channel " + s.o.channel + " (release-v1)"
}

// sha256hex is the consistency-identifier digest the release-v1 client checks the served bytes
// against. It is not a signature: it proves the client holds the same bytes the resolution
// named, and nothing about who signed them.
func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// --- air-gap: a local bundle directory ---------------------------------------

type bundleSource struct{ dir string }

func (s bundleSource) fetchManifest(_ context.Context) ([]byte, []byte, error) {
	m, err := os.ReadFile(filepath.Join(s.dir, "manifest.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("bundle manifest.json: %w", err)
	}
	sig, err := os.ReadFile(filepath.Join(s.dir, "manifest.json.sig"))
	if err != nil {
		return nil, nil, fmt.Errorf("bundle manifest.json.sig: %w", err)
	}
	return m, sig, nil
}

func (s bundleSource) fetchArtifact(_ context.Context, _ release.Manifest, a release.Artifact) ([]byte, error) {
	// A bundle carries artifacts by their manifest leaf name. Reject any path
	// separators in the (signed) filename before joining — defense in depth against
	// a crafted manifest name, even though the bytes are SHA-bound afterwards.
	name := filepath.Base(a.Filename)
	if name != a.Filename || strings.ContainsRune(a.Filename, '/') || strings.ContainsRune(a.Filename, '\\') {
		return nil, fmt.Errorf("bundle artifact name %q must be a bare filename", a.Filename)
	}
	return os.ReadFile(filepath.Join(s.dir, name))
}

func (s bundleSource) describe() string { return "air-gap bundle " + s.dir }

// openBundle resolves an air-gap bundle argument to a directory containing
// {manifest.json, manifest.json.sig, <artifacts>}. A directory is used in place
// (nil cleanup); a .tar.gz is extracted, path-safely, to a temp dir the returned
// cleanup removes. No network, no signature check here — VerifyManifest does that.
func openBundle(pathArg string) (dir string, cleanup func(), err error) {
	fi, err := os.Stat(pathArg)
	if err != nil {
		return "", nil, fmt.Errorf("bundle %q: %w", pathArg, err)
	}
	if fi.IsDir() {
		return pathArg, nil, nil
	}
	data, err := os.ReadFile(pathArg)
	if err != nil {
		return "", nil, err
	}
	raw, err := maybeGunzip(data)
	if err != nil {
		return "", nil, err
	}
	tmp, err := os.MkdirTemp("", "olivares-bundle-*")
	if err != nil {
		return "", nil, err
	}
	if err := untarToDir(raw, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return "", nil, err
	}
	return tmp, func() { _ = os.RemoveAll(tmp) }, nil
}

// untarToDir extracts every regular file of a tar into dst, rejecting any member
// path that would escape dst.
func untarToDir(tarBytes []byte, dst string) error {
	tr := tar.NewReader(bytes.NewReader(tarBytes))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read bundle: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		clean, err := safeTarName(h.Name)
		if err != nil {
			return fmt.Errorf("bundle: %w", err)
		}
		if h.Size > maxArtifactBytes {
			return fmt.Errorf("bundle entry %q is too large", h.Name)
		}
		target := filepath.Join(dst, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, io.LimitReader(tr, maxArtifactBytes)); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}

// httpStatusError carries the HTTP status of a non-200 answer as a NUMBER.
//
// It exists because the one caller that must act differently on one status —
// verify-channel-advance, for which a 404 on the channel manifest means "never
// published" and every other failure means "I could not look" — would otherwise have
// to match on the words of an error message. That is the failure the canon names with
// its own measurements: when the signal lives in the text and not in the code, it is
// read wrong, and it breaks silently on the first rewording. The operator message
// names the status and a display-only endpoint; it never carries a remote body.
type httpStatusError struct {
	status int
	url    string // display-only scheme://host; never the request URL
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("GET %s returned %d", e.url, e.status)
}

// errManifestSignature marks a failure that happened fetching the DETACHED SIGNATURE rather
// than the manifest itself. See fetchManifest for why the distinction is load-bearing.
var errManifestSignature = errors.New("fetch manifest signature")

// isHTTPStatus reports whether err (or anything it wraps) is a non-200 answer with
// exactly this status.
func isHTTPStatus(err error, status int) bool {
	var se *httpStatusError
	return errors.As(err, &se) && se.status == status
}

// httpGet fetches a URL with a bounded body and a clear error on non-200.
// The request URL is used as configured (userinfo remains a live credential on
// the wire). Operator-facing errors name a display-only endpoint and never a
// remote body or a nested *url.Error string. Unwrap keeps the original chain.
func httpGet(ctx context.Context, client *http.Client, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, wrapUpgradeTransport(u, err)
	}
	req.Header.Set("User-Agent", "olivares-upgrade")
	resp, err := client.Do(req)
	if err != nil {
		return nil, wrapUpgradeTransport(u, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
		return nil, &httpStatusError{status: resp.StatusCode, url: displayEndpoint(u)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxArtifactBytes))
	if err != nil {
		return nil, wrapUpgradeTransport(u, err)
	}
	return body, nil
}

const (
	displayEndpointMaxLen        = 96
	displayEndpointUnavailable   = "unavailable-endpoint"
	displayReleaseTagMaxLen      = 64
	displayReleaseTagUnavailable = "unavailable-tag"
)

// displayEndpoint is the one operator-facing formatter for an upgrade URL.
// It returns scheme://host (port included, userinfo/path/query/fragment
// excluded), with control bytes rejected and a hard length bound. Parse
// failure is a fixed label. Non-ASCII hosts, including IDN, are that same
// label. It never mutates the request URL.
func displayEndpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return displayEndpointUnavailable
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return displayEndpointUnavailable
	}
	host := u.Host
	if host == "" {
		return displayEndpointUnavailable
	}
	out := scheme + "://" + host
	if !displayEndpointSafe(out) || len(out) > displayEndpointMaxLen {
		return displayEndpointUnavailable
	}
	return out
}

// displayReleaseTag is the one operator-facing formatter for a channel-layout
// tag. Output only: the layout still uses the exact requested tag on the wire.
// Control bytes and over-length values become a fixed label.
func displayReleaseTag(tag string) string {
	if tag == "" {
		return ""
	}
	if !displayEndpointSafe(tag) || len(tag) > displayReleaseTagMaxLen {
		return displayReleaseTagUnavailable
	}
	return tag
}

func displayEndpointSafe(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

// upgradeTransportError is a transport failure whose Error() text never
// formats a raw *url.Error (those repeat userinfo). Unwrap keeps the original
// chain so errors.Is/As still observe *url.Error, context.Canceled, and other
// nested typed causes.
type upgradeTransportError struct {
	method   string // the request method as displayed; see wrapTransportMethod
	endpoint string
	err      error
}

func (e *upgradeTransportError) Error() string {
	return e.method + " " + e.endpoint + ": " + upgradeInnerMessage(e.err)
}

func (e *upgradeTransportError) Unwrap() error { return e.err }

// wrapUpgradeTransport is wrapTransportMethod for the upgrade client, whose requests are all GET.
func wrapUpgradeTransport(rawURL string, err error) error {
	return wrapTransportMethod(http.MethodGet, rawURL, err)
}

// transportMethodMaxLen bounds a displayed method; a longer or non-token one is a fixed label.
const transportMethodMaxLen = 16

// wrapTransportMethod is the redacted transport diagnostic for a request of the given method (the
// connected client's POST and DELETE as well as upgrade's GET). The operator text names the method
// and displayEndpoint only; the original chain stays reachable through Unwrap.
func wrapTransportMethod(method, rawURL string, err error) error {
	if err == nil {
		return nil
	}
	// Only a safe outermost error can be returned directly. A wrapper around
	// one may add sensitive URL or transport text to its own Error method.
	if _, ok := err.(*upgradeTransportError); ok {
		return err
	}
	if !displayEndpointSafe(method) || len(method) > transportMethodMaxLen || strings.ContainsAny(method, ":/") {
		method = "REQUEST"
	}
	return &upgradeTransportError{method: method, endpoint: displayEndpoint(rawURL), err: err}
}

func upgradeInnerMessage(err error) string {
	var re *upgradeRedirectRefusal
	if errors.As(err, &re) {
		return re.Error()
	}
	return "network error"
}

// extractBinary turns a downloaded artifact into the raw executable to install. It
// handles the two shapes the pipeline produces: a community `.tar.gz` archive
// (goreleaser: the binary lives at the archive root as `wantName`) and an
// enterprise single (optionally gzip'd) executable. tar member paths are validated
// so a crafted archive cannot path-escape; the exec-probe is still the final gate.
func extractBinary(data []byte, wantName string) ([]byte, error) {
	raw, err := maybeGunzip(data)
	if err != nil {
		return nil, err
	}
	if !looksLikeTar(raw) {
		return raw, nil // a bare executable
	}
	return untarBinary(raw, wantName)
}

// safeTarName validates a tar member name and returns its cleaned relative path,
// rejecting anything that could escape the extraction root. It rejects backslashes
// outright: path.Clean is forward-slash-only, so a "..\evil" member would slip past
// a slash-only guard and then traverse on Windows once filepath.Join treats "\" as a
// separator. Absolute paths and "../" escapes are rejected on every platform.
func safeTarName(name string) (string, error) {
	if strings.ContainsRune(name, '\\') {
		return "", fmt.Errorf("unsafe path %q (backslash separator)", name)
	}
	clean := path.Clean("/" + name)[1:] // strip any leading slash / .. escapes
	if clean == "" || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("unsafe path %q", name)
	}
	return clean, nil
}

// looksLikeTar sniffs the POSIX tar "ustar" magic at offset 257.
func looksLikeTar(b []byte) bool {
	return len(b) >= 265 && bytes.HasPrefix(b[257:], []byte("ustar"))
}

// untarBinary returns the bytes of the executable named wantName inside a tar. It
// prefers an exact root-level match; otherwise the first regular file whose base
// name is wantName. Entry names are rejected if they escape the archive root.
func untarBinary(tarBytes []byte, wantName string) ([]byte, error) {
	tr := tar.NewReader(bytes.NewReader(tarBytes))
	var fallback []byte
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read update archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		clean, err := safeTarName(h.Name)
		if err != nil {
			return nil, fmt.Errorf("update archive: %w", err)
		}
		if h.Size > maxArtifactBytes {
			return nil, fmt.Errorf("update archive entry %q is too large", h.Name)
		}
		if path.Base(clean) != wantName {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, maxArtifactBytes))
		if err != nil {
			return nil, err
		}
		if clean == wantName { // exact root-level match wins immediately
			return b, nil
		}
		if fallback == nil {
			fallback = b
		}
	}
	if fallback != nil {
		return fallback, nil
	}
	return nil, fmt.Errorf("update archive does not contain a %q executable", wantName)
}

// maybeGunzip decompresses gzip-wrapped data, else returns it unchanged.
func maybeGunzip(data []byte) ([]byte, error) {
	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		return data, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	return io.ReadAll(io.LimitReader(zr, maxArtifactBytes))
}
