// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// TestExitCodeContract exercises the table `olivares --help` publishes, end to
// end, through real command invocations rather than through the exitcode
// package's own vocabulary.
//
// The gap it closes is the transport one. httpErr already classified HTTP
// statuses, but it only ever sees a RESPONSE: an unreachable control plane
// returns a Go error from client.Do, which exitcode.From could only read as
// generic. Measured before: `dial tcp: connection refused` exited 1, so a
// CI script could not distinguish a dead engine from a malformed request. Now
// every CLI network path goes through cliDo, which states it once.
func TestExitCodeContract(t *testing.T) {
	// A closed port on the loopback interface: connection refused, immediately.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	const tenant = "00000000-0000-0000-0000-000000000000"

	for _, tc := range []struct {
		name string
		argv []string
		want int
	}{
		{"unknown top-level command", []string{"zzqq-not-a-command"}, exitcode.Usage},
		{"unknown subcommand in a group", []string{"agent", "zzqq-not-a-subcommand"}, exitcode.Usage},
		{"unknown flag", []string{"status", "--zzqq-not-a-flag"}, exitcode.Usage},
		{"engine down (status)", []string{"status", "--server", deadURL}, exitcode.Server},
		{"engine down (agent session ls)", []string{
			"agent", "session", "ls", "--server", deadURL, "--token", "t", "--tenant", tenant,
		}, exitcode.Server},
		{"client context not found", []string{"auth", "use-context", "zzqq-no-such-context"}, exitcode.NotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("OLIVARES_CLI_CONFIG", t.TempDir()+"/config.yaml")
			root := newRootCmd()
			var out, errb bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&errb)
			root.SetArgs(tc.argv)
			_, err := root.ExecuteC()
			if err == nil {
				t.Fatalf("`olivares %v` succeeded; expected exit %d", tc.argv, tc.want)
			}
			if got := exitcode.From(err); got != tc.want {
				t.Fatalf("exit = %d, want %d: %v", got, tc.want, err)
			}
		})
	}
}

// TestHTTPStatusesKeepTheirExitCodes covers the half httpErr already owned, so
// the two halves of the contract are asserted in one place and a change to
// either is visible here.
func TestHTTPStatusesKeepTheirExitCodes(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   int
	}{
		{http.StatusUnauthorized, exitcode.Auth},
		{http.StatusForbidden, exitcode.Auth},
		{http.StatusNotFound, exitcode.NotFound},
		{http.StatusConflict, exitcode.Conflict},
		{http.StatusInternalServerError, exitcode.Server},
		{http.StatusBadGateway, exitcode.Server},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":"nope"}`))
			}))
			t.Cleanup(srv.Close)

			t.Chdir(t.TempDir())
			t.Setenv("OLIVARES_CLI_CONFIG", t.TempDir()+"/config.yaml")
			root := newRootCmd()
			var out, errb bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&errb)
			root.SetArgs([]string{"agent", "session", "ls",
				"--server", srv.URL, "--token", "t",
				"--tenant", "00000000-0000-0000-0000-000000000000"})
			_, err := root.ExecuteC()
			if err == nil {
				t.Fatalf("HTTP %d must fail", tc.status)
			}
			if got := exitcode.From(err); got != tc.want {
				t.Fatalf("HTTP %d exit = %d, want %d: %v", tc.status, got, tc.want, err)
			}
		})
	}
}
