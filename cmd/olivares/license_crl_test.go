// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/release"
)

// license_crl_test.go covers the CRL observation store (§5.2): the
// first-observed clock must survive later manifests (the enterprise 14-day
// observation clock never resets while an identity stays listed), un-revoking
// clears it, an OLDER/replayed manifest cannot roll the CRL back (anti-freeze),
// and a corrupt store reads as UNAVAILABLE and is never silently reset.

func manifestRevoking(rs *release.RevokedSet, releasedAt time.Time) release.Manifest {
	return release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion,
		Channel:       release.ChannelStable,
		Version:       "26.8.0",
		ReleasedAt:    releasedAt,
		Artifacts: []release.Artifact{{
			OS: "linux", Arch: "amd64",
			Filename: "olivares_26.8.0_linux_amd64.tar.gz",
			SHA256:   strings.Repeat("a", 64),
		}},
		Revoked: rs,
	}
}

func TestCRLObservationClockNeverResetsWhileListed(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	if err := recordCRLObservations(dir, manifestRevoking(&release.RevokedSet{Serials: []string{"s1"}}, t0), t0); err != nil {
		t.Fatal(err)
	}
	// Ten days later a NEWER manifest still lists s1 and adds s2.
	t10 := t0.Add(10 * 24 * time.Hour)
	if err := recordCRLObservations(dir, manifestRevoking(&release.RevokedSet{Serials: []string{"s1", "s2"}}, t10), t10); err != nil {
		t.Fatal(err)
	}
	rev, first, ok := crlViewFromDataDir(dir)()
	if !ok || len(rev.Serials) != 2 {
		t.Fatalf("expected 2 revoked serials, got %+v ok=%v", rev, ok)
	}
	if got := first["serial:s1"]; !got.Equal(t0) {
		t.Fatalf("s1's clock must keep its FIRST observation (%s), got %s", t0, got)
	}
	if got := first["serial:s2"]; !got.Equal(t10) {
		t.Fatalf("s2's clock starts at its own first observation, got %s", got)
	}

	// s1 disappears (corrected CRL in a NEWER manifest): its clock clears; s2 keeps its own.
	t20 := t0.Add(20 * 24 * time.Hour)
	if err := recordCRLObservations(dir, manifestRevoking(&release.RevokedSet{Serials: []string{"s2"}}, t20), t20); err != nil {
		t.Fatal(err)
	}
	_, first, _ = crlViewFromDataDir(dir)()
	if _, still := first["serial:s1"]; still {
		t.Fatal("an un-revoked serial must drop its observation clock")
	}
	if got := first["serial:s2"]; !got.Equal(t10) {
		t.Fatalf("s2's clock must survive the update, got %s", got)
	}

	// A NEWER manifest with NO CRL clears the content entirely (legitimate un-revoke).
	t30 := t0.Add(30 * 24 * time.Hour)
	if err := recordCRLObservations(dir, manifestRevoking(nil, t30), t30); err != nil {
		t.Fatal(err)
	}
	rev, first, ok = crlViewFromDataDir(dir)()
	if !ok || len(rev.Serials) != 0 || len(first) != 0 {
		t.Fatalf("an empty CRL must clear content but stay observable, got %+v / %+v ok=%v", rev, first, ok)
	}
}

// Monotonicity (M2 / anti-freeze for the CRL): an OLDER (or undated) but validly
// verified manifest — the replay an attacker serves to drop a revocation — must
// NOT roll back a newer observed CRL.
func TestCRLObservationRejectsOlderManifestRollback(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)

	// Observe a revocation from a manifest released at t0.
	if err := recordCRLObservations(dir, manifestRevoking(&release.RevokedSet{Serials: []string{"s1"}}, t0), t0); err != nil {
		t.Fatal(err)
	}
	// Replay an OLDER (released 5 days earlier) manifest with an empty CRL.
	older := manifestRevoking(nil, t0.Add(-5*24*time.Hour))
	if err := recordCRLObservations(dir, older, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	rev, first, ok := crlViewFromDataDir(dir)()
	if !ok || len(rev.Serials) != 1 || rev.Serials[0] != "s1" {
		t.Fatalf("an older replayed manifest must NOT roll back the CRL, got %+v", rev)
	}
	if got := first["serial:s1"]; !got.Equal(t0) {
		t.Fatalf("the replay must not reset the observation clock, got %s", got)
	}

	// An UNDATED manifest (no released_at) also cannot displace a dated CRL.
	undated := manifestRevoking(nil, time.Time{})
	if err := recordCRLObservations(dir, undated, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	rev, _, _ = crlViewFromDataDir(dir)()
	if len(rev.Serials) != 1 {
		t.Fatalf("an undated manifest must not clear a dated CRL, got %+v", rev)
	}
}

func TestCRLObservationEpochAndHolders(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	rs := &release.RevokedSet{HolderIDs: []string{"org-a"}, LicenseKeyEpoch: t0.Add(-time.Hour).Unix()}
	if err := recordCRLObservations(dir, manifestRevoking(rs, t0), t0); err != nil {
		t.Fatal(err)
	}
	rev, first, ok := crlViewFromDataDir(dir)()
	if !ok || len(rev.HolderIDs) != 1 || rev.LicenseKeyEpoch == 0 {
		t.Fatalf("holder+epoch must persist, got %+v", rev)
	}
	if len(sortedCRLKeys(map[string]string{})) != 0 {
		t.Fatal("helper sanity")
	}
	if _, ok := first["holder:org-a"]; !ok {
		t.Fatal("holder observation key missing")
	}
}

func TestCRLObservationCorruptFileIsUnavailableNotClear(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(crlFilePath(dir), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := crlViewFromDataDir(dir)(); ok {
		t.Fatal("a corrupt store must read as UNAVAILABLE (never as clear or revoked)")
	}
	if _, ok, err := loadCRLObservations(dir); ok || err == nil {
		t.Fatal("loadCRLObservations must surface the corruption")
	}
}

// Hallazgo 4b / M3: a corrupt store must NOT be silently reset by a record — that
// would restart every 14-day grace clock. recordCRLObservations must refuse and
// leave the file untouched.
func TestCRLObservationCorruptRefusesWriteAndPreservesFile(t *testing.T) {
	dir := t.TempDir()
	corrupt := []byte("{not json")
	if err := os.WriteFile(crlFilePath(dir), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	err := recordCRLObservations(dir, manifestRevoking(&release.RevokedSet{Serials: []string{"s1"}}, t0), t0)
	if err == nil {
		t.Fatal("recording over a corrupt store must return an error, not silently reset it")
	}
	got, rerr := os.ReadFile(crlFilePath(dir))
	if rerr != nil || string(got) != string(corrupt) {
		t.Fatalf("the corrupt file must be preserved for investigation, got %q err=%v", got, rerr)
	}
}
