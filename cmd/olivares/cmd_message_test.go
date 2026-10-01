// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestMessageCLIPrivateCredentialPrecedenceAndDenial(t *testing.T) {
	for _, tc := range []struct {
		name, session, explicit string
		empty                   bool
		status                  int
	}{
		{name: "session before operator", session: "session-communication", status: 200},
		{name: "explicit token", session: "session-communication", explicit: "explicit-communication", status: 200},
		{name: "explicit empty", session: "session-communication", empty: true, status: 200},
		{name: "denial never retries", session: "session-communication", status: 403},
		{name: "newline refuses", session: "bad\ncommunication", status: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OLIVARES_COMMUNICATION_TOKEN", tc.session)
			t.Setenv("OLIVARES_TOKEN", "operator-fixture")
			t.Setenv("OLIVARES_WORK_TOKEN", "worker-fixture")
			t.Setenv(cliConfigOverrideEnv, filepath.Join(t.TempDir(), "absent.yaml"))
			count := 0
			want := tc.session
			if tc.explicit != "" {
				want = tc.explicit
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count++
				if r.Header.Get("Authorization") != "Bearer "+want {
					t.Error("wrong message credential")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"fixture":"` + want + `"}`))
			}))
			defer srv.Close()
			cmd := newMessageCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			args := []string{"get", testWorkItemID, "--server", srv.URL, "--tenant", "tenant-a"}
			if tc.empty {
				args = append(args, "--token=")
			}
			if tc.explicit != "" {
				args = append(args, "--token", tc.explicit)
			}
			cmd.SetArgs(args)
			err := cmd.Execute()
			invalid := tc.empty || strings.Contains(tc.session, "\n")
			if invalid && (err == nil || count != 0) {
				t.Fatal("invalid token reached HTTP")
			}
			if !invalid && count != 1 {
				t.Fatal("message request retried or missing")
			}
			if tc.status == 403 && err == nil {
				t.Fatal("denial hidden")
			}
			for _, token := range []string{want, "operator-fixture", "worker-fixture"} {
				if strings.Contains(out.String(), token) || err != nil && strings.Contains(err.Error(), token) {
					t.Fatal("CLI disclosed credential")
				}
			}
		})
	}
}

func TestMessageCLIHandoffTransportAndDenial(t *testing.T) {
	t.Setenv("OLIVARES_COMMUNICATION_TOKEN", "session-communication")
	t.Setenv("OLIVARES_TOKEN", "operator-fixture")
	t.Setenv(cliConfigOverrideEnv, filepath.Join(t.TempDir(), "absent.yaml"))
	id, workspace, channel, key := model.NewID().String(), model.NewID().String(), model.NewID().String(), model.NewID().String()
	for _, tc := range []struct {
		action, path, method string
		args                 []string
		input                string
		deny                 bool
	}{
		{"inbox", "/inbox/handoffs", "GET", []string{"--workspace-id", workspace, "--state", "accepted"}, "", false},
		{"get", "/deliveries/" + id + "/handoff", "GET", []string{id}, "", false},
		{"offer", "/handoffs", "POST", []string{"--channel-id", channel, "--work-item-id", id, "--to-sid", "osn_" + model.NewID().String(), "--owner-epoch", "2", "--version", "3", "--ack-deadline", "2026-10-01T12:00:00Z", "--context-file", "-", "--idempotency-key", key}, `{"summary":"fixture","next_action":"review"}`, false},
		{"respond", "/handoffs/" + id + "/responses", "POST", []string{id, "--transition", "reject", "--reason-file", "-", "--version", "1", "--idempotency-key", key}, `{"code":"changes_requested","text":"fixture review"}`, true},
	} {
		t.Run(tc.action, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "Bearer session-communication" || r.Method != tc.method || r.URL.Path != workAPIBase+tc.path {
					t.Error("handoff request changed its authority or endpoint")
				}
				if tc.method == "POST" && (r.Header.Get("Idempotency-Key") != key || r.Header.Get("If-Match") == "") {
					t.Error("handoff preconditions missing")
				}
				if tc.action == "inbox" && (r.URL.Query().Get("workspace_id") != workspace || r.URL.Query().Get("state") != "accepted") {
					t.Error("handoff inbox lost workspace/state")
				}
				if tc.deny {
					w.WriteHeader(403)
				}
				_, _ = w.Write([]byte(`{"fixture":"session-communication"}`))
			}))
			defer srv.Close()
			cmd := newMessageCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetIn(strings.NewReader(tc.input))
			args := append([]string{"handoff", tc.action}, tc.args...)
			args = append(args, "--server", srv.URL, "--tenant", "tenant-a")
			cmd.SetArgs(args)
			err := cmd.Execute()
			if calls != 1 || (err != nil) != tc.deny {
				t.Fatalf("handoff CLI calls=%d error=%v", calls, err)
			}
			if strings.Contains(out.String(), "session-communication") || err != nil && strings.Contains(err.Error(), "session-communication") {
				t.Fatal("CLI leaked communication credential")
			}
		})
	}
}
