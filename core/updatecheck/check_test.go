// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package updatecheck

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/release"
)

func serveManifest(t *testing.T, version string, sec bool, priv ed25519.PrivateKey, badSig bool) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256([]byte("artifact"))
	m := release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion, Channel: release.ChannelStable, Version: version,
		ReleasedAt: time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC), Security: sec, Advisories: []string{"OSV-2026-1"},
		Artifacts: []release.Artifact{{OS: "linux", Arch: "amd64", Filename: "a.tgz", SHA256: hex.EncodeToString(sum[:])}},
	}
	mb, _ := json.Marshal(m)
	sig := release.SignManifest(mb, priv)
	if badSig {
		sig = ed25519.Sign(priv, []byte("other"))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/stable/manifest.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(mb) })
	mux.HandleFunc("/stable/manifest.json.sig", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(sig)))
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func TestCheckAirGapIsSilent(t *testing.T) {
	st := Check(context.Background(), Config{CurrentVersion: "26.7.0"}) // no endpoint, no key
	if st.Enabled {
		t.Fatal("with no endpoint/key, checking must be disabled (air-gap silence, not error)")
	}
	if st.Error != "" {
		t.Fatalf("air-gap must not surface an error, got %q", st.Error)
	}
}

func TestCheckAvailableAndUpToDate(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	srv := serveManifest(t, "26.8.0", true, priv, false)
	cfg := Config{Endpoint: srv.URL, Channel: "stable", PubKey: pub, InstallID: "n1"}

	// current older -> available, security + advisories propagate.
	cfg.CurrentVersion = "26.7.0"
	st := Check(context.Background(), cfg)
	if !st.Enabled || !st.Available || st.UpToDate {
		t.Fatalf("older current must see an available update: %+v", st)
	}
	if st.LatestVersion != "26.8.0" || !st.Security || len(st.Advisories) != 1 {
		t.Fatalf("available update must carry version/security/advisories: %+v", st)
	}

	// current equal -> up to date, no security noise.
	cfg.CurrentVersion = "26.8.0"
	st = Check(context.Background(), cfg)
	if st.Available || !st.UpToDate {
		t.Fatalf("equal current must be up to date: %+v", st)
	}
	if st.Security || len(st.Advisories) != 0 {
		t.Fatalf("up-to-date must not advertise a security fix: %+v", st)
	}

	// current newer than channel -> still up to date (never a rollback nudge).
	cfg.CurrentVersion = "26.9.0"
	st = Check(context.Background(), cfg)
	if st.Available || !st.UpToDate {
		t.Fatalf("newer-than-channel must be up to date: %+v", st)
	}
}

// TestCheckUnstampedBuildMakesNoClaim pins the fail-open one package over from the
// one that opened that session. An unstamped build ("dev", or empty) parses to the ZERO
// Version, which sits BELOW every release — so reading plan.Direction told a source build
// that a release was "available" even when the channel was serving something OLDER than
// what it was running. The indicator is a notification, not the CLI's verdict, and the
// honest answer to "are you behind?" from a binary with no version is that it cannot say.
func TestCheckUnstampedBuildMakesNoClaim(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	// The channel serves an OLDER release than any plausible source build.
	srv := serveManifest(t, "26.7.0", true, priv, false)
	cfg := Config{Endpoint: srv.URL, Channel: "stable", PubKey: pub, InstallID: "n1"}

	for _, unstamped := range []string{"dev", "", "  ", "vdev"} {
		cfg.CurrentVersion = unstamped
		st := Check(context.Background(), cfg)
		if st.Available {
			t.Errorf("CurrentVersion=%q has no position in the ordering; it must not be told an update is available: %+v", unstamped, st)
		}
		if st.UpToDate {
			t.Errorf("CurrentVersion=%q cannot be up to date either — both answers are unavailable: %+v", unstamped, st)
		}
		if !strings.Contains(st.Error, "no version stamp") {
			t.Errorf("CurrentVersion=%q must report WHY it cannot compare, got Error=%q", unstamped, st.Error)
		}
		if st.Security || len(st.Advisories) != 0 {
			t.Errorf("a build that cannot be compared must not be advertised a security fix: %+v", st)
		}
	}

	// Control: a stamped build behind the channel still gets a real answer.
	cfg.CurrentVersion = "26.6.0"
	if st := Check(context.Background(), cfg); !st.Available || st.Error != "" {
		t.Fatalf("a stamped, older build must still see the update: %+v", st)
	}
}

func TestCheckVerifyFailureIsCapturedNotFatal(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	srv := serveManifest(t, "26.8.0", true, priv, true) // bad signature
	st := Check(context.Background(), Config{Endpoint: srv.URL, PubKey: pub, CurrentVersion: "26.7.0"})
	if !st.Enabled {
		t.Fatal("a configured check stays Enabled even when it fails")
	}
	if st.Error == "" || st.Available {
		t.Fatalf("a bad signature must set Error and NOT report an available update: %+v", st)
	}

	// Wrong key -> also captured, not fatal.
	otherPub, _, _ := ed25519.GenerateKey(nil)
	st = Check(context.Background(), Config{Endpoint: srv.URL, PubKey: otherPub, CurrentVersion: "26.7.0"})
	if st.Error == "" {
		t.Fatal("wrong key must be captured as an error")
	}
}

func TestCheckStaleManifestIsNotUpToDate(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	// A validly-SIGNED but EXPIRED manifest must not be reported as a trustworthy
	// "up to date" — a frozen/stale mirror should surface as a failed check.
	past := time.Now().UTC().Add(-time.Hour)
	sum := sha256.Sum256([]byte("artifact"))
	m := release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion, Channel: release.ChannelStable, Version: "26.8.0",
		ReleasedAt: past.Add(-24 * time.Hour), Expires: &past,
		Artifacts: []release.Artifact{{OS: "linux", Arch: "amd64", Filename: "a.tgz", SHA256: hex.EncodeToString(sum[:])}},
	}
	mb, _ := json.Marshal(m)
	sig := release.SignManifest(mb, priv)
	mux := http.NewServeMux()
	mux.HandleFunc("/stable/manifest.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(mb) })
	mux.HandleFunc("/stable/manifest.json.sig", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(sig)))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	st := Check(context.Background(), Config{Endpoint: srv.URL, PubKey: pub, CurrentVersion: "26.7.0"})
	if st.Available || st.UpToDate {
		t.Fatalf("an expired manifest must not report available/up-to-date: %+v", st)
	}
	if st.Error == "" {
		t.Fatalf("an expired manifest must surface an error, got %+v", st)
	}
}

func TestCheckerCaches(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	srv := serveManifest(t, "26.8.0", false, priv, false)
	c := NewChecker(Config{Endpoint: srv.URL, PubKey: pub, CurrentVersion: "26.7.0", InstallID: "n1"}, time.Hour)
	// Before refresh: Enabled reflects config, no result yet.
	if !c.Latest().Enabled {
		t.Fatal("configured checker should report Enabled before first refresh")
	}
	got := c.Refresh(context.Background())
	if !got.Available || got.LatestVersion != "26.8.0" {
		t.Fatalf("refresh should populate the available update: %+v", got)
	}
	if !strings.Contains(c.Latest().LatestVersion, "26.8.0") {
		t.Fatalf("Latest must return the cached refresh: %+v", c.Latest())
	}
}

// TestCheckReadsTheReleaseAssetLayout is the regression detector this package did not have.
//
// ⛔ WHY IT MATTERS HERE AND NOT ONLY IN core/release. This is the SECOND reader of a public
// update channel, and until 2026-08-27 it spelled the layout out itself as
// `<endpoint>/<channel>/manifest.json`. That string is gone — the layout is resolved once, for
// both readers — but every test in this file serves the DIRECTORY layout, so a regression that
// put the old concatenation back would keep them all green while this reader 404-ed against the
// carrier the product actually ships with. The external contrast named the gap: the resolver's
// own tests prove the resolver, the CLI's tests prove `upgrade`, and nothing here would fail.
func TestCheckReadsTheReleaseAssetLayout(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	sum := sha256.Sum256([]byte("artifact"))
	m := release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion, Channel: release.ChannelStable, Version: "26.9.0",
		ReleasedAt: time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC),
		Artifacts: []release.Artifact{{OS: "linux", Arch: "amd64", Filename: "a.tgz",
			SHA256: hex.EncodeToString(sum[:])}},
	}
	mb, _ := json.Marshal(m)
	sig := base64.StdEncoding.EncodeToString(release.SignManifest(mb, priv))

	// FLAT release assets, and NOTHING is registered under the directory layout: if this reader
	// ever concatenates `<endpoint>/stable/manifest.json` again, it gets a 404 and this fails.
	var seen []string
	mux := http.NewServeMux()
	base := "/example-owner/example-repo/releases/latest/download"
	mux.HandleFunc(base+"/stable-manifest.json", func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		_, _ = w.Write(mb)
	})
	mux.HandleFunc(base+"/stable-manifest.json.sig", func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		_, _ = w.Write([]byte(sig))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		http.Error(w, "not found", http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	st := Check(context.Background(), Config{
		Endpoint: srv.URL + base, Channel: "stable", PubKey: pub,
		CurrentVersion: "26.8.0", InstallID: "n1",
	})
	if st.Error != "" {
		t.Fatalf("the release-asset layout must be readable by this checker, got error %q (paths: %v)", st.Error, seen)
	}
	if !st.Enabled || st.LatestVersion != "26.9.0" || st.UpToDate {
		t.Fatalf("status = %+v, want an available 26.9.0 over a running 26.8.0", st)
	}
	for _, p := range seen {
		if p == "/stable/manifest.json" || p == "/stable/manifest.json.sig" {
			t.Fatalf("this reader asked for the DIRECTORY layout (%s): the layout must come from release.ResolveChannel", p)
		}
	}
}
