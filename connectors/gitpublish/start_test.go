// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
)

// The composition root decides how git starts (on the Olivares Server, in its own SELinux domain, the regression): every git
// this executor runs goes through that start, and a start that refuses runs nothing.
func TestEveryGitTheExecutorRunsGoesThroughItsStart(t *testing.T) {
	x, err := NewExecutor(gitPath(t), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var started []string
	x.StartWith(func(cmd *exec.Cmd) error {
		started = append(started, filepath.Base(cmd.Path))
		return cmd.Start()
	})
	repo := filepath.Join(t.TempDir(), "r.git")
	if err := x.InitManaged(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	if len(started) == 0 {
		t.Fatal("InitManaged ran git without the executor's start")
	}
	for _, name := range started {
		if name != "git" {
			t.Errorf("started %q, want git", name)
		}
	}

	// InitManaged maps every git failure to its own error; what matters is that a refused start fails it.
	x.StartWith(func(*exec.Cmd) error { return errors.New("refused") })
	if err := x.InitManaged(context.Background(), filepath.Join(t.TempDir(), "s.git")); err == nil {
		t.Fatal("InitManaged succeeded although its start refused git")
	}
}

// A start that refuses means git never ran: the push reports that the transport did not start, never an ambiguous
// dispatch (the regression security review).
func TestARefusedStartIsATransportThatDidNotStart(t *testing.T) {
	x, err := NewExecutor(gitPath(t), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	x.StartWith(func(*exec.Cmd) error { return errors.New("cannot enter the domain") })
	if err := x.run1(exec.Command(x.git, "--version")); !errors.Is(err, ErrTransportStart) {
		t.Fatalf("run1 with a refusing start = %v, want ErrTransportStart", err)
	}
}
