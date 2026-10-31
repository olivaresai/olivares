// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package main

// selinuxContext: SELinux exists only on Linux.
func selinuxContext() (enabled bool, context string, err error) { return false, "", nil }
