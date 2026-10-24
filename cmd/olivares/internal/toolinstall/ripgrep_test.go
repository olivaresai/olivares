// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// ripgrepFixture is a ripgrep release archive: its program under the release's
// folder, with documentation beside it that is never placed.
func ripgrepFixture(t *testing.T, program []byte) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, m := range []struct {
		name string
		body []byte
	}{{"ripgrep-15.1.0-fixture/doc/GUIDE.md", []byte("guide")}, {"ripgrep-15.1.0-fixture/rg", program}} {
		if err := tw.WriteHeader(&tar.Header{Name: m.name, Mode: 0o755, Size: int64(len(m.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(m.body)
	}
	tw.Close()
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	zw.Write(raw.Bytes())
	zw.Close()
	return out.Bytes()
}

// openCodeEngineServing is the OpenCode release engine of archiveEngine whose
// transport also serves the ripgrep archive, counting its downloads.
func openCodeEngineServing(t *testing.T, rgURL string, rgArchive []byte, served *int) (*Engine, RequestV2) {
	t.Helper()
	_, req, base := archiveEngine(t, DriverOpenCode, archiveFixture(t, DriverOpenCode, nil), false)
	client := &http.Client{Transport: archiveRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != rgURL {
			return base.Transport.RoundTrip(r)
		}
		*served++
		body := io.NopCloser(bytes.NewReader(rgArchive))
		if r.Method == http.MethodHead {
			body = http.NoBody
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: int64(len(rgArchive)), Body: body, Request: r}, nil
	})}
	catalog, err := NewCapabilityCatalog(NewCatalog(), NewOpenCode(ReleaseArchiveOptions{Client: client}))
	if err != nil {
		t.Fatal(err)
	}
	return NewEngineWithCapabilities(catalog, EngineOptions{PlaceRipgrep: true}), req
}

// #3173: OpenCode's glob and grep run ripgrep, which OpenCode otherwise downloads
// from github.com at first use, out of a confined session's reach. Installing
// OpenCode places the pinned ripgrep, verified by its archive's and its program's
// sha256, where a session finds it; a second install downloads nothing.
func TestOpenCodeInstallPlacesThePinnedRipgrep(t *testing.T) {
	program := []byte("#!/bin/sh\necho 'ripgrep 15.1.0'\n")
	archive := ripgrepFixture(t, program)
	saved := ripgrepPins
	t.Cleanup(func() { ripgrepPins = saved })
	pin := ripgrepPin{Version: "15.1.0", Platform: "fixture",
		URL:  "https://github.com/BurntSushi/ripgrep/releases/download/15.1.0/ripgrep-15.1.0-fixture.tar.gz",
		Size: int64(len(archive)), SHA256: sha256Hex(archive), Member: "ripgrep-15.1.0-fixture/rg",
		ProgramSHA256: sha256Hex(program), ProgramSize: int64(len(program))}
	ripgrepPins = map[string]ripgrepPin{"amd64": pin}

	served := 0
	e, req := openCodeEngineServing(t, pin.URL, archive, &served)
	if _, _, err := e.InstallV2(context.Background(), req, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	rg := filepath.Join(ripgrepDir(req.DestRoot, "amd64"), "rg")
	got, err := os.ReadFile(rg)
	if err != nil || !bytes.Equal(got, program) {
		t.Fatalf("installed ripgrep = %q %v, want the pinned program", got, err)
	}
	if info, _ := os.Stat(rg); info.Mode().Perm() != 0o755 {
		t.Fatalf("installed ripgrep mode = %v, want 0755", info.Mode())
	}
	if _, _, err := e.InstallV2(context.Background(), req, nil, io.Discard); err != nil || served != 1 {
		t.Fatalf("second install = %v after %d ripgrep downloads, want no error and one download", err, served)
	}
	if inv, err := e.list(context.Background(), req.DestRoot, ""); err != nil || len(inv.Unexpected) != 0 || len(inv.Leftovers) != 0 {
		t.Fatalf("the tools list after the install = %+v %v, want ripgrep's folder not reported", inv, err)
	}

	// An archive whose program is not the pinned one places nothing.
	ripgrepPins = map[string]ripgrepPin{"amd64": {Version: pin.Version, Platform: pin.Platform, URL: pin.URL, Size: pin.Size, SHA256: pin.SHA256,
		Member: pin.Member, ProgramSHA256: sha256Hex([]byte("another program")), ProgramSize: pin.ProgramSize}}
	other, otherReq := openCodeEngineServing(t, pin.URL, archive, &served)
	if _, _, err := other.InstallV2(context.Background(), otherReq, nil, io.Discard); err == nil {
		t.Fatal("an archive whose program differs from the pin was accepted")
	}
	if _, err := os.Lstat(ripgrepDir(otherReq.DestRoot, "amd64")); !os.IsNotExist(err) {
		t.Fatalf("a refused ripgrep left its folder: %v", err)
	}
}

// #3202: a damaged pinned ripgrep (its hash no longer the pin's, or its folder
// left without it) is replaced by a verified copy when OpenCode is installed again;
// the install no longer exits damaged or conflict.
func TestOpenCodeInstallRepairsADamagedRipgrep(t *testing.T) {
	program := []byte("#!/bin/sh\necho 'ripgrep 15.1.0'\n")
	archive := ripgrepFixture(t, program)
	saved := ripgrepPins
	t.Cleanup(func() { ripgrepPins = saved })
	pin := ripgrepPin{Version: "15.1.0", Platform: "fixture",
		URL:  "https://github.com/BurntSushi/ripgrep/releases/download/15.1.0/ripgrep-15.1.0-fixture.tar.gz",
		Size: int64(len(archive)), SHA256: sha256Hex(archive), Member: "ripgrep-15.1.0-fixture/rg",
		ProgramSHA256: sha256Hex(program), ProgramSize: int64(len(program))}
	ripgrepPins = map[string]ripgrepPin{"amd64": pin}

	served := 0
	e, req := openCodeEngineServing(t, pin.URL, archive, &served)
	if _, _, err := e.InstallV2(context.Background(), req, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	dir := ripgrepDir(req.DestRoot, "amd64")
	rg := filepath.Join(dir, "rg")
	for name, damage := range map[string]func() error{
		"overwritten": func() error { return os.WriteFile(rg, []byte("#!/bin/sh\necho planted\n"), 0o755) },
		"removed":     func() error { return os.Remove(rg) },
	} {
		if err := damage(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := e.InstallV2(context.Background(), req, nil, io.Discard); err != nil {
			t.Fatalf("%s ripgrep: installing OpenCode again = %v, want the ripgrep replaced", name, err)
		}
		if got, err := os.ReadFile(rg); err != nil || !bytes.Equal(got, program) {
			t.Fatalf("%s ripgrep: after installing again rg = %q %v, want the pinned program", name, got, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(req.DestRoot, RipgrepFolder))
	if err != nil || len(entries) != 1 {
		t.Fatalf("ripgrep folder after the repairs = %v %v, want only the version folder", entries, err)
	}
}
