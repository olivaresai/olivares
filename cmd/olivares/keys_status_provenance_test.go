// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/secure"
)

// The subject of these tests is PROVENANCE, not confidentiality: what `keys
// status` prints about an envelope's custody history, and whether a reader can
// tell proven-unedited fields from fields that were merely parsed out of a file.
//
// It matters because prior_public_keys is not decoration. It is the rotation
// history the default verifier folds into its candidate set, and the value an
// external auditor pins per generation with `audit verify --event-pubkey`. The
// envelope format defends it: PublicKey and PriorPublicKeys are bound into the
// GCM AAD (core/secure/envelope.go:127-134), so an attacker who can write the
// file but does not hold the KEK cannot edit the history and still have Open
// succeed. That defense only pays out on a path that OPENS. `keys status` did
// not open, so it re-published attacker-writable JSON under the banner of
// custody metadata — the hardened verification mode fed by unauthenticated data.

// statusReport runs `keys status` and decodes its JSON report.
func statusReport(t *testing.T, args ...string) (map[string]any, string, error) {
	t.Helper()
	out, err := runKeys(t, append([]string{"status"}, args...)...)
	var got map[string]any
	// The report is printed even when verification fails; decoding it is how the
	// test proves WHAT was said, not merely that something failed. A Decoder (not
	// Unmarshal) because cobra appends its "Error: …" line after the report, and
	// Unmarshal rejects the whole buffer over that trailing text.
	if idx := strings.Index(out, "{"); idx >= 0 {
		if derr := json.NewDecoder(strings.NewReader(out[idx:])).Decode(&got); derr != nil {
			got = nil
		}
	}
	return got, out, err
}

// envelopeField digs one envelope's field out of a decoded status report.
func envelopeField(t *testing.T, report map[string]any, envelope, field string) any {
	t.Helper()
	envs, ok := report["envelopes"].(map[string]any)
	if !ok {
		t.Fatalf("status report has no envelopes object: %v", report)
	}
	e, ok := envs[envelope].(map[string]any)
	if !ok {
		t.Fatalf("status report has no %q envelope: %v", envelope, envs)
	}
	return e[field]
}

// forgeAPriorKey rewrites the envelope FILE to append an attacker-chosen public
// key to the rotation history. It is the exact capability the AAD binding exists
// to defeat: file write, no KEK. The bytes are edited as JSON rather than
// re-sealed, because re-sealing would need the KEK the attacker does not have.
func forgeAPriorKey(t *testing.T, path string) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path) //nolint:gosec // test fixture path
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	priors, _ := raw["prior_public_keys"].([]any)
	raw["prior_public_keys"] = append(priors, base64.StdEncoding.EncodeToString(pub))
	edited, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(edited, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return pub
}

// sealedAuditEnvelope mints an audit envelope and rotates it once, so the
// history it carries is a REAL prior generation rather than an empty list — a
// forged entry has to be distinguishable from a genuine one, not just from none.
func sealedAuditEnvelope(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit-signing.key.sealed")
	if out, err := runKeys(t, "wrap", "--mint", "--out", path); err != nil {
		t.Fatalf("keys wrap --mint: %v\n%s", err, out)
	}
	if out, err := runKeys(t, "rotate", "--in", path, "--yes"); err != nil {
		t.Fatalf("keys rotate: %v\n%s", err, out)
	}
	return path
}

// TestKeysStatusMarksUnauthenticatedProvenance is the regression for the finding
// itself: the default report must not present parsed-from-disk custody history as
// though it were proven.
func TestKeysStatusMarksUnauthenticatedProvenance(t *testing.T) {
	startFakeKEKServer(t)
	path := sealedAuditEnvelope(t)
	t.Setenv(envAuditWrapped, path)

	report, out, err := statusReport(t)
	if err != nil {
		t.Fatalf("keys status: %v\n%s", err, out)
	}
	if got := envelopeField(t, report, "audit", "authenticated"); got != false {
		t.Fatalf("default `keys status` reported authenticated=%v; it makes no KMS call, so it "+
			"cannot have proven anything and must not imply it did", got)
	}
	note, _ := envelopeField(t, report, "audit", "provenance").(string)
	if !strings.Contains(note, "--verify-envelopes") {
		t.Fatalf("the unauthenticated report does not name the authenticated alternative, so a "+
			"reader is told the data is unproven and not how to prove it: %q", note)
	}
}

// TestKeysStatusVerifiedProvenanceIsProven is the other half: when the operator
// asks, the report must actually PROVE the fields, not relabel them.
func TestKeysStatusVerifiedProvenanceIsProven(t *testing.T) {
	startFakeKEKServer(t)
	path := sealedAuditEnvelope(t)
	t.Setenv(envAuditWrapped, path)

	report, out, err := statusReport(t, "--verify-envelopes")
	if err != nil {
		t.Fatalf("keys status --verify-envelopes on an INTACT envelope: %v\n%s", err, out)
	}
	if got := envelopeField(t, report, "audit", "authenticated"); got != true {
		t.Fatalf("an intact envelope opened under the configured KEK reported authenticated=%v\n%s", got, out)
	}
}

// TestKeysStatusRefusesForgedRotationHistory is the attack this exists to stop:
// an attacker with file write and no KEK appends a key of their own, and an
// auditor pins it because `keys status` presented it as custody history.
func TestKeysStatusRefusesForgedRotationHistory(t *testing.T) {
	startFakeKEKServer(t)
	path := sealedAuditEnvelope(t)
	t.Setenv(envAuditWrapped, path)
	forged := forgeAPriorKey(t, path)
	forgedB64 := base64.StdEncoding.EncodeToString(forged)

	// The default report still SHOWS the forged key — it parses the file and says
	// so. What it must never do is call it authenticated.
	report, out, err := statusReport(t)
	if err != nil {
		t.Fatalf("keys status: %v\n%s", err, out)
	}
	if got := envelopeField(t, report, "audit", "authenticated"); got != false {
		t.Fatalf("a forged history was reported authenticated=%v\n%s", got, out)
	}

	// Verification must FAIL, and the failure must reach the exit code: an
	// auditor who asked for proof and got a zero exit would pin the forged key.
	report, out, err = statusReport(t, "--verify-envelopes")
	if err == nil {
		t.Fatalf("`keys status --verify-envelopes` accepted an envelope whose rotation history "+
			"was edited without the KEK, and exited 0 — the forged pin %s would be trusted\n%s",
			forgedB64[:16], out)
	}
	if got := envelopeField(t, report, "audit", "authenticated"); got != false {
		t.Fatalf("forged envelope reported authenticated=%v\n%s", got, out)
	}
	if !strings.Contains(out, forgedB64) {
		t.Fatalf("the failing report hid the forged key; an operator needs to SEE what was "+
			"planted in order to know which pin to distrust\n%s", out)
	}
	// Showing the fields is only safe BECAUSE they are labeled as disputed. Printing
	// a forged rotation history under the same wording as a verified one would be the
	// original defect wearing the new flag's clothes, so the warning is asserted here
	// rather than left to the exit code — a JSON report gets read long after the exit
	// code is gone.
	note, _ := envelopeField(t, report, "audit", "provenance").(string)
	if !strings.Contains(note, "REFUSED") {
		t.Fatalf("a refused envelope is not labeled REFUSED, so its fields read exactly like a "+
			"verified report's: %q", note)
	}
	if !strings.Contains(note, "Do not") || !strings.Contains(note, "in question") {
		t.Fatalf("the refusal prints the envelope's own claims without warning that those claims "+
			"are the thing in dispute: %q", note)
	}
}

// TestKeysStatusVerifiedProvenanceRequiresAnOpen closes the gap between the two
// labels. "authenticated" and "provenance" are separate fields, so one can drift
// into claiming proof the other never obtained — and the JSON report outlives the
// exit code that would otherwise have contradicted it.
func TestKeysStatusVerifiedProvenanceRequiresAnOpen(t *testing.T) {
	startFakeKEKServer(t)
	path := sealedAuditEnvelope(t)
	t.Setenv(envAuditWrapped, path)

	report, out, err := statusReport(t)
	if err != nil {
		t.Fatalf("keys status: %v\n%s", err, out)
	}
	note, _ := envelopeField(t, report, "audit", "provenance").(string)
	// No KMS call was made, so no wording that asserts proof may appear.
	for _, claim := range []string{"PROVEN", "safe to pin"} {
		if strings.Contains(note, claim) {
			t.Fatalf("the default report claims %q while making no KMS call — the two provenance "+
				"labels have drifted apart and the unproven one is wearing the proven one's "+
				"words: %q", claim, note)
		}
	}
	if !strings.Contains(note, "NOT proven") {
		t.Fatalf("the default report does not say plainly that it is unproven: %q", note)
	}
}

// TestKeysStatusVerifyRefusesAPurposeSwap covers the second thing opening buys
// that parsing cannot: the slot a file sits in is checked against the purpose it
// was sealed for. A catalog envelope dropped at the audit path is a custody
// substitution, and the unopened report repeated the file's own claim about
// itself.
func TestKeysStatusVerifyRefusesAPurposeSwap(t *testing.T) {
	startFakeKEKServer(t)
	catalog := filepath.Join(t.TempDir(), "catalog-signing.key.sealed")
	if out, err := runKeys(t, "wrap", "--mint", "--purpose", "catalog", "--out", catalog); err != nil {
		t.Fatalf("keys wrap --mint --purpose catalog: %v\n%s", err, out)
	}
	// Planted in the AUDIT slot.
	t.Setenv(envAuditWrapped, catalog)

	report, out, err := statusReport(t, "--verify-envelopes")
	if err == nil {
		t.Fatalf("a catalog-key envelope in the audit slot verified clean — the purpose binding "+
			"exists precisely so this cannot pass\n%s", out)
	}
	// The control that keeps this test honest: a refusal is only evidence if it
	// came from the purpose check. Before --verify-envelopes existed, this test
	// went green on "unknown flag" — a failure the command reached without ever
	// looking at the envelope.
	if report == nil {
		t.Fatalf("no status report was rendered, so the refusal did not come from verifying "+
			"anything: %v\n%s", err, out)
	}
	if !strings.Contains(err.Error(), "expected") {
		t.Fatalf("the refusal does not name the purpose mismatch, so it may be refusing for an "+
			"unrelated reason: %v", err)
	}
}

// TestKeysStatusDefaultMakesNoKMSCall pins the contract row that says `keys
// status` needs no KMS ("Sin llamadas KMS (solo metadatos)",
// the key-custody contract). It is load-bearing, not trivia:
// `keys status` is the command an operator runs to DIAGNOSE a revoked KEK, so it
// must keep working when the KEK refuses every call.
func TestKeysStatusDefaultMakesNoKMSCall(t *testing.T) {
	kek := startFakeKEKServer(t)
	path := sealedAuditEnvelope(t)
	t.Setenv(envAuditWrapped, path)

	kek.revoked = true // every KMS call from here on fails
	report, out, err := statusReport(t)
	if err != nil {
		t.Fatalf("default `keys status` failed with the KEK revoked — the posture command must "+
			"survive the incident it exists to diagnose: %v\n%s", err, out)
	}
	if got := envelopeField(t, report, "audit", "public_key"); got == nil || got == "" {
		t.Fatalf("default `keys status` reported no public key with the KEK revoked\n%s", out)
	}
	// And the opposite, so the test cannot pass by the command doing nothing:
	// asking for verification against a revoked KEK must fail loudly.
	report, out, err = statusReport(t, "--verify-envelopes")
	if err == nil {
		t.Fatalf("--verify-envelopes reported success while the KEK refused every call\n%s", out)
	}
	if report == nil {
		t.Fatalf("no status report was rendered, so this half proved nothing about KMS calls: "+
			"%v\n%s", err, out)
	}
}

// TestKeysStatusReportsThePolicyEnvelope closes a posture gap of its own: the
// policy signing key is a fail-closed CMEK custody source like the other two
// (cmd/olivares/auditkey.go:304), `keys wrap --purpose policy` seals one, and the
// command whose entire job is to report custody posture omitted it.
func TestKeysStatusReportsThePolicyEnvelope(t *testing.T) {
	startFakeKEKServer(t)
	policy := filepath.Join(t.TempDir(), "policy-signing.key.sealed")
	if out, err := runKeys(t, "wrap", "--mint", "--purpose", "policy", "--out", policy); err != nil {
		t.Fatalf("keys wrap --mint --purpose policy: %v\n%s", err, out)
	}
	t.Setenv(envPolicyWrapped, policy)

	report, out, err := statusReport(t, "--verify-envelopes")
	if err != nil {
		t.Fatalf("keys status --verify-envelopes: %v\n%s", err, out)
	}
	if got := envelopeField(t, report, "policy", "purpose"); got != secure.PurposePolicySigningKey {
		t.Fatalf("the policy envelope is absent or mislabelled in the posture report: %v\n%s", got, out)
	}
	if got := envelopeField(t, report, "policy", "authenticated"); got != true {
		t.Fatalf("the policy envelope was not authenticated: %v\n%s", got, out)
	}
}
