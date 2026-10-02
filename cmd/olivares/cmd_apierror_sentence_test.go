// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// readableRefusal is the shape of a refusal the engine already wrote for a person (EU-08:
// the activation call printed addon_requires_license after such a sentence).
const readableRefusal = `{"error":{"code":"addon_requires_license","message":"Turning on AI Runtime Security needs a Business license that includes it; this deployment's license does not. Add it: https://olivares.ai/pricing"}}`

// TestARefusalThatIsASentenceIsPrintedAlone: when the engine's message is already a
// sentence for a person, the CLI prints it as it is, without "(HTTP 403 <code>)". A short
// or code-like message keeps the lead and the status (TestDescribeAPIRefusalLeadsWithWhatHappened).
func TestARefusalThatIsASentenceIsPrintedAlone(t *testing.T) {
	want := "Turning on AI Runtime Security needs a Business license that includes it; this deployment's license does not. Add it: https://olivares.ai/pricing"
	if got := describeAPIRefusal(http.StatusForbidden, []byte(readableRefusal)); got != want {
		t.Fatalf("describeAPIRefusal:\n got %q\nwant %q", got, want)
	}
	err := httpErr(http.StatusForbidden, []byte(readableRefusal))
	if exitcode.From(err) != exitcode.Auth || err.Error() != want {
		t.Fatalf("httpErr = %q (exit %d)", err, exitcode.From(err))
	}
	var r *apiRefusal
	if !errors.As(err, &r) || r.status != http.StatusForbidden || r.code != "addon_requires_license" {
		t.Fatalf("the status and code are not kept on the error: %#v", r)
	}
	// A message that is not a sentence keeps what it had.
	if got := describeAPIRefusal(http.StatusForbidden, []byte(`{"error":{"code":"addon_requires_license","message":"addon_requires_license"}}`)); got != "the engine refused this request: addon_requires_license (HTTP 403)" {
		t.Fatalf("a code-only message: %q", got)
	}
}

// TestARefusalInJSONModeCarriesStatusAndCode: with -o json the error is one JSON line
// with the message, the status and the code; text mode is one "Error:" line.
func TestARefusalInJSONModeCarriesStatusAndCode(t *testing.T) {
	err := httpErr(http.StatusForbidden, []byte(readableRefusal))
	var text bytes.Buffer
	if werr := printCLIErrorAs(&text, err, false); werr != nil || text.String() != "Error: "+err.Error()+"\n" {
		t.Fatalf("text = %q", text.String())
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
	if jerr := json.Unmarshal(out.Bytes(), &got); jerr != nil || bytes.Count(out.Bytes(), []byte("\n")) != 1 {
		t.Fatalf("json = %q (%v)", out.String(), jerr)
	}
	if got.Error.Message != err.Error() || got.Error.Status != 403 || got.Error.Code != "addon_requires_license" {
		t.Fatalf("json error = %+v", got.Error)
	}
}
