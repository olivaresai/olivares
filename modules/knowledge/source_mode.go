// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"strings"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/core/model"
)

const (
	sourceModeExport = "export"
	sourceModeLive   = "live"
	sourceModeDirect = "direct"
)

type sourceModeProvider interface {
	Mode() string
}

func sourceModeForSource(src contentsource.Source) string {
	if src == nil {
		return sourceModeExport
	}
	if p, ok := src.(sourceModeProvider); ok {
		return normalizeSourceMode(p.Mode(), sourceModeExport)
	}
	return sourceModeExport
}

func recordSourceMode(rec model.Record) string {
	if rec == nil {
		return sourceModeExport
	}
	return normalizeSourceMode(rec.String(colSourceMode), sourceModeExport)
}

func normalizeSourceMode(mode, fallback string) string {
	fallback = normalizeSourceModeFallback(fallback)
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case sourceModeLive:
		return sourceModeLive
	case sourceModeDirect:
		return sourceModeDirect
	case sourceModeExport:
		return sourceModeExport
	case "":
		return fallback
	default:
		return fallback
	}
}

func normalizeSourceModeFallback(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case sourceModeLive:
		return sourceModeLive
	case sourceModeDirect:
		return sourceModeDirect
	default:
		return sourceModeExport
	}
}
