// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/release"
)

// cmd_upgrade_release_channel_test.go is the wire proof for the FIRMA B community
// carrier: the update channel served from the PUBLIC REPOSITORY'S GITHUB RELEASES
// (2026-08-21, an internal design note (not shipped):314-326).
//
// It drives the REAL command BY ARGV against a server that speaks the release-asset
// layout — flat assets under /releases/latest/download/ and /releases/download/<tag>/ —
// and asserts the three things that layout can get wrong and the old static transport
// cannot even express:
//
//  1. the manifest is read as `<channel>-manifest.json` (the producer's asset name),
//     not as `<channel>/manifest.json` (the pre-FIRMA-B directory layout);
//  2. the ARTIFACT is fetched from the tag the SIGNED manifest names, never from a
//     second read of the mutable `latest` pointer;
//  3. an endpoint that names github.com but is neither a repository nor a releases base
//     is REFUSED, instead of being silently treated as a static host.
//
// NON-FIRING DIRECTION. Every assertion below is paired with a case that must NOT fire:
// the static layout still resolves through communitySource (the R2/mirror fallback of
// the same signature), and the release layout must NOT be reachable from a plain base
// URL. A control that accepted both would prove nothing about which one was used.

// ghFixture serves the GitHub release-asset layout and RECORDS every path it is asked
// for, which is what lets the assertions be about the wire and not about the outcome.
type ghFixture struct {
	server   *httptest.Server
	pubB64   string
	version  string
	artName  string
	artifact []byte

	mu    sync.Mutex
	paths []string
}

func (f *ghFixture) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

func (f *ghFixture) sawPath(p string) bool {
	for _, got := range f.seen() {
		if got == p {
			return true
		}
	}
	return false
}

// newGHFixture stands up a release-asset host under /<owner>/<repo>/releases/…, signing
// a stable manifest for `version` over the given binary.
func newGHFixture(t *testing.T, owner, repo, version string, bin []byte) *ghFixture {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	art := tarGzBinary(t, bin)
	sum := sha256.Sum256(art)
	f := &ghFixture{
		pubB64: base64.StdEncoding.EncodeToString(pub), version: version,
		artName: "olivares_" + version + "_linux_amd64.tar.gz", artifact: art,
	}
	m := release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion,
		Channel:       release.ChannelStable,
		Version:       version,
		ReleasedAt:    time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC),
		Artifacts: []release.Artifact{{
			OS: "linux", Arch: "amd64", Filename: f.artName,
			SHA256: hex.EncodeToString(sum[:]), Size: int64(len(art)),
		}},
	}
	mb, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	sig := base64.StdEncoding.EncodeToString(release.SignManifest(mb, priv))

	base := "/" + owner + "/" + repo + "/releases"
	mux := http.NewServeMux()
	record := func(h func(http.ResponseWriter)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			f.paths = append(f.paths, r.URL.Path)
			f.mu.Unlock()
			h(w)
		}
	}
	// The producer's asset names: release.yml:719 uploads `stable-manifest.json`, and
	// scripts/release-attach-stable-pair.sh:141-142 attaches `stable-manifest.json.sig`.
	for _, prefix := range []string{base + "/latest/download", base + "/download/v" + version} {
		mux.HandleFunc(prefix+"/stable-manifest.json", record(func(w http.ResponseWriter) { _, _ = w.Write(mb) }))
		mux.HandleFunc(prefix+"/stable-manifest.json.sig", record(func(w http.ResponseWriter) { _, _ = w.Write([]byte(sig)) }))
	}
	// The ARTIFACT lives only under the tag, never under /latest/download: if the
	// implementation ever fetched it through the mutable pointer this 404s, which is the
	// point of not registering it there.
	mux.HandleFunc(base+"/download/v"+version+"/"+f.artName,
		record(func(w http.ResponseWriter) { _, _ = w.Write(art) }))
	mux.HandleFunc("/", record(func(w http.ResponseWriter) { http.Error(w, "not found", http.StatusNotFound) }))

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *ghFixture) repoEndpoint(owner, repo string) string {
	return f.server.URL + "/" + owner + "/" + repo + "/releases/latest/download"
}

func (f *ghFixture) tagEndpoint(owner, repo, tag string) string {
	return f.server.URL + "/" + owner + "/" + repo + "/releases/tag/" + tag
}

func TestUpgradeFromGitHubReleases(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build stub binaries")
	}
	v1 := buildStub(t, "26.7.0")
	v2 := buildStub(t, "26.8.0")

	t.Run("latest release: manifest by asset name, artifact by the SIGNED tag", func(t *testing.T) {
		f := newGHFixture(t, "olivaresai", "olivares", "26.8.0", v2)
		target := writeTarget(t, v1)
		out, err := runUpgradeCmd(t, "--endpoint", f.repoEndpoint("olivaresai", "olivares"),
			"--pubkey", f.pubB64, "--target", target, "--os", "linux", "--arch", "amd64",
			"--yes", "--data-dir", t.TempDir())
		if err != nil {
			t.Fatalf("upgrade from release assets: %v\n%s", err, out)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.8.0") {
			t.Fatalf("target not upgraded: %q", got)
		}
		wantManifest := "/olivaresai/olivares/releases/latest/download/stable-manifest.json"
		if !f.sawPath(wantManifest) {
			t.Fatalf("manifest was not read as %s; the server saw %q", wantManifest, f.seen())
		}
		if !f.sawPath(wantManifest + ".sig") {
			t.Fatalf("detached signature was not read beside the manifest; server saw %q", f.seen())
		}
		wantArtifact := "/olivaresai/olivares/releases/download/v26.8.0/" + f.artName
		if !f.sawPath(wantArtifact) {
			t.Fatalf("artifact was not fetched from the signed tag %s; the server saw %q", wantArtifact, f.seen())
		}
		// NON-FIRING DIRECTION: the artifact must NOT be fetched through the mutable
		// `latest` pointer. Without this, an implementation that used /latest/download
		// for both reads would pass every assertion above.
		for _, p := range f.seen() {
			if strings.HasPrefix(p, "/olivaresai/olivares/releases/latest/download/olivares_") {
				t.Fatalf("the artifact was fetched through the mutable latest pointer: %s", p)
			}
		}
		// The operator is told which layout was chosen, on the first line, before anything
		// is downloaded — the layout decision is never silent.
		if !strings.Contains(out, "release assets of") {
			t.Fatalf("the run does not say it used the release-asset layout:\n%s", out)
		}
	})

	t.Run("a pinned release tag reads and downloads from that tag only", func(t *testing.T) {
		f := newGHFixture(t, "example-owner", "example-rehearsal", "26.8.0", v2)
		target := writeTarget(t, v1)
		out, err := runUpgradeCmd(t, "--endpoint",
			f.tagEndpoint("example-owner", "example-rehearsal", "v26.8.0"),
			"--pubkey", f.pubB64, "--target", target, "--os", "linux", "--arch", "amd64",
			"--yes", "--data-dir", t.TempDir())
		if err != nil {
			t.Fatalf("upgrade from a pinned tag: %v\n%s", err, out)
		}
		base := "/example-owner/example-rehearsal/releases/download/v26.8.0"
		if !f.sawPath(base + "/stable-manifest.json") {
			t.Fatalf("pinned manifest not read from the tag; server saw %q", f.seen())
		}
		if !f.sawPath(base + "/" + f.artName) {
			t.Fatalf("pinned artifact not read from the tag; server saw %q", f.seen())
		}
		// NON-FIRING: a pinned endpoint must never touch /latest/download at all. This is
		// what makes the T3 rehearsal meaningful — a rehearsal tag is a PRERELEASE, so
		// `latest` would not resolve to it and a silent fallback would test nothing.
		for _, p := range f.seen() {
			if strings.Contains(p, "/releases/latest/") {
				t.Fatalf("a pinned endpoint resolved through the latest pointer: %s", p)
			}
		}
	})

	t.Run("--check reads the channel and installs nothing", func(t *testing.T) {
		f := newGHFixture(t, "olivaresai", "olivares", "26.8.0", v2)
		target := writeTarget(t, v1)
		out, err := runUpgradeCmd(t, "--endpoint", f.repoEndpoint("olivaresai", "olivares"),
			"--pubkey", f.pubB64, "--target", target, "--os", "linux", "--arch", "amd64",
			"--check", "--data-dir", t.TempDir())
		if err != nil {
			t.Fatalf("--check against release assets: %v\n%s", err, out)
		}
		if !strings.Contains(out, "26.8.0") {
			t.Fatalf("--check did not report the available version:\n%s", out)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.7.0") {
			t.Fatalf("--check must not install: %q", got)
		}
	})

	t.Run("a wrong-channel ask is refused, not answered with stable", func(t *testing.T) {
		// The server publishes ONLY a stable manifest, which is exactly what release.yml
		// does for a tag that declares no advisory. Asking for `security` must fail with
		// the missing-channel explanation, never fall back to the stable bytes.
		f := newGHFixture(t, "olivaresai", "olivares", "26.8.0", v2)
		target := writeTarget(t, v1)
		_, err := runUpgradeCmd(t, "--endpoint", f.repoEndpoint("olivaresai", "olivares"),
			"--pubkey", f.pubB64, "--target", target, "--os", "linux", "--arch", "amd64",
			"--channel", "security", "--check", "--data-dir", t.TempDir())
		if err == nil {
			t.Fatal("asking for an unpublished channel must fail")
		}
		if !strings.Contains(err.Error(), "security-manifest.json") {
			t.Fatalf("the refusal must name the asset it looked for, got: %v", err)
		}
		if !strings.Contains(err.Error(), "is NOT a fallback to stable") {
			t.Fatalf("the refusal must say the channel does not fall back to stable, got: %v", err)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.7.0") {
			t.Fatalf("a refused channel must leave the target untouched: %q", got)
		}
	})
}

// TestCommunityEndpointWiring pins the two facts that belong to the CLI rather than to the
// layout resolver: the shipped default, and that the default resolves to the release-asset
// layout. Everything about the shapes themselves is proved in core/release
// (TestResolveChannel), where the resolver lives and where both of its readers can see it.
func TestCommunityEndpointWiring(t *testing.T) {
	t.Parallel()

	// The assertion that catches a silent revert to a host nobody serves. Measured
	// 2026-08-27: the previous default (https://olivares.ai/updates) and its stable
	// manifest both answered 404, with no producer in .github/ and no server.
	if defaultCommunityEndpoint != "https://github.com/olivaresai/olivares" {
		t.Fatalf("defaultCommunityEndpoint = %q; FIRMA B (2026-08-21) puts the community "+
			"channel on the public repository's GitHub Releases", defaultCommunityEndpoint)
	}
	src, err := buildCommunitySource(defaultCommunityEndpoint, release.ChannelStable, &http.Client{})
	if err != nil {
		t.Fatalf("the shipped default does not resolve: %v", err)
	}
	cs, ok := src.(communitySource)
	if !ok {
		t.Fatalf("the shipped default did not build the public transport: %T", src)
	}
	if !cs.layout.ReleaseAssets() {
		t.Fatal("the shipped default must resolve to the release-asset layout")
	}
	want := "https://github.com/olivaresai/olivares/releases/latest/download/stable-manifest.json"
	if got := cs.layout.ManifestURL(); got != want {
		t.Fatalf("default manifest URL = %q, want %q", got, want)
	}

	// NON-FIRING DIRECTION: the mirror fallback FIRMA B keeps must not be swallowed by the
	// release layout, and a github.com endpoint of neither shape must be refused here too —
	// the refusal has to survive the trip through the CLI, not only exist in the resolver.
	mirror, err := buildCommunitySource("https://updates.example.test/olivares", release.ChannelStable, &http.Client{})
	if err != nil {
		t.Fatalf("static mirror rejected: %v", err)
	}
	if mirror.(communitySource).layout.ReleaseAssets() {
		t.Fatal("a static mirror must keep the directory layout")
	}
	if _, err := buildCommunitySource("https://github.com/olivaresai", release.ChannelStable, &http.Client{}); err == nil {
		t.Fatal("a github.com endpoint that is not a repository must be refused through the CLI too")
	}
}
