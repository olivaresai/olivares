// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// catalogDestructiveVerbs: one DELETE and three POSTs whose effect is a change
// to what this control plane will accept or run.
var catalogDestructiveVerbs = []struct {
	name string
	args []string
	path string
}{
	{"entries rm", []string{"catalog", "entries", "rm", "ce_1"}, "/v1/m/catalog/entries/ce_1"},
	{"entries deprecate", []string{"catalog", "entries", "deprecate", "ce_1"}, "/v1/m/catalog/entries/ce_1/deprecate"},
	{"instances transition", []string{"catalog", "instances", "transition", "ci_1", "--status", "approved"}, "/v1/m/catalog/instances/ci_1/transition"},
	{"mcp-admission policy set", []string{"catalog", "mcp-admission", "policy", "set", "--require-signed", "--replace"}, "/v1/m/catalog/mcp-admission/policy"},
	{"connector-admission policy set", []string{"catalog", "connector-admission", "policy", "set", "--require-signed", "--replace"}, "/v1/m/catalog/connector-admission/policy"},
}

// TestCatalogDestructiveVerbsRefuseUnattendedConsent: DENY with a request count,
// POSITIVE CONTROL in the same subtest.
func TestCatalogDestructiveVerbsRefuseUnattendedConsent(t *testing.T) {
	for _, verb := range catalogDestructiveVerbs {
		t.Run(verb.name, func(t *testing.T) {
			prepareDatalaneCLITest(t)
			rec := newDatalaneRecorder(t, http.StatusOK, `{"id":"ce_1","status":"deprecated"}`)

			_, _, err := execDatalane(t, "", datalaneArgs(rec, verb.args...)...)
			if err == nil {
				t.Fatalf("%s without --yes must fail", verb.name)
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Errorf("exit = %d, want %d (usage)", got, exitcode.Usage)
			}
			if got := rec.count(); got != 0 {
				t.Fatalf("requests = %d, want 0", got)
			}

			args := append(append([]string{}, verb.args...), "--yes")
			if _, _, err := execDatalane(t, "", datalaneArgs(rec, args...)...); err != nil {
				t.Fatalf("%s --yes must succeed: %v", verb.name, err)
			}
			if got := rec.count(); got != 1 {
				t.Fatalf("requests with --yes = %d, want 1", got)
			}
			if got := rec.last(t).Path; got != verb.path {
				t.Errorf("path = %q, want %q", got, verb.path)
			}
		})
	}
}

// TestCatalogAuthoringLifecycleVerbsDoNotAskForConsent: submit and approve MOVE
// an entry forward rather than withdrawing it, so they must not have grown a
// confirmation. The asymmetry against `deprecate` is what keeps --yes meaningful.
func TestCatalogAuthoringLifecycleVerbsDoNotAskForConsent(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, `{"id":"ce_1","status":"approved","signed":true}`)
	for _, tc := range []struct {
		verb string
		path string
	}{
		{"submit", "/v1/m/catalog/entries/ce_1/submit"},
		{"approve", "/v1/m/catalog/entries/ce_1/approve"},
	} {
		if _, _, err := execDatalane(t, "", datalaneArgs(rec, "catalog", "entries", tc.verb, "ce_1")...); err != nil {
			t.Fatalf("entries %s: %v", tc.verb, err)
		}
		req := rec.last(t)
		if req.Method != http.MethodPost || req.Path != tc.path {
			t.Fatalf("request = %s %s, want POST %s", req.Method, req.Path, tc.path)
		}
		// These endpoints decode no body: sending one would be a request the module
		// never reads, and a caller could believe it carried a decision.
		if len(strings.TrimSpace(string(req.Body))) != 0 {
			t.Errorf("entries %s must send no body, got %q", tc.verb, req.Body)
		}
		if got := req.Header.Get("Content-Type"); got != "" {
			t.Errorf("entries %s must not declare a Content-Type with no body, got %q", tc.verb, got)
		}
	}
}

// TestCatalogAdmissionListsUsePluralRoute: the policy lives at
// /<group>/policy and the verdicts at /<group>s — two routes one character
// apart. Getting them the wrong way round would produce a 404 that looks like
// "no verdicts yet", which is the reading an operator must never be handed about
// a supply-chain gate.
func TestCatalogAdmissionListsUsePluralRoute(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, `{"items":[],"has_more":false}`)
	for _, tc := range []struct {
		args []string
		path string
	}{
		{[]string{"catalog", "mcp-admission", "ls"}, "/v1/m/catalog/mcp-admissions"},
		{[]string{"catalog", "connector-admission", "ls"}, "/v1/m/catalog/connector-admissions"},
		{[]string{"catalog", "mcp-admission", "policy", "get"}, "/v1/m/catalog/mcp-admission/policy"},
		{[]string{"catalog", "connector-admission", "policy", "get"}, "/v1/m/catalog/connector-admission/policy"},
	} {
		if _, _, err := execDatalane(t, "", datalaneArgs(rec, tc.args...)...); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if got := rec.last(t).Path; got != tc.path {
			t.Errorf("%v hit %q, want %q", tc.args, got, tc.path)
		}
	}
}

// TestCatalogAdmissionPolicySetRefusesAPartialReplace. The policy PUT replaces
// the whole document, so a trust anchor left out is REMOVED. A partial
// invocation that looked like a patch would quietly widen what this plane
// accepts — which is the opposite of what somebody editing an admission policy
// intends.
func TestCatalogAdmissionPolicySetRefusesAPartialReplace(t *testing.T) {
	for _, group := range []string{"mcp-admission", "connector-admission"} {
		t.Run(group, func(t *testing.T) {
			prepareDatalaneCLITest(t)
			rec := newDatalaneRecorder(t, http.StatusOK, `{"require_signed":true}`)

			_, _, err := execDatalane(t, "", datalaneArgs(rec,
				"catalog", group, "policy", "set", "--require-signed", "--yes")...)
			if err == nil {
				t.Fatal("a partial policy replace must be refused")
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Errorf("exit = %d, want %d", got, exitcode.Usage)
			}
			if got := rec.count(); got != 0 {
				t.Fatalf("requests = %d, want 0", got)
			}
			for _, want := range []string{"--trusted-root", "--allowed-issuer", "--replace"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal must name the anchor that would be dropped (%q): %v", want, err)
				}
			}

			// POSITIVE CONTROL: the full document is accepted and arrives whole.
			if _, _, err := execDatalane(t, "", datalaneArgs(rec,
				"catalog", group, "policy", "set", "--yes",
				"--require-signed", "--require-subject-digest",
				"--allowed-identity", "https://github.com/acme/.github/workflows/release.yml@refs/heads/main",
				"--allowed-issuer", "https://token.actions.githubusercontent.com",
				"--trusted-key", "PUBLIC-KEY", "--trusted-root", "ROOT-PEM",
				"--allowed-predicate", "https://slsa.dev/provenance/v1")...); err != nil {
				t.Fatalf("a complete policy must be accepted: %v", err)
			}
			req := rec.last(t)
			if req.Method != http.MethodPut {
				t.Fatalf("method = %s, want PUT", req.Method)
			}
			body := rec.jsonBody(t)
			if body["require_signed"] != true || body["require_subject_digest"] != true {
				t.Fatalf("body = %#v", body)
			}
			roots, ok := body["trusted_roots"].([]any)
			if !ok || len(roots) != 1 || roots[0] != "ROOT-PEM" {
				t.Fatalf("trusted_roots = %#v", body["trusted_roots"])
			}
		})
	}
}

// TestCatalogAdmitRefusesANonJSONBundleBeforeConnecting: an attestation bundle
// that is not JSON cannot be an attestation, and finding that out from a 400 —
// exit 1 — would make it indistinguishable from a bundle that failed to verify.
func TestCatalogAdmitRefusesANonJSONBundleBeforeConnecting(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, `{"admitted":true}`)

	file := filepath.Join(t.TempDir(), "bundle.json")
	if err := writeTestFile(file, "not a bundle"); err != nil {
		t.Fatal(err)
	}
	_, _, err := execDatalane(t, "", datalaneArgs(rec,
		"catalog", "entries", "admit", "ce_1", "--bundle-file", file)...)
	if err == nil {
		t.Fatal("a non-JSON bundle must be refused")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit = %d, want %d", got, exitcode.Usage)
	}
	if got := rec.count(); got != 0 {
		t.Errorf("requests = %d, want 0", got)
	}
	if !strings.Contains(err.Error(), file) {
		t.Errorf("the refusal must name the file: %v", err)
	}

	// No bundle at all is also refused, before connecting.
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "catalog", "entries", "admit", "ce_1")...); err == nil {
		t.Fatal("admit with no bundle must be refused")
	}
	if got := rec.count(); got != 0 {
		t.Errorf("requests = %d, want 0", got)
	}

	// POSITIVE CONTROL: a JSON bundle reaches the engine intact.
	if err := writeTestFile(file, `{"mediaType":"application/vnd.dev.sigstore.bundle+json;version=0.3"}`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"catalog", "entries", "admit", "ce_1", "--bundle-file", file,
		"--expected-digest", "sha256:abc", "--predicate-type", "https://slsa.dev/provenance/v1")...); err != nil {
		t.Fatalf("a JSON bundle must be accepted: %v", err)
	}
	body := rec.jsonBody(t)
	bundle, ok := body["bundle"].(map[string]any)
	if !ok || !strings.Contains(bundle["mediaType"].(string), "sigstore") {
		t.Fatalf("bundle did not reach the engine intact: %#v", body["bundle"])
	}
	if body["expected_digest"] != "sha256:abc" {
		t.Fatalf("expected_digest = %#v", body["expected_digest"])
	}
	preds, ok := body["predicate_types"].([]any)
	if !ok || len(preds) != 1 {
		t.Fatalf("predicate_types = %#v", body["predicate_types"])
	}
	if got := rec.last(t).Path; got != "/v1/m/catalog/entries/ce_1/admit" {
		t.Errorf("path = %q", got)
	}
}

// TestCatalogAdmitRecordsAVerdictThatMayBeNegative: a bundle that fails to
// verify is a RECORDED verdict, answered 200. The command must exit 0 and
// surface admitted=false rather than manufacturing a failure — the verdict is
// the product here, and a script has to read the field.
func TestCatalogAdmitRecordsAVerdictThatMayBeNegative(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK,
		`{"admitted":false,"verified":false,"reason":"no trusted identity matched"}`)
	file := filepath.Join(t.TempDir(), "bundle.json")
	if err := writeTestFile(file, `{"mediaType":"x"}`); err != nil {
		t.Fatal(err)
	}
	out, _, err := execDatalane(t, "", datalaneArgs(rec,
		"catalog", "entries", "admit", "ce_1", "--bundle-file", file)...)
	if err != nil {
		t.Fatalf("a recorded negative verdict is not a command failure: %v", err)
	}
	for _, want := range []string{"admitted", "false", "no trusted identity matched"} {
		if !strings.Contains(out, want) {
			t.Errorf("the verdict must be readable on stdout (%q missing):\n%s", want, out)
		}
	}
}

// TestCatalogEntryCreateRequiresItsFourIdentifyingFields: kind, name, slug and
// version identify an entry, and the module refuses a body missing any of them.
// Refusing locally keeps that a usage error rather than a generic failure.
func TestCatalogEntryCreateRequiresItsFourIdentifyingFields(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusCreated, `{"id":"ce_1"}`)
	for _, args := range [][]string{
		{"catalog", "entries", "create"},
		{"catalog", "entries", "create", "--kind", "mcp"},
		{"catalog", "entries", "create", "--kind", "mcp", "--name", "n", "--slug", "s"},
	} {
		_, _, err := execDatalane(t, "", datalaneArgs(rec, args...)...)
		if err == nil {
			t.Fatalf("%v must fail", args)
		}
		if got := exitcode.From(err); got != exitcode.Usage {
			t.Errorf("%v: exit = %d, want %d", args, got, exitcode.Usage)
		}
	}
	if got := rec.count(); got != 0 {
		t.Fatalf("requests = %d, want 0", got)
	}

	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"catalog", "entries", "create", "--kind", "mcp", "--name", "GitHub MCP",
		"--slug", "github-mcp", "--version", "1.0.0", "--owner-ref", "team-platform")...); err != nil {
		t.Fatalf("a complete entry must be accepted: %v", err)
	}
	body := rec.jsonBody(t)
	if body["kind"] != "mcp" || body["slug"] != "github-mcp" || body["version"] != "1.0.0" {
		t.Fatalf("body = %#v", body)
	}
}

// TestCatalogInstanceTransitionRequiresAStatus: a transition with no target
// state is not a decision.
func TestCatalogInstanceTransitionRequiresAStatus(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, `{"id":"ci_1","status":"approved"}`)
	_, _, err := execDatalane(t, "", datalaneArgs(rec,
		"catalog", "instances", "transition", "ci_1", "--yes")...)
	if err == nil {
		t.Fatal("transition without --status must fail")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit = %d, want %d", got, exitcode.Usage)
	}
	if got := rec.count(); got != 0 {
		t.Errorf("requests = %d, want 0", got)
	}

	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"catalog", "instances", "transition", "ci_1",
		"--status", "rejected", "--note", "no budget", "--yes")...); err != nil {
		t.Fatalf("a complete transition must be accepted: %v", err)
	}
	body := rec.jsonBody(t)
	if body["status"] != "rejected" || body["note"] != "no budget" {
		t.Fatalf("body = %#v", body)
	}
}

// TestCatalogDoesNotExtendTheMCPCommand is the collision guard this lot was
// handed. `mcp` was already modified on this branch by the exit-code work, and
// the adjacency of `catalog mcp-admission` to `mcp pins` is exactly the kind of
// thing a later sweep folds together. The two surfaces are different modules,
// different permissions and different lifecycles: admission decides whether an
// MCP server may be in the catalog at all, pinning decides whether an admitted
// server's tool definitions still match.
func TestCatalogDoesNotExtendTheMCPCommand(t *testing.T) {
	root := newRootCmd()
	// POSITIVE CONTROL FIRST: without it, a renamed `mcp` makes every absence
	// below trivially true.
	mcp := resolveCommandPath(t, root, "olivares mcp")
	if mcp == nil {
		t.Fatal("olivares mcp does not resolve; the absences below would prove nothing")
	}
	if resolveCommandPath(t, root, "olivares mcp pins ls") == nil {
		t.Fatal("olivares mcp pins ls does not resolve; the absences below would prove nothing")
	}
	for _, child := range mcp.Commands() {
		for _, name := range append([]string{child.Name()}, child.Aliases...) {
			if strings.Contains(name, "admission") || strings.Contains(name, "catalog") {
				t.Errorf("olivares mcp grew a %q subcommand: catalog admission belongs to "+
					"`olivares catalog`, which is a different module and a different permission tier", name)
			}
		}
	}
	// And the admission surface really is where it belongs.
	if resolveCommandPath(t, root, "olivares catalog mcp-admission policy get") == nil {
		t.Error("olivares catalog mcp-admission policy get must exist")
	}
}

// TestCatalogVerifyReportsItsThreeAnswersSeparately: a hash that no longer
// matches and a signature that does not verify are different failures. Text
// output must carry all three fields so an operator does not have to guess which
// one failed.
func TestCatalogVerifyReportsItsThreeAnswersSeparately(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK,
		`{"status":"approved","hash_ok":false,"signed":true,"signature_ok":true,"verified":false,"reason":"content hash mismatch"}`)
	out, _, err := execDatalane(t, "", datalaneArgs(rec, "catalog", "entries", "verify", "ce_1")...)
	if err != nil {
		t.Fatalf("entries verify: %v", err)
	}
	for _, want := range []string{"hash_ok", "signature_ok", "verified", "content hash mismatch"} {
		if !strings.Contains(out, want) {
			t.Errorf("verify output must keep %q distinct:\n%s", want, out)
		}
	}
	if got := rec.last(t).Path; got != "/v1/m/catalog/entries/ce_1/verify" {
		t.Errorf("path = %q", got)
	}
}
