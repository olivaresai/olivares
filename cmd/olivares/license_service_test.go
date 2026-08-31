// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/store"
)

// newTestLicenseService wires a real authenticator over an in-memory store (so the
// downgrade-acknowledge guard runs against a LIVE active-user count) plus a holder and
// service over a temp data dir. It returns the service, the authenticator and the
// mutable env map the override tests poke.
func newTestLicenseService(t *testing.T) (*licenseService, *auth.Authenticator, map[string]string) {
	t.Helper()
	ctx := context.Background()
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	authr := auth.NewAuthenticator(st, nil).WithSeatPolicy(auth.NewCommunitySeatPolicy())
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	holder := newLicenseHolder(license.DefaultPublicKey(), licenseSource{Kind: licenseSourceNone}, time.Now, discardLogger())
	svc := newLicenseService(holder, authr, t.TempDir(), "", getenv, "community", discardLogger())
	return svc, authr, env
}

func fillActiveUsers(t *testing.T, a *auth.Authenticator, n int) {
	t.Helper()
	ctx := context.Background()
	if _, err := a.BootstrapSuperadmin(ctx, "admin@acme.test", "supersecret-pw"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	actor := mustTestOperator("admin")
	for i := 0; i < n-1; i++ {
		if _, err := a.CreateUser(ctx, actor, auth.NewUser{Email: fmt.Sprintf("u%d@acme.test", i), DisplayName: "U", Password: "password-1x"}); err != nil {
			t.Fatalf("create user %d: %v", i, err)
		}
	}
}

// A clean install of a valid license verifies, persists to the canonical file and
// HOT-APPLIES (the holder lifts immediately), with the file readable on disk.
func TestLicenseServiceInstallHotApplies(t *testing.T) {
	svc, _, _ := newTestLicenseService(t)
	ctx := context.Background()
	blob := signTestLicense(t, license.Claims{Licensee: "Acme", MaxUsers: 50, IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(24 * time.Hour)})

	st, err := svc.InstallLicense(ctx, blob, false)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if st.Status != "valid" || st.Licensee != "Acme" || st.MaxUsers != 50 {
		t.Fatalf("status = %+v, want valid/Acme/50", st)
	}
	if st.Source != licenseSourceDataDir {
		t.Fatalf("source = %q, want data-dir", st.Source)
	}
	// Hot-applied: the holder lifts now, no restart.
	if c, ok := svc.holder.claims(); !ok || c.MaxUsers != 50 {
		t.Fatalf("holder claims after install = (%+v,%v), want MaxUsers=50", c, ok)
	}
	// Persisted on disk.
	if _, err := os.Stat(licenseDataDirPath(svc.dataDir)); err != nil {
		t.Fatalf("license file not written: %v", err)
	}
}

// B10: a license attesting FEWER seats than the deployment runs is not a downgrade
// any more — accounts are unlimited in every tier — so it installs with no
// acknowledge, and account creation keeps working afterwards.
func TestLicenseServiceSeatAttestationNeverBlocksInstall(t *testing.T) {
	svc, authr, _ := newTestLicenseService(t)
	ctx := context.Background()
	fillActiveUsers(t, authr, 8) // well past the old cap of 3

	blob := signTestLicense(t, license.Claims{Licensee: "Acme", MaxUsers: 1, IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(24 * time.Hour)})
	st, err := svc.InstallLicense(ctx, blob, false) // NO acknowledge
	if err != nil {
		t.Fatalf("install of a 1-seat attestation over 8 active accounts must succeed: %v", err)
	}
	if st.SeatLimited || st.SeatLimit > 0 {
		t.Fatalf("status = (seat_limit=%d, seat_limited=%v), want unlimited (0,false)", st.SeatLimit, st.SeatLimited)
	}
	if st.ActiveUsers != 8 {
		t.Fatalf("active users = %d, want the honest usage figure 8", st.ActiveUsers)
	}
	// And the attested figure did not become a runtime limit.
	actor := mustTestOperator("admin")
	if _, err := authr.CreateUser(ctx, actor, auth.NewUser{Email: "after-install@acme.test", DisplayName: "U"}); err != nil {
		t.Fatalf("account creation under a 1-seat attestation: %v", err)
	}
}

// B10: a license LAPSE (expiry, or removing it outright) must never degrade the
// deployment to the old community cap of 3 — the downgrade-to-3 is gone. This is the
// regression test for "retirar el downgrade a 3".
func TestLicenseServiceLapseDoesNotCapUsers(t *testing.T) {
	svc, authr, _ := newTestLicenseService(t)
	ctx := context.Background()
	fillActiveUsers(t, authr, 12) // 12 active accounts under a licensed deployment
	actor := mustTestOperator("admin")

	// An EXPIRED license, past its profile's grace window (30d by default) so the
	// status is "expired" and not "grace": the old behavior dropped the entitlement
	// to 3 seats at exactly this point.
	expired := signTestLicense(t, license.Claims{
		Licensee: "Acme", MaxUsers: 50,
		IssuedAt:  time.Now().UTC().Add(-400 * 24 * time.Hour),
		ExpiresAt: time.Now().UTC().Add(-90 * 24 * time.Hour),
	})
	st, err := svc.InstallLicense(ctx, expired, false)
	if err != nil {
		t.Fatalf("installing an expired license must succeed (it just reports expired): %v", err)
	}
	if st.Status != "expired" {
		t.Fatalf("status = %q, want expired", st.Status)
	}
	if st.SeatLimited || st.SeatLimit > 0 {
		t.Fatalf("an expired license must not impose a limit, got (%d,%v)", st.SeatLimit, st.SeatLimited)
	}
	if _, err := authr.CreateUser(ctx, actor, auth.NewUser{Email: "after-expiry@acme.test", DisplayName: "U"}); err != nil {
		t.Fatalf("account creation after a license lapse: %v", err)
	}

	// Removing the license entirely (back to the community edition) — no acknowledge,
	// no cap, and creation still works with far more than 3 active accounts.
	st, err = svc.UninstallLicense(ctx, false)
	if err != nil {
		t.Fatalf("uninstall with 13 active accounts must succeed without acknowledge: %v", err)
	}
	if st.Status != "none" || st.SeatLimited || st.SeatLimit > 0 {
		t.Fatalf("after uninstall status = (%q, seat_limit=%d, seat_limited=%v), want (none,0,false)", st.Status, st.SeatLimit, st.SeatLimited)
	}
	if _, err := authr.CreateUser(ctx, actor, auth.NewUser{Email: "after-uninstall@acme.test", DisplayName: "U"}); err != nil {
		t.Fatalf("account creation on the community edition: %v", err)
	}
	count, _, err := authr.ActiveUserCount(ctx, seatDisplayBound)
	if err != nil {
		t.Fatalf("ActiveUserCount: %v", err)
	}
	if count != 14 {
		t.Fatalf("active users = %d, want 14 (12 seeded + one after expiry + one after uninstall)", count)
	}
}

// An invalid blob is refused before anything is persisted (a paste/format error).
func TestLicenseServiceRejectsInvalidBlob(t *testing.T) {
	svc, _, _ := newTestLicenseService(t)
	if _, err := svc.InstallLicense(context.Background(), "not-a-valid-license", false); !errors.Is(err, api.ErrLicenseInvalid) {
		t.Fatalf("invalid blob err = %v, want ErrLicenseInvalid", err)
	}
	if _, err := os.Stat(licenseDataDirPath(svc.dataDir)); !os.IsNotExist(err) {
		t.Fatal("an invalid blob must not be persisted")
	}
}

// When a higher-precedence boot override is active, console/CLI install is refused
// (it would write a shadowed file) — honest, not a silent no-op.
func TestLicenseServiceRefusesUnderExternalOverride(t *testing.T) {
	svc, _, env := newTestLicenseService(t)
	env["OLIVARES_LICENSE"] = "some-inline-blob"
	blob := signTestLicense(t, license.Claims{Licensee: "Acme", IssuedAt: time.Now().UTC()})
	if _, err := svc.InstallLicense(context.Background(), blob, false); !errors.Is(err, api.ErrLicenseManagedExternally) {
		t.Fatalf("install under override err = %v, want ErrLicenseManagedExternally", err)
	}
	if _, err := svc.UninstallLicense(context.Background(), true); !errors.Is(err, api.ErrLicenseManagedExternally) {
		t.Fatalf("uninstall under override err = %v, want ErrLicenseManagedExternally", err)
	}
}

// Uninstall removes the file and hot-reverts to community; the status reads "none".
func TestLicenseServiceUninstall(t *testing.T) {
	svc, _, _ := newTestLicenseService(t)
	ctx := context.Background()
	blob := signTestLicense(t, license.Claims{Licensee: "Acme", MaxUsers: 50, IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(24 * time.Hour)})
	if _, err := svc.InstallLicense(ctx, blob, false); err != nil {
		t.Fatalf("install: %v", err)
	}
	st, err := svc.UninstallLicense(ctx, false)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if st.Status != "none" {
		t.Fatalf("status after uninstall = %q, want none", st.Status)
	}
	if _, ok := svc.holder.claims(); ok {
		t.Fatal("after uninstall the holder must not lift")
	}
	if _, err := os.Stat(licenseDataDirPath(svc.dataDir)); !os.IsNotExist(err) {
		t.Fatal("license file must be removed on uninstall")
	}
}

// LicenseDisplay is the cheap, no-DB live status for server-info, and it tracks the
// live holder.
func TestLicenseServiceDisplayTracksHolder(t *testing.T) {
	svc, _, _ := newTestLicenseService(t)
	if d := svc.LicenseDisplay(); d.Status != "none" {
		t.Fatalf("initial display = %q, want none", d.Status)
	}
	// CHANGED BY: the fixture carries a TERM. It used to be issued without an
	// expiry and expect "perpetual"; v8 is term-only, so a termless blob would now read
	// "expired" and this test would be asserting the wrong thing about a live license.
	now := time.Now().UTC()
	blob := signTestLicense(t, license.Claims{
		Licensee: "Acme", Plan: "commercial", SupportTier: "enterprise",
		IssuedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour),
	})
	if _, err := svc.InstallLicense(context.Background(), blob, false); err != nil {
		t.Fatalf("install: %v", err)
	}
	d := svc.LicenseDisplay()
	if d.Status != "valid" || d.Licensee != "Acme" {
		t.Fatalf("display after install = (%q,%q), want valid/Acme", d.Status, d.Licensee)
	}
	// The attested display-only labels surface on the cheap server-info path.
	if d.Plan != "commercial" || d.SupportTier != "enterprise" {
		t.Fatalf("display labels = (plan=%q, support=%q), want commercial/enterprise", d.Plan, d.SupportTier)
	}
}
