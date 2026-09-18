// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

// license_connect_evidence_test.go: a NEW owner-approved request (recover, reactivate, deactivate
// --owner-approval) needs the current purchase credential given explicitly, by file or stdin; a
// recorded request or completion is repeated with its exact bytes and never reads evidence again;
// the proof-of-possession paths need no evidence; and a lost connect request is reported with its own
// method (an internal design note (not shipped)).

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/license/connectv1"
)

// runStdin is connectCLI.run with in as the command's standard input.
func (c *connectCLI) runStdin(in string, args ...string) (int, map[string]any) {
	c.t.Helper()
	c.t.Chdir(c.t.TempDir())
	c.t.Setenv("OLIVARES_CLI_CONFIG", c.t.TempDir()+"/config.yaml")
	c.t.Setenv("OLIVARES_SERVER_URL", "")
	c.t.Setenv("OLIVARES_TOKEN", "")
	c.t.Setenv("OLIVARES_TENANT", "")
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetIn(strings.NewReader(in))
	root.SetArgs(append(append([]string{"license", "connect"}, args...), "--data-dir", c.dir))
	_, err := root.ExecuteC()
	code := exitcode.OK
	if err != nil {
		code = exitcode.From(err)
	}
	c.outputs = append(c.outputs, out.String(), errb.String())
	c.lastError = ""
	if err != nil {
		c.lastError = err.Error()
		c.outputs = append(c.outputs, c.lastError)
	}
	var rep map[string]any
	_ = json.Unmarshal(out.Bytes(), &rep)
	return code, rep
}

// ownerRequest is a bound data directory one command away from a NEW owner-approved request.
type ownerRequest struct {
	op      string
	args    []string // the command, without --evidence
	done    string   // the completed status
	fault   string   // the stub's lose key for the completing step
	nextKey bool     // a new request creates a proposed key
}

func arrangeOwnerRequest(t *testing.T, op string) (*connectStub, *connectCLI, ownerRequest) {
	t.Helper()
	stub := newConnectStub(t)
	c := newConnectCLI(t, stub)
	dep := c.bind()
	switch op {
	case "recover":
		if err := os.Remove(filepath.Join(c.dir, connectDirName, connectIdentityFileName)); err != nil {
			t.Fatal(err)
		}
		return stub, c, ownerRequest{op: op, args: []string{"recover", "--deployment", dep}, done: "recovered", fault: "recover", nextKey: true}
	case "reactivate":
		if code, rep := c.run("deactivate", "--yes"); code != exitcode.OK || rep["status"] != "deactivated" {
			t.Fatalf("deactivate = %d %v", code, rep)
		}
		return stub, c, ownerRequest{op: op, args: []string{"reactivate", "--deployment", dep}, done: "reactivated", fault: "reactivate", nextKey: true}
	case "deactivate":
		return stub, c, ownerRequest{op: op, args: []string{"deactivate", "--owner-approval", "--yes"}, done: "deactivated", fault: "delete"}
	}
	t.Fatalf("unknown owner request %q", op)
	return nil, nil, ownerRequest{}
}

// requestBodies returns the bodies of the pending-request POSTs the stub saw, in order.
func requestBodies(stub *connectStub) []stubSeen {
	seen, _ := stub.snapshot()
	var out []stubSeen
	for _, s := range seen {
		if s.method == http.MethodPost && s.path == connectv1.PathDeployments && !bytes.Contains(s.body, []byte(`"approval_id"`)) {
			out = append(out, s)
		}
	}
	return out
}

// TestConnectNewOwnerRequestsNeedExplicitPurchaseEvidence: without --evidence each new owner request
// stops locally with a usage diagnostic that says where the purchase credential goes, sends nothing,
// creates no key and writes no state; with --evidence <file> the request carries exactly that credential.
func TestConnectNewOwnerRequestsNeedExplicitPurchaseEvidence(t *testing.T) {
	for _, op := range []string{"recover", "reactivate", "deactivate"} {
		t.Run(op, func(t *testing.T) {
			stub, c, r := arrangeOwnerRequest(t, op)
			statePath := filepath.Join(c.dir, connectDirName, connectStateFileName)
			stateBefore := c.file(statePath)
			seenBefore, _ := stub.snapshot()

			code, rep := c.run(r.args...)
			diagnostic := c.lastError
			if code != exitcode.Usage {
				t.Fatalf("without --evidence: exit %d %v, want Usage", code, rep)
			}
			for _, want := range []string{"a new " + op + " request needs the current purchase credential", "--evidence <file>", "--evidence -", "nothing was sent"} {
				if !strings.Contains(diagnostic, want) {
					t.Fatalf("the diagnostic does not say %q:\n%s", want, diagnostic)
				}
			}
			if seen, _ := stub.snapshot(); len(seen) != len(seenBefore) {
				t.Fatalf("%d requests were sent without evidence", len(seen)-len(seenBefore))
			}
			if !bytes.Equal(c.file(statePath), stateBefore) {
				t.Fatal("a refused new request changed the state")
			}
			if _, err := os.Lstat(filepath.Join(c.dir, connectDirName, connectNextKeyFileName)); err == nil {
				t.Fatal("a refused new request created a proposed key")
			}

			code, rep = c.run(append(append([]string(nil), r.args...), "--evidence", c.purchaseEvidence())...)
			if code != exitcode.OK || rep["status"] != "approval_pending" {
				t.Fatalf("with --evidence <file>: %d %v", code, rep)
			}
			bodies := requestBodies(stub)
			var body map[string]any
			if err := json.Unmarshal(bodies[len(bodies)-1].body, &body); err != nil || body["evidence"] != c.purchase || body["operation"] != map[string]string{"recover": "recover", "reactivate": "reactivate", "deactivate": "delete"}[op] {
				t.Fatalf("the request did not carry the given purchase credential for %s: %v %v", op, err, body["operation"])
			}
			if _, err := os.Lstat(filepath.Join(c.dir, connectDirName, connectNextKeyFileName)); (err == nil) != r.nextKey {
				t.Fatalf("proposed key present = %v, want %v", err == nil, r.nextKey)
			}
			c.assertNoSecretsLeaked()
		})
	}
}

// TestConnectOwnerRequestEvidenceFromStdin: --evidence - reads the purchase credential from stdin.
func TestConnectOwnerRequestEvidenceFromStdin(t *testing.T) {
	stub, c, r := arrangeOwnerRequest(t, "recover")
	code, rep := c.runStdin(c.purchase+"\n", append(append([]string(nil), r.args...), "--evidence", "-")...)
	if code != exitcode.OK || rep["status"] != "approval_pending" {
		t.Fatalf("recover with --evidence -: %d %v", code, rep)
	}
	bodies := requestBodies(stub)
	var body map[string]any
	if err := json.Unmarshal(bodies[len(bodies)-1].body, &body); err != nil || body["evidence"] != c.purchase {
		t.Fatalf("the request did not carry the credential read from stdin: %v", err)
	}
	c.assertNoSecretsLeaked()
}

// TestConnectRecordedOwnerRequestReplaysWithoutReadingEvidence: once a new request is recorded, a
// repeat with an unreadable --evidence, with a different credential, or with none sends the SAME
// recorded bytes under the same key; after approval, a lost completion is repeated without evidence.
func TestConnectRecordedOwnerRequestReplaysWithoutReadingEvidence(t *testing.T) {
	for _, op := range []string{"recover", "reactivate", "deactivate"} {
		t.Run(op, func(t *testing.T) {
			stub, c, r := arrangeOwnerRequest(t, op)
			sendsBefore := len(requestBodies(stub)) // the bind request and its approved repeat
			code, rep := c.run(append(append([]string(nil), r.args...), "--evidence", c.purchaseEvidence())...)
			if code != exitcode.OK || rep["status"] != "approval_pending" {
				t.Fatalf("new request: %d %v", code, rep)
			}
			requestID := requestIDOf(t, rep)
			first := *c.state().Pending

			other := filepath.Join(t.TempDir(), "other-purchase.v3")
			if err := os.WriteFile(other, []byte(stub.credential("dep_other_purchase", 7, true, nil)+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, extra := range [][]string{{"--evidence", filepath.Join(t.TempDir(), "absent", "purchase.v3")}, {"--evidence", other}, nil} {
				code, rep := c.run(append(append([]string(nil), r.args...), extra...)...)
				if code != exitcode.OK || rep["status"] != "approval_pending" || requestIDOf(t, rep) != requestID {
					t.Fatalf("repeat with %v: %d %v", extra, code, rep)
				}
				if p := c.state().Pending; p == nil || p.IdempotencyKey != first.IdempotencyKey || p.Body != first.Body || p.BodySHA256 != first.BodySHA256 {
					t.Fatalf("repeat with %v rewrote the recorded request", extra)
				}
			}
			bodies := requestBodies(stub)[sendsBefore:]
			if len(bodies) != 4 {
				t.Fatalf("%d request sends, want the new request and three repeats", len(bodies))
			}
			for _, s := range bodies {
				if s.header.Get(connectv1.HeaderIdempotencyKey) != first.IdempotencyKey || connectv1.BodyDigest(s.body) != first.BodySHA256 {
					t.Fatal("a repeat sent other bytes or another key")
				}
			}

			stub.approve(requestID)
			stub.mu.Lock()
			stub.lose[r.fault] = 1
			stub.mu.Unlock()
			if code, _ := c.run(r.args...); code != exitcode.Indeterminate {
				t.Fatalf("lost completion answer: exit %d, want Indeterminate", code)
			}
			completion := *c.state().Pending
			if code, rep := c.run(append(append([]string(nil), r.args...), "--evidence", filepath.Join(t.TempDir(), "absent", "purchase.v3"))...); code != exitcode.OK || rep["status"] != r.done {
				t.Fatalf("completion repeat with an unreadable --evidence: %d %v", code, rep)
			}
			seen, _ := stub.snapshot()
			attempts := 0
			for _, s := range seen {
				if s.header.Get(connectv1.HeaderIdempotencyKey) == completion.IdempotencyKey {
					attempts++
					if connectv1.BodyDigest(s.body) != completion.BodySHA256 {
						t.Fatal("the completion repeat sent other bytes")
					}
				}
			}
			if attempts != 2 {
				t.Fatalf("%d completion sends, want the lost one and one repeat", attempts)
			}
			c.assertNoSecretsLeaked()
		})
	}
}

// TestConnectProofOfPossessionPathsNeedNoEvidence: start keeps its installed-purchase convenience, and
// refresh, rotate-key, status and bound-key deactivation run without --evidence.
func TestConnectProofOfPossessionPathsNeedNoEvidence(t *testing.T) {
	stub := newConnectStub(t)
	c := newConnectCLI(t, stub)
	c.bind()
	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"refresh"}, "refreshed"},
		{[]string{"rotate-key"}, "rotated"},
		{[]string{"status"}, "active"},
		{[]string{"deactivate", "--yes"}, "deactivated"},
	} {
		if code, rep := c.run(step.args...); code != exitcode.OK || rep["status"] != step.want {
			t.Fatalf("%v without --evidence: %d %v", step.args, code, rep)
		}
	}
	c.assertNoSecretsLeaked()
}

// TestConnectLostAnswerReportsTheActualRequestMethod: a connect POST whose answer is lost is reported
// with POST and the redacted origin, not with upgrade's GET wording.
func TestConnectLostAnswerReportsTheActualRequestMethod(t *testing.T) {
	stub := newConnectStub(t)
	c := newConnectCLI(t, stub)
	c.bind()
	stub.mu.Lock()
	stub.lose["refresh"] = 1
	stub.mu.Unlock()
	if code, _ := c.run("refresh"); code != exitcode.Indeterminate {
		t.Fatalf("lost refresh: exit %d, want Indeterminate", code)
	}
	diagnostic := c.lastError
	if !strings.Contains(diagnostic, "POST "+stub.srv.URL+": network error") || strings.Contains(diagnostic, "GET ") {
		t.Fatalf("the lost POST is not reported with its method:\n%s", diagnostic)
	}
	c.assertNoSecretsLeaked()
}
