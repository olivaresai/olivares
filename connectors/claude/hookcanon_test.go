// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHookCanonDirectorySymlink(t *testing.T) {
	realSecrets, link := hookCanonSymlinkedSecrets(t)
	db := filepath.Join(realSecrets, "db.pem")
	if err := os.WriteFile(db, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	body := hookCanonPayload(t, map[string]any{"file_path": filepath.Join(link, "db.pem")})
	out := mustCanon(t, body)

	if got, want := hookCanonInputString(t, out, "file_path"), db; got != want {
		t.Fatalf("canonical file_path = %q, want %q", got, want)
	}
}

func TestHookCanonFileSymlink(t *testing.T) {
	realSecrets, _ := hookCanonSymlinkedSecrets(t)
	db := filepath.Join(realSecrets, "db.pem")
	if err := os.WriteFile(db, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	link := filepath.Join(filepath.Dir(realSecrets), "db-link.pem")
	if err := os.Symlink(db, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	body := hookCanonPayload(t, map[string]any{"file_path": link})
	out := mustCanon(t, body)

	if got := hookCanonInputString(t, out, "file_path"); got != db {
		t.Fatalf("canonical symlink leaf file_path = %q, want %q", got, db)
	}
}

func TestHookCanonMissingLeafUnderDirectorySymlink(t *testing.T) {
	realSecrets, link := hookCanonSymlinkedSecrets(t)

	body := hookCanonPayload(t, map[string]any{"file_path": filepath.Join(link, "newfile.txt")})
	out := mustCanon(t, body)

	want := filepath.Join(realSecrets, "newfile.txt")
	if got := hookCanonInputString(t, out, "file_path"); got != want {
		t.Fatalf("canonical missing-leaf file_path = %q, want %q", got, want)
	}
}

func TestHookCanonNotebookPathSymlink(t *testing.T) {
	realSecrets, link := hookCanonSymlinkedSecrets(t)
	notebook := filepath.Join(realSecrets, "analysis.ipynb")
	if err := os.WriteFile(notebook, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	body := hookCanonPayload(t, map[string]any{"notebook_path": filepath.Join(link, "analysis.ipynb")})
	out := mustCanon(t, body)

	if got, want := hookCanonInputString(t, out, "notebook_path"), notebook; got != want {
		t.Fatalf("canonical notebook_path = %q, want %q", got, want)
	}
}

func TestHookCanonicalizeExistingAncestorPathNoChange(t *testing.T) {
	dir := hookCanonTempDir(t)
	file := filepath.Join(dir, "plain.txt")
	if err := os.WriteFile(file, []byte("plain"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got, changed := mustAncestor(t, file)
	if changed {
		t.Fatalf("plain absolute path changed to %q", got)
	}
	if got != file {
		t.Fatalf("plain absolute path = %q, want %q", got, file)
	}

	got, changed = mustAncestor(t, "relative/plain.txt")
	if changed || got != "relative/plain.txt" {
		t.Fatalf("relative path = (%q, %v), want unchanged", got, changed)
	}
}

func TestHookCanonPayloadWithoutChangesReturnsOriginalBytes(t *testing.T) {
	dir := hookCanonTempDir(t)
	file := filepath.Join(dir, "plain.txt")
	if err := os.WriteFile(file, []byte("plain"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	body := []byte(`{"tool_input":{"file_path":` + hookCanonJSONString(file) + `},"tool_name":"Read"}`)

	out := mustCanon(t, body)
	if !bytes.Equal(out, body) {
		t.Fatalf("unchanged payload was reserialized:\n got %s\nwant %s", out, body)
	}
}

func TestHookCanonUnsupportedPayloadsReturnOriginalBytes(t *testing.T) {
	cases := map[string][]byte{
		"invalid_json":          []byte(`{"tool_input":`),
		"missing_tool_input":    []byte(`{"hook_event_name":"PreToolUse","tool_name":"Read"}`),
		"tool_input_not_object": []byte(`{"tool_input":[]}`),
		"file_path_not_string":  []byte(`{"tool_input":{"file_path":123}}`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			out := mustCanon(t, body)
			if !bytes.Equal(out, body) {
				t.Fatalf("payload changed:\n got %s\nwant %s", out, body)
			}
		})
	}
}

func TestHookClientForwardsCanonicalizedFilePath(t *testing.T) {
	realSecrets, link := hookCanonSymlinkedSecrets(t)
	wantPath := filepath.Join(realSecrets, "newfile.txt")
	gotPath := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			gotPath <- ""
			return
		}
		ti, _ := m["tool_input"].(map[string]any)
		s, _ := ti["file_path"].(string)
		gotPath <- s
		_, _ = w.Write([]byte(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}`))
	}))
	defer srv.Close()

	body := hookCanonPayload(t, map[string]any{"file_path": filepath.Join(link, "newfile.txt")})
	var out bytes.Buffer
	if err := RunHookClient(context.Background(), bytes.NewReader(body), &out, HookClientConfig{
		Endpoint: srv.URL,
		Client:   srv.Client(),
	}); err != nil {
		t.Fatalf("RunHookClient: %v", err)
	}
	got := <-gotPath
	if got != wantPath {
		t.Fatalf("forwarded file_path = %q, want %q", got, wantPath)
	}
}

func mustCanon(t *testing.T, body []byte) []byte {
	t.Helper()
	out, err := canonicalizeHookPayloadPaths(body)
	if err != nil {
		t.Fatalf("canonicalize payload: %v", err)
	}
	return out
}

func mustAncestor(t *testing.T, path string) (string, bool) {
	t.Helper()
	got, changed, err := canonicalizeExistingAncestorPath(path)
	if err != nil {
		t.Fatalf("canonicalize ancestor: %v", err)
	}
	return got, changed
}

func hookCanonTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonical temp dir: %v", err)
	}
	return dir
}

func hookCanonSymlinkedSecrets(t *testing.T) (realSecrets, link string) {
	t.Helper()
	tmp := hookCanonTempDir(t)
	realSecrets = filepath.Join(tmp, "real", "secrets")
	if err := os.MkdirAll(realSecrets, 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	link = filepath.Join(tmp, "link")
	if err := os.Symlink(realSecrets, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	return realSecrets, link
}

func hookCanonPayload(t *testing.T, input map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "Read",
		"tool_input":      input,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}

func hookCanonInputString(t *testing.T, body []byte, key string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	ti, ok := m["tool_input"].(map[string]any)
	if !ok {
		t.Fatalf("missing tool_input in %s", body)
	}
	s, _ := ti[key].(string)
	return s
}

func hookCanonJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
