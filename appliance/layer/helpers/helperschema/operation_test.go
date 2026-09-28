// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package helperschema

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelperInput_MutatingDocumentsCarryAClientMintedOperationID(t *testing.T) {
	mutating := map[string]struct {
		new      func() Request
		document func(operation string) string
	}{
		"power reboot": {func() Request { return &PowerRequest{} },
			func(id string) string { return `{"verb": "reboot"` + id + `}` }},
		"power shutdown": {func() Request { return &PowerRequest{} },
			func(id string) string { return `{"verb": "shutdown"` + id + `}` }},
		"support-bundle produce": {func() Request { return &SupportBundleRequest{} },
			func(id string) string { return `{"op": "produce"` + id + `}` }},
	}
	for name, m := range mutating {
		t.Run(name, func(t *testing.T) {
			request := m.new()
			if err := Decode(strings.NewReader(m.document(`, "operation_id": "`+testOperationID+`"`)), request); err != nil {
				t.Fatalf("control: a document with its operation id was refused: %v", err)
			}
			if request.Operation() != testOperationID {
				t.Fatalf("the document's operation id reads %q", request.Operation())
			}
			for why, field := range map[string]string{
				"no operation id":               "",
				"an empty one":                  `, "operation_id": ""`,
				"uppercase digits":              `, "operation_id": "` + strings.ToUpper(testOperationID) + `"`,
				"one digit short":               `, "operation_id": "` + testOperationID[1:] + `"`,
				"a path":                        `, "operation_id": "/etc/DO-NOT-PRINT"`,
				"a request id beside it":        `, "operation_id": "` + testOperationID + `", "request_id": "` + testOperationID + `"`,
				"a request id instead of it":    `, "request_id": "` + testOperationID + `"`,
				"an operation id that is a map": `, "operation_id": {"id": "` + testOperationID + `"}`,
			} {
				t.Run(why, func(t *testing.T) {
					refused(t, Decode(strings.NewReader(m.document(field)), m.new()), "DO-NOT-PRINT")
				})
			}
		})
	}

	t.Run("a document that changes nothing carries none", func(t *testing.T) {
		var key SpoolKey
		nonce, err := IssueNonce(key, bytes.NewReader(bytes.Repeat([]byte{7}, 16)))
		if err != nil {
			t.Fatal(err)
		}
		fetch := `{"op": "fetch", "nonce": "` + string(nonce) + `"`
		request := &SupportBundleRequest{}
		if err := Decode(strings.NewReader(fetch+`}`), request); err != nil || request.Operation() != "" {
			t.Fatalf("control: fetch was refused or carries an operation: %v %q", err, request.Operation())
		}
		refused(t, Decode(strings.NewReader(fetch+`, "operation_id": "`+testOperationID+`"}`), &SupportBundleRequest{}))
	})

	t.Run("the client mints 128 random bits, a fresh id each time", func(t *testing.T) {
		random := bytes.NewReader(append(bytes.Repeat([]byte{0xab}, 16), bytes.Repeat([]byte{0x01}, 16)...))
		first, err := NewOperationID(random)
		if err != nil {
			t.Fatal(err)
		}
		second, err := NewOperationID(random)
		if err != nil {
			t.Fatal(err)
		}
		if first != strings.Repeat("ab", 16) || second != strings.Repeat("01", 16) {
			t.Fatalf("minted %q and %q, want the 16 random bytes each, in lowercase hexadecimal", first, second)
		}
		if _, err := NewOperationID(bytes.NewReader(nil)); err == nil {
			t.Fatal("an operation id was minted without random bytes")
		}
	})
}
