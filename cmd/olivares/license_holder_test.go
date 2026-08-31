// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/license"
)

// discardLogger is defined in orchdispatch_test.go (same test package).

func signTestLicense(t *testing.T, c license.Claims) string {
	t.Helper()
	blob, err := license.Sign(c, license.DevPrivateKey())
	if err != nil {
		t.Fatalf("sign test license: %v", err)
	}
	return blob
}

// The holder is the hot-apply heart: a swap takes effect at the next claims() read,
// with no restart (§3 point 3). The open binary never consults claims(), but the
// holder's behavior is what the enterprise seat policy rides.
func TestLicenseHolderHotApply(t *testing.T) {
	pub := license.DefaultPublicKey()
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	h := newLicenseHolder(pub, licenseSource{Kind: licenseSourceNone}, clock, discardLogger())
	if _, ok := h.claims(); ok {
		t.Fatal("an empty holder must not lift")
	}
	if got := h.display().status; got != "none" {
		t.Fatalf("display status = %q, want none", got)
	}

	// Hot-apply a valid license with MaxUsers=10 — it must take effect immediately.
	blob := signTestLicense(t, license.Claims{Licensee: "Acme", MaxUsers: 10, IssuedAt: now, ExpiresAt: now.Add(24 * time.Hour)})
	d := h.set(licenseSource{Blob: blob, Kind: licenseSourceDataDir})
	if d.status != "valid" {
		t.Fatalf("after set, display = %q, want valid", d.status)
	}
	c, ok := h.claims()
	if !ok || c.MaxUsers != 10 || c.Licensee != "Acme" {
		t.Fatalf("claims after hot-apply = (%+v, %v), want MaxUsers=10 Acme ok", c, ok)
	}
}

// Expiry degrades the lift WITHOUT a restart or crash — through the grace window the
// ISSUANCE attested, and only that one.
//
// CHANGED BY with the reason. This test used to assert the shape: past
// ExpiresAt the "online" profile kept lifting for 30 DAYS, a figure the verifier invented
// from the profile and no signature ever carried. The v8 canon signs G = T+168h, granted
// once per rolling 365 days and only for an INVOLUNTARY lapse — facts only the issuer
// holds. So the window now rides in the blob (Claims.GraceUntil) and the holder honors
// exactly what was signed. The behavior under test is unchanged in shape (valid → grace
// → expired, per call, no restart); what changed is where the number comes from.
func TestLicenseHolderExpiryDegradation(t *testing.T) {
	pub := license.DefaultPublicKey()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	cur := base
	clock := func() time.Time { return cur }

	expires := base.Add(time.Hour)
	blob := signTestLicense(t, license.Claims{
		Licensee: "Acme", MaxUsers: 5, IssuedAt: base,
		ExpiresAt: expires, GraceUntil: expires.Add(license.MaxGracePeriod),
	})
	h := newLicenseHolder(pub, licenseSource{Blob: blob, Kind: licenseSourceDataDir}, clock, discardLogger())

	if _, ok := h.claims(); !ok {
		t.Fatal("a valid, unexpired license must lift")
	}
	// 2h past expiry: inside the attested 168h window — the lift is MAINTAINED.
	cur = base.Add(2 * time.Hour)
	if _, ok := h.claims(); !ok {
		t.Fatal("an expired license inside its ATTESTED grace window must KEEP lifting")
	}
	if got := h.reEvaluate().status; got != "grace" {
		t.Fatalf("reEvaluate status = %q, want grace", got)
	}
	// One second past the attested window: the lift stops, no restart, no crash.
	cur = expires.Add(license.MaxGracePeriod).Add(time.Second)
	if _, ok := h.claims(); ok {
		t.Fatal("a license past its attested window must NOT lift")
	}
	if got := h.reEvaluate().status; got != "expired" {
		t.Fatalf("reEvaluate status = %q, want expired", got)
	}
}

// The other half of the same rule, and the one the old profile-derived design could not
// express: a license that attested NO grace stops lifting AT its expiry. That is what
// makes a voluntary cancellation cut at T (v8 canon) instead of buying another month.
func TestLicenseHolderWithoutAttestedGraceStopsAtExpiry(t *testing.T) {
	pub := license.DefaultPublicKey()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	cur := base
	clock := func() time.Time { return cur }

	expires := base.Add(time.Hour)
	blob := signTestLicense(t, license.Claims{
		Licensee: "Voluntary Exit", Profile: "online", MaxUsers: 5, IssuedAt: base, ExpiresAt: expires,
	})
	h := newLicenseHolder(pub, licenseSource{Blob: blob, Kind: licenseSourceDataDir}, clock, discardLogger())

	if _, ok := h.claims(); !ok {
		t.Fatal("a valid, unexpired license must lift")
	}
	cur = expires.Add(time.Second)
	if _, ok := h.claims(); ok {
		t.Fatal("no attested grace means the lift stops at the expiry, not thirty days later")
	}
	if got := h.reEvaluate().status; got != "expired" {
		t.Fatalf("reEvaluate status = %q, want expired", got)
	}
}

// A trial-profile license has NO grace: the lift stops the moment it expires.
func TestLicenseHolderTrialExpiresWithoutGrace(t *testing.T) {
	pub := license.DefaultPublicKey()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	cur := base
	clock := func() time.Time { return cur }

	blob := signTestLicense(t, license.Claims{Licensee: "Trial Co", Profile: "trial", Serial: "t-1",
		MaxUsers: 5, IssuedAt: base, ExpiresAt: base.Add(time.Hour)})
	h := newLicenseHolder(pub, licenseSource{Blob: blob, Kind: licenseSourceDataDir}, clock, discardLogger())

	if _, ok := h.claims(); !ok {
		t.Fatal("a valid trial must lift")
	}
	cur = base.Add(time.Hour + time.Second)
	if _, ok := h.claims(); ok {
		t.Fatal("an expired trial must not lift — trial grace is zero (§5.1)")
	}
	if got := h.reEvaluate().status; got != "expired" {
		t.Fatalf("reEvaluate status = %q, want expired", got)
	}
}

// A TERMLESS license does not lift; a malformed blob is reported as invalid and never
// lifts (verify-but-never-gate).
//
// CHANGED BY. This used to assert that a blob with no expiry lifts FOREVER, which
// was correct while perpetual licenses existed. The v8 package is term-only and LICENSING.md
// §ADR-0010 signs "no perpetual fallback", so a blob attesting no term now attests no
// right. Note what did NOT change: the blob still VERIFIES and is still displayed — the
// holder reports a status, it does not refuse anything.
func TestLicenseHolderTermlessAndInvalid(t *testing.T) {
	pub := license.DefaultPublicKey()
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	termless := signTestLicense(t, license.Claims{Licensee: "Acme", MaxUsers: 0, IssuedAt: now}) // no ExpiresAt
	h := newLicenseHolder(pub, licenseSource{Blob: termless, Kind: licenseSourceDataDir}, clock, discardLogger())
	if got := h.display().status; got != "expired" {
		t.Fatalf("termless display = %q, want expired (term-only)", got)
	}
	if d := h.display(); !d.verified || d.licensee != "Acme" {
		t.Fatalf("a termless blob must still verify and display its licensee, got verified=%v licensee=%q", d.verified, d.licensee)
	}
	if _, ok := h.claims(); ok {
		t.Fatal("a termless license must NOT lift")
	}

	h2 := newLicenseHolder(pub, licenseSource{Blob: "not-a-license", Kind: licenseSourceDataDir}, clock, discardLogger())
	if got := h2.display().status; got != "invalid" {
		t.Fatalf("garbage display = %q, want invalid", got)
	}
	if _, ok := h2.claims(); ok {
		t.Fatal("an invalid blob must NOT lift")
	}
}

// A hot-applied license must NEVER be reverted by the concurrent expiry monitor
// (reEvaluate) or by concurrent reads — reEvaluate is read-and-reclassify only and
// must never write h.src. This is the regression guard for the lost-update bug the
// adversarial review found: it runs the real contended interleaving (set() racing
// reEvaluate()/claims()/display()) that the single-goroutine tests never exercise,
// so `go test -race` actually observes it. Run under -race in the suite.
func TestLicenseHolderConcurrentHotApplyNotLost(t *testing.T) {
	pub := license.DefaultPublicKey()
	now := time.Now().UTC()
	clock := func() time.Time { return now }
	valid := signTestLicense(t, license.Claims{Licensee: "Acme", MaxUsers: 10, IssuedAt: now, ExpiresAt: now.Add(24 * time.Hour)})

	for iter := 0; iter < 200; iter++ {
		h := newLicenseHolder(pub, licenseSource{Kind: licenseSourceNone}, clock, discardLogger())
		var wg sync.WaitGroup
		stop := make(chan struct{})
		// The expiry monitor + readers, exactly the production contention.
		for r := 0; r < 4; r++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
						h.reEvaluate()
						_, _ = h.claims()
						_ = h.display()
					}
				}
			}()
		}
		// Hot-apply a valid license while the monitor ticks.
		h.set(licenseSource{Blob: valid, Kind: licenseSourceDataDir})
		runtime.Gosched()
		close(stop)
		wg.Wait()
		// The hot-applied license must still be in force — never reverted by reEvaluate.
		if c, ok := h.claims(); !ok || c.MaxUsers != 10 {
			t.Fatalf("iter %d: the hot-applied license was lost under concurrency: claims=(%+v,%v)", iter, c, ok)
		}
	}
}
