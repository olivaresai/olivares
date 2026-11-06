// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/driverfacts"
)

// providersDumpEnv names the file TestProvidersDump writes the catalog to. The
// "Providers and tools" page is generated from it (scripts/guide-docs). Unset, the
// test still runs its assertions and writes nothing.
const providersDumpEnv = "OLIVARES_PROVIDERS_DUMP_OUT"

// providersDumpSchema is the contract with scripts/guide-docs/providers.go.
const providersDumpSchema = "olivares.provider-presets/1"

type providersDumpTool struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// providersDumpRow is a catalog row plus the tools the engine runs on it. The
// tools are not a second table: each one is recordServesDriver's answer for the
// row's kind, endpoint and ref, the rule a binding and a launch already read.
type providersDumpRow struct {
	modelprovider.Preset
	Tools []providersDumpTool `json:"tools"`
}

func buildProvidersDump() []providersDumpRow {
	rows := []providersDumpRow{}
	for _, preset := range modelprovider.Presets() {
		row := providersDumpRow{Preset: preset, Tools: []providersDumpTool{}}
		for _, facts := range driverfacts.All() {
			if facts.Session && recordServesDriver(preset.Kind, preset.BaseURL, preset.Ref, facts.Key) {
				row.Tools = append(row.Tools, providersDumpTool{Key: facts.Key, Name: facts.Name})
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func marshalProvidersDump(t *testing.T) []byte {
	t.Helper()
	b, err := json.MarshalIndent(map[string]any{"schema": providersDumpSchema, "presets": buildProvidersDump()}, "", "  ")
	if err != nil {
		t.Fatalf("marshal providers dump: %v", err)
	}
	return append(b, '\n')
}

// TestProvidersDump holds what the page relies on: Claude Code runs on the
// DeepSeek Anthropic row (a dump that lists no tool at all is a broken walk, not a
// catalog without tools), the governed row runs none, and two walks are identical.
func TestProvidersDump(t *testing.T) {
	out := os.Getenv(providersDumpEnv)
	first := marshalProvidersDump(t)
	if string(first) != string(marshalProvidersDump(t)) {
		t.Fatal("providers dump is not deterministic")
	}
	for _, row := range buildProvidersDump() {
		switch {
		case row.Governed && len(row.Tools) != 0:
			t.Errorf("governed row %q lists tools %v; it serves no tool session", row.Ref, row.Tools)
		case row.Ref == "deepseek-anthropic" && !slices.ContainsFunc(row.Tools, func(tool providersDumpTool) bool { return tool.Key == "claude" }):
			t.Errorf("deepseek-anthropic must run Claude Code, got %v", row.Tools)
		}
	}
	if out == "" {
		return
	}
	if err := os.WriteFile(out, first, 0o600); err != nil {
		t.Fatal(err)
	}
}
