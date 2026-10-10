// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestAccountPasswordCLI(t *testing.T) {
	t.Setenv("OLIVARES_CLI_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("OLIVARES_TOKEN", "olvs_test-session")
	bodies := make(chan map[string]string, 8)
	refuse := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/account/password" || r.Header.Get("Authorization") != "Bearer olvs_test-session" {
			t.Error("wrong authenticated password-change transport")
			w.WriteHeader(404)
			return
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		bodies <- body
		if refuse {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":{"code":"current_password_incorrect","message":"The current password is incorrect."}}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	t.Setenv("OLIVARES_SERVER_URL", srv.URL)
	old := filepath.Join(t.TempDir(), "current")
	next := filepath.Join(t.TempDir(), "new")
	for file, value := range map[string]string{old: "current-secret\n", next: "replacement-secret\n"} {
		if err := os.WriteFile(file, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	out, stderr, err := execRoot(t, "account", "password", "--current-password-file", old, "--password-file", next)
	if err != nil {
		t.Fatalf("file password change: %v", err)
	}
	if body := <-bodies; len(body) != 2 || body["current_password"] != "current-secret" || body["new_password"] != "replacement-secret" {
		t.Fatal("file passwords were not sent in the native request")
	}
	if !strings.Contains(out, "Password changed.") || strings.Contains(out+stderr, "current-secret") || strings.Contains(out+stderr, "replacement-secret") {
		t.Fatal("success output missing or password exposed")
	}
	refuse = true
	_, _, err = execRoot(t, "account", "password", "--current-password-file", old, "--password-file", next)
	if err == nil || exitcode.From(err) != exitcode.Err || !strings.Contains(err.Error(), "The current password is incorrect.") {
		t.Fatalf("engine refusal = %v", err)
	}
	<-bodies
	refuse = false
	prevTTY, prevHidden := interactiveStdin, readHiddenInput
	t.Cleanup(func() { interactiveStdin, readHiddenInput = prevTTY, prevHidden })
	interactiveStdin = func(io.Reader) bool { return true }
	hidden := 0
	readHiddenInput = func(_ io.Reader, reader *bufio.Reader) (string, error) {
		hidden++
		line, err := reader.ReadString('\n')
		return strings.TrimSuffix(line, "\n"), err
	}
	root := newRootCmd()
	var stdout, prompt strings.Builder
	root.SetOut(&stdout)
	root.SetErr(&prompt)
	root.SetIn(strings.NewReader("current-secret\nreplacement-secret\nreplacement-secret\n"))
	root.SetArgs([]string{"account", "password"})
	if err := root.Execute(); err != nil {
		t.Fatalf("terminal password change: %v", err)
	}
	if hidden != 3 || !strings.Contains(prompt.String(), "Current password: ") || !strings.Contains(prompt.String(), "Confirm new password: ") {
		t.Fatal("passwords did not use the existing hidden terminal reader")
	}
	<-bodies
	root = newRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetIn(strings.NewReader("current-secret\nreplacement-secret\ndifferent-secret\n"))
	root.SetArgs([]string{"account", "password"})
	if err := root.Execute(); exitcode.From(err) != exitcode.Usage {
		t.Fatalf("mismatched confirmation = %v", err)
	}
	select {
	case <-bodies:
		t.Fatal("confirmation mismatch sent a password")
	default:
	}
	interactiveStdin = func(io.Reader) bool { return false }
	if _, _, err := execRoot(t, "account", "password"); exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), "--current-password-file") {
		t.Fatalf("script without files = %v", err)
	}
	if _, _, err := execRoot(t, "account", "password", "--current-password-file", "-", "--password-file", "-"); exitcode.From(err) != exitcode.Usage {
		t.Fatalf("two stdin passwords = %v", err)
	}
	if _, _, err := execRoot(t, "account", "password", "--token-file", "-", "--current-password-file", "-", "--password-file", next); exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), "separate files") {
		t.Fatalf("token and password shared stdin = %v", err)
	}

}
