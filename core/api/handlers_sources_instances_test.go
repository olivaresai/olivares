// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

// THE CONSOLE HALF OF THE "several sources of one kind" CONTRACT.
//
// The roster read is what the console renders, so the change has to be visible
// THERE and not only in the engine: two rows of one connector kind are two rows,
// each with its own status, and the connector serving them is reported beside
// their names rather than in place of them.
//
// The console test does not invent that shape. This test replays the REAL
// endpoint — real router, real auth gate, real handler, real DTO — and holds the
// body against the golden the console suite imports. If the DTO changes and the
// golden is not regenerated, this goes red; if the golden is regenerated wrongly,
// the console suite reads the new shape and its own assertions decide.
//
// Regenerate with: OLIVARES_UPDATE_GOLDEN=1 go test ./core/api/ -run TestSourceRosterTwoRowsOfOneKind
const sourcesTwoOfOneKindGolden = "../../web/src/features/console/testdata/sources-two-of-one-kind.json"

func TestSourceRosterTwoRowsOfOneKindReachesTheConsole(t *testing.T) {
	// Two rows of ONE kind, sharing the connector that serves them. This is the
	// state that could not exist before: the second row was refused, so the console
	// could list it and never see it run.
	bothRunning := []api.SourceRosterEntry{
		{
			Name: "grok-home-a", Kind: "grok", Tenant: "acme", Enabled: true, Status: "running",
			Component: "olivares.grok", SourceMode: "export",
			Config: map[string]string{"config_path": "/home/ana/.grok/config.toml"},
		},
		{
			Name: "grok-home-b", Kind: "grok", Tenant: "acme", Enabled: true, Status: "running",
			Component: "olivares.grok", SourceMode: "export",
			Config: map[string]string{"config_path": "/home/ana/.grok-b/config.toml"},
		},
	}
	// The same pair with A disabled: B must be untouched — the point of the lot.
	aDisabled := []api.SourceRosterEntry{
		{
			Name: "grok-home-a", Kind: "grok", Tenant: "acme", Enabled: false, Status: "disabled",
			SourceMode: "export",
			Config:     map[string]string{"config_path": "/home/ana/.grok/config.toml"},
		},
		bothRunning[1],
	}

	fetch := func(rows []api.SourceRosterEntry) json.RawMessage {
		t.Helper()
		h := newHarnessOpts(t, func(o *api.Options) { o.SourceRoster = &stubSourceRoster{sources: rows} })
		tok := h.adminLogin()
		r := h.do("GET", "/v1/console/sources", tok, nil, nil)
		if r.code != http.StatusOK {
			t.Fatalf("GET /v1/console/sources = %d %s", r.code, r.raw)
		}
		var pretty any
		if err := json.Unmarshal([]byte(r.raw), &pretty); err != nil {
			t.Fatalf("response is not JSON: %v\n%s", err, r.raw)
		}
		out, err := json.Marshal(pretty)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	got := map[string]json.RawMessage{
		"both_running": fetch(bothRunning),
		"a_disabled":   fetch(aDisabled),
	}

	// The engine's own assertions, before anything is written down: the two rows
	// survive as two, keyed by NAME, with independent statuses and one component.
	var doc struct {
		Sources []api.SourceRosterEntry `json:"sources"`
	}
	if err := json.Unmarshal(got["both_running"], &doc); err != nil {
		t.Fatalf("decode both_running: %v", err)
	}
	if len(doc.Sources) != 2 {
		t.Fatalf("the roster returned %d rows, want both", len(doc.Sources))
	}
	if doc.Sources[0].Name == doc.Sources[1].Name {
		t.Fatal("the two rows collapsed onto one name")
	}
	if doc.Sources[0].Component != doc.Sources[1].Component || doc.Sources[0].Component != "olivares.grok" {
		t.Errorf("both rows must report the SAME connector beside their names: %q / %q",
			doc.Sources[0].Component, doc.Sources[1].Component)
	}
	if err := json.Unmarshal(got["a_disabled"], &doc); err != nil {
		t.Fatalf("decode a_disabled: %v", err)
	}
	if doc.Sources[0].Status != "disabled" || doc.Sources[1].Status != "running" {
		t.Errorf("disabling one row must leave the other running: %q / %q", doc.Sources[0].Status, doc.Sources[1].Status)
	}

	blob, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	blob = append(blob, '\n')
	if os.Getenv("OLIVARES_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(sourcesTwoOfOneKindGolden), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sourcesTwoOfOneKindGolden, blob, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("golden regenerated: %s", sourcesTwoOfOneKindGolden)
		return
	}
	want, err := os.ReadFile(sourcesTwoOfOneKindGolden)
	if err != nil {
		t.Fatalf("the console golden is missing (%v). Regenerate it with OLIVARES_UPDATE_GOLDEN=1", err)
	}
	if string(want) != string(blob) {
		t.Fatalf("the console golden no longer matches the endpoint.\nwant:\n%s\ngot:\n%s\nRegenerate with OLIVARES_UPDATE_GOLDEN=1", want, blob)
	}
}
