// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/model"
)

// seedSourceForGet writes one roster row and hands back the data dir it lives in.
func seedSourceForGet(t *testing.T, def model.SourceDef) string {
	t.Helper()
	dir := t.TempDir()
	seed := &cobra.Command{}
	seed.SetContext(context.Background())
	eng, err := auditBoot(seed, dir, "sqlite", "")
	if err != nil {
		t.Fatalf("seed boot: %v", err)
	}
	putRow(t, eng.sourceStore, def)
	if err := eng.Close(); err != nil {
		t.Fatalf("close seed engine: %v", err)
	}
	return dir
}

// runSourcesGet goes through the ROOT, not newSourcesCmd(), and that is not incidental:
// `-o/--output` is registered as a ROOT persistent flag (main.go:131), so a test that
// executes the group directly cannot exercise the JSON pane at all — it fails with
// "unknown shorthand flag: 'o'". Measured while writing this file, and worth stating:
// a harness that cannot reach the JSON pane would have reported the masking as covered
// while the half a script actually reads went untested.
func runSourcesGet(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	full := append([]string{"sources", "get"}, args...)
	full = append(full, "--data-dir", dir, "--engine", "sqlite")
	out, errOut, err := runLeafCLI(t, full...)
	return out + errOut, err
}

// TestSourcesGetMasksALiteralSecretInBOTHRenderings. The parity note that ordered this
// verb states the constraint: a row written before the inline-secret guard existed can
// still hold a literal, so `get` must mask through the plan's rule rather than print
// config verbatim.
//
// ⛔ BOTH renderings are asserted in ONE test on purpose. The defect this guards against
// was measured on the sibling path and is recorded at cmd_sources_plan.go:258-263: a
// literal `ghp_...` was printed in full "to stdout AND to the JSON". A test that checked
// only the table would have passed with the JSON still leaking, which is the half that a
// script — the thing most likely to be piped somewhere durable — actually reads.
func TestSourcesGetMasksALiteralSecretInBOTHRenderings(t *testing.T) {
	const literal = "ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	dir := seedSourceForGet(t, model.SourceDef{
		Name: "gh-main", Kind: "github", Tenant: "acme", Enabled: true,
		Config: map[string]string{"pat": literal, "mode": "live"},
	})

	t.Run("table", func(t *testing.T) {
		got, err := runSourcesGet(t, dir, "gh-main")
		if err != nil {
			t.Fatalf("sources get: %v\n%s", err, got)
		}
		if strings.Contains(got, literal) {
			t.Fatalf("the literal secret reached the operator's terminal:\n%s", got)
		}
		if !strings.Contains(got, redactedPlanValue) {
			t.Fatalf("nothing was masked, so the mask never fired:\n%s", got)
		}
		if !strings.Contains(got, "gh-main") {
			t.Fatalf("output does not name the source:\n%s", got)
		}
	})

	// ⛔ THE JSON PANE IS DECODED, NOT GREPPED, AND THAT IS THE POINT OF THIS SUBTEST.
	// `json.Marshal` HTML-escapes by default, so the mask lands in the document as
	// "\u003credacted\u003e". A substring check for "<redacted>" therefore FAILS on a
	// correctly masked document — measured here while writing it. The trap generalises
	// past this test: anyone auditing a JSON pane for leaks by grepping the mask will
	// conclude nothing was masked, and anyone grepping for a secret that happens to
	// contain < or > will miss it too. Decode, then compare values.
	t.Run("json", func(t *testing.T) {
		got, err := runSourcesGet(t, dir, "gh-main", "-o", "json")
		if err != nil {
			t.Fatalf("sources get -o json: %v\n%s", err, got)
		}
		if strings.Contains(got, literal) {
			t.Fatalf("the literal secret reached the JSON pane:\n%s", got)
		}
		var doc struct {
			Name   string            `json:"name"`
			Config map[string]string `json:"config"`
		}
		if err := json.Unmarshal([]byte(got), &doc); err != nil {
			t.Fatalf("the JSON pane is not decodable: %v\n%s", err, got)
		}
		if doc.Name != "gh-main" {
			t.Fatalf("json name = %q, want gh-main", doc.Name)
		}
		if doc.Config["pat"] != redactedPlanValue {
			t.Fatalf("json config.pat = %q, want %q", doc.Config["pat"], redactedPlanValue)
		}
		// The non-secret key must survive, or masking has eaten the answer.
		if doc.Config["mode"] != "live" {
			t.Fatalf("json config.mode = %q, want live", doc.Config["mode"])
		}
	})
}

// TestSourcesGetShowsAReferenceUnmasked is the POSITIVE control, and without it the
// masking test above is satisfied by a command that redacts everything — which would
// destroy the only reason this verb exists. The parity note is explicit: the roster's
// contract is that config carries REFERENCES, never values, and an operator who cannot
// see WHICH reference a source resolves cannot audit that contract.
func TestSourcesGetShowsAReferenceUnmasked(t *testing.T) {
	dir := seedSourceForGet(t, model.SourceDef{
		Name: "vault-prod", Kind: "vault", Tenant: "acme", Enabled: true,
		Config: map[string]string{"token": "store:vault-token", "mode": "live"},
	})
	got, err := runSourcesGet(t, dir, "vault-prod")
	if err != nil {
		t.Fatalf("sources get: %v\n%s", err, got)
	}
	if !strings.Contains(got, "store:vault-token") {
		t.Fatalf("the secret REFERENCE was masked; the verb cannot audit what it exists to audit:\n%s", got)
	}
}

// TestSourcesGetOnAnUnknownNameIsNotFound: 4, not the generic 1. A script must be able
// to tell "this source is not in the roster" from "the store could not be read", and
// those two are the same exit code if the command just returns an error.
func TestSourcesGetOnAnUnknownNameIsNotFound(t *testing.T) {
	dir := seedSourceForGet(t, model.SourceDef{
		Name: "present", Kind: "vault", Tenant: "acme", Enabled: true,
	})
	got, err := runSourcesGet(t, dir, "absent")
	if err == nil {
		t.Fatalf("an unknown source name succeeded:\n%s", got)
	}
	if code := exitcode.From(err); code != exitcode.NotFound {
		t.Fatalf("exit = %d, want %d (NotFound); err = %v", code, exitcode.NotFound, err)
	}
	if !strings.Contains(err.Error(), "absent") {
		t.Fatalf("the error does not name what was not found: %v", err)
	}
}
