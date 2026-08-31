// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/secure"
)

// The nine SEALED-MATERIAL leaves of VER-06 lot L2 — keys wrap/rotate/rewrap/seal,
// secrets put/rotate/rm, ddil keygen, license keygen — pinned in BOTH directions,
// because they share one risk and it is not a cosmetic one: their text already
// prints fingerprints, public keys and hints, and `ddil keygen` prints the PRIVATE
// key. Serializing a field into a structured object is the cheapest way to publish
// a secret into a log pipeline, so the JSON of each leaf is asserted field by field
// AND the material that must not be there is asserted ABSENT.
//
// Direction (a): -o json parses, and carries the same facts the text states.
// Direction (b): with no -o at all the stdout bytes are IDENTICAL to what these
// commands printed before -o json existed. Every want string below was measured
// against the pre-change binary, not read off the source, and the assertions are
// `!=` on whole stdout rather than Contains: a Contains here would still pass while
// a new line leaked in beside the one it checks.
//
// The invariant the whole lot turns on, stated once:
//
//	EVERY FIELD THE JSON CARRIES IS A FIELD THE TEXT ALREADY PRINTED. A half of a
//	keypair that was written to a FILE reports its PATH, never its value; a half
//	printed to stdout in text reports that same value in JSON.
//
// That is why `ddil keygen --out` reports private_key_file and `ddil keygen`
// reports private_key: not an inconsistency, the rule applied twice.

// runKeysJSON drives one `keys …` invocation through the REAL root, which is what
// makes -o reachable at all: newKeysCmd() alone has no --output flag, so a test
// built on the group would exercise the text path under both spellings and prove
// nothing (selectedOutput falls back to "text" when the flag is absent).
func runKeysJSON(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	root := newRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"keys"}, args...))
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func runSecretsRoot(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	root := newRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"secrets"}, args...))
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func runDDILRoot(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	root := newRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"ddil"}, args...))
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func runLicenseKeygen(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	root := newRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"license", "keygen"}, args...))
	err := root.Execute()
	return out.String(), errOut.String(), err
}

// jsonObjectFault decodes stdout as exactly ONE JSON object, returning the
// complaint instead of failing so it can be calibrated like the other two guards.
//
// The trailing-content half is the part worth stating. json.Decoder.Decode reads
// the FIRST value and stops, so a helper that decodes once and reports success
// accepts `{…}{…}` and discards the rest in silence — which is the defect
// `lint:json-decoders` exists to catch on the request side of this same repo
// (scripts/check-json-decoders.sh). On the OUTPUT side it reads as: a leaf that
// emitted its object and then printed anything else would still satisfy "stdout is
// a JSON object", and the contract this lot sells is that `-o json` gives a script
// one document it can parse. Half a contract is not one.
func jsonObjectFault(stdout string) (map[string]any, string) {
	var obj map[string]any
	dec := json.NewDecoder(strings.NewReader(stdout))
	if err := dec.Decode(&obj); err != nil {
		return nil, fmt.Sprintf("is not a JSON object: %v", err)
	}
	var rest json.RawMessage
	if err := dec.Decode(&rest); err == nil {
		return nil, fmt.Sprintf("emitted a SECOND document after its object: %s", rest)
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Sprintf("has trailing content after its object: %v", err)
	}
	return obj, ""
}

func mustDecodeJSONObject(t *testing.T, what, stdout string) map[string]any {
	t.Helper()
	obj, bad := jsonObjectFault(stdout)
	if bad != "" {
		t.Fatalf("%s -o json %s\n%s", what, bad, stdout)
	}
	return obj
}

// keySetMismatch pins the COMPLETE key set of a JSON object, returning "" when the
// set is exact and the complaint otherwise. A missing key and an EXTRA key are both
// failures; the extra-key half is what catches a field grown into a report that
// never printed it.
//
// It returns a string instead of calling t.Fatalf so that it can be CALIBRATED —
// see TestTheGuardsOfThisFileCanActuallyFail. A guard nobody has watched fail is
// indistinguishable from a guard that always passes, and this file rests both of
// its contrafactuals on these three functions.
func keySetMismatch(obj map[string]any, want ...string) string {
	got := make([]string, 0, len(obj))
	for k := range obj {
		got = append(got, k)
	}
	sort.Strings(got)
	wantSet := map[string]bool{}
	for _, k := range want {
		wantSet[k] = true
		if _, ok := obj[k]; !ok {
			return fmt.Sprintf("missing %q; keys = %v", k, got)
		}
	}
	for _, k := range got {
		if !wantSet[k] {
			return fmt.Sprintf("UNDECLARED field %q (value %#v); keys = %v", k, obj[k], got)
		}
	}
	return ""
}

func assertKeySet(t *testing.T, what string, obj map[string]any, want ...string) {
	t.Helper()
	if bad := keySetMismatch(obj, want...); bad != "" {
		t.Fatalf("%s -o json: %s", what, bad)
	}
}

// secretMaterialLeak is the leak half of the double contrafactual: it reports the
// forbidden bytes found anywhere in the RENDERED output, whatever key they arrived
// under and however deeply nested. Grepping the rendered form rather than named
// fields is the point — a mutant that files the private key under "note", appends
// it to a path, or buries it one level down satisfies every per-field check and
// still publishes the key.
//
// An empty forbidden value is itself reported as a failure: a check whose needle is
// "" can never fire, and a guard that cannot fail is not a guard.
func secretMaterialLeak(rendered string, forbidden map[string]string) string {
	labels := make([]string, 0, len(forbidden))
	for label := range forbidden {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for _, label := range labels {
		material := forbidden[label]
		if material == "" {
			return fmt.Sprintf("the %s fixture is EMPTY, so this check could not fail — it is not a check", label)
		}
		if strings.Contains(rendered, material) {
			return fmt.Sprintf("LEAKED %s into its output:\n%s", label, rendered)
		}
	}
	return ""
}

func assertNoSecretMaterial(t *testing.T, what, rendered string, forbidden map[string]string) {
	t.Helper()
	if bad := secretMaterialLeak(rendered, forbidden); bad != "" {
		t.Fatalf("%s %s", what, bad)
	}
}

// TestTheGuardsOfThisFileCanActuallyFail calibrates both directions of all three
// guards, because every other assertion here is built on them. Same reasoning as
// TestAssertModeRefusesAModeThatDoesNotMatch in license_keygen_custody_test.go: it
// proves the checks are not resting on something that never fires.
//
// This is also the ONLY place the leak guard can be isolated. In the real leaves the
// per-field assertions are tight enough that a serialized private key trips three
// guards at once, so a production mutant proves whichever one is reached first —
// which is exactly the trap of measuring a guard other than the one you named.
func TestTheGuardsOfThisFileCanActuallyFail(t *testing.T) {
	// keySetMismatch: accepts the exact set, refuses a missing key, refuses an extra.
	exact := map[string]any{"private_key_file": "/p", "public_key": "AAA="}
	if bad := keySetMismatch(exact, "private_key_file", "public_key"); bad != "" {
		t.Fatalf("keySetMismatch rejected the exact key set: %s", bad)
	}
	if bad := keySetMismatch(exact, "private_key_file", "public_key", "absent"); bad == "" {
		t.Fatal("keySetMismatch accepted an object missing a required key")
	}
	leaky := map[string]any{"private_key_file": "/p", "public_key": "AAA=", "private_key": "SEED"}
	if bad := keySetMismatch(leaky, "private_key_file", "public_key"); bad == "" {
		t.Fatal("keySetMismatch accepted an object carrying an extra field")
	}

	// secretMaterialLeak: clean passes, the needle anywhere in the bytes fails, and
	// nesting or concatenation does not hide it.
	const seed = "c2VlZC1tYXRlcmlhbA=="
	if bad := secretMaterialLeak(`{"public_key":"AAA="}`, map[string]string{"the seed": seed}); bad != "" {
		t.Fatalf("secretMaterialLeak flagged a clean object: %s", bad)
	}
	for name, rendered := range map[string]string{
		"as its own field":    `{"private_key":"` + seed + `"}`,
		"under another name":  `{"note":"` + seed + `"}`,
		"appended to a path":  `{"private_key_file":"/p (seed ` + seed + `)"}`,
		"nested one level in": `{"custody":{"seed":"` + seed + `"}}`,
	} {
		if bad := secretMaterialLeak(rendered, map[string]string{"the seed": seed}); bad == "" {
			t.Fatalf("secretMaterialLeak missed the seed %s: %s", name, rendered)
		}
	}
	// The needle-is-empty case, which would otherwise make every call vacuously pass.
	if bad := secretMaterialLeak(`{}`, map[string]string{"an unset fixture": ""}); bad == "" {
		t.Fatal("secretMaterialLeak accepted an EMPTY needle, so any caller with an unset fixture proves nothing")
	}

	// oneLabelledLine: one line with a value passes; a second line, the wrong label
	// and an EMPTY value are all refused. The empty case is the one a shape check
	// alone waves through, which is how `private: ` passed once already.
	if v, ok := oneLabelledLine("private_key: AAA=\n", "private_key: "); !ok || v != "AAA=" {
		t.Fatalf("oneLabelledLine rejected one clean line: %q %v", v, ok)
	}
	for name, in := range map[string]string{
		"a second line":   "private_key: AAA=\npublic_key:  BBB=\n",
		"the wrong label": "public_key:  AAA=\n",
		"an empty value":  "private_key: \n",
	} {
		if _, ok := oneLabelledLine(in, "private_key: "); ok {
			t.Fatalf("oneLabelledLine accepted %s: %q", name, in)
		}
	}

	// publicOfLicensePrivate: it agrees with a REAL pair, and what it returns is not
	// the private key it was handed — which is what makes `pub != publicOf(priv)` able
	// to fail at all, and is precisely what the mutant made equal.
	realPub, realPriv, gerr := ed25519.GenerateKey(nil)
	if gerr != nil {
		t.Fatal(gerr)
	}
	privB64 := base64.StdEncoding.EncodeToString(realPriv)
	if got := publicOfLicensePrivate(t, privB64); got != base64.StdEncoding.EncodeToString(realPub) {
		t.Fatalf("publicOfLicensePrivate derived %q, want the pair's public half", got)
	}
	if publicOfLicensePrivate(t, privB64) == privB64 {
		t.Fatal("publicOfLicensePrivate returned the private key it was given, so the pair check could not fail")
	}

	// jsonObjectFault: one object passes, and everything that is not EXACTLY one
	// object is named. The trailing cases are the ones a bare Decode would wave
	// through, which is the whole reason this guard is a function.
	if obj, bad := jsonObjectFault(`{"public_key":"AAA="}` + "\n"); bad != "" || obj["public_key"] != "AAA=" {
		t.Fatalf("jsonObjectFault rejected one clean object: %s (%#v)", bad, obj)
	}
	for name, rendered := range map[string]string{
		"a second document":   `{"public_key":"AAA="}` + "\n" + `{"private_key":"SEED"}` + "\n",
		"a trailing fragment": `{"public_key":"AAA="}` + "\nprivate: SEED\n",
		"a JSON array":        `[{"public_key":"AAA="}]`,
		"a bare string":       `"AAA="`,
		"nothing at all":      "",
		"a truncated object":  `{"public_key":`,
	} {
		if _, bad := jsonObjectFault(rendered); bad == "" {
			t.Fatalf("jsonObjectFault accepted %s: %q", name, rendered)
		}
	}
}

// TestKeysCeremonyTextIsUnchangedAndJSONMatchesIt walks wrap → rotate → rewrap →
// seal, asserting each one's whole stdout byte for byte in text mode and the same
// facts as a parsed object in JSON mode. The expectations are built from the
// envelope that landed on disk, so the test pins the FORMAT rather than the fake
// KEK's fixture values.
func TestKeysCeremonyTextIsUnchangedAndJSONMatchesIt(t *testing.T) {
	startFakeKEKServer(t)
	dir := t.TempDir()
	envPath := filepath.Join(dir, "audit-signing.key.sealed")

	// ---- keys wrap --mint ------------------------------------------------
	textOut, _, err := runKeysJSON(t, "wrap", "--mint", "--purpose", "audit", "--out", envPath)
	if err != nil {
		t.Fatalf("keys wrap --mint: %v\n%s", err, textOut)
	}
	e1, err := secure.ReadSealedFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	pub1 := base64.StdEncoding.EncodeToString(e1.PublicKey)
	wantWrap := fmt.Sprintf("sealed %s envelope written to %s\n  kek:        %s %s\n  public key: %s\n",
		secure.PurposeAuditSigningKey, envPath, e1.Provider, e1.KeyID, pub1)
	if textOut != wantWrap {
		t.Fatalf("keys wrap text changed:\n got %q\nwant %q", textOut, wantWrap)
	}

	envJSON := filepath.Join(dir, "audit-signing.json.sealed")
	jsonOut, _, err := runKeysJSON(t, "wrap", "--mint", "--purpose", "audit", "--out", envJSON, "-o", "json")
	if err != nil {
		t.Fatalf("keys wrap --mint -o json: %v\n%s", err, jsonOut)
	}
	ej, err := secure.ReadSealedFile(envJSON)
	if err != nil {
		t.Fatal(err)
	}
	obj := mustDecodeJSONObject(t, "keys wrap", jsonOut)
	assertKeySet(t, "keys wrap", obj, "purpose", "out", "provider", "kek", "public_key")
	if obj["purpose"] != secure.PurposeAuditSigningKey || obj["out"] != envJSON ||
		obj["provider"] != ej.Provider || obj["kek"] != ej.KeyID ||
		obj["public_key"] != base64.StdEncoding.EncodeToString(ej.PublicKey) {
		t.Fatalf("keys wrap JSON does not match the envelope it wrote: %#v", obj)
	}

	// ---- keys rotate -----------------------------------------------------
	textOut, _, err = runKeysJSON(t, "rotate", "--in", envPath, "--yes")
	if err != nil {
		t.Fatalf("keys rotate: %v\n%s", err, textOut)
	}
	e2, err := secure.ReadSealedFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	wantRotate := fmt.Sprintf("rotated: new envelope written to %s\n  new public key: %s\n  prior generations kept: %d\n",
		envPath, base64.StdEncoding.EncodeToString(e2.PublicKey), len(e2.PriorPublicKeys))
	if textOut != wantRotate {
		t.Fatalf("keys rotate text changed:\n got %q\nwant %q", textOut, wantRotate)
	}
	if len(e2.PriorPublicKeys) != 1 {
		t.Fatalf("rotation history = %d, want 1 — the count this leaf reports must be real", len(e2.PriorPublicKeys))
	}

	jsonOut, _, err = runKeysJSON(t, "rotate", "--in", envPath, "--yes", "-o", "json")
	if err != nil {
		t.Fatalf("keys rotate -o json: %v\n%s", err, jsonOut)
	}
	e3, err := secure.ReadSealedFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	obj = mustDecodeJSONObject(t, "keys rotate", jsonOut)
	assertKeySet(t, "keys rotate", obj, "out", "new_public_key", "prior_generations_kept")
	if obj["out"] != envPath || obj["new_public_key"] != base64.StdEncoding.EncodeToString(e3.PublicKey) {
		t.Fatalf("keys rotate JSON does not match the envelope it wrote: %#v", obj)
	}
	if got, want := obj["prior_generations_kept"], float64(len(e3.PriorPublicKeys)); got != want {
		t.Fatalf("keys rotate JSON prior_generations_kept = %#v, want %v", got, want)
	}
	// The text prints the COUNT of prior generations, never the keys themselves, so
	// the JSON does not carry the list either. assertKeySet already refuses an extra
	// field; this states WHY the list is the field that must not appear.
	if _, leaked := obj["prior_public_keys"]; leaked {
		t.Fatal("keys rotate JSON grew a prior_public_keys list the text form never printed")
	}

	// ---- keys rewrap -----------------------------------------------------
	textOut, _, err = runKeysJSON(t, "rewrap", "--in", envPath, "--yes")
	if err != nil {
		t.Fatalf("keys rewrap: %v\n%s", err, textOut)
	}
	e4, err := secure.ReadSealedFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	wantRewrap := fmt.Sprintf("rewrapped %s under %s %s -> %s\n", envPath, e4.Provider, e4.KeyID, envPath)
	if textOut != wantRewrap {
		t.Fatalf("keys rewrap text changed:\n got %q\nwant %q", textOut, wantRewrap)
	}

	jsonOut, _, err = runKeysJSON(t, "rewrap", "--in", envPath, "--yes", "-o", "json")
	if err != nil {
		t.Fatalf("keys rewrap -o json: %v\n%s", err, jsonOut)
	}
	e5, err := secure.ReadSealedFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	obj = mustDecodeJSONObject(t, "keys rewrap", jsonOut)
	assertKeySet(t, "keys rewrap", obj, "in", "provider", "kek", "out")
	if obj["in"] != envPath || obj["out"] != envPath || obj["provider"] != e5.Provider || obj["kek"] != e5.KeyID {
		t.Fatalf("keys rewrap JSON does not match the envelope it wrote: %#v", obj)
	}

	// ---- keys seal -------------------------------------------------------
	cfgPath := filepath.Join(dir, "notify.json")
	if werr := os.WriteFile(cfgPath, []byte(`{"webhook":"https://h/x","secret":"s3cr3t-config"}`), 0o600); werr != nil {
		t.Fatal(werr)
	}
	sealedText := filepath.Join(dir, "notify.text.sealed")
	textOut, _, err = runKeysJSON(t, "seal", "--in", cfgPath, "--out", sealedText)
	if err != nil {
		t.Fatalf("keys seal: %v\n%s", err, textOut)
	}
	wantSeal := fmt.Sprintf("sealed %s -> %s (point the OLIVARES_*_CONFIG env at the sealed file; "+
		"the engine opens it transparently at boot)\n", cfgPath, sealedText)
	if textOut != wantSeal {
		t.Fatalf("keys seal text changed:\n got %q\nwant %q", textOut, wantSeal)
	}

	sealedJSON := filepath.Join(dir, "notify.json.sealed")
	jsonOut, _, err = runKeysJSON(t, "seal", "--in", cfgPath, "--out", sealedJSON, "-o", "json")
	if err != nil {
		t.Fatalf("keys seal -o json: %v\n%s", err, jsonOut)
	}
	obj = mustDecodeJSONObject(t, "keys seal", jsonOut)
	assertKeySet(t, "keys seal", obj, "in", "out")
	if obj["in"] != cfgPath || obj["out"] != sealedJSON {
		t.Fatalf("keys seal JSON does not report the paths it sealed: %#v", obj)
	}
	// keys seal reads a config whose PLAINTEXT contains a secret. The report names
	// the two paths and nothing else — the config's contents never enter it.
	assertNoSecretMaterial(t, "keys seal", jsonOut, map[string]string{
		"the plaintext config secret": "s3cr3t-config",
	})
}

// TestKeysWrapFromReportsThePlaintextSourceAndNeverItsKey covers the ONE path in
// this lot where a private signing key is in scope at the moment the report is
// built: `keys wrap --from` decodes an operator's plaintext key file to seal it, so
// the material is a local variable away from being serialized. The text names the
// source PATH in its NOTE and never the key; the JSON does the same.
func TestKeysWrapFromReportsThePlaintextSourceAndNeverItsKey(t *testing.T) {
	startFakeKEKServer(t)
	plainDir := t.TempDir()
	plainPath := filepath.Join(plainDir, "audit-signing.key")
	if _, _, err := secure.LoadOrCreateSigningKey(plainPath); err != nil {
		t.Fatal(err)
	}
	plainKeyFile, err := os.ReadFile(plainPath)
	if err != nil {
		t.Fatal(err)
	}
	privMaterial := strings.TrimSpace(string(plainKeyFile))

	migratedText := filepath.Join(plainDir, "migrated.text.sealed")
	textOut, _, err := runKeysJSON(t, "wrap", "--from", plainPath, "--out", migratedText)
	if err != nil {
		t.Fatalf("keys wrap --from: %v\n%s", err, textOut)
	}
	me, err := secure.ReadSealedFile(migratedText)
	if err != nil {
		t.Fatal(err)
	}
	wantText := fmt.Sprintf("sealed %s envelope written to %s\n  kek:        %s %s\n  public key: %s\n"+
		"NOTE: the plaintext key file %s still exists — verify the envelope boots, then shred it; "+
		"the sealed envelope is now the only at-rest copy you need.\n",
		secure.PurposeAuditSigningKey, migratedText, me.Provider, me.KeyID,
		base64.StdEncoding.EncodeToString(me.PublicKey), plainPath)
	if textOut != wantText {
		t.Fatalf("keys wrap --from text changed:\n got %q\nwant %q", textOut, wantText)
	}
	assertNoSecretMaterial(t, "keys wrap --from text", textOut, map[string]string{
		"the plaintext signing key": privMaterial,
	})

	migratedJSON := filepath.Join(plainDir, "migrated.json.sealed")
	jsonOut, _, err := runKeysJSON(t, "wrap", "--from", plainPath, "--out", migratedJSON, "-o", "json")
	if err != nil {
		t.Fatalf("keys wrap --from -o json: %v\n%s", err, jsonOut)
	}
	// The leak check runs FIRST on the json form, as it does on ddil keygen and
	// license keygen, and it was measured into this order rather than assumed. With
	// assertKeySet ahead of it, a mutant that filed the decoded signing key under an
	// UNDECLARED field died reporting `UNDECLARED field "note"` — the shape guard,
	// not the leak guard. Same bytes, same kill, wrong finding: whoever later widens
	// the declared key set would silently move a published private key from "caught"
	// to "not looked at".
	//
	// The TEXT half above keeps the opposite order on purpose. There the contract IS
	// byte-identity with the pre-change binary, so the byte comparison is the primary
	// assertion and the leak check is the belt behind it. The json form has no
	// byte-identity contract — it is new — so its two guards are peers, and the leak
	// is the finding that matters.
	assertNoSecretMaterial(t, "keys wrap --from -o json", jsonOut, map[string]string{
		"the plaintext signing key": privMaterial,
	})
	obj := mustDecodeJSONObject(t, "keys wrap --from", jsonOut)
	assertKeySet(t, "keys wrap --from", obj, "purpose", "out", "provider", "kek", "public_key", "migrated_from")
	if obj["migrated_from"] != plainPath {
		t.Fatalf("keys wrap --from JSON does not name the source: %#v", obj)
	}
}

// TestSecretsMutationsTextIsUnchangedAndJSONMatchesIt pins put/rotate/rm. The
// forbidden material is the secret VALUE, and the hint is asserted to be exactly
// the product's own published fingerprint — a truncated SHA-256 the text already
// prints — recomputed here from the value rather than copied out of the output, so
// the assertion cannot agree with a wrong answer.
func TestSecretsMutationsTextIsUnchangedAndJSONMatchesIt(t *testing.T) {
	dir := initialisedDataDir(t)
	const (
		name     = "vault/token"
		value    = "s3cr3t-value-one"
		nextVal  = "s3cr3t-value-two"
		actor    = "ops@example.com"
		reasonWh = "L2 regression fixture"
	)
	hintOf := func(v string) string {
		sum := sha256.Sum256([]byte(v))
		return hex.EncodeToString(sum[:])[:12]
	}

	// ---- secrets put -----------------------------------------------------
	textOut, _, err := runSecretsRoot(t, "put", "--data-dir", dir, "--name", name,
		"--value", value, "--actor", actor, "--reason", reasonWh)
	if err != nil {
		t.Fatalf("secrets put: %v\n%s", err, textOut)
	}
	wantPut := fmt.Sprintf("stored secret %q (hint %s)\n", name, hintOf(value))
	if textOut != wantPut {
		t.Fatalf("secrets put text changed:\n got %q\nwant %q", textOut, wantPut)
	}
	assertNoSecretMaterial(t, "secrets put text", textOut, map[string]string{"the secret value": value})

	jsonOut, _, err := runSecretsRoot(t, "put", "--data-dir", dir, "--name", name,
		"--value", value, "--actor", actor, "--reason", reasonWh, "-o", "json")
	if err != nil {
		t.Fatalf("secrets put -o json: %v\n%s", err, jsonOut)
	}
	// Leak check FIRST on every json form in this file — measured, not assumed. A
	// mutant that files forbidden material under a field nobody declared dies on
	// whichever guard is reached first, and with assertKeySet ahead of this one the
	// finding reads "UNDECLARED field", which is a shape complaint about a published
	// secret. Reported as the leak it is.
	assertNoSecretMaterial(t, "secrets put -o json", jsonOut, map[string]string{"the secret value": value})
	obj := mustDecodeJSONObject(t, "secrets put", jsonOut)
	assertKeySet(t, "secrets put", obj, "name", "hint")
	if obj["name"] != name || obj["hint"] != hintOf(value) {
		t.Fatalf("secrets put JSON = %#v, want name/hint of the stored value", obj)
	}

	// ---- secrets rotate --------------------------------------------------
	textOut, _, err = runSecretsRoot(t, "rotate", "--data-dir", dir, "--name", name,
		"--value", nextVal, "--actor", actor, "--reason", reasonWh)
	if err != nil {
		t.Fatalf("secrets rotate: %v\n%s", err, textOut)
	}
	wantRotate := fmt.Sprintf("rotated secret %q (hint %s)\n", name, hintOf(nextVal))
	if textOut != wantRotate {
		t.Fatalf("secrets rotate text changed:\n got %q\nwant %q", textOut, wantRotate)
	}

	jsonOut, _, err = runSecretsRoot(t, "rotate", "--data-dir", dir, "--name", name,
		"--value", nextVal, "--actor", actor, "--reason", reasonWh, "-o", "json")
	if err != nil {
		t.Fatalf("secrets rotate -o json: %v\n%s", err, jsonOut)
	}
	assertNoSecretMaterial(t, "secrets rotate -o json", jsonOut, map[string]string{
		"the new secret value": nextVal,
		"the old secret value": value,
	})
	obj = mustDecodeJSONObject(t, "secrets rotate", jsonOut)
	assertKeySet(t, "secrets rotate", obj, "name", "hint")
	if obj["name"] != name || obj["hint"] != hintOf(nextVal) {
		t.Fatalf("secrets rotate JSON = %#v, want the hint of the NEW value", obj)
	}

	// ---- secrets rm ------------------------------------------------------
	// --yes because confirmDestructive refuses a non-interactive session without
	// it; the prompt itself goes to stderr, which is what keeps stdout parseable.
	textOut, _, err = runSecretsRoot(t, "rm", "--data-dir", dir, "--name", name,
		"--actor", actor, "--reason", reasonWh, "--yes")
	if err != nil {
		t.Fatalf("secrets rm: %v\n%s", err, textOut)
	}
	wantRM := fmt.Sprintf("deleted secret %q\n", name)
	if textOut != wantRM {
		t.Fatalf("secrets rm text changed:\n got %q\nwant %q", textOut, wantRM)
	}

	if _, _, perr := runSecretsRoot(t, "put", "--data-dir", dir, "--name", name,
		"--value", value, "--actor", actor, "--reason", reasonWh); perr != nil {
		t.Fatalf("re-put for the rm -o json case: %v", perr)
	}
	jsonOut, _, err = runSecretsRoot(t, "rm", "--data-dir", dir, "--name", name,
		"--actor", actor, "--reason", reasonWh, "--yes", "-o", "json")
	if err != nil {
		t.Fatalf("secrets rm -o json: %v\n%s", err, jsonOut)
	}
	// rm never had a hint in its text and must not grow one: the hint is a
	// fingerprint OF THE VALUE, and a delete has no business reading the value.
	assertNoSecretMaterial(t, "secrets rm -o json", jsonOut, map[string]string{
		"the secret value": value,
		"the value hint":   hintOf(value),
	})
	obj = mustDecodeJSONObject(t, "secrets rm", jsonOut)
	assertKeySet(t, "secrets rm", obj, "name")
	if obj["name"] != name {
		t.Fatalf("secrets rm JSON = %#v, want the deleted name", obj)
	}
}

// TestSecretsMutationsHonourTheDeprecatedFormatAliasAndExplicitText closes the two
// spellings the leaves above do not exercise.
//
//   - `--format json` is the alias the `secrets` group has carried since before
//     -o existed (addTextJSONFormatFlag, cmd_secrets.go:37), and it is what
//     TestSecretsListTextAndJSON — the one pre-existing JSON test in this group —
//     actually uses. A script written against `secrets ls --format json` and
//     extended to `secrets put` must not find text there. This runs through the
//     GROUP rather than the root, which is the path where selectedOutput has no
//     --output to read and must fall back to the alias.
//   - `-o text` must be byte-identical to no flag at all. These leaves go through
//     renderOut, so they keep the global text default — unlike the report commands,
//     where renderReportOut deliberately keeps JSON when nobody asked
//     (render.go:221-231). Asserting it here is what distinguishes "text is the
//     default" from "text happens when the flag is absent".
func TestSecretsMutationsHonourTheDeprecatedFormatAliasAndExplicitText(t *testing.T) {
	dir := initialisedDataDir(t)
	const (
		name  = "vault/alias-token"
		value = "s3cr3t-alias-value"
		actor = "ops@example.com"
		why   = "L2 alias fixture"
	)
	sum := sha256.Sum256([]byte(value))
	hint := hex.EncodeToString(sum[:])[:12]

	// The alias, through the group, exactly as the pre-existing ls test spells it.
	aliasOut, err := runSecrets(t, "put", "--data-dir", dir, "--name", name,
		"--value", value, "--actor", actor, "--reason", why, "--format", "json")
	if err != nil {
		t.Fatalf("secrets put --format json: %v\n%s", err, aliasOut)
	}
	// The leak check FIRST, as on every other json form in this file. This one was
	// missing, and a mutant showed what that costs: with the secret VALUE added to
	// secretMutationResult, the -o json case above died on "LEAKED the secret value"
	// and this case died on `UNDECLARED field "value"` — a shape complaint about a
	// published secret, in the spelling a script written against `secrets ls --format
	// json` would actually use. The alias is not a lesser path; it is the older one.
	assertNoSecretMaterial(t, "secrets put --format json", aliasOut, map[string]string{
		"the secret value": value,
	})
	obj := mustDecodeJSONObject(t, "secrets put --format json", aliasOut)
	assertKeySet(t, "secrets put --format json", obj, "name", "hint")
	if obj["name"] != name || obj["hint"] != hint {
		t.Fatalf("secrets put --format json = %#v, want the same object -o json gives", obj)
	}

	// -o text and no flag at all must produce the SAME bytes.
	bare, err := runSecrets(t, "put", "--data-dir", dir, "--name", name,
		"--value", value, "--actor", actor, "--reason", why)
	if err != nil {
		t.Fatalf("secrets put (no flag): %v\n%s", err, bare)
	}
	explicit, err := runSecrets(t, "put", "--data-dir", dir, "--name", name,
		"--value", value, "--actor", actor, "--reason", why, "--format", "text")
	if err != nil {
		t.Fatalf("secrets put --format text: %v\n%s", err, explicit)
	}
	want := fmt.Sprintf("stored secret %q (hint %s)\n", name, hint)
	if bare != want {
		t.Fatalf("secrets put with no format flag = %q, want %q", bare, want)
	}
	if explicit != bare {
		t.Fatalf("asking for text changed the bytes: %q vs %q", explicit, bare)
	}
}

// TestDDILKeygenJSONReportsTheFileAndNotTheKeyWhenAskedToWriteOne is the leak
// contrafactual in both directions on the one leaf in the product that can print a
// private key to stdout.
//
//   - WITH --out the private seed goes to a 0600 file and the text prints ONLY the
//     public half (the documented `keygen --out priv > pub` idiom depends on that
//     bare line). The JSON therefore reports private_key_file — the path the
//     operator supplied — and the seed itself must be absent.
//   - WITHOUT --out the operator has chosen stdout as the sink for both halves and
//     the text already prints `private: <seed>`. Dropping it from the JSON would
//     make the two forms of one command disagree about which fields exist, which
//     render.go already refuses in writing for the empty-map case. It is present,
//     and it is the same value the text shows.
func TestDDILKeygenJSONReportsTheFileAndNotTheKeyWhenAskedToWriteOne(t *testing.T) {
	dir := t.TempDir()

	// ---- --out: text is the bare public key, byte for byte ---------------
	textPath := filepath.Join(dir, "ddil-text.key")
	textOut, errOut, err := runDDILRoot(t, "keygen", "--out", textPath)
	if err != nil {
		t.Fatalf("ddil keygen --out: %v\n%s", err, errOut)
	}
	if errOut != "" {
		t.Fatalf("ddil keygen --out wrote stderr: %q", errOut)
	}
	seedText, err := os.ReadFile(textPath)
	if err != nil {
		t.Fatal(err)
	}
	seedB64 := strings.TrimSpace(string(seedText))
	if textOut != strings.TrimSpace(textOut)+"\n" || !strings.HasSuffix(textOut, "\n") {
		t.Fatalf("ddil keygen --out stdout is not one bare line: %q", textOut)
	}
	if textOut != publicOfSeed(t, seedB64)+"\n" {
		t.Fatalf("ddil keygen --out text changed: got %q, want the bare public key line", textOut)
	}

	// ---- --out -o json: the PATH, never the seed -------------------------
	jsonPath := filepath.Join(dir, "ddil-json.key")
	jsonOut, errOut, err := runDDILRoot(t, "keygen", "--out", jsonPath, "-o", "json")
	if err != nil {
		t.Fatalf("ddil keygen --out -o json: %v\n%s", err, errOut)
	}
	if errOut != "" {
		t.Fatalf("ddil keygen --out -o json wrote stderr: %q", errOut)
	}
	jsonSeedText, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	jsonSeed := strings.TrimSpace(string(jsonSeedText))
	// THE mutant this exists to kill: the private seed serialized into the report
	// whose text form deliberately withholds it. It runs FIRST, before the key-set
	// and per-field checks, so that a leak is reported BY THE GUARD THAT NAMES IT.
	// A serialized seed trips all three of them; whichever runs first is the one a
	// mutation run measures, and the leak is the finding that matters.
	assertNoSecretMaterial(t, "ddil keygen --out -o json", jsonOut, map[string]string{
		"the private DDIL seed": jsonSeed,
	})
	obj := mustDecodeJSONObject(t, "ddil keygen --out", jsonOut)
	assertKeySet(t, "ddil keygen --out", obj, "private_key_file", "public_key")
	if obj["private_key_file"] != jsonPath {
		t.Fatalf("ddil keygen --out JSON does not name the sink: %#v", obj)
	}
	if obj["public_key"] != publicOfSeed(t, jsonSeed) {
		t.Fatalf("ddil keygen --out JSON public_key does not match the seed it wrote: %#v", obj)
	}

	// ---- no --out: both halves, in text and in JSON alike ----------------
	textOut, errOut, err = runDDILRoot(t, "keygen")
	if err != nil {
		t.Fatalf("ddil keygen: %v\n%s", err, errOut)
	}
	if !strings.Contains(errOut, "off the importing node") {
		t.Fatalf("ddil keygen dropped its stderr warning: %q", errOut)
	}
	privLine, pubLine, ok := twoLabelledLines(textOut, "private: ", "public: ")
	if !ok {
		t.Fatalf("ddil keygen text changed: %q", textOut)
	}
	if textOut != fmt.Sprintf("private: %s\npublic: %s\n", privLine, pubLine) {
		t.Fatalf("ddil keygen text is not exactly the two labeled lines: %q", textOut)
	}
	// A labeled line with nothing after the label still satisfies the shape check
	// above, so the emptiness is named separately. Measured with a mutant that
	// removed the seed from the struct the renderer reads: the text degraded to
	// `private: ` and the shape assertion passed.
	if privLine == "" || pubLine == "" {
		t.Fatalf("ddil keygen text kept its labels and LOST a value: %q", textOut)
	}
	if pubLine != publicOfSeed(t, privLine) {
		t.Fatalf("ddil keygen printed a public key that is not the seed's: %q / %q", privLine, pubLine)
	}

	jsonOut, errOut, err = runDDILRoot(t, "keygen", "-o", "json")
	if err != nil {
		t.Fatalf("ddil keygen -o json: %v\n%s", err, errOut)
	}
	if !strings.Contains(errOut, "off the importing node") {
		t.Fatalf("ddil keygen -o json dropped its stderr warning: %q", errOut)
	}
	obj = mustDecodeJSONObject(t, "ddil keygen", jsonOut)
	assertKeySet(t, "ddil keygen", obj, "private_key", "public_key")
	priv, _ := obj["private_key"].(string)
	if priv == "" {
		t.Fatal("ddil keygen -o json dropped the private key its own text form prints")
	}
	if obj["public_key"] != publicOfSeed(t, priv) {
		t.Fatalf("ddil keygen -o json halves do not match: %#v", obj)
	}
}

// TestLicenseKeygenJSONReportsEachHalfBySink pins all four sink combinations of
// `license keygen` against the one rule: a half written to a FILE reports its path,
// a half printed to stdout reports its value.
//
// It says "all four" and now runs four. It ran three: `--out-public` alone — the
// combination whose object carries a private key AND a path — was never invoked.
//
// And every case now proves the two halves are one PAIR, which is the gap two mutants
// located between them. Measured, in this order, because the first result is what made
// the second one worth posing:
//
//   - `pubB64 := license.EncodeKey(priv)` on EVERY sink — the private key published
//     under `public_key` — DIED, on the `--out-private` case's leak fixture below.
//     That case has the key on disk, so it has a needle to grep for. The class was
//     covered, and by the guard that names it.
//   - The same substitution applied ONLY when both halves go to stdout SURVIVED the
//     whole package. That is the one combination where a leak fixture is impossible —
//     the private key belongs in that object — so a shape assertion comparing the two
//     captured values against themselves is all that was left, and it cannot see WHICH
//     key it captured. It is also the bare `license keygen`, the form with no flags.
//
// The stdout spelling matters more than it looks. The text form is
// `public_key:  <b64>` with TWO spaces, so scripting it today means a sed that
// happens to tolerate the padding; `-o json | jq -r .public_key` is what the
// command's own Long text needs for the -ldflags injection it documents — and that
// injection is why publishing the wrong half here reaches every built binary.
func TestLicenseKeygenJSONReportsEachHalfBySink(t *testing.T) {
	dir := t.TempDir()

	// ---- both halves on stdout ------------------------------------------
	textOut, errOut, err := runLicenseKeygen(t)
	if err != nil {
		t.Fatalf("license keygen: %v\n%s", err, errOut)
	}
	if !strings.Contains(errOut, "Assign this pair to exactly one domain") {
		t.Fatalf("license keygen dropped its custody guidance: %q", errOut)
	}
	privLine, pubLine, ok := twoLabelledLines(textOut, "private_key: ", "public_key:  ")
	if !ok {
		t.Fatalf("license keygen text changed: %q", textOut)
	}
	if textOut != fmt.Sprintf("private_key: %s\npublic_key:  %s\n", privLine, pubLine) {
		t.Fatalf("license keygen text is not exactly the two labeled lines: %q", textOut)
	}
	// A labeled line with nothing after the label satisfies the shape check above —
	// the same trap already named on ddil keygen — so emptiness is stated separately.
	if privLine == "" || pubLine == "" {
		t.Fatalf("license keygen text kept its labels and LOST a value: %q", textOut)
	}
	// THE check of the case that had none. ddil keygen has proved its two halves are one
	// PAIR since the lot landed (publicOfSeed); this leaf did not, and here it cannot be
	// covered any other way: the private key belongs in this object, so there is no
	// needle a leak fixture could grep for. A mutant that emitted the private half as
	// the public one ONLY in this combination survived the whole package.
	//
	// What the operator does with the value is in this command's own Long text: bake it
	// into a release build with `-ldflags -X …releasePublicKeyB64=`.
	if pubLine != publicOfLicensePrivate(t, privLine) {
		t.Fatalf("license keygen printed a public key that is not the private half's: %q / %q", privLine, pubLine)
	}

	jsonOut, _, err := runLicenseKeygen(t, "-o", "json")
	if err != nil {
		t.Fatalf("license keygen -o json: %v\n%s", err, jsonOut)
	}
	obj := mustDecodeJSONObject(t, "license keygen", jsonOut)
	assertKeySet(t, "license keygen", obj, "private_key", "public_key")
	priv, _ := obj["private_key"].(string)
	if priv == "" {
		t.Fatal("license keygen -o json dropped the private key its own text form prints")
	}
	if obj["public_key"] != publicOfLicensePrivate(t, priv) {
		t.Fatalf("license keygen -o json halves do not match: %#v", obj)
	}

	// ---- private to a file, public on stdout ----------------------------
	privPath := filepath.Join(dir, "one.key")
	jsonOut, _, err = runLicenseKeygen(t, "--out-private", privPath, "-o", "json")
	if err != nil {
		t.Fatalf("license keygen --out-private -o json: %v\n%s", err, jsonOut)
	}
	privOnDisk, err := os.ReadFile(privPath)
	if err != nil {
		t.Fatal(err)
	}
	// The whole point of --out-private is that the key does not pass through stdout.
	// A JSON report that serializes it undoes the flag, so this runs before the
	// key-set and per-field checks that would otherwise report it as a mere shape
	// problem.
	assertNoSecretMaterial(t, "license keygen --out-private -o json", jsonOut, map[string]string{
		"the private license key": strings.TrimSpace(string(privOnDisk)),
	})
	obj = mustDecodeJSONObject(t, "license keygen --out-private", jsonOut)
	assertKeySet(t, "license keygen --out-private", obj, "private_key_file", "public_key")
	if obj["private_key_file"] != privPath {
		t.Fatalf("license keygen --out-private JSON does not name the sink: %#v", obj)
	}
	// The public half it reports must belong to the private half it wrote: "the key
	// is not in stdout" and "the key in stdout is the right one" are two claims.
	if obj["public_key"] != publicOfLicensePrivate(t, strings.TrimSpace(string(privOnDisk))) {
		t.Fatalf("license keygen --out-private JSON public_key is not the written key's half: %#v", obj)
	}

	// ---- private on stdout, public to a file ----------------------------
	// The FOURTH combination, and it was missing while the comment above claimed all
	// four. It is the one whose object carries a private key AND a path, so it is
	// where a sink mix-up would put key material under the wrong name unobserved.
	pubSink := filepath.Join(dir, "sink.pub")
	textOut, _, err = runLicenseKeygen(t, "--out-public", pubSink)
	if err != nil {
		t.Fatalf("license keygen --out-public: %v\n%s", err, textOut)
	}
	privOnly, ok := oneLabelledLine(textOut, "private_key: ")
	if !ok {
		t.Fatalf("license keygen --out-public text is not the single private line: %q", textOut)
	}
	pubSinkBytes, err := os.ReadFile(pubSink)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(pubSinkBytes)) != publicOfLicensePrivate(t, privOnly) {
		t.Fatal("license keygen --out-public wrote a public key that is not the printed private half's")
	}

	pubSinkJSON := filepath.Join(dir, "sink-json.pub")
	jsonOut, _, err = runLicenseKeygen(t, "--out-public", pubSinkJSON, "-o", "json")
	if err != nil {
		t.Fatalf("license keygen --out-public -o json: %v\n%s", err, jsonOut)
	}
	obj = mustDecodeJSONObject(t, "license keygen --out-public", jsonOut)
	assertKeySet(t, "license keygen --out-public", obj, "private_key", "public_key_file")
	if obj["public_key_file"] != pubSinkJSON {
		t.Fatalf("license keygen --out-public JSON does not name the sink: %#v", obj)
	}
	privInObj, _ := obj["private_key"].(string)
	if privInObj == "" {
		t.Fatal("license keygen --out-public -o json dropped the private key its own text form prints")
	}
	pubSinkJSONBytes, err := os.ReadFile(pubSinkJSON)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(pubSinkJSONBytes)) != publicOfLicensePrivate(t, privInObj) {
		t.Fatal("license keygen --out-public -o json: the file and the object are not one pair")
	}

	// ---- both halves to files -------------------------------------------
	priv2 := filepath.Join(dir, "two.key")
	pub2 := filepath.Join(dir, "two.pub")
	textOut, _, err = runLicenseKeygen(t, "--out-private", priv2, "--out-public", pub2)
	if err != nil {
		t.Fatalf("license keygen both files: %v\n%s", err, textOut)
	}
	if textOut != "" {
		t.Fatalf("license keygen with both sinks as files wrote stdout: %q", textOut)
	}
	priv3 := filepath.Join(dir, "three.key")
	pub3 := filepath.Join(dir, "three.pub")
	jsonOut, _, err = runLicenseKeygen(t, "--out-private", priv3, "--out-public", pub3, "-o", "json")
	if err != nil {
		t.Fatalf("license keygen both files -o json: %v\n%s", err, jsonOut)
	}
	privOnDisk3, err := os.ReadFile(priv3)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecretMaterial(t, "license keygen both files -o json", jsonOut, map[string]string{
		"the private license key": strings.TrimSpace(string(privOnDisk3)),
	})
	obj = mustDecodeJSONObject(t, "license keygen both files", jsonOut)
	assertKeySet(t, "license keygen both files", obj, "private_key_file", "public_key_file")
	if obj["private_key_file"] != priv3 || obj["public_key_file"] != pub3 {
		t.Fatalf("license keygen both-files JSON does not name both sinks: %#v", obj)
	}
	pubOnDisk3, err := os.ReadFile(pub3)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(pubOnDisk3)) != publicOfLicensePrivate(t, strings.TrimSpace(string(privOnDisk3))) {
		t.Fatal("license keygen both files: the two files it names are not one pair")
	}
}

// TestLicenseKeygenKeepsThePrivateLineWhenThePublicWriteFails pins the ordering
// cmd_license.go calls LOAD-BEARING in writing, and which nothing measured.
//
// When the private half goes to stdout and the PUBLIC write then fails, that
// printed line is the only copy of the key that ever existed — nothing was
// persisted. The comment on licenseKeygenCmd says this is why the private line is
// emitted where the ceremony reaches it instead of from the renderer at the end. A
// mutant that moves it into the renderer produces byte-identical stdout on the
// success path, so every other test in this file passes; only this case sees the
// line disappear.
//
// The json half is the fail-closed counterpart: a half-written object is not
// parseable, so it emits NOTHING and says to re-run the ceremony. That mode is new,
// so it breaks no contract.
//
// The pre-existing TestKeygenRemovesThePrivateKeyWhenThePublicHalfCannotLand covers
// the OTHER branch — private to a FILE, which gets removed — and it skips as root.
// This one poses the failure with an occupied path, so it runs everywhere.
func TestLicenseKeygenKeepsThePrivateLineWhenThePublicWriteFails(t *testing.T) {
	dir := t.TempDir()
	occupied := filepath.Join(dir, "taken.pub")
	if err := os.WriteFile(occupied, []byte("DO-NOT-OVERWRITE\n"), 0o644); err != nil { //nolint:gosec // the point of the fixture
		t.Fatal(err)
	}

	textOut, _, err := runLicenseKeygen(t, "--out-public", occupied)
	if err == nil {
		t.Fatal("license keygen reported success while the public key could not be written")
	}
	privLine, ok := oneLabelledLine(textOut, "private_key: ")
	if !ok {
		t.Fatalf("the private key line is GONE from a failed run that had already printed it: %q", textOut)
	}
	// It must be the real key, not a label with nothing after it.
	if _, derr := base64.StdEncoding.DecodeString(privLine); derr != nil {
		t.Fatalf("what was printed on the private line is not a key: %q", privLine)
	}

	jsonOut, _, err := runLicenseKeygen(t, "--out-public", occupied, "-o", "json")
	if err == nil {
		t.Fatal("license keygen -o json reported success while the public key could not be written")
	}
	if jsonOut != "" {
		t.Fatalf("license keygen -o json emitted a partial document on the failure path: %q", jsonOut)
	}
	if !strings.Contains(err.Error(), "re-run the ceremony") {
		t.Fatalf("the json failure does not tell the operator the ceremony is re-runnable: %v", err)
	}
}

// publicOfSeed derives the public half from a base64 Ed25519 SEED, independently
// of the command under test, so "the halves match" is a real check.
func publicOfSeed(t *testing.T, seedB64 string) string {
	t.Helper()
	seed, err := base64.StdEncoding.DecodeString(seedB64)
	if err != nil {
		t.Fatalf("seed is not base64: %v", err)
	}
	if len(seed) != ed25519.SeedSize {
		t.Fatalf("seed is %d bytes, want a %d-byte Ed25519 seed", len(seed), ed25519.SeedSize)
	}
	pub, ok := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("Ed25519 private key did not yield an Ed25519 public key")
	}
	return base64.StdEncoding.EncodeToString(pub)
}

// publicOfLicensePrivate derives the PUBLIC half of a base64 `license keygen`
// private key without asking the command, and cross-checks the two places an
// Ed25519 private key carries it: derived from the seed, and the 32 bytes the key
// format appends. Agreeing with itself is not enough — that is exactly what the
// shape assertions do.
//
// It exists because a mutant that emitted the private half as the public one in the
// ONE combination where both halves go to stdout survived the whole package: that is
// the case where the private key legitimately belongs in the output, so no leak
// fixture can have a needle and the pair is the only thing left to check.
func publicOfLicensePrivate(t *testing.T, privB64 string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(privB64)
	if err != nil {
		t.Fatalf("private key is not base64: %v", err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		t.Fatalf("private key is %d bytes, want a %d-byte Ed25519 private key", len(raw), ed25519.PrivateKeySize)
	}
	derived := publicOfSeed(t, base64.StdEncoding.EncodeToString(raw[:ed25519.SeedSize]))
	if embedded := base64.StdEncoding.EncodeToString(raw[ed25519.SeedSize:]); embedded != derived {
		t.Fatalf("the private key's embedded public half %q is not its seed's %q", embedded, derived)
	}
	return derived
}

// oneLabelledLine splits a single-line `<label><value>\n` stdout, refusing a second
// line so nothing can slip in beside the one value this sink prints, and refusing an
// EMPTY value so a label with nothing after it is not mistaken for a key.
func oneLabelledLine(stdout, label string) (string, bool) {
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], label) {
		return "", false
	}
	value := strings.TrimPrefix(lines[0], label)
	return value, value != ""
}

// twoLabelledLines splits a two-line `<a><valueA>\n<b><valueB>\n` stdout into its
// two values, refusing anything with a third line so an extra field cannot slip in
// beside the two this lot expects.
func twoLabelledLines(stdout, labelA, labelB string) (string, string, bool) {
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 2 {
		return "", "", false
	}
	if !strings.HasPrefix(lines[0], labelA) || !strings.HasPrefix(lines[1], labelB) {
		return "", "", false
	}
	return strings.TrimPrefix(lines[0], labelA), strings.TrimPrefix(lines[1], labelB), true
}
