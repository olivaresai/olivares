// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"testing"
)

// HU 016 (refresh 02): on a fresh install the console's first call is the browser
// session recovery. Before setup it must say so in the standard envelope, so the
// console can tell "set this engine up" from "sign in", never an empty answer.
func TestBrowserSessionDuringSetupSaysSetupRequired(t *testing.T) {
	h := newHarness(t)
	for _, hdr := range []map[string]string{nil, {"X-Olivares-Session": "cookie"}} {
		r := h.do("GET", "/v1/auth/browser-session", "", nil, hdr)
		if r.code != http.StatusConflict || errCode(r.body) != "setup_required" {
			t.Fatalf("browser-session before setup (%v) = %d %q, want 409 setup_required", hdr, r.code, r.raw)
		}
		msg, _ := r.body["error"].(map[string]any)["message"].(string)
		if msg == "" {
			t.Fatalf("browser-session before setup carries no message: %q", r.raw)
		}
	}
	if r := h.do("POST", "/v1/setup", "", map[string]any{"token": h.setupTok, "email": "root@x.io", "password": "supersecret1"}, nil); r.code != http.StatusCreated {
		t.Fatalf("setup = %d %s", r.code, r.raw)
	}
	// After setup a browser with no session hears 401, also with a body.
	r := h.do("GET", "/v1/auth/browser-session", "", nil, nil)
	if r.code != http.StatusUnauthorized || len(r.raw) == 0 {
		t.Fatalf("browser-session after setup, signed out = %d %q, want 401 with a body", r.code, r.raw)
	}
}
