// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package console

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"github.com/olivaresai/olivares/appliance/layer/portal/localclient"
)

type session struct {
	view   hostops.View
	err    error
	calls  int
	closed bool
}

func (s *session) Operation(string, string) (hostops.View, int, error) {
	s.calls++
	return s.view, 200, s.err
}
func (s *session) Plan(string, string, string, json.RawMessage) (hostops.Plan, error) {
	s.calls++
	return hostops.Plan{Descriptor: hostops.StatusDescriptor(), Inputs: json.RawMessage(`{}`), Permissions: hostops.Permissions{Read: true, Plan: true, Code: "act_not_adopted"}}, s.err
}
func (s *session) Description(string, string, string) (hostops.Descriptor, error) {
	return hostops.StatusDescriptor(), s.err
}
func (s *session) Close() error { s.closed = true; return nil }

func TestCLI_OutputJSONIsTheOperationRecordWithExits012(t *testing.T) {
	record := hostops.Record{OperationID: strings.Repeat("12", 16), TaskID: "task.one", Module: "host", Verb: "status", Target: "system", PlanDigest: strings.Repeat("ab", 32), Mode: "product-up", Surface: "web", Actor: "user-1", State: hostops.StateSucceeded, LogRef: "journal:OLIVARES_OPERATION_ID=" + strings.Repeat("12", 16), Postconditions: []string{"measured"}}
	for _, tc := range []struct {
		name string
		err  error
		exit int
		code string
	}{{"record", nil, 0, ""}, {"refused", &localclient.Refused{Code: "mode_refused"}, 1, "mode_refused"}, {"lost", io.EOF, 2, "session_lost"}} {
		t.Run(tc.name, func(t *testing.T) {
			s := &session{view: hostops.View{Record: record}, err: tc.err}
			var out, diagnostic bytes.Buffer
			code := Run(context.Background(), []string{"op", "get", record.OperationID, "--output", "json"}, strings.NewReader(""), &out, &diagnostic, func() (Session, error) { return s, nil })
			if code != tc.exit || !s.closed || s.calls != 1 {
				t.Fatalf("exit=%d calls=%d closed=%v", code, s.calls, s.closed)
			}
			if tc.exit == 0 {
				var got hostops.Record
				dec := json.NewDecoder(&out)
				dec.DisallowUnknownFields()
				if dec.Decode(&got) != nil || !reflect.DeepEqual(got, record) || dec.Decode(new(any)) != io.EOF || diagnostic.Len() != 0 {
					t.Fatalf("not exactly the record: %#v %s", got, diagnostic.String())
				}
			} else if out.Len() != 0 || strings.TrimSpace(diagnostic.String()) != tc.code {
				t.Fatalf("stdout=%q stderr=%q", out.String(), diagnostic.String())
			}
		})
	}
}

func TestCLI_RefusesCallerPathsAndDoesNotRedialAfterFailure(t *testing.T) {
	for _, args := range [][]string{{"--socket", "/tmp/other", "op", "get", strings.Repeat("12", 16)}, {"host", "status", "--actor", "root"}, {"host", "status", "--plan", "--output", "json"}} {
		calls := 0
		var out, diagnostic bytes.Buffer
		code := Run(context.Background(), args, strings.NewReader(""), &out, &diagnostic, func() (Session, error) { calls++; return &session{}, nil })
		if code != 1 || calls != 0 || strings.TrimSpace(diagnostic.String()) != "input_refused" {
			t.Fatalf("args=%v exit=%d dials=%d", args, code, calls)
		}
	}
	calls := 0
	var out, diagnostic bytes.Buffer
	code := Run(context.Background(), []string{"op", "get", strings.Repeat("12", 16)}, strings.NewReader(""), &out, &diagnostic, func() (Session, error) {
		calls++
		return nil, &localclient.Failure{Code: "response_unverified", Sent: true}
	})
	if code != 2 || calls != 1 || strings.Contains(diagnostic.String(), "Nothing was sent") {
		t.Fatalf("exit=%d dials=%d %s", code, calls, diagnostic.String())
	}
}

func TestCLIAndTUI_MissingTLSDeliveryNamesTheReasonImmediately(t *testing.T) {
	for _, args := range [][]string{{"op", "get", strings.Repeat("12", 16), "--output", "json"}, {"tui"}} {
		calls := 0
		var out, diagnostic bytes.Buffer
		code := Run(context.Background(), args, strings.NewReader(""), &out, &diagnostic, func() (Session, error) {
			calls++
			return nil, &localclient.Failure{Code: "local_unavailable", Reason: "portal_tls_not_delivered"}
		})
		if code != 2 || calls != 1 || out.Len() != 0 || strings.TrimSpace(diagnostic.String()) != "local_unavailable portal_tls_not_delivered" {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), diagnostic.String())
		}
	}
}

func TestCLI_WatchSucceedsOnlyForSucceededAndPrintsTheState(t *testing.T) {
	for _, state := range []string{"planned", "running", "succeeded", "failed", "partial", "rolled_back"} {
		for _, verb := range []string{"get", "watch"} {
			t.Run(verb+"/"+state, func(t *testing.T) {
				record := hostops.Record{OperationID: strings.Repeat("27", 16), State: state}
				s := &session{view: hostops.View{Record: record}}
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				var out, diagnostic bytes.Buffer
				code := Run(ctx, []string{"op", verb, record.OperationID, "--output", "json"}, strings.NewReader(""), &out, &diagnostic, func() (Session, error) { return s, nil })
				want := 2
				if verb == "get" || state == "succeeded" {
					want = 0
				}
				var got hostops.Record
				if code != want || json.Unmarshal(out.Bytes(), &got) != nil || got.State != state || s.calls != 1 || !s.closed {
					t.Fatalf("exit=%d state=%q output=%s calls=%d closed=%v", code, got.State, out.String(), s.calls, s.closed)
				}
			})
		}
	}
}
