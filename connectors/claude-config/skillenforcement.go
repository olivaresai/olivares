// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// skillenforcement.go defines the SEAM for enterprise-grade runtime enforcement of
// skill policy. The open-core build (this file) exposes the interface and a
// no-op default; the enterprise build-tag delta implements the deep enforcement
// (per-Skill content firewall + runtime blocking). The gate is the BUILD TAG, never
// a license.Claims read (core/license/license.go:26-34).
//
// The open-core side INVENTORIES and SIGNALS (skillscan + authorization findings);
// the enterprise side ENFORCES (blocks unauthorized/hostile skills at runtime). This
// split is intentional: the inventory widens the free governance surface; the
// enforcement is additive code that never caps something already open (no rug-pull).
package claudeconfig

// SkillEnforcer is the seam the enterprise build-tag delta plugs into. The open-core
// build wires NoopSkillEnforcer (inventory-and-signal only); the enterprise build
// wires a real enforcer that can block skill invocation at runtime.
type SkillEnforcer interface {
	// ShouldBlock reports whether a skill invocation should be blocked at runtime.
	// name is the skill's identity (directory name, or "plugin:skill" for bundled).
	// The open-core implementation always returns false (enforcement is a signal,
	// not a gate).
	ShouldBlock(name string) (blocked bool, reason string)
}

// NoopSkillEnforcer is the open-core default: it never blocks. The enterprise delta
// replaces this via the build-tag overlay.
type NoopSkillEnforcer struct{}

// ShouldBlock always returns false in the open-core build.
func (NoopSkillEnforcer) ShouldBlock(string) (bool, string) { return false, "" }
