// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall/toolinstalltest"
)

const codexFxVersion = "0.153.4"

func TestCodexProbePresentAbsentOld(t *testing.T) {
	dir := toolinstalltest.ExecCapableDir(t)
	c := NewCodex(CodexOptions{Verifier: HashMatchingVerifier{}, ProbeBudget: 5 * time.Second, ProbeGrace: 500 * time.Millisecond})
	present := filepath.Join(dir, "codex")
	if err := os.WriteFile(present, toolinstalltest.VersionReporter("Codex", codexFxVersion, ""), 0o755); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(dir, "scratch")
	if _, err := c.Probe(context.Background(), present, scratch, codexFxVersion); err != nil {
		t.Fatalf("present: %v", err)
	}
	if _, err := c.Probe(context.Background(), present, scratch, "9.9.9"); KindOf(err) != KindProbeMismatch {
		t.Fatalf("old: %v", err)
	}
	if _, err := c.Probe(context.Background(), filepath.Join(dir, "missing"), scratch, codexFxVersion); KindOf(err) != KindProbeFailed {
		t.Fatalf("absent: %v", err)
	}
}

func TestCodexOriginInstallRequiresVerifier(t *testing.T) {
	s := newTLSMap(t)
	pkg, pointer, sums := publishCodexPackage(t, s, codexFxVersion)
	_ = pkg
	dir := toolinstalltest.ExecCapableDir(t)
	root := filepath.Join(dir, "tools")
	c := NewCodex(CodexOptions{BaseURL: s.url(), Client: s.client(), ProbeBudget: 5 * time.Second, ProbeGrace: 500 * time.Millisecond})
	cat, err := NewCapabilityCatalog(NewCatalog(), c)
	if err != nil {
		t.Fatal(err)
	}
	eng := NewEngineWithCapabilities(cat, EngineOptions{InstallerVersion: "test"})
	req := RequestV2{Driver: DriverCodex, Version: codexFxVersion, Platform: PlatformV2{OS: "linux", Arch: "amd64", Libc: "musl"}, DestRoot: root, Source: s.url()}
	_, _, err = eng.InstallV2(context.Background(), req, nil, nil)
	if KindOf(err) != KindVerificationUnavailable {
		t.Fatalf("production without verifier: %v", err)
	}
	// A refusal that leaves a release directory behind would be read as an
	// install by list, by detect and by the session-runtime pin. The refusal has
	// to leave the destination as it found it: no release directory, and no
	// staging directory either.
	release := filepath.Join(root, DriverCodex, codexFxVersion+"-x86_64-unknown-linux-musl")
	if fi, err := os.Stat(release); err == nil {
		t.Fatalf("a refused install left a release directory: %s (dir=%t)", release, fi.IsDir())
	}
	leftovers, err := filepath.Glob(filepath.Join(root, DriverCodex, ".staging.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("a refused install left staging behind: %v", leftovers)
	}
	_ = pointer
	_ = sums
}

func TestCodexInstallWithHashMatchingVerifier(t *testing.T) {
	s := newTLSMap(t)
	_, _, _ = publishCodexPackage(t, s, codexFxVersion)
	dir := toolinstalltest.ExecCapableDir(t)
	root := filepath.Join(dir, "tools")
	c := NewCodex(CodexOptions{BaseURL: s.url(), Client: s.client(), Verifier: HashMatchingVerifier{}, ProbeBudget: 5 * time.Second, ProbeGrace: 500 * time.Millisecond})
	cat, err := NewCapabilityCatalog(NewCatalog(), c)
	if err != nil {
		t.Fatal(err)
	}
	eng := NewEngineWithCapabilities(cat, EngineOptions{InstallerVersion: "test", Now: func() time.Time {
		return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	}})
	req := RequestV2{Driver: DriverCodex, Version: codexFxVersion, Platform: PlatformV2{OS: "linux", Arch: "amd64", Libc: "musl"}, DestRoot: root, Source: s.url()}
	rec, plan, err := eng.InstallV2(context.Background(), req, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec.VerificationKind != VerificationSigstoreCosign || plan.Selection.PackagePolicyID != PackagePolicyCodexMixedV1 {
		t.Fatalf("receipt %+v", rec)
	}
	inv, err := eng.List(context.Background(), root)
	if err != nil || len(inv.Installed) != 1 || inv.Installed[0].State != StateInstalled {
		t.Fatalf("list %+v %v", inv, err)
	}
}

func publishCodexPackage(t *testing.T, s *tlsMapServer, version string) (pkg, pointer, sums []byte) {
	t.Helper()
	vendor := "x86_64-unknown-linux-musl"
	files := map[string][]byte{
		"bin/codex":                   toolinstalltest.VersionReporter("Codex", version, ""),
		"bin/codex-code-mode-host":    []byte("host-fixture\n"),
		"codex-package.json":          []byte(`{"name":"codex"}` + "\n"),
		"codex-path/rg":               []byte("rg\n"),
		"codex-resources/bwrap":       []byte("bwrap\n"),
		"codex-resources/zsh/bin/zsh": []byte("zsh\n"),
	}
	pkg = tarGz(t, files)
	pkgSum := sha256.Sum256(pkg)
	subjects := []map[string]string{
		subjectJSON("bin/codex", files["bin/codex"], "codex"),
		subjectJSON("bin/codex-code-mode-host", files["bin/codex-code-mode-host"], "codex-code-mode-host"),
		subjectJSON("codex-resources/bwrap", files["codex-resources/bwrap"], "bwrap"),
	}
	doc := map[string]any{
		"version": version,
		"package": map[string]any{
			"file":   "codex-package-" + vendor + ".tar.gz",
			"sha256": hex.EncodeToString(pkgSum[:]),
			"size":   len(pkg),
		},
		"subjects": subjects,
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	sums = []byte(fmt.Sprintf("%s %d %s\n", hex.EncodeToString(pkgSum[:]), len(pkg), "codex-package-"+vendor+".tar.gz"))
	s.put("/"+version+"/pointer.json", raw)
	s.put("/"+version+"/SHA256SUMS", sums)
	s.put("/"+version+"/codex-package-"+vendor+".tar.gz", pkg)
	s.put("/"+version+"/codex.sigstore", []byte("proof-codex\n"))
	s.put("/"+version+"/codex-code-mode-host.sigstore", []byte("proof-host\n"))
	s.put("/"+version+"/bwrap.sigstore", []byte("proof-bwrap\n"))
	return pkg, raw, sums
}

func subjectJSON(path string, body []byte, role string) map[string]string {
	sum := sha256.Sum256(body)
	return map[string]string{
		"path": path, "sha256": hex.EncodeToString(sum[:]),
		"identity": "https://github.com/openai/codex", "issuer": "https://token.actions.githubusercontent.com",
		"proof_role": role, "proof_file": role + ".sigstore",
	}
}

func tarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// Canonical member order matches the closed layout regulars; dirs are created on extract.
	order := []string{
		"bin/codex", "bin/codex-code-mode-host", "codex-package.json",
		"codex-path/rg", "codex-resources/bwrap", "codex-resources/zsh/bin/zsh",
	}
	for _, name := range order {
		b := files[name]
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(b)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
