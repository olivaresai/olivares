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
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/release"
)

// THE DOWNLOAD-TOKEN TRANSPORT CONTRACT (recovered from f32329a38d5): the bearer travels in the
// Authorization header, never the query string (a query bearer lands in logs and referers), and
// only the binary request carries the signed-manifest version.
func TestDownloadGatedTransportIsAuthorizationAndVersionOnlyOnBinary(t *testing.T) {
	type observed struct {
		authorization string
		query         url.Values
	}
	var got []observed
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, observed{authorization: r.Header.Get("Authorization"), query: r.URL.Query()})
		_, _ = w.Write([]byte("object"))
	}))
	t.Cleanup(server.Close)

	o := &upgradeOptions{token: "ota-secret-bearer", endpoint: server.URL, channel: "security", goos: "linux", goarch: "amd64"}
	ctx := context.Background()
	if _, err := downloadGated(ctx, server.Client(), o, "manifest", ""); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if _, err := downloadGated(ctx, server.Client(), o, "manifest.sig", ""); err != nil {
		t.Fatalf("manifest.sig: %v", err)
	}
	if _, err := downloadGated(ctx, server.Client(), o, "", "26.8.0"); err != nil {
		t.Fatalf("binary: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d requests, want 3", len(got))
	}
	for i, req := range got {
		if req.authorization != "Bearer ota-secret-bearer" {
			t.Errorf("request %d Authorization = %q", i, req.authorization)
		}
		if _, leaked := req.query["token"]; leaked {
			t.Errorf("request %d leaked the bearer into the query: %#v", i, req.query)
		}
	}
	for i := 0; i < 2; i++ {
		if _, hasV := got[i].query["version"]; hasV {
			t.Errorf("manifest request %d carried the binary version axis: %#v", i, got[i].query)
		}
	}
	if got[2].query.Get("version") != "26.8.0" {
		t.Fatalf("binary request did not carry the signed version: %#v", got[2].query)
	}
	if _, err := downloadGated(ctx, server.Client(), o, "", ""); err == nil {
		t.Fatal("binary request without a version succeeded")
	}
}

// v1Fixture is a fake worker that speaks the resolved-download protocol: /download/release-v1
// with the protocol marker and the {version,set,manifest,signature} tuple headers.
type v1Fixture struct {
	server   *httptest.Server
	pubB64   string
	priv     ed25519.PrivateKey
	version  string
	set      string
	manifest []byte
	sig      []byte
	mSha     string
	sSha     string
	artifact []byte
	// injectable faults
	manifestStatus int  // non-zero overrides the manifest resolution status (e.g. 409)
	badSig         bool // serve a signature whose bytes are a wrong Ed25519 signature (valid sha header)
	corruptArt     bool // serve artifact bytes that do not match the signed SHA
	dropMarker     bool // answer 200 without the protocol marker
}

func hexSum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func newV1Fixture(t *testing.T, version, set string, bin []byte) *v1Fixture {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	artifact := tarGzBinary(t, bin)
	sum := sha256.Sum256(artifact)
	m := release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion,
		Channel:       release.ChannelStable,
		Version:       version,
		ReleasedAt:    time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC),
		Artifacts: []release.Artifact{
			{OS: "linux", Arch: "amd64", Filename: "olivares_" + version + "_linux_amd64.tar.gz", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(artifact))},
		},
	}
	mb, _ := json.Marshal(m)
	sig := release.SignManifest(mb, priv)
	f := &v1Fixture{
		pubB64: base64.StdEncoding.EncodeToString(pub), priv: priv, version: version, set: set,
		manifest: mb, sig: []byte(base64.StdEncoding.EncodeToString(sig)), artifact: artifact,
	}
	f.mSha = hexSum(f.manifest)
	f.sSha = hexSum(f.sig)

	mux := http.NewServeMux()
	mux.HandleFunc("/download/release-v1", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if _, leaked := r.URL.Query()["token"]; leaked {
			http.Error(w, "token in query", http.StatusBadRequest)
			return
		}
		q := r.URL.Query()
		setMarker := func() {
			if !f.dropMarker {
				w.Header().Set("Olivares-Download-Protocol", "release-v1")
			}
		}
		// The worker names the tuple on EVERY release-v1 success (manifest, signature, artifact),
		// and the client corroborates the later two against the first; the fixture does the same.
		setTuple := func() {
			w.Header().Set("Olivares-Release-Version", f.version)
			w.Header().Set("Olivares-Release-Set", f.set)
			w.Header().Set("Olivares-Manifest-SHA256", f.mSha)
			w.Header().Set("Olivares-Signature-SHA256", f.sSha)
		}
		switch q.Get("kind") {
		case "manifest":
			setMarker()
			setTuple()
			if f.manifestStatus != 0 {
				w.Header().Set("Olivares-Download-Error", "release_token_version_stale")
				w.WriteHeader(f.manifestStatus)
				_, _ = w.Write([]byte(`{"error":"release_token_version_stale"}`))
				return
			}
			_, _ = w.Write(f.manifest)
		case "manifest.sig":
			setMarker()
			setTuple()
			if f.badSig {
				_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(ed25519.Sign(f.priv, []byte("other")))))
				return
			}
			_, _ = w.Write(f.sig)
		case "":
			// artifact: os/arch + tuple required
			if q.Get("expected_set") != f.set || q.Get("manifest_sha256") != f.mSha || q.Get("signature_sha256") != f.sSha {
				http.Error(w, "tuple mismatch", http.StatusConflict)
				return
			}
			setMarker()
			setTuple()
			body := f.artifact
			if f.corruptArt {
				body = append([]byte(nil), f.artifact...)
				body[len(body)/2] ^= 0xFF
			}
			_, _ = w.Write(body)
		default:
			http.Error(w, "unknown kind", http.StatusBadRequest)
		}
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func TestUpgradeReleaseV1E2E(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build stub binaries")
	}
	v1 := buildStub(t, "26.7.0")
	v2 := buildStub(t, "26.8.0")

	run := func(t *testing.T, f *v1Fixture, target string, extra ...string) (string, error) {
		args := append([]string{
			"--enterprise", "--token", "tkn", "--endpoint", f.server.URL, "--pubkey", f.pubB64,
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", t.TempDir(),
		}, extra...)
		installDevLicense(t, argValue(args, "--data-dir"))
		return runUpgradeCmd(t, args...)
	}

	t.Run("release-v1 happy path installs the resolved version", func(t *testing.T) {
		f := newV1Fixture(t, "26.8.0", "biz+reg", v2)
		target := writeTarget(t, v1)
		dataDir := t.TempDir()
		installDevLicense(t, dataDir)
		_, err := runUpgradeCmd(t, "--enterprise", "--token", "tkn", "--endpoint", f.server.URL, "--pubkey", f.pubB64,
			"--data-dir", dataDir, "--target", target, "--os", "linux", "--arch", "amd64", "--yes")
		if err != nil {
			t.Fatalf("release-v1 upgrade: %v", err)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.8.0") {
			t.Fatalf("target not upgraded: %q", got)
		}
	})

	t.Run("a tampered signature leaves the target intact", func(t *testing.T) {
		f := newV1Fixture(t, "26.8.0", "biz", v2)
		f.badSig = true
		target := writeTarget(t, v1)
		_, err := run(t, f, target)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "signature") {
			t.Fatalf("a bad manifest signature must abort, got %v", err)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.7.0") {
			t.Fatalf("target changed despite bad signature: %q", got)
		}
	})

	t.Run("a corrupt artifact leaves the target intact", func(t *testing.T) {
		f := newV1Fixture(t, "26.8.0", "biz", v2)
		f.corruptArt = true
		target := writeTarget(t, v1)
		_, err := run(t, f, target)
		if err == nil || !strings.Contains(err.Error(), "SHA-256") {
			t.Fatalf("a corrupt artifact must abort with a checksum error, got %v", err)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.7.0") {
			t.Fatalf("target changed despite corrupt artifact: %q", got)
		}
	})

	t.Run("a 409 version conflict on resolution leaves the target intact", func(t *testing.T) {
		f := newV1Fixture(t, "26.8.0", "biz", v2)
		f.manifestStatus = http.StatusConflict
		target := writeTarget(t, v1)
		_, err := run(t, f, target)
		if err == nil || !strings.Contains(err.Error(), "release_token_version_stale") {
			t.Fatalf("a 409 conflict must abort, got %v", err)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.7.0") {
			t.Fatalf("target changed despite a resolution conflict: %q", got)
		}
	})

	t.Run("a 200 without the protocol marker is refused, target intact", func(t *testing.T) {
		f := newV1Fixture(t, "26.8.0", "biz", v2)
		f.dropMarker = true
		target := writeTarget(t, v1)
		_, err := run(t, f, target)
		if err == nil || !strings.Contains(err.Error(), "marker") {
			t.Fatalf("a markerless 200 must be refused as not-v1, got %v", err)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.7.0") {
			t.Fatalf("target changed despite a non-v1 gateway: %q", got)
		}
	})

	t.Run("legacy mode against a v1 route reports the compatibility diagnostic", func(t *testing.T) {
		// The v1 fixture serves only /download/release-v1, so a legacy client (which requests
		// /download) gets a 404 that names the way out rather than a silent auto-negotiation.
		f := newV1Fixture(t, "26.8.0", "biz", v2)
		target := writeTarget(t, v1)
		_, err := run(t, f, target, "--download-protocol", "legacy")
		if err == nil {
			t.Fatal("a legacy client against a v1-only route must not succeed silently")
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.7.0") {
			t.Fatalf("target changed: %q", got)
		}
	})
}

// argValue returns the value following flag in args, or "".
func argValue(args []string, flag string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}
