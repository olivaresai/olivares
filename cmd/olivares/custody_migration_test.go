// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/secure/kmswrap"
)

// Migrating custody to a different KEK — a different cloud, a different account,
// a different vault — is one ceremony expressed through the config, not a new
// command. OLIVARES_KEY_WRAP_OLD declares the identity that SEALED the envelope;
// OLIVARES_KEY_WRAP stays the identity everything is sealed under from now on.
//
// The reason it needs declaring at all is the reason it must not be guessed.
// Rotation authenticates the old envelope before inheriting its history, so it
// needs a wrapper for the KEK that actually wrapped it. The envelope records a
// provider and a key id, but those fields are NOT covered by the AEAD — only the
// purpose, public key and rotation history are. Deriving the identity from them
// would let anyone who can write the file redirect the ceremony, bearer token
// included, at a KMS endpoint of their choosing. So the operator declares it, and
// the cryptography checks the declaration.

// migrationFixture is the composition these tests share: an AWS fake that sealed
// the original envelope, an Azure fake that is the destination, and the _OLD
// namespace pointing back at the AWS one. Order matters — startRotatingVault runs
// second so OLIVARES_KEY_WRAP ends up naming the DESTINATION, while the AWS
// endpoint and credential globals it does not touch keep serving the old side.
func migrationFixture(t *testing.T) (kms *fakeRotatingKMS, vault *fakeRotatingVault, envPath string) {
	t.Helper()
	kms = startRotatingKMS(t)
	envPath = filepath.Join(t.TempDir(), "audit-signing.key.sealed")
	if out, err := runKeys(t, "wrap", "--mint", "--out", envPath); err != nil {
		t.Fatalf("sealing the ORIGINAL envelope under the aws fake: %v\n%s", err, out)
	}
	vault = startRotatingVault(t)
	return kms, vault, envPath
}

// declareAWSMigrationSource points the _OLD namespace at the AWS fake. The
// credentials and endpoint are deliberately left to the standard AWS_* variables
// here: a cross-PROVIDER move needs no separate principal, which is the case the
// prefixed overrides do not have to carry.
func declareAWSMigrationSource(t *testing.T) {
	t.Helper()
	t.Setenv(envKeyWrapOld, "aws-kms")
	t.Setenv("OLIVARES_KEY_WRAP_OLD_AWS_REGION", kekRegion)
	t.Setenv("OLIVARES_KEY_WRAP_OLD_AWS_KEY_ID", kekAlias)
}

// TestCrossProviderRewrapMigratesCustody is the ceremony itself: same signing
// key, new custodian, nothing an auditor pinned moves.
func TestCrossProviderRewrapMigratesCustody(t *testing.T) {
	_, vault, envPath := migrationFixture(t)
	before, err := secure.ReadSealedFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if before.Provider != kmswrap.ProviderAWS {
		t.Fatalf("fixture did not seal under aws: %q", before.Provider)
	}

	// CONTROL, and it runs FIRST so the feature cannot be mistaken for something
	// that already worked: without the declaration the ceremony is refused.
	if out, cerr := runKeys(t, "rewrap", "--in", envPath, "--yes"); cerr == nil {
		t.Fatalf("rewrap migrated providers with NO migration declared — then the declaration "+
			"is decorative and this test proves nothing\n%s", out)
	} else if !strings.Contains(cerr.Error(), kmswrap.ProviderAWS) || !strings.Contains(cerr.Error(), kmswrap.ProviderAzure) {
		t.Fatalf("the refusal names neither side of the mismatch, so it may be failing for an "+
			"unrelated reason: %v", cerr)
	}

	declareAWSMigrationSource(t)
	if out, rerr := runKeys(t, "rewrap", "--in", envPath, "--yes"); rerr != nil {
		t.Fatalf("cross-provider rewrap: %v\n%s", rerr, out)
	}

	after, err := secure.ReadSealedFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.Provider != kmswrap.ProviderAzure {
		t.Fatalf("migrated envelope still records %q, want %q", after.Provider, kmswrap.ProviderAzure)
	}
	if !strings.Contains(after.KeyID, vault.Server.URL) {
		t.Fatalf("migrated envelope key id %q does not name the destination vault", after.KeyID)
	}
	// The whole point of choosing rewrap over rotate: the verification surface is
	// untouched, so no auditor has to re-pin anything.
	if !bytes.Equal(after.PublicKey, before.PublicKey) {
		t.Fatal("the signing key changed during a rewrap migration — every pinned public key would have to move")
	}
	if after.Purpose != before.Purpose {
		t.Fatalf("purpose changed across the migration: %q -> %q", before.Purpose, after.Purpose)
	}

	// And it genuinely opens under the DESTINATION alone. Clearing the migration
	// namespace is what proves the old identity is no longer needed.
	t.Setenv(envKeyWrapOld, "")
	got, err := loadAuditSigningKeyAt(t, envPath)
	if err != nil {
		t.Fatalf("the migrated envelope does not open under the destination KEK alone: %v", err)
	}
	if got.mode != custodyModeCMEK {
		t.Fatalf("custody mode after migration = %q", got.mode)
	}
}

// TestCrossProviderRotateCarriesAuthenticatedHistory covers the other ceremony —
// move custodian AND retire the key — and its tamper arm is the one that matters:
// the new door must not reopen the laundering hole that made a second identity
// necessary in the first place.
func TestCrossProviderRotateCarriesAuthenticatedHistory(t *testing.T) {
	t.Run("history survives the migration", func(t *testing.T) {
		kms, _, envPath := migrationFixture(t)
		_ = kms
		gen1, err := secure.ReadSealedFile(envPath)
		if err != nil {
			t.Fatal(err)
		}
		declareAWSMigrationSource(t)
		if out, rerr := runKeys(t, "rotate", "--in", envPath, "--yes"); rerr != nil {
			t.Fatalf("cross-provider rotate: %v\n%s", rerr, out)
		}
		after, err := secure.ReadSealedFile(envPath)
		if err != nil {
			t.Fatal(err)
		}
		if after.Provider != kmswrap.ProviderAzure {
			t.Fatalf("rotated envelope records %q, want the destination", after.Provider)
		}
		if bytes.Equal(after.PublicKey, gen1.PublicKey) {
			t.Fatal("rotate did not mint a new key — this ceremony is supposed to RETIRE the old one")
		}
		if len(after.PriorPublicKeys) != 1 || !bytes.Equal(after.PriorPublicKeys[0], gen1.PublicKey) {
			t.Fatalf("the retired generation did not survive the migration as history: %d priors", len(after.PriorPublicKeys))
		}
	})

	t.Run("an edited history is refused, not migrated", func(t *testing.T) {
		_, _, envPath := migrationFixture(t)
		// File write, no KEK: append a public key of our own to the history and see
		// whether the migration launders it into a fresh, authentic-looking envelope.
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(envPath) //nolint:gosec // test fixture path
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
		if err := os.WriteFile(envPath, append(edited, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}

		declareAWSMigrationSource(t)
		out, rerr := runKeys(t, "rotate", "--in", envPath, "--yes")
		if rerr == nil {
			t.Fatalf("a migration sealed an EDITED rotation history into a new envelope — the "+
				"authentication that closed this hole has been bypassed by the migration path\n%s", out)
		}
		if !strings.Contains(rerr.Error(), "does not authenticate") {
			t.Fatalf("the refusal does not come from the custody authentication, so the migration "+
				"may be failing for an unrelated reason: %v", rerr)
		}
	})
}

// TestStaleMigrationDeclarationFailsClosed is the cost of resolving by
// declaration, made explicit: a variable left over from a finished migration
// breaks ordinary ceremonies. It has to break LOUDLY, name both sides, and never
// quietly fall back to the configured KEK.
func TestStaleMigrationDeclarationFailsClosed(t *testing.T) {
	kms := startRotatingKMS(t)
	_ = kms
	envPath := filepath.Join(t.TempDir(), "audit-signing.key.sealed")
	if out, err := runKeys(t, "wrap", "--mint", "--out", envPath); err != nil {
		t.Fatalf("keys wrap --mint: %v\n%s", err, out)
	}

	// A COMPLETE azure namespace — including the token, without which the parser
	// would reject before anything custody-related happened and the test would be
	// verifying the wrong failure — pointed at an address nothing serves.
	const deadVault = "https://vault-that-does-not-exist.invalid"
	t.Setenv(envKeyWrapOld, "azure-kv")
	t.Setenv("OLIVARES_KEY_WRAP_OLD_AZURE_VAULT_URL", deadVault)
	t.Setenv("OLIVARES_KEY_WRAP_OLD_AZURE_KEY_NAME", vaultKeyName)
	t.Setenv("OLIVARES_KEY_WRAP_OLD_AZURE_TOKEN", "test-bearer")

	for _, ceremony := range []string{"rewrap", "rotate"} {
		// `--yes` como en las otras seis llamadas de este fichero: `rewrap`/`rotate` sobrescriben
		// en sitio y exigen intencion explicita en sesion no interactiva. Sin el, la ceremonia se
		// para en la guarda de confirmacion y este test mide ESA negativa, no la que busca.
		out, err := runKeys(t, ceremony, "--in", envPath, "--yes")
		if err == nil {
			t.Fatalf("`keys %s` proceeded with a stale migration declaration — silently falling "+
				"back to the configured KEK is exactly what must never happen\n%s", ceremony, out)
		}
		// The discriminator: the refusal is the PROVIDER MISMATCH, which is decided
		// before any network call. Had the code actually dialed the declared vault,
		// this would be a connection error against an unroutable host instead.
		if !strings.Contains(err.Error(), kmswrap.ProviderAWS) || !strings.Contains(err.Error(), kmswrap.ProviderAzure) {
			t.Fatalf("`keys %s` did not refuse with a provider mismatch naming both sides: %v", ceremony, err)
		}
		if strings.Contains(err.Error(), deadVault) && strings.Contains(err.Error(), "dial") {
			t.Fatalf("`keys %s` reached the network before deciding: %v", ceremony, err)
		}
		if !strings.Contains(err.Error(), envKeyWrapOld) {
			t.Fatalf("`keys %s` refuses without naming %s, so an operator staring at this has no "+
				"way to know which variable to unset: %v", ceremony, envKeyWrapOld, err)
		}
	}
}

// TestUndeclaredMigrationIsRefusedWithAUsefulMessage is the ordinary operator
// mistake, and the one a migration feature is most likely to get wrong: someone
// points the engine at the new KEK, runs the ceremony, and has not declared where
// the envelope came from. Three things are being measured, because a refusal that
// gets any of them wrong is a different bug: it must REFUSE (not proceed, not
// panic), it must say enough to act on, and it must leave the envelope untouched.
func TestUndeclaredMigrationIsRefusedWithAUsefulMessage(t *testing.T) {
	_, _, envPath := migrationFixture(t)
	// Deliberately NOT declaring the source identity.
	before, err := os.ReadFile(envPath) //nolint:gosec // test fixture path
	if err != nil {
		t.Fatal(err)
	}

	for _, ceremony := range []string{"rewrap", "rotate"} {
		// Ver la nota de `--yes` mas arriba: sin el, la negativa medida es la de confirmacion.
		out, cerr := runKeys(t, ceremony, "--in", envPath, "--yes")
		if cerr == nil {
			t.Fatalf("`keys %s` proceeded across KEKs with no migration declared\n%s", ceremony, out)
		}
		msg := cerr.Error()
		// It has to name BOTH sides — which KEK wrote the envelope and which one is
		// being used to open it — or the operator cannot tell which end is wrong.
		if !strings.Contains(msg, kmswrap.ProviderAWS) || !strings.Contains(msg, kmswrap.ProviderAzure) {
			t.Fatalf("`keys %s` refuses without naming both KEKs: %v", ceremony, msg)
		}
		// And it has to name the remedy. A deny-closed with no way forward is how an
		// operator ends up reaching for --no-verify equivalents.
		if !strings.Contains(msg, envKeyWrapOld) {
			t.Fatalf("`keys %s` refuses without naming %s, so the operator is told no and not "+
				"told what to do: %v", ceremony, envKeyWrapOld, msg)
		}
		// A refusal is only deny-CLOSED if nothing moved. A half-migrated envelope
		// would be strictly worse than either outcome.
		after, rerr := os.ReadFile(envPath) //nolint:gosec // test fixture path
		if rerr != nil {
			t.Fatalf("the envelope is gone after a refused `keys %s`: %v", ceremony, rerr)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("`keys %s` modified the envelope on its way to refusing — a refusal that "+
				"half-writes custody is worse than either answer", ceremony)
		}
	}
}

// countingProxy fronts an existing fake KMS and counts the calls that reach it,
// so a two-endpoint test can prove WHICH identity served which half of the
// ceremony. Without per-endpoint attribution a same-provider migration test
// passes even when both sides are wired to the same place, which would make it
// evidence of nothing.
func countingProxy(t *testing.T, target string) (proxyURL string, calls *atomic.Int64) {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	calls = &atomic.Int64{}
	rp := httputil.NewSingleHostReverseProxy(u)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, calls
}

// TestSameProviderMigrationUsesEachIdentityForItsOwnHalf covers the half of the
// problem class that is not cross-provider at all: the same backend, a different
// account or region, which a single configuration also cannot express. It is also
// where the prefixed AWS credential and endpoint overrides earn their place.
func TestSameProviderMigrationUsesEachIdentityForItsOwnHalf(t *testing.T) {
	source := startRotatingKMS(t)
	sourceURL, sourceCalls := countingProxy(t, source.Server.URL)
	t.Setenv("AWS_ENDPOINT_URL_KMS", sourceURL)

	envPath := filepath.Join(t.TempDir(), "audit-signing.key.sealed")
	if out, err := runKeys(t, "wrap", "--mint", "--out", envPath); err != nil {
		t.Fatalf("sealing under the SOURCE account: %v\n%s", err, out)
	}
	sealedCalls := sourceCalls.Load()
	if sealedCalls == 0 {
		t.Fatal("the source endpoint served no call while sealing — the fixture is not wired")
	}

	// A second AWS identity: its own endpoint, its own principal. startRotatingKMS
	// repoints the standard AWS_* globals at itself, which is exactly what a
	// destination account looks like to the current namespace.
	dest := startRotatingKMS(t)
	destURL, destCalls := countingProxy(t, dest.Server.URL)
	t.Setenv("AWS_ENDPOINT_URL_KMS", destURL)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIA-destination")

	t.Setenv(envKeyWrapOld, "aws-kms")
	t.Setenv("OLIVARES_KEY_WRAP_OLD_AWS_REGION", kekRegion)
	t.Setenv("OLIVARES_KEY_WRAP_OLD_AWS_KEY_ID", kekAlias)
	t.Setenv("OLIVARES_KEY_WRAP_OLD_AWS_ENDPOINT_URL_KMS", sourceURL)
	t.Setenv("OLIVARES_KEY_WRAP_OLD_AWS_ACCESS_KEY_ID", "AKIA-source")

	beforeSource, beforeDest := sourceCalls.Load(), destCalls.Load()
	if out, err := runKeys(t, "rewrap", "--in", envPath, "--yes"); err != nil {
		t.Fatalf("same-provider cross-identity rewrap: %v\n%s", err, out)
	}

	// The assertion that discriminates: the OPEN half landed on the source
	// endpoint and the SEAL half on the destination. Wire openCfg to the wrong
	// identity and one of these counters stays put.
	if got := sourceCalls.Load() - beforeSource; got == 0 {
		t.Fatal("the source endpoint served nothing during the migration — the open side did not " +
			"use the declared migration identity")
	}
	if got := destCalls.Load() - beforeDest; got == 0 {
		t.Fatal("the destination endpoint served nothing during the migration — the seal side did " +
			"not use the configured KEK")
	}
}

// TestBootIgnoresTheMigrationNamespace is the containment property: a migration
// source in the environment is a CEREMONY input, and a running engine must keep
// exactly one custody root whatever the environment says.
func TestBootIgnoresTheMigrationNamespace(t *testing.T) {
	_, _, envPath := migrationFixture(t)
	declareAWSMigrationSource(t)

	// The envelope is an AWS one and the configured KEK is Azure. A ceremony would
	// now open it, because a migration is declared. The boot path must not.
	if _, err := loadAuditSigningKeyAt(t, envPath); err == nil {
		t.Fatal("the boot key load opened an envelope through the MIGRATION identity — the runtime " +
			"would then have two custody roots, and anyone who can set an environment variable " +
			"could hand a live engine an envelope from a KEK the operator never configured")
	}

	// Control: the same envelope, same environment, opens fine for the CEREMONY.
	// Without this the test above would also pass if the envelope were simply
	// unopenable by anyone.
	if out, err := runKeys(t, "rewrap", "--in", envPath, "--yes"); err != nil {
		t.Fatalf("the ceremony cannot open it either, so the boot refusal proves nothing: %v\n%s", err, out)
	}
}

// loadAuditSigningKeyAt resolves the audit signing key from a specific envelope
// through the ordinary boot loader.
func loadAuditSigningKeyAt(t *testing.T, envPath string) (loadedSigningKey, error) {
	t.Helper()
	t.Setenv(envAuditWrapped, envPath)
	return loadAuditSigningKey(t.TempDir(), discardLog())
}

// TestMigrationNamespaceParsing pins the parser refactor. The messages are the
// subject, not an afterthought: templating the namespace into them is the only
// thing that tells an operator which of two near-identical variables is wrong.
func TestMigrationNamespaceParsing(t *testing.T) {
	t.Run("absent means no migration", func(t *testing.T) {
		t.Setenv(envKeyWrapOld, "")
		cfg, err := loadOldKeyWrapConfig()
		if cfg != nil || err != nil {
			t.Fatalf("undeclared = (%v, %v), want (nil, nil)", cfg, err)
		}
	})
	t.Run("an unknown kind is an error, never 'no migration'", func(t *testing.T) {
		t.Setenv(envKeyWrapOld, "hsm-magic")
		if _, err := loadOldKeyWrapConfig(); err == nil {
			t.Fatal("an unknown migration backend was accepted — a custody typo must never silently mean no custody")
		}
	})
	t.Run("an incomplete namespace names its OWN variables", func(t *testing.T) {
		t.Setenv(envKeyWrapOld, "aws-kms")
		t.Setenv("OLIVARES_KEY_WRAP_OLD_AWS_REGION", "")
		t.Setenv("OLIVARES_KEY_WRAP_OLD_AWS_KEY_ID", "")
		_, err := loadOldKeyWrapConfig()
		if err == nil {
			t.Fatal("an incomplete migration namespace parsed clean")
		}
		if !strings.Contains(err.Error(), "OLIVARES_KEY_WRAP_OLD_AWS_REGION") {
			t.Fatalf("the error names a variable the operator does not have to fix: %v", err)
		}
	})
	t.Run("the configured namespace still names ITS variables", func(t *testing.T) {
		t.Setenv(envKeyWrap, "azure-kv")
		// The token has to be present or the parser stops on IT, and this subtest
		// would be checking the wrong error's wording.
		t.Setenv("OLIVARES_KEY_WRAP_AZURE_TOKEN", "test-bearer")
		t.Setenv("OLIVARES_KEY_WRAP_AZURE_VAULT_URL", "")
		t.Setenv("OLIVARES_KEY_WRAP_AZURE_KEY_NAME", "")
		_, err := loadKeyWrapConfig()
		if err == nil {
			t.Fatal("an incomplete configured namespace parsed clean")
		}
		if !strings.Contains(err.Error(), "OLIVARES_KEY_WRAP_AZURE_VAULT_URL") ||
			strings.Contains(err.Error(), envKeyWrapOld) {
			t.Fatalf("the configured namespace reported the migration namespace's names: %v", err)
		}
	})
}

// TestAWSCredentialOverridePrecedence is a unit on the lookup itself. The fake
// KMS does not validate signatures, so which principal signed a call is not
// observable over the wire here — that limit is stated rather than papered over
// with a test that would pass either way.
func TestAWSCredentialOverridePrecedence(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIA-standard")
	c := &keyWrapConfig{kind: "aws-kms", envPrefix: envKeyWrapOld}

	if got := c.awsEnv("_AWS_ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID"); got != "AKIA-standard" {
		t.Fatalf("with no override the standard variable must win, got %q", got)
	}
	t.Setenv("OLIVARES_KEY_WRAP_OLD_AWS_ACCESS_KEY_ID", "AKIA-migration")
	if got := c.awsEnv("_AWS_ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID"); got != "AKIA-migration" {
		t.Fatalf("the namespaced override must win over the standard variable, got %q", got)
	}
	// An identity with no namespace (constructed rather than parsed) must not
	// invent one and read a bare "_AWS_ACCESS_KEY_ID".
	bare := &keyWrapConfig{kind: "aws-kms"}
	if got := bare.awsEnv("_AWS_ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID"); got != "AKIA-standard" {
		t.Fatalf("a config with no namespace resolved to %q", got)
	}
}
