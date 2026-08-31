// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"

	"github.com/olivaresai/olivares/core/secret"
)

// dsnref.go is the secret-bootstrap half of the install UX: a DSN flag may
// carry a STORELESS secret reference — `file:<path>` or `env:<VAR>` — resolved at
// boot, BEFORE the store opens. This keeps the database password out of the systemd
// env file (which is config|noreplace and often committed to config management):
// `olivares setup` writes the password-bearing DSN to a 0600 file and references it
// as `--dsn=file:/etc/olivares/secrets/db.dsn`. The store-backed schemes
// (store:/db:/vault:/cloud managers) are deliberately NOT resolvable here — the
// sealed store IS the database we are about to open — so they fail closed with a
// clear message rather than a confusing connection error.

// resolveDSNRef returns value unchanged when it is a literal DSN (the common case),
// resolves a file:/env: reference to its content, and refuses any other reference
// scheme. label is the flag name, used only in error messages.
func resolveDSNRef(ctx context.Context, label, value string, getenv func(string) string) (string, error) {
	ref, ok := secret.ParseReference(value)
	if !ok {
		return value, nil // a literal DSN (or empty)
	}
	switch ref.Scheme {
	case secret.SchemeEnv:
		b, err := secret.EnvHandler{Lookup: func(k string) (string, bool) {
			v := getenv(k)
			return v, v != ""
		}}.Resolve(ctx, ref.Locator)
		if err != nil {
			return "", fmt.Errorf("%s: %w", label, err)
		}
		return string(b), nil
	case secret.SchemeFile:
		b, err := secret.FileHandler{}.Resolve(ctx, ref.Locator)
		if err != nil {
			return "", fmt.Errorf("%s: %w", label, err)
		}
		return string(b), nil
	default:
		return "", fmt.Errorf("%s reference scheme %q needs the secret store, which is not available before the database opens; for a DSN use file:<path> or env:<VAR> (resolved at boot, keeping the password out of the env file)", label, ref.Scheme)
	}
}
