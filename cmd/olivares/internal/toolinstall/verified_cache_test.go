// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

type countedVerifier struct {
	SignatureVerifier
	calls atomic.Int32
}

func (v *countedVerifier) Verify(ctx context.Context, key []byte, pin string, sig, data []byte) (SignatureReport, error) {
	v.calls.Add(1)
	return v.SignatureVerifier.Verify(ctx, key, pin, sig, data)
}

func TestConcurrentInventoryReadsVerifyAReleaseOnce(t *testing.T) {
	f := newFixture(t)
	f.engine.verified = NewVerifiedCache()
	f.publish(fxVersion)
	if _, _, err := f.install(fxVersion); err != nil {
		t.Fatal(err)
	}
	v := &countedVerifier{SignatureVerifier: f.verifier}
	f.claude.verifier = v
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			<-start
			inv, err := f.engine.List(t.Context(), f.root)
			if err != nil || len(inv.Installed) != 1 || inv.Installed[0].State != StateInstalled {
				t.Errorf("inventory = %+v, %v", inv, err)
			}
		})
	}
	close(start)
	wg.Wait()
	if got := v.calls.Load(); got != 1 {
		t.Fatalf("concurrent inventory reads verified the unchanged release %d times, want 1", got)
	}
}

func TestDriverLookupDoesNotHashUnrelatedReleases(t *testing.T) {
	engine, req, _, _ := cachedArchiveInstall(t)
	engine.verified = nil // Even an uncached lookup must inspect only its driver.
	engine.capabilities = NewOfficialCatalog(nil, UnavailableVerifier{})
	for _, lookup := range []struct {
		name string
		run  func() error
	}{
		{"detect", func() error {
			_, err := engine.Detect(t.Context(), DetectOptions{Driver: DriverGrok, Root: req.DestRoot})
			return err
		}},
		{"latest", func() error {
			_, _, err := engine.LatestInstalled(t.Context(), req.DestRoot, DriverGrok)
			return err
		}},
	} {
		t.Run(lookup.name, func(t *testing.T) {
			if n := hashesDuring(t, func() {
				if err := lookup.run(); err != nil {
					t.Fatal(err)
				}
			}); n != 0 {
				t.Fatalf("Grok lookup hashed %d files from the unrelated Codex install", n)
			}
		})
	}
}

// EU-CB07 (09b) and Root's conditions of 2026-10-02: every read of a tool's status
// re-hashed the installed release (about 1.0 s for Claude Code, 0.5 s for Codex). A
// release a full check found installed is now kept, in memory, until anything it
// is made of changes; then it is checked in full again before use. These count the
// hash calls (fileHashes) around each List.

// rewrite writes b to path in place, lifting a read-only mode for the write and
// putting it back: the bytes are the given ones, the inode is the same, the ctime moves.
func rewrite(t *testing.T, path string, b []byte) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, fi.Mode().Perm()|0o200); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, fi.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, fi.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
}

func hashesDuring(t *testing.T, f func()) int64 {
	t.Helper()
	before := fileHashes.Load()
	f()
	return fileHashes.Load() - before
}

// cachedArchiveInstall installs the Codex release-archive fixture (the archive path
// OpenCode and Ollama share) with a cache-backed engine.
func cachedArchiveInstall(t *testing.T) (*Engine, RequestV2, *PlanV2, string) {
	t.Helper()
	engine, req, _ := archiveEngine(t, DriverCodex, archiveFixture(t, DriverCodex, nil), false)
	engine.verified = NewVerifiedCache()
	plan, err := engine.PlanV2(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	receipt, _, err := engine.InstallV2(context.Background(), req, plan, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return engine, req, plan, receipt.Destination.Executable
}

func archiveState(t *testing.T, e *Engine, root string) Installed {
	t.Helper()
	inv, err := e.List(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range inv.Installed {
		if in.Driver == DriverCodex {
			return in
		}
	}
	t.Fatalf("no Codex release in %+v", inv.Installed)
	return Installed{}
}

func TestVerifiedReleaseIsNotRehashedWhileUnchanged(t *testing.T) {
	engine, req, _, _ := cachedArchiveInstall(t)
	if n := hashesDuring(t, func() {
		if in := archiveState(t, engine, req.DestRoot); in.State != StateInstalled {
			t.Fatalf("first read: %+v", in)
		}
	}); n == 0 {
		t.Fatal("the first read did not check the release")
	}
	for i := 0; i < 3; i++ {
		if n := hashesDuring(t, func() {
			if in := archiveState(t, engine, req.DestRoot); in.State != StateInstalled {
				t.Fatalf("read %d: %+v", i, in)
			}
		}); n != 0 {
			t.Fatalf("read %d of an unchanged release hashed %d files", i, n)
		}
	}
}

func TestVerifiedReleaseIsCheckedAgainWhenAnyPartChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, exe, receipt string)
	}{
		{"a replaced executable (new inode)", func(t *testing.T, exe, _ string) {
			b, err := os.ReadFile(exe)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(exe); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(exe, b, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"an in-place write (ctime)", func(t *testing.T, exe, _ string) {
			b, err := os.ReadFile(exe)
			if err != nil {
				t.Fatal(err)
			}
			rewrite(t, exe, b)
		}},
		{"a rewritten receipt", func(t *testing.T, _, receipt string) {
			b, err := os.ReadFile(receipt)
			if err != nil {
				t.Fatal(err)
			}
			rewrite(t, receipt, b)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine, req, _, exe := cachedArchiveInstall(t)
			receipt := filepath.Join(filepath.Dir(filepath.Dir(exe)), ReceiptFile)
			archiveState(t, engine, req.DestRoot) // verified and kept
			tc.change(t, exe, receipt)
			if n := hashesDuring(t, func() {
				if in := archiveState(t, engine, req.DestRoot); in.State != StateInstalled {
					t.Fatalf("after %s: %+v", tc.name, in)
				}
			}); n == 0 {
				t.Fatalf("%s was not checked again", tc.name)
			}
		})
	}
}

// A tampered executable is refused, and the refusal is never kept: every read
// checks it again and refuses again.
func TestATamperedReleaseIsRefusedAtEveryRead(t *testing.T) {
	engine, req, _, exe := cachedArchiveInstall(t)
	archiveState(t, engine, req.DestRoot)
	rewrite(t, exe, []byte("#!/bin/sh\necho tampered\n"))
	for i := 0; i < 2; i++ {
		if n := hashesDuring(t, func() {
			if in := archiveState(t, engine, req.DestRoot); in.State != StateDamaged {
				t.Fatalf("read %d of a tampered release: %+v", i, in)
			}
		}); n == 0 {
			t.Fatalf("read %d of a tampered release was served from memory", i)
		}
	}
}

// ForgetVerified (a sign-in) and an install clear the tool's entry; an engine
// without the cache checks every read, as before.
func TestForgetInstallAndNoCacheCheckInFull(t *testing.T) {
	engine, req, plan, _ := cachedArchiveInstall(t)
	archiveState(t, engine, req.DestRoot)
	engine.ForgetVerified(DriverCodex)
	if n := hashesDuring(t, func() { archiveState(t, engine, req.DestRoot) }); n == 0 {
		t.Fatal("a forgotten release was not checked again")
	}
	if _, _, err := engine.InstallV2(context.Background(), req, plan, io.Discard); err != nil {
		t.Fatal(err)
	}
	if n := hashesDuring(t, func() { archiveState(t, engine, req.DestRoot) }); n == 0 {
		t.Fatal("the read after an install was not a full check")
	}
	engine.verified = nil
	for i := 0; i < 2; i++ {
		if n := hashesDuring(t, func() { archiveState(t, engine, req.DestRoot) }); n == 0 {
			t.Fatalf("read %d without a cache was not a full check", i)
		}
	}
}

// Claude Code's release is checked against its retained, publisher-signed manifest
// (pinned key). A rewritten manifest is verified again; a tampered one is refused.
func TestVerifiedClaudeReleaseRechecksItsManifest(t *testing.T) {
	f := newFixture(t)
	f.engine.verified = NewVerifiedCache()
	f.publish(fxVersion)
	rec, _, err := f.install(fxVersion)
	if err != nil {
		t.Fatal(err)
	}
	claudeState := func() Installed {
		t.Helper()
		inv, err := f.engine.List(context.Background(), f.root)
		if err != nil || len(inv.Installed) != 1 {
			t.Fatalf("inventory %+v %v", inv, err)
		}
		return inv.Installed[0]
	}
	if in := claudeState(); in.State != StateInstalled {
		t.Fatalf("first read: %+v", in)
	}
	if n := hashesDuring(t, func() { claudeState() }); n != 0 {
		t.Fatalf("an unchanged Claude release hashed %d files", n)
	}
	manifest := filepath.Join(filepath.Dir(filepath.Dir(rec.Destination.Executable)), rec.Retained.Manifest)
	b, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	rewrite(t, manifest, b)
	if n := hashesDuring(t, func() {
		if in := claudeState(); in.State != StateInstalled {
			t.Fatalf("after rewriting the manifest: %+v", in)
		}
	}); n == 0 {
		t.Fatal("a rewritten manifest was not checked again")
	}
	rewrite(t, manifest, append(bytes.Clone(b), []byte(" ")...))
	for i := 0; i < 2; i++ {
		if in := claudeState(); in.State == StateInstalled {
			t.Fatalf("read %d of a tampered manifest: %+v", i, in)
		}
	}
}

func claudeState(t *testing.T, e *Engine, root string) Installed {
	t.Helper()
	inv, err := e.List(context.Background(), root)
	if err != nil || len(inv.Installed) != 1 {
		t.Fatalf("inventory %+v %v", inv, err)
	}
	return inv.Installed[0]
}

// SR-FH-CACHE-01: an entry holds only for the verification posture that made it.
// The host observer reads releases with an unavailable verifier; with the installing
// engine's verdict in the shared cache it must still say unverified, and the
// installing engine keeps its own entry.
func TestVerifiedEntryHoldsOnlyForItsVerifierPosture(t *testing.T) {
	f := newFixture(t)
	f.engine.verified = NewVerifiedCache()
	f.publish(fxVersion)
	if _, _, err := f.install(fxVersion); err != nil {
		t.Fatal(err)
	}
	if in := claudeState(t, f.engine, f.root); in.State != StateInstalled {
		t.Fatalf("verifying engine: %+v", in)
	}
	unavailable := NewClaudeWithTrust(ClaudeOptions{Verifier: UnavailableVerifier{}}, f.key.Public, f.key.Fingerprint)
	observer := NewEngine(NewCatalog(unavailable), EngineOptions{Verified: f.engine.verified})
	for i := 0; i < 2; i++ {
		if in := claudeState(t, observer, f.root); in.State != StateUnverified || in.Provenance != nil {
			t.Fatalf("read %d without a verifier took the cached verdict: %s %+v", i, in.State, in.Provenance)
		}
	}
	if n := hashesDuring(t, func() { claudeState(t, f.engine, f.root) }); n != 0 {
		t.Fatalf("the verifying engine lost its entry: %d files hashed", n)
	}
}

// A check reads the executable through a directory link the tools root contains,
// and the identity walk does not descend into a link: such a release is checked in
// full at every read, so a changed byte behind the link is refused.
func TestAReleaseReadThroughADirectoryLinkIsNeverKept(t *testing.T) {
	f := newFixture(t)
	f.engine.verified = NewVerifiedCache()
	f.publish(fxVersion)
	rec, _, err := f.install(fxVersion)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Dir(rec.Destination.Executable)
	moved := filepath.Join(f.root, ".moved-bin")
	if err := os.Rename(bin, moved); err != nil {
		t.Fatal(err)
	}
	target, err := filepath.Rel(filepath.Dir(bin), moved)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, bin); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if n := hashesDuring(t, func() {
			if in := claudeState(t, f.engine, f.root); in.State != StateInstalled {
				t.Fatalf("read %d through the link: %+v", i, in)
			}
		}); n == 0 {
			t.Fatalf("read %d through a directory link was served from memory", i)
		}
	}
	exe := filepath.Join(moved, filepath.Base(rec.Destination.Executable))
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)-2] ^= 1
	rewrite(t, exe, b)
	if in := claudeState(t, f.engine, f.root); in.State != StateDamaged {
		t.Fatalf("a changed byte behind the link: %s", in.State)
	}
}
