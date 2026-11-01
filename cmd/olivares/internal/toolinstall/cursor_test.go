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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall/toolinstalltest"
)

// The shape of the official package and install script, measured on 2026.10.22-27dcf5f
// (cursor.com/install, downloads.cursor.com/lab/<version>/linux/x64/agent-cli-package.tar.gz):
// one top directory, regular files and directories only, a `cursor-agent` launcher beside
// its own `node`, `index.js` and native modules, and no checksum or signature anywhere.
const cursorFxVersion = "2026.10.22-27dcf5f"

func cursorFxScript(version string) []byte {
	return []byte("#!/usr/bin/env bash\nset -euo pipefail\nOS=\"$(uname -s)\"\nARCH=\"$(uname -m)\"\n" +
		"DOWNLOAD_URL=\"https://downloads.cursor.com/lab/" + version + "/${OS}/${ARCH}/agent-cli-package.tar.gz\"\n")
}

type cursorFxEntry struct {
	name string
	body string
	mode int64
	typ  byte
	link string
}

func cursorFxMembers(version string) []cursorFxEntry {
	launcher := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo " + version + "; exit 0; fi\nexit 0\n"
	return []cursorFxEntry{
		{name: "dist-package/", typ: tar.TypeDir, mode: 0o755},
		{name: "dist-package/cursor-agent", body: launcher, mode: 0o755},
		{name: "dist-package/node", body: "#!/bin/sh\nexit 0\n", mode: 0o755},
		{name: "dist-package/index.js", body: "// bundle\n", mode: 0o644},
		{name: "dist-package/node_modules/", typ: tar.TypeDir, mode: 0o755},
		{name: "dist-package/node_modules/pkg.json", body: "{}", mode: 0o644},
	}
}

func cursorFxTarball(t *testing.T, members []cursorFxEntry) []byte {
	t.Helper()
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	for _, m := range members {
		typ := m.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		hdr := &tar.Header{Name: m.name, Typeflag: typ, Mode: m.mode, Linkname: m.link}
		if typ == tar.TypeReg {
			hdr.Size = int64(len(m.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(m.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func cursorFxEngine(t *testing.T, s *tlsMapServer) (*Engine, string) {
	t.Helper()
	dir := toolinstalltest.ExecCapableDir(t)
	c := NewCursor(CursorOptions{ScriptURL: s.url() + "/install", PackageBase: s.url() + "/lab", Client: s.client(), ProbeBudget: 5 * time.Second, ProbeGrace: 500 * time.Millisecond})
	cat, err := NewCapabilityCatalog(NewCatalog(), c)
	if err != nil {
		t.Fatal(err)
	}
	eng := NewEngineWithCapabilities(cat, EngineOptions{InstallerVersion: "test", Now: func() time.Time {
		return time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	}})
	recordChildren(eng)
	stubLandlock(t)
	return eng, filepath.Join(dir, "tools")
}

func cursorFxServe(t *testing.T, members []cursorFxEntry) (*tlsMapServer, []byte) {
	t.Helper()
	s := newTLSMap(t)
	tarball := cursorFxTarball(t, members)
	s.put("/install", cursorFxScript(cursorFxVersion))
	s.put("/lab/"+cursorFxVersion+"/linux/x64/agent-cli-package.tar.gz", tarball)
	return s, tarball
}

func cursorFxRequest(root string) RequestV2 {
	return RequestV2{Driver: DriverCursor, Version: ChannelLatest, Platform: PlatformV2{OS: "linux", Arch: "amd64"}, DestRoot: root}
}

func TestCursorDetectSearchesPATHForTheOfficialAgentProgram(t *testing.T) {
	s, _ := cursorFxServe(t, cursorFxMembers(cursorFxVersion))
	eng, _ := cursorFxEngine(t, s)
	dir := t.TempDir()
	program := filepath.Join(dir, "agent")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	candidates, err := eng.Detect(context.Background(), DetectOptions{Driver: DriverCursor, PathEnv: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate.Path == program {
			return
		}
	}
	t.Fatalf("PATH candidates = %+v, want the official agent at %s", candidates, program)
}

// The version comes from the official install script's own download line; the package
// address is built from the pinned origin, never taken from the script.
func TestCursorPlanReadsTheVersionFromTheOfficialInstallScript(t *testing.T) {
	s, _ := cursorFxServe(t, cursorFxMembers(cursorFxVersion))
	eng, root := cursorFxEngine(t, s)
	plan, err := eng.PlanV2(context.Background(), cursorFxRequest(root))
	if err != nil {
		t.Fatal(err)
	}
	sel := plan.Selection
	if sel.Version != cursorFxVersion || sel.Channel != ChannelLatest || sel.VendorPlatform != "linux-x64" {
		t.Fatalf("selection %+v", sel)
	}
	if want := s.url() + "/lab/" + cursorFxVersion + "/linux/x64/agent-cli-package.tar.gz"; sel.Source.Package.URL != want {
		t.Fatalf("package %q, want %q", sel.Source.Package.URL, want)
	}
	if sel.Verification.Kind != VerificationOriginTarball || sel.PackagePolicyID != PackagePolicyOriginTarballV1 ||
		sel.FetchedObject.DigestState != DigestStateUnknown || sel.Source.Checksums.State != URLStateAbsent {
		t.Fatalf("a tool that publishes no checksum must be planned as origin-only: %+v", sel)
	}
	if sel.Destination.Executable != filepath.Join(root, "cursor", cursorFxVersion+"-linux-x64", "bin", "agent") {
		t.Fatalf("executable %q", sel.Destination.Executable)
	}
}

func TestCursorPlanRefusesAScriptWithoutADownloadLine(t *testing.T) {
	s := newTLSMap(t)
	s.put("/install", []byte("#!/bin/sh\necho hello\n"))
	eng, root := cursorFxEngine(t, s)
	if _, err := eng.PlanV2(context.Background(), cursorFxRequest(root)); KindOf(err) != KindManifestInvalid {
		t.Fatalf("script without a download line: %v", err)
	}
	// A version the script names must be a plain version, not a path.
	s.put("/install", cursorFxScript("../../evil"))
	if _, err := eng.PlanV2(context.Background(), cursorFxRequest(root)); KindOf(err) != KindManifestInvalid {
		t.Fatalf("script with a path for a version: %v", err)
	}
}

func TestCursorInstallPlacesThePackageAndRecordsTheObservedDigest(t *testing.T) {
	s, tarball := cursorFxServe(t, cursorFxMembers(cursorFxVersion))
	eng, root := cursorFxEngine(t, s)
	req := cursorFxRequest(root)
	rec, _, err := eng.InstallV2(context.Background(), req, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(tarball)
	if rec.VerificationKind != VerificationOriginTarball || rec.FetchedObject.SHA256 != hex.EncodeToString(sum[:]) || rec.Version != cursorFxVersion {
		t.Fatalf("receipt %+v", rec)
	}
	release := filepath.Join(root, "cursor", cursorFxVersion+"-linux-x64")
	for path, perm := range map[string]os.FileMode{
		"bin/agent": 0o755, "lib/agent/cursor-agent": 0o755, "lib/agent/node": 0o755,
		"lib/agent/index.js": 0o644, "lib/agent/node_modules/pkg.json": 0o644,
	} {
		fi, err := os.Lstat(filepath.Join(release, path))
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != perm {
			t.Fatalf("%s: %v mode %v, want regular %v", path, err, fi, perm)
		}
	}
	out, err := exec.Command(filepath.Join(release, "bin", "agent"), "--version").Output()
	if err != nil || strings.TrimSpace(string(out)) != cursorFxVersion {
		t.Fatalf("the placed agent runs its launcher: %q %v", out, err)
	}
	inv, err := eng.List(context.Background(), root)
	if err != nil || len(inv.Installed) != 1 || inv.Installed[0].State != StateInstalled {
		t.Fatalf("list: %+v %v", inv, err)
	}
	// Installing the same release again revalidates it against its receipt.
	if _, _, err := eng.InstallV2(context.Background(), req, nil, nil); err != nil {
		t.Fatalf("second install: %v", err)
	}
}

// With no published checksum the receipt is the only record of what was placed: a file
// changed after the install is damage, in the listing and in a second install.
func TestCursorReceiptCatchesAChangedFile(t *testing.T) {
	s, _ := cursorFxServe(t, cursorFxMembers(cursorFxVersion))
	eng, root := cursorFxEngine(t, s)
	req := cursorFxRequest(root)
	if _, _, err := eng.InstallV2(context.Background(), req, nil, nil); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "cursor", cursorFxVersion+"-linux-x64", "lib", "agent", "index.js")
	if err := os.WriteFile(target, []byte("// patched\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inv, err := eng.List(context.Background(), root)
	if err != nil || len(inv.Installed) != 1 || inv.Installed[0].State != StateDamaged {
		t.Fatalf("a changed file must list as damaged: %+v %v", inv, err)
	}
	if _, _, err := eng.InstallV2(context.Background(), req, nil, nil); KindOf(err) != KindDamaged {
		t.Fatalf("second install over a changed file: %v", err)
	}
}

func TestCursorInstallRefusesUnsafePackages(t *testing.T) {
	base := cursorFxMembers(cursorFxVersion)
	for name, extra := range map[string]cursorFxEntry{
		"symlink":       {name: "dist-package/link", typ: tar.TypeSymlink, link: "/etc/passwd"},
		"hardlink":      {name: "dist-package/hard", typ: tar.TypeLink, link: "dist-package/index.js"},
		"traversal":     {name: "dist-package/../escape", body: "x", mode: 0o644},
		"outside root":  {name: "other/file", body: "x", mode: 0o644},
		"absolute path": {name: "/dist-package/abs", body: "x", mode: 0o644},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := cursorFxServe(t, append(append([]cursorFxEntry{}, base...), extra))
			eng, root := cursorFxEngine(t, s)
			if _, _, err := eng.InstallV2(context.Background(), cursorFxRequest(root), nil, nil); KindOf(err) != KindManifestInvalid {
				t.Fatalf("%s: %v", name, err)
			}
			if _, err := os.Lstat(filepath.Join(root, "cursor", cursorFxVersion+"-linux-x64")); err == nil {
				t.Fatalf("%s: a refused package left a release behind", name)
			}
		})
	}
}

func TestCursorInstallRefusesAPackageWithoutItsLauncher(t *testing.T) {
	s, _ := cursorFxServe(t, []cursorFxEntry{{name: "dist-package/", typ: tar.TypeDir, mode: 0o755}, {name: "dist-package/index.js", body: "x", mode: 0o644}})
	eng, root := cursorFxEngine(t, s)
	if _, _, err := eng.InstallV2(context.Background(), cursorFxRequest(root), nil, nil); KindOf(err) != KindManifestInvalid {
		t.Fatalf("package without cursor-agent: %v", err)
	}
}

func TestCursorProbeIdentifiesTheVersionShape(t *testing.T) {
	dir := toolinstalltest.ExecCapableDir(t)
	c := NewCursor(CursorOptions{ProbeBudget: 5 * time.Second, ProbeGrace: 500 * time.Millisecond})
	exe := filepath.Join(dir, "agent")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho "+cursorFxVersion+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(dir, "scratch")
	if _, err := c.Probe(context.Background(), exe, scratch, cursorFxVersion); err != nil {
		t.Fatalf("version: %v", err)
	}
	if _, err := c.Probe(context.Background(), exe, scratch, "2026.1.1-aaaaaaa"); KindOf(err) != KindProbeMismatch {
		t.Fatalf("other release: %v", err)
	}
	// `agent` is a common name: a program that does not print a Cursor version is not Cursor.
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(other, []byte("#!/bin/sh\necho 'puppet agent 8.0.1'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Probe(context.Background(), other, scratch, ""); KindOf(err) != KindProbeMismatch {
		t.Fatalf("foreign agent: %v", err)
	}
}

func TestOfficialCatalogInstallsCursorFromItsOwnOrigin(t *testing.T) {
	cat := NewOfficialCatalog(nil, nil)
	p, err := cat.LookupV2(DriverCursor)
	if err != nil || p.Key() != DriverCursor {
		t.Fatalf("catalog: %v %v", p, err)
	}
}

// The script is read without following a redirect, as the package is: a script that moved
// elsewhere is refused, not read from wherever it moved to.
func TestCursorPlanRefusesAScriptThatRedirects(t *testing.T) {
	s := newTLSMap(t)
	s.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/install" {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		_, _ = w.Write(cursorFxScript(cursorFxVersion))
	})
	eng, root := cursorFxEngine(t, s)
	if _, err := eng.PlanV2(context.Background(), cursorFxRequest(root)); err == nil {
		t.Fatal("a redirected install script was read")
	}
}

// Two download lines are two answers; the installer takes neither.
func TestCursorPlanRefusesAScriptWithTwoDownloadLines(t *testing.T) {
	s := newTLSMap(t)
	s.put("/install", append(cursorFxScript(cursorFxVersion), cursorFxScript("2026.1.1-aaaaaaa")...))
	eng, root := cursorFxEngine(t, s)
	if _, err := eng.PlanV2(context.Background(), cursorFxRequest(root)); KindOf(err) != KindManifestInvalid {
		t.Fatalf("script with two download lines: %v", err)
	}
}

// The default adapter plans from the vendor's own script and package address only, and a plan
// that names anything else as official, follows a redirect or claims a checksum is refused
// wherever it came from.
func TestCursorOfficialPlanNamesTheVendorOriginOnly(t *testing.T) {
	good := func() SelectionV2 {
		plat, vendor, err := PlatformV2For(DriverCursor, Platform{OS: "linux", Arch: "amd64"})
		if err != nil {
			t.Fatal(err)
		}
		return SelectionV2{
			Driver: DriverCursor, Channel: ChannelExact, RequestedVersion: cursorFxVersion, Version: cursorFxVersion,
			Platform: plat, VendorPlatform: vendor,
			Source: SourcePolicyV2{
				Kind: SourceOfficial, Pointer: URLRef{State: URLStatePresent, URL: CursorScriptURL}, Checksums: URLRef{State: URLStateAbsent},
				Package: URLRef{State: URLStatePresent, URL: cursorPackageURL(CursorPackageBase, cursorFxVersion, vendor)}, Proofs: []ProofLocator{},
				AllowedOrigins: []string{"https://cursor.com", "https://downloads.cursor.com"}, Redirects: RedirectPolicyV2{AllowedOrigins: []string{}},
			},
			FetchedObject:    FetchedObjectExpectation{DigestState: DigestStateUnknown, SizeState: SizeStateBounded, MaxSize: cursorMaxArchive},
			Layout:           cursorLayout(),
			RequiredSubjects: []SubjectExpectation{},
			PackagePolicyID:  PackagePolicyOriginTarballV1,
			Verification:     VerificationProfileV2{Kind: VerificationOriginTarball},
			Destination:      destinationFor("/data/tools", DriverCursor, cursorFxVersion, vendor, "agent"),
		}
	}
	vendorPlan := good()
	if err := vendorPlan.validate(); err != nil {
		t.Fatalf("the vendor's own plan: %v", err)
	}
	for name, mutate := range map[string]func(*SelectionV2){
		"package on another host": func(s *SelectionV2) {
			s.Source.Package.URL = "https://evil.example/lab/" + cursorFxVersion + "/linux/x64/agent-cli-package.tar.gz"
			s.Source.AllowedOrigins = []string{"https://cursor.com", "https://evil.example"}
		},
		"script on another host": func(s *SelectionV2) {
			s.Source.Pointer.URL = "https://evil.example/install"
			s.Source.AllowedOrigins = []string{"https://downloads.cursor.com", "https://evil.example"}
		},
		"another version's package": func(s *SelectionV2) {
			s.Source.Package.URL = cursorPackageURL(CursorPackageBase, "2026.1.1-aaaaaaa", s.VendorPlatform)
		},
		"another platform's package": func(s *SelectionV2) {
			s.Source.Package.URL = cursorPackageURL(CursorPackageBase, cursorFxVersion, "linux-arm64")
		},
		"a redirect": func(s *SelectionV2) {
			s.Source.Redirects = RedirectPolicyV2{MaxHops: 1, AllowedOrigins: []string{"https://downloads.cursor.com"}}
		},
		"a claimed checksum": func(s *SelectionV2) {
			s.Source.Checksums = URLRef{State: URLStatePresent, URL: "https://downloads.cursor.com/sums"}
		},
		"an exact digest": func(s *SelectionV2) {
			s.FetchedObject = FetchedObjectExpectation{DigestState: DigestStateExact, SizeState: SizeStateExact, SHA256: strings.Repeat("a", 64), Size: 10, MaxSize: cursorMaxArchive}
		},
		"a larger cap":   func(s *SelectionV2) { s.FetchedObject.MaxSize = cursorMaxArchive * 2 },
		"a wider layout": func(s *SelectionV2) { s.Layout.Limits.MaxMembers = 1 << 20 },
		"another policy": func(s *SelectionV2) { s.PackagePolicyID = PackagePolicyOriginOnlyV1 },
	} {
		sel := good()
		mutate(&sel)
		if err := sel.validate(); err == nil {
			t.Errorf("%s: the plan was accepted", name)
		}
	}
}

func TestCursorPlatformMapAndRefusal(t *testing.T) {
	for arch, want := range map[string]string{"amd64": "linux-x64", "arm64": "linux-arm64"} {
		plat, vendor, err := PlatformV2For(DriverCursor, Platform{OS: "linux", Arch: arch})
		if err != nil || vendor != want || plat.Arch != arch || cursorVendorPath(vendor) != strings.Replace(want, "-", "/", 1) {
			t.Errorf("%s: %+v %q %v", arch, plat, vendor, err)
		}
	}
	if _, _, err := PlatformV2For(DriverCursor, Platform{OS: "linux", Arch: "riscv64"}); KindOf(err) != KindUnsupportedPlatform {
		t.Errorf("riscv64: %v", err)
	}
}

// The gzip trailer carries the archive's checksum: a package whose trailer is damaged was
// not delivered intact, even when every member read cleanly.
func TestCursorInstallRefusesAPackageWithADamagedTrailer(t *testing.T) {
	s, tarball := cursorFxServe(t, cursorFxMembers(cursorFxVersion))
	damaged := append([]byte{}, tarball...)
	damaged[len(damaged)-8] ^= 0xff // the CRC-32 of the uncompressed data
	s.put("/lab/"+cursorFxVersion+"/linux/x64/agent-cli-package.tar.gz", damaged)
	eng, root := cursorFxEngine(t, s)
	if _, _, err := eng.InstallV2(context.Background(), cursorFxRequest(root), nil, nil); err == nil {
		t.Fatal("a package with a damaged gzip trailer was installed")
	}
	if _, err := os.Lstat(filepath.Join(root, "cursor", cursorFxVersion+"-linux-x64")); err == nil {
		t.Fatal("a refused package left a release behind")
	}
}
