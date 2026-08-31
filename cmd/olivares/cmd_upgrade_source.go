// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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
		return nil, nil, fmt.Errorf("fetch manifest %s: %w%s", murl, err, s.missingChannelHint())
	}
	sig, err := httpGet(ctx, s.client, s.layout.SignatureURL())
	if err != nil {
		// ⛔ WRAPPED SO THE CALLER CAN TELL WHICH GET FAILED, and that is not tidiness. Both
		// fetches return through this one error value, and a caller that only asks "was this a
		// 404?" cannot tell "the channel publishes nothing" from "the manifest is there and its
		// signature is missing". Those are opposite facts: the first is a legitimate first
		// publication, the second is a SPLIT PAIR that every conforming client refuses. The
		// external contrast found verify-channel-advance taking the first reading for the
		// second and answering 0 with a live manifest in front of it.
		return nil, nil, fmt.Errorf("%w: %s: %w", errManifestSignature, s.layout.SignatureURL(), err)
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
		which = t
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

func (s communitySource) describe() string { return s.layout.Describe() }

// buildCommunitySource resolves an endpoint to the one public transport. The layout
// decision, including its deny-closed refusals, lives in release.ResolveChannel.
func buildCommunitySource(endpoint, channel string, client *http.Client) (updateSource, error) {
	layout, err := release.ResolveChannel(endpoint, channel)
	if err != nil {
		return nil, err
	}
	return communitySource{layout: layout, client: client}, nil
}

// --- enterprise: the licensed download worker (gate contract) -----------

type gatedSource struct {
	o      *upgradeOptions
	client *http.Client
}

func (s gatedSource) fetchManifest(ctx context.Context) ([]byte, []byte, error) {
	m, err := downloadGated(ctx, s.client, s.o, "manifest")
	if err != nil {
		return nil, nil, fmt.Errorf("fetch manifest: %w", err)
	}
	sig, err := downloadGated(ctx, s.client, s.o, "manifest.sig")
	if err != nil {
		return nil, nil, fmt.Errorf("fetch manifest signature: %w", err)
	}
	return m, sig, nil
}

func (s gatedSource) fetchArtifact(ctx context.Context, _ release.Manifest, _ release.Artifact) ([]byte, error) {
	// The gate serves THE enterprise binary for the token's os/arch/channel; the
	// bytes are still bound to the signed manifest digest by the caller.
	return downloadGated(ctx, s.client, s.o, "")
}

func (s gatedSource) describe() string { return "licensed worker channel " + s.o.channel }

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
// read wrong, and it breaks silently on the first rewording. The message is unchanged;
// only its type gained a field.
type httpStatusError struct {
	status int
	url    string
	body   string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("GET %s returned %d: %s", e.url, e.status, e.body)
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
func httpGet(ctx context.Context, client *http.Client, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "olivares-upgrade")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, &httpStatusError{status: resp.StatusCode, url: u, body: strings.TrimSpace(string(msg))}
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxArtifactBytes))
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
