// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/release"
)

// THE FAKE HERE INSPECTS `set`, AND THAT IS THE POINT.
//
// The pre-existing upgrade fake (cmd_upgrade_e2e_test.go) switches on `kind` alone and
// never reads `set`, so it accepts a request the deployed Worker refuses with
// "400 Bad Request: missing set" — which is how the engine's own gated manifest fetch
// could go green here and fail in production. A fake that is laxer than the contract
// it stands in for is not a witness; it is a second implementation nobody reviews.
//
// So this one enforces BOTH halves of the gate's split (gate.ts:72,209-210):
//   - manifest kinds REQUIRE `set` and must not carry os/arch,
//   - the binary path must NOT carry `set` (a client-supplied set there would be a
//     second derivation competing with the token) and REQUIRES os/arch.
// Either violation is a 400 with the gate's own wording, so this test fails the day
// export-mirror stops speaking the real contract.

type mirrorFake struct {
	server        *httptest.Server
	pubB64        string
	priv          ed25519.PrivateKey
	manifest      []byte
	sig           []byte
	arts          map[string][]byte // "os/arch" -> archive bytes
	sawSets       []string          // every `set` value the manifest path received
	sawChannels   []string          // and every `channel`, for the mutant that stopped sending it
	servesChannel string            // when set, the manifest is signed for THIS channel instead
	corrupt       bool              // serve artifact bytes that do not match the digest
}

func newMirrorFake(t *testing.T, platforms ...string) *mirrorFake {
	t.Helper()
	if len(platforms) == 0 {
		platforms = []string{"linux/amd64"}
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	f := &mirrorFake{pubB64: base64.StdEncoding.EncodeToString(pub), priv: priv, arts: map[string][]byte{}}

	var artifacts []release.Artifact
	for _, p := range platforms {
		goos, goarch, _ := strings.Cut(p, "/")
		blob := []byte("archive-for-" + goos + "-" + goarch)
		f.arts[p] = blob
		sum := sha256.Sum256(blob)
		artifacts = append(artifacts, release.Artifact{
			OS: goos, Arch: goarch,
			Filename: "olivares_26.8.0_" + goos + "_" + goarch + ".tar.gz",
			SHA256:   hex.EncodeToString(sum[:]), Size: int64(len(blob)),
		})
	}
	m := release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion,
		Channel:       release.ChannelStable,
		Version:       "26.8.0",
		ReleasedAt:    time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
		Artifacts:     artifacts,
	}
	mb, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	f.manifest = mb
	f.sig = []byte(release.SignManifest(mb, priv))

	mux := http.NewServeMux()
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		kind, set := q.Get("kind"), q.Get("set")
		// The channel steers the manifest key on the real gate, so a fake that ignores it
		// cannot see a command that stopped sending it. A mutant that dropped
		// `q.Set("channel", …)` survived precisely because of that.
		if q.Get("channel") == "" {
			http.Error(w, "Bad Request: missing channel", http.StatusBadRequest)
			return
		}
		f.sawChannels = append(f.sawChannels, q.Get("channel"))
		if kind != "" {
			// gate.ts:209-210 — a missing set is 400, never a silent default.
			if set == "" {
				http.Error(w, "Bad Request: missing set", http.StatusBadRequest)
				return
			}
			if q.Get("os") != "" || q.Get("arch") != "" {
				http.Error(w, "Bad Request: os/arch do not steer manifest keys", http.StatusBadRequest)
				return
			}
			f.sawSets = append(f.sawSets, set)
			switch kind {
			case "manifest":
				_, _ = w.Write(f.manifest)
			case "manifest.sig":
				_, _ = w.Write(f.sig)
			default:
				http.Error(w, `Bad Request: unknown kind "`+kind+`"`, http.StatusBadRequest)
			}
			return
		}
		// Binary path: set is refused, not ignored — and PRESENCE is what counts.
		// The Worker uses `searchParams.has("set")`, so `&set=` with an empty value is
		// already a 400 there. Testing `!= ""` here made the fake laxer than the contract
		// and a mutant that sent `set=""` on this branch survived every test.
		if q.Has("set") {
			http.Error(w, "Bad Request: set does not steer the binary key", http.StatusBadRequest)
			return
		}
		goos, goarch := q.Get("os"), q.Get("arch")
		if goos == "" || goarch == "" {
			http.Error(w, "Bad Request: missing os/arch", http.StatusBadRequest)
			return
		}
		blob, ok := f.arts[goos+"/"+goarch]
		if !ok {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		if f.corrupt {
			blob = append([]byte("tampered-"), blob...)
		}
		_, _ = w.Write(blob)
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func runMirror(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newReleaseCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"export-mirror"}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestExportMirrorWritesTheBundleUpgradeAlreadyConsumes(t *testing.T) {
	f := newMirrorFake(t, "linux/amd64", "darwin/arm64")
	dir := t.TempDir()
	out, err := runMirror(t,
		"--endpoint", f.server.URL, "--token", "tkn", "--set", "biz+reg",
		"--pubkey", f.pubB64, "--out", dir)
	if err != nil {
		t.Fatalf("export-mirror: %v\n%s", err, out)
	}

	// The bundle contract is bundleSource's, not one invented here.
	for _, want := range []string{"manifest.json", "manifest.json.sig",
		"olivares_26.8.0_linux_amd64.tar.gz", "olivares_26.8.0_darwin_arm64.tar.gz"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("bundle is missing %s: %v", want, err)
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil || !bytes.Equal(got, f.manifest) {
		t.Errorf("manifest.json is not the bytes the vendor signed")
	}

	// The whole reason this fake exists: `set` really travelled, on manifest kinds only.
	if len(f.sawSets) != 2 {
		t.Fatalf("expected the manifest and its signature to carry set; saw %v", f.sawSets)
	}
	for _, s := range f.sawSets {
		if s != "biz+reg" {
			t.Errorf("set reached the gate as %q, want biz+reg", s)
		}
	}
}

func TestExportMirrorRefusesABadSignatureBeforeWritingAnything(t *testing.T) {
	f := newMirrorFake(t)
	f.sig = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(f.priv, []byte("some other message"))))
	dir := t.TempDir()

	out, err := runMirror(t,
		"--endpoint", f.server.URL, "--token", "tkn", "--set", "biz",
		"--pubkey", f.pubB64, "--out", dir)
	if err == nil {
		t.Fatalf("a manifest that fails verification must be refused; output was:\n%s", out)
	}
	// `err != nil` is not enough: with the verdict ignored the manifest parses EMPTY and the
	// run dies later on "no artifacts", which is a different refusal wearing the same shape.
	// A mutant that verifies and drops the verdict survived exactly that assertion.
	if !strings.Contains(err.Error(), "verification") {
		t.Fatalf("the refusal must name verification, got: %v", err)
	}
	// "nothing written" is the claim the code makes, so the test has to check the disk,
	// not the message: a refusal that already dropped files leaves a half-mirror behind.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		t.Errorf("refused but wrote %v", names)
	}
}

func TestExportMirrorRefusesAnArtifactThatDoesNotMatchTheSignedDigest(t *testing.T) {
	f := newMirrorFake(t)
	f.corrupt = true
	dir := t.TempDir()

	out, err := runMirror(t,
		"--endpoint", f.server.URL, "--token", "tkn", "--set", "biz",
		"--pubkey", f.pubB64, "--out", dir)
	if err == nil {
		t.Fatalf("bytes that contradict the signed digest must be refused; output was:\n%s", out)
	}
	if !strings.Contains(err.Error(), "digest") && !strings.Contains(err.Error(), "sha256") {
		t.Errorf("the refusal should name the digest, got: %v", err)
	}
}

func TestExportMirrorRefusesAPlatformTheManifestDoesNotName(t *testing.T) {
	f := newMirrorFake(t, "linux/amd64")
	dir := t.TempDir()

	_, err := runMirror(t,
		"--endpoint", f.server.URL, "--token", "tkn", "--set", "biz",
		"--pubkey", f.pubB64, "--platform", "windows/arm64", "--out", dir)
	if err == nil {
		t.Fatal("an unmirrorable platform must refuse, not produce an empty bundle discovered air-gapped")
	}
	if !strings.Contains(err.Error(), "windows/arm64") {
		t.Errorf("the refusal should name the platform asked for, got: %v", err)
	}
}

func TestExportMirrorTarballIsFlatAndCarriesTheSameEntries(t *testing.T) {
	f := newMirrorFake(t, "linux/amd64")
	out := filepath.Join(t.TempDir(), "mirror.tar.gz")

	if _, err := runMirror(t,
		"--endpoint", f.server.URL, "--token", "tkn", "--set", "biz",
		"--pubkey", f.pubB64, "--out", out); err != nil {
		t.Fatalf("export-mirror: %v", err)
	}
	fh, err := os.Open(out)
	if err != nil {
		t.Fatalf("open tarball: %v", err)
	}
	defer func() { _ = fh.Close() }()
	gz, err := gzip.NewReader(fh)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		// A bundle reader joins bare leaf names; a tar carrying paths is how an
		// extractor gets talked out of its own directory.
		if strings.ContainsAny(h.Name, `/\`) {
			t.Errorf("tar entry %q must be a bare filename", h.Name)
		}
		names = append(names, h.Name)
	}
	sort.Strings(names)
	want := []string{"manifest.json", "manifest.json.sig", "olivares_26.8.0_linux_amd64.tar.gz"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("tarball entries = %v, want %v", names, want)
	}
}

func TestExportMirrorNamesEveryMissingRequiredFlagAtOnce(t *testing.T) {
	_, err := runMirror(t, "--endpoint", "http://example.invalid")
	if err == nil {
		t.Fatal("missing required flags must refuse")
	}
	for _, want := range []string{"--token", "--set", "--out"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should name %s in one message, got: %v", want, err)
		}
	}
}

func TestExportMirrorRequiresAnEndpointItDoesNotInvent(t *testing.T) {
	// The command DECLARES --endpoint required. Nothing tested it, so a mutant that
	// deleted the guard passed all six original witnesses: it then built a URL from an
	// empty base and failed later for an unrelated reason, which is a different bug
	// wearing the same exit code.
	_, err := runMirror(t, "--token", "tkn", "--set", "biz", "--out", t.TempDir())
	if err == nil {
		t.Fatal("a missing --endpoint must be refused by name")
	}
	if !strings.Contains(err.Error(), "--endpoint") {
		t.Errorf("the refusal must name --endpoint, got: %v", err)
	}
}

func TestExportMirrorRefusesAManifestSignedForAnotherChannel(t *testing.T) {
	// A perfectly signed manifest is not necessarily the manifest we asked for. Measured
	// by the contrast: with --channel security the command accepted a signed STABLE
	// manifest and then printed "channel security". The consumer catches it at install
	// time; that is not a reason for the exporter to lie about what it exported.
	f := newMirrorFake(t)
	dir := t.TempDir()
	_, err := runMirror(t,
		"--endpoint", f.server.URL, "--token", "tkn", "--set", "biz",
		"--channel", "security", "--pubkey", f.pubB64, "--out", dir)
	if err == nil {
		t.Fatal("a manifest signed for another channel must be refused")
	}
	if !strings.Contains(err.Error(), "security") || !strings.Contains(err.Error(), "stable") {
		t.Errorf("the refusal must name BOTH channels, got: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("refused after writing %d entrie(s)", len(entries))
	}
}

func TestExportMirrorWritesTheArtifactBytesAndNotSomethingElse(t *testing.T) {
	// The original suite checked that the file EXISTS. A mutant that verified the
	// artifact digest and then wrote manifestBytes into the artifact name survived that:
	// the file was there, the verification had run, and the content was wrong.
	f := newMirrorFake(t, "linux/amd64")
	dir := t.TempDir()
	if _, err := runMirror(t,
		"--endpoint", f.server.URL, "--token", "tkn", "--set", "biz",
		"--pubkey", f.pubB64, "--out", dir); err != nil {
		t.Fatalf("export-mirror: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "olivares_26.8.0_linux_amd64.tar.gz"))
	if err != nil {
		t.Fatalf("read the mirrored artifact: %v", err)
	}
	if !bytes.Equal(got, f.arts["linux/amd64"]) {
		t.Errorf("the mirrored artifact is not the bytes the gate served (%d vs %d bytes)",
			len(got), len(f.arts["linux/amd64"]))
	}
	if bytes.Equal(got, f.manifest) {
		t.Error("the artifact file contains the MANIFEST bytes")
	}
}

func TestExportMirrorRefusesANonEmptyDestinationUnlessForced(t *testing.T) {
	// A refresh that fails partway used to leave the old manifest live beside a replaced
	// artifact: a MIXED directory that the real consumer still accepts. Refusing a
	// non-empty destination is what makes "all or nothing" true on disk.
	f := newMirrorFake(t, "linux/amd64")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := runMirror(t,
		"--endpoint", f.server.URL, "--token", "tkn", "--set", "biz",
		"--pubkey", f.pubB64, "--out", dir)
	if err == nil {
		t.Fatal("a non-empty --out must be refused without --force")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("the refusal must name the way out, got: %v", err)
	}
	// And with --force it must succeed AND leave no trace of the old content.
	if _, err := runMirror(t,
		"--endpoint", f.server.URL, "--token", "tkn", "--set", "biz",
		"--pubkey", f.pubB64, "--force", "--out", dir); err != nil {
		t.Fatalf("--force should replace it: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil || bytes.Equal(got, []byte("{}")) {
		t.Error("--force left the OLD manifest in place")
	}
}

func TestOpenMirrorOutRestoresThePreviousBundleWhenPublishingFails(t *testing.T) {
	// The failure that MATTERS is not the mirror failing: it is a failed refresh that
	// destroys a good bundle. With --force the old destination is moved aside, and if the
	// final rename then fails the old one must come BACK, not be tidied away.
	//
	// Driven at openMirrorOut rather than through the command, and that is the honest
	// choice: the first version of this test tried to force the failure with --out as a
	// regular file, and it FAILED as a test because the aside-move makes the rename
	// succeed. There is no CLI-level input that makes the final rename fail on demand, so
	// pretending otherwise would have been a test that passes for the wrong reason.
	//
	// Here the staging directory is removed before finish(), so the rename fails with
	// ENOENT — deterministic, no permissions games, and it exercises exactly the branch
	// that decides whether a good bundle survives a failed publish.
	raiz := t.TempDir()
	out := filepath.Join(raiz, "destino")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "manifest.json"), []byte("el bundle viejo"), 0o644); err != nil {
		t.Fatal(err)
	}

	staging, finish, err := openMirrorOut(out, true)
	if err != nil {
		t.Fatalf("openMirrorOut: %v", err)
	}
	if err := os.RemoveAll(staging); err != nil { // <- fuerza el fallo de publicación
		t.Fatal(err)
	}
	if _, err := finish(); err == nil {
		t.Fatal("publishing from a staging directory that no longer exists must fail")
	}

	got, readErr := os.ReadFile(filepath.Join(out, "manifest.json"))
	if readErr != nil {
		t.Fatalf("the previous bundle is GONE after a failed publish: %v", readErr)
	}
	if !bytes.Equal(got, []byte("el bundle viejo")) {
		t.Errorf("the previous bundle was altered despite the failure: %q", got)
	}
	entries, _ := os.ReadDir(raiz)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".replaced-") || strings.Contains(e.Name(), ".olivares-mirror-") {
			t.Errorf("left behind %q", e.Name())
		}
	}
}

func TestExportMirrorDoesNotWriteThroughAPlantedSymlink(t *testing.T) {
	// HIGH from the contrast: os.WriteFile follows a pre-existing symlink at the target,
	// so a planted link made the mirrored bytes land outside the output directory while
	// the command returned success. Staging prevents it; O_NOFOLLOW proves it.
	f := newMirrorFake(t, "linux/amd64")
	raiz := t.TempDir()
	dir := filepath.Join(raiz, "salida")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fuera := filepath.Join(raiz, "fuera.txt")
	if err := os.WriteFile(fuera, []byte("intacto"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fuera, filepath.Join(dir, "olivares_26.8.0_linux_amd64.tar.gz")); err != nil {
		t.Skipf("this file system does not support symlinks: %v", err)
	}
	// It is refused as a non-empty destination; with --force it must STILL not write
	// through the link, which is the half a "non-empty" check alone would not give.
	if _, err := runMirror(t,
		"--endpoint", f.server.URL, "--token", "tkn", "--set", "biz",
		"--pubkey", f.pubB64, "--force", "--out", dir); err != nil {
		t.Fatalf("export-mirror with --force: %v", err)
	}
	got, err := os.ReadFile(fuera)
	if err != nil {
		t.Fatalf("read the file outside the output: %v", err)
	}
	if !bytes.Equal(got, []byte("intacto")) {
		t.Fatalf("the mirror wrote THROUGH the symlink: the file outside now holds %d bytes", len(got))
	}
}
