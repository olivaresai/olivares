// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package portal

import "errors"

// keyLabel has no SELinux attribute to read on this platform. Tests replace it.
var keyLabel = func(string) (string, error) { return "", errors.New("no SELinux label on this platform") }
