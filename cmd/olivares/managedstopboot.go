// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"strings"
	"time"
)

// managedstopboot.go (P2 / W3) — the configured admission timeout of a managed Stop.
//
// The sessions module bounds a managed Stop's admission, and each post-dispatch
// stage, by T. The composition root reads T from the operator environment and
// refuses to boot on a value it cannot honor: a zero, negative or unparsable timeout
// is a configuration error, not a reason to run managed Stops under a bound nobody
// chose.

const (
	envManagedStopAdmissionTimeout     = "OLIVARES_SESSIONS_MANAGED_STOP_ADMISSION_TIMEOUT"
	defaultManagedStopAdmissionTimeout = 10 * time.Second
)

// managedStopAdmissionTimeout parses T. Unset selects the default.
func managedStopAdmissionTimeout(getenv func(string) string) (time.Duration, error) {
	raw := strings.TrimSpace(getenv(envManagedStopAdmissionTimeout))
	if raw == "" {
		return defaultManagedStopAdmissionTimeout, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a duration: %w", envManagedStopAdmissionTimeout, raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s=%q must be a positive duration", envManagedStopAdmissionTimeout, raw)
	}
	return d, nil
}
