// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/license"
)

// cmd_upgrade_bundle_gate_test.go is the C02-20 witness for the license gate on the
// --bundle route, IN BOTH DIRECTIONS over ONE bundle.
//
// What it is about. `buildUpdateSource` had the license check on the worker route only:
// the bundle branch returned its source before requireValidLicense ran, so a tarball
// installed with no credential, no token and no network, and --help advertised exactly
// that ("100% offline"). One direction is easy to witness and it is the one that gets
// written — refuse without a license. The half that breaks the operator who paid is
// PERMIT: the same bundle, with a live license, must still install, and must still reach
// no network. Both are asserted here, over the SAME fixture and the SAME bundle bytes, so
// the only input that differs between the two verdicts is what is in the data dir. The
// fourth arm pins the carve-out: --check is not gated, and it changes nothing.
//
// Why each refusal assertion names the message. A test that only asserts "it errored"
// passes for a bad signature, a stale manifest, an unwritable target or a mistyped path —
// every one of which fails EARLIER or ELSEWHERE than the guard this file is named after.
// The bundle used below verifies, is a forward version step and points at a writable
// target, so the license is the only reason left to refuse; asserting the message keeps
// it that way if a future guard ever moves in front.
func TestBundleInstallIsGatedOnALiveLicense(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build stub binaries")
	}
	// THE PREMISE IS ASSERTED, NOT SKIPPED. Both directions depend on this build being
	// able to verify a license at all: with no embedded key requireValidLicense refuses
	// EVERY license, which would make the two DENY arms pass for a reason that has
	// nothing to do with the bundle route and the PERMIT arm fail for the same reason. A
	// skip here would report "gate verified" as "nothing to see".
	if len(license.DefaultPublicKey()) == 0 {
		t.Fatal("this build embeds no license verification key, so the bundle gate cannot be witnessed in either direction")
	}
	if len(license.DevPrivateKey()) != ed25519.PrivateKeySize {
		t.Fatal("this build embeds no dev license key, so no live license can be installed for the PERMIT arm")
	}

	v1 := buildStub(t, "26.7.0")
	v2 := buildStub(t, "26.8.0")
	// ONE fixture, ONE bundle, shared by all three arms: the bundle bytes are identical
	// in the refusals and in the install, so "the bundle was fine" is not an assumption.
	f := newUpdFixture(t, "26.8.0", "26.6.0", v2)
	bundle := f.writeBundle(t)

	// writeLicense installs a signed license with the given term into a fresh data dir.
	writeLicense := func(t *testing.T, expiresAt time.Time) string {
		t.Helper()
		dir := t.TempDir()
		blob, err := license.Sign(license.Claims{
			Licensee: "C02-20 Witness", Plan: "enterprise",
			IssuedAt: time.Now().UTC().Add(-48 * time.Hour), ExpiresAt: expiresAt,
		}, license.DevPrivateKey())
		if err != nil {
			t.Fatalf("sign license: %v", err)
		}
		if err := os.WriteFile(licenseDataDirPath(dir), []byte(blob+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	// nothingInstalled is the "sin instalar nada" half of a refusal: the binary at the
	// target still runs the OLD version and no backup was left behind. An error return
	// alone does not prove that — atomicSwap keeps a .bak, so a swap that happened and
	// then failed would still be visible here.
	nothingInstalled := func(t *testing.T, target string) {
		t.Helper()
		if got := runsVersion(t, target); !strings.Contains(got, "26.7.0") {
			t.Fatalf("the target was changed by a refused --bundle install: %q", got)
		}
		if b, _ := filepath.Glob(target + ".bak-*"); len(b) != 0 {
			t.Fatalf("a refused --bundle install must leave no backup: %v", b)
		}
	}

	t.Run("DENY: a verifying bundle with NO license installs nothing", func(t *testing.T) {
		target := writeTarget(t, v1)
		_, err := runUpgradeCmd(t, "--bundle", bundle, "--pubkey", f.pubB64,
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", t.TempDir())
		if err == nil {
			t.Fatal("--bundle with no installed license must refuse: this is the vector — whoever holds the tarball installs from it")
		}
		for _, want := range []string{"--bundle", "no license installed", "olivares license install"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("the refusal must name the guard and the way out; %q does not contain %q", err.Error(), want)
			}
		}
		nothingInstalled(t, target)
	})

	t.Run("DENY: an EXPIRED license installs nothing (the gate reads liveness, not presence)", func(t *testing.T) {
		target := writeTarget(t, v1)
		dataDir := writeLicense(t, time.Now().UTC().Add(-time.Hour)) // term already over
		_, err := runUpgradeCmd(t, "--bundle", bundle, "--pubkey", f.pubB64,
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", dataDir)
		if err == nil {
			t.Fatal("--bundle with an EXPIRED license must refuse: a gate that accepts any signed blob on disk checks presence, not a live term")
		}
		for _, want := range []string{"--bundle", "EXPIRED"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("the refusal must name the guard and the state; %q does not contain %q", err.Error(), want)
			}
		}
		nothingInstalled(t, target)
	})

	t.Run("PERMIT: the same bundle with a live license installs, and reaches no endpoint", func(t *testing.T) {
		target := writeTarget(t, v1)
		dataDir := writeLicense(t, time.Now().UTC().Add(365*24*time.Hour))

		// The air gap, asserted instead of commented. --endpoint is the ONLY host this
		// invocation is given, and the bundle route must never call it; a counter here
		// says so in the test's own output rather than in a claim about the code. (It
		// bounds what this arm can prove: no request reached the endpoint it was handed.)
		var hits int64
		spy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt64(&hits, 1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer spy.Close()

		_, err := runUpgradeCmd(t, "--bundle", bundle, "--pubkey", f.pubB64, "--endpoint", spy.URL,
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", dataDir)
		if err != nil {
			t.Fatalf("a live license must still install from a bundle — this is the half that breaks the operator who paid: %v", err)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.8.0") {
			t.Fatalf("bundle target not upgraded: %q", got)
		}
		if b, _ := filepath.Glob(target + ".bak-*"); len(b) != 1 || !strings.Contains(runsVersion(t, b[0]), "26.7.0") {
			t.Fatalf("the rollback backup is missing or wrong: %v", b)
		}
		if n := atomic.LoadInt64(&hits); n != 0 {
			t.Fatalf("the gated bundle route contacted the endpoint %d time(s); it must stay air-gapped", n)
		}
	})

	// THE CARVE-OUT, ASSERTED IN BOTH OF ITS HALVES. --check is deliberately NOT gated:
	// what the gate closes is an unlicensed INSTALL, and --check installs nothing. Three
	// callers depend on it — the release ceremony's updater smoke test, the key-domain
	// battery mainline-ci runs, and the documented habit of checking a bundle first — and
	// a gate that broke them would teach operators to skip straight to --yes.
	//
	// Both halves matter here. That --check RUNS is one assertion; that it CHANGES NOTHING
	// is the other, and it is the one that keeps the carve-out honest, because the reason
	// --check may skip the gate is precisely that it never installs. If that ever stops
	// being true, this is the test that goes red.
	t.Run("PERMIT: --bundle --check needs no license, and changes nothing", func(t *testing.T) {
		target := writeTarget(t, v1)
		out, err := runUpgradeCmd(t, "--bundle", bundle, "--pubkey", f.pubB64, "--check",
			"--target", target, "--os", "linux", "--arch", "amd64", "--data-dir", t.TempDir())
		if err != nil {
			t.Fatalf("--bundle --check must not need a license — the release ceremony and mainline-ci both run it that way: %v", err)
		}
		if !strings.Contains(out, "upgrade available") {
			t.Fatalf("--check must still verify the bundle and report the plan, got:\n%s", out)
		}
		nothingInstalled(t, target)
	})
}
