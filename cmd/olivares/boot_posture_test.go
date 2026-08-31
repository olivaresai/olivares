// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

func TestWarnUnsupportedProductionPosture(t *testing.T) {
	tests := []struct {
		name                 string
		engine               store.Engine
		effectiveRLSAttested bool
		wantWarning          string
	}{
		{
			name:        "SQLite evaluation profile warns",
			engine:      store.EngineSQLite,
			wantWarning: "evaluation/pilot profile, not the supported production profile",
		},
		{
			name:                 "Postgres with RLS FORCE does not warn",
			engine:               store.EnginePostgres,
			effectiveRLSAttested: true,
		},
		{
			name:        "Postgres without attested effective RLS warns",
			engine:      store.EnginePostgres,
			wantWarning: "effective RLS FORCE is not attested",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

			warnUnsupportedProductionPosture(log, tt.engine, tt.effectiveRLSAttested)

			got := buf.String()
			if tt.wantWarning == "" {
				if got != "" {
					t.Fatalf("unexpected posture warning: %s", got)
				}
				return
			}
			if !strings.Contains(got, "level=WARN") || !strings.Contains(got, tt.wantWarning) {
				t.Fatalf("posture log = %q, want WARN containing %q", got, tt.wantWarning)
			}
		})
	}
}
