// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/release"
)

// cmd_release_channel_advance_test.go proves the CFG-06 monotonicity fence in BOTH
// directions, which is the only way a refusal control means anything: a fence that
// refuses everything passes every "it refuses" case and blocks every real release.
//
// The hazard it guards is specific to the FIRMA B carrier: GitHub picks the "latest
// release" by PUBLICATION ORDER, not by version, so a backport tag published after a
// newer one repoints the whole community channel at the older release with nobody doing
// anything wrong. The cases below are that mistake, its neighbours, and the two ways of
// not being able to answer.

// chanManifest builds a valid manifest for a channel/version (ParseManifest rejects one
// with no artifacts, so the fixture carries a real digest rather than a placeholder).
func chanManifest(t *testing.T, channel, version string) []byte {
	t.Helper()
	body := []byte("artifact-" + version)
	sum := sha256.Sum256(body)
	m := release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion,
		Channel:       channel,
		Version:       version,
		ReleasedAt:    time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC),
		Artifacts: []release.Artifact{{
			OS: "linux", Arch: "amd64", Filename: "olivares_" + version + "_linux_amd64.tar.gz",
			SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body)),
		}},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// chanManifestExpiring is chanManifest with a freshness bound, for the staleness case.
func chanManifestExpiring(t *testing.T, channel, version string, expires time.Time) []byte {
	t.Helper()
	var m release.Manifest
	if err := json.Unmarshal(chanManifest(t, channel, version), &m); err != nil {
		t.Fatal(err)
	}
	m.Expires = &expires
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeManifestFile(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "candidate-manifest.json")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// signer is one throwaway OTA key pair for a test. The fence AUTHENTICATES the live version
// before believing it, so every fixture that expects to be believed has to sign.
type signer struct {
	pub  string
	priv ed25519.PrivateKey
}

func newSigner(t *testing.T) signer {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return signer{pub: base64.StdEncoding.EncodeToString(pub), priv: priv}
}

func (s signer) sign(b []byte) string {
	return base64.StdEncoding.EncodeToString(release.SignManifest(b, s.priv))
}

// liveChannelOpts is how a fixture MISBEHAVES. Each field is one distinct way the live
// channel can be wrong, and each has a case below.
type liveChannelOpts struct {
	// status != 200 makes EVERY asset answer that status: an unpublished channel (404) or a
	// transport that cannot answer at all (5xx).
	status int
	// sigStatus, when non-zero, applies ONLY to the detached signature — the manifest still
	// answers 200. That is the SPLIT PAIR the first cut of the fence read as "nothing
	// published yet", and answered 0 to.
	sigStatus int
	// sig overrides the signature bytes; empty means "sign the manifest properly".
	sig string
}

// liveChannel serves a release-asset layout for one channel.
func liveChannel(t *testing.T, channel string, manifest []byte, sg signer, o liveChannelOpts) string {
	t.Helper()
	status := o.status
	if status == 0 {
		status = http.StatusOK
	}
	sig := o.sig
	if sig == "" {
		sig = sg.sign(manifest)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		isSig := strings.HasSuffix(r.URL.Path, "/"+channel+"-manifest.json.sig")
		if isSig && o.sigStatus != 0 {
			http.Error(w, http.StatusText(o.sigStatus), o.sigStatus)
			return
		}
		if status != http.StatusOK {
			http.Error(w, http.StatusText(status), status)
			return
		}
		switch {
		case isSig:
			_, _ = w.Write([]byte(sig))
		case strings.HasSuffix(r.URL.Path, "/"+channel+"-manifest.json"):
			_, _ = w.Write(manifest)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/olivaresai/olivares/releases/latest/download"
}

func runChannelAdvance(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newReleaseVerifyChannelAdvanceCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	t.Logf("release verify-channel-advance %v ->\n%s", args, buf.String())
	return buf.String(), err
}

func TestVerifyChannelAdvance(t *testing.T) {
	t.Run("a newer candidate ADVANCES the channel", func(t *testing.T) {
		sg := newSigner(t)
		endpoint := liveChannel(t, "stable", chanManifest(t, "stable", "26.8.0"), sg, liveChannelOpts{})
		cand := writeManifestFile(t, chanManifest(t, "stable", "26.8.1"))
		out, err := runChannelAdvance(t, "--candidate", cand, "--endpoint", endpoint, "--pubkey", sg.pub)
		if err != nil {
			t.Fatalf("a forward step must be accepted: %v\n%s", err, out)
		}
		if !strings.Contains(out, "ADVANCES") {
			t.Fatalf("the verdict must say it advances:\n%s", out)
		}
		if !strings.Contains(out, "signature: VERIFIED") {
			t.Fatalf("the run must say the live version was authenticated:\n%s", out)
		}
	})

	t.Run("re-publishing the SAME version is refused", func(t *testing.T) {
		sg := newSigner(t)
		endpoint := liveChannel(t, "stable", chanManifest(t, "stable", "26.8.0"), sg, liveChannelOpts{})
		cand := writeManifestFile(t, chanManifest(t, "stable", "26.8.0"))
		_, err := runChannelAdvance(t, "--candidate", cand, "--endpoint", endpoint, "--pubkey", sg.pub)
		if err == nil {
			t.Fatal("re-publishing the live version does not advance the channel and must be refused")
		}
		if code := exitcode.From(err); code != exitcode.Err {
			t.Fatalf("a refusal is a FINDING (%d), got exit %d: %v", exitcode.Err, code, err)
		}
	})

	t.Run("a BACKWARDS publication is refused and names the remedy", func(t *testing.T) {
		// The real shape of the hazard: 26.8.1 cut after 26.9.0 is what a security backport
		// looks like, and on GitHub Releases it becomes the channel head.
		sg := newSigner(t)
		endpoint := liveChannel(t, "stable", chanManifest(t, "stable", "26.9.0"), sg, liveChannelOpts{})
		cand := writeManifestFile(t, chanManifest(t, "stable", "26.8.1"))
		_, err := runChannelAdvance(t, "--candidate", cand, "--endpoint", endpoint, "--pubkey", sg.pub)
		if err == nil {
			t.Fatal("a publication that takes the channel backwards must be refused")
		}
		if !strings.Contains(err.Error(), "make_latest=false") {
			t.Fatalf("the refusal must name the lever that makes a backport safe, got: %v", err)
		}
		if code := exitcode.From(err); code != exitcode.Err {
			t.Fatalf("a regression is a FINDING (%d), got exit %d", exitcode.Err, code)
		}
	})

	t.Run("a channel with no manifest yet is a legitimate FIRST publication", func(t *testing.T) {
		sg := newSigner(t)
		endpoint := liveChannel(t, "stable", nil, sg, liveChannelOpts{status: http.StatusNotFound})
		cand := writeManifestFile(t, chanManifest(t, "stable", "26.8.0"))
		out, err := runChannelAdvance(t, "--candidate", cand, "--endpoint", endpoint, "--pubkey", sg.pub)
		if err != nil {
			t.Fatalf("the first publication of a channel must pass: %v\n%s", err, out)
		}
		if !strings.Contains(out, "FIRST publication") {
			t.Fatalf("the verdict must say WHY it passed:\n%s", out)
		}
	})

	t.Run("a MANIFEST that is live with a missing SIGNATURE is not a first publication", func(t *testing.T) {
		// ⛔ THE CASE THE CONTRAST FOUND, and the first cut answered 0 to it. Both fetches
		// returned through one error value, so a 404 on the SIGNATURE took the
		// first-publication shortcut — with a live manifest, and a version never compared.
		sg := newSigner(t)
		endpoint := liveChannel(t, "stable", chanManifest(t, "stable", "26.9.0"), sg,
			liveChannelOpts{sigStatus: http.StatusNotFound})
		cand := writeManifestFile(t, chanManifest(t, "stable", "26.8.1"))
		out, err := runChannelAdvance(t, "--candidate", cand, "--endpoint", endpoint, "--pubkey", sg.pub)
		if err == nil {
			t.Fatalf("a split pair must not be read as an unpublished channel:\n%s", out)
		}
		if code := exitcode.From(err); code != exitcode.Indeterminate {
			t.Fatalf("want COULD NOT LOOK (%d), got exit %d: %v", exitcode.Indeterminate, code, err)
		}
		if !strings.Contains(err.Error(), "SPLIT PAIR") {
			t.Fatalf("the refusal must name what it saw, got: %v", err)
		}
	})

	t.Run("an unreadable channel is COULD NOT LOOK, never a first publication", func(t *testing.T) {
		// NON-FIRING DIRECTION for the 404 shortcut. A 500, a 403 or a proxy error must NOT be
		// read as "nothing published yet" — that reading would let a fence with no network at
		// all bless every publication it was asked about.
		for _, status := range []int{http.StatusInternalServerError, http.StatusForbidden, http.StatusBadGateway} {
			sg := newSigner(t)
			endpoint := liveChannel(t, "stable", nil, sg, liveChannelOpts{status: status})
			cand := writeManifestFile(t, chanManifest(t, "stable", "26.8.0"))
			out, err := runChannelAdvance(t, "--candidate", cand, "--endpoint", endpoint, "--pubkey", sg.pub)
			if err == nil {
				t.Fatalf("HTTP %d must not be read as a clean answer:\n%s", status, out)
			}
			if code := exitcode.From(err); code != exitcode.Indeterminate {
				t.Fatalf("HTTP %d must be COULD NOT LOOK (%d), got exit %d: %v",
					status, exitcode.Indeterminate, code, err)
			}
		}
	})

	t.Run("an UNAUTHENTICATED live version is never compared", func(t *testing.T) {
		// ⛔ THE ONE THAT REFUTES A COMMENT I WROTE. The first cut made --pubkey optional and
		// argued the failure direction was safe because a forged NEWER live version can only
		// cause a refusal. The other direction is the dangerous one: a forged or replayed
		// OLDER live version makes the fence report an advance while the real head is newer.
		// Here the live channel CLAIMS 26.8.0 under a key that is not ours, while a real head
		// of 26.9.0 would refuse the candidate.
		ours := newSigner(t)
		theirs := newSigner(t)
		endpoint := liveChannel(t, "stable", chanManifest(t, "stable", "26.8.0"), theirs, liveChannelOpts{})
		cand := writeManifestFile(t, chanManifest(t, "stable", "26.8.1"))
		out, err := runChannelAdvance(t, "--candidate", cand, "--endpoint", endpoint, "--pubkey", ours.pub)
		if err == nil {
			t.Fatalf("a live manifest signed by another key must not be compared:\n%s", out)
		}
		if code := exitcode.From(err); code != exitcode.Indeterminate {
			t.Fatalf("want COULD NOT LOOK (%d), got exit %d: %v", exitcode.Indeterminate, code, err)
		}
	})

	t.Run("an EXPIRED live manifest is not a head to compare against", func(t *testing.T) {
		// VerifyManifest authenticates and parses but does not evaluate Expires, and both
		// other readers of this channel call Stale. Without this, an old head replayed past
		// its own freshness bound produces the same false advance the signature check exists
		// to prevent.
		sg := newSigner(t)
		expired := chanManifestExpiring(t, "stable", "26.9.0", time.Now().UTC().Add(-1*time.Hour))
		endpoint := liveChannel(t, "stable", expired, sg, liveChannelOpts{})
		cand := writeManifestFile(t, chanManifest(t, "stable", "26.8.1"))
		_, err := runChannelAdvance(t, "--candidate", cand, "--endpoint", endpoint, "--pubkey", sg.pub)
		if err == nil {
			t.Fatal("an expired live manifest must not be compared")
		}
		if code := exitcode.From(err); code != exitcode.Indeterminate {
			t.Fatalf("want COULD NOT LOOK (%d), got exit %d: %v", exitcode.Indeterminate, code, err)
		}
		if !strings.Contains(err.Error(), "EXPIRED") {
			t.Fatalf("the refusal must say what it saw, got: %v", err)
		}
		// NON-FIRING: the same manifest with a live freshness bound still compares.
		fresh := chanManifestExpiring(t, "stable", "26.9.0", time.Now().UTC().Add(24*time.Hour))
		ep2 := liveChannel(t, "stable", fresh, sg, liveChannelOpts{})
		_, err = runChannelAdvance(t, "--candidate", cand, "--endpoint", ep2, "--pubkey", sg.pub)
		if code := exitcode.From(err); code != exitcode.Err {
			t.Fatalf("a fresh 26.9.0 head must REFUSE a 26.8.1 candidate as a regression (%d), got %d: %v",
				exitcode.Err, code, err)
		}
	})

	t.Run("a PINNED endpoint is refused: it is not the channel head", func(t *testing.T) {
		// The command's subject is the head. Pointed at one release it would compare two
		// authentic versions, neither of them the live one — the same false green by another
		// route, and the command's own Example used to show exactly that.
		cand := writeManifestFile(t, chanManifest(t, "stable", "26.8.1"))
		_, err := runChannelAdvance(t, "--candidate", cand,
			"--endpoint", "https://github.com/acme/widget/releases/tag/v26.8.0", "--pubkey", newSigner(t).pub)
		if err == nil {
			t.Fatal("a pinned endpoint must be refused")
		}
		if code := exitcode.From(err); code != exitcode.Usage {
			t.Fatalf("a wrong endpoint is a USAGE error (%d), got exit %d: %v", exitcode.Usage, code, err)
		}
		if !strings.Contains(err.Error(), "CHANNEL HEAD") {
			t.Fatalf("the refusal must say why, got: %v", err)
		}
	})

	t.Run("a live manifest for ANOTHER channel is COULD NOT LOOK", func(t *testing.T) {
		sg := newSigner(t)
		endpoint := liveChannel(t, "stable", chanManifest(t, "security", "26.9.0"), sg, liveChannelOpts{})
		cand := writeManifestFile(t, chanManifest(t, "stable", "26.8.1"))
		_, err := runChannelAdvance(t, "--candidate", cand, "--endpoint", endpoint, "--pubkey", sg.pub)
		if err == nil {
			t.Fatal("a live manifest signed for another channel must not be compared")
		}
		if code := exitcode.From(err); code != exitcode.Indeterminate {
			t.Fatalf("want COULD NOT LOOK (%d), got exit %d: %v", exitcode.Indeterminate, code, err)
		}
	})

	t.Run("bad inputs are USAGE errors, not findings", func(t *testing.T) {
		// The table reserves 1 for "I compared and found a regression". An unreadable
		// candidate is not that, and exiting 1 for it told the caller the wrong thing.
		sg := newSigner(t)
		endpoint := liveChannel(t, "stable", chanManifest(t, "stable", "26.8.0"), sg, liveChannelOpts{})
		cases := [][]string{
			{"--endpoint", endpoint, "--pubkey", sg.pub},
			{"--candidate", "/nonexistent/manifest.json", "--endpoint", endpoint, "--pubkey", sg.pub},
			{"--candidate", writeManifestFile(t, chanManifest(t, "security", "26.8.1")),
				"--endpoint", endpoint, "--channel", "stable", "--pubkey", sg.pub},
			{"--candidate", writeManifestFile(t, chanManifest(t, "stable", "26.8.1")),
				"--endpoint", "https://github.com/acme", "--pubkey", sg.pub},
		}
		for i, args := range cases {
			_, err := runChannelAdvance(t, args...)
			if err == nil {
				t.Fatalf("case %d must fail: %v", i, args)
			}
			if code := exitcode.From(err); code != exitcode.Usage {
				t.Fatalf("case %d must exit USAGE (%d), got %d: %v", i, exitcode.Usage, code, err)
			}
		}
	})
}
