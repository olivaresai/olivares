// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/dr/opgate"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

// ⛔ THE FIXTURES HERE ARE GENUINE KEYS ON DISK, and that is the point of the whole
// case file.
//
// The fixture this replaces built three SelectedKey values out of synthetic public
// keys that existed in no file and that no signer was ever constructed from, then
// asked the production encoder for their digest. Nothing in it could have failed if
// the boot had loaded three completely different keys: it measured the encoder
// against itself.
//
// So these write real Ed25519 private keys into a real data directory, in exactly the
// on-disk form the loaders read, and the expected digest is computed HERE — the
// canonical JSON assembled by hand and hashed by hand — rather than by calling
// opgate.NewKeyset on the same inputs the production path uses. A bug in that encoder
// must be able to fail this file.

// writeGenuineLocalKey installs one real signing key and returns its private object.
func writeGenuineLocalKey(t *testing.T, dataDir, name string) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataDir, name)
	// 0600 and base64, the exact shape secure.LoadOrCreateSigningKey writes.
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(priv)), 0o600); err != nil {
		t.Fatal(err)
	}
	// Proof the loader's own reader accepts what was written, so a failure later in
	// this file is about custody and not about the fixture's encoding.
	back, lerr := secure.LoadSigningKey(path)
	if lerr != nil {
		t.Fatalf("the genuine fixture key is not loadable by the production reader: %v", lerr)
	}
	if !back.Equal(priv) {
		t.Fatal("the genuine fixture key did not round-trip through the production reader")
	}
	return priv
}

// genuineThreeKeyDataDir installs the three selected signing keys a completed
// operation would have published, and returns the directory and the key objects.
func genuineThreeKeyDataDir(t *testing.T) (string, [3]ed25519.PrivateKey) {
	t.Helper()
	dir := t.TempDir()
	return dir, [3]ed25519.PrivateKey{
		writeGenuineLocalKey(t, dir, "audit-signing.key"),
		writeGenuineLocalKey(t, dir, "catalog-signing.key"),
		writeGenuineLocalKey(t, dir, "policy-signing.key"),
	}
}

// independentKeysetDigest recomputes the ratified v1 commitment from PUBLIC KEYS
// alone, without calling opgate's encoder.
//
// It is deliberately a second implementation. Calling opgate.NewKeyset here would
// make every assertion below tautological: the production path would be compared
// against itself, which is the exact defect this sublot closes one layer up.
func independentKeysetDigest(t *testing.T, sources [3]string, keys [3]ed25519.PrivateKey) string {
	t.Helper()
	type entry struct {
		Purpose      string `json:"purpose"`
		Source       string `json:"source"`
		PublicSHA256 string `json:"public_sha256"`
	}
	purposes := [3]string{"audit", "catalog", "policy"}
	var entries [3]entry
	for i, k := range keys {
		sum := sha256.Sum256(k.Public().(ed25519.PublicKey))
		entries[i] = entry{purposes[i], sources[i], hex.EncodeToString(sum[:])}
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(append([]byte("olivares.dr.keyset.v1\n"), raw...))
	return hex.EncodeToString(digest[:])
}

func allLocal() [3]string { return [3]string{"local", "local", "local"} }

// keyFileNames is the on-disk census a "no new key bytes" assertion is taken over.
var keyFileNames = []string{"audit-signing.key", "catalog-signing.key", "policy-signing.key"}

// keyBytesCensus records the exact bytes of every signing key file that exists.
func keyBytesCensus(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, name := range keyFileNames {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		sum := sha256.Sum256(b)
		out[name] = hex.EncodeToString(sum[:])
	}
	return out
}

// assertNoNewKeyBytes is the assertion every refusal case in this file ends with: a
// custody refusal must leave the data directory byte-identical. A refusal that minted
// a key on the way out would be the defect wearing an error message.
func assertNoNewKeyBytes(t *testing.T, dir string, before map[string]string) {
	t.Helper()
	after := keyBytesCensus(t, dir)
	for name, sum := range after {
		prior, existed := before[name]
		if !existed {
			t.Fatalf("the refusal CREATED %s: no such key existed before it ran", name)
		}
		if prior != sum {
			t.Fatalf("the refusal REPLACED %s: %s became %s", name, prior, sum)
		}
	}
	for name := range before {
		if _, still := after[name]; !still {
			t.Fatalf("the refusal removed %s", name)
		}
	}
}

// TestObservationIsBuiltFromTheActualLoadedKeyObjects is the measurement side of
// F3-IR-5, taken over the real loaders and the REAL SIGNERS.
//
// The assertion that matters is the last one: the public identity the audit signer
// actually signs with is the identity the observation reports. Comparing the
// observation to the helper's returned struct would prove only that one function
// copied another's field.
func TestObservationIsBuiltFromTheActualLoadedKeyObjects(t *testing.T) {
	dir, keys := genuineThreeKeyDataDir(t)
	log := bootGateTestLogger()

	auditKey, err := loadAuditSigningKey(dir, log, withEnrolledCustody())
	if err != nil {
		t.Fatalf("load the audit key: %v", err)
	}
	catalogKey, err := loadCatalogSigningKey(dir, log, withEnrolledCustody())
	if err != nil {
		t.Fatalf("load the catalog key: %v", err)
	}
	policyKey, err := loadPolicySigningKey(dir, log, withEnrolledCustody())
	if err != nil {
		t.Fatalf("load the policy key: %v", err)
	}

	obs, err := observeSelectedCustody(auditKey, catalogKey, policyKey)
	if err != nil {
		t.Fatalf("observe the selected custody: %v", err)
	}
	want := independentKeysetDigest(t, allLocal(), keys)
	if obs.KeysetSHA256() != want {
		t.Fatalf("the observation's digest is %s and the independent computation over the genuine public keys is %s", obs.KeysetSHA256(), want)
	}

	// THE ACTUAL SIGNER'S PUBLIC IDENTITY, not the loader's returned struct.
	//
	// audit.NewSigner is the constructor boot() calls, and PublicKey() is the identity
	// that signer reports for its own signatures — the one an off-box
	// `audit verify --pubkey` is checked against. Comparing the observation to
	// auditKey.priv would only prove one struct field was copied into another.
	signer, err := audit.NewSigner(auditKey.priv)
	if err != nil {
		t.Fatalf("construct the real audit signer: %v", err)
	}
	sum := sha256.Sum256(signer.PublicKey())
	observedAudit := obs.Keys()[0]
	if observedAudit.Purpose != "audit" || observedAudit.PublicSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("the observation does not name the public identity the real audit signer signs with: %+v vs %s", observedAudit, hex.EncodeToString(sum[:]))
	}
	if observedAudit.Source != "local" {
		t.Fatalf("an installed local key must observe as local custody, got %q", observedAudit.Source)
	}

	// No key bytes anywhere in the observation's own rendering.
	for _, k := range obs.Keys() {
		if strings.Contains(k.PublicSHA256, base64.StdEncoding.EncodeToString(keys[0])) {
			t.Fatal("the observation carries key material")
		}
	}
}

// TestStrictEnrolledModeNeverCreatesALocalKey is the difference between
// withoutMinting and the enrolled mode, stated as a measurement.
//
// withoutMinting checks existence and then calls LoadOrCreateSigningKey; the enrolled
// mode calls the load-only reader. With the file absent, the first would create and
// the second must not.
func TestStrictEnrolledModeNeverCreatesALocalKey(t *testing.T) {
	for _, tc := range []struct {
		purpose string
		file    string
		load    func(string, *slog.Logger, ...keyLoadOption) (loadedSigningKey, error)
	}{
		{"audit", "audit-signing.key", loadAuditSigningKey},
		{"catalog", "catalog-signing.key", loadCatalogSigningKey},
		{"policy", "policy-signing.key", loadPolicySigningKey},
	} {
		t.Run(tc.purpose, func(t *testing.T) {
			dir := t.TempDir()
			k, err := tc.load(dir, bootGateTestLogger(), withEnrolledCustody())
			if err == nil {
				t.Fatalf("the strict enrolled mode LOADED a %s key from an empty data directory: %+v", tc.purpose, k.mode)
			}
			if !strings.Contains(err.Error(), "COMPLETED restore control") {
				t.Fatalf("the refusal does not name the custody cause: %v", err)
			}
			if _, serr := os.Stat(filepath.Join(dir, tc.file)); !os.IsNotExist(serr) {
				t.Fatalf("the strict enrolled mode CREATED %s: %v", tc.file, serr)
			}
			// The ordinary unenrolled path is unchanged and still mints, which is the
			// genuine first boot this must not have broken.
			if _, merr := tc.load(dir, bootGateTestLogger()); merr != nil {
				t.Fatalf("the ordinary unenrolled load stopped minting: %v", merr)
			}
			if _, serr := os.Stat(filepath.Join(dir, tc.file)); serr != nil {
				t.Fatalf("the ordinary unenrolled load did not mint %s: %v", tc.file, serr)
			}
		})
	}
}

// TestStrictEnrolledModeRefusesAMissingConfiguredSourceWithALocalFallback is the
// source-substitution half, and it is the case withoutMinting cannot catch.
//
// The catalog and policy loaders are deliberately lenient about a configured-but-
// absent BYOK file: they warn and fall through to the local key. Under withoutMinting
// that fallthrough still happens and SUCCEEDS whenever a local key exists — serving
// the estate with a key the completed operation never published.
func TestStrictEnrolledModeRefusesAMissingConfiguredSourceWithALocalFallback(t *testing.T) {
	for _, tc := range []struct {
		purpose string
		env     string
		load    func(string, *slog.Logger, ...keyLoadOption) (loadedSigningKey, error)
	}{
		{"catalog", envCatalogKeyFile, loadCatalogSigningKey},
		{"policy", envPolicyKeyFile, loadPolicySigningKey},
	} {
		t.Run(tc.purpose, func(t *testing.T) {
			dir, _ := genuineThreeKeyDataDir(t)
			absent := filepath.Join(t.TempDir(), "mounted-secret-that-is-not-there.key")
			t.Setenv(tc.env, absent)
			before := keyBytesCensus(t, dir)

			// CONTROL: the lenient ordinary path still falls through to the local key,
			// which is the existing contract this change must not have altered.
			lenient, lerr := tc.load(dir, bootGateTestLogger())
			if lerr != nil || lenient.mode != custodyModeMinted {
				t.Fatalf("the ordinary lenient path changed: %+v %v", lenient.mode, lerr)
			}

			// STRICT: the configured source wins and it is unreadable, so this refuses
			// rather than serving the local key that happens to be there.
			got, err := tc.load(dir, bootGateTestLogger(), withEnrolledCustody())
			if err == nil {
				t.Fatalf("the strict enrolled mode substituted a %s key from %q for the missing configured source", tc.purpose, got.mode)
			}
			if !strings.Contains(err.Error(), "never falls back to a local key") {
				t.Fatalf("the refusal does not name source substitution: %v", err)
			}
			assertNoNewKeyBytes(t, dir, before)
		})
	}
}

// TestStrictEnrolledModeRefusesAmbiguousAndUnreadableExternalCustody keeps the
// existing ambiguity and CMEK fail-closed contracts under the strict mode, and proves
// no key is created while refusing either.
func TestStrictEnrolledModeRefusesAmbiguousAndUnreadableExternalCustody(t *testing.T) {
	t.Run("conflicting_cmek_and_byok", func(t *testing.T) {
		dir, _ := genuineThreeKeyDataDir(t)
		before := keyBytesCensus(t, dir)
		t.Setenv(envAuditWrapped, filepath.Join(dir, "sealed.json"))
		t.Setenv(envAuditKey, base64.StdEncoding.EncodeToString(make([]byte, ed25519.PrivateKeySize)))
		if _, err := loadAuditSigningKey(dir, bootGateTestLogger(), withEnrolledCustody()); err == nil ||
			!strings.Contains(err.Error(), "declare ONE custody source") {
			t.Fatalf("two declared custody sources were resolved by a guess: %v", err)
		}
		assertNoNewKeyBytes(t, dir, before)
	})
	t.Run("unreadable_sealed_envelope", func(t *testing.T) {
		dir, _ := genuineThreeKeyDataDir(t)
		before := keyBytesCensus(t, dir)
		t.Setenv(envAuditWrapped, filepath.Join(t.TempDir(), "absent-envelope.json"))
		if _, err := loadAuditSigningKey(dir, bootGateTestLogger(), withEnrolledCustody()); err == nil {
			t.Fatal("an unopenable sealed envelope was downgraded to a local key")
		}
		assertNoNewKeyBytes(t, dir, before)
	})
}

// TestTheObservationRefusesAnIncompleteOrUnloadedSelection covers the constructor's
// own closure: it takes exactly three actual key objects and no digest.
func TestTheObservationRefusesAnIncompleteOrUnloadedSelection(t *testing.T) {
	_, keys := genuineThreeKeyDataDir(t)
	good := func(i int) coreengine.SelectedSigner {
		return coreengine.SelectedSigner{
			Purpose:    []string{"audit", "catalog", "policy"}[i],
			LoaderMode: custodyModeMinted, Key: keys[i],
		}
	}
	if _, err := coreengine.ObserveSelectedCustody(good(0), good(1)); err == nil {
		t.Fatal("a two-key selection produced an observation")
	}
	empty := good(2)
	empty.Key = nil
	if _, err := coreengine.ObserveSelectedCustody(good(0), good(1), empty); err == nil {
		t.Fatal("an unloaded key produced an observation")
	}
	bad := good(2)
	bad.LoaderMode = "borrowed"
	if _, err := coreengine.ObserveSelectedCustody(good(0), good(1), bad); err == nil {
		t.Fatal("an unsupported custody source produced an observation")
	}
	obs, err := coreengine.ObserveSelectedCustody(good(0), good(1), good(2))
	if err != nil {
		t.Fatalf("the genuine three-key selection was refused: %v", err)
	}
	if obs.KeysetSHA256() != independentKeysetDigest(t, allLocal(), keys) {
		t.Fatal("the observation's digest does not match the independent computation")
	}
	if !obs.Present() {
		t.Fatal("a built observation reports itself absent")
	}
	if (coreengine.CustodyObservation{}).Present() {
		t.Fatal("the zero observation reports itself present")
	}
}

// TestRecheckDetectsAChangedExternalSelectionWithoutSubstituting is the pre-
// publication recheck. It DETECTS; it cannot immobilize a mounted Secret, and the
// property it must have is that a changed source refuses without a new key appearing.
func TestRecheckDetectsAChangedExternalSelectionWithoutSubstituting(t *testing.T) {
	dir, keys := genuineThreeKeyDataDir(t)
	mounted := filepath.Join(t.TempDir(), "catalog-secret.key")
	original := writeGenuineLocalKeyAt(t, mounted)
	t.Setenv(envCatalogKeyFile, mounted)

	auditKey, err := loadAuditSigningKey(dir, bootGateTestLogger(), withEnrolledCustody())
	if err != nil {
		t.Fatal(err)
	}
	catalogKey, err := loadCatalogSigningKey(dir, bootGateTestLogger(), withEnrolledCustody())
	if err != nil {
		t.Fatal(err)
	}
	policyKey, err := loadPolicySigningKey(dir, bootGateTestLogger(), withEnrolledCustody())
	if err != nil {
		t.Fatal(err)
	}
	if catalogKey.mode != custodyModeBYOKFile || !catalogKey.priv.Equal(original) {
		t.Fatalf("the configured external source did not win: %q", catalogKey.mode)
	}
	obs, err := observeSelectedCustody(auditKey, catalogKey, policyKey)
	if err != nil {
		t.Fatal(err)
	}
	want := independentKeysetDigest(t,
		[3]string{"local", "byok-file", "local"},
		[3]ed25519.PrivateKey{keys[0], original, keys[2]})
	if obs.KeysetSHA256() != want {
		t.Fatalf("the observed digest %s is not the independent one %s", obs.KeysetSHA256(), want)
	}

	// UNCHANGED: the recheck agrees with what the boot loaded.
	if rerr := recheckSelectedCustody(dir, nil, obs); rerr != nil {
		t.Fatalf("the recheck refused an unchanged selection: %v", rerr)
	}

	before := keyBytesCensus(t, dir)
	// CHANGED: the mounted Secret is replaced under the boot, as a rotation or a
	// hostile remount would.
	replaced := writeGenuineLocalKeyAt(t, mounted)
	if replaced.Equal(original) {
		t.Fatal("the fixture did not actually change the mounted key")
	}
	rerr := recheckSelectedCustody(dir, nil, obs)
	if rerr == nil {
		t.Fatal("the recheck accepted a selection that changed under the boot")
	}
	if !strings.Contains(rerr.Error(), "CHANGED while this boot was preparing") {
		t.Fatalf("the refusal does not name the change: %v", rerr)
	}
	assertNoNewKeyBytes(t, dir, before)

	// MISSING: the source is removed entirely. It refuses, and it does NOT fall back
	// to the local catalog key that is sitting right there.
	if err := os.Remove(mounted); err != nil {
		t.Fatal(err)
	}
	rerr = recheckSelectedCustody(dir, nil, obs)
	if rerr == nil {
		t.Fatal("the recheck accepted a selection whose configured source had vanished")
	}
	if !strings.Contains(rerr.Error(), "never falls back to a local key") {
		t.Fatalf("the refusal does not name source substitution: %v", rerr)
	}
	assertNoNewKeyBytes(t, dir, before)
}

func writeGenuineLocalKeyAt(t *testing.T, path string) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(priv)), 0o600); err != nil {
		t.Fatal(err)
	}
	return priv
}

// TestTheCanonicalDigestCommitsToSourceAndKey proves the two refusals the acceptance
// distinguishes are actually distinguishable: the same key from a different source
// and a different key from the same source produce different commitments.
func TestTheCanonicalDigestCommitsToSourceAndKey(t *testing.T) {
	_, keys := genuineThreeKeyDataDir(t)
	base := independentKeysetDigest(t, allLocal(), keys)

	wrongSource := independentKeysetDigest(t, [3]string{"local", "byok-file", "local"}, keys)
	if wrongSource == base {
		t.Fatal("the commitment does not distinguish the custody SOURCE, so a correct key from the wrong source would pass")
	}
	other := [3]ed25519.PrivateKey{keys[0], keys[1], writeGenuineLocalKeyAt(t, filepath.Join(t.TempDir(), "x.key"))}
	wrongKey := independentKeysetDigest(t, allLocal(), other)
	if wrongKey == base {
		t.Fatal("the commitment does not distinguish the KEY")
	}

	// And the production encoder agrees with the independent one on all three, which
	// is what lets the comparisons above stand for the real path.
	for _, tc := range []struct {
		name    string
		sources [3]string
		keys    [3]ed25519.PrivateKey
		want    string
	}{
		{"base", allLocal(), keys, base},
		{"wrong_source", [3]string{"local", "byok-file", "local"}, keys, wrongSource},
		{"wrong_key", allLocal(), other, wrongKey},
	} {
		var selected []opgate.SelectedKey
		for i, p := range []opgate.KeyPurpose{opgate.KeyAudit, opgate.KeyCatalog, opgate.KeyPolicy} {
			f, err := opgate.FingerprintPublicKey(tc.keys[i].Public().(ed25519.PublicKey))
			if err != nil {
				t.Fatal(err)
			}
			selected = append(selected, opgate.SelectedKey{Purpose: p, Source: opgate.CustodySource(tc.sources[i]), PublicSHA256: f})
		}
		wire, err := opgate.NewKeyset(selected)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if wire.SHA256 != tc.want {
			t.Fatalf("%s: the production encoder produced %s and the independent computation %s", tc.name, wire.SHA256, tc.want)
		}
	}
}

// writeCompletedLocalControl installs a genuine COMPLETE local restore control on the
// data directory anchor, naming the keyset the three genuine keys actually produce.
//
// It is written through the production codec under a real exclusive lease — the same
// path an operation would take — so the record this boot reads is one this build
// wrote, not a hand-assembled JSON blob whose validation nobody exercised.
func writeCompletedLocalControl(t *testing.T, dataDir, dsn string, keys []opgate.SelectedKey) opgate.Keyset {
	t.Helper()
	wire, err := opgate.NewKeyset(keys)
	if err != nil {
		t.Fatal(err)
	}
	anchor, present, err := opgate.AnchorForDataDir(dataDir)
	if err != nil || !present {
		t.Fatalf("anchor the data directory: present=%t %v", present, err)
	}
	lease, ok, err := opgate.TryAcquire(opgate.ModeExclusive, anchor)
	if err != nil || !ok {
		t.Fatalf("take the fixture lease: ok=%t %v", ok, err)
	}
	defer func() { _ = lease.Release() }()
	rec := opgate.Record{
		Format:     opgate.Format,
		Revision:   1,
		OpID:       "a1b2c3d4e5f60718293a4b5c6d7e8f90",
		State:      opgate.StateComplete,
		Enrolled:   true,
		PlanSHA256: strings.Repeat("ab", 32),
		Destination: opgate.Destination{
			Engine: "sqlite", CanonicalPath: anchor.Canonical(), SQLiteFile: resolvedSQLiteFile(t, dsn),
		},
		Keyset:     wire,
		ObservedAt: "2026-09-12T00:00:00Z",
	}
	if err := lease.Commit(anchor, rec); err != nil {
		t.Fatalf("commit the completed fixture control: %v", err)
	}
	return wire
}

// resolvedSQLiteFile is the frozen target the one authoritative resolver produces,
// which is what a record must name — never the caller's spelling of it.
func resolvedSQLiteFile(t *testing.T, dsn string) string {
	t.Helper()
	target, err := opgate.ResolveSQLiteTarget(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return target.CanonicalPath()
}

// selectedFor builds the per-purpose selection of the three genuine keys.
func selectedFor(t *testing.T, sources [3]string, keys [3]ed25519.PrivateKey) []opgate.SelectedKey {
	t.Helper()
	out := make([]opgate.SelectedKey, 0, 3)
	for i, p := range []opgate.KeyPurpose{opgate.KeyAudit, opgate.KeyCatalog, opgate.KeyPolicy} {
		f, err := opgate.FingerprintPublicKey(keys[i].Public().(ed25519.PublicKey))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, opgate.SelectedKey{Purpose: p, Source: opgate.CustodySource(sources[i]), PublicSHA256: f})
	}
	return out
}

// TestSQLiteCompletedCustodyAdmitsTheExactSelectionAndRefusesEveryOther is the
// end-to-end SQLite acceptance, taken through the REAL boot admission.
//
// The positive is the exact complete fixture: three genuine keys that are already on
// disk, a control that names their canonical keyset, and a boot that loads them
// unchanged and publishes. Every negative is the same fixture with exactly one fact
// moved, and each one must refuse leaving no new key bytes behind.
func TestSQLiteCompletedCustodyAdmitsTheExactSelectionAndRefusesEveryOther(t *testing.T) {
	t.Run("exact_complete_fixture_succeeds_read_only", func(t *testing.T) {
		dir, keys := genuineThreeKeyDataDir(t)
		dsn := filepath.Join(dir, "olivares.db")
		wire := writeCompletedLocalControl(t, dir, dsn, selectedFor(t, allLocal(), keys))
		if wire.SHA256 != independentKeysetDigest(t, allLocal(), keys) {
			t.Fatal("the fixture control does not name the independently computed digest")
		}
		before := keyBytesCensus(t, dir)

		pub, err := acquireBootPublication(t.Context(), storeConfigForTest(dsn), dir)
		if err != nil {
			t.Fatalf("the admission refused an exact complete fixture: %v", err)
		}
		defer pub.Close()

		req := pub.CustodyRequirement()
		if !req.CustodyRequired() {
			t.Fatal("a COMPLETE local control did not require completed custody")
		}
		if req.KeysetSHA256() != wire.SHA256 {
			t.Fatalf("the requirement names %s and the control names %s", req.KeysetSHA256(), wire.SHA256)
		}
		if len(req.ExpectedKeys()) != 3 {
			t.Fatalf("local completed evidence did not supply the expected per-purpose selection: %+v", req.ExpectedKeys())
		}

		// The strict load the requirement selects, and the observation built from it.
		obs := loadAndObserveForTest(t, dir)
		if obs.KeysetSHA256() != wire.SHA256 {
			t.Fatalf("the boot loaded %s and the control authorized %s", obs.KeysetSHA256(), wire.SHA256)
		}
		if rerr := recheckSelectedCustody(dir, nil, obs); rerr != nil {
			t.Fatalf("the pre-publication recheck refused an unchanged selection: %v", rerr)
		}
		st, err := pub.Open(t.Context(), nil, obs, nil)
		if err != nil {
			t.Fatalf("the exact complete fixture did not publish: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })

		// READ-ONLY over custody: the keys are byte-identical after a successful boot.
		assertNoNewKeyBytes(t, dir, before)
	})

	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, dir string, keys [3]ed25519.PrivateKey) []opgate.SelectedKey
		reason string
	}{
		{
			name: "wrong_public_key",
			mutate: func(t *testing.T, dir string, keys [3]ed25519.PrivateKey) []opgate.SelectedKey {
				other := writeGenuineLocalKeyAt(t, filepath.Join(t.TempDir(), "foreign.key"))
				return selectedFor(t, allLocal(), [3]ed25519.PrivateKey{keys[0], other, keys[2]})
			},
			reason: "the catalog key loaded here is",
		},
		{
			name: "correct_key_wrong_source",
			mutate: func(t *testing.T, dir string, keys [3]ed25519.PrivateKey) []opgate.SelectedKey {
				return selectedFor(t, [3]string{"local", "byok-file", "local"}, keys)
			},
			reason: "resolved from",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, keys := genuineThreeKeyDataDir(t)
			dsn := filepath.Join(dir, "olivares.db")
			writeCompletedLocalControl(t, dir, dsn, tc.mutate(t, dir, keys))
			before := keyBytesCensus(t, dir)
			pub, err := acquireBootPublication(t.Context(), storeConfigForTest(dsn), dir)
			if err != nil {
				t.Fatalf("the admission refused before the custody comparison could be made: %v", err)
			}
			defer pub.Close()
			if !pub.CustodyRequirement().CustodyRequired() {
				t.Fatal("the completed control did not require custody")
			}
			obs := loadAndObserveForTest(t, dir)
			st, oerr := pub.Open(t.Context(), nil, obs, nil)
			if st != nil {
				_ = st.Close()
			}
			if oerr == nil {
				t.Fatalf("%s was published under custody the completed control never authorized", tc.name)
			}
			if !strings.Contains(oerr.Error(), tc.reason) {
				t.Fatalf("the refusal does not name the difference (%s): %v", tc.reason, oerr)
			}
			assertNoNewKeyBytes(t, dir, before)
			if _, serr := os.Stat(dsn); !os.IsNotExist(serr) {
				t.Fatalf("a refused custody comparison created the destination: %v", serr)
			}
		})
	}

	t.Run("missing_audit_key_refuses_before_the_store", func(t *testing.T) {
		dir, keys := genuineThreeKeyDataDir(t)
		dsn := filepath.Join(dir, "olivares.db")
		writeCompletedLocalControl(t, dir, dsn, selectedFor(t, allLocal(), keys))
		if err := os.Remove(filepath.Join(dir, "audit-signing.key")); err != nil {
			t.Fatal(err)
		}
		before := keyBytesCensus(t, dir)
		pub, err := acquireBootPublication(t.Context(), storeConfigForTest(dsn), dir)
		if err != nil {
			t.Fatal(err)
		}
		defer pub.Close()
		if _, lerr := loadAuditSigningKey(dir, bootGateTestLogger(), withEnrolledCustody()); lerr == nil {
			t.Fatal("the strict mode loaded an audit key that is not there")
		}
		assertNoNewKeyBytes(t, dir, before)
		if _, serr := os.Stat(filepath.Join(dir, "audit-signing.key")); !os.IsNotExist(serr) {
			t.Fatal("the refusal minted the missing audit key")
		}
	})

	t.Run("open_without_an_observation_refuses", func(t *testing.T) {
		dir, keys := genuineThreeKeyDataDir(t)
		dsn := filepath.Join(dir, "olivares.db")
		writeCompletedLocalControl(t, dir, dsn, selectedFor(t, allLocal(), keys))
		pub, err := acquireBootPublication(t.Context(), storeConfigForTest(dsn), dir)
		if err != nil {
			t.Fatal(err)
		}
		defer pub.Close()
		st, oerr := pub.Open(t.Context(), nil, coreengine.CustodyObservation{}, nil)
		if st != nil {
			_ = st.Close()
		}
		if oerr == nil {
			t.Fatal("a completed destination published with no custody measurement at all")
		}
		if !strings.Contains(oerr.Error(), "no observation of the custody it loaded") {
			t.Fatalf("the refusal does not name the missing measurement: %v", oerr)
		}
	})

	t.Run("malformed_local_control_refuses", func(t *testing.T) {
		dir, keys := genuineThreeKeyDataDir(t)
		writeCompletedLocalControl(t, dir, filepath.Join(dir, "olivares.db"), selectedFor(t, allLocal(), keys))
		anchor, _, err := opgate.AnchorForDataDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if werr := os.WriteFile(anchor.RecordPath(), []byte("{not a control}"), 0o600); werr != nil {
			t.Fatal(werr)
		}
		before := keyBytesCensus(t, dir)
		pub, err := acquireBootPublication(t.Context(), storeConfigForTest(filepath.Join(dir, "olivares.db")), dir)
		if pub != nil {
			pub.Close()
		}
		if err == nil {
			t.Fatal("a malformed local control was read as permission")
		}
		assertNoNewKeyBytes(t, dir, before)
	})
}

// storeConfigForTest is the destination configuration a SQLite boot freezes.
func storeConfigForTest(dsn string) store.Config {
	return store.Config{Engine: store.EngineSQLite, DSN: dsn}
}

// loadAndObserveForTest runs the strict enrolled load the requirement selects and
// builds the observation from the resulting key objects, exactly as boot() does.
func loadAndObserveForTest(t *testing.T, dir string) coreengine.CustodyObservation {
	t.Helper()
	log := bootGateTestLogger()
	auditKey, err := loadAuditSigningKey(dir, log, withEnrolledCustody())
	if err != nil {
		t.Fatalf("load the audit key: %v", err)
	}
	catalogKey, err := loadCatalogSigningKey(dir, log, withEnrolledCustody())
	if err != nil {
		t.Fatalf("load the catalog key: %v", err)
	}
	policyKey, err := loadPolicySigningKey(dir, log, withEnrolledCustody())
	if err != nil {
		t.Fatalf("load the policy key: %v", err)
	}
	obs, err := observeSelectedCustody(auditKey, catalogKey, policyKey)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	return obs
}

// TestGenuineUnenrolledFirstBootStillMints is the positive this whole sublot must not
// have broken: an installation with no control and no keys is the ordinary first boot,
// and it mints its three keys exactly as before.
func TestGenuineUnenrolledFirstBootStillMints(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "olivares.db")
	pub, err := acquireBootPublication(t.Context(), storeConfigForTest(dsn), dir)
	if err != nil {
		t.Fatalf("the admission refused an unenrolled installation: %v", err)
	}
	defer pub.Close()
	if pub.CustodyRequirement().CustodyRequired() {
		t.Fatal("an unenrolled destination demanded completed custody, so a genuine first boot could never mint")
	}

	log := bootGateTestLogger()
	auditKey, err := loadAuditSigningKey(dir, log)
	if err != nil {
		t.Fatal(err)
	}
	catalogKey, err := loadCatalogSigningKey(dir, log)
	if err != nil {
		t.Fatal(err)
	}
	policyKey, err := loadPolicySigningKey(dir, log)
	if err != nil {
		t.Fatal(err)
	}
	if !auditKey.created || !catalogKey.created || !policyKey.created {
		t.Fatalf("the first boot did not mint: audit=%t catalog=%t policy=%t", auditKey.created, catalogKey.created, policyKey.created)
	}
	for _, name := range keyFileNames {
		if _, serr := os.Stat(filepath.Join(dir, name)); serr != nil {
			t.Fatalf("the first boot did not create %s: %v", name, serr)
		}
	}
	obs, err := observeSelectedCustody(auditKey, catalogKey, policyKey)
	if err != nil {
		t.Fatal(err)
	}
	// The measurement is still built and it authorizes nothing: an unenrolled
	// destination has no completed control for it to satisfy, and nothing writes it
	// back as a retrospective enrolment.
	st, err := pub.Open(t.Context(), nil, obs, nil)
	if err != nil {
		t.Fatalf("the unenrolled first boot did not publish: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	anchor, _, aerr := opgate.AnchorForDataDir(dir)
	if aerr != nil {
		t.Fatal(aerr)
	}
	if _, serr := os.Stat(anchor.RecordPath()); !os.IsNotExist(serr) {
		t.Fatal("the unenrolled boot WROTE a control, turning not-knowing into retrospective proof")
	}
}

// TestExistingReadOnlyInstallationStillOpens keeps the read-only positive: an
// installation whose keys already exist and that carries no control opens without
// minting anything.
func TestExistingReadOnlyInstallationStillOpens(t *testing.T) {
	dir, _ := genuineThreeKeyDataDir(t)
	before := keyBytesCensus(t, dir)
	log := bootGateTestLogger()
	auditKey, err := loadAuditSigningKey(dir, log, withoutMinting())
	if err != nil {
		t.Fatalf("the read-only load refused an existing installation: %v", err)
	}
	if auditKey.created {
		t.Fatal("the read-only load minted over an existing key")
	}
	catalogKey, err := loadCatalogSigningKey(dir, log, withoutMinting())
	if err != nil {
		t.Fatal(err)
	}
	policyKey, err := loadPolicySigningKey(dir, log, withoutMinting())
	if err != nil {
		t.Fatal(err)
	}
	if _, oerr := observeSelectedCustody(auditKey, catalogKey, policyKey); oerr != nil {
		t.Fatalf("an existing read-only installation could not be observed: %v", oerr)
	}
	assertNoNewKeyBytes(t, dir, before)
}

// TestTheObservedAuditIdentityActuallySignsAndVerifies proves the observation's audit
// entry names an identity that actually verifies this process's signatures.
//
// The identity check alone is not enough: comparing a fingerprint to a fingerprint shows
// two values agree, not that either one can validate anything. So this signs through the
// real signer bound as the store's SignEvent — exactly as boot() binds it — and verifies
// the resulting ledger offline with audit.VerifyEvents under the observed identity, which
// is the check `audit verify --pubkey` performs.
//
// The negatives are what give the positive content: a foreign key must not validate those
// events, and a direct roundtrip must fail for a different message and for a different
// key. The inverted-argument assertion is deliberate — ed25519.Verify takes
// (publicKey, message, sig), and pinning that swapping the last two yields false keeps a
// future reader from mistaking an argument-order mistake for a library failure.
func TestTheObservedAuditIdentityActuallySignsAndVerifies(t *testing.T) {
	ctx := context.Background()
	dir, _ := genuineThreeKeyDataDir(t)
	log := bootGateTestLogger()

	auditKey, err := loadAuditSigningKey(dir, log, withEnrolledCustody())
	if err != nil {
		t.Fatalf("load the audit key: %v", err)
	}
	catalogKey, err := loadCatalogSigningKey(dir, log, withEnrolledCustody())
	if err != nil {
		t.Fatal(err)
	}
	policyKey, err := loadPolicySigningKey(dir, log, withEnrolledCustody())
	if err != nil {
		t.Fatal(err)
	}
	obs, err := observeSelectedCustody(auditKey, catalogKey, policyKey)
	if err != nil {
		t.Fatalf("observe the selected custody: %v", err)
	}

	// The REAL signer, from the same key object boot() hands to audit.NewSigner.
	signer, err := audit.NewSigner(auditKey.priv)
	if err != nil {
		t.Fatalf("construct the real audit signer: %v", err)
	}
	identity := signer.PublicKey()
	sum := sha256.Sum256(identity)
	if obs.Keys()[0].PublicSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("the observation does not name the real signer's public identity: %s vs %s",
			obs.Keys()[0].PublicSHA256, hex.EncodeToString(sum[:]))
	}

	// ── The production path: sign through the store, verify offline with the identity.
	//
	// SignEvent is bound exactly as boot() binds it, so every event the store appends
	// is signed by this signer, and audit.VerifyEvents is the offline verification an
	// operator runs with `audit verify --pubkey`.
	st, err := coreengine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", SignEvent: signer.SignEvent,
	}, nil)
	if err != nil {
		t.Fatalf("open a store bound to the observed audit signer: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, oerr := sys.CreateOrg(ctx, model.Org{
			Name: "r61-ir5-custody", Slug: "r61-ir5-custody", Status: model.StatusActive,
		})
		if oerr == nil {
			tenant = org.TenantID
		}
		return oerr
	}); err != nil {
		t.Fatalf("provision the tenant whose events this signer signs: %v", err)
	}

	foreignPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		rep, verr := audit.VerifyEvents(ctx, sc.Audit(), identity)
		if verr != nil {
			return verr
		}
		if !rep.OK || rep.Events == 0 || rep.Events != rep.Signed {
			t.Fatalf("the events this signer signed did not verify under the identity the observation names: %+v", rep)
		}
		// NEGATIVE: a DIFFERENT key must not validate them. Without this the positive
		// above would also pass for a verifier that accepted anything.
		bad, verr := audit.VerifyEvents(ctx, sc.Audit(), foreignPub)
		if verr != nil {
			return verr
		}
		if bad.OK {
			t.Fatal("a foreign public key validated events signed by the observed identity")
		}
		if bad.Reason != "event-sig-invalid" {
			t.Fatalf("wrong-key verification failed for the wrong reason: %+v", bad)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify the ledger: %v", err)
	}

	// ── The direct control, in the CORRECT argument order.
	message := []byte("r61-ir5 selected custody identity control")
	signature := ed25519.Sign(auditKey.priv, message)
	if !ed25519.Verify(identity, message, signature) {
		t.Fatal("the identity the observation names does not verify a signature made with the key it was derived from")
	}
	// NEGATIVE: a different message under the same identity.
	if ed25519.Verify(identity, []byte("a different message"), signature) {
		t.Fatal("a signature verified against a message it was not made over")
	}
	// NEGATIVE: the same message under a different identity.
	if ed25519.Verify(foreignPub, message, signature) {
		t.Fatal("a foreign public key verified this signature")
	}
	// THE RETRACTION CONTROL: swapped arguments are false, and that is the whole
	// content of the finding this lot withdrew. It is not evidence about the library.
	if ed25519.Verify(identity, signature, message) {
		t.Fatal("the inverted argument order unexpectedly verified; the retraction control no longer discriminates")
	}
}
