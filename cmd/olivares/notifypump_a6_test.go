// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// A6 (CUTS, 2026-10-02): the pump-disable warning told the operator NO
// notification would be delivered — false: the module's nudge worker delivers
// fresh events as they are enqueued. The warning must name the real
// consequence (no periodic retry pass) and never claim total non-delivery.
func TestNotifyPumpDisableWarningNamesTheRealConsequence(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	d, ok := notifyPumpInterval("0", log)
	if ok || d != 0 {
		t.Fatalf("interval 0 must disable the pump (ok=false, d=0), got %v %v", d, ok)
	}
	out := buf.String()
	for _, falseClaim := range []string{"NO notification will be delivered", "no notification is ever delivered", "nothing delivers"} {
		if strings.Contains(strings.ToLower(out), strings.ToLower(falseClaim)) {
			t.Fatalf("the warning still claims total non-delivery (%q):\n%s", falseClaim, out)
		}
	}
	if !strings.Contains(out, "fresh events still deliver on nudge") {
		t.Fatalf("the warning must name what keeps working (nudge delivery):\n%s", out)
	}
	if !strings.Contains(out, "retries") || !strings.Contains(out, "DLQ") {
		t.Fatalf("the warning must name what stops (retries, DLQ):\n%s", out)
	}

	// A nonzero interval is untouched, and an unparsable value keeps the default.
	if d, ok := notifyPumpInterval("5s", log); !ok || d != 5*time.Second {
		t.Fatalf("5s = %v %v", d, ok)
	}
	if d, ok := notifyPumpInterval("not-a-duration", log); !ok || d != defaultNotifyPumpInterval {
		t.Fatalf("unparsable = %v %v, want the default", d, ok)
	}
}
