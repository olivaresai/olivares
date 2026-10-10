// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package voice

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// B7 (2026-10-01): on an unconfigured node the two WARNs "no dispatcher wired"
// and "no realtime call controller wired" are noise — the default works, an
// unconfigured optional subsystem is not an anomaly, and the posture is already
// carried by the one INFO line. The gate and data-handle WARNs stay: they name
// conditions that break governance or persistence, a different class.
func TestStartOnAnUnconfiguredNodeWarnsOnlyAboutGovernance(t *testing.T) {
	var buf bytes.Buffer
	m := New() // the unconfigured node: deny gate, unwired dispatcher and call controller
	m.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	out := buf.String()
	for _, gone := range []string{"no dispatcher wired", "no realtime call controller wired"} {
		if strings.Contains(out, gone) {
			t.Fatalf("the unconfigured-node log still warns %q:\n%s", gone, out)
		}
	}
	if !strings.Contains(out, "no approval gate wired") {
		t.Fatalf("the governance WARN (a denied-by-default gate) must stay:\n%s", out)
	}
	if !strings.Contains(out, "stays honestly empty") {
		t.Fatalf("the posture INFO line must stay:\n%s", out)
	}
	if strings.Count(out, "level=WARN") > 2 {
		t.Fatalf("an unconfigured node warns at most about the gate and the data handle, got:\n%s", out)
	}
}
