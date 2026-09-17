// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"context"
	"io"
	"sort"
)

// Provider adapts one vendor's release layout, signing scheme and probe. The
// driver key set is open: a driver without a registered provider is refused as
// unsupported, never guessed.
type Provider interface {
	// Key is the driver name operators use (claude, codex, grok).
	Key() string
	// Resolve fetches and verifies the signed metadata for the request and returns
	// the plan selection plus the verified material to retain. It must not touch
	// the destination, fetch the artifact or execute anything. The returned plan
	// has no Action, Existing or Digest yet; the engine fills those.
	Resolve(ctx context.Context, req Request) (*Plan, *Material, error)
	// VerifyMaterial re-verifies retained metadata under the pinned key and
	// returns the manifest's artifacts by vendor platform. Used to credit a no-op
	// and to corroborate a detected executable. The retained copy of the key is
	// not consulted: trust comes from the pin.
	VerifyMaterial(ctx context.Context, m *Material) (*VerifiedManifest, error)
	// FetchArtifact streams the selected artifact into w and enforces the exact
	// size and SHA-256 the plan binds. It is called only after Resolve verified
	// the metadata that named the artifact.
	FetchArtifact(ctx context.Context, plan *Plan, w io.Writer) (Artifact, error)
	// Probe executes exe from scratch (an empty directory the caller owns) with a
	// minimal fixed environment and reports the tool's own version line. When
	// wantVersion is non-empty the report must state that version.
	Probe(ctx context.Context, exe, scratch, wantVersion string) (ProbeReport, error)
	// DefaultPaths lists the vendor's conventional executable locations under a
	// home directory and system prefixes, for detection. Reading these paths
	// never opens the vendor's configuration, credentials or session files.
	DefaultPaths(home string) []string
}

// Catalog is the set of registered providers.
type Catalog struct {
	byKey map[string]Provider
}

func NewCatalog(providers ...Provider) *Catalog {
	c := &Catalog{byKey: map[string]Provider{}}
	for _, p := range providers {
		c.byKey[p.Key()] = p
	}
	return c
}

// Lookup returns the provider for driver or an unsupported_provider refusal that
// names what is registered.
func (c *Catalog) Lookup(driver string) (Provider, error) {
	if p, ok := c.byKey[driver]; ok {
		return p, nil
	}
	return nil, refuse(KindUnsupportedProvider,
		"driver %q has no installer in this release (registered: %v); Codex and Grok Build installation is release work still ahead, not a removed scope",
		driver, c.Keys())
}

func (c *Catalog) Keys() []string {
	keys := make([]string, 0, len(c.byKey))
	for k := range c.byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
