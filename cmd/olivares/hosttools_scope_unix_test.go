// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall/toolinstalltest"
	"github.com/olivaresai/olivares/modules/sessions"
)

// observeHostToolsBounded fails the test instead of hanging when an observation
// blocks (a FIFO opened by mistake would).
func observeHostToolsBounded(t *testing.T, obs *hostToolObserver, driver string) (sessions.HostToolObservation, error) {
	t.Helper()
	type result struct {
		res sessions.HostToolObservation
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := obs.ObserveHostTools(context.Background(), driver, "")
		done <- result{res, err}
	}()
	select {
	case r := <-done:
		return r.res, r.err
	case <-time.After(10 * time.Second):
		t.Fatal("the observation blocked")
		return sessions.HostToolObservation{}, nil
	}
}

func lockHostToolDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

// TestSessionRuntimeHostToolObserverUnobservableScopeIsNotAbsence is Root
// correction C1: Detect skips every Lstat error, so an empty result is absence
// only when every configured search location could be examined. Anything else is
// an observer error, which the read publishes as unknown.
func TestSessionRuntimeHostToolObserverUnobservableScopeIsNotAbsence(t *testing.T) {
	dir := toolinstalltest.ExecCapableDir(t)
	root := filepath.Join(dir, "tools-not-installed")
	emptyHome := filepath.Join(dir, "empty-home")
	emptyPath := filepath.Join(dir, "empty-path")
	for _, d := range []string{emptyHome, emptyPath} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	notDir := filepath.Join(dir, "notdir")
	if err := os.WriteFile(notDir, []byte("a regular file on PATH"), 0o644); err != nil {
		t.Fatal(err)
	}
	sep := string(os.PathListSeparator)

	t.Run("ordinary missing locations are absence", func(t *testing.T) {
		res, err := observeHostToolsBounded(t, newHostToolObserverAt(root, nil, emptyHome, emptyPath), "claude")
		if err != nil || res.UnsupportedDriver || len(res.Candidates) != 0 {
			t.Fatalf("missing locations: %+v %v", res, err)
		}
	})

	t.Run("ENOTDIR on a PATH entry is not absence", func(t *testing.T) {
		if _, err := os.Lstat(filepath.Join(notDir, "claude")); !errors.Is(err, syscall.ENOTDIR) {
			t.Fatalf("fixture is not ENOTDIR: %v", err)
		}
		res, err := observeHostToolsBounded(t, newHostToolObserverAt(root, nil, emptyHome, emptyPath+sep+notDir), "claude")
		if err == nil {
			t.Fatalf("an unexaminable PATH entry was published as a successful observation: %+v", res)
		}
		// The supported-driver decision still comes before filesystem errors.
		if res, err := observeHostToolsBounded(t, newHostToolObserverAt(root, nil, emptyHome, notDir), "codex"); err != nil || !res.UnsupportedDriver {
			t.Fatalf("codex with an ENOTDIR PATH: %+v %v", res, err)
		}
	})

	t.Run("an unenumerable versions location is not absence", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads through directory permissions")
		}
		home := filepath.Join(dir, "home-unenumerable")
		lockHostToolDir(t, filepath.Join(home, ".local", "share", "claude", "versions"))
		if res, err := observeHostToolsBounded(t, newHostToolObserverAt(root, nil, home, emptyPath), "claude"); err == nil {
			t.Fatalf("an unreadable versions directory was published as a successful observation: %+v", res)
		}
	})

	t.Run("an unexaminable vendor default path is not absence", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads through directory permissions")
		}
		home := filepath.Join(dir, "home-locked-bin")
		lockHostToolDir(t, filepath.Join(home, ".local", "bin"))
		if res, err := observeHostToolsBounded(t, newHostToolObserverAt(root, nil, home, emptyPath), "claude"); err == nil {
			t.Fatalf("an unexaminable vendor default path was published as a successful observation: %+v", res)
		}
	})

	t.Run("an unavailable home is not complete scope", func(t *testing.T) {
		t.Setenv("OLIVARES_DATA_DIR", filepath.Join(dir, "data"))
		t.Setenv("HOME", "")
		obs := newHostToolObserver(func(k string) string {
			if k == "PATH" {
				return emptyPath
			}
			return ""
		})
		if res, err := observeHostToolsBounded(t, obs, "claude"); err == nil {
			t.Fatalf("an unavailable home was published as a successful observation: %+v", res)
		}
		if res, err := observeHostToolsBounded(t, obs, "codex"); err != nil || !res.UnsupportedDriver {
			t.Fatalf("codex with an unavailable home: %+v %v", res, err)
		}
		if res, err := observeHostToolsBounded(t, newHostToolObserverAt(root, nil, "relative/home", emptyPath), "claude"); err == nil {
			t.Fatalf("a relative home was published as a successful observation: %+v", res)
		}
	})

	t.Run("dangling, nonregular and FIFO candidates are observations that never block", func(t *testing.T) {
		fifoDir := filepath.Join(dir, "path-fifo")
		danglingDir := filepath.Join(dir, "path-dangling")
		nonRegularDir := filepath.Join(dir, "path-directory")
		home := filepath.Join(dir, "home-fifo")
		versions := filepath.Join(home, ".local", "share", "claude", "versions")
		for _, d := range []string{fifoDir, danglingDir, filepath.Join(nonRegularDir, "claude"), versions} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := syscall.Mkfifo(filepath.Join(fifoDir, "claude"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(filepath.Join(versions, "9.9.9"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(danglingDir, "claude")); err != nil {
			t.Fatal(err)
		}
		res, err := observeHostToolsBounded(t, newHostToolObserverAt(root, nil, home, fifoDir+sep+danglingDir+sep+nonRegularDir), "claude")
		if err != nil || len(res.Candidates) != 4 {
			t.Fatalf("nonregular candidates: %+v %v", res, err)
		}
		origins := map[string]int{}
		for _, c := range res.Candidates {
			if c.Executable || c.Match != sessions.HostToolMatchUnregisteredObserved || c.Configured != sessions.HostToolCodeUnknown {
				t.Fatalf("nonregular candidate %+v", c)
			}
			origins[c.Origin]++
		}
		if origins[sessions.HostToolOriginPath] != 3 || origins[sessions.HostToolOriginVendorDefault] != 1 {
			t.Fatalf("origins %v", origins)
		}
	})

	t.Run("the versions location is the one the catalog enumerates", func(t *testing.T) {
		home := filepath.Join(dir, "home-drift")
		entry := filepath.Join(claudeVersionsDir(home), "1.2.3")
		if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(entry, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := newHostToolObserverAt(root, nil, home, emptyPath).engine.Catalog().Lookup("claude")
		if err != nil || !slices.Contains(p.DefaultPaths(home), entry) {
			t.Fatalf("claudeVersionsDir no longer names the directory DefaultPaths enumerates: %v", err)
		}
	})
}
