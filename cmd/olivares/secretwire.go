// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/connectors/secretref"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/modules/knowledge"
	"github.com/olivaresai/olivares/sdk"
)

// secretwire.go is the composition-root half of the runtime secret store and
// reference resolver. It builds (1) the AES-256-GCM sealer the store seals secret
// values under at rest (the federationwire/eventingwire pattern, a dedicated key),
// and (2) the resolver that turns a `<scheme>:<locator>` config reference into a
// live value at Open — env/file/the sealed store built in core, the external
// backends (vault + the cloud secret managers) from the connectors/secretref
// readers, each configured from the engine environment.
//
// Resolution is wired into EVERY Config-with-secrets path: the in-process and
// plugin sources, the identity roster, the knowledge document sources, the notify
// destinations and the Claude managed-agents thread readers — so the literal
// secret never has to live by value in the operator's config file.

const (
	// secretStoreKeyEnv supplies the 32-byte base64 sealer key directly (the HA
	// path: every node must seal/open with the SAME key). Unset => a per-node key
	// file in the data dir.
	secretStoreKeyEnv = "OLIVARES_SECRET_STORE_KEY"
	// secretStoreKeyFile is the on-disk key minted on first boot (0600,
	// fail-closed on wider permissions — the shared secure-package posture).
	secretStoreKeyFile = "secret-store.key"
)

// newSecretSealer builds the AES-256-GCM sealer for the runtime secret store over
// the engine-held key (env HA-shared key or a 0600 data-dir key file minted on
// first boot). Fail-closed: a malformed env key or an over-permissive key file is
// an error, never a silent downgrade. It mirrors newFederationSealer with a
// distinct key and AAD purpose, so a sealed store secret can never be opened as
// an SSO secret.
func newSecretSealer(dataDir string, getenv func(string) string) (auth.SecretSealer, error) {
	var key []byte
	if raw := strings.TrimSpace(getenv(secretStoreKeyEnv)); raw != "" {
		k, err := base64.StdEncoding.DecodeString(raw)
		if err != nil || len(k) != 32 {
			return nil, fmt.Errorf("secret-store: %s must be 32 base64-encoded bytes", secretStoreKeyEnv)
		}
		key = k
	} else {
		k, err := loadOrCreateAEADKey(filepath.Join(dataDir, secretStoreKeyFile))
		if err != nil {
			return nil, err
		}
		key = k
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secret-store: sealer cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret-store: sealer aead: %w", err)
	}
	return &secretStoreSealer{aead: aead}, nil
}

// secretStoreSealer is AES-256-GCM with the scope bound as AAD, so a sealed secret
// cannot be replayed across scopes. The sealed form is versioned ("v1:" +
// base64(nonce||ciphertext)).
type secretStoreSealer struct{ aead cipher.AEAD }

const secretStoreSealPrefix = "v1:"

func (s *secretStoreSealer) aad(scope model.TenantID) []byte {
	return []byte("secret.store.value|" + scope.String())
}

func (s *secretStoreSealer) Seal(_ context.Context, scope model.TenantID, plaintext []byte) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secret-store: seal nonce: %w", err)
	}
	ct := s.aead.Seal(nil, nonce, plaintext, s.aad(scope))
	return secretStoreSealPrefix + base64.StdEncoding.EncodeToString(append(nonce, ct...)), nil
}

func (s *secretStoreSealer) Open(_ context.Context, scope model.TenantID, sealed string) ([]byte, error) {
	raw, ok := strings.CutPrefix(sealed, secretStoreSealPrefix)
	if !ok {
		return nil, fmt.Errorf("secret-store: unknown sealed-secret version")
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(b) < s.aead.NonceSize() {
		return nil, fmt.Errorf("secret-store: malformed sealed secret")
	}
	nonce, ct := b[:s.aead.NonceSize()], b[s.aead.NonceSize():]
	pt, err := s.aead.Open(nil, nonce, ct, s.aad(scope))
	if err != nil {
		return nil, fmt.Errorf("secret-store: sealed secret does not open (wrong key or scope)")
	}
	return pt, nil
}

// storeSecretHandler resolves a `store:<name>` (or `db:<name>`) reference to the
// opened value from the sealed runtime secret store, at the global scope (the only
// scope resolves). It satisfies secret.Handler.
type storeSecretHandler struct{ store *auth.SecretStore }

func (h storeSecretHandler) Resolve(ctx context.Context, locator string) ([]byte, error) {
	return h.store.Resolve(ctx, auth.GlobalSecretScope, locator)
}

// newSecretResolver assembles the reference resolver over every wired backend:
// the env and file built-ins, the sealed store (when a store is available — it is
// nil in the storeless collector, where a `store:` reference then fails closed),
// and the external backends (vault + cloud secret managers) the secretref readers
// expose for whichever are configured in the environment. The secretref readers'
// Resolve shape is identical to secret.Handler, so they satisfy it directly.
func newSecretResolver(secretStore *auth.SecretStore, getenv func(string) string, log *slog.Logger) *secret.Resolver {
	// The env handler reads through the engine's getenv (osGetenv in production) so
	// the resolver and the rest of the engine see the SAME environment. A plain
	// getenv cannot tell "unset" from "set empty", so an empty value is treated as
	// absent — either way fail-closed (a referenced env secret must have a value).
	envLookup := func(k string) (string, bool) {
		v := getenv(k)
		return v, v != ""
	}
	handlers := map[string]secret.Handler{
		secret.SchemeEnv:  secret.EnvHandler{Lookup: envLookup},
		secret.SchemeFile: secret.FileHandler{},
	}
	if secretStore != nil {
		handlers[secret.SchemeStore] = storeSecretHandler{store: secretStore}
	}
	for scheme, reader := range secretref.Handlers(getenv, nil, log) {
		handlers[scheme] = reader
	}
	return secret.NewResolver(handlers)
}

// resolveConfig resolves a connector's config references to live values, applying
// the strict no-inline-secret check against the connector's declared secret
// fields. A nil resolver (only the e2e harness, which wires none) passes the
// config through unchanged. desc may be the zero value for a source whose
// descriptor is not known in-process (an out-of-process plugin) — references still
// resolve, the strict check is skipped.
func resolveConfig(ctx context.Context, r *secret.Resolver, desc sdk.Descriptor, cfg sdk.Config) (sdk.Config, error) {
	if r == nil {
		return cfg, nil
	}
	return r.Resolve(ctx, desc, cfg)
}

// --- deferred opens ---------------------------------------------------

// pendingContentSource is a knowledge document source constructed before the
// store existed; boot resolves its config references and Opens it once the store
// (and thus a `store:` reference) is available, then registers it on the module.
type pendingContentSource struct {
	name   string
	kind   string
	src    contentsource.Source
	cfg    sdk.Config
	plugin *externalPluginSpec
	digest string
}

// deferredSecretWiring holds the connectors whose secret-bearing config must be
// resolved and opened AFTER the store exists (they are constructed in buildModules,
// before the store, so their `store:` references can only resolve post-store).
type deferredSecretWiring struct {
	content   []pendingContentSource
	knowledge *knowledge.Module // the module the opened document sources register on
	rt        *runtime.Runtime
	// connectorDir is boot's scratch dir for firstparty.Extract — handed to the
	// notify dispatcher so out-of-process destination kinds (outputPluginForKind)
	// can extract and launch their embedded plugin binaries (E5).
	connectorDir string
	notify       *connectorDispatcher
	claude       *claudeThreadEventProvider
}

// openAll resolves every deferred connector's config references and opens it, now
// that the store (and the resolver's store handler) exists. A connector whose
// secret cannot resolve, or that fails to open, is logged and SKIPPED — a single
// misconfigured destination/source never aborts boot, and its secret error is
// never logged (it could embed the value). A document source is registered on the
// knowledge module ONLY after it opens (the "only openable sources are wired"
// contract). It runs before rt.Start.
func (d *deferredSecretWiring) openAll(ctx context.Context, r *secret.Resolver, log *slog.Logger) {
	if d == nil {
		return
	}
	for _, p := range d.content {
		if p.plugin != nil {
			resolved, err := resolveConfig(ctx, r, sdk.Descriptor{}, p.cfg)
			if err != nil {
				log.Warn("knowledge: external content-source plugin secret reference could not be resolved; source NOT wired", "name", p.name)
				continue
			}
			if d.rt == nil {
				log.Warn("knowledge: external content-source plugin has no runtime loader; source NOT wired", "name", p.name)
				continue
			}
			raw, err := d.rt.LoadContentSourcePluginVerified(p.plugin.Path, resolved, "", p.digest)
			if err != nil {
				log.Warn("knowledge: failed to load external content-source plugin; source not wired", "name", p.name, "error", err)
				continue
			}
			src := wrapContentSourceMode(wrapSDKContentSource(raw), p.cfg.Settings["mode"])
			if d.knowledge != nil {
				d.knowledge.AddSource(p.name, src)
			}
			log.Info("knowledge: wired EXTERNAL document source (signature verified, checksum-pinned, out-of-process AutoMTLS)", "name", p.name, "digest", p.digest)
			continue
		}
		resolved, err := resolveConfig(ctx, r, p.src.Descriptor(), p.cfg)
		if err != nil {
			log.Warn("knowledge: document source secret reference could not be resolved; not wired", "name", p.name, "kind", p.kind)
			continue
		}
		if err := p.src.Open(ctx, resolved); err != nil {
			log.Warn("knowledge: document source failed to open (configuration error); not wired", "name", p.name, "kind", p.kind)
			continue
		}
		if d.knowledge != nil {
			d.knowledge.AddSource(p.name, p.src)
		}
		log.Info("knowledge: wired document source", "name", p.name, "kind", p.kind)
	}
	if d.notify != nil {
		// Late-bind the plugin-destination loader (boot owns rt and the scratch
		// dir); with either missing, plugin kinds are skipped honestly in openAll.
		d.notify.rt = d.rt
		d.notify.embedDir = d.connectorDir
		d.notify.openAll(ctx, r, log)
	}
	if d.claude != nil {
		d.claude.populate(ctx, r, log)
	}
}
