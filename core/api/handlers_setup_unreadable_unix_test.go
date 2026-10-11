// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package api_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/secure"
)

// A setup token the engine cannot read answered the same bare 403 as a wrong one and
// logged nothing (#530), so neither the person at the console nor the operator could
// tell why setup refused. It answers its own code now, without the path, and the log
// names the file and the errno once. A wrong or used token answers as it always did.
func TestSetupNamesAnUnreadableTokenFileAndLogsItsCause(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup.token")
	tok := secure.NewSetupToken(path)
	plaintext, _, err := tok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	h := newHarnessOpts(t, func(o *api.Options) {
		o.SetupToken = tok
		o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	})
	setup := func(token string) resp {
		return h.do("POST", "/v1/setup", "", map[string]any{"token": token, "email": "root@x.io", "password": "supersecret1"}, nil)
	}
	const refused = `{"error":{"code":"forbidden","message":"forbidden"}}`

	if r := setup("olst_wrong"); r.code != http.StatusForbidden || strings.TrimSpace(r.raw) != refused {
		t.Fatalf("wrong token = %d %s, want 403 %s", r.code, r.raw, refused)
	}

	// The engine's account may not open the file: what a token written by another
	// account looks like to it (#514). Root opens any file, so as root the file is
	// made unreadable the other way the engine refuses it: a mode wider than 0600.
	mode, reason := os.FileMode(0o200), "permission denied"
	if os.Geteuid() == 0 {
		mode, reason = 0o640, "too open"
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	// Whatever the caller presents: the engine cannot tell, so it does not pretend to.
	if r := setup("olst_wrong"); r.code != http.StatusServiceUnavailable {
		t.Fatalf("wrong token, unreadable file = %d %s, want 503", r.code, r.raw)
	}
	logs.Reset()
	r := setup(plaintext)
	if r.code != http.StatusServiceUnavailable || r.hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("unreadable token = %d (Cache-Control %q) %s, want 503 no-store", r.code, r.hdr.Get("Cache-Control"), r.raw)
	}
	e, _ := r.body["error"].(map[string]any)
	msg, _ := e["message"].(string)
	if e["code"] != "setup_token_unreadable" || !strings.Contains(msg, "first-boot") {
		t.Fatalf("unreadable token body = %s, want code setup_token_unreadable and the remedy", r.raw)
	}
	if strings.Contains(r.raw, path) || strings.Contains(r.raw, filepath.Dir(path)) || strings.Contains(r.raw, plaintext) {
		t.Fatalf("the response carries the path or the token: %s", r.raw)
	}
	var cause []string
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if strings.Contains(line, plaintext) {
			t.Fatalf("a log line carries the token: %s", line)
		}
		if strings.Contains(line, path) {
			cause = append(cause, line)
		}
	}
	if len(cause) != 1 || !strings.Contains(cause[0], reason) || !strings.Contains(cause[0], "setup_token_unreadable") {
		t.Fatalf("want one log line with the code, the path and %q, got %q in:\n%s", reason, cause, logs.String())
	}

	// Readable again: the token works once, and a used token answers as before.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if r := setup(plaintext); r.code != http.StatusCreated {
		t.Fatalf("setup after the fix = %d %s, want 201", r.code, r.raw)
	}
	if r := setup(plaintext); r.code != http.StatusForbidden || strings.TrimSpace(r.raw) != refused {
		t.Fatalf("used token = %d %s, want 403 %s", r.code, r.raw, refused)
	}
}
