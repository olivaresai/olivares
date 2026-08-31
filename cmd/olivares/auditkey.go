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
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/secure"
)

// Env vars that externalize a signing key from the per-node data dir so an
// active-passive HA cluster shares ONE key. They mirror the
// OLIVARES_LEDGER_* precedent of buildCheckpointKey: an inline base64 value, or a
// path to a mounted Secret (preferred — the value never lands in the process
// environment or a `ps`/`/proc` dump). Adds the third, CMEK form: a path to
// a SEALED ENVELOPE (`keys wrap`) whose payload only the customer-managed KEK in
// an external KMS can unwrap — at rest the key is never in clear anywhere.
const (
	// envAuditKey / envAuditKeyFile externalize the per-event audit signing key —
	// the one whose coupling to the node disk made >1 replica impossible. Sharing it
	// is REQUIRED for HA: without it each pod mints its own key and the ledger forks
	// at failover.
	envAuditKey     = "OLIVARES_AUDIT_SIGNING_KEY"
	envAuditKeyFile = "OLIVARES_AUDIT_SIGNING_KEY_FILE"
	// envCatalogKey / envCatalogKeyFile externalize the INDEPENDENT catalog
	// (artifact) signing key. Sharing it is OPTIONAL — it is not ledger integrity —
	// but recommended in HA so every replica signs/verifies approved registry
	// entries identically.
	envCatalogKey     = "OLIVARES_CATALOG_SIGNING_KEY"
	envCatalogKeyFile = "OLIVARES_CATALOG_SIGNING_KEY_FILE"
	// envPolicyKey / envPolicyKeyFile externalize the INDEPENDENT policy-artifact
	// signing key — the key the claude-policy distributor signs published
	// managed-* artifacts with, which pull agents verify. Same optional-sharing
	// stance as the catalog key: not ledger integrity, but share it in HA so every
	// replica signs/serves identical artifacts (and pinned fingerprints hold).
	envPolicyKey     = "OLIVARES_POLICY_SIGNING_KEY"
	envPolicyKeyFile = "OLIVARES_POLICY_SIGNING_KEY_FILE"
)

// Custody modes a signing key can actually load under (the DECLARED posture in
// OLIVARES_KEY_CUSTODY is validated against these — custody.go).
const (
	custodyModeMinted   = "minted"    // mint-on-first-boot in the data dir (single-node/dev)
	custodyModeBYOKEnv  = "byok-env"  // customer-provisioned inline value
	custodyModeBYOKFile = "byok-file" // customer-provisioned mounted Secret
	custodyModeCMEK     = "cmek"      // sealed envelope under the customer's KMS KEK
)

// loadedSigningKey is the result of resolving a signing key: the key, how it was
// custodied, and — for CMEK envelopes — the non-secret KEK reference and the
// rotation history (prior public keys) verification consumes.
type loadedSigningKey struct {
	priv    ed25519.PrivateKey
	created bool   // a new key was minted (back it up)
	mode    string // custodyMode*
	kek     string // non-secret KEK description (cmek only)
	priors  []ed25519.PublicKey
	// createdAt is known for a newly minted local key and for a CMEK envelope.
	// Existing plain BYOK/local files do not carry trustworthy creation metadata.
	createdAt time.Time
}

// fromCMEKEnvelope retains every non-secret custody field authenticated by the
// envelope. Only well-formed Ed25519 prior keys enter the verification-history
// count, matching the audit verifier's existing behavior.
func fromCMEKEnvelope(priv ed25519.PrivateKey, env *secure.SealedEnvelope) loadedSigningKey {
	out := loadedSigningKey{
		priv:      priv,
		mode:      custodyModeCMEK,
		kek:       env.Provider + " " + env.KeyID,
		createdAt: env.CreatedAt.UTC(),
	}
	for _, prior := range env.PriorPublicKeys {
		if len(prior) == ed25519.PublicKeySize {
			out.priors = append(out.priors, ed25519.PublicKey(prior))
		}
	}
	return out
}

func mintedSigningKey(priv ed25519.PrivateKey, created bool) loadedSigningKey {
	out := loadedSigningKey{priv: priv, created: created, mode: custodyModeMinted}
	if created {
		out.createdAt = time.Now().UTC()
	}
	return out
}

// custodyInfo projects a loaded signing key into the public, non-secret API
// inventory. The fingerprint is the FULL SHA-256 of the raw public key; private
// material has no representation in api.KeyInfo.
func (k loadedSigningKey) custodyInfo(purpose string) api.KeyInfo {
	info := api.KeyInfo{
		Purpose:     purpose,
		Algorithm:   "ed25519",
		CustodyMode: k.mode,
		KEK:         k.kek,
		PriorCount:  len(k.priors),
	}
	if !k.createdAt.IsZero() {
		info.Created = k.createdAt.UTC().Format(time.RFC3339)
	}
	if len(k.priv) == ed25519.PrivateKeySize {
		publicKey := k.priv.Public().(ed25519.PublicKey)
		sum := sha256.Sum256(publicKey)
		info.PublicKey = base64.StdEncoding.EncodeToString(publicKey)
		info.Fingerprint = hex.EncodeToString(sum[:])
	}
	return info
}

func sealerCustodyInfo(purpose, envValue string, present bool) api.KeyInfo {
	source := "file"
	if strings.TrimSpace(envValue) != "" {
		source = "env"
	}
	return api.KeyInfo{Purpose: purpose, Source: source, Present: present}
}

// keyLoadOption tunes how a signing key is resolved.
type keyLoadOption func(*keyLoadOptions)

type keyLoadOptions struct{ noMint bool }

func applyKeyLoadOptions(opts []keyLoadOption) keyLoadOptions {
	var o keyLoadOptions
	for _, apply := range opts {
		apply(&o)
	}
	return o
}

// withoutMinting refuses the mint-on-absent fallback: an absent key is reported
// instead of created. It is what a READ-ONLY command passes. Reading a
// ledger must not leave a private key behind in whatever directory the operator
// happened to run the command from — `olivares sources ls`, which prints one
// line, was minting three signing keys and a 6 MB store into ./olivares-data.
//
// This makes an uninitialised data directory an error for a read-only command
// rather than something a read silently repairs. That is the point: repairing an
// installation is a job for `serve`/`quickstart`, which say what they are doing.
func withoutMinting() keyLoadOption { return func(o *keyLoadOptions) { o.noMint = true } }

// errNoMint reports an absent key under withoutMinting, as a NotFound so the
// caller's exit code says "there is nothing here", not "something broke".
func errNoMint(purpose, path string) error {
	return exitcode.New(exitcode.NotFound, fmt.Errorf(
		"no %s signing key at %s: this is not an initialized Olivares data directory, "+
			"and a read-only command never mints one — run `olivares quickstart`, or point "+
			"--data-dir (or OLIVARES_DATA_DIR) at the installation", purpose, path))
}

// fileExistsAt reports whether path names an existing regular file.
func fileExistsAt(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// loadAuditSigningKey resolves the per-event audit signing key, FAIL-CLOSED for
// every shared/custodied source (minting is only ever the no-config single-node
// fallback — a forked or vendor-re-mintable ledger key is exactly what BYOK/CMEK
// buyers exclude):
//
//  1. envAuditWrapped (CMEK): a sealed envelope opened through the
//     customer's KEK (OLIVARES_KEY_WRAP). Missing file, missing KEK config or a
//     failed unwrap (e.g. the customer REVOKED the KEK) refuses the boot.
//  2. envAuditKey / envAuditKeyFile (BYOK): customer-provisioned value or
//     mounted Secret, never minted here.
//  3. Mint-on-first-boot in the data dir (the honest single-node / dev mode).
//
// Configuring both the CMEK and a BYOK source is an ERROR, not a precedence
// guess: two declared custody sources for the ledger key means the operator's
// intent is ambiguous, and custody is the one place a guess is unacceptable.

func loadAuditSigningKey(dataDir string, log *slog.Logger, opts ...keyLoadOption) (loadedSigningKey, error) {
	wrapped := strings.TrimSpace(os.Getenv(envAuditWrapped))
	inline := strings.TrimSpace(os.Getenv(envAuditKey))
	file := strings.TrimSpace(os.Getenv(envAuditKeyFile))

	if wrapped != "" && (inline != "" || file != "") {
		return loadedSigningKey{}, fmt.Errorf("both %s and %s/%s are set — declare ONE custody source for the audit signing key", envAuditWrapped, envAuditKey, envAuditKeyFile)
	}
	if wrapped != "" {
		k, env, err := loadSealedKey(wrapped, secure.PurposeAuditSigningKey)
		if err != nil {
			return loadedSigningKey{}, fmt.Errorf("audit signing key (CMEK): %w", err)
		}
		out := fromCMEKEnvelope(k, env)
		log.Info("audit signing key unwrapped from a sealed envelope (CMEK custody); per-event signing stays on-box, the key at rest only exists KEK-wrapped",
			"path", wrapped, "kek", out.kek, "prior_generations", len(out.priors))
		return out, nil
	}
	if inline != "" {
		k, derr := secure.DecodeSigningKey(inline)
		if derr != nil {
			return loadedSigningKey{}, fmt.Errorf("%s: %w", envAuditKey, derr)
		}
		log.Info("audit signing key loaded from environment (shared-custody / HA); per-event signing is on-box on every replica", "source", envAuditKey)
		return loadedSigningKey{priv: k, mode: custodyModeBYOKEnv}, nil
	}
	if file != "" {
		k, lerr := secure.LoadSigningKey(file)
		if lerr != nil {
			return loadedSigningKey{}, lerr
		}
		log.Info("audit signing key loaded from a mounted file (shared-custody / HA)", "path", file)
		return loadedSigningKey{priv: k, mode: custodyModeBYOKFile}, nil
	}
	// No shared source: single-node / dev. Mint-on-absent in the data dir, exactly
	// as before. >1 replica is rejected by the Helm chart in this mode.
	path := filepath.Join(dataDir, "audit-signing.key")
	if applyKeyLoadOptions(opts).noMint && !fileExistsAt(path) {
		return loadedSigningKey{}, errNoMint("audit", path)
	}
	k, created, err := secure.LoadOrCreateSigningKey(path)
	if err != nil {
		return loadedSigningKey{}, err
	}
	return mintedSigningKey(k, created), nil
}

// loadCatalogSigningKey resolves the catalog (artifact) signing key. The plain
// shared-file path keeps LENIENT semantics (a configured-but-absent file
// mints per-node with a loud warning — that is what lets the chart default the
// catalog Secret to the audit Secret without forcing catalog-signing.key into
// it). The CMEK envelope path is FAIL-CLOSED like the audit key's: a sealed
// envelope is an explicit per-key opt-in, never a chart default, so an absent or
// un-unwrappable envelope is a custody error — silently minting a plaintext key
// under a declared customer-custody posture would be a lie.
func loadCatalogSigningKey(dataDir string, log *slog.Logger, opts ...keyLoadOption) (loadedSigningKey, error) {
	wrapped := strings.TrimSpace(os.Getenv(envCatalogWrapped))
	inline := strings.TrimSpace(os.Getenv(envCatalogKey))
	file := strings.TrimSpace(os.Getenv(envCatalogKeyFile))

	if wrapped != "" && (inline != "" || file != "") {
		return loadedSigningKey{}, fmt.Errorf("both %s and %s/%s are set — declare ONE custody source for the catalog signing key", envCatalogWrapped, envCatalogKey, envCatalogKeyFile)
	}
	if wrapped != "" {
		k, env, err := loadSealedKey(wrapped, secure.PurposeCatalogSigningKey)
		if err != nil {
			return loadedSigningKey{}, fmt.Errorf("catalog signing key (CMEK): %w", err)
		}
		log.Info("catalog signing key unwrapped from a sealed envelope (CMEK custody)", "path", wrapped, "kek", env.Provider+" "+env.KeyID)
		return fromCMEKEnvelope(k, env), nil
	}
	if inline != "" {
		k, derr := secure.DecodeSigningKey(inline)
		if derr != nil {
			return loadedSigningKey{}, fmt.Errorf("%s: %w", envCatalogKey, derr)
		}
		log.Info("catalog signing key loaded from environment (shared-custody / HA)", "source", envCatalogKey)
		return loadedSigningKey{priv: k, mode: custodyModeBYOKEnv}, nil
	}
	if file != "" {
		if _, statErr := os.Stat(file); statErr == nil {
			k, lerr := secure.LoadSigningKey(file)
			if lerr != nil {
				return loadedSigningKey{}, lerr
			}
			log.Info("catalog signing key loaded from a mounted file (shared-custody / HA)", "path", file)
			return loadedSigningKey{priv: k, mode: custodyModeBYOKFile}, nil
		}
		// Configured but ABSENT. Unlike the audit key — whose absence MUST fail closed
		// (a forked ledger is catastrophic) — the catalog key is artifact-signing with
		// an honest unpinned fallback, so a missing shared key mints a per-node key with
		// a loud warning rather than failing boot. This is what lets the chart default
		// the catalog Secret to the audit Secret WITHOUT forcing the operator to also
		// place catalog-signing.key in it (a single-key audit Secret still boots).
		log.Warn("catalog signing key file not present; minting a per-node catalog key — provision catalog-signing.key in the shared Secret for consistent artifact verification across HA replicas", "path", file)
	}
	path := filepath.Join(dataDir, "catalog-signing.key")
	if applyKeyLoadOptions(opts).noMint && !fileExistsAt(path) {
		return loadedSigningKey{}, errNoMint("catalog", path)
	}
	k, created, err := secure.LoadOrCreateSigningKey(path)
	if err != nil {
		return loadedSigningKey{}, err
	}
	return mintedSigningKey(k, created), nil
}

// loadPolicySigningKey resolves the policy-artifact signing key — the key
// the claude-policy distributor signs published managed-* artifacts with, which
// pull agents verify against a pinned fingerprint. It mirrors the catalog key's
// semantics exactly: artifact signing with an honest fallback, so the plain
// shared-file path is LENIENT (configured-but-absent mints per-node with a loud
// warning) while the CMEK envelope path is FAIL-CLOSED (a declared customer-
// custody posture is never silently downgraded to a minted plaintext key).
func loadPolicySigningKey(dataDir string, log *slog.Logger, opts ...keyLoadOption) (loadedSigningKey, error) {
	wrapped := strings.TrimSpace(os.Getenv(envPolicyWrapped))
	inline := strings.TrimSpace(os.Getenv(envPolicyKey))
	file := strings.TrimSpace(os.Getenv(envPolicyKeyFile))

	if wrapped != "" && (inline != "" || file != "") {
		return loadedSigningKey{}, fmt.Errorf("both %s and %s/%s are set — declare ONE custody source for the policy signing key", envPolicyWrapped, envPolicyKey, envPolicyKeyFile)
	}
	if wrapped != "" {
		k, env, err := loadSealedKey(wrapped, secure.PurposePolicySigningKey)
		if err != nil {
			return loadedSigningKey{}, fmt.Errorf("policy signing key (CMEK): %w", err)
		}
		log.Info("policy signing key unwrapped from a sealed envelope (CMEK custody)", "path", wrapped, "kek", env.Provider+" "+env.KeyID)
		return fromCMEKEnvelope(k, env), nil
	}
	if inline != "" {
		k, derr := secure.DecodeSigningKey(inline)
		if derr != nil {
			return loadedSigningKey{}, fmt.Errorf("%s: %w", envPolicyKey, derr)
		}
		log.Info("policy signing key loaded from environment (shared-custody / HA)", "source", envPolicyKey)
		return loadedSigningKey{priv: k, mode: custodyModeBYOKEnv}, nil
	}
	if file != "" {
		if _, statErr := os.Stat(file); statErr == nil {
			k, lerr := secure.LoadSigningKey(file)
			if lerr != nil {
				return loadedSigningKey{}, lerr
			}
			log.Info("policy signing key loaded from a mounted file (shared-custody / HA)", "path", file)
			return loadedSigningKey{priv: k, mode: custodyModeBYOKFile}, nil
		}
		// Configured but ABSENT: lenient like the catalog key (artifact signing
		// with an honest fallback, never ledger integrity) — mint per-node loudly.
		log.Warn("policy signing key file not present; minting a per-node policy key — provision policy-signing.key in the shared Secret so pull agents verify one pinned fingerprint across HA replicas", "path", file)
	}
	path := filepath.Join(dataDir, "policy-signing.key")
	if applyKeyLoadOptions(opts).noMint && !fileExistsAt(path) {
		return loadedSigningKey{}, errNoMint("policy", path)
	}
	k, created, err := secure.LoadOrCreateSigningKey(path)
	if err != nil {
		return loadedSigningKey{}, err
	}
	return mintedSigningKey(k, created), nil
}

// loadSealedKey opens a sealed signing-key envelope through the configured KEK.
// Every failure is closed: no KEK configured, unreadable envelope, KMS unwrap
// refused (revoked KEK), or an inconsistent custody record.
func loadSealedKey(path, purpose string) (ed25519.PrivateKey, *secure.SealedEnvelope, error) {
	cfg, err := loadKeyWrapConfig()
	if err != nil {
		return nil, nil, err
	}
	if cfg == nil {
		return nil, nil, fmt.Errorf("a sealed envelope is configured at %s but no KEK is (%s) — the envelope cannot be opened without the customer-managed key", path, envKeyWrap)
	}
	e, err := secure.ReadSealedFile(path)
	if err != nil {
		return nil, nil, err
	}
	w, err := cfg.wrapperFor(e)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), kmsCallTimeout)
	defer cancel()
	k, err := e.OpenSigningKey(ctx, w, purpose)
	if err != nil {
		return nil, nil, err
	}
	return k, e, nil
}
