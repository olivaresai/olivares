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

// serveManifestOnChannel publishes a VALIDLY SIGNED manifest whose own channel field is
// `signedAs`, at the path for `servedAt`. That split is the whole point: the two are the
// same in an honest deployment and differ in every case this file exists for.
func serveManifestOnChannel(t *testing.T, signedAs, servedAt, version string, priv ed25519.PrivateKey) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256([]byte("artifact"))
	m := release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion, Channel: signedAs, Version: version,
		ReleasedAt: time.Now().UTC().Add(-time.Hour),
		Security:   signedAs == release.ChannelSecurity,
		Artifacts:  []release.Artifact{{OS: "linux", Arch: "amd64", Filename: "a.tgz", SHA256: hex.EncodeToString(sum[:])}},
	}
	mb, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sig := release.SignManifest(mb, priv)
	mux := http.NewServeMux()
	mux.HandleFunc("/"+servedAt+"/manifest.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(mb) })
	mux.HandleFunc("/"+servedAt+"/manifest.json.sig", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(sig)))
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// A VALID SIGNATURE IS NOT AN ANSWER TO THE QUESTION THAT WAS ASKED.
//
// stable, security and lts are signed by the SAME key, so an authentic stable manifest
// satisfies every cryptographic check a `security` request makes. Before this indicator
// read the channel only to print it, so an estate configured to watch the security line was
// shown a calm "up to date" computed from the stable line — no error, no warning, and nobody
// typed a command to notice.
//
// The failure is quiet by construction, which is why it needs a test rather than review:
// every assertion that existed still passed.
func TestCheckRefusesAManifestSignedForAnotherChannel(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	// The endpoint answers the SECURITY path with a genuine STABLE manifest — a stale
	// mirror, a misrouted gate, or the wrong air-gap bundle all produce exactly this.
	srv := serveManifestOnChannel(t, release.ChannelStable, release.ChannelSecurity, "26.9.0", priv)

	st := Check(context.Background(), Config{
		Endpoint: srv.URL, Channel: release.ChannelSecurity, PubKey: pub,
		CurrentVersion: "26.8.0", InstallID: "n1",
	})

	if st.Error == "" {
		t.Fatal("a manifest signed for another channel was accepted silently")
	}
	if !strings.Contains(st.Error, release.ChannelSecurity) || !strings.Contains(st.Error, release.ChannelStable) {
		t.Fatalf("the error must name BOTH channels so the operator can tell which is which, got %q", st.Error)
	}
	// The two claims must stay FALSE. An error that still reported "available" would be
	// worse than silence: it would act on the wrong channel's version.
	if st.Available || st.UpToDate {
		t.Fatalf("a wrong-channel answer must make no claim: available=%v uptodate=%v", st.Available, st.UpToDate)
	}
	// And it must NOT be reported as a security update, which is what the stable manifest's
	// own fields would have produced had it been accepted.
	if st.Security {
		t.Fatal("a refused check must not carry the rejected manifest's security flag")
	}
}

// THE NOT-FIRING DIRECTION, and it is what stops the fix from being an outage. A check that
// refused everything would satisfy the test above perfectly.
func TestCheckAcceptsTheChannelItAskedFor(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	srv := serveManifestOnChannel(t, release.ChannelSecurity, release.ChannelSecurity, "26.9.0", priv)

	st := Check(context.Background(), Config{
		Endpoint: srv.URL, Channel: release.ChannelSecurity, PubKey: pub,
		CurrentVersion: "26.8.0", InstallID: "n1",
	})

	if st.Error != "" {
		t.Fatalf("the matching channel was refused: %s", st.Error)
	}
	if !st.Available {
		t.Fatalf("a newer release on the requested channel must be reported available: %+v", st)
	}
	if !st.Security {
		t.Fatal("a security-channel manifest must still surface as a security update")
	}
}

// The default. An empty Config.Channel means stable (check.go normalises it), so a stable
// manifest must satisfy it — otherwise the binding would break every default deployment,
// which is the most likely way a change like this ships an outage.
func TestCheckDefaultChannelIsStableAndStillAccepted(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	srv := serveManifestOnChannel(t, release.ChannelStable, release.ChannelStable, "26.9.0", priv)

	st := Check(context.Background(), Config{
		Endpoint: srv.URL, PubKey: pub, CurrentVersion: "26.8.0", InstallID: "n1",
	})

	if st.Error != "" {
		t.Fatalf("the default (stable) channel was refused: %s", st.Error)
	}
	if !st.Available {
		t.Fatalf("default channel must still resolve normally: %+v", st)
	}
}
