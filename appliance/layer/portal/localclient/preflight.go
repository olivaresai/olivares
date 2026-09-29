// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package localclient

import (
	"errors"
	"os"
)

// dialAfterTLS checks delivery metadata only. It never opens the certificate or
// key, creates credentials, or replaces the portal's full custody validation.
// Both paths are examined before any account lookup or socket connection.
func dialAfterTLS(lstat func(string) (os.FileInfo, error), dial func() (*Client, error)) (*Client, error) {
	missing, unverifiable := false, false
	for _, path := range []string{"/etc/olivares-portal/tls.crt", "/etc/olivares-portal/tls.key"} {
		_, err := lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			missing = true
		} else if err != nil {
			unverifiable = true
		}
	}
	if missing {
		return nil, &Failure{Code: "local_unavailable", Reason: "portal_tls_not_delivered"}
	}
	if unverifiable {
		return nil, &Failure{Code: "local_unavailable", Reason: "portal_tls_unverifiable"}
	}
	return dial()
}
