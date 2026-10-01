// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package toolinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"github.com/klauspost/compress/zstd"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type archiveRoundTrip func(*http.Request) (*http.Response, error)

func (f archiveRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func archiveFixture(t *testing.T, driver string, extra *tar.Header) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	name := driver
	if driver == DriverOllama {
		name = "bin/ollama"
	}
	body := []byte("#!/bin/sh\nprintf '" + driver + " version 1.2.3\\n'\n")
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write(body)
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
	pkg := "https://github.com/" + repo + "/releases/download/v1.2.3/" + asset
	digest := sha256Hex(payload)
	if badDigest {
		digest = strings.Repeat("a", 64)
	}
	meta, _ := json.Marshal(map[string]any{"tag_name": "v1.2.3", "assets": []any{map[string]any{"name": asset, "size": len(payload), "digest": "sha256:" + digest, "browser_download_url": pkg}}})
	client := &http.Client{Transport: archiveRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" {
			t.Error("non-HTTPS source")
		}
		body := meta
		if r.URL.String() == pkg {
			body = payload
		} else if r.URL.String() != "https://api.github.com/repos/"+repo+"/releases/latest" && r.URL.String() != "https://api.github.com/repos/"+repo+"/releases/tags/v1.2.3" {
			t.Errorf("unexpected repository URL %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
	})}
	provider := NewOpenCode(ReleaseArchiveOptions{Client: client})
	if driver == DriverOllama {
		provider = NewOllama(ReleaseArchiveOptions{Client: client})
	}
	catalog, err := NewCapabilityCatalog(NewCatalog(), provider)
	if err != nil {
		t.Fatal(err)
	}
	return NewEngineWithCapabilities(catalog, EngineOptions{}), RequestV2{Driver: driver, Version: "latest", Platform: PlatformV2{OS: "linux", Arch: "amd64"}, DestRoot: filepath.Join(t.TempDir(), "tools")}, client
}
func TestReleaseArchiveInstallAndRevalidation(t *testing.T) {
	for _, driver := range []string{DriverOpenCode, DriverOllama} {
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

func TestReleaseArchiveRefusesMetadataOutsidePinnedPolicy(t *testing.T) {
	for _, mutation := range []string{"missing_digest", "different_repository", "oversize", "draft"} {
		t.Run(mutation, func(t *testing.T) {
			engine, req, client := archiveEngine(t, DriverOllama, archiveFixture(t, DriverOllama, nil), false)
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
					asset["browser_download_url"] = "https://github.com/other/ollama/releases/download/v1.2.3/ollama-linux-amd64.tar.zst"
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
