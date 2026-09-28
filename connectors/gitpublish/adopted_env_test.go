// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

// Closed-environment tests adopted from the construction read.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Every hostile repo-local key named in the brief refuses the push.
func TestAdoptedHostileRepoLocalKeysRefused(t *testing.T) {
	keys := [][2]string{
		{"core.sshCommand", "/bin/false"}, {"core.fsmonitor", "/bin/false"}, {"protocol.allow", "always"},
		{"protocol.file.allow", "always"}, {"http.proxy", "http://127.0.0.1:9"}, {"url.file:///x.insteadOf", "file:///y"},
		{"includeIf.gitdir:/.path", "/etc/passwd"}, {"remote.origin.url", "file:///x"}, {"core.gitProxy", "/bin/false"},
		{"http.sslVerify", "false"}, {"extensions.worktreeConfig", "true"}, {"credential.https://h.helper", "store"},
		{"core.alternateRefsCommand", "/bin/false"}, {"http.https://h.extraHeader", "X: y"}, {"CORE.HooksPath", "/x"},
	}
	for _, kv := range keys {
		f := newGitFixture(t)
		run(t, f.server, "config", kv[0], kv[1])
		if _, err := f.x.Push(context.Background(), f.req("refs/heads/main", f.base)); !errors.Is(err, ErrRepositoryConfig) {
			t.Errorf("%s=%s: err = %v, want ErrRepositoryConfig", kv[0], kv[1], err)
		}
		if f.refAt(f.target, "refs/heads/main") != f.base {
			t.Errorf("%s: the refused push moved the target", kv[0])
		}
	}
}

// No repository hook of any name runs during a push.
func TestAdoptedNoRepositoryHookRuns(t *testing.T) {
	f := newGitFixture(t)
	marks := t.TempDir()
	for _, name := range []string{"pre-push", "reference-transaction", "post-update", "push-to-checkout", "fsmonitor-watchman", "post-index-change"} {
		p := filepath.Join(f.server, "hooks", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\ntouch "+filepath.Join(marks, name)+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if res, err := f.x.Push(context.Background(), f.req("refs/heads/main", f.base)); err != nil || res.Class != Applied {
		t.Fatalf("push = %+v %v", res, err)
	}
	ents, _ := os.ReadDir(marks)
	for _, e := range ents {
		t.Errorf("hook ran: %s", e.Name())
	}
}
