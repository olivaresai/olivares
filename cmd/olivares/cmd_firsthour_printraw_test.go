// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestPrintRawIndentsTheLedgerAnOperatorHasToRead pins that printRaw indents the
// record it is handed, two spaces per level, without changing the document.
//
// MEASURED 2026-09-18: `agent session events` printed its lifecycle ledger as one
// 900-character line. That ledger is what an operator reads to find out why a
// session failed, and nothing in it was findable by eye.
func TestPrintRawIndentsTheLedgerAnOperatorHasToRead(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	ledger := `{"items":[{"seq":0,"event":"created","to_state":"pending"},` +
		`{"seq":1,"event":"failed","from_state":"pending","to_state":"failed","detail":"exit 143"}],"has_more":false}`
	if err := printRaw(cmd, []byte(ledger)); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Count(strings.TrimRight(got, "\n"), "\n") == 0 {
		t.Fatalf("the ledger is still one line:\n%s", got)
	}
	// Two spaces per level, as renderOut marshals: "items" at 2, the array's
	// objects at 4, their fields at 6.
	if !strings.Contains(got, "\n  \"items\": [") || !strings.Contains(got, "\n      \"seq\": 0") {
		t.Fatalf("want two-space indentation as every other report command uses:\n%s", got)
	}
	// Indentation is whitespace, so what the engine SAID is unchanged. Compare the
	// values, not the bytes: a reader that re-parses this gets the same document.
	var before, after any
	if err := json.Unmarshal([]byte(ledger), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(got), &after); err != nil {
		t.Fatalf("the indented form is no longer valid JSON: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("indenting changed the document")
	}
}

// TestPrintRawPassesThroughWhatItCannotParse is the other side: an engine that
// answered with something that is not JSON must still reach the operator. That is
// exactly the moment the raw bytes matter most.
func TestPrintRawPassesThroughWhatItCannotParse(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := printRaw(cmd, []byte("upstream proxy: 502 Bad Gateway\n")); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "upstream proxy: 502 Bad Gateway\n" {
		t.Fatalf("a non-JSON body must pass through verbatim, got %q", got)
	}
}
