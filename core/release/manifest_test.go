// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/license"
)

func intp(i int) *int { return &i }

// goodManifest is a valid stable manifest for two platforms.
func goodManifest() Manifest {
	return Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Channel:       ChannelStable,
		Version:       "26.8.0",
		MinVersion:    "26.6.0",
		ReleasedAt:    time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC),
		Security:      true,
		Advisories:    []string{"OSV-2026-1234"},
		Notes:         "routine GA",
		Rollout:       Rollout{Percentage: intp(100)},
		Artifacts: []Artifact{
			{OS: "linux", Arch: "amd64", Filename: "olivares_26.8.0_linux_amd64.tar.gz", SHA256: strings.Repeat("a", 64), Size: 1000},
			{OS: "darwin", Arch: "arm64", Filename: "olivares_26.8.0_darwin_arm64.tar.gz", SHA256: strings.Repeat("b", 64), Size: 1000},
		},
	}
}

func signManifestBytes(t *testing.T, m Manifest, priv ed25519.PrivateKey) (mb, sig []byte) {
	t.Helper()
	mb, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sig = SignManifest(mb, priv)
	return mb, sig
}

func TestVerifyManifestHappyPath(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	mb, sig := signManifestBytes(t, goodManifest(), priv)
	m, err := VerifyManifest(mb, sig, pub)
	if err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}
	if m.Version != "26.8.0" || m.Channel != ChannelStable || !m.Security {
		t.Fatalf("decoded manifest wrong: %+v", m)
	}
	if a, ok := m.ArtifactFor("linux", "amd64"); !ok || a.Filename == "" {
		t.Fatalf("ArtifactFor(linux/amd64) failed: %+v", a)
	}
	if _, ok := m.ArtifactFor("windows", "amd64"); ok {
		t.Fatalf("ArtifactFor(windows/amd64) must be false")
	}
}

func TestVerifyManifestTamperedBodyAborts(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	mb, sig := signManifestBytes(t, goodManifest(), priv)
	// Flip the version AFTER signing: the sig no longer covers these bytes.
	tampered := []byte(strings.Replace(string(mb), "26.8.0", "99.9.9", 1))
	if _, err := VerifyManifest(tampered, sig, pub); err == nil {
		t.Fatal("a tampered manifest body MUST fail verification")
	}
}

func TestVerifyManifestWrongKeyAborts(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	mb, sig := signManifestBytes(t, goodManifest(), priv)
	if _, err := VerifyManifest(mb, sig, otherPub); err == nil {
		t.Fatal("a manifest signed by another key MUST fail verification")
	}
}

func TestVerifyManifestNoKeyFailsClosed(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	mb, sig := signManifestBytes(t, goodManifest(), priv)
	if _, err := VerifyManifest(mb, sig, nil); err != ErrNoKey {
		t.Fatalf("nil key must fail closed with ErrNoKey, got %v", err)
	}
}

func TestManifestSignatureIsDomainSeparated(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	mb, _ := json.Marshal(goodManifest())
	// A RAW signature (no domain tag) must NOT verify as a manifest. This
	// cross-protocol defense remains useful even with dedicated
	// license and OTA keypairs. The tag contains the blast radius if config ever
	// regresses to key reuse.
	if _, err := VerifyManifest(mb, ed25519.Sign(priv, mb), pub); err == nil {
		t.Fatal("a raw (un-domain-tagged) signature MUST NOT verify as a manifest")
	}
	// The domain-tagged signature verifies.
	if _, err := VerifyManifest(mb, SignManifest(mb, priv), pub); err != nil {
		t.Fatalf("domain-tagged signature must verify: %v", err)
	}
	// The manifest signature does NOT verify over the bare bytes (the path a license
	// verifier takes) — so a signed manifest can never be accepted as a license.
	if ed25519.Verify(pub, mb, SignManifest(mb, priv)) {
		t.Fatal("manifest signature must not verify over the bare bytes (cross-protocol replay)")
	}
}

// TestLicenseAndOTAKeysDoNotCrossDomains pins the custody boundary in code: the
// online license signer cannot mint an OTA manifest, and the off-box OTA signer
// cannot mint a license accepted by the embedded license anchor.
func TestLicenseAndOTAKeysDoNotCrossDomains(t *testing.T) {
	licensePub, licensePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otaPub, otaPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	claims := license.Claims{
		Licensee: "key-domain test",
		IssuedAt: time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC),
	}
	licenseBlob, err := license.Sign(claims, licensePriv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := license.Verify(licenseBlob, licensePub); err != nil {
		t.Fatalf("license key must verify its own license: %v", err)
	}
	otaSignedLicense, err := license.Sign(claims, otaPriv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := license.Verify(otaSignedLicense, licensePub); err == nil {
		t.Fatal("license anchor accepted a license signed by the OTA key")
	}

	mb, otaSig := signManifestBytes(t, goodManifest(), otaPriv)
	if _, err := VerifyManifest(mb, otaSig, otaPub); err != nil {
		t.Fatalf("OTA key must verify its own manifest: %v", err)
	}
	licenseSig := SignManifest(mb, licensePriv)
	if _, err := VerifyManifest(mb, licenseSig, otaPub); err == nil {
		t.Fatal("OTA anchor accepted a manifest signed by the online license key")
	}
}

// checksumsFor renders a goreleaser-shaped checksums.txt covering every artifact
// of m, plus the extra entries a real release carries (FIPS archive, SBOMs) that
// the manifest deliberately does not list.
func checksumsFor(m Manifest) []byte {
	var b strings.Builder
	for _, a := range m.Artifacts {
		fmt.Fprintf(&b, "%s  %s\n", strings.ToLower(a.SHA256), a.Filename)
	}
	fmt.Fprintf(&b, "%s  olivares_26.8.0_fips_linux_amd64.tar.gz\n", strings.Repeat("c", 64))
	fmt.Fprintf(&b, "%s  olivares_26.8.0_linux_amd64.tar.gz.spdx.sbom.json\n", strings.Repeat("d", 64))
	return []byte(b.String())
}

// TestCrossCheckChecksumsBindsManifestToSignedChecksums is the M-01 regression:
// the OTA manifest is signed off-box while checksums.txt is signed in CI by cosign,
// so the two MUST be forced to agree or the ceremony signs whatever bytes happen to
// sit on the draft release. Extra checksums entries are fine; a manifest digest that
// disagrees, or that names a file the signed checksums do not cover, must be rejected.
func TestCrossCheckChecksumsBindsManifestToSignedChecksums(t *testing.T) {
	m := goodManifest()
	if err := m.CrossCheckChecksums(checksumsFor(m)); err != nil {
		t.Fatalf("a manifest whose digests match the signed checksums must pass: %v", err)
	}

	// The attack: the draft manifest is swapped for one pointing at a malicious
	// archive's digest. checksums.txt is cosign-signed and cannot follow.
	tampered := goodManifest()
	tampered.Artifacts[0].SHA256 = strings.Repeat("e", 64)
	err := tampered.CrossCheckChecksums(checksumsFor(m))
	if !errors.Is(err, ErrDigestDisagreement) {
		t.Fatalf("a substituted digest must fail with ErrDigestDisagreement, got %v", err)
	}
	if !strings.Contains(err.Error(), tampered.Artifacts[0].Filename) {
		t.Errorf("the error must name the offending file, got %q", err.Error())
	}

	// A manifest that invents a whole artifact the signed checksums never covered.
	invented := goodManifest()
	invented.Artifacts = append(invented.Artifacts, Artifact{
		OS: "linux", Arch: "arm64", Filename: "olivares_26.8.0_linux_arm64.tar.gz",
		SHA256: strings.Repeat("f", 64), Size: 10,
	})
	if err := invented.CrossCheckChecksums(checksumsFor(m)); !errors.Is(err, ErrNotInManifest) {
		t.Fatalf("an artifact absent from the signed checksums must fail with ErrNotInManifest, got %v", err)
	}

	// Case-insensitive digests still bind (ParseChecksums lowercases; a manifest may
	// arrive uppercase when it has not been through ParseManifest).
	upper := goodManifest()
	upper.Artifacts[0].SHA256 = strings.ToUpper(upper.Artifacts[0].SHA256)
	if err := upper.CrossCheckChecksums(checksumsFor(m)); err != nil {
		t.Errorf("an uppercase manifest digest must still bind: %v", err)
	}

	// Unusable inputs fail closed rather than silently passing.
	if err := m.CrossCheckChecksums([]byte("not a checksums file")); err == nil {
		t.Error("an unparseable checksums.txt must fail closed")
	}
	empty := goodManifest()
	empty.Artifacts = nil
	if err := empty.CrossCheckChecksums(checksumsFor(m)); err == nil {
		t.Error("a manifest with no artifacts must fail closed, not vacuously pass")
	}
}

func TestManifestFreshness(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	m := goodManifest()
	if m.Stale(now) {
		t.Fatal("a manifest with no expires is never stale")
	}
	past := now.Add(-time.Hour)
	m.Expires = &past
	if !m.Stale(now) {
		t.Fatal("a manifest past its expires must be stale (anti-freeze)")
	}
	future := now.Add(time.Hour)
	m.Expires = &future
	if m.Stale(now) {
		t.Fatal("a manifest before its expires must not be stale")
	}
}

func TestParseManifestRejectsBadShapes(t *testing.T) {
	base := goodManifest()
	mutate := func(f func(*Manifest)) []byte {
		m := base
		f(&m)
		b, _ := json.Marshal(m)
		return b
	}
	cases := map[string][]byte{
		"unknown schema":  mutate(func(m *Manifest) { m.SchemaVersion = 99 }),
		"bad channel":     mutate(func(m *Manifest) { m.Channel = "nightly" }),
		"bad version":     mutate(func(m *Manifest) { m.Version = "not-semver" }),
		"bad min_version": mutate(func(m *Manifest) { m.MinVersion = "x.y.z" }),
		"rollout >100":    mutate(func(m *Manifest) { m.Rollout = Rollout{Percentage: intp(101)} }),
		"rollout <0":      mutate(func(m *Manifest) { m.Rollout = Rollout{Percentage: intp(-1)} }),
		"no artifacts":    mutate(func(m *Manifest) { m.Artifacts = nil }),
		"bad sha":         mutate(func(m *Manifest) { m.Artifacts[0].SHA256 = "deadbeef" }),
		"missing os":      mutate(func(m *Manifest) { m.Artifacts[0].OS = "" }),
		"unknown field":   []byte(`{"schema_version":1,"channel":"stable","version":"1.2.3","released_at":"2026-01-01T00:00:00Z","rollout":{},"artifacts":[{"os":"linux","arch":"amd64","filename":"x","sha256":"` + strings.Repeat("a", 64) + `"}],"surprise":true}`),
	}
	for name, b := range cases {
		if _, err := ParseManifest(b); err == nil {
			t.Errorf("%s: ParseManifest should have rejected it", name)
		}
	}
	// A minimal valid manifest (omitted rollout => full) parses.
	min := []byte(`{"schema_version":1,"channel":"lts","version":"26.7.1","released_at":"2026-07-09T00:00:00Z","rollout":{},"artifacts":[{"os":"linux","arch":"amd64","filename":"x.tgz","sha256":"` + strings.Repeat("c", 64) + `"}]}`)
	if _, err := ParseManifest(min); err != nil {
		t.Fatalf("minimal valid manifest should parse: %v", err)
	}
}

func TestRolloutEligibility(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	// Omitted percentage => everyone.
	m := goodManifest()
	m.Rollout = Rollout{}
	if !m.Eligible("node-1", now) {
		t.Error("omitted rollout must be full (everyone eligible)")
	}
	// Paused (0) => nobody.
	m.Rollout = Rollout{Percentage: intp(0)}
	if m.Eligible("node-1", now) {
		t.Error("rollout 0 must be paused (nobody eligible)")
	}
	// start_at in the future => not yet, even at 100.
	m.Rollout = Rollout{Percentage: intp(100), StartAt: now.Add(time.Hour)}
	if m.Eligible("node-1", now) {
		t.Error("rollout with a future start_at must not be eligible yet")
	}
	// Deterministic bucketing: a 50% rollout admits ~half the fleet, stably.
	m = goodManifest()
	m.Rollout = Rollout{Percentage: intp(50)}
	in := 0
	const N = 2000
	for i := 0; i < N; i++ {
		id := "node-" + string(rune('A'+i%26)) + time.Duration(i).String()
		if m.Eligible(id, now) {
			in++
			// Determinism: the same node decides the same way twice.
			if !m.Eligible(id, now) {
				t.Fatalf("Eligible not deterministic for %q", id)
			}
		}
	}
	if in < N*4/10 || in > N*6/10 {
		t.Errorf("50%% rollout admitted %d/%d nodes; want roughly half", in, N)
	}
}

func TestPlanUpgradeDirectionsAndGates(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	m := goodManifest() // version 26.8.0, min_version 26.6.0

	// Forward from 26.7.0 -> 26.8.0, min satisfied, eligible, security.
	p, err := m.PlanUpgrade("26.7.0", "linux", "amd64", "node-1", now)
	if err != nil {
		t.Fatalf("PlanUpgrade: %v", err)
	}
	if p.Direction != 1 || p.IsRollback() || p.IsUpToDate() {
		t.Errorf("forward plan wrong: dir=%d", p.Direction)
	}
	if p.MinTooOld {
		t.Error("26.7.0 >= min 26.6.0, MinTooOld must be false")
	}
	if !p.HasArtifact || p.Artifact.OS != "linux" {
		t.Error("expected a linux/amd64 artifact")
	}
	if !p.Security || len(p.Advisories) != 1 {
		t.Error("security + advisories must propagate to the plan")
	}

	// Same version -> up to date.
	p, _ = m.PlanUpgrade("26.8.0", "linux", "amd64", "node-1", now)
	if !p.IsUpToDate() || p.Direction != 0 {
		t.Errorf("same-version plan should be up-to-date, dir=%d", p.Direction)
	}

	// Older target than current -> would be a rollback.
	p, _ = m.PlanUpgrade("27.0.0", "linux", "amd64", "node-1", now)
	if !p.IsRollback() || p.Direction != -1 {
		t.Errorf("downgrade plan should be a rollback, dir=%d", p.Direction)
	}

	// current below min_version -> MinTooOld (direct jump not allowed).
	p, _ = m.PlanUpgrade("26.5.0", "linux", "amd64", "node-1", now)
	if !p.MinTooOld {
		t.Error("26.5.0 < min 26.6.0 must set MinTooOld")
	}

	// Platform absent from the manifest -> no artifact.
	p, _ = m.PlanUpgrade("26.7.0", "windows", "amd64", "node-1", now)
	if p.HasArtifact {
		t.Error("windows/amd64 is not in the manifest; HasArtifact must be false")
	}

	// Every plan above was built from a real version, so all of them are orderable.
	for _, cur := range []string{"26.7.0", "26.8.0", "27.0.0", "26.5.0"} {
		p, _ = m.PlanUpgrade(cur, "linux", "amd64", "node-1", now)
		if !p.CurrentKnown {
			t.Errorf("%s is a real version; CurrentKnown must be true", cur)
		}
	}

	// An UNSTAMPED build is not a position in the ordering. This used to assert
	// "dev -> release must be forward", which was true of Direction and dangerously
	// incomplete: the same zero version made MinTooOld true against EVERY min_version,
	// so the build the comment called "always upgradable" was the one build that could
	// never take a gated release. The plan now says it does not know, and the ordering
	// predicates refuse to make a claim.
	for _, unstamped := range []string{"dev", "", "  ", "v dev"[:1] + "dev"} {
		p, _ = m.PlanUpgrade(unstamped, "linux", "amd64", "node-1", now)
		if p.CurrentKnown {
			t.Errorf("%q is unstamped; CurrentKnown must be false", unstamped)
		}
		if p.MinTooOld {
			t.Errorf("%q has no position in the ordering, so it cannot be BELOW min_version", unstamped)
		}
		if p.IsRollback() || p.IsUpToDate() {
			t.Errorf("%q must yield no ordering claim, got rollback=%v uptodate=%v", unstamped, p.IsRollback(), p.IsUpToDate())
		}
	}
}

// TestIsUnstamped pins the single predicate the upgrade path shares between its two
// ways of not knowing the installed version — an unstamped build and a target whose
// exec-probe could not run. Everything goreleaser publishes is stamped
// (.goreleaser.yaml injects -X main.version), so this only ever answers true for a
// build from source.
func TestIsUnstamped(t *testing.T) {
	for _, s := range []string{"", "dev", "  dev  ", "vdev", "v"} {
		if !IsUnstamped(s) {
			t.Errorf("%q carries no version stamp; IsUnstamped must be true", s)
		}
	}
	for _, s := range []string{"26.7.0", "v26.7.1", "26.8.0-rc.1", "0.0.0"} {
		if IsUnstamped(s) {
			t.Errorf("%q is a real version; IsUnstamped must be false", s)
		}
	}
	// 0.0.0 is the boundary that matters: it PARSES to the same zero Version as "dev",
	// but an operator who declares it has stated a position, and the guards must treat
	// it as one rather than fold it back into "unknown".
	if v, err := ParseVersion("0.0.0"); err != nil || Compare(v, Version{}) != 0 {
		t.Fatalf("0.0.0 must parse to the zero Version: %v %v", v, err)
	}
}

// TestCheckPolicyBoundsTheFieldsChecksumsCannotBind is the MAJOR-2 regression in the
// core verifier. CrossCheckChecksums binds DIGESTS; every field below is covered by
// the custodian's signature and by nothing else, and each one alone is enough to
// block, delay or suppress every upgrade in the fleet while every digest stays honest.
func TestCheckPolicyBoundsTheFieldsChecksumsCannotBind(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	// A plausible production manifest: released now, 90-day window, full rollout.
	base := func() Manifest {
		m := goodManifest()
		m.ReleasedAt = now
		exp := now.Add(2160 * time.Hour)
		m.Expires = &exp
		return m
	}
	if _, err := base().CheckPolicy(now, DefaultPolicyBounds()); err != nil {
		t.Fatalf("a plausible production manifest must pass: %v", err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*Manifest)
		wantIn string
	}{{
		name:   "min_version above the release blocks the whole fleet forever",
		mutate: func(m *Manifest) { m.MinVersion = "99.0.0" },
		wantIn: "permanently blocks the whole fleet",
	}, {
		// The EQUAL case is the same kill switch one version lower, and it reads as
		// an honest manifest: MinTooOld is Compare(current, min) < 0, so every node
		// that still needs the upgrade is refused, while a node already on the
		// release short-circuits at IsUpToDate and never reads min_version.
		name:   "min_version equal to the release is the same fleet kill switch",
		mutate: func(m *Manifest) { m.MinVersion = m.Version },
		wantIn: "permanently blocks the whole fleet",
	}, {
		name:   "expires far beyond the bound re-opens anti-freeze",
		mutate: func(m *Manifest) { e := now.Add(500 * 24 * time.Hour); m.Expires = &e },
		wantIn: "anti-freeze defense being switched off",
	}, {
		name:   "no expires at all disables anti-freeze",
		mutate: func(m *Manifest) { m.Expires = nil },
		wantIn: "no freshness bound",
	}, {
		name:   "an already-expired manifest is born dead",
		mutate: func(m *Manifest) { e := now.Add(-time.Hour); m.Expires = &e },
		wantIn: "already expired",
	}, {
		name:   "paused rollout suppresses a security release",
		mutate: func(m *Manifest) { m.Rollout.Percentage = intp(0) },
		wantIn: "would install it for NOBODY",
	}, {
		name:   "a far-future start_at suppresses it just as well",
		mutate: func(m *Manifest) { m.Rollout.StartAt = now.Add(400 * 24 * time.Hour) },
		wantIn: "nobody ever upgrades",
	}, {
		name:   "a start_at after expires means nobody ever upgrades",
		mutate: func(m *Manifest) { m.Rollout.StartAt = now.Add(3000 * time.Hour) },
		wantIn: "can never begin before the manifest dies",
	}, {
		name:   "a security release that names no advisory hides what it fixes",
		mutate: func(m *Manifest) { m.Advisories = nil },
		wantIn: "carries no advisories",
	}, {
		name:   "the security channel with security:false was not made by the generator",
		mutate: func(m *Manifest) { m.Channel = ChannelSecurity; m.Security = false },
		wantIn: "security is false",
	}, {
		name:   "a forward-dated released_at buys freshness the release never had",
		mutate: func(m *Manifest) { m.ReleasedAt = now.Add(72 * time.Hour) },
		wantIn: "in the FUTURE",
	}, {
		name:   "control characters in notes are a terminal-escape payload",
		mutate: func(m *Manifest) { m.Notes = "all good\x1b[2K\rnothing to see" },
		wantIn: "control characters",
	}, {
		name:   "control characters in an advisory id, likewise",
		mutate: func(m *Manifest) { m.Advisories = []string{"GHSA-a\x1b[31m"} },
		wantIn: "control characters",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			m := base()
			tc.mutate(&m)
			_, err := m.CheckPolicy(now, DefaultPolicyBounds())
			if err == nil {
				t.Fatalf("this policy must be REFUSED, got nil")
			}
			if !errors.Is(err, ErrImplausiblePolicy) {
				t.Errorf("must classify as ErrImplausiblePolicy, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("the refusal must explain the lever (%q), got: %v", tc.wantIn, err)
			}
		})
	}

	// Every bound has an explicit, audited escape hatch — a plausibility check that
	// cannot be overridden becomes a check people route around.
	t.Run("escape hatches", func(t *testing.T) {
		noExp := base()
		noExp.Expires = nil
		b := DefaultPolicyBounds()
		b.AllowNoExpiry = true
		warns, err := noExp.CheckPolicy(now, b)
		if err != nil {
			t.Errorf("AllowNoExpiry must permit it: %v", err)
		}
		if len(warns) == 0 {
			t.Error("...but it must still WARN that anti-freeze is disabled")
		}

		paused := base()
		paused.Rollout.Percentage = intp(0)
		b = DefaultPolicyBounds()
		b.AllowPausedRollout = true
		if _, err := paused.CheckPolicy(now, b); err != nil {
			t.Errorf("AllowPausedRollout must permit a deliberate pause: %v", err)
		}

		far := base()
		e := now.Add(500 * 24 * time.Hour)
		far.Expires = &e
		b = DefaultPolicyBounds()
		b.MaxFreshnessWindow = 600 * 24 * time.Hour
		if _, err := far.CheckPolicy(now, b); err != nil {
			t.Errorf("a raised MaxFreshnessWindow must permit a longer window: %v", err)
		}
	})

	// With no expires to compare against (the explicit opt-out), a far-future start_at
	// is still a suppression — the freshness-window bound is what catches it there.
	t.Run("far-future start_at is bounded even without an expires", func(t *testing.T) {
		m := base()
		m.Expires = nil
		m.Rollout.StartAt = now.Add(400 * 24 * time.Hour)
		b := DefaultPolicyBounds()
		b.AllowNoExpiry = true
		_, err := m.CheckPolicy(now, b)
		if err == nil {
			t.Fatal("a start_at 400 days out must be refused")
		}
		if !strings.Contains(err.Error(), "suppresses the rollout as effectively as percentage 0") {
			t.Errorf("the refusal must name the suppression, got: %v", err)
		}
	})

	// A partial rollout is legitimate; it must be surfaced, not refused.
	t.Run("partial rollout warns rather than refuses", func(t *testing.T) {
		m := base()
		m.Rollout.Percentage = intp(25)
		warns, err := m.CheckPolicy(now, DefaultPolicyBounds())
		if err != nil {
			t.Fatalf("a staged rollout is a normal publishing choice: %v", err)
		}
		if !strings.Contains(strings.Join(warns, "\n"), "25") {
			t.Errorf("a staged rollout must be surfaced to the custodian, got %v", warns)
		}
	})

	// A refusal reports EVERY lever that moved, not just the first.
	t.Run("all violations are reported at once", func(t *testing.T) {
		m := base()
		m.MinVersion = "99.0.0"
		m.Rollout.Percentage = intp(0)
		m.Advisories = nil
		_, err := m.CheckPolicy(now, DefaultPolicyBounds())
		if err == nil {
			t.Fatal("expected a refusal")
		}
		for _, want := range []string{"min_version", "rollout.percentage", "advisories"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal must name %q too, got: %v", want, err)
			}
		}
	})
}

// TestPolicySummaryPrintsEveryFieldTheSignatureCovers: silence about a field is what
// let a substituted min_version/rollout/notes through unread. The summary is the
// human half of the MAJOR-2 fix, so it must actually name every policy field.
func TestPolicySummaryPrintsEveryFieldTheSignatureCovers(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	m := goodManifest()
	m.ReleasedAt = now
	exp := now.Add(2160 * time.Hour)
	m.Expires = &exp
	eol := now.Add(24 * time.Hour)
	m.EOLAt = &eol

	got := map[string]PolicyField{}
	for _, f := range m.PolicySummary(now) {
		got[f.Name] = f
	}
	for _, want := range []string{"schema_version", "channel", "version", "min_version",
		"released_at", "expires", "rollout", "security", "advisories", "eol_at", "notes",
		"revoked", "artifacts"} {
		if _, ok := got[want]; !ok {
			t.Errorf("PolicySummary must print %q — an unprinted field is an unreviewed field", want)
		}
	}
	// The fields that restrict the fleet must be flagged, not buried in the list.
	for _, want := range []string{"min_version", "notes", "security", "advisories"} {
		if !got[want].Alert {
			t.Errorf("%q is set and must be flagged for the custodian's attention", want)
		}
	}
	if !strings.Contains(got["min_version"].Value, "26.6.0") {
		t.Errorf("min_version must show its value, got %q", got["min_version"].Value)
	}
	// A missing freshness bound must read as the alarm it is.
	m.Expires = nil
	for _, f := range m.PolicySummary(now) {
		if f.Name == "expires" {
			if !f.Alert || !strings.Contains(f.Value, "anti-freeze DISABLED") {
				t.Errorf("an absent expires must render as an alarm, got %+v", f)
			}
		}
	}
}

// TestCheckArtifactNamingRefusesVariantAndPlatformRemap: checksums.txt covers the
// FIPS variant and every other platform, so a digest that "matches" proves nothing
// about WHICH archive a platform was pointed at.
func TestCheckArtifactNamingRefusesVariantAndPlatformRemap(t *testing.T) {
	if err := goodManifest().CheckArtifactNaming(); err != nil {
		t.Fatalf("the honest manifest must pass: %v", err)
	}
	for _, tc := range []struct{ name, filename, wantIn string }{
		{"the FIPS variant is not an OTA target", "olivares_26.8.0_fips_linux_amd64.tar.gz", "olivares_26.8.0_linux_amd64.tar.gz"},
		{"another platform's archive", "olivares_26.8.0_darwin_arm64.tar.gz", "olivares_26.8.0_linux_amd64.tar.gz"},
		{"another version's archive", "olivares_26.7.0_linux_amd64.tar.gz", "olivares_26.8.0_linux_amd64.tar.gz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := goodManifest()
			m.Artifacts[0].Filename = tc.filename
			err := m.CheckArtifactNaming()
			if err == nil {
				t.Fatal("a filename that does not match the declared platform/version must be REFUSED")
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("the refusal must name the expected archive %q, got: %v", tc.wantIn, err)
			}
		})
	}

	// Two entries for one platform: ArtifactFor returns the first, so the manifest
	// says two different things and the reader picks by accident.
	dup := goodManifest()
	dup.Artifacts = append(dup.Artifacts, dup.Artifacts[0])
	if err := dup.CheckArtifactNaming(); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("a duplicated platform must be refused, got %v", err)
	}

	// And the cross-check must enforce it. checksumsFor already covers the FIPS
	// archive (digest "cccc…"), which is exactly the point: the remapped entry carries
	// a GENUINE, cosign-attested digest, so the digest comparison alone waves it
	// through and only the naming check catches the swap.
	remap := goodManifest()
	remap.Artifacts[0].Filename = "olivares_26.8.0_fips_linux_amd64.tar.gz"
	remap.Artifacts[0].SHA256 = strings.Repeat("c", 64)
	err := remap.CrossCheckChecksums(checksumsFor(goodManifest()))
	if err == nil {
		t.Fatal("CrossCheckChecksums must refuse a platform remapped onto the FIPS archive even though its digest is genuine")
	}
	if errors.Is(err, ErrDigestDisagreement) || errors.Is(err, ErrNotInManifest) {
		t.Errorf("the FIPS digest is genuine and listed — the refusal must come from the NAMING check, got: %v", err)
	}
}

// TestCheckArtifactNamingRejectsImplausiblePlatform: os/arch are attacker-controlled
// strings that go on to compose a filename a caller joins onto a directory.
func TestCheckArtifactNamingRejectsImplausiblePlatform(t *testing.T) {
	for _, tc := range []struct{ name, goos, goarch string }{
		{"path separators", "li/nux", "amd64"},
		{"parent traversal", "..", "amd64"},
		{"empty arch", "linux", ""},
		{"uppercase", "Linux", "amd64"},
		{"punctuation", "linux", "amd_64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := goodManifest()
			m.Artifacts = m.Artifacts[:1]
			m.Artifacts[0].OS, m.Artifacts[0].Arch = tc.goos, tc.goarch
			m.Artifacts[0].Filename = ExpectedArtifactName(m.Version, tc.goos, tc.goarch, "")
			if err := m.CheckArtifactNaming(); err == nil {
				t.Fatalf("os/arch %q/%q must be refused before it can compose a path", tc.goos, tc.goarch)
			}
		})
	}
}

// TestManifestSubstitutionAcrossTheSubsetAxis is the counterfactual the 08-15 spec
// wrote before anyone implemented it (an internal design note (not shipped)
// -2026-08-15.md:288-292):
//
//	«un manifiesto que declare la variante `base` y nombre el fichero del superset
//	debe ser RECHAZADO, con todos los digests correctos y la firma del custodio
//	válida. Si el test no puede construir ese manifiesto, no está probando la amenaza.»
//
// So this test does not merely assert an error: it first PROVES that every other
// defense on the seam says yes. The digests are honest, checksums.txt covers the
// superset because it is a legitimate artifact of this same release, and the
// custodian's signature verifies. If any of those failed, a red bar here would be
// evidence of nothing — the manifest would have been rejected for a reason that has
// nothing to do with the subset axis.
func TestManifestSubstitutionAcrossTheSubsetAxis(t *testing.T) {
	const (
		baseArchive  = "olivares_26.8.0_base_linux_amd64.tar.gz"
		superArchive = "olivares_26.8.0_full_linux_amd64.tar.gz"
	)
	superDigest := strings.Repeat("e", 64)

	// A 16-SKU release: checksums.txt covers every subset, all cosign-signed.
	baseDigest := strings.Repeat("f", 64)
	// checksums.txt of a 16-SKU release: every subset, cosign-signed. Written from the
	// SKU table and not from the manifest, because the whole threat is that the two
	// disagree — deriving one from the other is the oracle-from-the-subject trap.
	signedChecksums := func() []byte {
		var b strings.Builder
		fmt.Fprintf(&b, "%s  %s\n", superDigest, superArchive)
		fmt.Fprintf(&b, "%s  %s\n", baseDigest, baseArchive)
		fmt.Fprintf(&b, "%s  olivares_26.8.0_darwin_arm64.tar.gz\n", strings.Repeat("b", 64))
		return []byte(b.String())
	}

	substituted := func() Manifest {
		m := goodManifest()
		m.Artifacts = []Artifact{{
			OS: "linux", Arch: "amd64",
			Variant:  "base",       // what the client is entitled to …
			Filename: superArchive, // … and what it would actually receive
			SHA256:   superDigest,  // honest: this IS the superset's digest
		}}
		return m
	}

	// The premise, isolated to ONE variable. The same filename and the same digest are
	// accepted when the declared variant matches the archive, so the digest is honest and
	// checksums.txt really covers it: the ONLY thing that changes between accept and
	// refuse is the variant the manifest declares.
	//
	// It is asserted this way and not by "CrossCheckChecksums says yes" because that
	// function CALLS CheckArtifactNaming (manifest.go, the supply-chain seam) — a fact
	// worth having in a test, since it is what stops the ceremony from skipping the bind.
	t.Run("the digest is honest: the same file and digest pass when the variant matches", func(t *testing.T) {
		m := substituted()
		m.Artifacts[0].Variant = "full" // same filename, same digest, truthful declaration
		if err := m.CrossCheckChecksums(signedChecksums()); err != nil {
			t.Fatalf("the threat needs honest digests; this pair must be accepted: %v", err)
		}
		pub, priv, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatalf("keygen: %v", err)
		}
		mb, sig := signManifestBytes(t, m, priv)
		if _, err := VerifyManifest(mb, sig, pub); err != nil {
			t.Fatalf("and the custodian's signature over these bytes must verify: %v", err)
		}
	})

	t.Run("and the naming bind is what refuses it", func(t *testing.T) {
		err := substituted().CheckArtifactNaming()
		if err == nil {
			t.Fatal("a manifest declaring variant \"base\" while naming the superset archive must be REFUSED: " +
				"every digest is honest and the custodian signed, so this bind is the only thing between a base " +
				"customer and the full product")
		}
		if !strings.Contains(err.Error(), superArchive) || !strings.Contains(err.Error(), baseArchive) {
			t.Fatalf("the refusal must name BOTH the archive it got and the one that variant demands, "+
				"or the operator cannot tell substitution from a typo; got: %v", err)
		}
	})

	// Negative control. Without this, the test above would also pass if the variant
	// machinery refused EVERY manifest — which is a broken gate, not a defense.
	t.Run("the same manifest naming its own variant's archive is accepted", func(t *testing.T) {
		m := substituted()
		m.Artifacts[0].Filename = baseArchive
		m.Artifacts[0].SHA256 = baseDigest
		if err := m.CheckArtifactNaming(); err != nil {
			t.Fatalf("variant \"base\" naming %s is exactly right and must pass: %v", baseArchive, err)
		}
		if err := m.CrossCheckChecksums(signedChecksums()); err != nil {
			t.Fatalf("and it must still cross-check: %v", err)
		}
	})

	// The community shape must not have changed meaning by this field existing.
	t.Run("an artifact with no variant keeps the old name and is still accepted", func(t *testing.T) {
		m := goodManifest()
		if err := m.CheckArtifactNaming(); err != nil {
			t.Fatalf("a variant-less manifest is every manifest published today: %v", err)
		}
		if got := ExpectedArtifactName("26.8.0", "linux", "amd64", ""); got != "olivares_26.8.0_linux_amd64.tar.gz" {
			t.Fatalf("the empty variant must compose the byte-identical old name, got %q", got)
		}
	})

	// The variant composes a filename, so it is attacker-controlled string reaching a path.
	t.Run("an implausible variant is refused before it can compose a path", func(t *testing.T) {
		for _, bad := range []string{"../etc", "ba/se", "Base", "ba_se", "base "} {
			m := substituted()
			m.Artifacts[0].Variant = bad
			m.Artifacts[0].Filename = ExpectedArtifactName(m.Version, "linux", "amd64", bad)
			if err := m.CheckArtifactNaming(); err == nil {
				t.Fatalf("variant %q composes a path segment and must be refused", bad)
			}
		}
	})
}
