// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package secure provides the engine's secure-by-default boot primitives
// (docs/SECURITY-HARDENING.md): TLS that is on by default (a self-signed certificate is minted
// on first boot if none is configured), the on-disk audit signing key, and the
// first-boot setup token. All private key material is written 0600 in a 0700 data
// directory, and reads FAIL CLOSED if the file permissions are wider than
// owner-only — the engine refuses to start rather than silently trusting a
// world-readable key.
//
// There are no default credentials: the engine mints a one-time setup token
// (printed to stdout only, never logged) used to create the first superadmin, and
// every endpoint except liveness and setup is closed until that account exists.
package secure
