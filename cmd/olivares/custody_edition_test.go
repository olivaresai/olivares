// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// The custody assertions (OLIVARES_KEY_CUSTODY=byok, OLIVARES_LEDGER_CUSTODY=hyok), the
// off-box ledger checkpoint signer (OLIVARES_LEDGER_SIGNER) and `audit key-transition`
// are Business capabilities. An operator-provided audit key and `keys status` stay
// Community. Each test pins the edition it speaks for with asEdition, so it holds in the
// Community build and in the Business overlay build alike.

const (
	custodyMarkerRegion = "marker-region-7f3a"
	custodyMarkerSigner = "marker-signer-kind-91c2"
)

// operatorAuditKey is a key the operator provides (the source), as base64.
func operatorAuditKey(t *testing.T) string {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(private)
}

// bootCustody boots the real engine as the named edition with exactly this custody
// environment.
func bootCustody(t *testing.T, edition string, serve bool, dir string, env map[string]string) (*engine, error) {
	t.Helper()
	asEdition(t, edition)
	clearOlivaresEnv(t)
	for key, value := range env {
		t.Setenv(key, value)
	}
	prepareCompositionTestBoot(t)
	eng, err := boot(context.Background(), bootConfig{DataDir: dir, Engine: "sqlite", Version: "test", ServeMode: serve, Logger: discardLog(), NoIngest: true})
	if err == nil {
		t.Cleanup(func() { _ = eng.Close() })
	}
	return eng, err
}

func TestCommunityServeRefusesBusinessCustody(t *testing.T) {
	key := operatorAuditKey(t)
	for _, tc := range []struct {
		name    string
		env     map[string]string
		mention string
	}{
		{"byok assertion", map[string]string{envAuditKey: key, envKeyCustody: "byok"}, envKeyCustody},
		{"padded byok assertion", map[string]string{envAuditKey: key, envKeyCustody: "  byok  "}, envKeyCustody},
		{"padded hyok assertion", map[string]string{envLedgerCustody: " hyok "}, envLedgerCustody},
		{"padded signer", map[string]string{envLedgerSigner: " aws-kms "}, envLedgerSigner},
		{"hyok with the on-box signer", map[string]string{envLedgerCustody: "hyok", envLedgerSigner: "on-box"}, envLedgerCustody},
		{"hyok assertion", map[string]string{envLedgerCustody: "hyok"}, envLedgerCustody},
		{"aws-kms signer", map[string]string{envLedgerSigner: "aws-kms", "OLIVARES_LEDGER_KMS_AWS_REGION": custodyMarkerRegion}, envLedgerSigner},
		{"gcp-kms signer", map[string]string{envLedgerSigner: "gcp-kms"}, envLedgerSigner},
		{"azure-kv signer", map[string]string{envLedgerSigner: "azure-kv"}, envLedgerSigner},
		{"unsupported signer", map[string]string{envLedgerSigner: custodyMarkerSigner}, envLedgerSigner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			eng, err := bootCustody(t, "community", true, dir, tc.env)
			if err == nil {
				t.Fatal("Community served with a Business custody setting")
			}
			if eng != nil {
				t.Fatal("a refused boot returned an engine")
			}
			if code := exitcode.From(err); code != exitcode.Edition {
				t.Fatalf("exit code = %d, want %d (Edition): %v", code, exitcode.Edition, err)
			}
			msg := err.Error()
			for _, want := range []string{tc.mention, "Business", pricingURL} {
				if !strings.Contains(msg, want) {
					t.Errorf("refusal %q does not name %q", msg, want)
				}
			}
			for _, leak := range []string{key, custodyMarkerRegion, custodyMarkerSigner} {
				if strings.Contains(msg, leak) {
					t.Errorf("refusal %q carries the operator value %q", msg, leak)
				}
			}
			// Refused before anything is opened: no store, key or lock was created.
			if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
				t.Errorf("the refused boot touched the data directory: %v (err %v)", entries, err)
			}
		})
	}
}

// Offline commands that boot an engine (dr, audit) are refused too: like a CMEK
// installation, the setting is unset or the Business build runs, and the data stays.
func TestCommunityOfflineBootRefusesBusinessCustody(t *testing.T) {
	dir := t.TempDir()
	_, err := bootCustody(t, "community", false, dir, map[string]string{envLedgerCustody: "hyok"})
	if err == nil || exitcode.From(err) != exitcode.Edition {
		t.Fatalf("a non-serving Community boot = %v (exit %d), want an Edition refusal", err, exitcode.From(err))
	}
	if entries, rerr := os.ReadDir(dir); rerr != nil || len(entries) != 0 {
		t.Errorf("the refused boot touched the data directory: %v (err %v)", entries, rerr)
	}
}

// The default stays open: no assertion, the on-box signer spellings, and an operator
// key without an assertion are all Community.
func TestCommunityServeStartsWithoutBusinessCustody(t *testing.T) {
	key := operatorAuditKey(t)
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"unset", nil},
		{"blank values", map[string]string{envKeyCustody: "  ", envLedgerCustody: "  ", envLedgerSigner: "  "}},
		{"on-box signer", map[string]string{envLedgerSigner: "on-box"}},
		{"ed25519 signer", map[string]string{envLedgerSigner: "ed25519"}},
		{"operator key without an assertion", map[string]string{envAuditKey: key}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := bootCustody(t, "community", true, t.TempDir(), tc.env); err != nil {
				t.Fatalf("Community refused a configuration with no Business custody: %v", err)
			}
		})
	}
}

// Read-only verification (audit verify, tree, dr) builds the off-box signer through
// buildCheckpointKey, so a ledger signed off-box stays checkable in Community. The
// refusal lives in boot, which is where checkpoints are signed, not in this builder.
func TestCommunityVerificationStillBuildsOffBoxSigner(t *testing.T) {
	asEdition(t, "community")
	clearOlivaresEnv(t)
	t.Setenv(envLedgerSigner, "aws-kms")
	t.Setenv("OLIVARES_LEDGER_KMS_AWS_REGION", "eu-west-1")
	t.Setenv("OLIVARES_LEDGER_KMS_AWS_KEY_ID", "alias/test")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret-example")
	ck, err := buildCheckpointKey(discardLog())
	if err != nil || ck == nil {
		t.Fatalf("Community verification lost the off-box signer: %v, %v", ck, err)
	}
}

// Business keeps the capability as it was: the same configuration is not refused.
func TestBusinessKeepsCustodyAndOffBoxSigner(t *testing.T) {
	key := operatorAuditKey(t)
	t.Run("byok serve", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := bootCustody(t, "enterprise", true, dir, map[string]string{envAuditKey: key, envKeyCustody: "byok"}); err != nil {
			t.Fatalf("the Business edition refused BYOK custody: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "audit-signing.key")); !os.IsNotExist(err) {
			t.Fatalf("BYOK boot minted a replacement audit key: %v", err)
		}
	})
	t.Run("off-box signer is built", func(t *testing.T) {
		asEdition(t, "enterprise")
		clearOlivaresEnv(t)
		t.Setenv(envLedgerSigner, "aws-kms")
		t.Setenv("OLIVARES_LEDGER_KMS_AWS_REGION", "eu-west-1")
		t.Setenv("OLIVARES_LEDGER_KMS_AWS_KEY_ID", "alias/test")
		t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "secret-example")
		ck, err := buildCheckpointKey(discardLog())
		if err != nil || ck == nil {
			t.Fatalf("the Business edition did not build its off-box signer: %v, %v", ck, err)
		}
	})
}

// `audit key-transition` answers the one Business sentence in Community, and refuses
// before a data directory is opened or created.
func TestCommunityKeyTransitionAnswersBusiness(t *testing.T) {
	asEdition(t, "community")
	clearOlivaresEnv(t)
	absent := filepath.Join(t.TempDir(), "must-stay-absent")
	want := "Key transition is a Business feature: " + pricingURL
	_, _, err := execRootOwnProcess(t, "audit", "key-transition", "--yes", "--data-dir", absent)
	if err == nil || err.Error() != want || exitcode.From(err) != exitcode.Edition {
		t.Fatalf("key-transition = %v (exit %d), want %q with exit %d", err, exitcode.From(err), want, exitcode.Edition)
	}
	if _, statErr := os.Stat(absent); !os.IsNotExist(statErr) {
		t.Fatalf("key-transition touched %s: %v", absent, statErr)
	}
}

// Cobra rejects a malformed argv before the edition answer, as for every paid verb.
func TestCommunityKeyTransitionArgvIsValidatedFirst(t *testing.T) {
	asEdition(t, "community")
	_, _, err := execRoot(t, "audit", "key-transition", "unexpected")
	if err == nil || strings.Contains(err.Error(), "Business") {
		t.Fatalf("a malformed argv was answered as an edition problem: %v", err)
	}
}

// `keys status` stays Community: an operator reads the custody of an installation to
// recover it (TestCommunityCMEKStatusKeepsUnverifiedMetadata). It is read-only, so it
// reports a declared posture even though boot refuses it.
func TestCommunityKeysStatusReportsDeclaredCustody(t *testing.T) {
	asEdition(t, "community")
	clearOlivaresEnv(t)
	t.Setenv(envKeyCustody, "byok")
	t.Setenv(envLedgerCustody, "hyok")
	out, _, err := execRootOwnProcess(t, "keys", "status")
	if err != nil || !strings.Contains(out, "byok") {
		t.Fatalf("Community lost keys status: %v\n%s", err, out)
	}
}

// `audit recover` boots the engine to append a marker signed by the off-box signer. In
// Community the boot refusal keeps its Edition class there too: exit code 9, not the
// generic refusal, and nothing is created.
func TestCommunityAuditRecoverKeepsEditionExitCode(t *testing.T) {
	asEdition(t, "community")
	clearOlivaresEnv(t)
	t.Setenv(envLedgerCustody, "hyok")
	dir := t.TempDir()
	cmd := auditRecoverCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"--tenant", "11111111-1111-1111-1111-111111111111", "--data-dir", dir, "--pubkey", "ecdsa-p256-sha256:AAAA"})
	err := cmd.Execute()
	if err == nil || exitcode.From(err) != exitcode.Edition || !strings.Contains(err.Error(), "Business") {
		t.Fatalf("audit recover = %v (exit %d), want an Edition refusal naming Business", err, exitcode.From(err))
	}
	if entries, rerr := os.ReadDir(dir); rerr != nil || len(entries) != 0 {
		t.Errorf("the refused recover touched the data directory: %v (err %v)", entries, rerr)
	}
}

// Verification is refused before opening an off-box ledger in Community.
func TestCommunityAuditVerifyRefusedBeforeOffBoxSigner(t *testing.T) {
	asEdition(t, "community")
	clearOlivaresEnv(t)
	t.Setenv(envLedgerSigner, "aws-kms")
	_, _, err := execRootOwnProcess(t, "audit", "verify", "--data-dir", filepath.Join(t.TempDir(), "absent"))
	if err == nil || exitcode.From(err) != exitcode.Edition {
		t.Fatalf("verify = %v, want edition refusal", err)
	}
}
