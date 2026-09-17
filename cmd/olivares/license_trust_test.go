// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/license"
)

func trustTestKey(b byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{b}, ed25519.SeedSize))
	return priv.Public().(ed25519.PublicKey), priv
}

// trustTestCredential signs a live v3 credential whose key_id is derived from priv.
func trustTestCredential(t *testing.T, priv ed25519.PrivateKey, deployment string, epoch int, issued time.Time) string {
	t.Helper()
	kid, err := license.KeyID(priv.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	iat := issued.UTC().Format(time.RFC3339)
	payload := fmt.Sprintf(`{"schema":"olivares.commercial.credential.v3","serial":"cred_%s","issue_seq":1,"key_id":%q,`+
		`"key_epoch":%d,"issued_at":%q,"not_before":%q,"entity_id":"cus_1","deployment_id":%q,"purpose":"production",`+
		`"licensee":{"display_name":"Trust Test S.L."},"grants":[{"grant_id":"gr_base","order_line_id":"ol_base",`+
		`"product_id":"pdt_business","kind":"base","cadence":"year","paid_through":"2099-01-01T00:00:00Z",`+
		`"expires_at":"2099-01-01T00:00:00Z","issuance_phase":"term","guarantee_deadline":null,`+
		`"promotion_hold_deadline":null,"lease_until":null}]}`, deployment, kid, epoch, iat, iat, deployment)
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(payload)) + "." + enc.EncodeToString(ed25519.Sign(priv, []byte(payload)))
}

func trustCLI(t *testing.T, args ...string) (int, map[string]any, string) {
	t.Helper()
	code, stdout, stderr, _ := runCLIExit(t, args...)
	var rep map[string]any
	_ = json.Unmarshal([]byte(stdout), &rep)
	return code, rep, stdout + stderr
}

// newDirLicenseService is the engine's license service over dir, as boot builds it.
func newDirLicenseService(t *testing.T, dir string) (*licenseService, *licenseHolder) {
	t.Helper()
	svc, _, _ := newTestLicenseService(t)
	src, err := resolveLicense("", dir, osGetenv)
	if err != nil {
		t.Fatal(err)
	}
	holder := newDataDirLicenseHolder(dir, src, time.Now, discardLogger())
	return newLicenseService(holder, svc.authr, dir, "", osGetenv, "community", discardLogger()), holder
}

var _ = auth.NewCommunitySeatPolicy // the service fixture's authenticator comes from newTestLicenseService

// TestLicenseTrustIsOneKeyringForBootReloadInstallUpgradeAndDoctor applies the same
// administrative trust change to every consumer and expects the same answer from each.
func TestLicenseTrustIsOneKeyringForBootReloadInstallUpgradeAndDoctor(t *testing.T) {
	t.Setenv("OLIVARES_LICENSE", "")
	t.Setenv("OLIVARES_LICENSE_PATH", "")
	dir := t.TempDir()
	pubB, privB := trustTestKey(0xB1)
	blob := trustTestCredential(t, privB, "dep_trust", 1, time.Now().Add(-time.Hour))
	if err := os.WriteFile(licenseDataDirPath(dir), []byte(blob+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, holder := newDirLicenseService(t, dir)
	ctx := context.Background()

	expect := func(stage string, wantStatus string, wantErr error) {
		t.Helper()
		svc.Reconcile(ctx)
		d := holder.display()
		if d.status != wantStatus || (wantErr != nil && !errors.Is(d.reason, wantErr)) {
			t.Fatalf("%s: reload status %q (%v), want %q (%v)", stage, d.status, d.reason, wantStatus, wantErr)
		}
		src, _ := resolveLicense("", dir, osGetenv)
		boot := newDataDirLicenseHolder(dir, src, time.Now, discardLogger()).display()
		if boot.status != d.status {
			t.Fatalf("%s: boot says %q, reload says %q", stage, boot.status, d.status)
		}
		_, gateErr := requireValidLicense("", dir)
		doc := doctorLicenseCheck(dir)
		if wantErr == nil {
			if gateErr != nil || doc.Status != "pass" {
				t.Fatalf("%s: upgrade gate %v, doctor %q", stage, gateErr, doc.Status)
			}
		} else if !errors.Is(gateErr, wantErr) || doc.Status != "fail" {
			t.Fatalf("%s: upgrade gate %v (want %v), doctor %q", stage, gateErr, wantErr, doc.Status)
		}
	}

	expect("untrusted key", "invalid", license.ErrKeyUnknown)

	if code, _, out := trustCLI(t, "license", "trust", "set", "--data-dir", dir, "--public-key", base64.StdEncoding.EncodeToString(pubB), "--state", "current", "--epoch", "1"); code != exitcode.OK {
		t.Fatalf("trust set: %d %s", code, out)
	}
	expect("administratively trusted", "valid", nil)
	if d := holder.display(); d.lic.Trust.Source != "data-dir" || d.lic.Trust.State != license.KeyStateCurrent {
		t.Fatalf("trust recorded for the verified license: %+v", d.lic.Trust)
	}

	if code, _, out := trustCLI(t, "license", "trust", "set", "--data-dir", dir, "--public-key", base64.StdEncoding.EncodeToString(pubB), "--state", "current", "--epoch", "2"); code != exitcode.OK {
		t.Fatalf("trust set epoch: %d %s", code, out)
	}
	expect("pinned to another epoch", "invalid", license.ErrKeyEpochFenced)

	kid, _ := license.KeyID(pubB)
	if code, _, out := trustCLI(t, "license", "trust", "set", "--data-dir", dir, "--kid", kid, "--state", "revoked"); code != exitcode.OK {
		t.Fatalf("trust revoke: %d %s", code, out)
	}
	expect("revoked", "invalid", license.ErrKeyRevoked)

	// An install signed by the revoked key is refused by the console path and the CLI, and the
	// installed file is unchanged.
	before, _ := os.ReadFile(licenseDataDirPath(dir))
	other := trustTestCredential(t, privB, "dep_other", 1, time.Now())
	if _, err := svc.InstallLicense(ctx, other, false); err == nil || !errors.Is(err, license.ErrKeyRevoked) {
		t.Fatalf("console install of a revoked-key credential: %v", err)
	}
	src := filepath.Join(t.TempDir(), "other.license")
	if err := os.WriteFile(src, []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := trustCLI(t, "license", "install", src, "--data-dir", dir); code == exitcode.OK {
		t.Fatal("CLI install of a revoked-key credential succeeded")
	}
	if after, _ := os.ReadFile(licenseDataDirPath(dir)); !bytes.Equal(after, before) {
		t.Fatal("a refused install changed the installed license")
	}

	// A trust document others can write is not trusted; every consumer says so.
	if err := os.Chmod(licenseTrustPath(dir), 0o666); err != nil {
		t.Fatal(err)
	}
	expect("unsafe trust document", "invalid", errLicenseTrustUnsafe)
	if code, _, _ := trustCLI(t, "license", "trust", "status", "--data-dir", dir); code == exitcode.OK {
		t.Fatal("trust status must refuse an unsafe document")
	}
}

// TestLicenseTrustVerifyOnlyRotationSurvivesRestart is acceptance §7.9 on the client: key A is
// verify-only while B signs; A's earlier credential still verifies, a credential A signs after its
// retirement does not, and a credential installed under B verifies again after a restart from
// the persisted trust alone.
func TestLicenseTrustVerifyOnlyRotationSurvivesRestart(t *testing.T) {
	t.Setenv("OLIVARES_LICENSE", "")
	t.Setenv("OLIVARES_LICENSE_PATH", "")
	dir := t.TempDir()
	pubA, privA := trustTestKey(0xA1)
	pubB, privB := trustTestKey(0xB2)
	retired := time.Now().Add(-24 * time.Hour).UTC().Truncate(time.Second)
	for _, args := range [][]string{
		{"--public-key", base64.StdEncoding.EncodeToString(pubA), "--state", "verify_only", "--epoch", "1", "--retired-at", retired.Format(time.RFC3339)},
		{"--public-key", base64.StdEncoding.EncodeToString(pubB), "--state", "current", "--epoch", "2"},
	} {
		if code, _, out := trustCLI(t, append([]string{"license", "trust", "set", "--data-dir", dir}, args...)...); code != exitcode.OK {
			t.Fatalf("trust set %v: %d %s", args, code, out)
		}
	}
	kr, err := licenseKeyringForDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kr.Verify(trustTestCredential(t, privA, "dep_a", 1, retired.Add(-time.Hour)), time.Now()); err != nil {
		t.Fatalf("A's credential issued before retirement: %v", err)
	}
	if _, err := kr.Verify(trustTestCredential(t, privA, "dep_a", 1, retired.Add(time.Hour)), time.Now()); !errors.Is(err, license.ErrKeyRetired) {
		t.Fatalf("A's credential issued after retirement: %v", err)
	}

	credB := filepath.Join(t.TempDir(), "b.license")
	if err := os.WriteFile(credB, []byte(trustTestCredential(t, privB, "dep_b", 2, time.Now())), 0o600); err != nil {
		t.Fatal(err)
	}
	code, rep, out := trustCLI(t, "license", "install", credB, "--data-dir", dir)
	if code != exitcode.OK {
		t.Fatalf("install under B: %d %s", code, out)
	}
	kidB, _ := license.KeyID(pubB)
	if tr, _ := rep["trust"].(map[string]any); tr["kid"] != kidB || tr["state"] != "current" {
		t.Fatalf("install report trust = %v", rep["trust"])
	}
	// Restart: a new holder built only from what is on disk.
	src, _ := resolveLicense("", dir, osGetenv)
	if d := newDataDirLicenseHolder(dir, src, time.Now, discardLogger()).display(); d.status != "valid" || d.lic.Trust.KID != kidB {
		t.Fatalf("after restart: %q %+v (%v)", d.status, d.lic.Trust, d.reason)
	}
	code, rep, out = trustCLI(t, "license", "status", "--data-dir", dir)
	if code != exitcode.OK || rep["status"] != "valid" {
		t.Fatalf("license status: %d %s", code, out)
	}
}

func TestLicenseTrustCLIStatusSetAndFence(t *testing.T) {
	dir := t.TempDir()
	code, rep, out := trustCLI(t, "license", "trust", "status", "--data-dir", dir)
	if code != exitcode.OK || rep["document"] != false {
		t.Fatalf("status without a document: %d %s", code, out)
	}
	keys, _ := rep["keys"].([]any)
	if license.HasDevKey && len(keys) != 1 {
		t.Fatalf("a dev build trusts exactly its embedded key by default, got %v", keys)
	}
	embeddedKID, _ := license.KeyID(license.DefaultPublicKey())
	if code, _, _ := trustCLI(t, "license", "trust", "set", "--data-dir", dir, "--kid", embeddedKID, "--state", "verify_only"); code == exitcode.OK {
		t.Fatal("verify_only without --retired-at must be refused")
	}
	if code, _, out := trustCLI(t, "license", "trust", "set", "--data-dir", dir, "--kid", embeddedKID, "--state", "verify_only", "--retired-at", "2026-09-01T00:00:00Z"); code != exitcode.OK {
		t.Fatalf("demote the embedded key: %d %s", code, out)
	}
	if code, _, _ := trustCLI(t, "license", "trust", "set", "--data-dir", dir, "--kid", "sha256:"+strings.Repeat("0", 64), "--state", "current"); code == exitcode.OK {
		t.Fatal("an unknown kid without its public key must be refused")
	}
	if code, _, _ := trustCLI(t, "license", "trust", "set", "--data-dir", dir, "--kid", embeddedKID, "--public-key", "AAAA", "--state", "current"); code == exitcode.OK {
		t.Fatal("--kid together with --public-key must be refused")
	}
	if code, _, _ := trustCLI(t, "license", "trust", "set", "--data-dir", dir, "--kid", embeddedKID, "--state", "verify_only", "--retired-at", "2026-09-01T00:00:00+02:00"); code == exitcode.OK {
		t.Fatal("a non-UTC instant must be refused")
	}
	if code, _, out := trustCLI(t, "license", "trust", "fence", "--data-dir", dir, "--min-key-epoch", "3"); code != exitcode.OK {
		t.Fatalf("fence: %d %s", code, out)
	}
	code, rep, out = trustCLI(t, "license", "trust", "status", "--data-dir", dir)
	if code != exitcode.OK || rep["document"] != true || rep["min_key_epoch"] != float64(3) {
		t.Fatalf("status after set and fence: %d %s", code, out)
	}
	for _, k := range rep["keys"].([]any) {
		if m := k.(map[string]any); m["kid"] == embeddedKID && (m["state"] != "verify_only" || m["source"] != "data-dir") {
			t.Fatalf("embedded key entry: %v", m)
		}
	}
	fi, err := os.Stat(licenseTrustPath(dir))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("trust document mode: %v %v", fi.Mode().Perm(), err)
	}
}

// newExplicitLicenseService is the engine's license service started with --license path.
func newExplicitLicenseService(t *testing.T, dir, path string) (*licenseService, *licenseHolder) {
	t.Helper()
	base, _, _ := newTestLicenseService(t)
	src, err := resolveLicense(path, dir, osGetenv)
	if err != nil {
		t.Fatal(err)
	}
	holder := newDataDirLicenseHolder(dir, src, time.Now, discardLogger())
	return newLicenseService(holder, base.authr, dir, path, osGetenv, "community", discardLogger()), holder
}

// TestLicenseTrustReloadAppliesCurrentTrustWhenTheSourceCannotBeRead is the reload boundary with the
// configured license source unavailable. The live license BLOB is kept — a failed read is not a
// removal — but it is verified under the trust as it is now: a revoked signer, an epoch fence and an
// unusable document take effect, and the consumers (claims, grants, status) stop lifting. An absent
// trust document leaves the embedded key alone, a different reason from an unusable one. Unchanged
// trust keeps the license valid, and a source that comes back is read again.
func TestLicenseTrustReloadAppliesCurrentTrustWhenTheSourceCannotBeRead(t *testing.T) {
	t.Setenv("OLIVARES_LICENSE", "")
	t.Setenv("OLIVARES_LICENSE_PATH", "")
	ctx := context.Background()
	pubB, privB := trustTestKey(0xB4)
	kidB, _ := license.KeyID(pubB)
	blob := trustTestCredential(t, privB, "dep_reload", 1, time.Now().Add(-time.Hour))
	writeTrust := func(t *testing.T, dir string, doc license.TrustDocument) {
		t.Helper()
		if _, err := writeLicenseTrustDocument(dir, doc); err != nil {
			t.Fatal(err)
		}
	}
	writeBlob := func(t *testing.T, path string) {
		t.Helper()
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(blob+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	keyB := func(state license.KeyState, epoch int) license.TrustDocumentKey {
		return license.TrustDocumentKey{PublicKey: pubB, State: state, Epoch: epoch}
	}

	sources := []struct {
		name string
		lose func(t *testing.T, path string)
	}{
		{"absent", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
		// A directory at the path: every read fails, also for a root test process.
		{"unreadable", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	}
	changes := []struct {
		name       string
		change     func(t *testing.T, dir string)
		wantStatus string
		wantErr    error
	}{
		{"trust unchanged", func(*testing.T, string) {}, "valid", nil},
		{"signer revoked", func(t *testing.T, dir string) {
			writeTrust(t, dir, license.TrustDocument{Keys: []license.TrustDocumentKey{keyB(license.KeyStateRevoked, 0)}})
		}, "invalid", license.ErrKeyRevoked},
		{"epoch fenced", func(t *testing.T, dir string) {
			writeTrust(t, dir, license.TrustDocument{MinKeyEpoch: 2, Keys: []license.TrustDocumentKey{keyB(license.KeyStateCurrent, 0)}})
		}, "invalid", license.ErrKeyEpochFenced},
		{"document writable by others", func(t *testing.T, dir string) {
			if err := os.Chmod(licenseTrustPath(dir), 0o666); err != nil {
				t.Fatal(err)
			}
		}, "invalid", errLicenseTrustUnsafe},
		{"document does not decode", func(t *testing.T, dir string) {
			if err := os.WriteFile(licenseTrustPath(dir), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "invalid", license.ErrKeyringInvalid},
		{"document removed", func(t *testing.T, dir string) {
			if err := os.Remove(licenseTrustPath(dir)); err != nil {
				t.Fatal(err)
			}
		}, "invalid", license.ErrKeyUnknown},
	}
	for _, src := range sources {
		for _, tc := range changes {
			t.Run(src.name+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				writeTrust(t, dir, license.TrustDocument{Keys: []license.TrustDocumentKey{keyB(license.KeyStateCurrent, 1)}})
				licPath := filepath.Join(t.TempDir(), "olivares.license")
				writeBlob(t, licPath)
				svc, holder := newExplicitLicenseService(t, dir, licPath)
				svc.Reconcile(ctx)
				if d := holder.display(); d.status != "valid" || d.lic.Trust.KID != kidB {
					t.Fatalf("baseline: %q %+v (%v)", d.status, d.lic.Trust, d.reason)
				}

				src.lose(t, licPath)
				if _, err := resolveLicense(licPath, dir, osGetenv); err == nil {
					t.Fatal("the configured source must be unreadable for this case")
				}
				tc.change(t, dir)
				svc.Reconcile(ctx)

				d := holder.display()
				if d.status != tc.wantStatus || (tc.wantErr != nil && !errors.Is(d.reason, tc.wantErr)) || (tc.wantErr == nil && d.reason != nil) {
					t.Fatalf("reload: %q (%v), want %q (%v)", d.status, d.reason, tc.wantStatus, tc.wantErr)
				}
				if d.source.Kind != licenseSourceFlag || d.source.Path != licPath {
					t.Fatalf("the kept license must keep its source: %+v", d.source)
				}
				lifts := tc.wantStatus == "valid"
				if _, ok := holder.claims(); ok != lifts {
					t.Fatalf("claims lift = %v, want %v", ok, lifts)
				}
				if _, ok := holder.grants(); ok != lifts {
					t.Fatalf("grants lift = %v, want %v", ok, lifts)
				}
				if st, err := svc.LicenseStatus(ctx); err != nil || st.Status != tc.wantStatus {
					t.Fatalf("license status = %q (%v)", st.Status, err)
				}
				// Boot with the same trust over the same blob gives the same answer.
				boot := newDataDirLicenseHolder(dir, licenseSource{Blob: blob, Kind: licenseSourceFlag, Path: licPath}, time.Now, discardLogger()).display()
				if boot.status != d.status {
					t.Fatalf("boot says %q, reload says %q", boot.status, d.status)
				}

				// The source comes back: reload reads it again, still under the current trust.
				writeBlob(t, licPath)
				svc.Reconcile(ctx)
				if d := holder.display(); d.status != tc.wantStatus {
					t.Fatalf("after the source returned: %q (%v)", d.status, d.reason)
				}
			})
		}
	}

	// Control: an ABSENT data-dir default is removal, not an unreadable source. It reads none.
	t.Run("absent data-dir default reads none", func(t *testing.T) {
		dir := t.TempDir()
		writeTrust(t, dir, license.TrustDocument{Keys: []license.TrustDocumentKey{keyB(license.KeyStateCurrent, 1)}})
		writeBlob(t, licenseDataDirPath(dir))
		svc, holder := newDirLicenseService(t, dir)
		svc.Reconcile(ctx)
		if d := holder.display(); d.status != "valid" {
			t.Fatalf("baseline: %q (%v)", d.status, d.reason)
		}
		if err := os.Remove(licenseDataDirPath(dir)); err != nil {
			t.Fatal(err)
		}
		svc.Reconcile(ctx)
		if d := holder.display(); d.status != "none" || d.source.Kind != licenseSourceNone {
			t.Fatalf("after removing the data-dir license: %q %+v", d.status, d.source)
		}
	})
}
