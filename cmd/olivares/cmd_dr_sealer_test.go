// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/model"
)

// drSealer is the Seal/Open shape the four data-dir sealers share.
type drSealer interface {
	Seal(ctx context.Context, scope model.TenantID, plaintext []byte) (string, error)
	Open(ctx context.Context, scope model.TenantID, sealed string) ([]byte, error)
}

// drSealers builds every data-dir sealer over dir, from the key files alone.
func drSealers(t *testing.T, dir string) map[string]drSealer {
	t.Helper()
	noEnv := func(string) string { return "" }
	secret, err := newSecretSealer(dir, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	totp, err := newTOTPSeedSealer(dir, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	sso, err := newFederationSealer(dir, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	eventing, err := newEventingSealer(dir, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]drSealer{
		secretStoreKeyFile: secret, totpSeedKeyFile: totp,
		federationSecretKeyFile: sso, eventingSecretKeyFile: eventing,
	}
}

// drSealerKeys pins, independently of the backup code, the four AEAD keys the
// engine mints in the data dir and the variable that supplies each instead.
var drSealerKeys = []struct{ name, env string }{
	{secretStoreKeyFile, secretStoreKeyEnv},
	{totpSeedKeyFile, totpSeedKeyEnv},
	{federationSecretKeyFile, federationSecretKeyEnv},
	{eventingSecretKeyFile, eventingSecretKeyEnv},
}

// clearSealerKeyEnv makes every sealer read its data-dir file, the default install,
// and stamps a release version: restore refuses a bundle from an unstamped binary.
func clearSealerKeyEnv(t *testing.T) {
	t.Helper()
	prevVersion := version
	version = "26.1001"
	t.Cleanup(func() { version = prevVersion })
	for _, k := range drSealerKeys {
		t.Setenv(k.env, "")
	}
}

// TestDRRestoreOpensWhatTheSourceSealed is the #493 reproducer. A provider key
// (secret store), a TOTP seed, an SSO secret and an eventing secret sealed on the
// source must open on an estate restored from its bundle into an empty data dir.
// Before the fix the bundle carried none of the four sealer keys, the restore boot
// minted fresh ones, every Open failed, and restore still said "key custody intact".
func TestDRRestoreOpensWhatTheSourceSealed(t *testing.T) {
	clearSealerKeyEnv(t)
	ctx := context.Background()
	scope := model.TenantID("tenant-493")
	src := t.TempDir()
	seedDataDir(t, src)

	sealed := map[string]string{}
	for name, s := range drSealers(t, src) {
		v, err := s.Seal(ctx, scope, []byte("sealed by "+name))
		if err != nil {
			t.Fatalf("seal with %s: %v", name, err)
		}
		sealed[name] = v
	}

	pf := l3Passphrase(t)
	bundle := filepath.Join(t.TempDir(), "estate.drbundle")
	if out, err := runDR("backup", "--data-dir", src, "--out", bundle, "--passphrase-file", pf); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	dst := filepath.Join(t.TempDir(), "restored")
	out, err := runDR("restore", "--in", bundle, "--data-dir", dst, "--engine", "sqlite", "--passphrase-file", pf)
	if err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	if !strings.Contains(out, "restore verified: ledger continuity and key custody intact") {
		t.Fatalf("a bundle that carries every sealer key must report custody intact:\n%s", out)
	}
	for name, s := range drSealers(t, dst) {
		got, err := s.Open(ctx, scope, sealed[name])
		if err != nil {
			t.Fatalf("%s: the restored estate cannot open what the source sealed: %v", name, err)
		}
		if string(got) != "sealed by "+name {
			t.Fatalf("%s opened to %q", name, got)
		}
	}
}

// TestDRRestoreNamesSealerKeysTheBundleDoesNotCarry keeps an older bundle
// restorable on a new host, and makes the restore name the sealer keys it cannot
// vouch for instead of saying "key custody intact". The bundle has neither the
// sealer files nor their probes, which is what every bundle written before #493
// holds, so nothing in it confirms a key a variable supplies either.
func TestDRRestoreNamesSealerKeysTheBundleDoesNotCarry(t *testing.T) {
	clearSealerKeyEnv(t)
	src := t.TempDir()
	seedDataDir(t, src)
	pf := l3Passphrase(t)
	bundle := filepath.Join(t.TempDir(), "estate.drbundle")
	if out, err := runDR("backup", "--data-dir", src, "--out", bundle, "--passphrase-file", pf); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	bundle = drLegacyBundle(t, bundle, pf)

	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"no variables", nil},
		{"variables", map[string]string{
			secretStoreKeyEnv: drValidSealerKey, totpSeedKeyEnv: drValidSealerKey,
			federationSecretKeyEnv: "not-a-key", eventingSecretKeyEnv: drValidSealerKey,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			dst := filepath.Join(t.TempDir(), "restored")
			out, err := runDR("restore", "--in", bundle, "--data-dir", dst, "--engine", "sqlite", "--passphrase-file", pf)
			if err != nil {
				t.Fatalf("an older bundle must still restore: %v\n%s", err, out)
			}
			if strings.Contains(out, "key custody intact") {
				t.Fatalf("restore claimed key custody intact without anything to check the sealer keys against:\n%s", out)
			}
			for _, k := range drSealerKeys {
				if !strings.Contains(out, k.name) || !strings.Contains(out, k.env) {
					t.Fatalf("restore must name %s and %s:\n%s", k.name, k.env, out)
				}
			}
		})
	}
}

// drValidSealerKey is a well-formed sealer key value (32 bytes, base64).
const drValidSealerKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

// drOtherSealerKey is a second well-formed sealer key, different from the first.
const drOtherSealerKey = "ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA="

// drLegacyBundle rewrites bundle as 26.10.1 and earlier wrote it: no sealer key
// files and no sealer probes, authenticated under the same passphrase.
func drLegacyBundle(t *testing.T, bundle, passFile string) string {
	t.Helper()
	work := t.TempDir()
	m, kek, err := openAndCheckBundle(bundle, work)
	if err != nil {
		t.Fatal(err)
	}
	pass, err := readPassphrase(passFile)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := dr.OpenCipher(pass, kek)
	if err != nil {
		t.Fatal(err)
	}
	sealer := map[string]bool{}
	for _, k := range drSealerKeys {
		sealer[k.name] = true
	}
	sealed := map[string][]byte{}
	var keys []dr.KeyRef
	for _, kr := range m.Keys {
		if sealer[kr.Name] {
			continue
		}
		if sealed[kr.File], err = os.ReadFile(filepath.Join(work, kr.File)); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, kr)
	}
	m.Keys, m.SealerProbes, m.Files = keys, nil, nil
	out := filepath.Join(t.TempDir(), "legacy.drbundle")
	f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	err = dr.WriteAuthenticatedBundle(f, dr.BundleInput{
		Manifest: m, KEK: kek, SnapshotPath: filepath.Join(work, m.Store.File), SealedKeys: sealed,
	}, cipher)
	if cerr := f.Close(); err != nil || cerr != nil {
		t.Fatalf("write legacy bundle: %v, close: %v", err, cerr)
	}
	return out
}

// TestDRRestoreNamesASealerVariableThatIsNotTheSourceKey is the #550 reproducer,
// the dangerous direction: a valid variable that is not the key the source sealed
// with still made restore say "key custody intact", while nothing sealed opened.
func TestDRRestoreNamesASealerVariableThatIsNotTheSourceKey(t *testing.T) {
	clearSealerKeyEnv(t)
	ctx := context.Background()
	scope := model.TenantID("tenant-550")
	src := t.TempDir()
	// The source supplies the TOTP key from its variable; the other three are files.
	t.Setenv(totpSeedKeyEnv, drValidSealerKey)
	seedDataDir(t, src)
	pf := l3Passphrase(t)
	bundle := filepath.Join(t.TempDir(), "estate.drbundle")
	if out, err := runDR("backup", "--data-dir", src, "--out", bundle, "--passphrase-file", pf); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	srcSecret, err := newSecretSealer(src, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	sealedSecret, err := srcSecret.Seal(ctx, scope, []byte("provider key"))
	if err != nil {
		t.Fatal(err)
	}

	restore := func(t *testing.T) string {
		t.Helper()
		dst := filepath.Join(t.TempDir(), "restored")
		out, err := runDR("restore", "--in", bundle, "--data-dir", dst, "--engine", "sqlite", "--passphrase-file", pf)
		if err != nil {
			t.Fatalf("restore must keep its exit code: %v\n%s", err, out)
		}
		return out
	}
	named := func(t *testing.T, out string, want map[string]bool) {
		t.Helper()
		if strings.Contains(out, "key custody intact") {
			t.Fatalf("restore claimed key custody intact with a key that is not the source's:\n%s", out)
		}
		for _, k := range drSealerKeys {
			if strings.Contains(out, k.name) != want[k.name] {
				t.Fatalf("restore line names %s = %v, want %v:\n%s", k.name, !want[k.name], want[k.name], out)
			}
		}
	}

	t.Run("source variable restored as is", func(t *testing.T) {
		if out := restore(t); !strings.Contains(out, "restore verified: ledger continuity and key custody intact") {
			t.Fatalf("the source's own variable and the carried files are its custody:\n%s", out)
		}
	})
	t.Run("variable over a carried file", func(t *testing.T) {
		t.Setenv(secretStoreKeyEnv, drOtherSealerKey)
		if s, err := newSecretSealer(t.TempDir(), osGetenv); err != nil {
			t.Fatal(err)
		} else if _, err := s.Open(ctx, scope, sealedSecret); err == nil {
			t.Fatal("fixture: the variable must not open what the source sealed")
		}
		out := restore(t)
		named(t, out, map[string]bool{secretStoreKeyFile: true})
		if !strings.Contains(out, secretStoreKeyEnv) {
			t.Fatalf("restore must name the variable that supplies the wrong key:\n%s", out)
		}
	})
	t.Run("variable over the source's variable", func(t *testing.T) {
		t.Setenv(totpSeedKeyEnv, drOtherSealerKey)
		out := restore(t)
		named(t, out, map[string]bool{totpSeedKeyFile: true})
		if !strings.Contains(out, totpSeedKeyEnv) {
			t.Fatalf("restore must name the variable that supplies the wrong key:\n%s", out)
		}
	})
	t.Run("invalid variable", func(t *testing.T) {
		t.Setenv(federationSecretKeyEnv, "not-a-key")
		named(t, restore(t), map[string]bool{federationSecretKeyFile: true})
	})
	t.Run("source variable unset", func(t *testing.T) {
		t.Setenv(totpSeedKeyEnv, "")
		named(t, restore(t), map[string]bool{totpSeedKeyFile: true})
	})
	t.Run("in place over the source", func(t *testing.T) {
		t.Setenv(secretStoreKeyEnv, drOtherSealerKey)
		out, err := runDR("restore", "--in", bundle, "--data-dir", src, "--engine", "sqlite", "--passphrase-file", pf,
			"--in-place", "--operator", "ops@example.com", "--reason", "INC-550")
		if err != nil {
			t.Fatalf("in-place restore must keep its exit code: %v\n%s", err, out)
		}
		named(t, out, map[string]bool{secretStoreKeyFile: true})
	})
}

// TestDRRestoreNamesASealerTheBackupHadNoKeyFor is the review finding on #550: a
// source serve whose secret-store key comes from its variable has no key file, and
// a dr backup run without that variable (cron, a shell) sees no key. Nothing
// vouches for that key then, so restore must name it whatever key it finds, and
// backup says so while it can still be fixed.
func TestDRRestoreNamesASealerTheBackupHadNoKeyFor(t *testing.T) {
	clearSealerKeyEnv(t)
	ctx := context.Background()
	scope := model.TenantID("tenant-550")
	src := t.TempDir()
	seedDataDir(t, src)
	if err := os.Remove(filepath.Join(src, secretStoreKeyFile)); err != nil {
		t.Fatal(err)
	}
	srcSecret, err := newSecretSealer(src, func(env string) string {
		if env == secretStoreKeyEnv {
			return drValidSealerKey
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	sealedSecret, err := srcSecret.Seal(ctx, scope, []byte("provider key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(src, secretStoreKeyFile)); !os.IsNotExist(err) {
		t.Fatalf("fixture: a sealer with its variable must mint no file: %v", err)
	}

	// The backup also runs with an invalid SSO variable: serve cannot seal with
	// it either, so it vouches for nothing.
	t.Setenv(federationSecretKeyEnv, "not-a-key")
	pf := l3Passphrase(t)
	bundle := filepath.Join(t.TempDir(), "estate.drbundle")
	out, err := runDR("backup", "--data-dir", src, "--out", bundle, "--passphrase-file", pf)
	if err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	note := "note: no valid sealer key in effect for " + secretStoreKeyFile + " (" + secretStoreKeyEnv + "), " +
		federationSecretKeyFile + " (" + federationSecretKeyEnv + ")"
	if !strings.Contains(out, note) {
		t.Fatalf("backup must name the sealer keys it cannot vouch for:\n%s", out)
	}
	if n := len(l3Manifest(t, bundle).SealerProbes); n != len(drSealerKeys) {
		t.Fatalf("manifest has %d sealer probes, want one per sealer so it is never taken for an older bundle", n)
	}
	// Under -o json the note is commentary on stderr; stdout stays one document.
	jsonOut, errOut, err := execRoot(t, "dr", "backup", "--data-dir", src, "--out", filepath.Join(t.TempDir(), "json.drbundle"),
		"--passphrase-file", pf, "-o", "json")
	if err != nil {
		t.Fatalf("backup -o json: %v\n%s", err, errOut)
	}
	mustJSONObject(t, "dr backup", jsonOut)
	if strings.Contains(jsonOut, "note:") || !strings.Contains(errOut, note) {
		t.Fatalf("the note belongs on stderr under -o json; stdout = %q, stderr = %q", jsonOut, errOut)
	}
	t.Setenv(federationSecretKeyEnv, "")

	for _, tc := range []struct {
		name, env string
		args      []string
	}{
		{"fresh dir, variable unset", "", nil},
		{"fresh dir, another key", drOtherSealerKey, nil},
		{"in place over the source, variable unset", "", []string{"--in-place", "--operator", "ops@example.com", "--reason", "INC-550"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(secretStoreKeyEnv, tc.env)
			dst := filepath.Join(t.TempDir(), "restored")
			if tc.args != nil {
				dst = src
			}
			out, err := runDR(append([]string{"restore", "--in", bundle, "--data-dir", dst, "--engine", "sqlite",
				"--passphrase-file", pf}, tc.args...)...)
			if err != nil {
				t.Fatalf("restore must keep its exit code: %v\n%s", err, out)
			}
			if s, err := newSecretSealer(dst, osGetenv); err != nil {
				t.Fatal(err)
			} else if _, err := s.Open(ctx, scope, sealedSecret); err == nil {
				t.Fatal("fixture: the restored estate must not open what the source sealed")
			}
			if strings.Contains(out, "key custody intact") {
				t.Fatalf("restore claimed key custody intact for a key the backup had none for:\n%s", out)
			}
			for _, k := range []struct{ name, env string }{
				{secretStoreKeyFile, secretStoreKeyEnv}, {federationSecretKeyFile, federationSecretKeyEnv},
			} {
				if !strings.Contains(out, k.name+" ("+k.env+"): dr backup had no valid key for it") {
					t.Fatalf("restore must name %s:\n%s", k.name, out)
				}
			}
		})
	}
}

// TestRestoreCustodyNoteJudgesEachSealerByItsProbe drives the note directly: a
// sealer with an empty probe (the backup had no key for it) and one the backup
// recorded no probe for are named, even over the source's dir.
func TestRestoreCustodyNoteJudgesEachSealerByItsProbe(t *testing.T) {
	clearSealerKeyEnv(t)
	dir := t.TempDir()
	for _, k := range drSealerKeys {
		writeSealerKey(t, filepath.Join(dir, k.name))
	}
	probes := sealerProbes(dir)
	if len(probes) != len(drSealerKeys) {
		t.Fatalf("sealerProbes = %v", probes)
	}
	m := &dr.Manifest{SealerProbes: probes}
	if got := restoreCustodyNote("restore verified", m, dir, false); got != "restore verified: ledger continuity and key custody intact" {
		t.Fatalf("every key opens its probe, got %q", got)
	}
	m.SealerProbes = []dr.SealerProbe{{Name: secretStoreKeyFile}, probes[1], probes[2]}
	got := restoreCustodyNote("restore verified", m, dir, true)
	for name, want := range map[string]bool{
		secretStoreKeyFile: true, totpSeedKeyFile: false, federationSecretKeyFile: false, eventingSecretKeyFile: true,
	} {
		if strings.Contains(got, name) != want {
			t.Fatalf("note names %s = %v, want %v: %q", name, !want, want, got)
		}
	}

	// An invalid key file is named, whatever its probe.
	m.SealerProbes = probes
	if err := os.WriteFile(filepath.Join(dir, totpSeedKeyFile), []byte("not-a-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := restoreCustodyNote("restore verified", m, dir, true); !strings.Contains(got, totpSeedKeyFile+" ("+totpSeedKeyEnv+"): ") ||
		!strings.Contains(got, "is not a valid key") {
		t.Fatalf("an invalid key file must be named: %q", got)
	}
	// A key file that cannot be read is named with the reason, not as an invalid key.
	if err := os.Remove(filepath.Join(dir, totpSeedKeyFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, totpSeedKeyFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := restoreCustodyNote("restore verified", m, dir, true); !strings.Contains(got, totpSeedKeyFile+" ("+totpSeedKeyEnv+"): ") ||
		!strings.Contains(got, "cannot be read: is a directory") {
		t.Fatalf("an unreadable key file must be named with its reason: %q", got)
	}
	if err := os.Remove(filepath.Join(dir, totpSeedKeyFile)); err != nil {
		t.Fatal(err)
	}

	// An older bundle over the source's own data dir vouches only for the files
	// it kept: not for a missing one, nor for a variable set since.
	writeSealerKey(t, filepath.Join(dir, totpSeedKeyFile))
	if err := os.Remove(filepath.Join(dir, secretStoreKeyFile)); err != nil {
		t.Fatal(err)
	}
	t.Setenv(eventingSecretKeyEnv, drOtherSealerKey)
	got = restoreCustodyNote("restore verified", &dr.Manifest{}, dir, true)
	for name, want := range map[string]bool{
		secretStoreKeyFile: true, totpSeedKeyFile: false, federationSecretKeyFile: false, eventingSecretKeyFile: true,
	} {
		if strings.Contains(got, name) != want {
			t.Fatalf("older bundle: note names %s = %v, want %v: %q", name, !want, want, got)
		}
	}
}

// TestDRSameHostRestoreOfAnOlderBundleKeepsCustody is the safe side of #550: an
// older bundle restored over the source's own data dir keeps that dir's sealer
// keys, which open what the bundle sealed, so custody is intact. The same bundle
// over another installation's data dir is not.
func TestDRSameHostRestoreOfAnOlderBundleKeepsCustody(t *testing.T) {
	clearSealerKeyEnv(t)
	ctx := context.Background()
	scope := model.TenantID("tenant-550")
	pf := l3Passphrase(t)
	for _, tc := range []struct {
		name, line string
		args       []string
	}{
		{"in place", "restore verified and promoted in place: ledger continuity and key custody intact", []string{"--in-place"}},
		{"force", "restore verified: ledger continuity and key custody intact", []string{"--force"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			seedDataDir(t, dir)
			sealed := map[string]string{}
			for name, s := range drSealers(t, dir) {
				v, err := s.Seal(ctx, scope, []byte("sealed by "+name))
				if err != nil {
					t.Fatal(err)
				}
				sealed[name] = v
			}
			bundle := filepath.Join(t.TempDir(), "estate.drbundle")
			if out, err := runDR("backup", "--data-dir", dir, "--out", bundle, "--passphrase-file", pf); err != nil {
				t.Fatalf("backup: %v\n%s", err, out)
			}
			bundle = drLegacyBundle(t, bundle, pf)
			args := append([]string{"restore", "--in", bundle, "--engine", "sqlite", "--passphrase-file", pf,
				"--operator", "ops@example.com", "--reason", "INC-550"}, tc.args...)

			out, err := runDR(append(args, "--data-dir", dir)...)
			if err != nil {
				t.Fatalf("same-host restore: %v\n%s", err, out)
			}
			if !strings.Contains(out, tc.line) {
				t.Fatalf("an older bundle over its own data dir keeps the source's sealer keys:\n%s", out)
			}
			for name, s := range drSealers(t, dir) {
				if got, err := s.Open(ctx, scope, sealed[name]); err != nil || string(got) != "sealed by "+name {
					t.Fatalf("%s: the kept key does not open what the source sealed: %q, %v", name, got, err)
				}
			}

			other := t.TempDir()
			seedDataDir(t, other)
			out, err = runDR(append(args, "--data-dir", other)...)
			if err != nil {
				t.Fatalf("restore over another installation: %v\n%s", err, out)
			}
			if strings.Contains(out, "key custody intact") {
				t.Fatalf("another installation's sealer keys were taken for the source's:\n%s", out)
			}
			for _, k := range drSealerKeys {
				if !strings.Contains(out, k.name) {
					t.Fatalf("restore must name %s:\n%s", k.name, out)
				}
			}
		})
	}
}

// TestDRBackupLeavesOutASealerFileItsVariableSupplies: the engine reads the
// variable and ignores the file then, so the file is stale. Carrying it would let
// a restore without the variable claim a custody it does not have.
func TestDRBackupLeavesOutASealerFileItsVariableSupplies(t *testing.T) {
	clearSealerKeyEnv(t)
	src := t.TempDir()
	seedDataDir(t, src)
	t.Setenv(secretStoreKeyEnv, drValidSealerKey)
	pf := l3Passphrase(t)
	bundle := filepath.Join(t.TempDir(), "env.drbundle")
	if out, err := runDR("backup", "--data-dir", src, "--out", bundle, "--passphrase-file", pf); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	carried := map[string]bool{}
	for _, kr := range l3Manifest(t, bundle).Keys {
		carried[kr.Name] = true
	}
	for _, k := range drSealerKeys {
		if want := k.name != secretStoreKeyFile; carried[k.name] != want {
			t.Fatalf("bundle carries %s = %v, want %v: %v", k.name, carried[k.name], want, carried)
		}
	}
	// Carried or not, every key in effect leaves a probe, and no probe is the key.
	probes := map[string]string{}
	for _, p := range l3Manifest(t, bundle).SealerProbes {
		probes[p.Name] = p.Probe
	}
	for _, k := range drSealerKeys {
		if probes[k.name] == "" || strings.Contains(probes[k.name], drValidSealerKey) {
			t.Fatalf("manifest probe for %s = %q", k.name, probes[k.name])
		}
	}
}

// TestDRInPlaceRestoreBringsBackTheSourceSealerKeys: an in-place restore over a
// node whose sealer keys were replaced puts the bundle's keys back, keeps the
// replaced ones as *.pre-restore-*, and the source's sealed values open again.
func TestDRInPlaceRestoreBringsBackTheSourceSealerKeys(t *testing.T) {
	clearSealerKeyEnv(t)
	ctx := context.Background()
	scope := model.TenantID("tenant-493")
	dir := t.TempDir()
	seedDataDir(t, dir)
	sealed := map[string]string{}
	for name, s := range drSealers(t, dir) {
		v, err := s.Seal(ctx, scope, []byte("sealed by "+name))
		if err != nil {
			t.Fatal(err)
		}
		sealed[name] = v
	}
	pf := l3Passphrase(t)
	bundle := filepath.Join(t.TempDir(), "estate.drbundle")
	if out, err := runDR("backup", "--data-dir", dir, "--out", bundle, "--passphrase-file", pf); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	// The node loses its sealer keys; the next boot mints different ones.
	for _, k := range drSealerKeys {
		if err := os.Remove(filepath.Join(dir, k.name)); err != nil {
			t.Fatal(err)
		}
	}
	drSealers(t, dir)

	out, err := runDR("restore", "--in", bundle, "--data-dir", dir, "--engine", "sqlite", "--passphrase-file", pf,
		"--in-place", "--operator", "ops@example.com", "--reason", "INC-493")
	if err != nil {
		t.Fatalf("in-place restore: %v\n%s", err, out)
	}
	if !strings.Contains(out, "restore verified and promoted in place: ledger continuity and key custody intact") {
		t.Fatalf("in-place restore output:\n%s", out)
	}
	for name, s := range drSealers(t, dir) {
		got, err := s.Open(ctx, scope, sealed[name])
		if err != nil || string(got) != "sealed by "+name {
			t.Fatalf("%s: in-place restore did not bring the source key back: %q, %v", name, got, err)
		}
		if m, _ := filepath.Glob(filepath.Join(dir, name+".pre-restore-*")); len(m) != 1 {
			t.Fatalf("%s: the replaced key was not preserved: %v", name, m)
		}
	}
}

// TestDRBackupRefusesADataDirWithoutSigningKeys: keyrings and sealer keys are not
// signing keys, so without external custody a data dir holding only those is the
// wrong data dir and backup refuses, naming the remedy.
func TestDRBackupRefusesADataDirWithoutSigningKeys(t *testing.T) {
	clearSealerKeyEnv(t)
	src := t.TempDir()
	seedDataDir(t, src)
	l3MoveSigningKeysOutOfTheDataDir(t, src, t.TempDir())
	for _, k := range drSealerKeys {
		writeSealerKey(t, filepath.Join(src, k.name))
	}
	pf := l3Passphrase(t)
	out, err := runDR("backup", "--data-dir", src, "--out", filepath.Join(t.TempDir(), "x.drbundle"), "--passphrase-file", pf)
	if err == nil || !strings.Contains(err.Error(), "no installation signing keys found") {
		t.Fatalf("backup must refuse a data dir without signing keys or external custody; err=%v\n%s", err, out)
	}
}

func writeSealerKey(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(drValidSealerKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
