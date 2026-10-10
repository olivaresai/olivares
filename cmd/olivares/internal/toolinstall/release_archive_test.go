// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package toolinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"github.com/klauspost/compress/zstd"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type archiveRoundTrip func(*http.Request) (*http.Response, error)

func (f archiveRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func archiveFixture(t *testing.T, driver string, extra *tar.Header) []byte {
	t.Helper()
	if driver == DriverGemini {
		return geminiArchive(t, "")
	}
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	name := driver
	if driver == DriverOllama {
		name = "bin/ollama"
	}
	if driver == DriverCodex {
		name = "bin/codex" // the release package's layout (codex-package.json "entrypoint")
	}
	body := []byte("#!/bin/sh\nprintf '" + driver + " version 1.2.3\\n'\n")
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write(body)
	if driver == DriverCodex {
		// The package's other members, as rust-v0.160.0 ships them: the code-mode host
		// is placed; the manifest, bundled rg and voice resources are not.
		for _, m := range []struct{ name, data string }{
			{"bin/codex-code-mode-host", "fixture host"}, {"codex-package.json", `{"layoutVersion":1}`},
			{"codex-path/rg", "fixture rg"}, {"codex-resources/voice/bin/codex-voice-host", "fixture voice"},
		} {
			tw.WriteHeader(&tar.Header{Name: m.name, Mode: 0755, Size: int64(len(m.data)), Typeflag: tar.TypeReg})
			tw.Write([]byte(m.data))
		}
		tw.WriteHeader(&tar.Header{Name: "codex-resources/voice/lib/libfixture.so", Linkname: "libfixture.so.0", Typeflag: tar.TypeSymlink})
	}
	if driver == DriverOllama {
		b := []byte("fixture runtime library")
		tw.WriteHeader(&tar.Header{Name: "lib/ollama/libfixture.so.1", Mode: 0644, Size: int64(len(b)), Typeflag: tar.TypeReg})
		tw.Write(b)
		tw.WriteHeader(&tar.Header{Name: "lib/ollama/libfixture.so", Linkname: "libfixture.so.1", Typeflag: tar.TypeSymlink})
	}
	if extra != nil {
		if err := tw.WriteHeader(extra); err != nil {
			t.Fatal(err)
		}
		if extra.Size > 0 {
			tw.Write(bytes.Repeat([]byte("x"), int(extra.Size)))
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	if driver == DriverOllama {
		zw, _ := zstd.NewWriter(&compressed)
		zw.Write(raw.Bytes())
		zw.Close()
	} else {
		zw := gzip.NewWriter(&compressed)
		zw.Write(raw.Bytes())
		zw.Close()
	}
	return compressed.Bytes()
}
func archiveEngine(t *testing.T, driver string, payload []byte, badDigest bool) (*Engine, RequestV2, *http.Client) {
	t.Helper()
	_, vendor, err := PlatformV2For(driver, Platform{OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	repo := releaseRepository(driver)
	asset := releaseAssetName(driver, vendor)
	tag := releaseTagPrefix(driver) + "1.2.3"
	pkg := "https://github.com/" + repo + "/releases/download/" + tag + "/" + asset
	digest := sha256Hex(payload)
	if badDigest {
		digest = strings.Repeat("a", 64)
	}
	meta, _ := json.Marshal(map[string]any{"tag_name": tag, "assets": []any{map[string]any{"name": asset, "size": len(payload), "digest": "sha256:" + digest, "browser_download_url": pkg}}})
	// GitHub's own pages: releases/latest redirects to the tag, and the release's
	// checksum file (Codex, Ollama) names the archive the way each repository does.
	var sumsURL string
	sums := []byte("0000000000000000000000000000000000000000000000000000000000000000  other.tar.gz\n" + digest + "  " + asset + "\n")
	if file := releaseChecksumFile(driver); file != "" {
		sumsURL = "https://github.com/" + repo + "/releases/download/" + tag + "/" + file
	}
	if driver == DriverOllama {
		sums = []byte(digest + "  ./" + asset + "\n")
	}
	client := &http.Client{Transport: archiveRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" {
			t.Error("non-HTTPS source")
		}
		body := meta
		switch u := r.URL.String(); {
		case u == "https://github.com/"+repo+"/releases/latest":
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://github.com/" + repo + "/releases/tag/" + tag}}, Body: http.NoBody, Request: r}, nil
		case u == pkg:
			body = payload
		case u == sumsURL && sumsURL != "":
			body = sums
		case u != "https://api.github.com/repos/"+repo+"/releases/latest" && u != "https://api.github.com/repos/"+repo+"/releases/tags/"+tag:
			t.Errorf("unexpected repository URL %s", r.URL)
		}
		if r.Method == http.MethodHead {
			return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: int64(len(body)), Body: http.NoBody, Request: r}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
	})}
	provider := NewOpenCode(ReleaseArchiveOptions{Client: client})
	if driver == DriverOllama {
		provider = NewOllama(ReleaseArchiveOptions{Client: client})
	}
	if driver == DriverCodex {
		provider = NewCodexRelease(ReleaseArchiveOptions{Client: client})
	}
	if driver == DriverGemini {
		provider = newReleaseArchive(DriverGemini, ReleaseArchiveOptions{Client: client})
	}
	catalog, err := NewCapabilityCatalog(NewCatalog(), provider)
	if err != nil {
		t.Fatal(err)
	}
	return NewEngineWithCapabilities(catalog, EngineOptions{}), RequestV2{Driver: driver, Version: "latest", Platform: PlatformV2{OS: "linux", Arch: "amd64"}, DestRoot: filepath.Join(t.TempDir(), "tools")}, client
}
func TestReleaseArchiveInstallAndRevalidation(t *testing.T) {
	for _, driver := range []string{DriverOpenCode, DriverOllama, DriverCodex, DriverGemini} {
		t.Run(driver, func(t *testing.T) {
			payload := archiveFixture(t, driver, nil)
			engine, req, _ := archiveEngine(t, driver, payload, false)
			plan, err := engine.PlanV2(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Selection.Verification.Kind != VerificationGitHubReleaseSHA256 {
				t.Fatal("invented signature")
			}
			if _, err := os.Stat(req.DestRoot); !os.IsNotExist(err) {
				t.Fatal("planning wrote to disk")
			}
			receipt, _, err := engine.InstallV2(context.Background(), req, plan, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.FetchedObject.SHA256 != sha256Hex(payload) {
				t.Fatal("archive digest was not retained")
			}
			inv, err := engine.List(context.Background(), req.DestRoot)
			if err != nil || len(inv.Installed) != 1 || inv.Installed[0].State != StateInstalled {
				t.Fatalf("inventory: %+v %v", inv, err)
			}
			if _, _, err := engine.InstallV2(context.Background(), req, plan, io.Discard); err != nil {
				t.Fatal(err)
			}
			if driver == DriverCodex {
				// Codex finds its code-mode host next to itself; nothing else of
				// the package is placed.
				if _, err := os.Stat(filepath.Join(receipt.Destination.ReleaseDir, "bin/codex-code-mode-host")); err != nil {
					t.Fatalf("code-mode host not placed: %v", err)
				}
				for _, skipped := range []string{"codex-package.json", "codex-path", "codex-resources"} {
					if _, err := os.Lstat(filepath.Join(receipt.Destination.ReleaseDir, skipped)); !os.IsNotExist(err) {
						t.Fatalf("package resource %s was placed (%v)", skipped, err)
					}
				}
			}
			damaged := receipt.Destination.Executable
			if driver == DriverOllama {
				damaged = filepath.Join(receipt.Destination.ReleaseDir, "lib/ollama/libfixture.so")
			}
			if err := os.WriteFile(damaged, []byte("changed"), 0600); err != nil {
				t.Fatal(err)
			}
			inv, err = engine.List(context.Background(), req.DestRoot)
			if err != nil || inv.Installed[0].State != StateDamaged {
				t.Fatalf("damaged inventory: %+v %v", inv, err)
			}
			if _, _, err := engine.InstallV2(context.Background(), req, plan, io.Discard); err == nil {
				t.Fatal("damaged runtime revalidated")
			}
		})
	}
}

// A person who types an exact version in AI tools (CONTRACT on the builder, 09570b6b: Codex
// 0.160.0 and OpenCode 1.18.34 refused "source URLs contain duplicate" before any download).
// For an exact version the release's tag metadata is both the pointer and the checksums.
func TestReleaseArchiveExactVersionPlansAndInstalls(t *testing.T) {
	for _, driver := range []string{DriverCodex, DriverOpenCode, DriverOllama} {
		t.Run(driver, func(t *testing.T) {
			engine, req, _ := archiveEngine(t, driver, archiveFixture(t, driver, nil), false)
			req.Version = "1.2.3"
			plan, err := engine.PlanV2(context.Background(), req)
			if err != nil {
				t.Fatalf("exact plan: %v", err)
			}
			tags := "https://api.github.com/repos/" + releaseRepository(driver) + "/releases/tags/" + releaseTagPrefix(driver) + "1.2.3"
			src := plan.Selection.Source
			if plan.Selection.Channel != ChannelExact || plan.Selection.Version != "1.2.3" || src.Pointer.URL != tags || src.Checksums.URL != tags {
				t.Fatalf("exact selection: %+v", plan.Selection)
			}
			receipt, _, err := engine.InstallV2(context.Background(), req, plan, io.Discard)
			if err != nil || receipt.Version != "1.2.3" {
				t.Fatalf("exact install: %+v %v", receipt, err)
			}
			// Only the pointer and the checksums share a URL: any other repeat is still refused.
			repeated := plan.Selection
			repeated.Source.Package = src.Checksums
			if err := repeated.Source.originsCover(repeated.presentFetchURLs()); err == nil || !strings.Contains(err.Error(), "duplicate") {
				t.Fatalf("a package URL equal to the metadata was accepted: %v", err)
			}
		})
	}
}
func TestReleaseArchiveRejectsDigestBeforePlacement(t *testing.T) {
	engine, req, _ := archiveEngine(t, DriverOllama, archiveFixture(t, DriverOllama, nil), true)
	plan, err := engine.PlanV2(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.InstallV2(context.Background(), req, plan, io.Discard); err == nil {
		t.Fatal("digest mismatch accepted")
	}
	matches, _ := filepath.Glob(filepath.Join(req.DestRoot, "ollama", "1.2.3-*"))
	if len(matches) > 0 {
		t.Fatal("unverified package was published")
	}
}
func TestReleaseArchiveRejectsUnsafeMembers(t *testing.T) {
	for _, hdr := range []*tar.Header{
		{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0755, Size: 1},
		{Name: "/bin/escape", Typeflag: tar.TypeReg, Mode: 0755, Size: 1},
		{Name: "lib/ollama/out", Typeflag: tar.TypeSymlink, Linkname: "../../../outside"},
		{Name: "lib/ollama/out", Typeflag: tar.TypeLink, Linkname: "/outside"},
		{Name: "bin/ollama", Typeflag: tar.TypeReg, Mode: 0755, Size: 1},
		{Name: "lib/ollama/fifo", Typeflag: tar.TypeFifo},
	} {
		t.Run(hdr.Name+string(hdr.Typeflag), func(t *testing.T) {
			engine, req, _ := archiveEngine(t, DriverOllama, archiveFixture(t, DriverOllama, hdr), false)
			plan, err := engine.PlanV2(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := engine.InstallV2(context.Background(), req, plan, io.Discard); err == nil {
				t.Fatal("unsafe member accepted")
			}
		})
	}
}

// OpenCode attaches no checksum file, so its API metadata decides.
func TestReleaseArchiveRefusesMetadataOutsidePinnedPolicy(t *testing.T) {
	for _, mutation := range []string{"missing_digest", "different_repository", "oversize", "draft"} {
		t.Run(mutation, func(t *testing.T) {
			engine, req, client := archiveEngine(t, DriverOpenCode, archiveFixture(t, DriverOpenCode, nil), false)
			original := client.Transport
			client.Transport = archiveRoundTrip(func(r *http.Request) (*http.Response, error) {
				response, err := original.RoundTrip(r)
				if err != nil {
					return response, err
				}
				body, _ := io.ReadAll(response.Body)
				response.Body.Close()
				var meta map[string]any
				if err := json.Unmarshal(body, &meta); err != nil {
					t.Fatal(err)
				}
				asset := meta["assets"].([]any)[0].(map[string]any)
				switch mutation {
				case "missing_digest":
					asset["digest"] = ""
				case "different_repository":
					asset["browser_download_url"] = "https://github.com/other/opencode/releases/download/v1.2.3/opencode-linux-x64.tar.gz"
				case "oversize":
					asset["size"] = float64(maxReleaseArchiveBytes + 1)
				case "draft":
					meta["draft"] = true
				}
				body, _ = json.Marshal(meta)
				response.Body = io.NopCloser(bytes.NewReader(body))
				response.ContentLength = int64(len(body))
				return response, nil
			})
			if _, err := engine.PlanV2(context.Background(), req); err == nil {
				t.Fatal("invalid official metadata was accepted")
			}
		})
	}
}

func TestReleaseArchiveRefusesHugeMemberBeforeWritingIt(t *testing.T) {
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	if err := tw.WriteHeader(&tar.Header{Name: "bin/ollama", Typeflag: tar.TypeReg, Size: (2 << 30) + 1, Mode: 0755}); err != nil {
		t.Fatal(err)
	}
	// A header alone is sufficient: the policy must refuse its declared size
	// before attempting to consume or allocate the missing multi-gigabyte body.
	var payload bytes.Buffer
	zw, _ := zstd.NewWriter(&payload)
	zw.Write(raw.Bytes())
	zw.Close()
	engine, req, _ := archiveEngine(t, DriverOllama, payload.Bytes(), false)
	plan, err := engine.PlanV2(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.InstallV2(context.Background(), req, plan, io.Discard); err == nil {
		t.Fatal("oversized member accepted")
	}
}

func TestReleaseArchiveOllamaProbePrefersClientVersion(t *testing.T) {
	for _, tc := range []struct {
		output, want string
		valid        bool
	}{
		{"ollama version is 0.34.0\nWarning: client version is 1.2.3\n", "1.2.3", true},
		{"ollama version is 1.2.3\nWarning: client version is 0.34.0\n", "1.2.3", false},
	} {
		t.Run(tc.output, func(t *testing.T) {
			exe := filepath.Join(t.TempDir(), "ollama")
			if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf '"+tc.output+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			_, err := NewOllama(ReleaseArchiveOptions{}).Probe(context.Background(), exe, t.TempDir(), tc.want)
			if (err == nil) != tc.valid {
				t.Fatalf("client version result: %v", err)
			}
		})
	}
}
func TestReleaseArchiveRootHeadersCountAgainstPolicy(t *testing.T) {
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for range 513 {
		if err := tw.WriteHeader(&tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0755}); err != nil {
			t.Fatal(err)
		}
	}
	body := []byte("#!/bin/sh\nprintf 'ollama version 1.2.3\\n'\n")
	tw.WriteHeader(&tar.Header{Name: "bin/ollama", Typeflag: tar.TypeReg, Mode: 0755, Size: int64(len(body))})
	tw.Write(body)
	tw.Close()
	var payload bytes.Buffer
	zw, _ := zstd.NewWriter(&payload)
	zw.Write(raw.Bytes())
	zw.Close()
	engine, req, _ := archiveEngine(t, DriverOllama, payload.Bytes(), false)
	plan, err := engine.PlanV2(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = engine.InstallV2(context.Background(), req, plan, io.Discard); err == nil {
		t.Fatal("root headers bypassed member limit")
	}
}

func TestReleaseArchiveDecodedMetadataCountsAgainstByteLimit(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "payload"), archiveFixture(t, DriverOpenCode, nil), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	layout := releaseArchiveLayout(DriverOpenCode)
	layout.Limits.MaxExpandedBytes = 64 // file fits; its tar header does not
	if _, err = extractReleaseArchive(context.Background(), DriverOpenCode, root, "stage", "payload", layout); KindOf(err) != KindResponseTooLarge {
		t.Fatalf("decoded metadata was not bounded: %v", err)
	}
}

// The Codex plan is resolved from the official release metadata exactly as
// GitHub answered it for rust-v0.159.3 (recorded 2026-10-01, trimmed to the
// Codex archives): the rust-v tag, the platform archive and its SHA-256.
func TestReleaseArchiveCodexPlansFromRecordedOfficialMetadata(t *testing.T) {
	meta, err := os.ReadFile(filepath.Join("testdata", "codex-release-rust-v0.159.3.json"))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: archiveRoundTrip(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case "https://github.com/openai/codex/releases/latest":
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://github.com/openai/codex/releases/tag/rust-v0.159.3"}}, Body: http.NoBody, Request: r}, nil
		case "https://github.com/openai/codex/releases/download/rust-v0.159.3/codex-package_SHA256SUMS":
			// This release predates the checksum file, so its metadata decides.
			return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
		case "https://api.github.com/repos/openai/codex/releases/latest", "https://api.github.com/repos/openai/codex/releases/tags/rust-v0.159.3":
		default:
			t.Errorf("unexpected URL %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: int64(len(meta)), Body: io.NopCloser(bytes.NewReader(meta)), Request: r}, nil
	})}
	catalog, err := NewCapabilityCatalog(NewCatalog(), NewCodexRelease(ReleaseArchiveOptions{Client: client}))
	if err != nil {
		t.Fatal(err)
	}
	engine := NewEngineWithCapabilities(catalog, EngineOptions{})
	for _, tc := range []struct{ arch, url, digest string }{
		{"amd64", "https://github.com/openai/codex/releases/download/rust-v0.159.3/codex-x86_64-unknown-linux-musl.tar.gz", "b48ca1b2d6b1bf42b944e02c3d937c898e24651916684cdc35fdedf31b291bcb"},
		{"arm64", "https://github.com/openai/codex/releases/download/rust-v0.159.3/codex-aarch64-unknown-linux-musl.tar.gz", "cd5f307b3fcd6080773e684b86c3114a67d4f1c61dc447be09876b552eb4bea7"},
	} {
		plan, err := engine.PlanV2(context.Background(), RequestV2{Driver: DriverCodex, Version: "latest", Platform: PlatformV2{OS: "linux", Arch: tc.arch}, DestRoot: filepath.Join(t.TempDir(), "tools")})
		if err != nil {
			t.Fatalf("%s: %v", tc.arch, err)
		}
		s := plan.Selection
		if s.Version != "0.159.3" || s.Source.Package.URL != tc.url || s.FetchedObject.SHA256 != tc.digest || s.Layout.EntryPoint != "bin/codex" || s.Verification.Kind != VerificationGitHubReleaseSHA256 {
			t.Fatalf("%s plan = %+v", tc.arch, s)
		}
	}
}

// rust-v0.160.0 publishes codex-package next to the single-binary archive; the
// plan takes the package and its SHA-256 exactly as GitHub's metadata states them
// (recorded 2026-10-03, trimmed to the four Linux archives).
func TestReleaseArchiveCodexPlansThePackageFromRecordedOfficialMetadata(t *testing.T) {
	meta, err := os.ReadFile(filepath.Join("testdata", "codex-release-rust-v0.160.0.json"))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: archiveRoundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: int64(len(meta)), Body: io.NopCloser(bytes.NewReader(meta)), Request: r}, nil
	})}
	catalog, err := NewCapabilityCatalog(NewCatalog(), NewCodexRelease(ReleaseArchiveOptions{Client: client}))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := NewEngineWithCapabilities(catalog, EngineOptions{}).PlanV2(context.Background(),
		RequestV2{Driver: DriverCodex, Version: "latest", Platform: PlatformV2{OS: "linux", Arch: "amd64"}, DestRoot: filepath.Join(t.TempDir(), "tools")})
	if err != nil {
		t.Fatal(err)
	}
	s := plan.Selection
	if s.Version != "0.160.0" || s.Layout.ID != LayoutIDCodexReleasePackageV1 ||
		s.Source.Package.URL != "https://github.com/openai/codex/releases/download/rust-v0.160.0/codex-package-x86_64-unknown-linux-musl.tar.gz" ||
		s.FetchedObject.SHA256 != "4fcc47ab57f52ff75363951a8761146cd10c8288bd86fed45487dbb204a16b71" {
		t.Fatalf("plan = %+v", s)
	}
}

// A Codex release without the package keeps the single-binary archive: the plan, the
// install and the revalidation of such an install still work (and receipts of installs
// made with the single-binary archive stay valid).
func TestReleaseArchiveCodexWithoutAPackageKeepsTheSingleBinaryArchive(t *testing.T) {
	var raw, compressed bytes.Buffer
	tw := tar.NewWriter(&raw)
	body := []byte("#!/bin/sh\nprintf 'codex version 1.2.3\\n'\n")
	tw.WriteHeader(&tar.Header{Name: "codex-x86_64-unknown-linux-musl", Mode: 0755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write(body)
	tw.Close()
	zw := gzip.NewWriter(&compressed)
	zw.Write(raw.Bytes())
	zw.Close()
	payload := compressed.Bytes()
	pkg := "https://github.com/openai/codex/releases/download/rust-v1.2.3/codex-x86_64-unknown-linux-musl.tar.gz"
	meta, _ := json.Marshal(map[string]any{"tag_name": "rust-v1.2.3", "assets": []any{map[string]any{"name": "codex-x86_64-unknown-linux-musl.tar.gz", "size": len(payload), "digest": "sha256:" + sha256Hex(payload), "browser_download_url": pkg}}})
	client := &http.Client{Transport: archiveRoundTrip(func(r *http.Request) (*http.Response, error) {
		b := meta
		if r.URL.String() == pkg {
			b = payload
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: int64(len(b)), Body: io.NopCloser(bytes.NewReader(b)), Request: r}, nil
	})}
	catalog, err := NewCapabilityCatalog(NewCatalog(), NewCodexRelease(ReleaseArchiveOptions{Client: client}))
	if err != nil {
		t.Fatal(err)
	}
	engine := NewEngineWithCapabilities(catalog, EngineOptions{})
	req := RequestV2{Driver: DriverCodex, Version: "latest", Platform: PlatformV2{OS: "linux", Arch: "amd64"}, DestRoot: filepath.Join(t.TempDir(), "tools")}
	plan, err := engine.PlanV2(context.Background(), req)
	if err != nil || plan.Selection.Layout.ID != LayoutIDCodexArchiveV1 || plan.Selection.Source.Package.URL != pkg {
		t.Fatalf("plan = %+v %v, want the single-binary archive", plan, err)
	}
	if _, _, err := engine.InstallV2(context.Background(), req, plan, io.Discard); err != nil {
		t.Fatal(err)
	}
	inv, err := engine.List(context.Background(), req.DestRoot)
	if err != nil || len(inv.Installed) != 1 || inv.Installed[0].State != StateInstalled {
		t.Fatalf("inventory: %+v %v", inv, err)
	}
}

// A member outside the package's known layout is still refused, including a binary
// named like the single-binary archive's (SR5C on da79cb63: it was renamed to bin/codex).
func TestReleaseArchiveCodexPackageRefusesAnUnknownMember(t *testing.T) {
	var raw, compressed bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, name := range []string{"codex-wrong-unknown-linux-musl", "bin/codex-code-mode-host"} {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: 1, Typeflag: tar.TypeReg})
		tw.Write([]byte("x"))
	}
	tw.Close()
	zw := gzip.NewWriter(&compressed)
	zw.Write(raw.Bytes())
	zw.Close()
	for name, payload := range map[string][]byte{
		"share/unexpected":               archiveFixture(t, DriverCodex, &tar.Header{Name: "share/unexpected", Typeflag: tar.TypeReg, Mode: 0755, Size: 1}),
		"codex-wrong-unknown-linux-musl": compressed.Bytes(),
	} {
		t.Run(name, func(t *testing.T) {
			engine, req, _ := archiveEngine(t, DriverCodex, payload, false)
			plan, err := engine.PlanV2(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := engine.InstallV2(context.Background(), req, plan, io.Discard); KindOf(err) != KindManifestInvalid {
				t.Fatalf("unknown package member %s = %v, want %s", name, err, KindManifestInvalid)
			}
		})
	}
}

// On a release candidate installing Codex failed with "unsupported_source: …/releases/latest
// answered HTTP 403" because GitHub's limit of 60 API calls an hour for the network was
// used up. The install now says what happened and when to try again; any other 403
// keeps its answer.
func TestReleaseArchiveSaysWhenGitHubsLimitIsUsedUp(t *testing.T) {
	const reset = 1791063399
	for _, tc := range []struct {
		name, remaining, reset, kind, want string
	}{
		{"limit used up", "0", "1791063399", KindTransport, "GitHub's limit for this network is used up; try again after " + time.Unix(reset, 0).UTC().Format("15:04") + " UTC"},
		{"no reset time", "0", "", KindTransport, "GitHub's limit for this network is used up; try again within the hour"},
		{"another refusal", "41", "1791063399", KindUnsupportedSource, "answered HTTP 403"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: archiveRoundTrip(func(r *http.Request) (*http.Response, error) {
				h := http.Header{"X-Ratelimit-Remaining": {tc.remaining}}
				if tc.reset != "" {
					h.Set("X-RateLimit-Reset", tc.reset)
				}
				return &http.Response{StatusCode: http.StatusForbidden, Header: h, Body: io.NopCloser(strings.NewReader(`{"message":"API rate limit exceeded"}`)), Request: r}, nil
			})}
			catalog, err := NewCapabilityCatalog(NewCatalog(), NewCodexRelease(ReleaseArchiveOptions{Client: client}))
			if err != nil {
				t.Fatal(err)
			}
			_, err = NewEngineWithCapabilities(catalog, EngineOptions{}).PlanV2(context.Background(),
				RequestV2{Driver: DriverCodex, Version: "latest", Platform: PlatformV2{OS: "linux", Arch: "amd64"}, DestRoot: filepath.Join(t.TempDir(), "tools")})
			if KindOf(err) != tc.kind || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("plan = %v (kind %q), want %s saying %q", err, KindOf(err), tc.kind, tc.want)
			}
		})
	}
}

// GitHub's API answers a network without a token 60 times an hour, and installs behind one
// address ran out of it within the hour. Codex and Ollama attach a checksum file to every
// release, so their install reads github.com: releases/latest for the tag, the checksum
// file for the SHA-256 and HEAD for the size. An exact version asks the API metadata
// first (only it says whether the version is a pre-release) and, with the limit used up,
// is planned from the checksum file. OpenCode attaches none and keeps its API metadata.
func TestReleaseArchiveInstallsFromTheReleasesOwnChecksumFileWithoutGitHubsAPI(t *testing.T) {
	for _, driver := range []string{DriverCodex, DriverOllama} {
		for _, version := range []string{ChannelLatest, "1.2.3"} {
			t.Run(driver+"/"+version, func(t *testing.T) {
				payload := archiveFixture(t, driver, nil)
				engine, req, client := archiveEngine(t, driver, payload, false)
				original, api := client.Transport, 0
				client.Transport = archiveRoundTrip(func(r *http.Request) (*http.Response, error) {
					if r.URL.Host == "api.github.com" {
						api++
						return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{"X-Ratelimit-Remaining": {"0"}}, Body: http.NoBody, Request: r}, nil
					}
					return original.RoundTrip(r)
				})
				req.Version = version
				plan, err := engine.PlanV2(context.Background(), req)
				if err != nil {
					t.Fatal(err)
				}
				repo := releaseRepository(driver)
				sums := "https://github.com/" + repo + "/releases/download/" + releaseTagPrefix(driver) + "1.2.3/" + releaseChecksumFile(driver)
				pointer := sums
				if version == ChannelLatest {
					pointer = "https://github.com/" + repo + "/releases/latest"
				}
				s := plan.Selection
				if s.Version != "1.2.3" || s.Source.Checksums.URL != sums || s.Source.Pointer.URL != pointer || s.FetchedObject.SHA256 != sha256Hex(payload) || s.FetchedObject.Size != int64(len(payload)) {
					t.Fatalf("selection = %+v %+v", s.Source, s.FetchedObject)
				}
				if _, _, err := engine.InstallV2(context.Background(), req, plan, io.Discard); err != nil {
					t.Fatal(err)
				}
				if (version == ChannelLatest) != (api == 0) {
					t.Fatalf("%d requests went to GitHub's API for %s: latest asks it nothing, an exact version asks it first", api, version)
				}
			})
		}
	}
}

// An install planned from GitHub's API metadata is the same release when the release is
// later planned from its own checksum file: installing it again, by any route, finds it in
// place instead of calling it damaged.
func TestReleaseArchiveKeepsAnInstallPlannedFromTheAPIMetadata(t *testing.T) {
	for _, driver := range []string{DriverCodex, DriverOllama} {
		t.Run(driver, func(t *testing.T) {
			engine, req, client := archiveEngine(t, driver, archiveFixture(t, driver, nil), false)
			original, before := client.Transport, true
			client.Transport = archiveRoundTrip(func(r *http.Request) (*http.Response, error) {
				if before && r.URL.Host == "github.com" && (strings.HasSuffix(r.URL.Path, "/releases/latest") || strings.HasSuffix(r.URL.Path, "/"+releaseChecksumFile(driver))) {
					return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
				}
				return original.RoundTrip(r)
			})
			plan, err := engine.PlanV2(context.Background(), req)
			if err != nil || !strings.HasPrefix(plan.Selection.Source.Checksums.URL, "https://api.github.com/") {
				t.Fatalf("first plan = %+v %v, want one from the API metadata", plan, err)
			}
			first, _, err := engine.InstallV2(context.Background(), req, plan, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			before = false
			for _, version := range []string{ChannelLatest, "1.2.3", ChannelStable} {
				if rec := installAs(t, engine, req, version); rec.InstalledAt != first.InstalledAt || rec.PlanDigest != first.PlanDigest {
					t.Fatalf("as %q: a second installation instead of the release in place", version)
				}
			}
		})
	}
}

// An exact pre-release is refused as before, though its checksum file and archive answer
// on github.com: only the API metadata says a release is a pre-release (a tag need not
// carry a "-rc" part), so an exact version reads it first. With the API limit used up,
// the checksum file admits an exact version only when releases/latest names it, and
// here it names another release.
func TestReleaseArchiveRefusesAnExactPreReleaseOnTheChecksumRoute(t *testing.T) {
	for _, driver := range []string{DriverCodex, DriverOllama} {
		for _, tc := range []struct {
			version string
			limited bool
		}{{"1.2.3-rc.1", false}, {"1.2.3-rc.1", true}, {"1.2.4", false}, {"1.2.4", true}} {
			version, limited := tc.version, tc.limited
			t.Run(fmt.Sprintf("%s/%s/limited=%t", driver, version, limited), func(t *testing.T) {
				payload := archiveFixture(t, driver, nil)
				_, vendor, _ := PlatformV2For(driver, Platform{OS: "linux", Arch: "amd64"})
				repo, tag, asset := releaseRepository(driver), releaseTagPrefix(driver)+version, releaseAssetName(driver, vendor)
				download := "https://github.com/" + repo + "/releases/download/" + tag + "/"
				meta, _ := json.Marshal(map[string]any{"tag_name": tag, "prerelease": true, "assets": []any{map[string]any{"name": asset, "size": len(payload), "digest": "sha256:" + sha256Hex(payload), "browser_download_url": download + asset}}})
				client := &http.Client{Transport: archiveRoundTrip(func(r *http.Request) (*http.Response, error) {
					body := meta
					switch u := r.URL.String(); {
					case r.URL.Host == "api.github.com" && limited:
						return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{"X-Ratelimit-Remaining": {"0"}}, Body: http.NoBody, Request: r}, nil
					case u == "https://github.com/"+repo+"/releases/latest":
						return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://github.com/" + repo + "/releases/tag/" + releaseTagPrefix(driver) + "1.2.3"}}, Body: http.NoBody, Request: r}, nil
					case u == download+releaseChecksumFile(driver):
						body = []byte(sha256Hex(payload) + "  " + asset + "\n")
					case u == download+asset:
						body = payload
					}
					if r.Method == http.MethodHead {
						return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: int64(len(body)), Body: http.NoBody, Request: r}, nil
					}
					return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
				})}
				provider := NewOllama(ReleaseArchiveOptions{Client: client})
				if driver == DriverCodex {
					provider = NewCodexRelease(ReleaseArchiveOptions{Client: client})
				}
				catalog, err := NewCapabilityCatalog(NewCatalog(), provider)
				if err != nil {
					t.Fatal(err)
				}
				plan, err := NewEngineWithCapabilities(catalog, EngineOptions{}).PlanV2(context.Background(),
					RequestV2{Driver: driver, Version: version, Platform: PlatformV2{OS: "linux", Arch: "amd64"}, DestRoot: filepath.Join(t.TempDir(), "tools")})
				if err == nil {
					t.Fatalf("an exact pre-release was planned: %+v", plan.Selection.Source)
				}
			})
		}
	}
}

// OpenCode attaches no checksum file, so its install still reads GitHub's API. With the
// network's API limit used up, the release this build was qualified with installs exactly
// from its recorded metadata; the archive still comes from github.com, pinned to the
// recorded SHA-256.
func TestReleaseArchiveInstallsTheQualifiedOpenCodeWhenGitHubsLimitIsUsedUp(t *testing.T) {
	for _, requested := range []string{ChannelLatest, "1.18.34"} {
		t.Run(requested, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: archiveRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				h := http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"1791045150"}}
				return &http.Response{StatusCode: http.StatusForbidden, Header: h, Body: io.NopCloser(strings.NewReader(`{"message":"API rate limit exceeded"}`)), Request: r}, nil
			})}
			catalog, err := NewCapabilityCatalog(NewCatalog(), NewOpenCode(ReleaseArchiveOptions{Client: client}))
			if err != nil {
				t.Fatal(err)
			}
			plan, err := NewEngineWithCapabilities(catalog, EngineOptions{}).PlanV2(context.Background(),
				RequestV2{Driver: DriverOpenCode, Version: requested, Platform: PlatformV2{OS: "linux", Arch: "amd64"}, DestRoot: filepath.Join(t.TempDir(), "tools")})
			if err != nil {
				t.Fatalf("plan with GitHub's limit used up: %v", err)
			}
			s := plan.Selection
			tags := "https://api.github.com/repos/" + releaseRepository(DriverOpenCode) + "/releases/tags/v1.18.34"
			if calls != 1 || s.Version != "1.18.34" || s.Channel != ChannelExact || s.RequestedVersion != "1.18.34" || s.Source.Pointer.URL != tags || s.Source.Checksums.URL != tags {
				t.Fatalf("API calls %d, selection %+v", calls, s)
			}
			body, _ := qualifiedRelease(DriverOpenCode)
			var meta githubReleaseMetadata
			if err := json.Unmarshal(body, &meta); err != nil {
				t.Fatal(err)
			}
			recorded := ""
			for _, a := range meta.Assets {
				if a.URL == s.Source.Package.URL {
					recorded = strings.TrimPrefix(a.Digest, "sha256:")
				}
			}
			if recorded == "" || s.FetchedObject.SHA256 != recorded {
				t.Fatalf("package %s sha256 %s, want the recorded archive's %q", s.Source.Package.URL, s.FetchedObject.SHA256, recorded)
			}
		})
	}
	// Only the qualified release: another exact version still says the limit is used up.
	client := &http.Client{Transport: archiveRoundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{"X-Ratelimit-Remaining": {"0"}}, Body: http.NoBody, Request: r}, nil
	})}
	catalog, err := NewCapabilityCatalog(NewCatalog(), NewOpenCode(ReleaseArchiveOptions{Client: client}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewEngineWithCapabilities(catalog, EngineOptions{}).PlanV2(context.Background(),
		RequestV2{Driver: DriverOpenCode, Version: "1.18.33", Platform: PlatformV2{OS: "linux", Arch: "amd64"}, DestRoot: filepath.Join(t.TempDir(), "tools")}); err == nil || !strings.Contains(err.Error(), "limit for this network is used up") {
		t.Fatalf("another version with the limit used up = %v", err)
	}
	// With one call left, latest answers and the read of its own tag finds the limit
	// used up: the qualified release is planned, as with no call left.
	calls := 0
	client = &http.Client{Transport: archiveRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			body, _ := qualifiedRelease(DriverOpenCode)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(bytes.ReplaceAll(body, []byte("1.18.34"), []byte("1.20.0")))), Request: r}, nil
		}
		return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{"X-Ratelimit-Remaining": {"0"}}, Body: http.NoBody, Request: r}, nil
	})}
	if catalog, err = NewCapabilityCatalog(NewCatalog(), NewOpenCode(ReleaseArchiveOptions{Client: client})); err != nil {
		t.Fatal(err)
	}
	plan, err := NewEngineWithCapabilities(catalog, EngineOptions{}).PlanV2(context.Background(),
		RequestV2{Driver: DriverOpenCode, Version: ChannelLatest, Platform: PlatformV2{OS: "linux", Arch: "amd64"}, DestRoot: filepath.Join(t.TempDir(), "tools")})
	if err != nil {
		t.Fatalf("plan with the limit used up at the tag read: %v", err)
	}
	if s := plan.Selection; calls != 2 || s.Version != "1.18.34" || s.Channel != ChannelExact || !strings.HasSuffix(s.Source.Checksums.URL, "/releases/tags/v1.18.34") {
		t.Fatalf("API calls %d, selection %+v", calls, s)
	}
}

// TestReleaseReadsAskAgainOnceAfterAGatewayError: a gateway error (502, 503, 504) or
// a dropped connection gets one more attempt after a short pause. Any other answer, a
// failure that would repeat, and a second failure are returned as they came; a second
// failure names the first, and a read canceled during the pause stops waiting.
func TestReleaseReadsAskAgainOnceAfterAGatewayError(t *testing.T) {
	type answer func() (int, error)
	code := func(c int) answer { return func() (int, error) { return c, nil } }
	fail := func(err error) answer { return func() (int, error) { return 0, err } }
	u := "https://github.com/openai/codex/releases/download/rust-v1.0.0/codex-package_SHA256SUMS"
	read := func(ctx context.Context, pause time.Duration, answers ...answer) (string, int, error, int) {
		calls := 0
		f := fetcher{pause: pause, client: &http.Client{Transport: archiveRoundTrip(func(r *http.Request) (*http.Response, error) {
			status, err := answers[min(calls, len(answers)-1)]()
			calls++
			if err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("sums")), Request: r}, nil
		})}}
		body, status, err := f.small(ctx, u, 1<<10)
		return string(body), status, err, calls
	}
	for i, first := range []answer{code(http.StatusBadGateway), code(http.StatusServiceUnavailable), code(http.StatusGatewayTimeout), fail(syscall.ECONNRESET), fail(syscall.ECONNREFUSED), fail(io.ErrUnexpectedEOF)} {
		if body, status, err, calls := read(context.Background(), time.Millisecond, first, code(http.StatusOK)); err != nil || status != http.StatusOK || body != "sums" || calls != 2 {
			t.Fatalf("case %d then 200: body %q status %d err %v calls %d", i, body, status, err, calls)
		}
	}
	for i, only := range []answer{code(http.StatusInternalServerError), code(http.StatusTooManyRequests), code(http.StatusForbidden), code(http.StatusNotFound),
		fail(x509.UnknownAuthorityError{}), fail(&net.DNSError{Err: "no such host", Name: "github.com", IsNotFound: true}), fail(os.ErrDeadlineExceeded)} {
		if _, _, _, calls := read(context.Background(), time.Millisecond, only); calls != 1 {
			t.Fatalf("case %d was asked again: calls %d", i, calls)
		}
	}
	_, status, err, calls := read(context.Background(), time.Millisecond, code(http.StatusGatewayTimeout))
	if refusal := statusOrTransport(status, u, "source"); status != http.StatusGatewayTimeout || calls != 2 || err != nil || refusal == nil || !strings.Contains(refusal.Error(), "try again in a few minutes") {
		t.Fatalf("504 twice: status %d calls %d err %v refusal %v", status, calls, err, refusal)
	}
	if _, _, err, calls = read(context.Background(), time.Millisecond, code(http.StatusGatewayTimeout), fail(syscall.ECONNRESET)); err == nil || !strings.Contains(err.Error(), "first attempt: HTTP 504") || calls != 2 {
		t.Fatalf("504 then reset: err %v calls %d", err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	answered := make(chan struct{})
	go func() { <-answered; time.Sleep(200 * time.Millisecond); cancel() }()
	started := time.Now()
	first := func() (int, error) {
		select {
		case <-answered:
		default:
			close(answered)
		}
		return http.StatusGatewayTimeout, nil
	}
	if _, _, err, calls = read(ctx, time.Minute, first); err == nil || !strings.Contains(err.Error(), "canceled after HTTP 504") || calls != 1 || time.Since(started) > 5*time.Second {
		t.Fatalf("canceled during the pause: err %v calls %d after %s", err, calls, time.Since(started))
	}
}

// TestReleaseArchivePlansThroughOneGatewayError: one gateway error from GitHub does not
// fail a Codex plan, on the API metadata read of an exact version or on the checksum
// route of latest (its redirect and its HEAD); a second one is refused with advice.
func TestReleaseArchivePlansThroughOneGatewayError(t *testing.T) {
	payload := archiveFixture(t, DriverCodex, nil)
	plan := func(version string, fails func(*http.Request, map[string]int) int) (*PlanV2, int, error) {
		engine, req, client := archiveEngine(t, DriverCodex, payload, false)
		original, api, seen := client.Transport, 0, map[string]int{}
		client.Transport = archiveRoundTrip(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host == "api.github.com" {
				api++
			}
			key := r.Method + " " + r.URL.String()
			seen[key]++
			if status := fails(r, seen); status != 0 {
				return &http.Response{StatusCode: status, Body: http.NoBody, Request: r}, nil
			}
			return original.RoundTrip(r)
		})
		req.Version = version
		p, err := engine.PlanV2(context.Background(), req)
		return p, api, err
	}
	once := func(host, method string, status int) func(*http.Request, map[string]int) int {
		return func(r *http.Request, seen map[string]int) int {
			if r.URL.Host == host && r.Method == method && seen[r.Method+" "+r.URL.String()] == 1 {
				return status
			}
			return 0
		}
	}
	if _, api, err := plan("1.2.3", once("api.github.com", http.MethodGet, http.StatusGatewayTimeout)); err != nil || api != 2 {
		t.Fatalf("exact version through one API 504: api calls %d err %v", api, err)
	}
	always := func(r *http.Request, _ map[string]int) int {
		if r.URL.Host == "api.github.com" {
			return http.StatusGatewayTimeout
		}
		return 0
	}
	if _, _, err := plan("1.2.3", always); err == nil || !strings.Contains(err.Error(), "try again in a few minutes") {
		t.Fatalf("exact version with the API answering 504 = %v", err)
	}
	flaky := func(r *http.Request, seen map[string]int) int {
		if status := once("github.com", http.MethodGet, http.StatusBadGateway)(r, seen); status != 0 && strings.HasSuffix(r.URL.Path, "/releases/latest") {
			return status
		}
		return once("github.com", http.MethodHead, http.StatusServiceUnavailable)(r, seen)
	}
	p, api, err := plan(ChannelLatest, flaky)
	if err != nil || api != 0 || !strings.HasSuffix(p.Selection.Source.Checksums.URL, "/"+releaseChecksumFile(DriverCodex)) {
		t.Fatalf("latest through a redirect 502 and a HEAD 503: api calls %d err %v", api, err)
	}
}
