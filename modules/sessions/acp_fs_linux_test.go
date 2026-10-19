// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/confine"
)

func mustRead(t *testing.T, path string) string {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(got)
}

func TestFileScopeWritesInsideTheFolder(t *testing.T) {
	dir := t.TempDir()
	scope := newFileScope(dir, false, PresetAsk, nil, nil, confine.Mounts{})
	if err := scope.writeFile(filepath.Join(dir, "a", "b", "new.txt"), "first line\nsecond line\n", nil); err != nil {
		t.Fatalf("new nested file: %v", err)
	}
	if got := mustRead(t, filepath.Join(dir, "a", "b", "new.txt")); got != "first line\nsecond line\n" {
		t.Fatalf("content = %q", got)
	}
	if err := scope.writeFile(filepath.Join(dir, "a", "b", "new.txt"), "short", nil); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if got := mustRead(t, filepath.Join(dir, "a", "b", "new.txt")); got != "short" {
		t.Fatalf("an overwrite kept the old tail: %q", got)
	}
}

func TestFileScopeReachesTheFolderThroughItsLinkedName(t *testing.T) {
	real := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	scope := newFileScope(alias, false, PresetAsk, nil, nil, confine.Mounts{})
	for _, name := range []string{filepath.Join(alias, "via-alias.txt"), filepath.Join(real, "via-real.txt")} {
		if err := scope.writeFile(name, "ok", nil); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestFileScopeRefusesWhatTheSessionFolderDoesNotHold(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(outside, "target.txt"), "untouched")
	writeFile(t, filepath.Join(dir, ".git", "hooks", "pre-commit"), "hook")
	writeFile(t, filepath.Join(dir, ".git", "config"), "config")
	writeFile(t, filepath.Join(dir, "sub", "ok.txt"), "ok")
	writeFile(t, filepath.Join(dir, "engine-data", "key"), "secret")
	for link, target := range map[string]string{
		"link-file":   filepath.Join(outside, "target.txt"),
		"link-dir":    outside,
		"link-hooks":  filepath.Join(dir, ".git", "hooks"),
		"link-config": filepath.Join(dir, ".git", "config"),
	} {
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	mounts := confine.Mounts{ReadOnly: []string{filepath.Join(dir, ".git", "hooks"), filepath.Join(dir, ".git", "config")}}
	policy := &confine.Policy{Protect: []string{filepath.Join(dir, "engine-data")}}
	scope := newFileScope(dir, false, PresetAsk, nil, policy, mounts)

	cases := []struct {
		name, path string
	}{
		{"relative path", "sub/ok.txt"},
		{"parent traversal", filepath.Join(dir, "..", "escape.txt")},
		{"another folder", filepath.Join(outside, "new.txt")},
		{"the folder itself", dir},
		{"a link to a file outside", filepath.Join(dir, "link-file")},
		{"through a link to a folder outside", filepath.Join(dir, "link-dir", "new.txt")},
		{"an existing hook", filepath.Join(dir, ".git", "hooks", "pre-commit")},
		{"a new hook", filepath.Join(dir, ".git", "hooks", "post-commit")},
		{"a new folder in the hooks", filepath.Join(dir, ".git", "hooks", "sub", "x")},
		{"the git configuration", filepath.Join(dir, ".git", "config")},
		{"a hook through a link to the hooks", filepath.Join(dir, "link-hooks", "pre-commit")},
		{"a new file through a link to the hooks", filepath.Join(dir, "link-hooks", "sub", "new")},
		{"the configuration through a link", filepath.Join(dir, "link-config")},
		{"a protected engine folder", filepath.Join(dir, "engine-data", "key")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := scope.writeFile(tc.path, "pwned", nil)
			var refusal fileRefusal
			if !errors.As(err, &refusal) {
				t.Fatalf("err = %v, want a refusal", err)
			}
		})
	}
	for path, want := range map[string]string{
		filepath.Join(outside, "target.txt"):              "untouched",
		filepath.Join(dir, ".git", "hooks", "pre-commit"): "hook",
		filepath.Join(dir, ".git", "config"):              "config",
		filepath.Join(dir, "engine-data", "key"):          "secret",
		filepath.Join(dir, "sub", "ok.txt"):               "ok",
	} {
		if got := mustRead(t, path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	for _, path := range []string{
		filepath.Join(dir, ".git", "hooks", "post-commit"), filepath.Join(dir, ".git", "hooks", "sub"),
		filepath.Join(outside, "new.txt"), filepath.Join(filepath.Dir(dir), "escape.txt"),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s exists after a refused write (%v)", path, err)
		}
	}
}

func TestFileScopeKeepsTheWorkspaceAllowedFolders(t *testing.T) {
	dir := t.TempDir()
	ws := &resolvedWorkspace{allowSubpaths: []string{"docs"}, readOnlyFolders: []string{filepath.Join(dir, "docs", "frozen")}}
	writeFile(t, filepath.Join(dir, "docs", "frozen", "a.md"), "frozen")
	scope := newFileScope(dir, false, PresetEditsOnly, ws, nil, confine.Mounts{})
	if err := scope.writeFile(filepath.Join(dir, "docs", "a.md"), "ok", nil); err != nil {
		t.Fatalf("inside the allowed folder: %v", err)
	}
	for _, path := range []string{filepath.Join(dir, "src", "a.go"), filepath.Join(dir, "docs", "frozen", "a.md")} {
		var refusal fileRefusal
		if err := scope.writeFile(path, "no", nil); !errors.As(err, &refusal) {
			t.Fatalf("%s: err = %v, want a refusal", path, err)
		}
	}
	if got := mustRead(t, filepath.Join(dir, "docs", "frozen", "a.md")); got != "frozen" {
		t.Fatalf("a read-only folder changed: %q", got)
	}
}

func TestNewFileScopeIsNilWhereTheSessionMayNotWrite(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name      string
		dir       string
		container bool
		preset    string
		ws        *resolvedWorkspace
	}{
		{"no folder", "", false, PresetAsk, nil},
		{"a container", dir, true, PresetAsk, nil},
		{"a read-only session", dir, false, PresetReadOnly, nil},
		{"a read-only workspace", dir, false, PresetAsk, &resolvedWorkspace{mountMode: mountRO}},
	}
	for _, tc := range cases {
		if scope := newFileScope(tc.dir, tc.container, tc.preset, tc.ws, nil, confine.Mounts{}); scope != nil {
			t.Errorf("%s: scope = %+v, want nil", tc.name, scope)
		}
	}
	if newFileScope(dir, false, PresetNone, &resolvedWorkspace{mountMode: mountNoDelete}, nil, confine.Mounts{}) == nil {
		t.Error("a write-without-delete workspace may still change files")
	}
}

// --- the protocol -------------------------------------------------------------

// liveTurn opens the turn a write belongs to: the client writes only for a live one.
func liveTurn(t *testing.T, peer *openCodePeer) {
	t.Helper()
	go func() { _, _ = peer.session.Input(context.Background(), "edit") }()
	peer.nextRequest()
}

func writeAnswer(t *testing.T, peer *openCodePeer, id string) map[string]any {
	t.Helper()
	var answer map[string]any
	waitFor(t, "the request was answered", func() bool {
		for _, line := range peer.sentLines() {
			var frame map[string]any
			if json.Unmarshal(line, &frame) == nil && frame["id"] == id && frame["method"] == nil {
				answer = frame
				return true
			}
		}
		return false
	})
	return answer
}

func errorCodeOf(answer map[string]any) int {
	body, _ := answer["error"].(map[string]any)
	code, _ := body["code"].(float64)
	return int(code)
}

func TestOpenCodeWriteTextFileAnswersAnEmptyResult(t *testing.T) {
	dir := t.TempDir()
	peer := newOpenCodePeer(t, func(cfg *DriverSessionConfig) {
		cfg.files = newFileScope(dir, false, PresetAsk, nil, nil, confine.Mounts{})
	})
	if _, err := peer.answerHandshake("ses_fs"); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	liveTurn(t, peer)
	peer.requestFromServer("w1", "fs/write_text_file", map[string]any{
		"sessionId": "ses_fs", "path": filepath.Join(dir, "x.txt"), "content": "hello",
	})
	answer := writeAnswer(t, peer, "w1")
	if answer["error"] != nil {
		t.Fatalf("answer = %v", answer)
	}
	if result, ok := answer["result"].(map[string]any); !ok || len(result) != 0 {
		t.Fatalf("result = %v, want an empty object", answer["result"])
	}
	if got := mustRead(t, filepath.Join(dir, "x.txt")); got != "hello" {
		t.Fatalf("content = %q", got)
	}
}

func TestOpenCodeWriteTextFileRefusals(t *testing.T) {
	dir := t.TempDir()
	scope := func() *fileScope { return newFileScope(dir, false, PresetAsk, nil, nil, confine.Mounts{}) }
	cases := []struct {
		name   string
		scope  *fileScope
		params map[string]any
		code   int
	}{
		{"no scope", nil, map[string]any{"sessionId": "ses_fs", "path": filepath.Join(dir, "x.txt"), "content": "no"}, acpErrMethodNotSupported},
		{"another conversation", scope(), map[string]any{"sessionId": "ses_other", "path": filepath.Join(dir, "x.txt"), "content": "no"}, acpErrInvalidParams},
		{"no conversation named", scope(), map[string]any{"path": filepath.Join(dir, "x.txt"), "content": "no"}, acpErrInvalidParams},
		{"outside the folder", scope(), map[string]any{"sessionId": "ses_fs", "path": filepath.Join(filepath.Dir(dir), "x.txt"), "content": "no"}, acpErrInvalidParams},
		{"a malformed request", scope(), map[string]any{"sessionId": "ses_fs", "path": 7, "content": "no"}, acpErrInvalidParams},
		{"no content", scope(), map[string]any{"sessionId": "ses_fs", "path": filepath.Join(dir, "x.txt")}, acpErrInvalidParams},
		{"a path with ..", scope(), map[string]any{"sessionId": "ses_fs", "path": filepath.Join(dir, "sub") + "/../x.txt", "content": "no"}, acpErrInvalidParams},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			peer := newOpenCodePeer(t, func(cfg *DriverSessionConfig) { cfg.files = tc.scope })
			if _, err := peer.answerHandshake("ses_fs"); err != nil {
				t.Fatalf("handshake: %v", err)
			}
			liveTurn(t, peer)
			peer.requestFromServer("w1", "fs/write_text_file", tc.params)
			if code := errorCodeOf(writeAnswer(t, peer, "w1")); code != tc.code {
				t.Fatalf("code = %d, want %d", code, tc.code)
			}
		})
	}
	for _, path := range []string{filepath.Join(dir, "x.txt"), filepath.Join(filepath.Dir(dir), "x.txt")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a refused write left %s: %v", path, err)
		}
	}
}

func TestOpenCodeWriteTextFileLosesItsAuthorityWithTheLaunch(t *testing.T) {
	dir := t.TempDir()
	peer := newOpenCodePeer(t, func(cfg *DriverSessionConfig) {
		cfg.files = newFileScope(dir, false, PresetAsk, nil, nil, confine.Mounts{})
		cfg.AuthorityCheck = func(context.Context) error { return errors.New("the launch authority moved") }
	})
	if _, err := peer.answerHandshake("ses_fs"); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	liveTurn(t, peer)
	peer.requestFromServer("w1", "fs/write_text_file", map[string]any{
		"sessionId": "ses_fs", "path": filepath.Join(dir, "x.txt"), "content": "no",
	})
	if code := errorCodeOf(writeAnswer(t, peer, "w1")); code != acpErrInvalidParams {
		t.Fatalf("code = %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a write without authority left a file: %v", err)
	}
}

func TestOpenCodeReadTextFileStaysUnsupported(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "x.txt"), "secret")
	peer := newOpenCodePeer(t, func(cfg *DriverSessionConfig) {
		cfg.files = newFileScope(dir, false, PresetAsk, nil, nil, confine.Mounts{})
	})
	if _, err := peer.answerHandshake("ses_fs"); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	liveTurn(t, peer)
	peer.requestFromServer("r1", "fs/read_text_file", map[string]any{"sessionId": "ses_fs", "path": filepath.Join(dir, "x.txt")})
	if code := errorCodeOf(writeAnswer(t, peer, "r1")); code != acpErrMethodNotSupported {
		t.Fatalf("code = %d", code)
	}
}

// The folder the engine judged can be replaced by a link into a protected path
// before the file is opened; the descriptor, not the earlier look, has the last word.
func TestFileScopeRefusesAFolderSwappedForALinkAfterItWasJudged(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".git", "hooks", "pre-commit"), "hook")
	writeFile(t, filepath.Join(dir, "plain", "pre-commit"), "plain")
	mounts := confine.Mounts{ReadOnly: []string{filepath.Join(dir, ".git", "hooks")}}
	scope := newFileScope(dir, false, PresetAsk, nil, nil, mounts)
	scope.afterJudged = func() {
		_ = os.Rename(filepath.Join(dir, "plain"), filepath.Join(dir, "plain.moved"))
		_ = os.Symlink(filepath.Join(".git", "hooks"), filepath.Join(dir, "plain"))
	}
	var refusal fileRefusal
	if err := scope.writeFile(filepath.Join(dir, "plain", "pre-commit"), "pwned", nil); !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if got := mustRead(t, filepath.Join(dir, ".git", "hooks", "pre-commit")); got != "hook" {
		t.Fatalf("the protected hook was rewritten: %q", got)
	}
}

func TestFileScopeWritesThroughALinkThatStaysInsideTheFolder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "docs", "a.md"), "old")
	if err := os.Symlink(filepath.Join(dir, "docs"), filepath.Join(dir, "docs-link")); err != nil {
		t.Fatal(err)
	}
	scope := newFileScope(dir, false, PresetAsk, nil, nil, confine.Mounts{})
	if err := scope.writeFile(filepath.Join(dir, "docs-link", "a.md"), "new", nil); err != nil {
		t.Fatalf("write through an inside link: %v", err)
	}
	if got := mustRead(t, filepath.Join(dir, "docs", "a.md")); got != "new" {
		t.Fatalf("content = %q", got)
	}
}

func TestOpenCodeWriteTextFileNeedsALiveTurnAndABoundConversation(t *testing.T) {
	dir := t.TempDir()
	peer := newOpenCodePeer(t, func(cfg *DriverSessionConfig) {
		cfg.files = newFileScope(dir, false, PresetAsk, nil, nil, confine.Mounts{})
	})
	params := map[string]any{"sessionId": "", "path": filepath.Join(dir, "x.txt"), "content": "no"}
	peer.requestFromServer("early", "fs/write_text_file", params)
	if code := errorCodeOf(writeAnswer(t, peer, "early")); code != acpErrInvalidParams {
		t.Fatalf("before the handshake: code = %d", code)
	}
	for len(peer.frames) > 0 {
		<-peer.frames // the answer above is not a request the handshake waits for
	}
	if _, err := peer.answerHandshake("ses_fs"); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	params["sessionId"] = "ses_fs"
	peer.requestFromServer("idle", "fs/write_text_file", params)
	if code := errorCodeOf(writeAnswer(t, peer, "idle")); code != acpErrInvalidParams {
		t.Fatalf("with no turn: code = %d", code)
	}
	for len(peer.frames) > 0 {
		<-peer.frames
	}
	liveTurn(t, peer)
	if _, err := peer.session.Interrupt(context.Background()); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	peer.requestFromServer("late", "fs/write_text_file", params)
	if code := errorCodeOf(writeAnswer(t, peer, "late")); code != acpErrInvalidParams {
		t.Fatalf("on a cancelled turn: code = %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a write with no live turn left a file: %v", err)
	}
}

// What a refusal cannot be allowed to leave behind or hang on.

func TestFileScopeRefusesALinkThatLeadsNowhere(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "docs", "a.md"), "ok")
	writeFile(t, filepath.Join(dir, ".git", "hooks", "pre-commit"), "hook")
	for link, target := range map[string]string{
		"docs/to-hook":    filepath.Join("..", ".git", "hooks", "post-commit"),
		"docs/to-src":     filepath.Join("..", "src", "new.go"),
		"docs/to-nowhere": filepath.Join("..", "docs", "missing.md"),
	} {
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	ws := &resolvedWorkspace{allowSubpaths: []string{"docs"}}
	scope := newFileScope(dir, false, PresetAsk, ws, nil, confine.Mounts{ReadOnly: []string{filepath.Join(dir, ".git", "hooks")}})
	for _, name := range []string{"to-hook", "to-src", "to-nowhere"} {
		var refusal fileRefusal
		if err := scope.writeFile(filepath.Join(dir, "docs", name), "pwned", nil); !errors.As(err, &refusal) {
			t.Fatalf("%s: err = %v, want a refusal", name, err)
		}
	}
	for _, path := range []string{
		filepath.Join(dir, ".git", "hooks", "post-commit"), filepath.Join(dir, "src"), filepath.Join(dir, "docs", "missing.md"),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s exists after a refused write (%v)", path, err)
		}
	}
}

func TestFileScopeDoesNotWriteOrWaitOnAPipe(t *testing.T) {
	dir := t.TempDir()
	pipe := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(pipe, 0o644); err != nil {
		t.Skipf("no named pipes here: %v", err)
	}
	scope := newFileScope(dir, false, PresetAsk, nil, nil, confine.Mounts{})
	write := func() error {
		done := make(chan error, 1)
		go func() { done <- scope.writeFile(pipe, "secret", nil) }()
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("a write to a named pipe is still waiting for a reader")
			return nil
		}
	}
	// No reader: the open must not wait for one.
	if err := write(); err == nil {
		t.Fatal("a write to a named pipe with no reader succeeded")
	}
	// A reader: the open succeeds, and still nothing is written into the pipe.
	reader, err := os.OpenFile(pipe, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var refusal fileRefusal
	if err := write(); !errors.As(err, &refusal) {
		t.Fatalf("a write to a named pipe with a reader: err = %v, want a refusal", err)
	}
	buf := make([]byte, 16)
	if n, _ := reader.Read(buf); n != 0 {
		t.Fatalf("the engine wrote %q into a named pipe", buf[:n])
	}
}

func TestFileScopeJudgesAProtectedPathThatDoesNotExistYet(t *testing.T) {
	real := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	// The folder is given by its linked name and .git/hooks is not there yet.
	scope := newFileScope(alias, false, PresetAsk, nil, nil, confine.Mounts{ReadOnly: []string{filepath.Join(alias, ".git", "hooks")}})
	var refusal fileRefusal
	if err := scope.writeFile(filepath.Join(real, ".git", "hooks", "pre-commit"), "pwned", nil); !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if _, err := os.Lstat(filepath.Join(real, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused write created %s (%v)", filepath.Join(real, ".git"), err)
	}
}

func TestChildSpecGivesTheFileScopeOnlyToASessionThatMayWrite(t *testing.T) {
	if !fileScopeSupported {
		t.Skip("the engine writes files for a session on Linux only")
	}
	folder, tools := t.TempDir(), t.TempDir()
	m, _, tenant, _ := newRuntimeHarness(t)
	open := func(params CreateWorkspaceParams) *resolvedWorkspace {
		t.Helper()
		params.RootPath = folder
		ws, err := m.createWorkspace(t.Context(), tenant, params)
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := m.resolveLaunchWorkspace(t.Context(), tenant, ws.WorkspaceRef)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(resolved.closeReadOnlyHandles)
		return resolved
	}
	spec := func(p CreateRunParams, ws *resolvedWorkspace) LaunchSpec {
		p.ProviderHome = &ProviderHomeSnapshot{Driver: providerDriverOpenCode}
		if p.Isolation == "" {
			p.Isolation = IsolationNative
		}
		return m.childSpec(p, childDecision{ws: ws})
	}

	rw := open(CreateWorkspaceParams{ReadOnlyFolders: []string{tools}, MountMode: mountRW})
	got := spec(CreateRunParams{}, rw).files
	if got == nil || got.root != rw.rootReal {
		t.Fatalf("a writable native session got scope %+v, want one rooted at %s", got, rw.rootReal)
	}
	if !slices.Contains(got.denied, tools) {
		t.Fatalf("the scope does not protect the workspace's read-only folder: %v", got.denied)
	}
	if got := spec(CreateRunParams{PermissionMode: "plan"}, rw).files; got != nil {
		t.Fatalf("a read-only session got scope %+v", got)
	}
	if got := spec(CreateRunParams{Isolation: IsolationContainer}, rw).files; got != nil {
		t.Fatalf("a container session got scope %+v", got)
	}
	if got := spec(CreateRunParams{}, open(CreateWorkspaceParams{MountMode: mountRO})).files; got != nil {
		t.Fatalf("a read-only workspace got scope %+v", got)
	}
}

func TestFileScopeTakesBackWhatARefusedWriteMade(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".git", "hooks", "pre-commit"), "hook")
	if err := os.Mkdir(filepath.Join(dir, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	scope := newFileScope(dir, false, PresetAsk, nil, nil, confine.Mounts{ReadOnly: []string{filepath.Join(dir, ".git", "hooks")}})
	scope.afterJudged = func() { // the judged folder becomes a link into the hooks
		_ = os.Remove(filepath.Join(dir, "plain"))
		_ = os.Symlink(filepath.Join(".git", "hooks"), filepath.Join(dir, "plain"))
	}
	var refusal fileRefusal
	if err := scope.writeFile(filepath.Join(dir, "plain", "d1", "d2", "new.txt"), "pwned", nil); !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, ".git", "hooks"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "pre-commit" {
		t.Fatalf("the hooks folder holds %v after a refused write, want only pre-commit", entries)
	}
}

func TestFileScopeStopsAWriteTheLiveCheckRefusesBeforeItMakesAnything(t *testing.T) {
	dir := t.TempDir()
	scope := newFileScope(dir, false, PresetAsk, nil, nil, confine.Mounts{})
	if err := scope.writeFile(filepath.Join(dir, "sub", "new.txt"), "x", func() error { return errors.New("stop here") }); err == nil {
		t.Fatal("the live check did not stop the write")
	}
	for _, name := range []string{"sub", "sub/new.txt"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s exists after a write the live check stopped (%v)", name, err)
		}
	}
}

func TestFileScopeAnswersTheFilesystemsOwnNoAsARefusal(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a-file"), "x")
	if err := os.Mkdir(filepath.Join(dir, "a-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	scope := newFileScope(dir, false, PresetAsk, nil, nil, confine.Mounts{})
	for name, path := range map[string]string{
		"a folder named as the file": filepath.Join(dir, "a-dir"),
		"a file named as a folder":   filepath.Join(dir, "a-file", "inside.txt"),
	} {
		var refusal fileRefusal
		if err := scope.writeFile(path, "x", nil); !errors.As(err, &refusal) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
	if got := mustRead(t, filepath.Join(dir, "a-file")); got != "x" {
		t.Fatalf("a-file = %q", got)
	}
}

func TestFileScopeQueuesAFewWritesAndRefusesTheRest(t *testing.T) {
	dir := t.TempDir()
	scope := newFileScope(dir, false, PresetAsk, nil, nil, confine.Mounts{})
	for i := 0; i < cap(scope.queue); i++ {
		scope.queue <- struct{}{}
	}
	if err := scope.writeFile(filepath.Join(dir, "x.txt"), "x", nil); !errors.Is(err, errFileBusy) {
		t.Fatalf("err = %v, want busy", err)
	}
	<-scope.queue
	if err := scope.writeFile(filepath.Join(dir, "x.txt"), "x", nil); err != nil {
		t.Fatalf("with a free place: %v", err)
	}
}
