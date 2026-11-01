// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// checkinToolsInventory is what the engine's tool inventory answers for a host with
// Claude installed (managed release 2.1.286) and Codex not installed.
const checkinToolsInventory = `{"drivers":["claude","codex"],"presence":{"codex":{"present":false}},
	"inventory":{"installed":[{"driver":"claude","version":"2.1.286","state":"installed","installed_at":"2026-10-01T09:45:19Z"}]}}`

// newCheckinToolsEngine serves the tool inventory and the tools' own sign-in the way
// the engine does, and answers the check-in as verified. claudeSignedIn sets Claude's
// sign-in; the account it reports must never reach the check-in body.
func newCheckinToolsEngine(t *testing.T, inventory string, claudeSignedIn bool) *lot3Server {
	t.Helper()
	return newLot3Server(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/agenttools/inventory"):
			_, _ = io.WriteString(w, inventory)
		case strings.HasSuffix(r.URL.Path, "/agenttools/sign-in"):
			driver := r.URL.Query().Get("driver")
			st := map[string]any{"installed": driver == "claude", "signed_in": driver == "claude" && claudeSignedIn, "account": "ana@example.com"}
			b, _ := json.Marshal(st)
			_, _ = w.Write(b)
		default:
			_, _ = io.WriteString(w, `{"surface":"managed-settings","scope":"host-7","verified":true,"drift":[]}`)
		}
	})
}

// checkinBodyTools decodes the check-in body and returns its tools list, and whether the
// body carries a tools key at all: absent and empty mean different things to the engine.
func checkinBodyTools(t *testing.T, body string) ([]map[string]any, bool) {
	t.Helper()
	var sent struct {
		Tools *[]map[string]any `json:"tools"`
	}
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("check-in body is not JSON: %v: %s", err, body)
	}
	if sent.Tools == nil {
		return nil, false
	}
	return *sent.Tools, true
}

// TestClaudePolicyCheckinWithoutToolsSendsNoToolsKey: the default check-in is unchanged,
// so an agent that never passes --tools keeps the held tool report.
func TestClaudePolicyCheckinWithoutToolsSendsNoToolsKey(t *testing.T) {
	srv := newCheckinToolsEngine(t, checkinToolsInventory, true)
	if _, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "checkin", "managed-settings",
		"--scope", "host-7")...); err != nil {
		t.Fatalf("check-in without --tools must succeed, got %v", err)
	}
	if _, present := checkinBodyTools(t, srv.lastBody()); present {
		t.Fatalf("a check-in without --tools must not send a tools key: %s", srv.lastBody())
	}
}
