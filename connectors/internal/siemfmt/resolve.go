// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siemfmt

import (
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/sdk/siemwire"
)

// ResolveFormat is the ONE format-option resolver for the notification
// connectors: it applies the connectors' shared public input behavior
// (trim + lowercase — unlike the engine surfaces, which are case-sensitive on
// purpose), maps the empty string to the surface default, validates the
// SUBMITTED spelling against the given catalog subset, and returns the
// CANONICAL token as the in-memory encoder key (so the alias spelling reaches
// the same encoder byte-for-byte). Before the catalog, each connector carried
// its own copy of this switch and the copies had already diverged — one
// connector accepted eight tokens, its sibling seven, and none had a guard.
// The error names the accepted set so a connector's error text can never
// advertise a different list than it enforces.
func ResolveFormat(set siemwire.FormatSet, raw string) (siemwire.FormatToken, error) {
	tok := siemwire.FormatToken(strings.ToLower(strings.TrimSpace(raw)))
	if tok == "" {
		return siemwire.Canonical(set.Default()), nil
	}
	if !set.Valid(tok) {
		return "", fmt.Errorf("unknown format %q (want %s)", raw, set.List())
	}
	return siemwire.Canonical(tok), nil
}
