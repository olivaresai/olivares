// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package license_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/license"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	pub, priv, err := license.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	in := license.Claims{
		Licensee: "Acme GmbH", Plan: "commercial", HolderID: "h-1",
		Serial: "lic-2026-0001", Profile: "airgapped",
		Features: []string{"a", "b"}, MaxTenants: 10,
		IssuedAt: now, ExpiresAt: now.Add(365 * 24 * time.Hour),
	}
	blob, err := license.Sign(in, priv)
	if err != nil {
		t.Fatal(err)
	}
	out, err := license.Verify(blob, pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if out.Licensee != in.Licensee || out.Serial != in.Serial || out.Profile != in.Profile || out.MaxTenants != 10 || !out.IssuedAt.Equal(in.IssuedAt) || !out.ExpiresAt.Equal(in.ExpiresAt) {
		t.Fatalf("claims roundtrip mismatch: %+v vs %+v", out, in)
	}
	if out.Status(now) != license.StatusValid {
		t.Fatalf("status = %v, want valid", out.Status(now))
	}
	if out.Status(now.Add(500*24*time.Hour)) != license.StatusExpired {
		t.Fatal("expected expired in the future")
	}
}

func TestTamperRejected(t *testing.T) {
	pub, priv, _ := license.GenerateKey()
	blob, _ := license.Sign(license.Claims{Licensee: "X", IssuedAt: time.Now()}, priv)
	// Flip a character in the payload.
	tampered := "Z" + blob[1:]
	if _, err := license.Verify(tampered, pub); err == nil {
		t.Fatal("tampered license verified")
	}
	// A different key must not verify.
	otherPub, _, _ := license.GenerateKey()
	if _, err := license.Verify(blob, otherPub); !errors.Is(err, license.ErrBadSignature) {
		t.Fatalf("wrong-key err = %v, want ErrBadSignature", err)
	}
	if _, err := license.Verify("no-dot-here", pub); !errors.Is(err, license.ErrMalformed) {
		t.Fatalf("malformed err = %v, want ErrMalformed", err)
	}
}

// CHANGED BY and the reason is the whole point of the test.
//
// It used to assert that a blob with no expiry is PERPETUAL, and it passed for as long
// as that was the product. The v8 package made every offer term-only and LICENSING.md
// §ADR-0010 signs "no perpetual fallback", so a blob that attests no term now attests no
// right. Leaving the old assertion would have kept a signed-but-termless blob entitling
// the closed build's add-ons for ever — measured, not hypothetical: the enterprise
// durable-bus gate grants on `Status(...) != StatusExpired`.
//
// Verify still accepts the blob: nothing here blocks, and the claims are still returned
// as display facts. What changed is only what the status MEANS.
func TestNoExpiryIsNotAPerpetualRight(t *testing.T) {
	pub, priv, _ := license.GenerateKey()
	blob, _ := license.Sign(license.Claims{Licensee: "Forever", IssuedAt: time.Now()}, priv)
	c, err := license.Verify(blob, pub)
	if err != nil {
		t.Fatalf("a termless blob must still VERIFY — only its status changed: %v", err)
	}
	if got := c.Status(time.Now()); got != license.StatusExpired {
		t.Fatalf("termless status = %q, want %q (term-only: no term attested, no right)", got, license.StatusExpired)
	}
	if got := c.Status(time.Now().Add(100 * 365 * 24 * time.Hour)); got != license.StatusExpired {
		t.Fatalf("termless status far in the future = %q, want %q", got, license.StatusExpired)
	}
	// The symbol survives for callers that still switch on it; only the RIGHT is gone.
	if license.StatusPerpetual != "perpetual" {
		t.Fatal("StatusPerpetual must remain exported and unchanged for wire/source compatibility")
	}
}

// CHANGED BY: the grace column no longer varies by PROFILE.
//
// The matrix used to encode 30d for online, 90d for air-gapped and 0 for trial — figures
// no signature ever carried, invented by the verifier. The canon signs one window,
// G = T+168h, granted by the ISSUER (once per rolling 365 days, only for an involuntary
// lapse), so the profile is now what it always claimed to be in the field doc: a
// display/record label. The axis kept here is the one that is real: whether the issuance
// ATTESTED a grace window at all.
func TestProfileStatusAndRevocationMatrix(t *testing.T) {
	issued := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	expires := issued.Add(24 * time.Hour)
	profiles := []struct {
		name    string
		profile string
		grace   time.Duration
	}{
		{name: "online-attested-grace", profile: "online", grace: license.MaxGracePeriod},
		{name: "airgapped-attested-grace", profile: "airgapped", grace: license.MaxGracePeriod},
		{name: "trial-no-attested-grace", profile: "trial", grace: 0},
		{name: "online-no-attested-grace", profile: "online", grace: 0},
	}

	for _, profile := range profiles {
		graceWant := license.StatusGrace
		if profile.grace == 0 {
			graceWant = license.StatusExpired
		}
		cases := []struct {
			name       string
			now        time.Time
			mutate     func(*license.Claims)
			revocation license.Revocation
			want       license.Status
		}{
			{
				name: "valid", now: expires.Add(-time.Hour),
				want: license.StatusValid,
			},
			{
				name: "grace", now: expires.Add(time.Second),
				want: graceWant,
			},
			{
				name: "expired", now: expires.Add(profile.grace).Add(time.Second),
				want: license.StatusExpired,
			},
			{
				name: "revoked-by-serial", now: expires.Add(-time.Hour),
				revocation: license.Revocation{Serials: []string{"serial-1"}},
				want:       license.StatusRevoked,
			},
			{
				name: "revoked-by-holder", now: expires.Add(-time.Hour),
				revocation: license.Revocation{HolderIDs: []string{"holder-1"}},
				want:       license.StatusRevoked,
			},
			{
				name: "revoked-by-epoch", now: expires.Add(-time.Hour),
				revocation: license.Revocation{LicenseKeyEpoch: issued.Unix() + 1},
				want:       license.StatusRevoked,
			},
			{
				name: "legacy-without-serial", now: expires.Add(-time.Hour),
				mutate: func(c *license.Claims) {
					c.Serial = ""
				},
				revocation: license.Revocation{Serials: []string{"", "serial-1"}},
				want:       license.StatusValid,
			},
		}

		for _, tc := range cases {
			t.Run(profile.name+"/"+tc.name, func(t *testing.T) {
				claims := license.Claims{
					Licensee: "Acme", HolderID: "holder-1", Serial: "serial-1",
					Profile: profile.profile, IssuedAt: issued, ExpiresAt: expires,
				}
				if profile.grace > 0 {
					claims.GraceUntil = expires.Add(profile.grace)
				}
				if tc.mutate != nil {
					tc.mutate(&claims)
				}
				if got := claims.StatusWithRevocation(tc.now, tc.revocation); got != tc.want {
					t.Fatalf("StatusWithRevocation() = %q, want %q", got, tc.want)
				}
			})
		}
	}
}

// CHANGED BY: the boundary is the ATTESTED one, and the important cases are the
// ones the old test could not express — no attestation, a malformed window, and a window
// wider than the canon allows.
func TestGracePeriodBoundaries(t *testing.T) {
	expires := time.Date(2026, time.February, 3, 4, 5, 6, 0, time.UTC)

	t.Run("attested window is honored to its last instant", func(t *testing.T) {
		claims := license.Claims{Profile: "online", ExpiresAt: expires, GraceUntil: expires.Add(license.MaxGracePeriod)}
		if got := claims.Status(expires.Add(time.Second)); got != license.StatusGrace {
			t.Fatalf("just after expiry = %q, want %q", got, license.StatusGrace)
		}
		if got := claims.Status(expires.Add(license.MaxGracePeriod)); got != license.StatusGrace {
			t.Fatalf("at the attested boundary = %q, want %q", got, license.StatusGrace)
		}
		if got := claims.Status(expires.Add(license.MaxGracePeriod).Add(time.Second)); got != license.StatusExpired {
			t.Fatalf("one second past it = %q, want %q", got, license.StatusExpired)
		}
	})

	// The case that used to be impossible to state, and is now the default: nothing was
	// granted, so nothing is honored. A voluntary cancellation cuts at T, and this is how.
	t.Run("no attestation means zero grace, not thirty days", func(t *testing.T) {
		claims := license.Claims{Profile: "online", ExpiresAt: expires}
		if got := claims.Status(expires); got != license.StatusValid {
			t.Fatalf("at expiry = %q, want %q", got, license.StatusValid)
		}
		if got := claims.Status(expires.Add(time.Nanosecond)); got != license.StatusExpired {
			t.Fatalf("one nanosecond past expiry = %q, want %q", got, license.StatusExpired)
		}
		if got := claims.GracePeriod(); got != 0 {
			t.Fatalf("GracePeriod() with nothing attested = %v, want 0", got)
		}
	})

	// A window that does not extend the term grants nothing. A verifier that subtracted
	// blindly would produce a NEGATIVE grace and, depending on the comparison, a right.
	t.Run("a window at or before the expiry grants nothing", func(t *testing.T) {
		for _, gu := range []time.Time{expires, expires.Add(-time.Hour)} {
			claims := license.Claims{ExpiresAt: expires, GraceUntil: gu}
			if got := claims.Status(expires.Add(time.Second)); got != license.StatusExpired {
				t.Fatalf("grace_until %v: status = %q, want %q", gu, got, license.StatusExpired)
			}
			if got := claims.GracePeriod(); got != 0 {
				t.Fatalf("grace_until %v: GracePeriod() = %v, want 0", gu, got)
			}
		}
	})

	// THE STRUCTURAL BOUND. The issuer owns whether to grant; the verifier still refuses
	// to honor more than the canon signed, so a buggy or over-generous issuance cannot
	// reintroduce the perpetual right through the back door.
	// A window so far out that the delta cannot fit in a time.Duration. Sub() saturates
	// rather than wrapping, so the clamp still holds — pinned here because a wrapped
	// NEGATIVE delta would read as "no grace" by luck rather than by design, and the day
	// that changed nobody would notice.
	t.Run("a window beyond the range of a Duration is still clamped", func(t *testing.T) {
		claims := license.Claims{ExpiresAt: expires, GraceUntil: time.Date(9999, time.December, 31, 0, 0, 0, 0, time.UTC)}
		if got := claims.GracePeriod(); got != license.MaxGracePeriod {
			t.Fatalf("GracePeriod() = %v, want the %v clamp", got, license.MaxGracePeriod)
		}
		if got := claims.Status(expires.Add(license.MaxGracePeriod).Add(time.Second)); got != license.StatusExpired {
			t.Fatalf("past the clamp = %q, want %q", got, license.StatusExpired)
		}
	})

	t.Run("an over-wide window is clamped to the canon, never honored beyond it", func(t *testing.T) {
		claims := license.Claims{ExpiresAt: expires, GraceUntil: expires.Add(10 * 365 * 24 * time.Hour)}
		if got := claims.GracePeriod(); got != license.MaxGracePeriod {
			t.Fatalf("GracePeriod() = %v, want the %v clamp", got, license.MaxGracePeriod)
		}
		if got := claims.Status(expires.Add(license.MaxGracePeriod)); got != license.StatusGrace {
			t.Fatalf("inside the clamp = %q, want %q", got, license.StatusGrace)
		}
		if got := claims.Status(expires.Add(license.MaxGracePeriod).Add(time.Second)); got != license.StatusExpired {
			t.Fatalf("past the clamp = %q, want %q — an over-wide attestation must not be honored", got, license.StatusExpired)
		}
	})

	// A grace window on a termless blob grants nothing either: without a term there is
	// no expiry to extend.
	t.Run("grace on a termless blob grants nothing", func(t *testing.T) {
		claims := license.Claims{GraceUntil: expires.Add(time.Hour)}
		if got := claims.Status(expires); got != license.StatusExpired {
			t.Fatalf("termless with grace = %q, want %q", got, license.StatusExpired)
		}
		if got := claims.GracePeriod(); got != 0 {
			t.Fatalf("GracePeriod() on a termless blob = %v, want 0", got)
		}
	})

	// The canon figure itself, pinned so a silent edit of the constant fails here.
	if license.MaxGracePeriod != 168*time.Hour {
		t.Fatalf("MaxGracePeriod = %v, want 168h (design/PRICING-CANON.md, docs/07 ADR-0010)", license.MaxGracePeriod)
	}
}

// CHANGED BY. NormalizedProfile still normalizes — it is a display/record label and
// unknown values still fold to "online". What it no longer does is decide a grace window:
// GracePeriod is now a function of the ATTESTATION alone, so this test pins exactly that
// independence. If someone reintroduces a profile-derived fallback, this fails.
func TestNormalizedProfileAndGracePeriod(t *testing.T) {
	expires := time.Date(2026, time.April, 5, 6, 7, 8, 0, time.UTC)
	cases := []struct {
		profile string
		want    string
	}{
		{profile: "online", want: "online"},
		{profile: "airgapped", want: "airgapped"},
		{profile: "trial", want: "trial"},
		{profile: "", want: "online"},
		{profile: "future-profile", want: "online"},
	}
	for _, tc := range cases {
		claims := license.Claims{Profile: tc.profile, ExpiresAt: expires}
		if got := claims.NormalizedProfile(); got != tc.want {
			t.Errorf("NormalizedProfile(%q) = %q, want %q", tc.profile, got, tc.want)
		}
		if got := claims.GracePeriod(); got != 0 {
			t.Errorf("GracePeriod(%q) with nothing attested = %v, want 0 for EVERY profile", tc.profile, got)
		}
		withGrace := claims
		withGrace.GraceUntil = expires.Add(48 * time.Hour)
		if got := withGrace.GracePeriod(); got != 48*time.Hour {
			t.Errorf("GracePeriod(%q) with 48h attested = %v, want 48h for EVERY profile", tc.profile, got)
		}
	}
}

func TestRevocationEmptyEntriesAndLegacySerial(t *testing.T) {
	issued := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
	emptyClaims := license.Claims{}
	emptyLists := license.Revocation{
		Serials:   []string{""},
		HolderIDs: []string{""},
	}
	if emptyClaims.RevokedBy(emptyLists) {
		t.Fatal("empty revocation entries matched empty claim fields")
	}

	legacy := license.Claims{HolderID: "legacy-holder", IssuedAt: issued}
	if legacy.RevokedBy(license.Revocation{Serials: []string{"", "serial-1"}}) {
		t.Fatal("legacy claim without serial matched serial revocation")
	}
	if !legacy.RevokedBy(license.Revocation{HolderIDs: []string{"legacy-holder"}}) {
		t.Fatal("legacy claim was not revoked by holder ID")
	}
	if !legacy.RevokedBy(license.Revocation{LicenseKeyEpoch: issued.Unix() + 1}) {
		t.Fatal("legacy claim was not revoked by license-key epoch")
	}
	if legacy.RevokedBy(license.Revocation{LicenseKeyEpoch: issued.Unix()}) {
		t.Fatal("license-key epoch must be strictly later than issuance")
	}
	if emptyClaims.RevokedBy(license.Revocation{LicenseKeyEpoch: issued.Unix() + 1}) {
		t.Fatal("license-key epoch matched a zero issuance time")
	}
}

func TestRevocationPrecedesPerpetualStatus(t *testing.T) {
	claims := license.Claims{Serial: "perpetual-1"}
	r := license.Revocation{Serials: []string{"perpetual-1"}}
	if got := claims.StatusWithRevocation(time.Now(), r); got != license.StatusRevoked {
		t.Fatalf("StatusWithRevocation() = %q, want %q", got, license.StatusRevoked)
	}
}

func TestLegacyBlobWithoutAdditiveFields(t *testing.T) {
	pub, priv, err := license.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"licensee":"Legacy Corp","issued_at":"2026-01-02T03:04:05Z"}`)
	sig := ed25519.Sign(priv, payload)
	enc := base64.RawURLEncoding
	blob := enc.EncodeToString(payload) + "." + enc.EncodeToString(sig)

	claims, err := license.Verify(blob, pub)
	if err != nil {
		t.Fatalf("verify legacy blob: %v", err)
	}
	if claims.Serial != "" {
		t.Fatalf("legacy serial = %q, want empty", claims.Serial)
	}
	if claims.Profile != "" {
		t.Fatalf("legacy profile = %q, want empty", claims.Profile)
	}
}

// GraceUntil must survive the wire, not just exist in the struct. Sign → Verify → compare,
// because a field that marshals but does not unmarshal would silently mean "no grace" on
// every real deployment while every in-process test passed.
func TestGraceUntilRoundTripsOnTheWire(t *testing.T) {
	pub, priv, _ := license.GenerateKey()
	issued := time.Date(2026, time.August, 2, 9, 0, 0, 0, time.UTC)
	expires := issued.Add(30 * 24 * time.Hour)
	in := license.Claims{
		Licensee: "Zeta", Serial: "z-1", Profile: "online",
		IssuedAt: issued, ExpiresAt: expires, GraceUntil: expires.Add(license.MaxGracePeriod),
	}
	blob, err := license.Sign(in, priv)
	if err != nil {
		t.Fatal(err)
	}
	out, err := license.Verify(blob, pub)
	if err != nil {
		t.Fatal(err)
	}
	if !out.GraceUntil.Equal(in.GraceUntil) {
		t.Fatalf("GraceUntil round-trip = %v, want %v", out.GraceUntil, in.GraceUntil)
	}
	if got := out.GracePeriod(); got != license.MaxGracePeriod {
		t.Fatalf("GracePeriod after round-trip = %v, want %v", got, license.MaxGracePeriod)
	}
	if got := out.Status(expires.Add(time.Hour)); got != license.StatusGrace {
		t.Fatalf("status inside the round-tripped window = %q, want grace", got)
	}

	// And a blob with NO attested window round-trips to the zero time, not to a default.
	noGrace := in
	noGrace.GraceUntil = time.Time{}
	blob2, err := license.Sign(noGrace, priv)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := license.Verify(blob2, pub)
	if err != nil {
		t.Fatal(err)
	}
	if !out2.GraceUntil.IsZero() {
		t.Fatalf("absent grace_until decoded to %v, want the zero time", out2.GraceUntil)
	}
}

// THE ROLLOUT HAZARD, demonstrated rather than described (an adversarial contrast raised it
// and it is real). An engine compiled BEFORE grace_until existed parses a new blob happily —
// encoding/json ignores unknown fields — and then applies its OWN profile-derived grace. So a
// license this code intends to carry a 168h window is honored as THIRTY DAYS by an
// un-upgraded binary.
//
// Nothing in this package can fix that: the old classifier is baked into a shipped artifact.
// What this test does is make the hazard visible and keep it visible, so the rollout order —
// upgrade engines, or fence old issuance with Revocation.LicenseKeyEpoch — is a decision
// someone takes rather than a surprise someone discovers. Today the practical impact is nil:
// the product is pre-launch and no customer blob exists.
func TestOldVerifierIgnoresTheAttestedWindow(t *testing.T) {
	pub, priv, _ := license.GenerateKey()
	issued := time.Date(2026, time.August, 2, 9, 0, 0, 0, time.UTC)
	expires := issued.Add(30 * 24 * time.Hour)
	blob, err := license.Sign(license.Claims{
		Licensee: "Zeta", Serial: "z-1", Profile: "online",
		IssuedAt: issued, ExpiresAt: expires, GraceUntil: expires.Add(license.MaxGracePeriod),
	}, priv)
	if err != nil {
		t.Fatal(err)
	}

	// The blob still verifies under the old signature check — that is the whole problem: it
	// is a perfectly valid license, just read by a classifier that cannot see one field.
	c, err := license.Verify(blob, pub)
	if err != nil {
		t.Fatalf("a new blob must still verify for an old engine: %v", err)
	}

	// The pre classifier, reproduced exactly: profile-derived grace, no grace_until.
	oldGrace := func(profile string) time.Duration {
		switch profile {
		case "airgapped":
			return 90 * 24 * time.Hour
		case "trial":
			return 0
		default:
			return 30 * 24 * time.Hour
		}
	}
	oldStatus := func(c license.Claims, now time.Time) license.Status {
		if c.ExpiresAt.IsZero() {
			return license.StatusPerpetual
		}
		if !now.After(c.ExpiresAt) {
			return license.StatusValid
		}
		if !now.After(c.ExpiresAt.Add(oldGrace(c.NormalizedProfile()))) {
			return license.StatusGrace
		}
		return license.StatusExpired
	}

	// Ten days past the term: this code says expired (the attested window was 168h); the old
	// one still says grace. That gap is the hazard, and it is asserted so nobody can quietly
	// "fix" the new classifier back into agreement.
	tenDaysPast := expires.Add(10 * 24 * time.Hour)
	if got := c.Status(tenDaysPast); got != license.StatusExpired {
		t.Fatalf("current classifier at T+10d = %q, want expired", got)
	}
	if got := oldStatus(c, tenDaysPast); got != license.StatusGrace {
		t.Fatalf("the pre-attestation classifier at T+10d = %q, want grace — if this changed, re-check the rollout note", got)
	}
}
