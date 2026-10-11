// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// The datalane half of #483: datalaneHTTPError appended its "built without"
// guess to every 404 — including the one refusal the engine had already
// diagnosed itself — and wrapped it in a plain error, which lost the
// *apiRefusal, so -o json dropped the status and code for all 24 datalane
// verbs, real not-found included.
func TestDatalaneModuleOffKeepsTheEngineDiagnosis(t *testing.T) {
	err := datalaneHTTPError("skills", http.StatusNotFound, []byte(moduleOffRefusal))
	if got := err.Error(); !strings.Contains(got, "olivares modules on skills") || strings.Contains(got, "built without") {
		t.Fatalf("datalane refusal = %q", got)
	}
	if got := exitcode.From(err); got != exitcode.NotFound {
		t.Fatalf("exit = %d, want %d", got, exitcode.NotFound)
	}
	var out bytes.Buffer
	if werr := printCLIErrorAs(&out, err, true); werr != nil {
		t.Fatal(werr)
	}
	var got struct {
		Error struct {
			Message string `json:"message"`
			Status  int    `json:"status"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if jerr := json.Unmarshal(out.Bytes(), &got); jerr != nil {
		t.Fatalf("json = %q (%v)", out.String(), jerr)
	}
	if got.Error.Status != http.StatusNotFound || got.Error.Code != "module_not_enabled" {
		t.Fatalf("-o json lost the refusal's status and code: %+v", got.Error)
	}
}

// A real not-found with the module on keeps the lane's cause note AND its
// status and code: the note is the existing text, the code was the lost half
// of the defect.
func TestDatalaneNotFoundKeepsItsNoteAndCode(t *testing.T) {
	err := datalaneHTTPError("skills", http.StatusNotFound,
		[]byte(`{"error":{"code":"not_found","message":"no such pack"}}`))
	if !strings.Contains(err.Error(), "built without") {
		t.Fatalf("the 404 cause note is gone: %v", err)
	}
	if got := exitcode.From(err); got != exitcode.NotFound {
		t.Fatalf("exit = %d, want %d", got, exitcode.NotFound)
	}
	var out bytes.Buffer
	if werr := printCLIErrorAs(&out, err, true); werr != nil {
		t.Fatal(werr)
	}
	var got struct {
		Error struct {
			Status int    `json:"status"`
			Code   string `json:"code"`
		} `json:"error"`
	}
	if jerr := json.Unmarshal(out.Bytes(), &got); jerr != nil {
		t.Fatalf("json = %q (%v)", out.String(), jerr)
	}
	if got.Error.Status != http.StatusNotFound || got.Error.Code != "not_found" {
		t.Fatalf("-o json lost the refusal's status and code: %+v", got.Error)
	}
}

// The engine always names the module (moduleNotEnabledHandler), but a refusal
// that does not — a proxy, a future engine — keeps the lane's cause note: the
// note is skipped only when the remedy is actually named.
func TestDatalaneModuleOffWithoutANameKeepsTheNote(t *testing.T) {
	body := `{"error":{"code":"module_not_enabled","message":"The skills module is not enabled on this node.` +
		` An administrator can enable it in the module settings."}}`
	err := datalaneHTTPError("skills", http.StatusNotFound, []byte(body))
	if got := err.Error(); !strings.Contains(got, "built without") {
		t.Fatalf("a nameless module-off refusal lost its cause note: %q", got)
	}
}
