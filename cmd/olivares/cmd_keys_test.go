// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/secure"
)

// runKeys executes one `keys …` invocation against a fresh command tree.
func runKeys(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newKeysCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// TestKeysCeremony drives the full operator ceremony against the fake KEK:
// wrap --mint → status → rotate (history grows) → rewrap → seal/unseal.
func TestKeysCeremony(t *testing.T) {
	startFakeKEKServer(t)
	dir := t.TempDir()
	envPath := filepath.Join(dir, "audit-signing.key.sealed")

	// Mint + seal.
	out, err := runKeys(t, "wrap", "--mint", "--out", envPath)
	if err != nil {
		t.Fatalf("keys wrap --mint: %v\n%s", err, out)
	}
	if !strings.Contains(out, "public key:") {
		t.Fatalf("wrap output missing the public key: %s", out)
	}
	e1, err := secure.ReadSealedFile(envPath)
	if err != nil || e1.Purpose != secure.PurposeAuditSigningKey {
		t.Fatalf("envelope after wrap: %+v err=%v", e1, err)
	}

	// Status reads the envelope without any KMS call. The recorded KEK is the
	// RESOLVED ARN (not the configured alias), so the operator can always see
	// which key actually wraps the DEK.
	t.Setenv(envAuditWrapped, envPath)
	out, err = runKeys(t, "status")
	if err != nil || !strings.Contains(out, `"prior_public_keys": []`) || !strings.Contains(out, "key/test-resolved") {
		t.Fatalf("keys status: %v\n%s", err, out)
	}

	// Rotate: new key, old public key becomes history.
	out, err = runKeys(t, "rotate", "--in", envPath, "--yes")
	if err != nil {
		t.Fatalf("keys rotate: %v\n%s", err, out)
	}
	e2, err := secure.ReadSealedFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(e2.PriorPublicKeys) != 1 || !bytes.Equal(e2.PriorPublicKeys[0], e1.PublicKey) {
		t.Fatalf("rotation did not preserve history: %+v", e2.PriorPublicKeys)
	}
	if bytes.Equal(e2.PublicKey, e1.PublicKey) {
		t.Fatal("rotation did not mint a new key")
	}

	// Rewrap: same key, fresh wrap (still opens — the loader proves it).
	if out, err = runKeys(t, "rewrap", "--in", envPath, "--yes"); err != nil {
		t.Fatalf("keys rewrap: %v\n%s", err, out)
	}
	got, gerr := loadAuditSigningKey(dir, discardLog())
	if gerr != nil || got.mode != custodyModeCMEK || len(got.priors) != 1 {
		t.Fatalf("boot load after rotate+rewrap = (%q, priors=%d, %v)", got.mode, len(got.priors), gerr)
	}

	// Migrate an existing plaintext key (the data-dir form) into an envelope.
	plainDir := t.TempDir()
	plainKey, _, err := secure.LoadOrCreateSigningKey(filepath.Join(plainDir, "audit-signing.key"))
	if err != nil {
		t.Fatal(err)
	}
	migrated := filepath.Join(plainDir, "migrated.sealed")
	if out, err = runKeys(t, "wrap", "--from", filepath.Join(plainDir, "audit-signing.key"), "--out", migrated); err != nil {
		t.Fatalf("keys wrap --from: %v\n%s", err, out)
	}
	me, err := secure.ReadSealedFile(migrated)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(me.PublicKey, plainKey.Public().(ed25519.PublicKey)) {
		t.Fatal("migrated envelope does not record the original key's public half")
	}

	// Seal/unseal an operator config.
	cfgPath := filepath.Join(dir, "notify.json")
	if err := os.WriteFile(cfgPath, []byte(`{"webhook":"https://h/x","secret":"s3cr3t"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sealedCfg := filepath.Join(dir, "notify.sealed.json")
	if out, err = runKeys(t, "seal", "--in", cfgPath, "--out", sealedCfg); err != nil {
		t.Fatalf("keys seal: %v\n%s", err, out)
	}
	if out, err = runKeys(t, "unseal", "--in", sealedCfg); err != nil || !strings.Contains(out, "s3cr3t") {
		t.Fatalf("keys unseal: %v\n%s", err, out)
	}
	// Sealing an already-sealed file is refused.
	if _, err = runKeys(t, "seal", "--in", sealedCfg, "--out", sealedCfg+"2"); err == nil {
		t.Fatal("keys seal accepted an already-sealed input")
	}
}

// TestVersionReportsFIPSMode pins the contract Dockerfile.fips relies on: the
// `version` output self-reports the FIPS 140-3 mode and linked module version
// (off in a normal test build; the FIPS image asserts the same line shows
// fips140=on module=v1.0.0).
func TestVersionReportsFIPSMode(t *testing.T) {
	cmd := newVersionCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "fips140=") || !strings.Contains(out.String(), "module=") ||
		!strings.Contains(out.String(), "license-key=") || !strings.Contains(out.String(), "ota-key=") {
		t.Fatalf("version output does not report FIPS mode and both trust anchors: %s", out.String())
	}
}

// OVERWRITING THE ONLY SEALED ENVELOPE IS THE DESTRUCTIVE ACT, AND UNTIL 2026-08-21 NEITHER
// `rotate` NOR `rewrap` ASKED.
//
// Both take `--out`, and both document the default as "overwrite --in atomically", so the
// in-place path was a choice rather than an oversight — just one with no guard. What it
// destroys is the ONLY copy of the previous sealed key, and the risk is written down in this
// repository already: the key-custody contract says that without the KEK version
// pinned a rewrap "se brickearía" — an envelope sealed under a KEK nobody can open, with
// nothing left to go back to.
//
// The confirmation runs BEFORE the KMS call, not before the write: declining after minting
// would leave orphan key material in the KMS for an answer the operator was always going to
// give. With `--out` nothing is asked, because nothing is destroyed — that is what keeps the
// safe path cheap.
//
// This witness asserts the WIRING (which paths ask and which do not). confirmDestructive's own
// behavior — terminal vs pipe, y/yes parsing — is already covered by confirm_test.go, and a
// second copy of it here would age separately.
func TestKeysInPlaceOverwriteNeedsConsentAndRefusesWithoutWriting(t *testing.T) {
	for _, verb := range []string{"rotate", "rewrap"} {
		t.Run(verb, func(t *testing.T) {
			startFakeKEKServer(t)
			dir := t.TempDir()
			envPath := filepath.Join(dir, "audit-signing.key.sealed")
			if out, err := runKeys(t, "wrap", "--mint", "--out", envPath); err != nil {
				t.Fatalf("wrap --mint: %v\n%s", err, out)
			}
			before, err := os.ReadFile(envPath)
			if err != nil {
				t.Fatalf("reading the freshly minted envelope: %v", err)
			}

			// REFUSING DIRECTION. A test session is not a terminal, so there is nobody to
			// ask and the verb must refuse rather than proceed on an unanswered prompt.
			out, err := runKeys(t, verb, "--in", envPath)
			if err == nil {
				t.Fatalf("`keys %s --in <env>` overwrote in place with no confirmation:\n%s", verb, out)
			}
			if !strings.Contains(err.Error(), "--yes") {
				t.Fatalf("the refusal does not tell the operator how to mean it: %v", err)
			}

			// AND IT MUST NOT HAVE WRITTEN. A guard that refuses AFTER mutating is not a
			// guard, and this is the half that a "returns an error" assertion misses.
			after, rerr := os.ReadFile(envPath)
			if rerr != nil {
				t.Fatalf("the envelope is unreadable after a REFUSED %s: %v", verb, rerr)
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("the refused %s still rewrote the envelope (%d bytes -> %d)", verb, len(before), len(after))
			}

			// NEGATIVE CONTROL 1 — `--out` ASKS NOTHING. Without this case, "always confirm"
			// would satisfy the assertion above while making the SAFE path require a flag,
			// which is the wrong direction: it would push operators toward in-place.
			beside := filepath.Join(dir, "beside.sealed")
			if out, oerr := runKeys(t, verb, "--in", envPath, "--out", beside); oerr != nil {
				t.Fatalf("`keys %s --out` must not need confirmation: %v\n%s", verb, oerr, out)
			}
			if kept, kerr := os.ReadFile(envPath); kerr != nil || !bytes.Equal(before, kept) {
				t.Fatalf("`keys %s --out` modified --in, which is the whole point of --out", verb)
			}
			if _, serr := os.Stat(beside); serr != nil {
				t.Fatalf("`keys %s --out` did not write the new envelope: %v", verb, serr)
			}

			// NEGATIVE CONTROL 2 — `--yes` PROCEEDS AND ACTUALLY WRITES. Without it, a verb
			// that refused unconditionally would pass everything above: a guard that only
			// knows how to say no cannot tell "correct" from "does nothing".
			if out, yerr := runKeys(t, verb, "--in", envPath, "--yes"); yerr != nil {
				t.Fatalf("`keys %s --in --yes` was refused: %v\n%s", verb, yerr, out)
			}
			done, derr := os.ReadFile(envPath)
			if derr != nil {
				t.Fatalf("reading the envelope after a confirmed %s: %v", verb, derr)
			}
			if bytes.Equal(before, done) {
				t.Fatalf("`keys %s --in --yes` returned success without changing the envelope", verb)
			}
		})
	}
}
