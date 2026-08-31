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
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/release"
)

// cmd_upgrade_e2e_test.go is the wire-proof for `olivares upgrade`: it stands
// up a fake update host serving a SIGNED per-channel manifest + artifact (both the
// community static layout and the enterprise gate contract), and drives the REAL
// command end to end for every path — community, enterprise, air-gap bundle, --check,
// anti-rollback (blocked + audited-force), up-to-date, min-version, and the tamper
// aborts (bad artifact, tampered manifest, wrong signature). It execs the command;
// nothing internal is mocked.

// buildStub compiles a tiny binary that prints "olivares <ver>", so the upgrade
// exec-probe accepts it and the test can read which version is installed.
func buildStub(t *testing.T, ver string) []byte {
	t.Helper()
	dir := t.TempDir()
	src := "package main\nimport (\"fmt\"; \"os\")\nfunc main(){ _ = os.Args; fmt.Println(\"olivares " + ver + " (commit t, built t, test)\") }\n"
	srcPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(dir, "stub")
	cmd := exec.Command("go", "build", "-o", binPath, srcPath)
	cmd.Env = append(os.Environ(), "GOFLAGS=-p=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build stub %q: %v\n%s", ver, err, out)
	}
	b, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// buildFlakyStub compiles a binary that PASSES its first `version` invocation (the
// candidate pre-probe) but FAILS every subsequent one (the post-swap probe), using a
// sentinel file. It lets the test deterministically exercise atomicSwap's automatic
// rollback: a SHA-valid artifact that does not run after the swap must revert to the
// previous binary ("arranque roto ⇒ vuelve al anterior" DoD).
func buildFlakyStub(t *testing.T, sentinel string) []byte {
	t.Helper()
	dir := t.TempDir()
	src := "package main\nimport (\"fmt\"; \"os\")\nfunc main(){\n" +
		"if _, err := os.Stat(" + strconvQuote(sentinel) + "); err == nil { os.Exit(1) }\n" +
		"_ = os.WriteFile(" + strconvQuote(sentinel) + ", []byte(\"x\"), 0o644)\n" +
		"fmt.Println(\"olivares 26.8.0 (flaky test)\")\n}\n"
	srcPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(dir, "flaky")
	cmd := exec.Command("go", "build", "-o", binPath, srcPath)
	cmd.Env = append(os.Environ(), "GOFLAGS=-p=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build flaky stub: %v\n%s", err, out)
	}
	b, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// strconvQuote is strconv.Quote inlined to avoid an extra import in this test file.
func strconvQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// tarGzBinary wraps a binary in a goreleaser-style .tar.gz with the executable at
// the archive root named `olivares` (what extractBinary looks for).
func tarGzBinary(t *testing.T, bin []byte) []byte {
	t.Helper()
	var gzBuf bytes.Buffer
	zw := gzip.NewWriter(&gzBuf)
	tw := tar.NewWriter(zw)
	hdr := &tar.Header{Name: "olivares", Mode: 0o755, Size: int64(len(bin)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(bin); err != nil {
		t.Fatal(err)
	}
	// A second file (LICENSE) to prove extractBinary picks the executable, not the first entry.
	lic := []byte("AGPL")
	if err := tw.WriteHeader(&tar.Header{Name: "LICENSE", Mode: 0o644, Size: int64(len(lic)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(lic); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return gzBuf.Bytes()
}

// CHANGED BY: the fixture license carries a TERM. It was issued without an expiry,
// which meant "perpetual" and satisfied `upgrade --enterprise`; under term-only v8 a
// termless blob reads "expired" and the upgrade refuses it — correctly, but that would
// make this OTA test fail for a reason that has nothing to do with OTA.
func installDevLicense(t *testing.T, dataDir string) {
	t.Helper()
	now := time.Now().UTC()
	blob, err := license.Sign(license.Claims{
		Licensee: "Wire-Proof Test", Plan: "enterprise",
		IssuedAt: now, ExpiresAt: now.Add(365 * 24 * time.Hour),
	}, license.DevPrivateKey())
	if err != nil {
		t.Fatalf("sign dev license: %v", err)
	}
	if err := os.WriteFile(licenseDataDirPath(dataDir), []byte(blob+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// updFixture is a signed release served by a fake host (both community + gate routes).
type updFixture struct {
	server     *httptest.Server
	pubB64     string
	priv       ed25519.PrivateKey
	version    string
	minVersion string
	artifact   []byte // the served .tar.gz
	artName    string
	corrupt    bool // serve a byte-flipped artifact (SHA mismatch)
	badSig     bool // serve a wrong manifest signature
	// enterpriseGrants is the fake gate's live registry, keyed by the token the
	// engine presents. The default holder owns a non-trivial set so a hard-coded
	// `biz` fallback cannot impersonate grant derivation.
	enterpriseGrants map[string][]string
	enterpriseSet    string
	enterpriseMu     sync.Mutex
	enterpriseTrace  []enterpriseGateRequest
	// onArtifact, when set, runs just before the artifact bytes are served. It is the
	// only deterministic way to act DURING the download window — the interval between
	// the reading the ordering guards were decided against and the swap — which is
	// where the concurrent-install defects of C03-23 live. A sleep would be a race.
	onArtifact func()
}

type enterpriseGateRequest struct {
	kind   string
	hasSet bool
}

func (f *updFixture) recordEnterpriseRequest(r *http.Request) {
	f.enterpriseMu.Lock()
	defer f.enterpriseMu.Unlock()
	f.enterpriseTrace = append(f.enterpriseTrace, enterpriseGateRequest{
		kind: r.URL.Query().Get("kind"), hasSet: r.URL.Query().Has("set"),
	})
}

func (f *updFixture) enterpriseRequests() []enterpriseGateRequest {
	f.enterpriseMu.Lock()
	defer f.enterpriseMu.Unlock()
	return append([]enterpriseGateRequest(nil), f.enterpriseTrace...)
}

// fakeSetSlug mirrors the Worker's canonical set derivation closely enough for this
// end-to-end double to enforce the same observable contract. Empty, duplicated,
// unknown, add-on-only and enterprise-plus-other grant sets are not derivable.
func fakeSetSlug(codes []string) (string, bool) {
	if len(codes) == 0 {
		return "", false
	}
	seen := make(map[string]struct{}, len(codes))
	addons := make([]string, 0, len(codes))
	hasBase := false
	hasEnterprise := false
	for _, code := range codes {
		if _, duplicate := seen[code]; duplicate {
			return "", false
		}
		seen[code] = struct{}{}
		switch code {
		case "biz":
			hasBase = true
		case "ent":
			hasEnterprise = true
		case "airs", "cp", "ids", "reg":
			addons = append(addons, code)
		default:
			return "", false
		}
	}
	if hasEnterprise {
		return "ent", len(codes) == 1
	}
	if !hasBase {
		return "", false
	}
	sort.Strings(addons)
	return strings.Join(append([]string{"biz"}, addons...), "+"), true
}

func fakeCodesFromSlug(slug string) ([]string, bool) {
	codes := strings.Split(slug, "+")
	canonical, ok := fakeSetSlug(codes)
	return codes, ok && canonical == slug
}

// resolveEnterpriseSet is the entitlement seam the old double omitted. The real
// CLI never sends set: manifest requests derive it from the token holder's grants;
// an explicit mirror selector remains allowlisted and grant-checked. Binary requests
// also require a derivable grant set and reject client steering.
func (f *updFixture) resolveEnterpriseSet(w http.ResponseWriter, r *http.Request) (string, bool) {
	q := r.URL.Query()
	live, knownToken := f.enterpriseGrants[q.Get("token")]
	if !knownToken {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return "", false
	}

	switch q.Get("kind") {
	case "manifest", "manifest.sig":
		if !q.Has("set") {
			derived, ok := fakeSetSlug(live)
			if !ok {
				if len(live) == 0 {
					http.Error(w, "Forbidden: no live grant for set", http.StatusForbidden)
				} else {
					http.Error(w, "Forbidden: live grants do not form an allowed set", http.StatusForbidden)
				}
				return "", false
			}
			return derived, true
		}
		rawSet := strings.ReplaceAll(q.Get("set"), " ", "+")
		if rawSet == "" {
			http.Error(w, "Bad Request: missing set", http.StatusBadRequest)
			return "", false
		}
		requested, ok := fakeCodesFromSlug(rawSet)
		if !ok {
			http.Error(w, "Bad Request: unknown set", http.StatusBadRequest)
			return "", false
		}
		liveCodes := make(map[string]struct{}, len(live))
		for _, code := range live {
			liveCodes[code] = struct{}{}
		}
		for _, code := range requested {
			if _, ok := liveCodes[code]; !ok {
				http.Error(w, "Forbidden: no live grant for "+code, http.StatusForbidden)
				return "", false
			}
		}
		return rawSet, true
	case "":
		if q.Has("set") || q.Has("variant") {
			http.Error(w, "Bad Request: binary selector is grant-derived", http.StatusBadRequest)
			return "", false
		}
		derived, ok := fakeSetSlug(live)
		if !ok {
			http.Error(w, "Forbidden: no live grant for set", http.StatusForbidden)
			return "", false
		}
		return derived, true
	default:
		http.Error(w, "Bad Request: unknown kind", http.StatusBadRequest)
		return "", false
	}
}

func newUpdFixture(t *testing.T, version, minVersion string, binBytes []byte) *updFixture {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	f := &updFixture{
		pubB64: base64.StdEncoding.EncodeToString(pub), priv: priv,
		version: version, minVersion: minVersion,
		artifact: tarGzBinary(t, binBytes), artName: "olivares_" + version + "_linux_amd64.tar.gz",
		enterpriseGrants: map[string][]string{"tkn": {"biz", "reg"}},
		enterpriseSet:    "biz+reg",
	}
	sum := sha256.Sum256(f.artifact)
	m := release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion,
		Channel:       release.ChannelStable,
		Version:       version,
		MinVersion:    minVersion,
		ReleasedAt:    time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC),
		Security:      true,
		Advisories:    []string{"OSV-2026-9999"},
		Notes:         "wire-proof",
		Artifacts: []release.Artifact{
			{OS: "linux", Arch: "amd64", Filename: f.artName, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(f.artifact))},
		},
	}
	mb, _ := json.Marshal(m)
	sig := release.SignManifest(mb, priv)

	writeSig := func(w http.ResponseWriter) {
		if f.badSig {
			_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte("other")))))
			return
		}
		_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(sig)))
	}
	writeArtifact := func(w http.ResponseWriter) {
		if f.onArtifact != nil {
			f.onArtifact()
		}
		body := f.artifact
		if f.corrupt {
			body = append([]byte(nil), f.artifact...)
			body[len(body)/2] ^= 0xFF
		}
		_, _ = w.Write(body)
	}

	mux := http.NewServeMux()
	// Enterprise gate contract: /download?kind=manifest|manifest.sig|<binary>.
	// Unlike the old double, this resolves the set from live grants before the kind
	// switch, so an engine-shaped request without set is either derived or refused.
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		f.recordEnterpriseRequest(r)
		resolvedSet, ok := f.resolveEnterpriseSet(w, r)
		if !ok {
			return
		}
		if resolvedSet != f.enterpriseSet {
			http.Error(w, "Not Found: object unavailable for set", http.StatusNotFound)
			return
		}
		switch r.URL.Query().Get("kind") {
		case "manifest":
			_, _ = w.Write(mb)
		case "manifest.sig":
			writeSig(w)
		default:
			writeArtifact(w)
		}
	})
	// Community static layout: /<channel>/manifest.json[.sig] and /<channel>/<filename>.
	mux.HandleFunc("/stable/manifest.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(mb) })
	mux.HandleFunc("/stable/manifest.json.sig", func(w http.ResponseWriter, _ *http.Request) { writeSig(w) })
	mux.HandleFunc("/stable/"+f.artName, func(w http.ResponseWriter, _ *http.Request) { writeArtifact(w) })
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// writeBundle materializes an air-gap bundle dir from the fixture (manifest+sig+artifact).
func (f *updFixture) writeBundle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	m := release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion, Channel: release.ChannelStable, Version: f.version,
		MinVersion: f.minVersion, ReleasedAt: time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC),
		Artifacts: []release.Artifact{{OS: "linux", Arch: "amd64", Filename: f.artName,
			SHA256: hex.EncodeToString(sha256hash(f.artifact)), Size: int64(len(f.artifact))}},
	}
	mb, _ := json.Marshal(m)
	sig := release.SignManifest(mb, f.priv)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "manifest.json"), mb, 0o644))
	must(os.WriteFile(filepath.Join(dir, "manifest.json.sig"), []byte(base64.StdEncoding.EncodeToString(sig)), 0o644))
	must(os.WriteFile(filepath.Join(dir, f.artName), f.artifact, 0o644))
	return dir
}

func sha256hash(b []byte) []byte { s := sha256.Sum256(b); return s[:] }

// runUpgradeCmd execs the real command with the given args.
func runUpgradeCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	// Pin the data directory. `upgrade --install-timer` resolves one to write
	// the unit files, and defaultDataDir falls back to $HOME — correctly
	// refusing when there is none, because that directory holds private
	// signing keys and must never default to the working directory. On a CI
	// runner with no $HOME that refusal failed TestUpgradeInstallTimer while
	// the production code was behaving exactly as designed. No upgrade test
	// asserts the refusal (that lives in datadir_git_test.go), so pinning here
	// makes all 25 call sites independent of the ambient environment.
	t.Setenv("OLIVARES_DATA_DIR", t.TempDir())
	cmd := newUpgradeCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	t.Logf("upgrade %v ->\n%s", args, buf.String())
	return buf.String(), err
}

func runsVersion(t *testing.T, bin string) string {
	t.Helper()
	out, err := exec.Command(bin, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("run %s: %v\n%s", bin, err, out)
	}
	return strings.TrimSpace(string(out))
}

// execCapableDir returns a fresh directory whose files can actually be EXECUTED.
//
// t.TempDir() honors $TMPDIR and falls back to /tmp, and a hardened host mounts
// /tmp noexec — where every binary this suite installs is unrunnable. That is not
// hypothetical: it is exactly what made nine of these twelve subtests red while
// REPORTING a minimum-version error. The exec-probe of the target failed with
// "permission denied", the updater silently substituted its own main.version ("dev"),
// "dev" parses to the zero version, and the zero version is below every min_version —
// so the anti-rollback and auto-rollback assertions received the min-version gate's
// message and the real cause never appeared anywhere.
//
// A fixture that cannot execute the binaries it installs cannot state its own
// premise, so this asks the filesystem instead of assuming.
func execCapableDir(t *testing.T) string {
	t.Helper()
	try := func(dir string) bool {
		p := filepath.Join(dir, "execprobe")
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			return false
		}
		defer func() { _ = os.Remove(p) }()
		return exec.Command(p).Run() == nil
	}
	if d := t.TempDir(); try(d) {
		return d
	}
	// $TMPDIR/tmp is noexec here. $HOME is on the image's own filesystem and is the
	// documented escape in this dev container.
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if d, err := os.MkdirTemp(home, ".olivares-ota-e2e-*"); err == nil {
			t.Cleanup(func() { _ = os.RemoveAll(d) })
			if try(d) {
				return d
			}
			_ = os.RemoveAll(d)
		}
	}
	t.Skipf("no exec-capable directory for the OTA fixtures: $TMPDIR/t.TempDir() and $HOME both refuse to execute a file "+
		"(TMPDIR=%q). This suite installs and runs real binaries; it cannot assert anything here. "+
		"Re-run with TMPDIR pointing at an exec-capable filesystem.", os.Getenv("TMPDIR"))
	return ""
}

func writeTarget(t *testing.T, bin []byte) string {
	t.Helper()
	p := filepath.Join(execCapableDir(t), "olivares")
	if err := os.WriteFile(p, bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// The premise, asserted rather than assumed: this binary must run, or every
	// version-ordering assertion downstream is about a version nobody could read.
	if out, err := exec.Command(p, "version").CombinedOutput(); err != nil {
		t.Fatalf("fixture premise broken: the target %s cannot be executed (%v)\n%s", p, err, out)
	}
	return p
}

func TestUpgradeE2E(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build stub binaries")
	}
	v1 := buildStub(t, "26.7.0")
	v2 := buildStub(t, "26.8.0")

	t.Run("community happy path (no license, no token)", func(t *testing.T) {
		f := newUpdFixture(t, "26.8.0", "26.6.0", v2)
		target := writeTarget(t, v1)
		_, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", t.TempDir())
		if err != nil {
			t.Fatalf("community upgrade: %v", err)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.8.0") {
			t.Fatalf("target not upgraded: %q", got)
		}
		if b, _ := filepath.Glob(target + ".bak-*"); len(b) != 1 || !strings.Contains(runsVersion(t, b[0]), "26.7.0") {
			t.Fatalf("backup missing or wrong: %v", b)
		}
	})

	t.Run("enterprise happy path (license + token via gate)", func(t *testing.T) {
		dataDir := t.TempDir()
		installDevLicense(t, dataDir)
		f := newUpdFixture(t, "26.8.0", "26.6.0", v2)
		target := writeTarget(t, v1)
		_, err := runUpgradeCmd(t, "--enterprise", "--token", "tkn", "--endpoint", f.server.URL,
			"--pubkey", f.pubB64, "--data-dir", dataDir, "--target", target, "--os", "linux", "--arch", "amd64", "--yes")
		if err != nil {
			t.Fatalf("enterprise upgrade: %v", err)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.8.0") {
			t.Fatalf("target not upgraded: %q", got)
		}
		trace := f.enterpriseRequests()
		wantKinds := []string{"manifest", "manifest.sig", ""}
		if len(trace) != len(wantKinds) {
			t.Fatalf("enterprise request trace = %#v, want kinds %q", trace, wantKinds)
		}
		for i, wantKind := range wantKinds {
			if trace[i].kind != wantKind || trace[i].hasSet {
				t.Fatalf("enterprise request %d = %#v, want kind %q without set", i, trace[i], wantKind)
			}
		}
	})

	t.Run("enterprise manifest without live grants is refused", func(t *testing.T) {
		dataDir := t.TempDir()
		installDevLicense(t, dataDir)
		f := newUpdFixture(t, "26.8.0", "26.6.0", v2)
		f.enterpriseGrants["tkn"] = nil
		target := writeTarget(t, v1)
		_, err := runUpgradeCmd(t, "--enterprise", "--token", "tkn", "--endpoint", f.server.URL,
			"--pubkey", f.pubB64, "--data-dir", dataDir, "--target", target, "--os", "linux", "--arch", "amd64", "--check")
		if err == nil || !strings.Contains(err.Error(), "endpoint returned 403: Forbidden: no live grant for set") {
			t.Fatalf("the gate double must 403 a set-less manifest without live grants, got %v", err)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.7.0") {
			t.Fatalf("a refused manifest must leave the target untouched: %q", got)
		}
	})

	// CHANGED BY C02-20: the bundle route is license-gated, so this arm installs the
	// fixture license first. Its old name — "air-gap bundle (100% offline)" — is retired
	// with the behavior it described: this subtest USED to prove that a bundle installs
	// with an empty data dir, which is exactly the vector `buildUpdateSource` now closes,
	// so leaving the name would have left a green test asserting the hole. The gate itself
	// is witnessed in both directions in cmd_upgrade_bundle_gate_test.go; this stays the
	// route's place in the end-to-end sweep.
	t.Run("air-gap bundle (no network, gated on a live license)", func(t *testing.T) {
		dataDir := t.TempDir()
		installDevLicense(t, dataDir)
		f := newUpdFixture(t, "26.8.0", "26.6.0", v2)
		bundle := f.writeBundle(t)
		target := writeTarget(t, v1)
		// No --endpoint: the bundle path must not touch the network.
		_, err := runUpgradeCmd(t, "--bundle", bundle, "--pubkey", f.pubB64,
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", dataDir)
		if err != nil {
			t.Fatalf("bundle upgrade: %v", err)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.8.0") {
			t.Fatalf("bundle target not upgraded: %q", got)
		}
	})

	t.Run("tampered artifact aborts, binary untouched", func(t *testing.T) {
		f := newUpdFixture(t, "26.8.0", "26.6.0", v2)
		f.corrupt = true
		target := writeTarget(t, v1)
		_, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "SHA-256") {
			t.Fatalf("tampered artifact must abort with a checksum error, got %v", err)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.7.0") {
			t.Fatalf("target changed despite tamper: %q", got)
		}
		if b, _ := filepath.Glob(target + ".bak-*"); len(b) != 0 {
			t.Fatalf("a tamper-aborted upgrade must not leave a backup: %v", b)
		}
	})

	t.Run("tampered manifest signature aborts", func(t *testing.T) {
		f := newUpdFixture(t, "26.8.0", "26.6.0", v2)
		f.badSig = true
		target := writeTarget(t, v1)
		_, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", t.TempDir())
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "signature") {
			t.Fatalf("bad manifest signature must abort, got %v", err)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.7.0") {
			t.Fatalf("target changed despite bad signature: %q", got)
		}
	})

	t.Run("no license refuses enterprise with guidance", func(t *testing.T) {
		f := newUpdFixture(t, "26.8.0", "26.6.0", v2)
		target := writeTarget(t, v1)
		_, err := runUpgradeCmd(t, "--enterprise", "--token", "tkn", "--endpoint", f.server.URL,
			"--pubkey", f.pubB64, "--data-dir", t.TempDir(), "--target", target, "--os", "linux", "--arch", "amd64", "--yes")
		if err == nil || !strings.Contains(err.Error(), "license") {
			t.Fatalf("missing license must refuse, got %v", err)
		}
	})

	t.Run("--check verifies without swapping", func(t *testing.T) {
		f := newUpdFixture(t, "26.8.0", "26.6.0", v2)
		target := writeTarget(t, v1)
		out, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
			"--target", target, "--os", "linux", "--arch", "amd64", "--check", "--data-dir", t.TempDir())
		if err != nil {
			t.Fatalf("--check: %v", err)
		}
		if !strings.Contains(out, "upgrade available") || !strings.Contains(out, "OSV-2026-9999") {
			t.Fatalf("--check should show the plan + advisory, got:\n%s", out)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.7.0") {
			t.Fatalf("--check must not swap: %q", got)
		}
	})

	t.Run("up-to-date is a no-op", func(t *testing.T) {
		f := newUpdFixture(t, "26.7.0", "26.6.0", v1) // manifest version == installed
		target := writeTarget(t, v1)
		out, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", t.TempDir())
		if err != nil {
			t.Fatalf("up-to-date: %v", err)
		}
		if !strings.Contains(out, "nothing to do") {
			t.Fatalf("expected up-to-date no-op, got:\n%s", out)
		}
	})

	t.Run("anti-rollback: blocked, then audited force", func(t *testing.T) {
		dataDir := t.TempDir()
		// NO min_version: this subtest is about anti-rollback, so the min-version gate
		// must be incapable of firing here. It used to declare 26.6.0, which meant a
		// mutant that broke min_version could kill this test too — a test that dies for
		// two different guards discriminates neither (targeted mutation).
		f := newUpdFixture(t, "26.7.0", "", v1) // manifest points at OLDER 26.7.0
		target := writeTarget(t, v2)            // installed is NEWER 26.8.0
		// Blocked without --force-rollback.
		out, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", dataDir)
		// The premise this subtest rests on: the plan read the version ACTUALLY installed
		// at the target. Nothing asserted this before, which is how `current: dev` — this
		// process's own version, for a binary it never asked — went unnoticed.
		if !strings.Contains(out, "current:   26.8.0") {
			t.Fatalf("the plan must report the version installed AT THE TARGET (26.8.0), got:\n%s", out)
		}
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "anti-rollback") {
			t.Fatalf("downgrade must be blocked by anti-rollback, got %v", err)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.8.0") {
			t.Fatalf("blocked rollback must not swap: %q", got)
		}
		// Allowed with --force-rollback, and audited.
		_, err = runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64, "--force-rollback",
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", dataDir)
		if err != nil {
			t.Fatalf("forced rollback: %v", err)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.7.0") {
			t.Fatalf("forced rollback should install the older version: %q", got)
		}
		audit, _ := os.ReadFile(filepath.Join(dataDir, "upgrade-audit.log"))
		if !strings.Contains(string(audit), "force-rollback") || !strings.Contains(string(audit), "from=26.8.0") {
			t.Fatalf("forced rollback must be audited, log:\n%s", audit)
		}
	})

	t.Run("broken new binary auto-rolls-back to the previous", func(t *testing.T) {
		// The artifact is SHA-valid and signed, but the installed binary fails its
		// post-swap self-check. atomicSwap must restore the previous binary.
		sentinel := filepath.Join(t.TempDir(), "probe-once")
		broken := buildFlakyStub(t, sentinel)
		// NO min_version, for the same isolation reason as the anti-rollback subtest.
		f := newUpdFixture(t, "26.8.0", "", broken)
		target := writeTarget(t, v1)
		_, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", t.TempDir())
		// The needle was "roll", which ALSO matches "anti-rollback" and "--force-rollback":
		// this assertion would have accepted the anti-rollback gate's refusal as proof of
		// an auto-rollback that never happened. Match the post-swap revert specifically
		// (same class as the failure that opened this session).
		if err == nil || !strings.Contains(err.Error(), "rolled back to the previous binary") {
			t.Fatalf("a broken new binary must auto-roll-back with a clear error, got %v", err)
		}
		// The target still runs the PREVIOUS (working) binary — never left broken.
		if got := runsVersion(t, target); !strings.Contains(got, "26.7.0") {
			t.Fatalf("after auto-rollback the target must run the previous binary, got %q", got)
		}
	})

	t.Run("min_version gate refuses too-old a jump", func(t *testing.T) {
		f := newUpdFixture(t, "26.9.0", "26.8.0", v2) // requires >= 26.8.0
		target := writeTarget(t, v1)                  // installed 26.7.0 < 26.8.0
		out, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", t.TempDir())
		// This subtest was GREEN before for the wrong reason: the gate fired against
		// "dev" (the zero version, below every minimum), not against the 26.7.0 the
		// comment above claims is installed. Pin the premise so it can only pass for the
		// stated reason. The move is also strictly FORWARD (26.7.0 -> 26.9.0), so
		// anti-rollback cannot fire here and this test discriminates by guard.
		if !strings.Contains(out, "current:   26.7.0") {
			t.Fatalf("the min_version gate must fire against the INSTALLED 26.7.0, got:\n%s", out)
		}
		if err == nil || !strings.Contains(err.Error(), "minimum current version") {
			t.Fatalf("too-old jump must be refused by min_version, got %v", err)
		}
		if strings.Contains(err.Error(), "REFUSING to downgrade") {
			t.Fatalf("min_version must refuse on its OWN terms, not anti-rollback's: %v", err)
		}
	})

	t.Run("no key fails closed", func(t *testing.T) {
		f := newUpdFixture(t, "26.8.0", "26.6.0", v2)
		target := writeTarget(t, v1)
		// No --pubkey and a community/test build embeds no release key.
		_, err := runUpgradeCmd(t, "--endpoint", f.server.URL,
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", t.TempDir())
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "key") {
			t.Fatalf("no verification key must fail closed, got %v", err)
		}
	})
}

// TestUpgradeRefusesAnUnknownInstalledVersion is the regression: when the
// updater cannot establish what is installed at --target, it must REFUSE rather than
// substitute its own main.version.
//
// The measured behavior before the fix, with a manifest carrying NO min_version:
// the probe failed, "dev" became the current version, "dev" parses to the ZERO
// version, so Compare(26.7.0, 0) = +1 and a 26.8.0 -> 26.7.0 DOWNGRADE was not a
// rollback at all. It installed with exit 0, printed "status: upgrade available",
// and wrote NOTHING to the audit log — anti-rollback, a security control, failed
// OPEN exactly when it could not see. A signed-but-old release from a stale or
// hostile mirror was therefore installable on any host where the target binary
// cannot be executed: a noexec mount, a cross-arch binary staged with --os/--arch,
// or a previous install that left the file non-executable.
func TestUpgradeRefusesAnUnknownInstalledVersion(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build stub binaries")
	}
	v1 := buildStub(t, "26.7.0")
	v2 := buildStub(t, "26.8.0")

	// breakProbe makes the target unreadable-as-a-program while leaving it writable,
	// which is what a noexec mount or a botched install looks like to execProbe.
	breakProbe := func(t *testing.T, target string) {
		t.Helper()
		if err := os.Chmod(target, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("an unprobeable target is refused, not guessed", func(t *testing.T) {
		dataDir := t.TempDir()
		f := newUpdFixture(t, "26.7.0", "", v1) // OLDER than what is installed, no min_version
		target := writeTarget(t, v2)            // installed 26.8.0
		breakProbe(t, target)

		out, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", dataDir)
		if err == nil {
			t.Fatalf("a downgrade decided against an UNKNOWN current version must refuse, got success:\n%s", out)
		}
		// Cause AND way out, both named: a fail-closed with no exit is a wall, not a guard.
		for _, want := range []string{"cannot establish the version installed at", "REFUSING:", "--current-version"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal must name %q; got: %v", want, err)
			}
		}
		// It must never report a version it did not read.
		if strings.Contains(out, "current:   dev") {
			t.Errorf("the plan reported this process's own version for a target it never asked:\n%s", out)
		}
		if !strings.Contains(out, "current:   UNKNOWN") {
			t.Errorf("an unknown version must print as unknown, got:\n%s", out)
		}
		// And nothing may have moved.
		if got := runsVersionMode(t, target); !strings.Contains(got, "26.8.0") {
			t.Errorf("a refused upgrade must not swap: %q", got)
		}
		if b, _ := filepath.Glob(target + ".bak-*"); len(b) != 0 {
			t.Errorf("a refused upgrade must not leave a backup: %v", b)
		}
		if audit, _ := os.ReadFile(filepath.Join(dataDir, "upgrade-audit.log")); len(audit) != 0 {
			t.Errorf("nothing was installed, so nothing may be audited: %q", audit)
		}
	})

	t.Run("--current-version keeps anti-rollback armed", func(t *testing.T) {
		// The legitimate case the fail-closed must not break: the operator declares what
		// is installed, and the guard then does its job on real numbers.
		f := newUpdFixture(t, "26.7.0", "", v1)
		target := writeTarget(t, v2)
		breakProbe(t, target)

		_, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64, "--current-version", "26.8.0",
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", t.TempDir())
		// NOT the needle "anti-rollback": the unknown-version refusal added by this same
		// change contains that word too ("anti-rollback and the minimum-version gate are
		// both claims ABOUT the installed version"), so matching it would let THIS test
		// pass on the refusal that means the flag was ignored. Match the downgrade refusal
		// itself. Caught by the panel — the defect class this session exists to kill,
		// re-introduced by its own fix.
		if err == nil || !strings.Contains(err.Error(), "REFUSING to downgrade") || !strings.Contains(err.Error(), "--force-rollback") {
			t.Fatalf("a declared current version must ARM anti-rollback, not bypass it; got %v", err)
		}
	})

	t.Run("a declaration may not override a working probe", func(t *testing.T) {
		// The escape hatch must not be a bypass. Before this was fixed, the declaration was
		// consulted BEFORE the probe, so --current-version won over a target that answered
		// perfectly well: declaring 26.0.0 against a real 26.8.0 installed a 26.7.0
		// downgrade with exit 0 and no audit line — an UNAUDITED anti-rollback bypass,
		// strictly more powerful than the audited --force-rollback beside it.
		dataDir := t.TempDir()
		f := newUpdFixture(t, "26.7.0", "", v1) // OLDER
		target := writeTarget(t, v2)            // installed 26.8.0, and the probe WORKS

		out, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
			"--current-version", "26.0.0", // a claim the box contradicts
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", dataDir)
		if err == nil {
			t.Fatalf("a declaration contradicting a working probe must refuse, got success:\n%s", out)
		}
		if !strings.Contains(err.Error(), "refusing to act on a declaration the target contradicts") {
			t.Fatalf("the refusal must name the contradiction, got %v", err)
		}
		if got := runsVersionMode(t, target); !strings.Contains(got, "26.8.0") {
			t.Fatalf("nothing may be installed: %q", got)
		}
		if audit, _ := os.ReadFile(filepath.Join(dataDir, "upgrade-audit.log")); len(audit) != 0 {
			t.Fatalf("nothing was installed, so nothing may be audited: %q", audit)
		}
	})

	t.Run("a declared version is marked as declared, in the plan and in the audit", func(t *testing.T) {
		// A record that cannot tell a measurement from an operator's claim is not evidence.
		dataDir := t.TempDir()
		f := newUpdFixture(t, "26.7.0", "", v1)
		target := writeTarget(t, v2)
		breakProbe(t, target)

		out, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
			"--current-version", "26.8.0", "--force-rollback",
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", dataDir)
		if err != nil {
			t.Fatalf("an audited forced rollback on a declared version must proceed: %v", err)
		}
		if !strings.Contains(out, "declared with --current-version, not measured") {
			t.Fatalf("the plan must not render a claim as if it were a reading:\n%s", out)
		}
		audit, _ := os.ReadFile(filepath.Join(dataDir, "upgrade-audit.log"))
		if !strings.Contains(string(audit), "from=26.8.0(declared)") {
			t.Fatalf("the audit record must mark a declared from-version, got:\n%s", audit)
		}
	})

	t.Run("an unknown version makes no ordering claim in the plan either", func(t *testing.T) {
		// The status line is the same lie in a different row: with CurrentKnown false both
		// IsUpToDate and IsRollback are false, so every unknown fell into the default arm
		// and printed "upgrade available" two lines under the word UNKNOWN.
		f := newUpdFixture(t, "26.8.0", "26.6.0", v2)
		target := writeTarget(t, v2) // SAME version: genuinely up to date
		breakProbe(t, target)

		out, _ := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
			"--target", target, "--os", "linux", "--arch", "amd64", "--check", "--data-dir", t.TempDir())
		if strings.Contains(out, "status:    upgrade available") {
			t.Errorf("an unknown current version must not be reported as an available upgrade:\n%s", out)
		}
		if !strings.Contains(out, "status:    UNKNOWN") {
			t.Errorf("the status line must say it cannot tell:\n%s", out)
		}
		if !strings.Contains(out, "min_ver:   26.6.0 (NOT CHECKABLE") {
			t.Errorf("the min_ver line must not imply it was checked:\n%s", out)
		}
	})

	t.Run("a whitespace --target is not this binary", func(t *testing.T) {
		// resolveTargetBinary tested the RAW flag while targetIsSelf trimmed it, so
		// `--target "  "` resolved to a path AND was treated as the running executable —
		// re-opening the exact main.version substitution this session removed.
		f := newUpdFixture(t, "26.8.0", "", v2)
		_, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
			"--target", "   ", "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", t.TempDir())
		if err == nil {
			t.Fatal("a whitespace --target names no readable binary; it must not be silently answered for")
		}
		if !strings.Contains(err.Error(), "--target was given but is blank") {
			t.Fatalf("a blank --target must be rejected as blank, got %v", err)
		}
		// The specific regression: it must never be answered with THIS build's stamp, which
		// is what the raw-vs-trimmed disagreement between resolveTargetBinary and
		// targetIsSelf produced.
		if strings.Contains(err.Error(), "built from source without a version stamp") {
			t.Fatalf("a whitespace --target was answered with THIS build's stamp: %v", err)
		}
	})

	t.Run("a malformed declaration is an error, not a shrug", func(t *testing.T) {
		f := newUpdFixture(t, "26.8.0", "", v2)
		target := writeTarget(t, v1)
		breakProbe(t, target)
		for _, bad := range []string{"26.8", "banana", "   "} {
			_, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
				"--current-version", bad,
				"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", t.TempDir())
			if err == nil {
				t.Errorf("--current-version %q must not be accepted", bad)
			}
		}
	})

	t.Run("--current-version allows a genuine forward move", func(t *testing.T) {
		f := newUpdFixture(t, "26.8.0", "26.6.0", v2)
		target := writeTarget(t, v1)
		breakProbe(t, target)

		if _, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64, "--current-version", "26.7.0",
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", t.TempDir()); err != nil {
			t.Fatalf("a declared 26.7.0 -> 26.8.0 is a legitimate upgrade: %v", err)
		}
		if got := runsVersionMode(t, target); !strings.Contains(got, "26.8.0") {
			t.Fatalf("target not upgraded: %q", got)
		}
	})

	t.Run("an unstamped declaration is not a version", func(t *testing.T) {
		f := newUpdFixture(t, "26.8.0", "", v2)
		target := writeTarget(t, v1)
		breakProbe(t, target)

		_, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64, "--current-version", "dev",
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "not a version, it is the absence of one") {
			t.Fatalf("declaring \"dev\" must not satisfy the guard, got %v", err)
		}
	})

	t.Run("a target that reports dev is unknown, not ancient", func(t *testing.T) {
		// The other half of the unification: an UNSTAMPED build is the same missing fact
		// as an unprobeable one. Here the probe SUCCEEDS and answers "dev" — before
		// that was the zero version, so a min_version of 26.6.0 refused it as TOO OLD,
		// which is the precise contradiction version.go's header used to enshrine.
		f := newUpdFixture(t, "26.8.0", "26.6.0", v2)
		target := writeTarget(t, buildStub(t, "dev"))

		_, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", t.TempDir())
		if err == nil {
			t.Fatal("an unstamped target has no position in the ordering; it must be refused")
		}
		if !strings.Contains(err.Error(), "cannot establish the version installed at") {
			t.Fatalf("an unstamped build must be refused as UNKNOWN, got %v", err)
		}
		if strings.Contains(err.Error(), "minimum current version") {
			t.Fatalf("an unstamped build must not be called TOO OLD — it has no position in the ordering: %v", err)
		}
	})
}

// runsVersionMode is runsVersion for a target whose mode may have been broken by the
// test: it restores exec permission first, because the question here is which BINARY
// is installed, not whether the fixture left it runnable.
func runsVersionMode(t *testing.T, bin string) string {
	t.Helper()
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	return runsVersion(t, bin)
}

// TestUpgradeInstallTimer proves the systemd generator emits enable-able units.
func TestUpgradeInstallTimer(t *testing.T) {
	dir := t.TempDir()
	out, err := runUpgradeCmd(t, "--install-timer", "--timer-dir", dir, "--channel", "security", "--target", "/opt/olivares/olivares")
	if err != nil {
		t.Fatalf("install-timer: %v", err)
	}
	svc, err := os.ReadFile(filepath.Join(dir, "olivares-upgrade.service"))
	if err != nil {
		t.Fatalf("service not written: %v", err)
	}
	tmr, err := os.ReadFile(filepath.Join(dir, "olivares-upgrade.timer"))
	if err != nil {
		t.Fatalf("timer not written: %v", err)
	}
	if !strings.Contains(string(svc), "--if-eligible") || !strings.Contains(string(svc), "--channel security") {
		t.Fatalf("service must run a rollout-aware upgrade on the chosen channel:\n%s", svc)
	}
	if !strings.Contains(string(tmr), "OnCalendar=") || !strings.Contains(string(tmr), "WantedBy=timers.target") {
		t.Fatalf("timer must be a schedulable unit:\n%s", tmr)
	}
	if !strings.Contains(out, "systemctl enable") {
		t.Fatalf("expected enable instructions, got:\n%s", out)
	}
}

// compile-time guard: the command constructor stays a *cobra.Command.
var _ = func() *cobra.Command { return newUpgradeCmd() }

// requireValidLicense answers each commercial state with a next step, and the GRACE arm is
// the one that changed: it used to say "the installed license is not live (status grace)" —
// a state and no action, shown to someone whose renewal charge had just bounced. Refusing is
// still correct (fetching a new enterprise binary is not the way out of a lapsed term); what
// was missing was telling them which way out is.
func TestRequireValidLicenseAnswersEachState(t *testing.T) {
	base := time.Now().UTC().Add(-48 * time.Hour)

	write := func(t *testing.T, c license.Claims) string {
		t.Helper()
		dir := t.TempDir()
		blob, err := license.Sign(c, license.DevPrivateKey())
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if err := os.WriteFile(licenseDataDirPath(dir), []byte(blob+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	t.Run("valid proceeds", func(t *testing.T) {
		dir := write(t, license.Claims{Licensee: "Acme", IssuedAt: base, ExpiresAt: time.Now().UTC().Add(24 * time.Hour)})
		if _, err := requireValidLicense("", dir); err != nil {
			t.Fatalf("a live license must proceed: %v", err)
		}
	})

	t.Run("grace refuses, naming the term end and the way out", func(t *testing.T) {
		expires := time.Now().UTC().Add(-time.Hour)
		dir := write(t, license.Claims{
			Licensee: "Acme", IssuedAt: base, ExpiresAt: expires, GraceUntil: expires.Add(license.MaxGracePeriod),
		})
		_, err := requireValidLicense("", dir)
		if err == nil {
			t.Fatal("a license inside its grace window must not authorize an enterprise upgrade")
		}
		for _, want := range []string{"GRACE", "settle the renewal", "keeps running"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("grace message %q does not mention %q", err.Error(), want)
			}
		}
	})

	t.Run("termless refuses as expired", func(t *testing.T) {
		dir := write(t, license.Claims{Licensee: "Acme", IssuedAt: base}) // no ExpiresAt
		_, err := requireValidLicense("", dir)
		if err == nil || !strings.Contains(err.Error(), "EXPIRED") {
			t.Fatalf("a termless license must refuse as expired, got %v", err)
		}
	})
}
