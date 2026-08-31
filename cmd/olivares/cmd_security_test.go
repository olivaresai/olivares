// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/secadvisory"
)

// runSecurityCheck drives `security check` through the real command tree and returns its
// combined output and error, exactly as main() would see it.
func runSecurityCheck(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newSecurityCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(append([]string{"check"}, args...))
	err := cmd.Execute()
	t.Logf("security check %v ->\n%s", args, buf.String())
	return buf.String(), err
}

// writeSignedFeed signs a feed of one advisory (introduced 0, fixed `fixedIn`) and writes
// <dir>/advisories.json + .sig, returning the feed path and the base64 public key.
func writeSignedFeed(t *testing.T, dir, id, fixedIn string) (feedPath, pubB64 string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	feed := secadvisory.NewFeed("security@olivares.ai", time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC), []secadvisory.Advisory{{
		SchemaVersion: "1.6.0", ID: id, Modified: "2026-07-09T00:00:00Z",
		Summary:  "test advisory",
		Severity: []secadvisory.Severity{{Type: secadvisory.SeverityCVSS3, Score: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"}},
		Affected: []secadvisory.Affected{{
			Package: secadvisory.Package{Ecosystem: secadvisory.EcosystemGo, Name: productModule},
			Ranges:  []secadvisory.Range{{Type: secadvisory.RangeSemver, Events: []secadvisory.Event{{Introduced: "0"}, {Fixed: fixedIn}}}},
		}},
		References: []secadvisory.Ref{{Type: "ADVISORY", URL: "https://example.test/" + id}},
	}})
	fb, sig, err := feed.Sign(priv)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	feedPath = filepath.Join(dir, "advisories.json")
	if err := os.WriteFile(feedPath, fb, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(feedPath+".sig", sig, 0o600); err != nil {
		t.Fatal(err)
	}
	return feedPath, base64.StdEncoding.EncodeToString(pub)
}

// TestSecurityCheckWireProof is the wire-proof: the `olivares security check` command
// is wired into the production command tree and, against a signed OSV feed, correctly
// reports an affected version (non-zero exit) and a safe version (clean exit) — all
// offline, and refusing a tampered feed.
func TestSecurityCheckWireProof(t *testing.T) {
	// The command must be reachable from the real root, not only constructible here.
	root := newRootCmd()
	if _, _, err := root.Find([]string{"security", "check"}); err != nil {
		t.Fatalf("`security check` is not wired into the production command tree: %v", err)
	}

	// Pin the running version to a real semver for the duration of the test; the
	// build-time default ("dev") is not a semver. Restore it afterwards.
	prev := version
	version = "26.7.0"
	t.Cleanup(func() { version = prev })

	dir := t.TempDir()
	feedPath, pubB64 := writeSignedFeed(t, dir, "GHSA-test-affected", "26.7.2") // fix above 26.7.0

	t.Run("affected", func(t *testing.T) {
		out, err := runSecurityCheck(t, "--feed", feedPath, "--pubkey", pubB64)
		if !errors.Is(err, errAffected) {
			t.Fatalf("affected version should return errAffected, got %v", err)
		}
		if !strings.Contains(out, "AFFECTED") || !strings.Contains(out, "GHSA-test-affected") {
			t.Fatalf("output did not report the advisory:\n%s", out)
		}
		if !strings.Contains(out, "fixed in 26.7.2") {
			t.Errorf("output did not name the fix version:\n%s", out)
		}
	})

	t.Run("not_affected", func(t *testing.T) {
		// Running 26.7.0 with a feed fixed at 26.6.0 — the running version is already past
		// the fix, so nothing affects it. The command still verifies the feed offline.
		safeFeed, safePub := writeSignedFeed(t, t.TempDir(), "GHSA-test-safe", "26.6.0")
		out, err := runSecurityCheck(t, "--feed", safeFeed, "--pubkey", safePub)
		if err != nil {
			t.Fatalf("safe version should exit clean, got err=%v\n%s", err, out)
		}
		if !strings.Contains(out, "no known advisory affects this version") {
			t.Fatalf("output did not confirm safety:\n%s", out)
		}
	})

	t.Run("tampered_feed_rejected", func(t *testing.T) {
		tamperedDir := t.TempDir()
		tf, tpub := writeSignedFeed(t, tamperedDir, "GHSA-tamper", "999.0.0")
		// Corrupt the feed after signing.
		b, _ := os.ReadFile(tf)
		b[len(b)/2] ^= 0xff
		_ = os.WriteFile(tf, b, 0o600)
		_, err := runSecurityCheck(t, "--feed", tf, "--pubkey", tpub)
		if err == nil || errors.Is(err, errAffected) {
			t.Fatalf("a tampered feed must fail verification, got err=%v", err)
		}
		if !strings.Contains(err.Error(), "did not verify") {
			t.Errorf("expected a verification error, got: %v", err)
		}
	})
}
