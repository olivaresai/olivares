// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

// license_connect_capacity_test.go: an owner-approved bind, recover or reactivate completion that the
// service refuses for capacity alone (409 quota_exhausted or order_form_policy_required) keeps the
// approved request and the exact pending completion, so the same command completes later with the same
// approval and the same Idempotency-Key; every other refusal, the request step, ordinary operations and
// owner-approved deletion end as before, and a lost successful completion replays exactly once
// (assessments/integration/r115-commercial-composed-joint/capacity-correction-1).

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/license/connectv1"
)

var capacityRefusalCodes = []string{"quota_exhausted", "order_form_policy_required"}

// refusalHookFor names the stub step that answers the completion of op.
func refusalHookFor(op string) string {
	switch op {
	case "bind":
		return "complete"
	case "deactivate":
		return "delete"
	}
	return op
}

// arrangeApprovedCompletion leaves a data directory whose owner-approved op request is approved: the
// next run of the same command learns the approval and sends the completion. It returns that command
// (without --evidence), its completed status and the request id.
func arrangeApprovedCompletion(t *testing.T, op string) (*connectStub, *connectCLI, []string, string, string) {
	t.Helper()
	if op == "bind" {
		stub := newConnectStub(t)
		c := newConnectCLI(t, stub)
		code, rep := c.start()
		if code != exitcode.OK || rep["status"] != "approval_pending" {
			t.Fatalf("start = %d %v", code, rep)
		}
		requestID := requestIDOf(t, rep)
		stub.approve(requestID)
		return stub, c, []string{"start", "--endpoint", stub.srv.URL, "--business-id", "bus_1", "--holder-id", "sub_1", "--license-id", "lic_1"}, "bound", requestID
	}
	stub, c, r := arrangeOwnerRequest(t, op)
	code, rep := c.run(append(append([]string(nil), r.args...), "--evidence", c.purchaseEvidence())...)
	if code != exitcode.OK || rep["status"] != "approval_pending" {
		t.Fatalf("%s request = %d %v", op, code, rep)
	}
	requestID := requestIDOf(t, rep)
	stub.approve(requestID)
	return stub, c, r.args, r.done, requestID
}

// connectKeyFiles returns the bound and proposed identity files (nil when absent).
func connectKeyFiles(t *testing.T, c *connectCLI) [2][]byte {
	t.Helper()
	var out [2][]byte
	for i, name := range []string{connectIdentityFileName, connectNextKeyFileName} {
		b, err := os.ReadFile(filepath.Join(c.dir, connectDirName, name))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		out[i] = b
	}
	return out
}

// sendsWithKey returns the effect requests (not challenges) that carried the Idempotency-Key key.
func sendsWithKey(seen []stubSeen, key string) []stubSeen {
	var out []stubSeen
	for _, s := range seen {
		if s.path != connectv1.PathChallenges && s.header.Get(connectv1.HeaderIdempotencyKey) == key {
			out = append(out, s)
		}
	}
	return out
}

// TestConnectApprovedCompletionRefusedForCapacityKeepsTheApproval: the refused run returns the service's
// conflict and says the approval is kept; it installs nothing, replaces no key and sends the completion
// once; the same command, once capacity allows, sends only that kept completion (same key and bytes)
// and completes without another approval.
func TestConnectApprovedCompletionRefusedForCapacityKeepsTheApproval(t *testing.T) {
	for _, op := range []string{"bind", "recover", "reactivate"} {
		for _, code := range capacityRefusalCodes {
			t.Run(op+"/"+code, func(t *testing.T) {
				stub, c, args, done, requestID := arrangeApprovedCompletion(t, op)
				hook := refusalHookFor(op)
				licenseBefore, keysBefore := c.license(), connectKeyFiles(t, c)
				stub.mu.Lock()
				stub.refuse[hook] = stubRefusal{http.StatusConflict, code}
				depsBefore := len(stub.deps)
				stub.mu.Unlock()
				seenBefore, _ := stub.snapshot()

				exit, rep := c.run(args...)
				if exit != exitcode.Conflict {
					t.Fatalf("capacity-refused completion: exit %d %v, want Conflict", exit, rep)
				}
				for _, want := range []string{code, "run the same command again"} {
					if !strings.Contains(c.lastError, want) {
						t.Fatalf("the diagnostic does not say %q: %s", want, c.lastError)
					}
				}
				st := c.state()
				if st.Pending == nil || st.Pending.Intent != op || st.Pending.Phase != "complete" || st.Request == nil || st.Request.RequestID != requestID || st.Completion != nil {
					t.Fatalf("the approved request and its completion were not kept: pending %+v request %+v", st.Pending, st.Request)
				}
				kept := *st.Pending
				keysAfter := connectKeyFiles(t, c)
				if !bytes.Equal(c.license(), licenseBefore) || !bytes.Equal(keysAfter[0], keysBefore[0]) || !bytes.Equal(keysAfter[1], keysBefore[1]) {
					t.Fatal("a refused completion installed a credential or replaced a key")
				}
				stub.mu.Lock()
				depsAfter := len(stub.deps)
				delete(stub.refuse, hook)
				stub.mu.Unlock()
				if depsAfter != depsBefore {
					t.Fatal("a refused completion created a deployment")
				}
				seenRefused, _ := stub.snapshot()
				if n := len(sendsWithKey(seenRefused[len(seenBefore):], kept.IdempotencyKey)); n != 1 {
					t.Fatalf("the refused run sent the completion %d times, want exactly once", n)
				}

				exit, rep = c.run(args...)
				if exit != exitcode.OK || rep["status"] != done {
					t.Fatalf("the same command after capacity allows: exit %d %v, want %s", exit, rep, done)
				}
				seenAfter, _ := stub.snapshot()
				sends := 0
				for _, s := range seenAfter[len(seenRefused):] {
					if s.path == connectv1.PathChallenges {
						continue
					}
					sends++
					if s.header.Get(connectv1.HeaderIdempotencyKey) != kept.IdempotencyKey || connectv1.BodyDigest(s.body) != kept.BodySHA256 {
						t.Fatalf("the retry sent %s %s instead of the kept completion", s.method, s.path)
					}
				}
				if sends != 1 {
					t.Fatalf("the retry sent %d effect requests, want the one kept completion", sends)
				}
				if st := c.state(); st.Pending != nil || st.Request != nil {
					t.Fatalf("a completed approval left state behind: pending %+v request %+v", st.Pending, st.Request)
				}
				c.assertNoSecretsLeaked()
			})
		}
	}
}

// TestConnectOtherApprovedCompletionRefusalsStillEndTheRequest: a stale generation, an expired or
// missing approval and a lost current authority end the approved request and its completion as before.
func TestConnectOtherApprovedCompletionRefusalsStillEndTheRequest(t *testing.T) {
	for _, tc := range []struct {
		op     string
		status int
		code   string
		exit   int
	}{
		{"bind", http.StatusConflict, "generation_stale", exitcode.Conflict},
		{"bind", http.StatusForbidden, "approval_required", exitcode.Auth},
		{"bind", http.StatusForbidden, "authority_denied", exitcode.Auth},
		{"recover", http.StatusConflict, "generation_stale", exitcode.Conflict},
		{"recover", http.StatusForbidden, "recovery_required", exitcode.Auth},
		{"reactivate", http.StatusForbidden, "authority_denied", exitcode.Auth},
	} {
		t.Run(tc.op+"/"+tc.code, func(t *testing.T) {
			stub, c, args, _, _ := arrangeApprovedCompletion(t, tc.op)
			licenseBefore := c.license()
			stub.mu.Lock()
			stub.refuse[refusalHookFor(tc.op)] = stubRefusal{tc.status, tc.code}
			stub.mu.Unlock()
			if exit, rep := c.run(args...); exit != tc.exit {
				t.Fatalf("exit %d %v, want %d", exit, rep, tc.exit)
			}
			if st := c.state(); st.Pending != nil || st.Request != nil {
				t.Fatalf("a %d %s completion refusal kept the request: pending %+v request %+v", tc.status, tc.code, st.Pending, st.Request)
			}
			if strings.Contains(c.lastError, "run the same command again") || !bytes.Equal(c.license(), licenseBefore) {
				t.Fatalf("unexpected diagnostic or license change: %s", c.lastError)
			}
		})
	}
}

// TestConnectCapacityRefusalOutsideApprovedCompletionsIsUnchanged: the exception belongs only to approved
// bind/recover/reactivate completions. A refused refresh, a refused request step and a refused
// owner-approved deletion end their steps exactly as before.
func TestConnectCapacityRefusalOutsideApprovedCompletionsIsUnchanged(t *testing.T) {
	t.Run("refresh", func(t *testing.T) {
		stub := newConnectStub(t)
		c := newConnectCLI(t, stub)
		c.bind()
		licenseBefore := c.license()
		stub.mu.Lock()
		stub.refuse["refresh"] = stubRefusal{http.StatusConflict, "quota_exhausted"}
		stub.mu.Unlock()
		if exit, rep := c.run("refresh"); exit != exitcode.Conflict {
			t.Fatalf("refresh = %d %v, want Conflict", exit, rep)
		}
		if st := c.state(); st.Pending != nil || strings.Contains(c.lastError, "run the same command again") || !bytes.Equal(c.license(), licenseBefore) {
			t.Fatalf("a capacity-refused refresh kept its step or changed the license: %+v %s", st.Pending, c.lastError)
		}
	})
	t.Run("request", func(t *testing.T) {
		stub := newConnectStub(t)
		c := newConnectCLI(t, stub)
		stub.mu.Lock()
		stub.refuse["request"] = stubRefusal{http.StatusConflict, "order_form_policy_required"}
		stub.mu.Unlock()
		if exit, rep := c.start(); exit != exitcode.Conflict {
			t.Fatalf("start = %d %v, want Conflict", exit, rep)
		}
		if st := c.state(); st.Pending != nil || st.Request != nil {
			t.Fatalf("a capacity-refused request step was kept: %+v %+v", st.Pending, st.Request)
		}
	})
	t.Run("owner-approved deactivate", func(t *testing.T) {
		stub, c, args, _, _ := arrangeApprovedCompletion(t, "deactivate")
		stub.mu.Lock()
		stub.refuse["delete"] = stubRefusal{http.StatusConflict, "quota_exhausted"}
		stub.mu.Unlock()
		if exit, rep := c.run(args...); exit != exitcode.Conflict {
			t.Fatalf("deactivate = %d %v, want Conflict", exit, rep)
		}
		if st := c.state(); st.Pending != nil || st.Request != nil || strings.Contains(c.lastError, "run the same command again") {
			t.Fatalf("a refused owner-approved deletion kept its request: %+v %+v %s", st.Pending, st.Request, c.lastError)
		}
	})
}

// TestConnectKeptCompletionReplaysALostAnswerOnce: after a capacity refusal, a successful completion whose
// answer is lost is repeated with the same key and bytes and served once: one deployment or one key
// transition, and no state left behind.
func TestConnectKeptCompletionReplaysALostAnswerOnce(t *testing.T) {
	for _, op := range []string{"bind", "recover", "reactivate"} {
		t.Run(op, func(t *testing.T) {
			stub, c, args, done, _ := arrangeApprovedCompletion(t, op)
			hook := refusalHookFor(op)
			stub.mu.Lock()
			stub.refuse[hook] = stubRefusal{http.StatusConflict, "quota_exhausted"}
			stub.mu.Unlock()
			if exit, rep := c.run(args...); exit != exitcode.Conflict {
				t.Fatalf("refused completion = %d %v, want Conflict", exit, rep)
			}
			st := c.state()
			if st.Pending == nil || st.Pending.Phase != "complete" {
				t.Fatalf("the refused completion was not kept: %+v", st.Pending)
			}
			kept := *st.Pending
			stub.mu.Lock()
			delete(stub.refuse, hook)
			stub.lose[hook] = 1
			depsBefore := len(stub.deps)
			transitionsBefore := 0
			for _, n := range stub.transitions {
				transitionsBefore += n
			}
			stub.mu.Unlock()
			if exit, rep := c.run(args...); exit != exitcode.Indeterminate {
				t.Fatalf("lost completion answer = %d %v, want Indeterminate", exit, rep)
			}
			if st := c.state(); st.Pending == nil || st.Pending.IdempotencyKey != kept.IdempotencyKey || st.Pending.BodySHA256 != kept.BodySHA256 {
				t.Fatal("the lost completion is not the kept one")
			}
			if exit, rep := c.run(args...); exit != exitcode.OK || rep["status"] != done {
				t.Fatalf("replay = %d %v, want %s", exit, rep, done)
			}
			seen, _ := stub.snapshot()
			if n := len(sendsWithKey(seen, kept.IdempotencyKey)); n != 3 {
				t.Fatalf("%d sends of the kept completion, want the refused, the lost and the replayed one", n)
			}
			stub.mu.Lock()
			deps := len(stub.deps)
			transitions := 0
			for _, n := range stub.transitions {
				transitions += n
			}
			stub.mu.Unlock()
			if op == "bind" && deps != depsBefore+1 {
				t.Fatalf("%d deployments created, want exactly one", deps-depsBefore)
			}
			if op != "bind" && transitions != transitionsBefore+1 {
				t.Fatalf("%d key transitions committed, want exactly one", transitions-transitionsBefore)
			}
			if st := c.state(); st.Pending != nil || st.Request != nil {
				t.Fatalf("state left behind: %+v %+v", st.Pending, st.Request)
			}
			c.assertNoSecretsLeaked()
		})
	}
}
