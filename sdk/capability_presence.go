// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sdk

// CapabilityEnabled reports whether an optional capability a composition wired in
// is in use right now. A nil capability is not. A capability that can be turned on
// and off while the engine runs implements CapabilityEnabled() bool and answers
// for itself; any other capability is in use whenever it is present. Callers that
// used to test the capability against nil ask this, so a capability wired once at
// boot can follow an activation without a restart.
func CapabilityEnabled(capability any) bool {
	if capability == nil {
		return false
	}
	if live, ok := capability.(interface{ CapabilityEnabled() bool }); ok {
		return live.CapabilityEnabled()
	}
	return true
}
