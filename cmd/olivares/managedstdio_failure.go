// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/modules/security"
)

type managedStdioFailure struct {
	reason, detail string
}

func (e *managedStdioFailure) Error() string { return e.detail }
func (e *managedStdioFailure) Unwrap() error { return auth.ErrMCPGatewayUnavailable }

func newManagedStdioFailure(reason string, err error, patterns []string) error {
	detail := "MCP server exited before replying"
	if err != nil {
		detail = err.Error()
	}
	return &managedStdioFailure{reason: reason, detail: safeStdioDiagnostic(detail, patterns)}
}

func safeStdioDiagnostic(raw string, patterns []string) string {
	for _, pattern := range patterns {
		if pattern != "" {
			raw = strings.ReplaceAll(raw, pattern, "[redacted]")
		}
	}
	// Reuse the credential catalog; keep paths and OS errors useful. The MCP
	// response guard also catches credential values in escaped JSON strings.
	raw, _ = security.RedactCredentials(raw)
	if (&managedStdio{patterns: patterns}).guard([]byte(raw)) != nil {
		raw = "MCP server diagnostic contained a credential [redacted]"
	}
	raw = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, raw)
	raw = strings.Join(strings.Fields(raw), " ")
	if len(raw) > 512 {
		raw = raw[:509]
		for !utf8.ValidString(raw) {
			raw = raw[:len(raw)-1]
		}
		raw += "..."
	}
	return raw
}

func stdioProbeFailure(err error) (reason, detail string) {
	var failure *managedStdioFailure
	if errors.As(err, &failure) {
		return failure.reason, failure.detail
	}
	return "", ""
}

func (m *mcpManagement) warnStdioFailure(id string, err error) {
	reason, detail := stdioProbeFailure(err)
	if detail != "" && m.eng != nil && m.eng.log != nil {
		m.eng.log.Warn("mcp-gateway: stdio server unavailable", "server_id", id, "reason", reason, "detail", detail)
	}
}
