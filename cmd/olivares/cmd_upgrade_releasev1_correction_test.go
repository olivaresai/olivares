// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/release"
)

// THE 2026-09-05 CORRECTION OF THE RELEASE-V1 CLIENT (independent review R1, R5, R7, R8), against
// an OWNED loopback gateway that speaks the protocol the way the worker does: the marker on every
// response, the worker's error NAMES in Olivares-Download-Error, tuple corroboration on the
// signature and artifact requests, and injectable conflicts and header mutations. Dummy bearers
// are not credentials. The end-to-end cases drive the real `upgrade` command over an owned
// executable target; the unit cases drive the real gatedSource.

// v1Release is one signed publication the gateway can serve.
type v1Release struct {
	version          string
	manifest         []byte
	sig              []byte // base64 std of the detached, domain-separated Ed25519 signature
	artifact         []byte
	mSha, sSha, aSha string
}

func newV1Release(t *testing.T, priv ed25519.PrivateKey, channel, version, notes string, artifact []byte) *v1Release {
	t.Helper()
	sum := sha256.Sum256(artifact)
	m := release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion,
		Channel:       channel,
		Version:       version,
		ReleasedAt:    time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC),
		Notes:         notes,
		Artifacts: []release.Artifact{{
			OS: "linux", Arch: "amd64", Filename: "olivares_" + version + "_linux_amd64.tar.gz",
			SHA256: hex.EncodeToString(sum[:]), Size: int64(len(artifact)),
		}},
	}
	mb, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	sig := []byte(base64.StdEncoding.EncodeToString(release.SignManifest(mb, priv)))
	return &v1Release{version: version, manifest: mb, sig: sig, artifact: artifact, mSha: hexSum(mb), sSha: hexSum(sig), aSha: hexSum(artifact)}
}

// v1Gateway is the owned release-v1 fixture. Every field under `mu` is read by the handler.
type v1Gateway struct {
	t      *testing.T
	server *httptest.Server
	pubB64 string
	priv   ed25519.PrivateKey
	set    string

	mu     sync.Mutex
	rel    *v1Release
	counts map[string]int
	// Faults: the first N signature/artifact requests answer 409 with conflictCode, calling
	// onConflict (under the lock) so a case can move the publication at that exact moment.
	sigConflicts, artConflicts int
	conflictCode               string
	onConflict                 func()
	// Header mutations applied to a SUCCESS response of each step.
	mutateResolution, mutateSig, mutateArtifact func(http.Header)
}

func newV1Gateway(t *testing.T, set string) *v1Gateway {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	g := &v1Gateway{t: t, set: set, priv: priv, pubB64: base64.StdEncoding.EncodeToString(pub), counts: map[string]int{}, conflictCode: v1ErrMetadataChanged}
	mux := http.NewServeMux()
	mux.HandleFunc(releaseV1Path, g.handle)
	g.server = httptest.NewServer(mux)
	t.Cleanup(g.server.Close)
	return g
}

func (g *v1Gateway) release(version, notes string, artifact []byte) *v1Release {
	return newV1Release(g.t, g.priv, release.ChannelStable, version, notes, artifact)
}

func (g *v1Gateway) count(step string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.counts[step]
}

func (g *v1Gateway) handle(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	h := w.Header()
	h.Set(hdrDownloadProtocol, releaseV1Marker)
	refuse := func(status int, code string) {
		h.Set(hdrDownloadError, code)
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"error":%q}`, code)
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		refuse(http.StatusForbidden, "token_missing")
		return
	}
	q := r.URL.Query()
	if _, leaked := q["token"]; leaked {
		refuse(http.StatusBadRequest, "protocol_token_presented_twice")
		return
	}
	rel := g.rel
	setTuple := func() {
		h.Set(hdrReleaseVersion, rel.version)
		h.Set(hdrReleaseSet, g.set)
		h.Set(hdrManifestSHA256, rel.mSha)
		h.Set(hdrSignatureSHA256, rel.sSha)
	}
	tupleOK := func() bool {
		return q.Get("expected_set") == g.set && q.Get("manifest_sha256") == rel.mSha && q.Get("signature_sha256") == rel.sSha
	}
	conflict := func(budget *int) bool {
		if *budget <= 0 {
			return false
		}
		*budget--
		code := g.conflictCode
		if g.onConflict != nil {
			g.onConflict()
		}
		refuse(http.StatusConflict, code)
		return true
	}
	switch q.Get("kind") {
	case "manifest":
		g.counts["manifest"]++
		setTuple()
		if g.mutateResolution != nil {
			g.mutateResolution(h)
		}
		_, _ = w.Write(rel.manifest)
	case "manifest.sig":
		g.counts["manifest.sig"]++
		if conflict(&g.sigConflicts) {
			return
		}
		if !tupleOK() {
			refuse(http.StatusConflict, v1ErrMetadataChanged)
			return
		}
		setTuple()
		if g.mutateSig != nil {
			g.mutateSig(h)
		}
		_, _ = w.Write(rel.sig)
	case "":
		g.counts["artifact"]++
		if conflict(&g.artConflicts) {
			return
		}
		if !tupleOK() {
			refuse(http.StatusConflict, v1ErrMetadataChanged)
			return
		}
		setTuple()
		h.Set(hdrArtifactSHA256, rel.aSha)
		if g.mutateArtifact != nil {
			g.mutateArtifact(h)
		}
		_, _ = w.Write(rel.artifact)
	default:
		refuse(http.StatusBadRequest, "protocol_invalid_kind")
	}
}

// runV1Upgrade drives the real command against the gateway over an owned target.
func runV1Upgrade(t *testing.T, g *v1Gateway, target string, extra ...string) (string, error) {
	t.Helper()
	_, out, err := runV1UpgradeData(t, g, target, extra...)
	return out, err
}

func runV1UpgradeData(t *testing.T, g *v1Gateway, target string, extra ...string) (dataDir, out string, err error) {
	t.Helper()
	dataDir = t.TempDir()
	installDevLicense(t, dataDir)
	args := []string{
		"--enterprise", "--token", "owned-dummy-bearer", "--endpoint", g.server.URL, "--pubkey", g.pubB64,
		"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", dataDir,
	}
	out, err = runUpgradeCmd(t, append(args, extra...)...)
	return dataDir, out, err
}

// replaceSignedManifest re-signs the current publication after mutate, keeping the
// same artifact bytes. onConflict runs under the gateway lock.
func (g *v1Gateway) replaceSignedManifest(mutate func(*release.Manifest)) {
	g.t.Helper()
	if g.rel == nil {
		g.t.Fatal("replaceSignedManifest: no publication")
	}
	var m release.Manifest
	if err := json.Unmarshal(g.rel.manifest, &m); err != nil {
		g.t.Fatal(err)
	}
	mutate(&m)
	mb, err := json.Marshal(m)
	if err != nil {
		g.t.Fatal(err)
	}
	g.rel.manifest = mb
	g.rel.sig = []byte(base64.StdEncoding.EncodeToString(release.SignManifest(mb, g.priv)))
	g.rel.mSha = hexSum(mb)
	g.rel.sSha = hexSum(g.rel.sig)
}

func unitSource(g *v1Gateway) *gatedSource {
	return &gatedSource{
		o:      &upgradeOptions{endpoint: g.server.URL, token: "owned-dummy-bearer", channel: "stable", goos: "linux", goarch: "amd64"},
		client: g.server.Client(),
	}
}

func requireStubs(t *testing.T) ([]byte, []byte) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build stub binaries")
	}
	return buildStub(t, "26.7.0"), buildStub(t, "26.8.0")
}

func assertTargetRuns(t *testing.T, target, version string) {
	t.Helper()
	if got := runsVersion(t, target); !strings.Contains(got, version) {
		t.Fatalf("target reports %q, want %s", got, version)
	}
}

// ---------------------------------------------------------------------------------------------
// R1 — a redirect is refused BEFORE any redirected (authenticated) request is sent.
// ---------------------------------------------------------------------------------------------
func TestGatedRequestsRefuseRedirectsBeforeForwardingTheBearer(t *testing.T) {
	var mu sync.Mutex
	received := 0
	receivedAuth := ""
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		received++
		receivedAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set(hdrDownloadProtocol, releaseV1Marker)
		_, _ = w.Write([]byte("owned-response"))
	}))
	t.Cleanup(receiver.Close)
	// An HTTPS gateway redirecting to a DIFFERENT origin over plain HTTP: the reviewer's
	// reproduction, where Go copied Authorization because the host (127.0.0.1) matched.
	gateway := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, receiver.URL+"/redirected", http.StatusFound)
	}))
	t.Cleanup(gateway.Close)
	o := &upgradeOptions{endpoint: gateway.URL, token: "owned-review-dummy-bearer", channel: "stable", goos: "linux", goarch: "amd64"}
	ctx := context.Background()

	// A default client with NO policy of its own: the protection must come from the gated path.
	plain := gateway.Client()
	plain.CheckRedirect = nil

	_, _, err := downloadReleaseV1(ctx, plain, o, "manifest", nil)
	if err == nil || !strings.Contains(err.Error(), "refusing to follow a redirect") {
		t.Fatalf("release-v1: want a redirect refusal, got %v", err)
	}
	if strings.Contains(err.Error(), "owned-review-dummy-bearer") {
		t.Fatalf("the refusal echoed the bearer: %v", err)
	}
	// The legacy route carries the same bearer and gets the same policy.
	_, err = downloadGated(ctx, plain, o, "manifest", "")
	if err == nil || !strings.Contains(err.Error(), "refusing to follow a redirect") {
		t.Fatalf("legacy: want a redirect refusal, got %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if received != 0 || receivedAuth != "" {
		t.Fatalf("a redirected request was sent (%d) with Authorization present=%t", received, receivedAuth != "")
	}

	// A SAME-origin redirect is refused too: the worker answers its gated routes directly, so any
	// redirect is a misrouted gateway, and there is no legitimate second authenticated request.
	sameCount := 0
	var same *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc(releaseV1Path, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/elsewhere", func(w http.ResponseWriter, _ *http.Request) { sameCount++; _, _ = w.Write([]byte("x")) })
	same = httptest.NewServer(mux)
	t.Cleanup(same.Close)
	o2 := &upgradeOptions{endpoint: same.URL, token: "owned-dummy", channel: "stable"}
	if _, _, err := downloadReleaseV1(ctx, same.Client(), o2, "manifest", nil); err == nil || !strings.Contains(err.Error(), "refusing to follow a redirect") {
		t.Fatalf("same-origin: want a redirect refusal, got %v", err)
	}
	if sameCount != 0 {
		t.Fatalf("a same-origin redirected request was sent (%d)", sameCount)
	}
}

// ---------------------------------------------------------------------------------------------
// R5 — the tuple is validated field by field and corroborated against the signed release.
// ---------------------------------------------------------------------------------------------
func TestReleaseV1TupleIsValidatedAndCorroborated(t *testing.T) {
	ctx := context.Background()

	t.Run("a declared version that disagrees with the signed manifest is refused before any decision", func(t *testing.T) {
		old, desired := requireStubs(t)
		g := newV1Gateway(t, "biz")
		g.rel = g.release("26.8.0", "", tarGzBinary(t, desired))
		// A gateway that is INTERNALLY coherent about its lie: every step declares 26.9.0, so the
		// only thing that can catch it is the comparison against the version the signature covers.
		lie := func(h http.Header) { h.Set(hdrReleaseVersion, "26.9.0") }
		g.mutateResolution, g.mutateSig, g.mutateArtifact = lie, lie, lie
		target := writeTarget(t, old)
		_, err := runV1Upgrade(t, g, target)
		if err == nil || !strings.Contains(err.Error(), "declared version \"26.9.0\"") || !strings.Contains(err.Error(), "26.8.0") {
			t.Fatalf("want a version corroboration refusal, got %v", err)
		}
		assertTargetRuns(t, target, "26.7.0")
		if g.count("artifact") != 0 {
			t.Fatalf("an artifact was requested despite the incoherent tuple")
		}
	})

	t.Run("a resolution with a missing or malformed field is refused and no signature is requested", func(t *testing.T) {
		cases := map[string]func(http.Header){
			"missing set":         func(h http.Header) { h.Del(hdrReleaseSet) },
			"missing digest":      func(h http.Header) { h.Del(hdrSignatureSHA256) },
			"missing version":     func(h http.Header) { h.Del(hdrReleaseVersion) },
			"unparseable version": func(h http.Header) { h.Set(hdrReleaseVersion, "latest") },
			"malformed set":       func(h http.Header) { h.Set(hdrReleaseSet, "biz;rm") },
			"uppercase digest":    func(h http.Header) { h.Set(hdrManifestSHA256, strings.ToUpper(g0Digest)) },
			"short digest":        func(h http.Header) { h.Set(hdrManifestSHA256, "abc") },
		}
		for name, mutate := range cases {
			g := newV1Gateway(t, "biz")
			g.rel = g.release("26.8.0", "", []byte("unused"))
			g.mutateResolution = mutate
			_, _, err := unitSource(g).fetchManifest(ctx)
			if err == nil || !strings.Contains(err.Error(), "release-v1 resolution") {
				t.Fatalf("%s: want a tuple validation refusal, got %v", name, err)
			}
			if g.count("manifest.sig") != 0 {
				t.Fatalf("%s: a signature was requested after an invalid resolution", name)
			}
		}
	})

	t.Run("a version with surrounding whitespace is refused by the parser itself", func(t *testing.T) {
		// HTTP transports trim header values, so this shape cannot arrive over the wire; the
		// parser still refuses it, because the exact-claim rule is the parser's, not the wire's.
		h := http.Header{}
		h.Set(hdrReleaseVersion, "26.8.0")
		h.Set(hdrReleaseSet, "biz")
		h.Set(hdrManifestSHA256, g0Digest)
		h.Set(hdrSignatureSHA256, g0Digest)
		if _, err := parseV1Tuple(h); err != nil {
			t.Fatalf("control: %v", err)
		}
		h[hdrReleaseVersion] = []string{" 26.8.0"}
		if _, err := parseV1Tuple(h); err == nil || !strings.Contains(err.Error(), "whitespace") {
			t.Fatalf("want a whitespace refusal, got %v", err)
		}
	})

	t.Run("a signature response naming another tuple is refused", func(t *testing.T) {
		g := newV1Gateway(t, "biz")
		g.rel = g.release("26.8.0", "", []byte("unused"))
		g.mutateSig = func(h http.Header) { h.Set(hdrReleaseSet, "ent") }
		_, _, err := unitSource(g).fetchManifest(ctx)
		if err == nil || !strings.Contains(err.Error(), "names another tuple") {
			t.Fatalf("want a corroboration refusal, got %v", err)
		}
	})

	t.Run("an artifact response naming another version or digest leaves the target intact", func(t *testing.T) {
		old, desired := requireStubs(t)
		for name, mutate := range map[string]func(http.Header){
			"other version":         func(h http.Header) { h.Set(hdrReleaseVersion, "26.9.0") },
			"other artifact digest": func(h http.Header) { h.Set(hdrArtifactSHA256, strings.Repeat("0", 64)) },
		} {
			g := newV1Gateway(t, "biz")
			g.rel = g.release("26.8.0", "", tarGzBinary(t, desired))
			g.mutateArtifact = mutate
			target := writeTarget(t, old)
			_, err := runV1Upgrade(t, g, target)
			if err == nil || !strings.Contains(err.Error(), "release-v1 artifact response") {
				t.Fatalf("%s: want an artifact corroboration refusal, got %v", name, err)
			}
			assertTargetRuns(t, target, "26.7.0")
		}
	})

	t.Run("a coherent tuple still installs (control)", func(t *testing.T) {
		old, desired := requireStubs(t)
		g := newV1Gateway(t, "biz+reg")
		g.rel = g.release("26.8.0", "", tarGzBinary(t, desired))
		target := writeTarget(t, old)
		if _, err := runV1Upgrade(t, g, target); err != nil {
			t.Fatalf("control: %v", err)
		}
		assertTargetRuns(t, target, "26.8.0")
		if g.count("manifest") != 1 || g.count("manifest.sig") != 1 || g.count("artifact") != 1 {
			t.Fatalf("control made %d/%d/%d requests, want 1/1/1", g.count("manifest"), g.count("manifest.sig"), g.count("artifact"))
		}
	})
}

// g0Digest is a well-formed lowercase digest used as the base of a malformed (uppercase) one.
const g0Digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// ---------------------------------------------------------------------------------------------
// R8 — ONE automatic fresh resolution for a same-version conflict; version conflicts never retry.
// ---------------------------------------------------------------------------------------------
func TestReleaseV1BoundedReresolution(t *testing.T) {
	ctx := context.Background()

	t.Run("one same-version conflict on the signature restarts the whole resolution once, then installs", func(t *testing.T) {
		old, desired := requireStubs(t)
		g := newV1Gateway(t, "biz")
		g.rel = g.release("26.8.0", "", tarGzBinary(t, desired))
		g.sigConflicts = 1
		target := writeTarget(t, old)
		if _, err := runV1Upgrade(t, g, target); err != nil {
			t.Fatalf("expected the bounded re-resolution to recover: %v", err)
		}
		assertTargetRuns(t, target, "26.8.0")
		if g.count("manifest") != 2 || g.count("manifest.sig") != 2 || g.count("artifact") != 1 {
			t.Fatalf("requests manifest=%d sig=%d artifact=%d, want 2/2/1 (a full restart, once)", g.count("manifest"), g.count("manifest.sig"), g.count("artifact"))
		}
	})

	t.Run("a repeated conflict terminates without a third resolution", func(t *testing.T) {
		g := newV1Gateway(t, "biz")
		g.rel = g.release("26.8.0", "", []byte("unused"))
		g.sigConflicts = 2
		_, _, err := unitSource(g).fetchManifest(ctx)
		if err == nil || !strings.Contains(err.Error(), "changed AGAIN") || !strings.Contains(err.Error(), v1ErrMetadataChanged) {
			t.Fatalf("want a terminal repeated-change error, got %v", err)
		}
		if g.count("manifest") != 2 || g.count("manifest.sig") != 2 {
			t.Fatalf("requests manifest=%d sig=%d, want exactly 2/2", g.count("manifest"), g.count("manifest.sig"))
		}
	})

	t.Run("a version conflict is never retried", func(t *testing.T) {
		g := newV1Gateway(t, "biz")
		g.rel = g.release("26.8.0", "", []byte("unused"))
		g.sigConflicts = 1
		g.conflictCode = v1ErrTokenVersionStale
		_, _, err := unitSource(g).fetchManifest(ctx)
		if err == nil || !strings.Contains(err.Error(), v1ErrTokenVersionStale) || strings.Contains(err.Error(), "AGAIN") {
			t.Fatalf("want the version conflict propagated as-is, got %v", err)
		}
		if g.count("manifest") != 1 || g.count("manifest.sig") != 1 {
			t.Fatalf("a version conflict was retried: manifest=%d sig=%d", g.count("manifest"), g.count("manifest.sig"))
		}
	})

	t.Run("a set conflict on the artifact re-resolves once and installs when the release is unchanged", func(t *testing.T) {
		old, desired := requireStubs(t)
		g := newV1Gateway(t, "biz")
		g.rel = g.release("26.8.0", "", tarGzBinary(t, desired))
		g.artConflicts = 1
		g.conflictCode = v1ErrSetChanged
		target := writeTarget(t, old)
		if _, err := runV1Upgrade(t, g, target); err != nil {
			t.Fatalf("expected recovery through one fresh resolution: %v", err)
		}
		assertTargetRuns(t, target, "26.8.0")
		if g.count("manifest") != 2 || g.count("manifest.sig") != 2 || g.count("artifact") != 2 {
			t.Fatalf("requests manifest=%d sig=%d artifact=%d, want 2/2/2", g.count("manifest"), g.count("manifest.sig"), g.count("artifact"))
		}
	})

	t.Run("a same-version metadata change that keeps the artifact descriptor is re-resolved and installed", func(t *testing.T) {
		old, desired := requireStubs(t)
		g := newV1Gateway(t, "biz")
		art := tarGzBinary(t, desired)
		g.rel = g.release("26.8.0", "first notes", art)
		g.artConflicts = 1
		g.onConflict = func() { g.rel = g.release("26.8.0", "notes changed, same binary", art) }
		target := writeTarget(t, old)
		if _, err := runV1Upgrade(t, g, target); err != nil {
			t.Fatalf("expected recovery: %v", err)
		}
		assertTargetRuns(t, target, "26.8.0")
		if g.count("artifact") != 2 || g.count("manifest") != 2 {
			t.Fatalf("requests manifest=%d artifact=%d, want 2/2", g.count("manifest"), g.count("artifact"))
		}
	})

	t.Run("a same-version change that alters the artifact descriptor terminates, target intact", func(t *testing.T) {
		old, desired := requireStubs(t)
		g := newV1Gateway(t, "biz")
		g.rel = g.release("26.8.0", "", tarGzBinary(t, desired))
		g.artConflicts = 1
		g.onConflict = func() { g.rel = g.release("26.8.0", "", append(tarGzBinary(t, desired), 0)) }
		target := writeTarget(t, old)
		_, err := runV1Upgrade(t, g, target)
		if err == nil || !strings.Contains(err.Error(), "artifact descriptor") {
			t.Fatalf("want a descriptor-changed refusal, got %v", err)
		}
		assertTargetRuns(t, target, "26.7.0")
		if g.count("artifact") != 1 {
			t.Fatalf("a potentially different artifact was requested (%d artifact requests)", g.count("artifact"))
		}
	})

	t.Run("a fresh resolution that publishes another version is a new authorization, not a retry", func(t *testing.T) {
		old, desired := requireStubs(t)
		g := newV1Gateway(t, "biz")
		g.rel = g.release("26.8.0", "", tarGzBinary(t, desired))
		g.artConflicts = 1
		g.onConflict = func() { g.rel = g.release("26.9.0", "", tarGzBinary(t, desired)) }
		target := writeTarget(t, old)
		_, err := runV1Upgrade(t, g, target)
		if err == nil || !strings.Contains(err.Error(), "new authorization") {
			t.Fatalf("want a new-version refusal, got %v", err)
		}
		assertTargetRuns(t, target, "26.7.0")
		if g.count("artifact") != 1 {
			t.Fatalf("bytes of another version were requested (%d artifact requests)", g.count("artifact"))
		}
	})

	t.Run("a conflict re-resolved once on the signature is not re-resolved again on the artifact", func(t *testing.T) {
		g := newV1Gateway(t, "biz")
		g.rel = g.release("26.8.0", "", []byte("unused"))
		g.sigConflicts = 1
		g.artConflicts = 1
		src := unitSource(g)
		mb, sig, err := src.fetchManifest(ctx)
		if err != nil {
			t.Fatalf("manifest stage should recover once: %v", err)
		}
		m, err := release.ParseManifest(mb)
		if err != nil {
			t.Fatal(err)
		}
		_ = sig
		_, err = src.fetchArtifact(ctx, m, m.Artifacts[0])
		if err == nil || !strings.Contains(err.Error(), "changed AGAIN") {
			t.Fatalf("the run's single re-resolution was spent on the signature; the artifact conflict must terminate, got %v", err)
		}
		if g.count("manifest") != 2 {
			t.Fatalf("a third resolution was made (manifest=%d)", g.count("manifest"))
		}
	})
}

// ---------------------------------------------------------------------------------------------
// R7 — the generated timer preserves the selected protocol; selectors are validated first.
// ---------------------------------------------------------------------------------------------
func TestUpgradeTimerPreservesTheSelectedDownloadProtocol(t *testing.T) {
	execStartOf := func(t *testing.T, out string) string {
		t.Helper()
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "ExecStart=") {
				return line
			}
		}
		t.Fatalf("no ExecStart in:\n%s", out)
		return ""
	}

	t.Run("an explicit legacy selection is pinned into the scheduled command", func(t *testing.T) {
		out, err := runUpgradeCmd(t, "--install-timer", "--enterprise", "--download-protocol", "legacy", "--endpoint", "https://owned.invalid", "--target", "/owned/olivares", "--data-dir", t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		cmd := execStartOf(t, out)
		if !strings.Contains(cmd, "--download-protocol legacy") {
			t.Fatalf("the selected protocol was dropped: %s", cmd)
		}
		if strings.Contains(cmd, "--token") {
			t.Fatalf("a token argument was generated: %s", cmd)
		}
		if !strings.Contains(out, "EnvironmentFile=") {
			t.Fatalf("the bearer must come from an EnvironmentFile:\n%s", out)
		}
	})

	t.Run("the default is pinned explicitly as release-v1", func(t *testing.T) {
		out, err := runUpgradeCmd(t, "--install-timer", "--enterprise", "--target", "/owned/olivares", "--data-dir", t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if cmd := execStartOf(t, out); !strings.Contains(cmd, "--download-protocol release-v1") {
			t.Fatalf("the default protocol is not pinned: %s", cmd)
		}
	})

	t.Run("the JSON document carries the protocol", func(t *testing.T) {
		// `-o` is the root command's flag, so the JSON pane is reached through the root.
		out, _, err := execRoot(t, "upgrade", "--install-timer", "--enterprise", "--download-protocol", "legacy", "--target", "/owned/olivares", "--data-dir", t.TempDir(), "-o", "json")
		if err != nil {
			t.Fatal(err)
		}
		var doc upgradeTimerResult
		if jerr := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &doc); jerr != nil {
			t.Fatalf("document: %v\n%s", jerr, out)
		}
		if doc.DownloadProtocol != "legacy" || !strings.Contains(doc.ExecStart, "--download-protocol legacy") {
			t.Fatalf("document does not carry the selected protocol: %+v", doc)
		}
	})

	t.Run("a community unit carries no protocol, where it has no meaning", func(t *testing.T) {
		out, err := runUpgradeCmd(t, "--install-timer", "--target", "/owned/olivares", "--data-dir", t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if cmd := execStartOf(t, out); strings.Contains(cmd, "--download-protocol") || strings.Contains(cmd, "--enterprise") {
			t.Fatalf("community unit carries enterprise arguments: %s", cmd)
		}
	})

	t.Run("an invalid protocol or channel is refused before any unit is written", func(t *testing.T) {
		for _, args := range [][]string{
			{"--install-timer", "--enterprise", "--download-protocol", "release-v2"},
			{"--install-timer", "--channel", "nightly"},
		} {
			dir := filepath.Join(t.TempDir(), "units")
			_, err := runUpgradeCmd(t, append(args, "--timer-dir", dir, "--target", "/owned/olivares", "--data-dir", t.TempDir())...)
			if err == nil || !strings.Contains(err.Error(), "unknown --") {
				t.Fatalf("%v: want a validation refusal, got %v", args, err)
			}
			if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
				t.Fatalf("%v: units were written for an invalid selection", args)
			}
		}
	})
}

// Independent discriminator: a real newly signed minimum-version policy on the same bytes.
func TestRootSameVersionReresolutionPreservesMinimumVersionPolicy(t *testing.T) {
	old := buildStub(t, "26.7.0")
	desired := buildStub(t, "26.8.0")
	g := newV1Gateway(t, "biz")
	g.rel = g.release("26.8.0", "initial", desired)
	g.artConflicts = 1
	g.onConflict = func() {
		g.replaceSignedManifest(func(m *release.Manifest) {
			m.Notes = "new minimum"
			m.MinVersion = "26.7.9"
		})
	}
	target := writeTarget(t, old)
	_, err := runV1Upgrade(t, g, target)
	if err == nil {
		t.Errorf("new signed min_version26.7.9 must refuse installed26.7.0; actual command installed %q", runsVersion(t, target))
	}
	if got := runsVersion(t, target); !strings.Contains(got, "26.7.0") {
		t.Errorf("target changed despite fresh signed minimum-version policy: %q", got)
	}
	if err != nil && !strings.Contains(err.Error(), "minimum current version") {
		t.Errorf("want the productive min_version refusal, got %v", err)
	}
}

// R9 — an artifact-step same-version retry must carry the freshly verified manifest
// through the same policy/plan authority. A whitelist of min_version alone would
// miss rollout, CRL, notes and the next Manifest field.
func TestReleaseV1ReresolutionAppliesFreshSignedPolicy(t *testing.T) {
	ctx := context.Background()

	t.Run("the artifact-step retry surfaces the newly verified manifest instead of installing against the old plan", func(t *testing.T) {
		g := newV1Gateway(t, "biz")
		g.rel = g.release("26.8.0", "initial", []byte("owned-artifact"))
		g.artConflicts = 1
		g.onConflict = func() {
			g.replaceSignedManifest(func(m *release.Manifest) { m.MinVersion = "26.7.9" })
		}
		pub, err := base64.StdEncoding.DecodeString(g.pubB64)
		if err != nil {
			t.Fatal(err)
		}
		src := unitSource(g)
		src.pub = ed25519.PublicKey(pub)
		mb, _, err := src.fetchManifest(ctx)
		if err != nil {
			t.Fatal(err)
		}
		m, err := release.ParseManifest(mb)
		if err != nil {
			t.Fatal(err)
		}
		_, err = src.fetchArtifact(ctx, m, m.Artifacts[0])
		if !errors.Is(err, errFreshVerifiedManifest) {
			t.Fatalf("want errFreshVerifiedManifest so the command re-plans, got %v", err)
		}
		fresh, ok := src.takeRefreshedVerifiedManifest()
		if !ok {
			t.Fatal("the retry stored no verified manifest")
		}
		if fresh.MinVersion != "26.7.9" {
			t.Fatalf("stored manifest min_version = %q, want 26.7.9", fresh.MinVersion)
		}
		if g.count("artifact") != 1 {
			t.Fatalf("bytes were fetched before the caller re-applied policy (artifact=%d)", g.count("artifact"))
		}
	})

	t.Run("eligibility that changes after planning is skipped with --if-eligible, target intact", func(t *testing.T) {
		old, desired := requireStubs(t)
		g := newV1Gateway(t, "biz")
		art := tarGzBinary(t, desired)
		g.rel = g.release("26.8.0", "in cohort", art)
		g.artConflicts = 1
		g.onConflict = func() {
			g.replaceSignedManifest(func(m *release.Manifest) {
				zero := 0
				m.Notes = "rollout paused"
				m.Rollout.Percentage = &zero
			})
		}
		target := writeTarget(t, old)
		out, err := runV1Upgrade(t, g, target, "--if-eligible")
		if err != nil {
			t.Fatalf("a newly ineligible cohort is a skip, not an install error: %v", err)
		}
		if !strings.Contains(out, "not in the staged-rollout cohort") {
			t.Fatalf("want the productive --if-eligible skip, got:\n%s", out)
		}
		assertTargetRuns(t, target, "26.7.0")
		if g.count("artifact") != 1 {
			t.Fatalf("ineligible policy still fetched artifact bytes after re-planning (artifact=%d)", g.count("artifact"))
		}
	})

	t.Run("the same paused rollout without --if-eligible still installs (gate not widened)", func(t *testing.T) {
		old, desired := requireStubs(t)
		g := newV1Gateway(t, "biz")
		art := tarGzBinary(t, desired)
		g.rel = g.release("26.8.0", "in cohort", art)
		g.artConflicts = 1
		g.onConflict = func() {
			g.replaceSignedManifest(func(m *release.Manifest) {
				zero := 0
				m.Rollout.Percentage = &zero
			})
		}
		target := writeTarget(t, old)
		if _, err := runV1Upgrade(t, g, target); err != nil {
			t.Fatalf("without --if-eligible, paused rollout is not a new refusal: %v", err)
		}
		assertTargetRuns(t, target, "26.8.0")
	})

	t.Run("a newly signed CRL and notes are observed from the fresh manifest, then the unchanged artifact installs", func(t *testing.T) {
		old, desired := requireStubs(t)
		g := newV1Gateway(t, "biz")
		art := tarGzBinary(t, desired)
		g.rel = g.release("26.8.0", "initial notes", art)
		g.artConflicts = 1
		g.onConflict = func() {
			g.replaceSignedManifest(func(m *release.Manifest) {
				m.Notes = "crl republish"
				m.Revoked = &release.RevokedSet{Serials: []string{"r9-retry-serial"}}
			})
		}
		target := writeTarget(t, old)
		dataDir, out, err := runV1UpgradeData(t, g, target)
		if err != nil {
			t.Fatalf("CRL/notes on the same bytes must still install: %v", err)
		}
		assertTargetRuns(t, target, "26.8.0")
		if !strings.Contains(out, "crl republish") {
			t.Fatalf("the result must report the freshly signed notes, got:\n%s", out)
		}
		if !strings.Contains(out, "recorded the channel license CRL") {
			t.Fatalf("the freshly signed CRL must be recorded, got:\n%s", out)
		}
		raw, rerr := os.ReadFile(crlFilePath(dataDir))
		if rerr != nil {
			t.Fatalf("CRL store: %v", rerr)
		}
		var obs crlObservations
		if jerr := json.Unmarshal(raw, &obs); jerr != nil {
			t.Fatalf("CRL JSON: %v", jerr)
		}
		if len(obs.Serials) != 1 || obs.Serials[0] != "r9-retry-serial" {
			t.Fatalf("CRL serials = %#v, want [r9-retry-serial]", obs.Serials)
		}
	})

	t.Run("unchanged-policy conflict recovery still installs", func(t *testing.T) {
		old, desired := requireStubs(t)
		g := newV1Gateway(t, "biz")
		g.rel = g.release("26.8.0", "stable policy", tarGzBinary(t, desired))
		g.artConflicts = 1
		target := writeTarget(t, old)
		if _, err := runV1Upgrade(t, g, target); err != nil {
			t.Fatalf("expected recovery through one fresh resolution: %v", err)
		}
		assertTargetRuns(t, target, "26.8.0")
		if g.count("manifest") != 2 || g.count("manifest.sig") != 2 || g.count("artifact") != 2 {
			t.Fatalf("requests manifest=%d sig=%d artifact=%d, want 2/2/2", g.count("manifest"), g.count("manifest.sig"), g.count("artifact"))
		}
	})
}

// TestReleaseV1ReresolutionRequiresCompleteArtifactDescriptor pins the retry to the
// whole ParseManifest Artifact, not Filename/SHA256. Size, Cosign and Variant are
// signed descriptor fields; changing any of them with the same bytes is a new
// publication, not a metadata-only recovery.
func TestReleaseV1ReresolutionRequiresCompleteArtifactDescriptor(t *testing.T) {
	t.Run("unchanged complete descriptor installs", func(t *testing.T) {
		old, desired := requireStubs(t)
		g := newV1Gateway(t, "biz")
		g.rel = g.release("26.8.0", "unchanged", tarGzBinary(t, desired))
		g.artConflicts = 1
		target := writeTarget(t, old)
		if _, err := runV1Upgrade(t, g, target); err != nil {
			t.Fatalf("unchanged descriptor control must recover: %v", err)
		}
		assertTargetRuns(t, target, "26.8.0")
		if g.count("manifest") != 2 || g.count("manifest.sig") != 2 || g.count("artifact") != 2 {
			t.Fatalf("control requests manifest=%d signature=%d artifact=%d, want 2/2/2", g.count("manifest"), g.count("manifest.sig"), g.count("artifact"))
		}
	})

	t.Run("ParseManifest-normalized SHA256 case is not a descriptor change", func(t *testing.T) {
		old, desired := requireStubs(t)
		g := newV1Gateway(t, "biz")
		art := tarGzBinary(t, desired)
		g.rel = g.release("26.8.0", "lowercase digest", art)
		g.artConflicts = 1
		g.onConflict = func() {
			g.replaceSignedManifest(func(m *release.Manifest) {
				m.Artifacts[0].SHA256 = strings.ToUpper(m.Artifacts[0].SHA256)
			})
		}
		target := writeTarget(t, old)
		if _, err := runV1Upgrade(t, g, target); err != nil {
			t.Fatalf("SHA256 case is normalized by ParseManifest, want install, got %v", err)
		}
		assertTargetRuns(t, target, "26.8.0")
	})

	for _, tc := range []struct {
		name   string
		mutate func(*release.Artifact)
	}{
		{
			name: "signed size changed",
			mutate: func(a *release.Artifact) {
				a.Size++
			},
		},
		{
			name: "signed cosign reference changed",
			mutate: func(a *release.Artifact) {
				a.Cosign = "olivares_26.8.0_linux_amd64.tar.gz.sigstore.json"
			},
		},
		{
			name: "signed variant changed",
			mutate: func(a *release.Artifact) {
				// "base" is the valid subset specimen in core/release (manifest_test.go).
				a.Variant = "base"
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old, desired := requireStubs(t)
			g := newV1Gateway(t, "biz")
			g.rel = g.release("26.8.0", "initial", tarGzBinary(t, desired))
			g.artConflicts = 1
			g.onConflict = func() {
				g.replaceSignedManifest(func(m *release.Manifest) { tc.mutate(&m.Artifacts[0]) })
			}
			target := writeTarget(t, old)
			_, err := runV1Upgrade(t, g, target)
			if err == nil || !strings.Contains(err.Error(), "artifact descriptor") {
				t.Fatalf("want a complete-descriptor refusal after %s, got %v", tc.name, err)
			}
			assertTargetRuns(t, target, "26.7.0")
			if g.count("artifact") != 1 {
				t.Fatalf("%s fetched a second artifact (%d)", tc.name, g.count("artifact"))
			}
		})
	}
}
