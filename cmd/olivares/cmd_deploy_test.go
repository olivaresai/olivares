// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// Family tests for `olivares deploy`.

func TestDeployVerbsReachTheRoutesTheEngineRegisters(t *testing.T) {
	specFile := lot3WriteTempJSON(t, `{"image":"x"}`)
	for _, tc := range []struct {
		argv       []string
		wantMethod string
		wantPath   string
	}{
		{[]string{"deploy", "definitions", "ls"}, "GET", "/v1/m/deploy/definitions"},
		{[]string{"deploy", "definitions", "get", "dep-1"}, "GET", "/v1/m/deploy/definitions/dep-1"},
		{[]string{"deploy", "definitions", "create", "--subject-ref", "a", "--name", "n",
			"--environment", "prod", "--target", "c1", "--runtime", "container", "--spec-file", specFile},
			"POST", "/v1/m/deploy/definitions"},
		{[]string{"deploy", "definitions", "update", "dep-1", "--spec-file", specFile}, "PUT", "/v1/m/deploy/definitions/dep-1"},
		{[]string{"deploy", "definitions", "rm", "dep-1", "--yes"}, "DELETE", "/v1/m/deploy/definitions/dep-1"},
		{[]string{"deploy", "definitions", "revisions", "dep-1"}, "GET", "/v1/m/deploy/definitions/dep-1/revisions"},
		{[]string{"deploy", "plan", "dep-1"}, "POST", "/v1/m/deploy/definitions/dep-1/plan"},
		{[]string{"deploy", "verify", "dep-1"}, "POST", "/v1/m/deploy/definitions/dep-1/verify"},
		{[]string{"deploy", "apply", "dep-1"}, "POST", "/v1/m/deploy/definitions/dep-1/apply"},
		{[]string{"deploy", "retire", "dep-1", "--yes"}, "POST", "/v1/m/deploy/definitions/dep-1/retire"},
		{[]string{"deploy", "rollback", "dep-1", "--to-version", "4", "--yes"}, "POST", "/v1/m/deploy/definitions/dep-1/rollback"},
		{[]string{"deploy", "operations"}, "GET", "/v1/m/deploy/operations"},
		{[]string{"deploy", "wirings"}, "GET", "/v1/m/deploy/wirings"},
	} {
		t.Run(strings.Join(tc.argv, "-"), func(t *testing.T) {
			srv := newLot3Server(t, lot3OK(`{"items":[],"has_more":false,"id":"dep-1","op":"x","status":"ok"}`))
			if _, _, err := execRoot(t, lot3Args(srv.URL, tc.argv...)...); err != nil {
				t.Fatalf("verb failed: %v", err)
			}
			if got, _ := srv.method.Load().(string); got != tc.wantMethod {
				t.Errorf("method = %s, want %s", got, tc.wantMethod)
			}
			if got := srv.lastPath(); got != tc.wantPath {
				t.Errorf("path = %s, want %s", got, tc.wantPath)
			}
		})
	}
}

// TestDeployIrreversibleVerbsAreAllGatedNotJustTheDelete is the census claim this
// family exists to make good on: counting DELETEs finds ONE, and there are FOUR
// irreversible verbs. Three of them are POSTs.
func TestDeployIrreversibleVerbsAreAllGatedNotJustTheDelete(t *testing.T) {
	for name, verb := range map[string][]string{
		"rm (the only DELETE)": {"deploy", "definitions", "rm", "dep-1"},
		"retire (a POST)":      {"deploy", "retire", "dep-1"},
		"rollback (a POST)":    {"deploy", "rollback", "dep-1", "--to-version", "4"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newLot3Server(t, lot3OK(`{"id":"dep-1","status":"ok"}`))
			_, _, err := execRoot(t, lot3Args(srv.URL, verb...)...)
			if err == nil || exitcode.From(err) != exitcode.Usage {
				t.Fatalf("%s must refuse unattended, got %v", name, err)
			}
			if n := srv.calls.Load(); n != 0 {
				t.Fatalf("%d request(s) reached the server before consent", n)
			}
		})
	}

	// THE CONTROL: the read-only and merely-actuating verbs on the SAME resource
	// are NOT gated. "Everything needs --yes" would pass the loop above and make
	// the CLI unusable from CI.
	srv := newLot3Server(t, lot3OK(`{"id":"dep-1","op":"plan","status":"planned"}`))
	for _, ungated := range [][]string{
		{"deploy", "plan", "dep-1"},
		{"deploy", "verify", "dep-1"},
		{"deploy", "apply", "dep-1"},
	} {
		if _, _, err := execRoot(t, lot3Args(srv.URL, ungated...)...); err != nil {
			t.Fatalf("%v must not need a confirmation, got %v", ungated, err)
		}
	}
	if n := srv.calls.Load(); n != 3 {
		t.Fatalf("the ungated verbs made %d requests, want 3", n)
	}
}

// TestDeployRollbackRefusesAnImpossibleVersionBeforeAnyRequest: version 0 or a
// negative is not a revision, and sending it would let the engine answer a
// question the operator did not mean to ask.
func TestDeployRollbackRefusesAnImpossibleVersionBeforeAnyRequest(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"op":"rollback","status":"ok"}`))
	_, _, err := execRoot(t, lot3Args(srv.URL,
		"deploy", "rollback", "dep-1", "--to-version", "0", "--yes")...)
	if err == nil || exitcode.From(err) != exitcode.Usage {
		t.Fatalf("--to-version 0 must exit %d, got %v", exitcode.Usage, err)
	}
	if n := srv.calls.Load(); n != 0 {
		t.Fatalf("%d request(s) were sent with an impossible version", n)
	}

	// THE CONTROL: a real revision number travels.
	if _, _, err := execRoot(t, lot3Args(srv.URL,
		"deploy", "rollback", "dep-1", "--to-version", "4", "--note", "bad image", "--yes")...); err != nil {
		t.Fatalf("a valid rollback must succeed, got %v", err)
	}
	body := srv.lastBody()
	if !strings.Contains(body, `"to_version":4`) || !strings.Contains(body, "bad image") {
		t.Fatalf("the rollback body lost a field: %s", body)
	}
}

// TestDeployUpdateSendsOnlyTheSpecAndTheFlagsTyped. `definitions update` is a PUT
// of the SPEC, but --target and --source-ref are optional: sending them unset
// would blank a target the operator never mentioned.
func TestDeployUpdateSendsOnlyTheSpecAndTheFlagsTyped(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"id":"dep-1"}`))
	if _, _, err := execRoot(t, lot3Args(srv.URL, "deploy", "definitions", "update", "dep-1",
		"--spec-file", lot3WriteTempJSON(t, `{"image":"v2"}`))...); err != nil {
		t.Fatalf("the update must succeed, got %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(srv.lastBody()), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if _, present := body["spec"]; !present {
		t.Fatalf("the spec did not travel: %s", srv.lastBody())
	}
	for _, never := range []string{"target", "source_ref"} {
		if _, present := body[never]; present {
			t.Errorf("an untyped %q reached the engine and would blank the stored value", never)
		}
	}

	// THE CONTROL: a typed --target DOES travel.
	if _, _, err := execRoot(t, lot3Args(srv.URL, "deploy", "definitions", "update", "dep-1",
		"--spec-file", lot3WriteTempJSON(t, `{"image":"v2"}`), "--target", "cluster-2")...); err != nil {
		t.Fatalf("the update must succeed, got %v", err)
	}
	if !strings.Contains(srv.lastBody(), "cluster-2") {
		t.Fatalf("the typed --target did not travel: %s", srv.lastBody())
	}
}

// TestDeployCreateRejectsAMalformedSpecBeforeAnyRequest.
func TestDeployCreateRejectsAMalformedSpecBeforeAnyRequest(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"id":"dep-1"}`))
	_, _, err := execRoot(t, lot3Args(srv.URL, "deploy", "definitions", "create",
		"--subject-ref", "a", "--name", "n", "--environment", "prod",
		"--target", "c1", "--runtime", "container",
		"--spec-file", lot3WriteTempJSON(t, `{not json`))...)
	if err == nil || exitcode.From(err) != exitcode.Usage {
		t.Fatalf("a malformed spec must exit %d, got %v", exitcode.Usage, err)
	}
	if n := srv.calls.Load(); n != 0 {
		t.Fatalf("%d request(s) were sent with a malformed spec", n)
	}
}

// TestDeployDeleteSaysWhatWentEvenWithNoBody. The engine answers 204 with nothing
// at all, and a command that printed nothing would be indistinguishable from one
// that did nothing.
func TestDeployDeleteSaysWhatWentEvenWithNoBody(t *testing.T) {
	srv := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	out, _, err := execRoot(t, lot3Args(srv.URL, "deploy", "definitions", "rm", "dep-1", "--yes")...)
	if err != nil {
		t.Fatalf("a 204 delete must exit 0, got %v", err)
	}
	if !strings.Contains(out, "dep-1") {
		t.Fatalf("the delete must name what went, got: %q", out)
	}

	// And -o json must give a script something parseable, not zero bytes.
	jsonOut, _, jerr := execRoot(t, lot3Args(srv.URL,
		"deploy", "definitions", "rm", "dep-1", "--yes", "-o", "json")...)
	if jerr != nil {
		t.Fatalf("a 204 delete must exit 0 in json mode, got %v", jerr)
	}
	var parsed map[string]any
	if uerr := json.Unmarshal([]byte(jsonOut), &parsed); uerr != nil {
		t.Fatalf("-o json on a 204 is not parseable: %v (%q)", uerr, jsonOut)
	}
}

// TestDeployAppliedDeletionIsAConflictNotAGenericFailure: the engine refuses to
// delete a definition whose deployment is still running, and a script must be
// able to tell that from a broken request.
func TestDeployAppliedDeletionIsAConflictNotAGenericFailure(t *testing.T) {
	srv := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":{"message":"deployment is still applied; retire it before deleting its definition"}}`)
	})
	_, _, err := execRoot(t, lot3Args(srv.URL, "deploy", "definitions", "rm", "dep-1", "--yes")...)
	if err == nil || exitcode.From(err) != exitcode.Conflict {
		t.Fatalf("a still-applied deletion must exit %d, got %v", exitcode.Conflict, err)
	}
	if !strings.Contains(err.Error(), "retire it before") {
		t.Errorf("the engine's remedy must survive, got: %v", err)
	}
}
