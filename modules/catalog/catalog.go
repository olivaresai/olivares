// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"sync"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the module's globally unique identifier (the runtime registry key).
const Name = "olivares.catalog"

// Namespace is the module's store and API namespace. Its registered entities are
// "catalog.<entity>" and its routes mount under /v1/m/catalog/.
const Namespace = "catalog"

// cfgSigningKeyPath is the config key naming the catalog's Ed25519 signing-key
// file. When set, approving an entry signs its content hash; when unset, entries
// are hash-pinned and ledger-attested but unsigned (honest, reported by the API).
const cfgSigningKeyPath = "signing_key_path"

// Module is the catalog/marketplace module (module XIV). It is request-driven (it
// does not consume the observation bus): it curates the approved registry, governs
// the self-service instantiation flow, and verifies entry integrity.
type Module struct {
	log   *slog.Logger
	data  api.ModuleData
	clock model.Clock

	mu     sync.Mutex
	signer ed25519.PrivateKey // nil when no signing key is configured
}

// Compile-time proof the module satisfies the SDK lifecycle, the engine-side
// schema seam, the API route/permission seam and the data-consumer seam.
var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

// Option configures a Module at construction.
type Option func(*Module)

// WithSigningKey provisions the catalog's Ed25519 signing key at construction —
// the composition root's on-by-default path. The boot loads/creates the key
// fail-closed (0600) and warns to back it up on first mint, exactly as it does for
// the audit signer, then injects it here; from then on approving an entry signs its
// content hash and the verifier PINS to this key (a downgrade or substitution is
// reported NOT verified, sign.go:112-126). The catalog key is INDEPENDENT of the
// audit signing key — it signs registry artifacts, not the ledger (docs/SECURITY-HARDENING.md). A
// nil/short key is ignored, so the module falls back to the signing_key_path config
// (an operator pointing a node at an HSM-exported / custom-path key) or stays
// unsigned (the honest unpinned posture for a node with no key).
func WithSigningKey(priv ed25519.PrivateKey) Option {
	return func(m *Module) {
		if len(priv) == ed25519.PrivateKeySize {
			m.signer = priv
		}
	}
}

// New returns a catalog module.
func New(opts ...Option) *Module {
	m := &Module{clock: model.SystemClock{}}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Descriptor returns the module's self-description.
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeModule,
		Title:       "Internal catalog & marketplace",
		Description: "Curates and reuses approved agents, MCP servers, skills and templates: a versioned registry (semver) with content-hash integrity and optional Ed25519 signing, and governed self-service instantiation.",
	}
}

// UseData receives the least-privilege, tenant-scoped data handle from the engine
// boot (the api.DataConsumer seam), before Start.
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// Init loads the optional catalog signing key from config (the same fail-closed
// 0600 seam the engine uses for its own keys). The module does not subscribe to
// the bus — the catalog is curated by people, not derived from observations. It
// must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	// A signer PROVISIONED AT CONSTRUCTION (the composition root's on-by-default
	// path) takes precedence: the boot already loaded/created it fail-closed (0600)
	// and warned to back it up on first mint, so there is nothing to load here.
	if priv := m.signingKey(); priv != nil {
		if m.log != nil {
			m.log.Info("catalog: signing enabled (provisioned at boot)", "fingerprint", keyFingerprint(priv.Public().(ed25519.PublicKey)))
		}
		return nil
	}
	// Otherwise fall back to the signing_key_path config (an operator pointing a node
	// at an HSM-exported / custom-path key the boot did not provision). Same
	// fail-closed 0600 seam; warn to back it up when this node mints the key itself.
	cfg := host.Config()
	if path := cfg.Get(cfgSigningKeyPath); path != "" {
		priv, created, err := secure.LoadOrCreateSigningKey(path)
		if err != nil {
			return err
		}
		m.mu.Lock()
		m.signer = priv
		m.mu.Unlock()
		if m.log != nil {
			if created {
				m.log.Warn("catalog: generated a new catalog signing key; back it up", "path", path, "fingerprint", keyFingerprint(priv.Public().(ed25519.PublicKey)))
			} else {
				m.log.Info("catalog: signing enabled", "fingerprint", keyFingerprint(priv.Public().(ed25519.PublicKey)))
			}
		}
	}
	return nil
}

// Start checks the data handle was wired; the module has no background work.
func (m *Module) Start(context.Context) error {
	if m.data == nil && m.log != nil {
		m.log.Warn("catalog: started without a data handle; the catalog will not persist")
	}
	return nil
}

// Stop is a no-op (nothing to drain); it is idempotent.
func (m *Module) Stop(context.Context) error { return nil }

// signingKey returns the configured signer, or nil.
func (m *Module) signingKey() ed25519.PrivateKey {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.signer
}

// expectedFingerprint returns the fingerprint of this node's configured catalog
// signing key, or "" when none is configured. It is the verifier's trust anchor:
// a non-empty value means an approved entry must carry a signature from that key.
func (m *Module) expectedFingerprint() string {
	priv := m.signingKey()
	if priv == nil {
		return ""
	}
	return keyFingerprint(priv.Public().(ed25519.PublicKey))
}
