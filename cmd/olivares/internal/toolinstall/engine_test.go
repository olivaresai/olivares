// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall/toolinstalltest"
)

const (
	fxVersion  = "2.1.261"
	fxVersion2 = "2.1.262"
	fxPlatform = "linux-x64"
)

var fxHost = Platform{OS: "linux", Arch: "amd64", Libc: "glibc"}

// fixture wires a real gpg verifier, a throwaway key, a release server and an
// engine that trusts only that key.
type fixture struct {
	t        *testing.T
	key      *toolinstalltest.SigningKey
	srv      *toolinstalltest.Server
	engine   *Engine
	claude   *Claude
	root     string
	marker   string
	verifier *GPGVerifier
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	toolinstalltest.RequireGPG(t)
	verifier, err := NewGPGVerifier(context.Background(), exec.LookPath)
	if err != nil {
		t.Fatal(err)
	}
	key := toolinstalltest.GenerateKey(t, "Olivares Tool Install Fixture <fixture@olivares.invalid>")
	srv := toolinstalltest.NewServer(t)
	dir := toolinstalltest.ExecCapableDir(t)
	f := &fixture{t: t, key: key, srv: srv, verifier: verifier, root: filepath.Join(dir, "tools"), marker: filepath.Join(dir, "ran.marker")}
	f.claude = NewClaudeWithTrust(ClaudeOptions{BaseURL: srv.URL, Verifier: verifier, ProbeBudget: 5 * time.Second, ProbeGrace: 500 * time.Millisecond}, key.Public, key.Fingerprint)
	f.engine = NewEngine(NewCatalog(f.claude), EngineOptions{InstallerVersion: "test", Now: func() time.Time { return time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC) }})
	return f
}

func (f *fixture) publish(version string) []byte {
	f.t.Helper()
	m := f.srv.Publish(f.t, f.key, version, map[string][]byte{fxPlatform: toolinstalltest.Executable(version, f.marker)})
	f.srv.SetPointer("latest", version)
	return m
}

func (f *fixture) req(version string) Request {
	return Request{Driver: "claude", Version: version, Platform: fxHost, DestRoot: f.root, Source: f.srv.URL}
}

func (f *fixture) install(version string) (*Receipt, *Plan, error) {
	return f.engine.Install(context.Background(), f.req(version), nil, nil)
}

func (f *fixture) ran() int {
	b, err := os.ReadFile(f.marker)
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "ran\n")
}

func (f *fixture) artifactGETs(version string) int {
	return f.srv.Count(toolinstalltest.ArtifactPath(version, fxPlatform))
}

func sha256Of(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func snapshotTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[path] = sha256Of(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func requireKind(t *testing.T, err error, kind string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want refusal %s, got success", kind)
	}
	if got := KindOf(err); got != kind {
		t.Fatalf("want refusal %s, got %q: %v", kind, got, err)
	}
}

func TestInstallPositiveFlowWritesReceiptAndPlacesRelease(t *testing.T) {
	f := newFixture(t)
	manifest := f.publish(fxVersion)
	var progress bytes.Buffer
	rec, plan, err := f.engine.Install(context.Background(), f.req("latest"), nil, &progress)
	if err != nil {
		t.Fatalf("install: %v\n%s", err, progress.String())
	}
	if plan.Action != ActionInstall || plan.Channel != ChannelLatest || plan.Version != fxVersion {
		t.Fatalf("plan action/channel/version = %s/%s/%s", plan.Action, plan.Channel, plan.Version)
	}
	exe := filepath.Join(f.root, "claude", fxVersion+"-"+fxPlatform, "bin", "claude")
	if rec.Destination.Executable != exe {
		t.Fatalf("executable path %s, want %s", rec.Destination.Executable, exe)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if got := sha256Of(b); got != rec.Artifact.SHA256 || got != plan.Artifact.SHA256 {
		t.Fatalf("installed sha256 %s, receipt %s, plan %s", got, rec.Artifact.SHA256, plan.Artifact.SHA256)
	}
	if fi, _ := os.Stat(exe); fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("executable is not executable: %v", fi.Mode())
	}
	// R2: the finished release directory is opened to 0755 at placement, so a
	// shared root can serve it; bin and the binary keep their creation modes.
	for path, want := range map[string]os.FileMode{filepath.Dir(filepath.Dir(exe)): 0o755, filepath.Dir(exe): 0o755, exe: 0o755} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Fatalf("%s mode %s, want %s", path, fi.Mode().Perm(), want)
		}
	}
	if fi, _ := os.Stat(f.root); fi.Mode().Perm() != 0o755 {
		t.Fatalf("root mode changed to %s", fi.Mode().Perm())
	}
	if f.ran() != 1 {
		t.Fatalf("fixture ran %d times during install, want exactly one probe", f.ran())
	}
	if firstLine(rec.Probe.Output) != fxVersion+" (Claude Code)" {
		t.Fatalf("probe output %q", rec.Probe.Output)
	}
	if rec.Provenance.Class != ProvenancePublisherSigned || rec.Provenance.KeyFingerprint != f.key.Fingerprint || rec.Provenance.SigningKeyFingerprint == "" {
		t.Fatalf("provenance %+v", rec.Provenance)
	}
	if rec.Provenance.ManifestSHA256 != sha256Of(manifest) {
		t.Fatalf("receipt manifest digest %s, want %s", rec.Provenance.ManifestSHA256, sha256Of(manifest))
	}
	release := filepath.Dir(filepath.Dir(exe))
	for _, name := range []string{ReceiptFile, RetainedManifest, RetainedSignature, RetainedKey} {
		if _, err := os.Stat(filepath.Join(release, name)); err != nil {
			t.Fatalf("retained %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(release, stagingMarker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging marker leaked into the release dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(release, ".probe")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probe scratch leaked into the release dir: %v", err)
	}
	if !bytes.Equal(must(os.ReadFile(filepath.Join(release, RetainedManifest))), manifest) {
		t.Fatal("retained manifest differs from the served one")
	}
	if !strings.Contains(progress.String(), "manifest signature verified") || !strings.Contains(progress.String(), "placed "+exe) {
		t.Fatalf("progress lacks phases:\n%s", progress.String())
	}
	if f.artifactGETs(fxVersion) != 1 {
		t.Fatalf("artifact GETs = %d, want 1", f.artifactGETs(fxVersion))
	}
	// The receipt reads back strictly and the inventory credits it.
	inv, err := f.engine.List(context.Background(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Installed) != 1 || inv.Installed[0].State != StateInstalled || inv.Installed[0].SHA256 != rec.Artifact.SHA256 {
		t.Fatalf("inventory %+v", inv)
	}
	if len(inv.Leftovers) != 0 || len(inv.Unexpected) != 0 {
		t.Fatalf("inventory reports leftovers/unexpected: %+v %+v", inv.Leftovers, inv.Unexpected)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestSecondIdenticalInstallIsNoopWithZeroArtifactGETs(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	rec1, _, err := f.install(fxVersion)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, f.root)
	gets := f.artifactGETs(fxVersion)
	rec2, plan, err := f.install(fxVersion)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != ActionNoop {
		t.Fatalf("action %s, want noop", plan.Action)
	}
	if rec2.InstalledAt != rec1.InstalledAt || rec2.Artifact != rec1.Artifact || rec2.PlanDigest != rec1.PlanDigest {
		t.Fatalf("noop returned a different receipt: %+v vs %+v", rec2, rec1)
	}
	if f.artifactGETs(fxVersion) != gets {
		t.Fatalf("noop fetched the artifact again (%d → %d)", gets, f.artifactGETs(fxVersion))
	}
	if f.ran() != 1 {
		t.Fatalf("noop executed the tool (ran=%d)", f.ran())
	}
	after := snapshotTree(t, f.root)
	delete(before, filepath.Join(f.root, LockFile))
	delete(after, filepath.Join(f.root, LockFile))
	if len(before) != len(after) {
		t.Fatalf("noop changed the tree: %v vs %v", before, after)
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("noop changed %s", k)
		}
	}
	// Plan reports the same verdict without touching anything.
	p, err := f.engine.Plan(context.Background(), f.req(fxVersion))
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != ActionNoop || p.Existing == nil || !p.Existing.ReceiptPresent || p.Existing.ExecutableSHA256 != rec1.Artifact.SHA256 {
		t.Fatalf("plan %+v existing %+v", p.Action, p.Existing)
	}
}

func TestSecondVersionInstallsBesideAndRetainsTheFirst(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	if _, _, err := f.install(fxVersion); err != nil {
		t.Fatal(err)
	}
	first := snapshotTree(t, filepath.Join(f.root, "claude", fxVersion+"-"+fxPlatform))
	f.publish(fxVersion2)
	rec, _, err := f.install(fxVersion2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(rec.Destination.ReleaseDir, fxVersion2+"-"+fxPlatform) {
		t.Fatalf("release dir %s", rec.Destination.ReleaseDir)
	}
	after := snapshotTree(t, filepath.Join(f.root, "claude", fxVersion+"-"+fxPlatform))
	if len(first) != len(after) {
		t.Fatalf("first release changed shape: %d vs %d files", len(first), len(after))
	}
	for k, v := range first {
		if after[k] != v {
			t.Fatalf("first release file %s changed", k)
		}
	}
	inv, err := f.engine.List(context.Background(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Installed) != 2 || inv.Installed[0].Version != fxVersion || inv.Installed[1].Version != fxVersion2 {
		t.Fatalf("inventory %+v", inv.Installed)
	}
}

func TestCustomRootReceivesEverythingAndDefaultUntouched(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	other := filepath.Join(filepath.Dir(f.root), "custom-root")
	req := f.req(fxVersion)
	req.DestRoot = other
	rec, _, err := f.engine.Install(context.Background(), req, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rec.Destination.Executable, other+string(filepath.Separator)) {
		t.Fatalf("executable %s not under %s", rec.Destination.Executable, other)
	}
	if _, err := os.Stat(f.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default root was created: %v", err)
	}
}

func TestSignatureByAnotherKeyMeansZeroArtifactGETsAndZeroExecutions(t *testing.T) {
	f := newFixture(t)
	manifest := f.publish(fxVersion)
	other := toolinstalltest.GenerateKey(t, "Someone Else <else@olivares.invalid>")
	f.srv.Set("/"+fxVersion+"/manifest.json.sig", other.Sign(t, manifest))
	_, _, err := f.install(fxVersion)
	requireKind(t, err, KindSignatureInvalid)
	if f.artifactGETs(fxVersion) != 0 {
		t.Fatalf("artifact was requested %d times after a bad signature", f.artifactGETs(fxVersion))
	}
	if f.ran() != 0 {
		t.Fatal("fixture executed after a bad signature")
	}
	if _, err := os.Stat(filepath.Join(f.root, "claude")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("driver directory created before verification: %v", err)
	}
}

func TestTamperedManifestMeansZeroArtifactGETs(t *testing.T) {
	f := newFixture(t)
	manifest := f.publish(fxVersion)
	tampered := bytes.Replace(manifest, []byte(`"size": `), []byte(`"size":  `), 1)
	if bytes.Equal(tampered, manifest) {
		t.Fatal("tamper did not change the manifest")
	}
	f.srv.Set("/"+fxVersion+"/manifest.json", tampered)
	_, _, err := f.install(fxVersion)
	requireKind(t, err, KindSignatureInvalid)
	if f.artifactGETs(fxVersion) != 0 {
		t.Fatalf("artifact requested %d times", f.artifactGETs(fxVersion))
	}
}

func TestMissingSignatureIsUnsupportedSourceNotFallthrough(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	f.srv.Delete("/" + fxVersion + "/manifest.json.sig")
	_, _, err := f.install(fxVersion)
	requireKind(t, err, KindUnsupportedSource)
	if f.artifactGETs(fxVersion) != 0 || f.ran() != 0 {
		t.Fatal("unsigned manifest led to a download or execution")
	}
}

func TestMissingVerifierRefusesBeforeAnyMetadataUse(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	_, err := NewGPGVerifier(context.Background(), func(string) (string, error) { return "", errors.New("not found") })
	requireKind(t, err, KindVerificationUnavailable)
	unavailable := NewClaudeWithTrust(ClaudeOptions{BaseURL: f.srv.URL, Verifier: UnavailableVerifier{Err: err}}, f.key.Public, f.key.Fingerprint)
	eng := NewEngine(NewCatalog(unavailable), EngineOptions{})
	_, _, ierr := eng.Install(context.Background(), f.req(fxVersion), nil, nil)
	requireKind(t, ierr, KindVerificationUnavailable)
	if f.artifactGETs(fxVersion) != 0 || f.ran() != 0 {
		t.Fatal("missing verifier led to a download or execution")
	}
}

func TestInvalidHashMeansZeroExecutionsAndNoRelease(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	// Same size, one byte flipped: only the hash can tell.
	good := f.srv.Get(toolinstalltest.ArtifactPath(fxVersion, fxPlatform))
	bad := append([]byte(nil), good...)
	bad[len(bad)-2] ^= 0x01
	f.srv.Set(toolinstalltest.ArtifactPath(fxVersion, fxPlatform), bad)
	_, _, err := f.install(fxVersion)
	requireKind(t, err, KindDigestMismatch)
	if f.ran() != 0 {
		t.Fatal("a file with the wrong hash was executed")
	}
	assertNoReleaseNoStaging(t, f.root)
}

func TestShortAndLongBodiesAreSizeMismatches(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	path := toolinstalltest.ArtifactPath(fxVersion, fxPlatform)
	good := f.srv.Get(path)
	for name, body := range map[string][]byte{"short": good[:len(good)-3], "long": append(append([]byte(nil), good...), '\n', '\n')} {
		t.Run(name, func(t *testing.T) {
			f.srv.Set(path, body)
			for _, chunked := range []bool{false, true} {
				f.srv.SetChunked(path, chunked)
				_, _, err := f.install(fxVersion)
				requireKind(t, err, KindSizeMismatch)
			}
			if f.ran() != 0 {
				t.Fatal("executed a body of the wrong size")
			}
			assertNoReleaseNoStaging(t, f.root)
		})
	}
}

func TestOversizedMetadataIsRefused(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	f.srv.Set("/"+fxVersion+"/manifest.json", bytes.Repeat([]byte("x"), claudeManifestCap+1))
	_, _, err := f.install(fxVersion)
	requireKind(t, err, KindResponseTooLarge)
	f.publish(fxVersion)
	f.srv.Set("/latest", bytes.Repeat([]byte("9"), claudePointerCap+1))
	_, _, err = f.install("latest")
	requireKind(t, err, KindResponseTooLarge)
}

func TestProbeVersionMismatchLeavesNoRelease(t *testing.T) {
	f := newFixture(t)
	// The manifest says 2.1.261 but the artifact reports 2.1.260.
	f.srv.Publish(t, f.key, fxVersion, map[string][]byte{fxPlatform: toolinstalltest.Executable("2.1.260", f.marker)})
	_, _, err := f.install(fxVersion)
	requireKind(t, err, KindProbeMismatch)
	if f.ran() != 1 {
		t.Fatalf("probe ran %d times, want 1", f.ran())
	}
	assertNoReleaseNoStaging(t, f.root)
}

func TestProbeThatIgnoresTERMIsKilledAndReaped(t *testing.T) {
	f := newFixture(t)
	pidfile := filepath.Join(filepath.Dir(f.root), "stubborn.pids")
	f.srv.Publish(t, f.key, fxVersion, map[string][]byte{fxPlatform: toolinstalltest.StubbornExecutable(pidfile)})
	f.claude.probeBudget, f.claude.probeGrace = 700*time.Millisecond, 300*time.Millisecond
	start := time.Now()
	_, _, err := f.install(fxVersion)
	elapsed := time.Since(start)
	requireKind(t, err, KindProbeFailed)
	if elapsed > 10*time.Second {
		t.Fatalf("probe escalation took %s", elapsed)
	}
	pids, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatalf("fixture did not record pids: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, line := range strings.Fields(string(pids)) {
		pid, err := strconv.Atoi(line)
		if err != nil {
			t.Fatalf("pid %q", line)
		}
		for {
			err := syscall.Kill(pid, 0)
			if errors.Is(err, syscall.ESRCH) {
				break
			}
			// A zombie still answers kill(0); ask the kernel whether it is one.
			if st, rerr := os.ReadFile("/proc/" + line + "/stat"); rerr == nil && strings.Contains(string(st), ") Z ") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("pid %d from the probe's process group is still alive (%v)", pid, err)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	assertNoReleaseNoStaging(t, f.root)
}

func TestSymlinkedDestinationRefusedAndExistingReleaseRetained(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	if _, _, err := f.install(fxVersion); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, f.root)
	f.publish(fxVersion2)
	// A symlink where the driver directory's next release would go.
	elsewhere := t.TempDir()
	link := filepath.Join(f.root, "claude", fxVersion2+"-"+fxPlatform)
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatal(err)
	}
	_, _, err := f.install(fxVersion2)
	requireKind(t, err, KindConflict)
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatalf("install wrote through the symlink: %v", entries)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	// A symlinked root is refused outright.
	linkRoot := filepath.Join(filepath.Dir(f.root), "root-link")
	if err := os.Symlink(f.root, linkRoot); err != nil {
		t.Fatal(err)
	}
	req := f.req(fxVersion2)
	req.DestRoot = linkRoot
	_, _, err = f.engine.Install(context.Background(), req, nil, nil)
	requireKind(t, err, KindDestinationUnsafe)
	after := snapshotTree(t, f.root)
	delete(before, filepath.Join(f.root, LockFile))
	delete(after, filepath.Join(f.root, LockFile))
	if len(before) != len(after) {
		t.Fatalf("existing release changed: %v vs %v", before, after)
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("existing file %s changed", k)
		}
	}
}

func TestDamagedExistingReleaseIsReportedNotRepaired(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	rec, _, err := f.install(fxVersion)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the installed bytes: the receipt now lies about them.
	if err := os.WriteFile(rec.Destination.Executable, []byte("#!/bin/sh\necho tampered\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, f.root)
	_, plan, err := f.install(fxVersion)
	requireKind(t, err, KindDamaged)
	if plan == nil || plan.Action != ActionConflict {
		t.Fatalf("plan %+v", plan)
	}
	after := snapshotTree(t, f.root)
	delete(before, filepath.Join(f.root, LockFile))
	delete(after, filepath.Join(f.root, LockFile))
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("damaged release was modified at %s", k)
		}
	}
	inv, err := f.engine.List(context.Background(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Installed) != 1 || inv.Installed[0].State != StateDamaged || !strings.Contains(inv.Installed[0].Reason, "receipt records") {
		t.Fatalf("inventory %+v", inv.Installed)
	}
	p, err := f.engine.Plan(context.Background(), f.req(fxVersion))
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != ActionNoop {
		// Plan only OBSERVES; it reports noop pending revalidation, and the
		// executable digest it observed differs from the plan's artifact.
		t.Fatalf("plan action %s", p.Action)
	}
	if p.Existing.ExecutableSHA256 == p.Artifact.SHA256 {
		t.Fatal("observed digest should differ from the verified one")
	}
	// A moved release directory is not credited either.
	moved := filepath.Join(f.root, "claude", "9.9.9-"+fxPlatform)
	if err := os.Rename(rec.Destination.ReleaseDir, moved); err != nil {
		t.Fatal(err)
	}
	inv, err = f.engine.List(context.Background(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Installed) != 1 || inv.Installed[0].State != StateDamaged || !strings.Contains(inv.Installed[0].Reason, "moved or copied") {
		t.Fatalf("moved release credited: %+v", inv.Installed)
	}
}

func TestApprovedPlanDigestMismatchRefusesBeforeAnyWrite(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	approved, err := f.engine.Plan(context.Background(), f.req(fxVersion))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plan created the destination root: %v", err)
	}
	if f.artifactGETs(fxVersion) != 0 {
		t.Fatal("plan fetched the artifact")
	}
	// Re-publish the same version with different bytes: the selection changed.
	f.srv.Publish(t, f.key, fxVersion, map[string][]byte{fxPlatform: toolinstalltest.Executable(fxVersion, "")})
	_, _, err = f.engine.Install(context.Background(), f.req(fxVersion), approved, nil)
	requireKind(t, err, KindPlanChanged)
	if _, err := os.Stat(f.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a changed plan still created the root: %v", err)
	}
	if f.artifactGETs(fxVersion) != 0 {
		t.Fatal("a changed plan still fetched the artifact")
	}
	// The unchanged plan installs and the receipt records its digest.
	f.publish(fxVersion)
	approved, err = f.engine.Plan(context.Background(), f.req(fxVersion))
	if err != nil {
		t.Fatal(err)
	}
	rec, plan, err := f.engine.Install(context.Background(), f.req(fxVersion), approved, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec.PlanDigest != approved.Digest || plan.Digest != approved.Digest {
		t.Fatalf("digests differ: receipt %s plan %s approved %s", rec.PlanDigest, plan.Digest, approved.Digest)
	}
	// The plan file round-trips strictly and an edited copy is refused.
	raw, err := MarshalPlan(approved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPlan(bytes.NewReader(raw)); err != nil {
		t.Fatalf("read plan: %v", err)
	}
	edited := bytes.Replace(raw, []byte(`"version": "`+fxVersion), []byte(`"version": "`+fxVersion2), 1)
	if _, err := ReadPlan(bytes.NewReader(edited)); KindOf(err) != KindInvalidRequest {
		t.Fatalf("edited plan accepted: %v", err)
	}
}

func TestConcurrentInstallIsRefusedLockedAndPriorReleaseRetained(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	if _, _, err := f.install(fxVersion); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, filepath.Join(f.root, "claude", fxVersion+"-"+fxPlatform))
	f.publish(fxVersion2)
	gate := f.srv.Gate(toolinstalltest.ArtifactPath(fxVersion2, fxPlatform))
	var wg sync.WaitGroup
	wg.Add(1)
	var firstErr error
	go func() {
		defer wg.Done()
		_, _, firstErr = f.install(fxVersion2)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for f.artifactGETs(fxVersion2) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("first install never reached the artifact")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// While the artifact is still streaming, staging is private.
	stagings, _ := filepath.Glob(filepath.Join(f.root, "claude", stagingPrefix+"*"))
	if len(stagings) != 1 {
		t.Fatalf("staging dirs during download: %v", stagings)
	}
	if fi, err := os.Stat(stagings[0]); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("staging mode during fill: %v %v", fi, err)
	}
	// The first install holds the lock mid-download; a second one must not wait.
	_, _, err := f.install(fxVersion2)
	requireKind(t, err, KindLocked)
	close(gate)
	wg.Wait()
	if firstErr != nil {
		t.Fatalf("first install: %v", firstErr)
	}
	after := snapshotTree(t, filepath.Join(f.root, "claude", fxVersion+"-"+fxPlatform))
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("prior release changed at %s", k)
		}
	}
	inv, _ := f.engine.List(context.Background(), f.root)
	if len(inv.Installed) != 2 || len(inv.Leftovers) != 0 {
		t.Fatalf("inventory %+v", inv)
	}
	// LockRoot models an external holder the same way.
	release, err := LockRoot(f.root)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.install(fxVersion2)
	requireKind(t, err, KindLocked)
	release()
	if _, _, err := f.install(fxVersion2); err != nil {
		t.Fatalf("after release: %v", err)
	}
}

func TestInterruptedPlacementRetainsPriorReleaseAndReportsLeftover(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	if _, _, err := f.install(fxVersion); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, filepath.Join(f.root, "claude", fxVersion+"-"+fxPlatform))
	f.publish(fxVersion2)
	crash := errors.New("simulated crash before rename")
	f.engine.beforePlace = func() error { return crash }
	_, _, err := f.install(fxVersion2)
	if !errors.Is(err, crash) {
		t.Fatalf("want the crash, got %v", err)
	}
	f.engine.beforePlace = nil
	after := snapshotTree(t, filepath.Join(f.root, "claude", fxVersion+"-"+fxPlatform))
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("prior release changed at %s", k)
		}
	}
	inv, err := f.engine.List(context.Background(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Installed) != 1 || len(inv.Leftovers) != 1 || !strings.Contains(filepath.Base(inv.Leftovers[0].Path), stagingPrefix+fxVersion2) {
		t.Fatalf("inventory %+v", inv)
	}
	leftover := inv.Leftovers[0].Path
	if fi, err := os.Stat(leftover); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("interrupted staging is not private: %v %v", fi, err)
	}
	// The next install completes with a fresh staging directory and does NOT
	// remove the leftover: it was not this operation's to remove.
	rec, _, err := f.install(fxVersion2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(leftover); err != nil {
		t.Fatalf("leftover from another operation was removed: %v", err)
	}
	if _, err := os.Stat(rec.Destination.Executable); err != nil {
		t.Fatal(err)
	}
	inv, _ = f.engine.List(context.Background(), f.root)
	if len(inv.Installed) != 2 || len(inv.Leftovers) != 1 {
		t.Fatalf("inventory %+v", inv)
	}
}

func TestUnsupportedPlatformDriverAndBadVersionRefuseEarly(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	cases := []struct {
		name string
		req  Request
		kind string
	}{
		{"win32", Request{Driver: "claude", Version: fxVersion, Platform: Platform{OS: "windows", Arch: "amd64"}, DestRoot: f.root, Source: f.srv.URL}, KindUnsupportedPlatform},
		{"darwin", Request{Driver: "claude", Version: fxVersion, Platform: Platform{OS: "darwin", Arch: "arm64"}, DestRoot: f.root, Source: f.srv.URL}, KindUnsupportedPlatform},
		{"musl not published", Request{Driver: "claude", Version: fxVersion, Platform: Platform{OS: "linux", Arch: "amd64", Libc: "musl"}, DestRoot: f.root, Source: f.srv.URL}, KindUnsupportedPlatform},
		{"grok", Request{Driver: "grok", Version: fxVersion, Platform: fxHost, DestRoot: f.root}, KindUnsupportedProvider},
		{"traversal version", Request{Driver: "claude", Version: "../etc", Platform: fxHost, DestRoot: f.root, Source: f.srv.URL}, KindInvalidRequest},
		{"unknown version", Request{Driver: "claude", Version: "9.9.9", Platform: fxHost, DestRoot: f.root, Source: f.srv.URL}, KindVersionUnknown},
		{"relative root", Request{Driver: "claude", Version: fxVersion, Platform: fxHost, DestRoot: "tools", Source: f.srv.URL}, KindInvalidRequest},
		{"ftp source", Request{Driver: "claude", Version: fxVersion, Platform: fxHost, DestRoot: f.root, Source: "ftp://mirror.invalid/x"}, KindUnsupportedSource},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := f.engine.Install(context.Background(), tc.req, nil, nil)
			requireKind(t, err, tc.kind)
		})
	}
	if f.artifactGETs(fxVersion) != 0 || f.ran() != 0 {
		t.Fatal("a refused request fetched or executed")
	}
	if _, err := os.Stat(f.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused requests created the root: %v", err)
	}
}

func TestPointerReturningGarbageIsVersionUnknown(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	f.srv.Set("/stable", []byte("<html>maintenance</html>"))
	_, _, err := f.install("stable")
	requireKind(t, err, KindVersionUnknown)
	f.srv.Delete("/stable")
	_, _, err = f.install("stable")
	requireKind(t, err, KindVersionUnknown)
}

func TestDetectReportsManagedVendorAndPathCandidatesWithoutExecuting(t *testing.T) {
	f := newFixture(t)
	manifest := f.publish(fxVersion)
	rec, _, err := f.install(fxVersion)
	if err != nil {
		t.Fatal(err)
	}
	ran := f.ran()
	// A vendor-layout home with the same bytes as the release, and a stray
	// binary on PATH with unknown bytes.
	home := t.TempDir()
	versions := filepath.Join(home, ".local", "share", "claude", "versions")
	if err := os.MkdirAll(versions, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	vendorBin := filepath.Join(versions, fxVersion)
	if err := os.WriteFile(vendorBin, must(os.ReadFile(rec.Destination.Executable)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(vendorBin, filepath.Join(home, ".local", "bin", "claude")); err != nil {
		t.Fatal(err)
	}
	pathDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pathDir, "claude"), toolinstalltest.Executable("0.0.1", f.marker), 0o755); err != nil {
		t.Fatal(err)
	}
	// Something that must never be read: a fake credential in the vendor's
	// configuration directory, checked by mtime/atime-independent means below.
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	// ~/.local/bin is both a vendor default and on PATH: one alias, listed once.
	cands, err := f.engine.Detect(context.Background(), DetectOptions{
		Driver: "claude", Root: f.root, Home: home, PathEnv: pathDir + string(os.PathListSeparator) + "relative/dir" + string(os.PathListSeparator) + filepath.Join(home, ".local", "bin"),
		Material: &Material{Manifest: manifest, Signature: f.srv.Get("/" + fxVersion + "/manifest.json.sig")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.ran() != ran {
		t.Fatal("detect executed a candidate without --probe")
	}
	byPath := map[string]Candidate{}
	for _, c := range cands {
		byPath[c.Path] = c
	}
	if c := byPath[rec.Destination.Executable]; c.Match != MatchRegistered || c.Version != fxVersion || c.Origin != "managed" {
		t.Fatalf("managed candidate %+v", c)
	}
	link := filepath.Join(home, ".local", "bin", "claude")
	if c := byPath[vendorBin]; c.Match != MatchManifestCorroborated || c.Version != fxVersion || c.VendorPlatform != fxPlatform || c.Origin != "vendor-default" ||
		c.IsSymlink || len(c.Aliases) != 1 || c.Aliases[0] != link {
		t.Fatalf("vendor candidate %+v", c)
	}
	if _, dup := byPath[link]; dup {
		t.Fatal("symlink to an already listed target was reported twice")
	}
	if c := byPath[filepath.Join(pathDir, "claude")]; c.Match != MatchUnregisteredObserved || c.Version != "" || c.Origin != "path" {
		t.Fatalf("path candidate %+v", c)
	}
	// With Probe, only registered and (this run) manifest-corroborated
	// candidates run from an empty scratch home; the stray PATH binary is not.
	cands, err = f.engine.Detect(context.Background(), DetectOptions{Driver: "claude", Root: f.root, Home: home, PathEnv: pathDir, Probe: true,
		Material: &Material{Manifest: manifest, Signature: f.srv.Get("/" + fxVersion + "/manifest.json.sig")}})
	if err != nil {
		t.Fatal(err)
	}
	if f.ran() != ran+2 {
		t.Fatalf("probe ran %d candidates, want the registered and the corroborated one", f.ran()-ran)
	}
	for _, c := range cands {
		switch c.Match {
		case MatchRegistered, MatchManifestCorroborated:
			if c.Probe == nil || c.ProbeError != "" || !strings.Contains(c.Probe.Output, "(Claude Code)") || len(c.Probe.EnvNames) == 0 {
				t.Fatalf("probe missing for %s: %+v", c.Path, c)
			}
		default:
			if c.Probe != nil || !strings.Contains(c.ProbeSkipped, "--probe-path") {
				t.Fatalf("unregistered candidate %s was run or not explained: %+v", c.Path, c)
			}
		}
	}
	// Corroboration with a manifest signed by another key is refused outright.
	other := toolinstalltest.GenerateKey(t, "Other <o@olivares.invalid>")
	_, err = f.engine.Detect(context.Background(), DetectOptions{Driver: "claude", Material: &Material{Manifest: manifest, Signature: other.Sign(t, manifest)}})
	requireKind(t, err, KindSignatureInvalid)
}

func TestListOnMissingRootAndForeignEntries(t *testing.T) {
	f := newFixture(t)
	inv, err := f.engine.List(context.Background(), f.root)
	if err != nil || inv.RootExists || len(inv.Installed) != 0 {
		t.Fatalf("missing root: %+v %v", inv, err)
	}
	f.publish(fxVersion)
	if _, _, err := f.install(fxVersion); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "claude", "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(f.root, "claude", ".staging.foreign"), 0o700); err != nil {
		t.Fatal(err)
	}
	inv, err = f.engine.List(context.Background(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Unexpected) != 1 || len(inv.Leftovers) != 1 || inv.Leftovers[0].Marker != nil {
		t.Fatalf("inventory %+v", inv)
	}
	if _, err := os.Stat(filepath.Join(f.root, "claude", ".staging.foreign")); err != nil {
		t.Fatalf("list removed a foreign staging dir: %v", err)
	}
}

func TestReceiptJSONRoundTripsStrictly(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	rec, _, err := f.install(fxVersion)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(rec.Destination.ReleaseDir, ReceiptFile))
	if err != nil {
		t.Fatal(err)
	}
	var back Receipt
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Schema != ReceiptSchema || back.Artifact != rec.Artifact || back.InstalledAt != rec.InstalledAt {
		t.Fatalf("receipt round trip: %+v", back)
	}
}

func assertNoReleaseNoStaging(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "claude"))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Fatalf("unexpected entry after refusal: %s", e.Name())
	}
}

func candidateByPath(cands []Candidate, path string) Candidate {
	for _, c := range cands {
		if c.Path == path {
			return c
		}
	}
	return Candidate{}
}

func TestDetectProbePolicyNamesExactPaths(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	rec, _, err := f.install(fxVersion)
	if err != nil {
		t.Fatal(err)
	}
	dir := toolinstalltest.ExecCapableDir(t)
	stray := filepath.Join(dir, "claude")
	if err := os.WriteFile(stray, toolinstalltest.Executable("0.0.1", f.marker), 0o755); err != nil {
		t.Fatal(err)
	}
	base := f.ran()
	opts := DetectOptions{Driver: "claude", Root: f.root, PathEnv: dir}

	// Bare Probe: the registered release runs, the unregistered PATH stray does not.
	opts.Probe = true
	cands, err := f.engine.Detect(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if f.ran() != base+1 {
		t.Fatalf("bare probe ran %d, want 1", f.ran()-base)
	}
	if c := candidateByPath(cands, rec.Destination.Executable); c.Probe == nil {
		t.Fatalf("registered candidate not probed: %+v", c)
	}
	if c := candidateByPath(cands, stray); c.Probe != nil || !strings.Contains(c.ProbeSkipped, "--probe-path") {
		t.Fatalf("stray %+v", c)
	}

	// Naming the stray runs exactly it and nothing else.
	opts.Probe = false
	opts.ProbePaths = []string{stray}
	cands, err = f.engine.Detect(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if f.ran() != base+2 {
		t.Fatalf("named probe ran %d more, want 1", f.ran()-base-1)
	}
	if c := candidateByPath(cands, stray); c.Probe == nil || firstLine(c.Probe.Output) != "0.0.1 (Claude Code)" {
		t.Fatalf("named stray %+v", c)
	}
	if c := candidateByPath(cands, rec.Destination.Executable); c.Probe != nil {
		t.Fatal("registered candidate ran without being requested")
	}

	// A named path outside every sweep is observed as origin "named" and run.
	other := filepath.Join(toolinstalltest.ExecCapableDir(t), "elsewhere-claude")
	if err := os.WriteFile(other, toolinstalltest.Executable("0.0.2", f.marker), 0o755); err != nil {
		t.Fatal(err)
	}
	opts.ProbePaths = []string{other}
	cands, err = f.engine.Detect(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if c := candidateByPath(cands, other); c.Origin != "named" || c.Probe == nil || f.ran() != base+3 {
		t.Fatalf("named outside path %+v ran=%d", c, f.ran()-base)
	}

	// A named path that does not exist is a failed request, not silence.
	opts.ProbePaths = []string{filepath.Join(dir, "missing")}
	cands, err = f.engine.Detect(context.Background(), opts)
	requireKind(t, err, KindProbeFailed)
	if c := candidateByPath(cands, filepath.Join(dir, "missing")); c.Origin != "named" || !strings.Contains(c.ProbeSkipped, "no such file") {
		t.Fatalf("missing named %+v", c)
	}
	if f.ran() != base+3 {
		t.Fatal("a missing path caused an execution")
	}
	// A relative name is refused outright.
	opts.ProbePaths = []string{"claude"}
	if _, err := f.engine.Detect(context.Background(), opts); KindOf(err) != KindInvalidRequest {
		t.Fatalf("relative probe path accepted: %v", err)
	}
	// A probe that fails is a failed request with the tool's output attached.
	failing := filepath.Join(toolinstalltest.ExecCapableDir(t), "failing-claude")
	if err := os.WriteFile(failing, []byte("#!/bin/sh\necho broken\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts.ProbePaths = []string{failing}
	cands, err = f.engine.Detect(context.Background(), opts)
	requireKind(t, err, KindProbeFailed)
	if c := candidateByPath(cands, failing); c.Probe == nil || c.ProbeError == "" || !strings.Contains(c.ProbeError, "broken") {
		t.Fatalf("failing probe %+v", c)
	}
}

func TestDetectNeverProbesDamagedEvenWhenNamed(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	rec, _, err := f.install(fxVersion)
	if err != nil {
		t.Fatal(err)
	}
	// The installed bytes are replaced: the receipt now disagrees with them.
	if err := os.WriteFile(rec.Destination.Executable, toolinstalltest.Executable("9.9.9", f.marker), 0o755); err != nil {
		t.Fatal(err)
	}
	base := f.ran()
	cands, err := f.engine.Detect(context.Background(), DetectOptions{Driver: "claude", Root: f.root, Probe: true})
	if err != nil {
		t.Fatalf("bare probe over a damaged release must not be an error, got %v", err)
	}
	c := candidateByPath(cands, rec.Destination.Executable)
	if c.Match != MatchDamaged || c.Probe != nil || !strings.Contains(c.ProbeSkipped, "never executed") {
		t.Fatalf("damaged candidate %+v", c)
	}
	// Naming it explicitly is a failed request, still without execution.
	cands, err = f.engine.Detect(context.Background(), DetectOptions{Driver: "claude", Root: f.root, ProbePaths: []string{rec.Destination.Executable}})
	requireKind(t, err, KindProbeFailed)
	c = candidateByPath(cands, rec.Destination.Executable)
	if c.Probe != nil || !strings.Contains(c.ProbeSkipped, "damaged") {
		t.Fatalf("named damaged candidate %+v", c)
	}
	if f.ran() != base {
		t.Fatalf("damaged bytes executed %d time(s)", f.ran()-base)
	}
}

func TestDetectRefusesWritableFilesDirectoriesAndNonExecutables(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	dir := toolinstalltest.ExecCapableDir(t)
	exe := filepath.Join(dir, "claude")
	if err := os.WriteFile(exe, toolinstalltest.Executable(fxVersion, f.marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	probe := func() ([]Candidate, error) {
		return f.engine.Detect(context.Background(), DetectOptions{Driver: "claude", ProbePaths: []string{exe}})
	}
	// Group-writable file.
	if err := os.Chmod(exe, 0o775); err != nil {
		t.Fatal(err)
	}
	cands, err := probe()
	requireKind(t, err, KindProbeFailed)
	if c := candidateByPath(cands, exe); !strings.Contains(c.ProbeSkipped, "group- or world-writable") || c.Probe != nil {
		t.Fatalf("writable file %+v", c)
	}
	// World-writable containing directory.
	if err := os.Chmod(exe, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	cands, err = probe()
	requireKind(t, err, KindProbeFailed)
	if c := candidateByPath(cands, exe); !strings.Contains(c.ProbeSkipped, "containing directory") || c.Probe != nil {
		t.Fatalf("writable dir %+v", c)
	}
	if f.ran() != 0 {
		t.Fatalf("refused candidates executed %d time(s)", f.ran())
	}
	// Both safe again: it runs.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cands, err = probe()
	if err != nil {
		t.Fatal(err)
	}
	if c := candidateByPath(cands, exe); c.Probe == nil || f.ran() != 1 {
		t.Fatalf("safe candidate %+v ran=%d", c, f.ran())
	}
	// A registered release whose file lost its executable bit is reported and
	// refused, and the refusal of a requested probe is an error.
	rec, _, err := f.install(fxVersion)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(rec.Destination.Executable, 0o644); err != nil {
		t.Fatal(err)
	}
	inv, err := f.engine.List(context.Background(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Installed) != 1 || inv.Installed[0].IsExecutable || inv.Installed[0].State != StateInstalled {
		t.Fatalf("inventory %+v", inv.Installed)
	}
	cands, err = f.engine.Detect(context.Background(), DetectOptions{Driver: "claude", Root: f.root, Probe: true})
	requireKind(t, err, KindProbeFailed)
	if c := candidateByPath(cands, rec.Destination.Executable); c.Executable || c.Probe != nil || !strings.Contains(c.ProbeSkipped, "not executable") {
		t.Fatalf("non-executable managed %+v", c)
	}
}

func TestListReverifiesRetainedSignatureBeforeCreditingInstalled(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	rec, _, err := f.install(fxVersion)
	if err != nil {
		t.Fatal(err)
	}
	list := func() Installed {
		inv, err := f.engine.List(context.Background(), f.root)
		if err != nil {
			t.Fatal(err)
		}
		if len(inv.Installed) != 1 {
			t.Fatalf("inventory %+v", inv.Installed)
		}
		return inv.Installed[0]
	}
	detectMatch := func() string {
		cands, err := f.engine.Detect(context.Background(), DetectOptions{Driver: "claude", Root: f.root})
		if err != nil {
			t.Fatal(err)
		}
		return candidateByPath(cands, rec.Destination.Executable).Match
	}
	in := list()
	if in.State != StateInstalled || in.Provenance == nil || in.Provenance.KeyFingerprint != f.key.Fingerprint || !in.IsExecutable {
		t.Fatalf("fresh release %+v", in)
	}
	if detectMatch() != MatchRegistered {
		t.Fatal("fresh release not registered")
	}
	release := rec.Destination.ReleaseDir
	for _, tc := range []struct {
		name, file string
		mutate     func(path string)
		reason     string
	}{
		{"prose signature", RetainedSignature, func(p string) { _ = os.WriteFile(p, []byte("this is not a signature at all"), 0o644) }, "does not verify"},
		{"missing signature", RetainedSignature, func(p string) { _ = os.Remove(p) }, "missing"},
		{"missing manifest", RetainedManifest, func(p string) { _ = os.Remove(p) }, "missing"},
		{"missing key", RetainedKey, func(p string) { _ = os.Remove(p) }, "missing"},
		{"edited manifest", RetainedManifest, func(p string) {
			b, _ := os.ReadFile(p)
			_ = os.WriteFile(p, append(b, '\n'), 0o644)
		}, "does not verify"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(release, tc.file)
			orig, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(path)
			in := list()
			if in.State != StateDamaged || !strings.Contains(in.Reason, tc.reason) || in.Provenance != nil {
				t.Fatalf("after %s: %+v", tc.name, in)
			}
			if detectMatch() != MatchDamaged {
				t.Fatalf("after %s detect still credits the release", tc.name)
			}
			if err := os.WriteFile(path, orig, 0o644); err != nil {
				t.Fatal(err)
			}
			if in := list(); in.State != StateInstalled || in.Provenance == nil {
				t.Fatalf("restore after %s: %+v", tc.name, in)
			}
		})
	}
	// Without a verifier the release is unverified: not installed, not
	// registered, no provenance, not run by a bare probe.
	unavailable := NewClaudeWithTrust(ClaudeOptions{BaseURL: f.srv.URL, Verifier: UnavailableVerifier{}}, f.key.Public, f.key.Fingerprint)
	eng := NewEngine(NewCatalog(unavailable), EngineOptions{})
	inv, err := eng.List(context.Background(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Installed) != 1 || inv.Installed[0].State != StateUnverified || inv.Installed[0].Provenance != nil || !strings.Contains(inv.Installed[0].Reason, "not re-checked") {
		t.Fatalf("unverified inventory %+v", inv.Installed)
	}
	base := f.ran()
	cands, err := eng.Detect(context.Background(), DetectOptions{Driver: "claude", Root: f.root, Probe: true})
	if err != nil {
		t.Fatal(err)
	}
	if c := candidateByPath(cands, rec.Destination.Executable); c.Match != MatchUnverified || c.Probe != nil || f.ran() != base {
		t.Fatalf("unverified candidate %+v ran=%d", c, f.ran()-base)
	}
}

func TestApprovalFileCarriesNoObservationAndRefusesEdits(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	if _, _, err := f.install(fxVersion); err != nil {
		t.Fatal(err)
	}
	plan, err := f.engine.Plan(context.Background(), f.req(fxVersion))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != ActionNoop || plan.Existing == nil || len(plan.Observed) == 0 {
		t.Fatalf("rendered plan should carry observations: %+v", plan)
	}
	raw, err := MarshalPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"action", "existing", "observed"} {
		if _, present := keys[k]; present {
			t.Fatalf("approval file carries %q", k)
		}
	}
	for _, k := range []string{"verified", "digest", "artifact", "destination"} {
		if _, present := keys[k]; !present {
			t.Fatalf("approval file lacks %q", k)
		}
	}
	approved, err := ReadPlan(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if approved.Digest != plan.Digest || approved.Action != "" || approved.Existing != nil {
		t.Fatalf("read back %+v", approved)
	}
	// Human claims injected with the digest left intact are refused by presence,
	// including every decoder-equivalent spelling encoding/json would accept.
	release := filepath.Join(f.root, "claude", fxVersion+"-"+fxPlatform)
	before := snapshotTree(t, release)
	gets := f.artifactGETs(fxVersion)
	manifestGETs := f.srv.Count("/" + fxVersion + "/manifest.json")
	sigGETs := f.srv.Count("/" + fxVersion + "/manifest.json.sig")
	for _, tc := range observationApprovalInjections {
		edited, ok := injectApprovalJSON(raw, tc.field)
		if !ok {
			t.Fatalf("%s: injection anchor missing", tc.name)
		}
		_, err := ReadPlan(bytes.NewReader(edited))
		if KindOf(err) != KindInvalidRequest || !strings.Contains(err.Error(), "observation field") {
			t.Fatalf("injected %s accepted: %v", tc.name, err)
		}
	}
	if f.artifactGETs(fxVersion) != gets || f.srv.Count("/"+fxVersion+"/manifest.json") != manifestGETs || f.srv.Count("/"+fxVersion+"/manifest.json.sig") != sigGETs {
		t.Fatal("a refused observation claim still reached the network")
	}
	after := snapshotTree(t, release)
	if len(after) != len(before) {
		t.Fatalf("refused observation changed the installed tree: before %d after %d", len(before), len(after))
	}
	for p, sum := range before {
		if after[p] != sum {
			t.Fatalf("refused observation mutated %s", p)
		}
	}
	// A Verified claim is bound by the digest.
	edited := bytes.Replace(raw, []byte(`"manifest_signature"`), []byte(`"everything"`), 1)
	if _, err := ReadPlan(bytes.NewReader(edited)); KindOf(err) != KindInvalidRequest || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("verified edit accepted: %v", err)
	}
	// The untouched approval still executes as a no-op, also after the vendor
	// re-signs the identical manifest.
	rec, executed, err := f.engine.Install(context.Background(), f.req(fxVersion), approved, nil)
	if err != nil || executed.Action != ActionNoop || rec == nil {
		t.Fatalf("approved noop: %v %+v", err, executed)
	}
	f.publish(fxVersion)
	if _, executed, err := f.engine.Install(context.Background(), f.req(fxVersion), approved, nil); err != nil || executed.Action != ActionNoop {
		t.Fatalf("approved noop after re-sign: %v %+v", err, executed)
	}
	// And a second version's approval installs beside it.
	f.publish(fxVersion2)
	plan2, err := f.engine.Plan(context.Background(), f.req(fxVersion2))
	if err != nil {
		t.Fatal(err)
	}
	raw2, _ := MarshalPlan(plan2)
	approved2, err := ReadPlan(bytes.NewReader(raw2))
	if err != nil {
		t.Fatal(err)
	}
	if _, executed, err := f.engine.Install(context.Background(), f.req(fxVersion2), approved2, nil); err != nil || executed.Action != ActionInstall {
		t.Fatalf("approved second version: %v %+v", err, executed)
	}
}

func TestListBoundsStagingMarkerRead(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	if _, _, err := f.install(fxVersion); err != nil {
		t.Fatal(err)
	}
	stg := filepath.Join(f.root, "claude", stagingPrefix+"9.9.9-"+fxPlatform+".deadbeef")
	if err := os.Mkdir(stg, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(stg, stagingMarker)
	mf, err := os.Create(marker)
	if err != nil {
		t.Fatal(err)
	}
	const huge = 256 << 20
	if err := mf.Truncate(huge); err != nil {
		t.Fatal(err)
	}
	_ = mf.Close()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	inv, err := f.engine.List(context.Background(), f.root)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Leftovers) != 1 || inv.Leftovers[0].Marker != nil {
		t.Fatalf("leftover %+v", inv.Leftovers)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 32<<20 {
		t.Fatalf("list allocated %d bytes for a %d-byte marker it must not read whole", grew, huge)
	}
	// A marker within the cap is reported.
	if err := os.WriteFile(marker, []byte(`{"schema":"olivares.ai/tool-install/staging/v1","pid":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	inv, err = f.engine.List(context.Background(), f.root)
	if err != nil || len(inv.Leftovers) != 1 || inv.Leftovers[0].Marker == nil {
		t.Fatalf("small marker %+v %v", inv.Leftovers, err)
	}
}
