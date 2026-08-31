// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// activation.go is the OPEN, build-independent half of the enterprise
// activation pack: the governed ACTIVATION MANIFEST and its overlay onto
// osGetenv. The manifest is a small, generic {env → materialized-config-path}
// mapping written by `olivares enterprise enable <preset>` (the enterprise-only
// writer, cmd-overlay). Reading it needs no enterprise knowledge, so it lives
// here and the community binary can carry a manifest across an in-place upgrade
// — the same reason the license file is community-readable.
//
// The overlay is deliberately minimal: an add-on activated by a preset gets its
// config path supplied WHEN its OLIVARES_*_CONFIG env is unset. A real env value
// always wins (override / break-glass), and an add-on the preset STAGED but could
// not fully activate (it needs an operator secret) is recorded as "pending" and
// is NOT overlaid — so a bought-but-unconfigured control never silently pretends
// to run (the "stage + needs-input" contract).

const activationManifestFile = "enterprise-activation.json"

// ActivationState is an entry's lifecycle in the manifest.
const (
	// ActivationActive: the add-on is enabled and its materialized config is
	// overlaid onto its env var (the add-on runs on the next reload/restart).
	ActivationActive = "active"
	// ActivationPending: the preset staged a config TEMPLATE but the add-on needs
	// an operator secret before it can run; NOT overlaid until promoted to active.
	ActivationPending = "pending"
)

// ActivationEntry is one add-on's activation record.
type ActivationEntry struct {
	// Addon is the canonical add-on key (e.g. "reporting", "rtbf-depth").
	Addon string `json:"addon"`
	// Env is the OLIVARES_*_CONFIG variable the add-on is gated by.
	Env string `json:"env"`
	// Value is the materialized config path (or a literal like "true" for the
	// boolean-gated add-ons) supplied when the entry is active.
	Value string `json:"value"`
	// State is ActivationActive or ActivationPending.
	State string `json:"state"`
	// NeedsSecret marks that the staged config carries placeholders the operator
	// must fill (drives the console "needs input" surface).
	NeedsSecret bool `json:"needs_secret,omitempty"`
	// Reason is a human explanation for a pending entry (what to fill/review before
	// it activates). Empty for an active entry. Display-only.
	Reason string `json:"reason,omitempty"`
}

// ActivationManifest is the governed activation state: which add-ons a preset
// turned on, and where their config was materialized.
type ActivationManifest struct {
	Version string `json:"version"`
	Preset  string `json:"preset,omitempty"`
	// Modules is the customer-facing list (PRICING-CANON activation:
	// toggle per module, not only a preset name). Empty means "derive
	// from Entries".
	Modules   []string          `json:"modules,omitempty"`
	UpdatedAt string            `json:"updated_at,omitempty"`
	UpdatedBy string            `json:"updated_by,omitempty"`
	Entries   []ActivationEntry `json:"entries"`
}

// activationManifestVersion is the on-disk schema version.
const activationManifestVersion = "olivares.enterprise.activation.v1"

// activeOverlay returns the {env → value} overlay for the ACTIVE entries only.
func (m *ActivationManifest) activeOverlay() map[string]string {
	out := make(map[string]string, len(m.Entries))
	for _, e := range m.Entries {
		if e.State == ActivationActive && e.Env != "" && e.Value != "" {
			out[e.Env] = e.Value
		}
	}
	return out
}

// ActivationManifestPath returns the manifest path within a data dir.
func ActivationManifestPath(dataDir string) string {
	return filepath.Join(dataDir, activationManifestFile)
}

// LoadActivationManifest reads the manifest from dataDir. A missing file is not
// an error — it yields an empty manifest (nothing activated).
func LoadActivationManifest(dataDir string) (*ActivationManifest, error) {
	b, err := os.ReadFile(ActivationManifestPath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return &ActivationManifest{Version: activationManifestVersion}, nil
		}
		return nil, err
	}
	var m ActivationManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m.Version == "" {
		m.Version = activationManifestVersion
	}
	return &m, nil
}

// ErrModuleNotPurchased is the deny-closed miss: the customer asked to
// activate a module that is not in the purchased set.
var ErrModuleNotPurchased = errors.New("activation refused: module is not in the purchased set")

// RequestedModules is the list the customer asked to turn on: Modules if
// set, otherwise every Entry.Addon. A preset name alone is not a module.
func (m *ActivationManifest) RequestedModules() []string {
	if m == nil {
		return nil
	}
	if len(m.Modules) > 0 {
		out := make([]string, 0, len(m.Modules))
		for _, id := range m.Modules {
			if n := normalizeAddonKey(id); n != "" {
				out = append(out, n)
			}
		}
		return out
	}
	out := make([]string, 0, len(m.Entries))
	for _, e := range m.Entries {
		if n := normalizeAddonKey(e.Addon); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// AdmitActivationModules is the entry point PRICING-CANON A.2 requires:
// it accepts a MODULE LIST (not a preset) and refuses anything not owned.
// An empty owned set is not a grant: any requested name is refused.
func AdmitActivationModules(owned, requested []string) error {
	want := make([]string, 0, len(requested))
	for _, id := range requested {
		if n := normalizeAddonKey(id); n != "" {
			want = append(want, n)
		}
	}
	if len(want) == 0 {
		return nil
	}
	have := make(map[string]struct{}, len(owned))
	for _, id := range owned {
		if n := normalizeAddonKey(id); n != "" {
			have[n] = struct{}{}
		}
	}
	var missing []string
	seen := map[string]struct{}{}
	for _, id := range want {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if _, ok := have[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", ErrModuleNotPurchased, strings.Join(missing, ", "))
	}
	return nil
}

// SaveActivationManifest writes the manifest to dataDir atomically (temp +
// rename), mode 0600. It stamps UpdatedAt from now.
func SaveActivationManifest(dataDir string, m *ActivationManifest, now time.Time) error {
	return SaveActivationManifestOwned(dataDir, m, nil, now)
}

// SaveActivationManifestOwned writes the manifest after AdmitActivationModules.
// owned == nil skips the purchase check (legacy preset writer). A non-nil
// owned list — including empty — is deny-closed.
func SaveActivationManifestOwned(dataDir string, m *ActivationManifest, owned []string, now time.Time) error {
	if owned != nil {
		if err := AdmitActivationModules(owned, m.RequestedModules()); err != nil {
			return err
		}
	}
	m.Version = activationManifestVersion
	m.UpdatedAt = now.UTC().Format(time.RFC3339)
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path := ActivationManifestPath(dataDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// --- the osGetenv overlay -------------------------------------------------------

// activationOverlay is the process-wide overlay loaded by initActivationManifest.
// It is only ever populated with the specific activation env keys, so overlaying
// osGetenv globally cannot affect any other env read.
var (
	activationOverlay   map[string]string
	activationOverlayMu sync.RWMutex
)

// initActivationManifest loads the activation manifest from dataDir into the
// process-wide overlay (called by boot() before buildModules). A read error is
// non-fatal — it logs via the returned error and leaves the overlay empty, so a
// corrupt manifest degrades to "nothing activated" rather than failing boot.
func initActivationManifest(dataDir string) error {
	m, err := LoadActivationManifest(dataDir)
	if err != nil {
		return err
	}
	activationOverlayMu.Lock()
	activationOverlay = m.activeOverlay()
	activationOverlayMu.Unlock()
	return nil
}

// activationManifestLookup returns the overlaid value for an activation env key,
// or "" when the key is not an active manifest entry.
func activationManifestLookup(k string) string {
	activationOverlayMu.RLock()
	defer activationOverlayMu.RUnlock()
	if activationOverlay == nil {
		return ""
	}
	return activationOverlay[k]
}

// setActivationOverlayForTest replaces the overlay (test seam).
func setActivationOverlayForTest(overlay map[string]string) {
	activationOverlayMu.Lock()
	activationOverlay = overlay
	activationOverlayMu.Unlock()
}

// normalizeAddonKey canonicalizes an add-on key (lowercase, hyphenated) so the
// CLI, the manifest and the generated table agree on spelling.
func normalizeAddonKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
